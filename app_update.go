package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"
)

const (
	defaultAppUpdateRepo   = "santian666/SanTianShell"
	defaultGitHubAPIBase   = "https://api.github.com"
	appUpdateCheckTimeout  = 15 * time.Second
	appUpdateDownloadLimit = 300 * 1024 * 1024
)

type AppUpdateInfo struct {
	Available      bool   `json:"available"`
	CurrentVersion string `json:"currentVersion"`
	LatestVersion  string `json:"latestVersion"`
	ReleaseNotes   string `json:"releaseNotes"`
	DownloadURL    string `json:"downloadUrl"`
	AssetName      string `json:"assetName,omitempty"`
	AssetSize      int64  `json:"assetSize"`
	PublishedAt    string `json:"publishedAt"`
	Status         string `json:"status"` // "update_available", "already_latest", "check_failed"
	Message        string `json:"message,omitempty"`
}

type AppUpdateProgress struct {
	Phase       string  `json:"phase"` // "downloading", "extracting", "applying", "ready"
	Percent     float64 `json:"percent"`
	Bytes       int64   `json:"bytes"`
	Total       int64   `json:"total"`
	SpeedString string  `json:"speedString"`
	Message     string  `json:"message"`
}

type AppUpdateManager struct {
	mu          sync.Mutex
	repo        string
	apiBaseURL  string
	lastInfo    *AppUpdateInfo
	lastCheckAt time.Time
	isUpdating  bool
}

func newAppUpdateManager() *AppUpdateManager {
	return &AppUpdateManager{
		repo:       defaultAppUpdateRepo,
		apiBaseURL: defaultGitHubAPIBase,
	}
}

func newUpdateHTTPClient(timeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = http.ProxyFromEnvironment
	return &http.Client{Timeout: timeout, Transport: transport}
}

func compareSemver(v1, v2 string) int {
	clean := func(v string) []string {
		v = strings.TrimPrefix(strings.TrimSpace(v), "v")
		if idx := strings.IndexAny(v, "-+"); idx >= 0 {
			v = v[:idx]
		}
		return strings.Split(v, ".")
	}
	parts1 := clean(v1)
	parts2 := clean(v2)
	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}
	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			n1, _ = strconv.Atoi(parts1[i])
		}
		if i < len(parts2) {
			n2, _ = strconv.Atoi(parts2[i])
		}
		if n1 > n2 {
			return 1
		}
		if n1 < n2 {
			return -1
		}
	}
	return 0
}

type githubReleaseAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

type githubLatestRelease struct {
	TagName     string               `json:"tag_name"`
	Name        string               `json:"name"`
	Body        string               `json:"body"`
	PublishedAt string               `json:"published_at"`
	Assets      []githubReleaseAsset `json:"assets"`
}

func selectUpdateZipAsset(assets []githubReleaseAsset) (githubReleaseAsset, bool) {
	for _, asset := range assets {
		lower := strings.ToLower(asset.Name)
		if strings.HasSuffix(lower, ".zip") && strings.Contains(lower, "santianshell") {
			return asset, true
		}
	}
	for _, asset := range assets {
		if strings.HasSuffix(strings.ToLower(asset.Name), ".zip") {
			return asset, true
		}
	}
	return githubReleaseAsset{}, false
}

func (m *AppUpdateManager) CheckUpdate() (*AppUpdateInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	base := strings.TrimRight(m.apiBaseURL, "/")
	if base == "" {
		base = defaultGitHubAPIBase
	}
	apiURL := fmt.Sprintf("%s/repos/%s/releases/latest", base, m.repo)
	req, err := http.NewRequest(http.MethodGet, apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "SanTianShell/"+appVersion)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := newUpdateHTTPClient(appUpdateCheckTimeout)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("检查更新失败，无法连接 GitHub：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.New("未找到已发布的 Release（仓库尚未发布版本）")
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API 返回异常状态码 %d", resp.StatusCode)
	}

	var release githubLatestRelease
	if err := json.NewDecoder(io.LimitReader(resp.Body, 2*1024*1024)).Decode(&release); err != nil {
		return nil, fmt.Errorf("解析更新信息失败：%w", err)
	}

	latestVer := strings.TrimPrefix(strings.TrimSpace(release.TagName), "v")
	curVer := strings.TrimPrefix(strings.TrimSpace(appVersion), "v")
	asset, hasZip := selectUpdateZipAsset(release.Assets)
	isNewer := latestVer != "" && compareSemver(latestVer, curVer) > 0 && hasZip && asset.BrowserDownloadURL != ""

	info := &AppUpdateInfo{
		Available:      isNewer,
		CurrentVersion: appVersion,
		LatestVersion:  release.TagName,
		ReleaseNotes:   release.Body,
		DownloadURL:    asset.BrowserDownloadURL,
		AssetName:      asset.Name,
		AssetSize:      asset.Size,
		PublishedAt:    release.PublishedAt,
	}
	if isNewer {
		info.Status = "update_available"
	} else {
		info.Status = "already_latest"
	}

	m.lastInfo = info
	m.lastCheckAt = time.Now()
	return info, nil
}

type updateProgressWriter struct {
	total      int64
	downloaded int64
	lastEmit   time.Time
	lastBytes  int64
	onProgress func(downloaded, total int64, speed string, percent float64)
}

func (pw *updateProgressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.downloaded += int64(n)

	now := time.Now()
	if now.Sub(pw.lastEmit) >= 200*time.Millisecond || (pw.total > 0 && pw.downloaded >= pw.total) {
		elapsed := now.Sub(pw.lastEmit).Seconds()
		var speedStr string
		if elapsed > 0 {
			speedBytes := float64(pw.downloaded-pw.lastBytes) / elapsed
			if speedBytes >= 1024*1024 {
				speedStr = fmt.Sprintf("%.2f MB/s", speedBytes/(1024*1024))
			} else {
				speedStr = fmt.Sprintf("%.1f KB/s", speedBytes/1024)
			}
		}
		pw.lastEmit = now
		pw.lastBytes = pw.downloaded

		var percent float64
		if pw.total > 0 {
			percent = float64(pw.downloaded) / float64(pw.total) * 100
		}
		if pw.onProgress != nil {
			pw.onProgress(pw.downloaded, pw.total, speedStr, percent)
		}
	}
	return n, nil
}

func isValidUpdateZip(zipPath string, expectedSize int64) bool {
	fi, err := os.Stat(zipPath)
	if err != nil || fi.Size() == 0 {
		return false
	}
	if expectedSize > 0 && fi.Size() != expectedSize {
		return false
	}
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return false
	}
	defer reader.Close()

	for _, file := range reader.File {
		normalizedName := strings.ReplaceAll(file.Name, "\\", "/")
		base := filepath.Base(normalizedName)
		if strings.EqualFold(base, "SanTianShell.exe") {
			return true
		}
	}
	return false
}

func downloadUpdateAsset(downloadURL, destPath string, expectedSize int64, onProgress func(downloaded, total int64, speed string, percent float64)) error {
	var existingSize int64
	if fi, err := os.Stat(destPath); err == nil && fi.Size() > 0 {
		if expectedSize > 0 && fi.Size() < expectedSize {
			existingSize = fi.Size()
		} else if expectedSize > 0 && fi.Size() > expectedSize {
			_ = os.Remove(destPath)
		}
	}

	req, err := http.NewRequest(http.MethodGet, downloadURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "SanTianShell/"+appVersion)
	if existingSize > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", existingSize))
	}

	client := newUpdateHTTPClient(10 * time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("下载更新包失败：%w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
		_ = os.Remove(destPath)
		return downloadUpdateAsset(downloadURL, destPath, expectedSize, onProgress)
	}

	var out *os.File
	var total int64
	var downloadedSoFar int64

	if resp.StatusCode == http.StatusPartialContent && existingSize > 0 {
		out, err = os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		total = existingSize + resp.ContentLength
		downloadedSoFar = existingSize
	} else if resp.StatusCode == http.StatusOK {
		out, err = os.OpenFile(destPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		total = resp.ContentLength
		downloadedSoFar = 0
	} else {
		return fmt.Errorf("下载服务器返回状态码 %d", resp.StatusCode)
	}
	defer out.Close()

	if total > appUpdateDownloadLimit {
		return errors.New("更新文件大小超过安全限制")
	}

	pw := &updateProgressWriter{
		total:      total,
		downloaded: downloadedSoFar,
		lastEmit:   time.Now(),
		lastBytes:  downloadedSoFar,
		onProgress: onProgress,
	}
	_, err = io.Copy(out, io.TeeReader(io.LimitReader(resp.Body, appUpdateDownloadLimit), pw))
	return err
}

func extractZipArchive(zipPath, destDir string) error {
	reader, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer reader.Close()

	cleanDest := filepath.Clean(destDir)
	for _, file := range reader.File {
		normalizedName := strings.ReplaceAll(file.Name, "\\", "/")
		isDir := file.FileInfo().IsDir() || strings.HasSuffix(normalizedName, "/")

		path := filepath.Join(cleanDest, filepath.FromSlash(normalizedName))
		if path != cleanDest && !strings.HasPrefix(filepath.Clean(path), cleanDest+string(os.PathSeparator)) {
			continue
		}
		if isDir {
			_ = os.MkdirAll(path, 0o755)
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		src, err := file.Open()
		if err != nil {
			return err
		}
		dst, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, file.Mode())
		if err != nil {
			_ = src.Close()
			return err
		}
		_, copyErr := io.Copy(dst, src)
		_ = src.Close()
		_ = dst.Close()
		if copyErr != nil {
			return copyErr
		}
	}
	return nil
}

func locateExtractedAppDir(stageDir, exeName string) string {
	if _, err := os.Stat(filepath.Join(stageDir, exeName)); err == nil {
		return stageDir
	}
	entries, err := os.ReadDir(stageDir)
	if err == nil {
		for _, e := range entries {
			if e.IsDir() {
				subPath := filepath.Join(stageDir, e.Name())
				if _, err := os.Stat(filepath.Join(subPath, exeName)); err == nil {
					return subPath
				}
			}
		}
	}
	return stageDir
}

func copyUpdateFile(srcPath, dstPath string) error {
	src, err := os.Open(srcPath)
	if err != nil {
		return err
	}
	defer src.Close()

	dst, err := os.OpenFile(dstPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o755)
	if err != nil {
		return err
	}
	defer dst.Close()

	if _, err := io.Copy(dst, src); err != nil {
		return err
	}
	return dst.Sync()
}

func performAtomicInPlaceUpdate(extractedAppDir, exePath string) error {
	exeName := filepath.Base(exePath)
	oldExe := exePath + ".old"
	_ = os.Remove(oldExe)

	var renameErr error
	for i := 0; i < 5; i++ {
		renameErr = os.Rename(exePath, oldExe)
		if renameErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if renameErr != nil {
		return fmt.Errorf("重命名旧主程序失败（可能被安全软件或系统占用）：%w", renameErr)
	}

	newExeSrc := filepath.Join(extractedAppDir, exeName)
	if err := copyUpdateFile(newExeSrc, exePath); err != nil {
		_ = os.Rename(oldExe, exePath)
		return fmt.Errorf("复制新主程序失败：%w", err)
	}
	return nil
}

func launchNewAppProcess(exePath, workDir string) error {
	cmd := exec.Command(exePath)
	cmd.Dir = workDir
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP,
	}
	return cmd.Start()
}

func updateStagingRoot() string {
	return filepath.Join(appDataPath(), "updates")
}

func cleanupOldUpdateArtifacts() {
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		_ = os.Remove(exePath + ".old")
	}

	updatesDir := updateStagingRoot()
	targetVersionPath := filepath.Join(updatesDir, "target_version.txt")
	if targetVerBytes, err := os.ReadFile(targetVersionPath); err == nil {
		targetVer := strings.TrimSpace(string(targetVerBytes))
		if targetVer != "" {
			if compareSemver(appVersion, targetVer) >= 0 {
				_ = os.RemoveAll(updatesDir)
				return
			}
			_ = os.RemoveAll(filepath.Join(updatesDir, "staging"))
			return
		}
	}
	_ = os.RemoveAll(filepath.Join(updatesDir, "staging"))
}

func (a *App) emitUpdateProgress(progress AppUpdateProgress) {
	if a.ctx != nil {
		runtime.EventsEmit(a.ctx, "app-update-progress", progress)
	}
}

func (a *App) CheckAppUpdate() (*AppUpdateInfo, error) {
	if a.updateManager == nil {
		a.updateManager = newAppUpdateManager()
	}
	return a.updateManager.CheckUpdate()
}

func (a *App) ApplyAppUpdate() error {
	if a.updateManager == nil {
		a.updateManager = newAppUpdateManager()
	}

	a.updateManager.mu.Lock()
	if a.updateManager.isUpdating {
		a.updateManager.mu.Unlock()
		return errors.New("正在更新中，请勿重复操作")
	}
	a.updateManager.isUpdating = true
	info := a.updateManager.lastInfo
	a.updateManager.mu.Unlock()

	defer func() {
		a.updateManager.mu.Lock()
		a.updateManager.isUpdating = false
		a.updateManager.mu.Unlock()
	}()

	if info == nil || !info.Available || info.DownloadURL == "" {
		var err error
		info, err = a.CheckAppUpdate()
		if err != nil {
			return err
		}
		if info == nil || !info.Available || info.DownloadURL == "" {
			return errors.New("当前已是最新版本或未找到可用安装包")
		}
	}

	updatesDir := updateStagingRoot()
	if err := os.MkdirAll(updatesDir, 0o755); err != nil {
		return fmt.Errorf("创建更新缓存目录失败：%w", err)
	}

	zipPath := filepath.Join(updatesDir, "update.zip")
	targetVerFile := filepath.Join(updatesDir, "target_version.txt")
	if prevBytes, err := os.ReadFile(targetVerFile); err == nil {
		if strings.TrimSpace(string(prevBytes)) != strings.TrimSpace(info.LatestVersion) {
			_ = os.Remove(zipPath)
		}
	}
	_ = os.WriteFile(targetVerFile, []byte(info.LatestVersion), 0o644)

	if isValidUpdateZip(zipPath, info.AssetSize) {
		a.emitUpdateProgress(AppUpdateProgress{
			Phase:   "downloading",
			Percent: 100,
			Bytes:   info.AssetSize,
			Total:   info.AssetSize,
			Message: "检测到已下载的更新包，校验通过，直接准备更新",
		})
	} else {
		err := downloadUpdateAsset(info.DownloadURL, zipPath, info.AssetSize, func(downloaded, total int64, speed string, percent float64) {
			a.emitUpdateProgress(AppUpdateProgress{
				Phase:       "downloading",
				Percent:     percent,
				Bytes:       downloaded,
				Total:       total,
				SpeedString: speed,
				Message:     "正在下载更新包...",
			})
		})
		if err != nil {
			return err
		}
		if !isValidUpdateZip(zipPath, info.AssetSize) {
			_ = os.Remove(zipPath)
			return errors.New("更新包校验失败（未找到主程序或文件损坏），请重试")
		}
	}

	a.emitUpdateProgress(AppUpdateProgress{
		Phase:   "extracting",
		Percent: 100,
		Message: "下载完成，正在解压更新包...",
	})

	stageDir := filepath.Join(updatesDir, "staging")
	_ = os.RemoveAll(stageDir)
	if err := os.MkdirAll(stageDir, 0o755); err != nil {
		return fmt.Errorf("创建解压目录失败：%w", err)
	}
	if err := extractZipArchive(zipPath, stageDir); err != nil {
		return fmt.Errorf("解压更新包失败：%w", err)
	}

	exePath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("无法定位当前主程序路径：%w", err)
	}
	exePath, _ = filepath.Abs(exePath)
	appRoot := filepath.Dir(exePath)
	extractedAppDir := locateExtractedAppDir(stageDir, filepath.Base(exePath))

	if _, err := os.Stat(filepath.Join(extractedAppDir, filepath.Base(exePath))); err != nil {
		return fmt.Errorf("更新包中未找到主程序 %s", filepath.Base(exePath))
	}

	a.emitUpdateProgress(AppUpdateProgress{
		Phase:   "applying",
		Percent: 100,
		Message: "正在替换主程序并准备重启...",
	})

	if err := performAtomicInPlaceUpdate(extractedAppDir, exePath); err != nil {
		return err
	}

	a.emitUpdateProgress(AppUpdateProgress{
		Phase:   "ready",
		Percent: 100,
		Message: "更新成功，正在重启 SanTianShell...",
	})

	if err := launchNewAppProcess(exePath, appRoot); err != nil {
		return fmt.Errorf("已替换新版本，但自动重启失败：%w（请手动重新打开软件）", err)
	}

	go func() {
		time.Sleep(450 * time.Millisecond)
		if a.ctx != nil {
			runtime.Quit(a.ctx)
		}
	}()
	return nil
}
