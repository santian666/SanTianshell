package nodemode

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"

	"santianshell/internal/nodemode/core"

	"github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

type App struct {
	service              *core.Service
	ctx                  context.Context
	dataDirectoryWarning string
	accessGuardMu        sync.RWMutex
	accessGuard          func() error
	proxyCancelMu        sync.Mutex
	proxyCancel          context.CancelFunc
}

func New(connector, interactiveConnector func(string, string, int) (*ssh.Client, func(), error), passwordResolver ...func(string, string) string) *App {
	wrap := func(connect func(string, string, int) (*ssh.Client, func(), error)) core.SSHConnector {
		return func(vps core.VPS, timeoutSeconds int) (*ssh.Client, func(), error) {
			alias := strings.TrimSpace(vps.Alias)
			if alias == "" {
				alias = strings.TrimSpace(vps.ID)
			}
			return connect(strings.TrimSpace(vps.HostID), alias, timeoutSeconds)
		}
	}
	directory, warning := resolveDataDirectory(defaultDataDirectoryOptions())
	var resolver core.PasswordResolver
	if len(passwordResolver) > 0 && passwordResolver[0] != nil {
		resolver = passwordResolver[0]
	}
	service := core.NewServiceWithConnectorsAndResolver(directory, wrap(connector), wrap(interactiveConnector), resolver)
	service.EnablePersistentDB()
	return &App{service: service, dataDirectoryWarning: warning}
}

// Close releases persistent resources such as the SQLite database connection on shutdown.
func Close(a *App) error {
	if a == nil || a.service == nil {
		return nil
	}
	return a.service.Close()
}

// SetAccessGuard configures a lock check invoked before Wails-bound node operations.
func SetAccessGuard(a *App, guard func() error) {
	if a == nil {
		return
	}
	a.accessGuardMu.Lock()
	a.accessGuard = guard
	a.accessGuardMu.Unlock()
}

// Startup initializes the Wails runtime context and background schedulers without exposing a Wails RPC method.
func Startup(a *App, ctx context.Context) {
	if a == nil {
		return
	}
	a.ctx = ctx
	if a.dataDirectoryWarning != "" {
		if strings.Contains(a.dataDirectoryWarning, "无法创建任何可写") {
			runtime.LogError(ctx, a.dataDirectoryWarning)
		} else {
			runtime.LogWarning(ctx, a.dataDirectoryWarning)
		}
	}
	if a.service != nil {
		a.service.StartAutoDeleteScheduler(ctx)
	}
}

// ForgetHostLocalData removes local node-mode cache/backup state for a deleted or modified host.
func ForgetHostLocalData(a *App, hostID, alias string) error {
	if a == nil || a.service == nil {
		return nil
	}
	return a.service.ForgetHostLocalData(hostID, alias)
}

// ReconcileHostLocalData prunes orphaned local node-mode state at startup against active host IDs and aliases.
func ReconcileHostLocalData(a *App, hostIDs, aliases []string) error {
	if a == nil || a.service == nil {
		return nil
	}
	return a.service.ReconcileHostLocalData(hostIDs, aliases)
}

func (a *App) requireAccess() error {
	if a == nil || a.service == nil {
		return errors.New("节点模式服务未初始化")
	}
	a.accessGuardMu.RLock()
	guard := a.accessGuard
	a.accessGuardMu.RUnlock()
	if guard != nil {
		return guard()
	}
	return nil
}

func dataDirectory() string {
	directory, _ := resolveDataDirectory(defaultDataDirectoryOptions())
	return directory
}

type dataDirectoryOptions struct {
	production          bool
	workingDirectory    string
	workingDirectoryErr string
	userConfigDirectory string
	userConfigErr       string
	userHomeDirectory   string
	userHomeErr         string
	tempDirectory       string
}

type dataDirectoryCandidate struct {
	name string
	path string
}

func defaultDataDirectoryOptions() dataDirectoryOptions {
	options := dataDirectoryOptions{production: isProductionBuild(), tempDirectory: os.TempDir()}
	var err error
	options.workingDirectory, err = os.Getwd()
	if err != nil {
		options.workingDirectoryErr = err.Error()
	}
	options.userConfigDirectory, err = os.UserConfigDir()
	if err != nil {
		options.userConfigErr = err.Error()
	}
	options.userHomeDirectory, err = os.UserHomeDir()
	if err != nil {
		options.userHomeErr = err.Error()
	}
	return options
}

func isProductionBuild() bool {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return false
	}
	for _, setting := range info.Settings {
		if setting.Key != "-tags" {
			continue
		}
		for _, tag := range strings.FieldsFunc(setting.Value, func(value rune) bool {
			return value == ',' || value == ' ' || value == '\t'
		}) {
			if tag == "production" {
				return true
			}
		}
	}
	return false
}

func resolveDataDirectory(options dataDirectoryOptions) (string, string) {
	diagnostics := make([]string, 0, 4)
	candidates := make([]dataDirectoryCandidate, 0, 4)
	if !options.production {
		if strings.TrimSpace(options.workingDirectory) == "" {
			diagnostics = append(diagnostics, "无法获取项目目录: "+options.workingDirectoryErr)
		} else {
			candidates = append(candidates, dataDirectoryCandidate{"项目数据目录", filepath.Join(options.workingDirectory, "data", "node-mode")})
		}
	}
	if strings.TrimSpace(options.userConfigDirectory) == "" {
		if options.userConfigErr != "" {
			diagnostics = append(diagnostics, "无法获取用户配置目录: "+options.userConfigErr)
		}
	} else {
		candidates = append(candidates, dataDirectoryCandidate{"用户配置目录", filepath.Join(options.userConfigDirectory, "SanTianShell", "node-mode")})
	}
	if strings.TrimSpace(options.userHomeDirectory) == "" {
		if options.userHomeErr != "" {
			diagnostics = append(diagnostics, "无法获取用户主目录: "+options.userHomeErr)
		}
	} else {
		candidates = append(candidates, dataDirectoryCandidate{"用户主目录", filepath.Join(options.userHomeDirectory, ".santianshell", "node-mode")})
	}
	if strings.TrimSpace(options.tempDirectory) != "" {
		candidates = append(candidates, dataDirectoryCandidate{"用户临时目录", filepath.Join(options.tempDirectory, "SanTianShell", "node-mode")})
	}

	seen := make(map[string]bool, len(candidates))
	for index, candidate := range candidates {
		absolutePath, err := filepath.Abs(candidate.path)
		if err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s路径无效: %v", candidate.name, err))
			continue
		}
		key := strings.ToLower(filepath.Clean(absolutePath))
		if seen[key] {
			continue
		}
		seen[key] = true
		if err = ensureWritableDirectory(absolutePath); err != nil {
			diagnostics = append(diagnostics, fmt.Sprintf("%s不可写（%s）: %v", candidate.name, absolutePath, err))
			continue
		}

		if index > 0 || len(diagnostics) > 0 {
			diagnostics = append(diagnostics, "节点数据已使用可写目录: "+absolutePath)
		}
		return absolutePath, strings.Join(diagnostics, "；")
	}

	emergencyDirectory, err := os.MkdirTemp(options.tempDirectory, "SanTianShell-node-mode-")
	if err == nil {
		diagnostics = append(diagnostics, "固定数据目录均不可写，已临时使用 "+emergencyDirectory+"；请检查当前 Windows 用户的目录权限，临时目录可能被系统清理")
		return emergencyDirectory, strings.Join(diagnostics, "；")
	}
	diagnostics = append(diagnostics, "无法创建任何可写的节点数据目录: "+err.Error()+"；请检查当前 Windows 用户的 AppData、用户主目录和临时目录权限")
	return filepath.Join(options.tempDirectory, "SanTianShell", "node-mode"), strings.Join(diagnostics, "；")
}

func ensureWritableDirectory(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	probe, err := os.CreateTemp(path, ".write-test-*")
	if err != nil {
		return err
	}
	probePath := probe.Name()
	if err = probe.Close(); err != nil {
		_ = os.Remove(probePath)
		return err
	}
	if err = os.Remove(probePath); err != nil {
		return err
	}
	return nil
}

func sameDirectory(left, right string) bool {
	leftPath, leftErr := filepath.Abs(left)
	rightPath, rightErr := filepath.Abs(right)
	if leftErr != nil || rightErr != nil {
		return false
	}
	return strings.EqualFold(filepath.Clean(leftPath), filepath.Clean(rightPath))
}

func (a *App) TestProxyNodes(req core.ProxyDetectRequest) ([]core.ProxyDetectResult, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.proxyCancelMu.Lock()
	if a.proxyCancel != nil {
		a.proxyCancel()
	}
	a.proxyCancel = cancel
	a.proxyCancelMu.Unlock()
	defer func() {
		a.proxyCancelMu.Lock()
		a.proxyCancel = nil
		a.proxyCancelMu.Unlock()
	}()

	return a.service.TestProxyNodesWithProgress(ctx, req, func(index int, total int, item core.ProxyDetectResult) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "proxy:item", item)
			runtime.EventsEmit(a.ctx, "proxy:progress", map[string]any{
				"index": index + 1,
				"total": total,
			})
		}
	})
}

func (a *App) CancelProxyDetect() {
	if a.requireAccess() != nil {
		return
	}
	a.proxyCancelMu.Lock()
	defer a.proxyCancelMu.Unlock()
	if a.proxyCancel != nil {
		a.proxyCancel()
		a.proxyCancel = nil
	}
}

func (a *App) RunMiddleAcceleration(req core.MiddleAccelerationRequest) (core.MiddleAccelerationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.MiddleAccelerationResult{}, err
	}
	return a.service.RunMiddleAccelerationWithLogger(req, func(line string) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "middle:log", line)
		}
	})
}

func (a *App) RunMiddleAccelerationExcel(req core.MiddleAccelerationExcelRequest) (core.MiddleAccelerationExcelResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.MiddleAccelerationExcelResult{}, err
	}
	return a.service.RunMiddleAccelerationExcel(req, func(line string) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "middle:log", line)
		}
	})
}

func (a *App) RunMiddleAccelerationExcelToFile(req core.MiddleAccelerationExcelRequest) (core.MiddleAccelerationExcelSavedResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.MiddleAccelerationExcelSavedResult{}, err
	}
	result, err := a.service.RunMiddleAccelerationExcelBytes(req, func(line string) {
		if a.ctx != nil {
			runtime.EventsEmit(a.ctx, "middle:log", line)
		}
	})
	saved := core.MiddleAccelerationExcelSavedResult{Success: result.Success, Message: result.Message, Logs: result.Logs, Output: result.Output, FileName: result.File.FileName}
	if err != nil {
		return saved, err
	}
	path, err := a.saveExportBytes(result.File.FileName, result.File.Data)
	if err != nil {
		if strings.Contains(err.Error(), "取消") {
			saved.Message += "，已取消导出"
			return saved, nil
		}
		return saved, err
	}
	saved.FilePath = path
	return saved, nil
}

func (a *App) ExportNodeExcelToFile(alias, filename, mode string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	fileName, err := a.service.ExportNodeExcelFileName(alias, filename, mode)
	if err != nil {
		return "", err
	}
	path, err := a.chooseExcelSavePath(fileName)
	if err != nil {
		return "", err
	}
	file, err := a.service.ExportNodeExcelBytes(alias, filename, mode)
	if err != nil {
		return "", err
	}
	return writeExportBytes(path, file.Data)
}

func (a *App) SaveExportFile(file core.ExportFile) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	if strings.TrimSpace(file.Base64) == "" {
		return "", errors.New("没有可保存的文件")
	}
	path, err := a.chooseExcelSavePath(file.FileName)
	if err != nil {
		return "", err
	}
	data, err := base64.StdEncoding.DecodeString(file.Base64)
	if err != nil {
		return "", errors.New("文件数据解码失败")
	}
	return writeExportBytes(path, data)
}

func (a *App) ExportSelectedNodesExcel(req core.ExportSelectedNodesRequest) (core.ExportFile, error) {
	if err := a.requireAccess(); err != nil {
		return core.ExportFile{}, err
	}
	return a.service.ExportSelectedNodesExcel(req)
}

func (a *App) ExportSelectedNodesExcelToFile(req core.ExportSelectedNodesRequest) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	file, err := a.service.ExportSelectedNodesExcelBytes(req)
	if err != nil {
		return "", err
	}
	path, err := a.chooseExcelSavePath(file.FileName)
	if err != nil {
		return "", err
	}
	return writeExportBytes(path, file.Data)
}

func (a *App) ForceDeleteNodes(alias string, filenames []string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.ForceDeleteNodes(alias, filenames)
}

// ExportAllJSONToFile 根据本地保存的节点配置重新生成可迁移的 sing-box all.json。
// 该操作只读取本地节点数据，不连接或修改远程 VPS。
func (a *App) ExportAllJSONToFile(alias string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return "", errors.New("请先选择 VPS")
	}
	content, err := a.service.ReadAllJSON(alias)
	if err != nil {
		return "", err
	}
	data := []byte(strings.TrimSpace(content))
	if err := validateExportAllJSON(data); err != nil {
		return "", err
	}
	data = append(data, '\n')

	fileName := safeJSONExportName(alias) + "_all.json"
	path, err := a.chooseJSONSavePath(fileName)
	if err != nil {
		return "", err
	}
	return writeJSONExportBytes(path, data)
}

// BackupAllJSONFromNodes 根据本地节点列表生成标准 all.json，并自动保存到备份列表。
func (a *App) BackupAllJSONFromNodes(alias string) (core.BackupFile, error) {
	if err := a.requireAccess(); err != nil {
		return core.BackupFile{}, err
	}
	return a.service.BackupAllJSON(alias)
}

func (a *App) saveExportBytes(fileName string, data []byte) (string, error) {
	if len(data) == 0 {
		return "", errors.New("没有可保存的文件")
	}
	path, err := a.chooseExcelSavePath(fileName)
	if err != nil {
		return "", err
	}
	return writeExportBytes(path, data)
}

func (a *App) chooseExcelSavePath(fileName string) (string, error) {
	if a.ctx == nil {
		return "", errors.New("应用窗口尚未初始化")
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = "export.xlsx"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "导出 Excel", DefaultDirectory: downloadDirectory(), DefaultFilename: fileName, Filters: []runtime.FileFilter{{DisplayName: "Excel 文件 (*.xlsx)", Pattern: "*.xlsx"}}, CanCreateDirectories: true})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("已取消导出")
	}
	return path, nil
}

func (a *App) chooseJSONSavePath(fileName string) (string, error) {
	if a.ctx == nil {
		return "", errors.New("应用窗口尚未初始化")
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		fileName = "all.json"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:                "保存 all.json",
		DefaultDirectory:     downloadDirectory(),
		DefaultFilename:      fileName,
		Filters:              []runtime.FileFilter{{DisplayName: "JSON 文件 (*.json)", Pattern: "*.json"}},
		CanCreateDirectories: true,
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("已取消保存")
	}
	return path, nil
}

func writeExportBytes(path string, data []byte) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".xlsx") {
		path += ".xlsx"
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		return "", err
	}
	return path, nil
}

func writeJSONExportBytes(path string, data []byte) (string, error) {
	if !strings.EqualFold(filepath.Ext(path), ".json") {
		path += ".json"
	}
	if len(data) == 0 {
		return "", errors.New("没有可保存的 JSON")
	}
	if !json.Valid(data) {
		return "", errors.New("生成的 all.json 格式无效")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return "", err
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return "", err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempPath, path); err == nil {
		return path, nil
	} else if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
		return "", fmt.Errorf("替换 JSON 文件失败: %w（原文件无法覆盖: %v）", err, removeErr)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return "", fmt.Errorf("保存 JSON 文件失败: %w", err)
	}
	return path, nil
}

func safeJSONExportName(alias string) string {
	var builder strings.Builder
	for _, char := range strings.TrimSpace(alias) {
		if char < 32 || strings.ContainsRune("\\\\/:*?\"<>|", char) {
			builder.WriteRune('_')
			continue
		}
		builder.WriteRune(char)
	}
	name := strings.Trim(strings.TrimSpace(builder.String()), ".")
	if name == "" {
		return "vps"
	}
	return name
}

func validateExportAllJSON(data []byte) error {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return fmt.Errorf("生成的 all.json 无法解析: %w", err)
	}
	if root == nil {
		return errors.New("生成的 all.json 根对象无效")
	}
	inbounds, ok := root["inbounds"].([]any)
	if !ok || len(inbounds) == 0 {
		return errors.New("生成的 all.json 没有可用的 inbounds")
	}
	outbounds, ok := root["outbounds"].([]any)
	if !ok || len(outbounds) == 0 {
		return errors.New("生成的 all.json 没有可用的 outbounds")
	}
	route, ok := root["route"].(map[string]any)
	if !ok || route == nil {
		return errors.New("生成的 all.json 缺少 route 配置")
	}
	if _, ok := route["rules"].([]any); !ok {
		return errors.New("生成的 all.json route.rules 格式无效")
	}
	rules := route["rules"].([]any)
	inboundTags := make(map[string]struct{}, len(inbounds))
	listenPorts := make(map[float64]string, len(inbounds))
	for index, item := range inbounds {
		inbound, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("生成的 all.json inbound[%d] 格式无效", index)
		}
		inboundType, _ := inbound["type"].(string)
		inboundTag, _ := inbound["tag"].(string)
		if strings.TrimSpace(inboundType) == "" {
			return fmt.Errorf("生成的 all.json inbound[%d] 缺少 type", index)
		}
		if strings.TrimSpace(inboundTag) == "" {
			return fmt.Errorf("生成的 all.json inbound[%d] 缺少 tag", index)
		}
		port, ok := inbound["listen_port"].(float64)
		if !ok || port <= 0 || port > 65535 || port != float64(int(port)) {
			return fmt.Errorf("生成的 all.json inbound[%d] listen_port 无效", index)
		}
		if _, duplicate := inboundTags[inboundTag]; duplicate {
			return fmt.Errorf("生成的 all.json inbound tag 重复: %s", inboundTag)
		}
		inboundTags[inboundTag] = struct{}{}
		if previous := listenPorts[port]; previous != "" {
			return fmt.Errorf("生成的 all.json listen_port 重复: %.0f (%s / %s)", port, previous, inboundTag)
		}
		listenPorts[port] = inboundTag
	}
	outboundTags := make(map[string]struct{}, len(outbounds))
	for index, item := range outbounds {
		outbound, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("生成的 all.json outbound[%d] 格式无效", index)
		}
		outboundType, _ := outbound["type"].(string)
		if strings.TrimSpace(outboundType) == "" {
			return fmt.Errorf("生成的 all.json outbound[%d] 缺少 type", index)
		}
		if tag, _ := outbound["tag"].(string); strings.TrimSpace(tag) != "" {
			if _, duplicate := outboundTags[tag]; duplicate {
				return fmt.Errorf("生成的 all.json outbound tag 重复: %s", tag)
			}
			outboundTags[tag] = struct{}{}
		}
	}
	routedTags := make(map[string]struct{}, len(inbounds))
	for index, item := range rules {
		rule, ok := item.(map[string]any)
		if !ok {
			return fmt.Errorf("生成的 all.json route.rules[%d] 格式无效", index)
		}
		inboundList, hasInbound := rule["inbound"].([]any)
		if !hasInbound {
			// 保留用户节点配置中的其他合法路由规则（例如域名、进程规则）。
			continue
		}
		if len(inboundList) == 0 {
			return fmt.Errorf("生成的 all.json route.rules[%d] inbound 不能为空", index)
		}
		outboundTag, _ := rule["outbound"].(string)
		if strings.TrimSpace(outboundTag) == "" {
			return fmt.Errorf("生成的 all.json route.rules[%d] 缺少 outbound", index)
		}
		if _, exists := outboundTags[outboundTag]; !exists {
			return fmt.Errorf("生成的 all.json route.rules[%d] 引用了不存在的 outbound: %s", index, outboundTag)
		}
		for _, value := range inboundList {
			inboundTag, _ := value.(string)
			if _, exists := inboundTags[inboundTag]; !exists {
				return fmt.Errorf("生成的 all.json route.rules[%d] 引用了不存在的 inbound: %s", index, inboundTag)
			}
			routedTags[inboundTag] = struct{}{}
		}
	}
	if len(routedTags) != len(inboundTags) {
		return errors.New("生成的 all.json 存在未绑定路由的 inbound")
	}
	return nil
}

func (a *App) SaveTextFile(fileName, content string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New("没有可保存的内容")
	}
	if a.ctx == nil {
		return "", errors.New("应用窗口尚未初始化")
	}
	if strings.TrimSpace(fileName) == "" {
		fileName = "export.txt"
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{Title: "导出 TXT", DefaultDirectory: downloadDirectory(), DefaultFilename: fileName, Filters: []runtime.FileFilter{{DisplayName: "文本文件 (*.txt)", Pattern: "*.txt"}}, CanCreateDirectories: true})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(path) == "" {
		return "", errors.New("已取消导出")
	}
	if !strings.EqualFold(filepath.Ext(path), ".txt") {
		path += ".txt"
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		return "", err
	}
	return path, nil
}

func (a *App) GetAppInfo() core.AppInfo {
	if a.requireAccess() != nil {
		return core.AppInfo{}
	}
	return a.service.GetAppInfo()
}

func (a *App) GetOverview() (*core.Overview, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetOverview()
}

func (a *App) RefreshVPSCountries() ([]core.VPS, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.RefreshVPSCountries()
}

func (a *App) SaveVPS(vps core.VPS) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.SaveVPS(vps)
}

func (a *App) DeleteVPS(alias string) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.DeleteVPS(alias)
}

func (a *App) GetNodes(alias string) ([]core.NodeSummary, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetNodes(alias)
}

func (a *App) GetBackups(alias string) ([]core.BackupFile, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetBackups(alias)
}

func (a *App) ReadBackup(path string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	return a.service.ReadBackup(path)
}

func (a *App) TestTCP(address string, port int) core.CheckResult {
	if err := a.requireAccess(); err != nil {
		return core.CheckResult{Success: false, Message: err.Error()}
	}
	return a.service.TestTCP(address, port)
}

func (a *App) AddNodes(req core.AddNodeRequest) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.AddNodes(req)
}

func (a *App) BatchAddNodes(req core.BatchAddNodeRequest) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.BatchAddNodes(req)
}

func (a *App) DetectVPSIPs(alias string) (core.VPSIPList, error) {
	if err := a.requireAccess(); err != nil {
		return core.VPSIPList{}, err
	}
	return a.service.DetectVPSIPs(alias)
}

func (a *App) AddMultiIPNodes(req core.MultiIPNodeRequest) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.AddMultiIPNodes(req)
}

func (a *App) DeleteNode(alias, filename string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.DeleteNode(alias, filename)
}

func (a *App) GetNodeDefaults(alias string, count int) (core.NodeDefaults, error) {
	if err := a.requireAccess(); err != nil {
		return core.NodeDefaults{}, err
	}
	return a.service.GetNodeDefaults(alias, count)
}

func (a *App) BatchDeleteNodes(alias string, filenames []string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.BatchDeleteNodes(alias, filenames)
}

func (a *App) DeleteNodeGroup(alias, group string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.DeleteNodeGroup(alias, group)
}

func (a *App) ModifyNodeOutbounds(alias string, filenames []string, outboundLines, outboundProtocol string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.ModifyNodeOutbounds(alias, filenames, outboundLines, outboundProtocol)
}

func (a *App) BackupNow(alias string) (core.BackupFile, error) {
	if err := a.requireAccess(); err != nil {
		return core.BackupFile{}, err
	}
	return a.service.BackupNow(alias)
}

func (a *App) BackupAllJSON(alias string) (core.BackupFile, error) {
	if err := a.requireAccess(); err != nil {
		return core.BackupFile{}, err
	}
	return a.service.BackupAllJSON(alias)
}

func (a *App) ReadNodeJSON(alias, filename string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	return a.service.ReadNodeJSON(alias, filename)
}

func (a *App) ReadAllJSON(alias string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	return a.service.ReadAllJSON(alias)
}

func (a *App) SaveNodeJSONAndDeploy(alias, filename, text string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.SaveNodeJSONAndDeploy(alias, filename, text)
}

func (a *App) SaveAllJSONAndDeploy(alias, text string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.SaveAllJSONAndDeploy(alias, text)
}

func (a *App) ImportAllJSONAndDeploy(alias, text string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.ImportAllJSONAndDeploy(alias, text)
}

func (a *App) RestoreBackup(targetAlias, backupPath string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.RestoreBackup(targetAlias, backupPath)
}

func (a *App) MigrateBackup(targetAlias, backupPath string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.MigrateBackup(targetAlias, backupPath)
}

func (a *App) RollbackLocalNodes(alias string, filenames []string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.RollbackLocalNodes(alias, filenames)
}

func (a *App) DeleteBackup(path string) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.DeleteBackup(path)
}

func (a *App) ExportGroupExcel(alias, group string) (core.ExportFile, error) {
	if err := a.requireAccess(); err != nil {
		return core.ExportFile{}, err
	}
	return a.service.ExportGroupExcel(alias, group)
}

func (a *App) ExportNodeExcel(alias, filename, mode string) (core.ExportFile, error) {
	if err := a.requireAccess(); err != nil {
		return core.ExportFile{}, err
	}
	return a.service.ExportNodeExcel(alias, filename, mode)
}

func (a *App) GetNodeQRCode(alias, filename string) (core.QRCodeResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.QRCodeResult{}, err
	}
	return a.service.GetNodeQRCode(alias, filename)
}

func (a *App) RunSSHCommand(alias, command string, timeoutSeconds int) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.RunSSHCommand(alias, command, timeoutSeconds)
}

func (a *App) GetSingboxStatus(alias string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.GetSingboxStatus(alias)
}

func (a *App) RefreshVPSState(alias string, interactive bool) core.VPSRuntimeState {
	if err := a.requireAccess(); err != nil {
		return core.VPSRuntimeState{Alias: alias, Message: err.Error()}
	}
	return a.service.RefreshVPSState(alias, interactive)
}

func (a *App) RefreshVPSStateInteractive(alias string, interactive bool) core.VPSRuntimeState {
	if err := a.requireAccess(); err != nil {
		return core.VPSRuntimeState{Alias: alias, Message: err.Error()}
	}
	return a.service.RefreshVPSStateInteractive(alias, interactive)
}

func (a *App) ControlSingbox(alias, action string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.ControlSingbox(alias, action)
}

func (a *App) DeploySingbox(alias string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.DeploySingbox(alias)
}

func (a *App) GetSingboxLog(alias string, maxLines int) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.GetSingboxLog(alias, maxLines)
}

func (a *App) RebootVPS(alias string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.RebootVPS(alias)
}

func (a *App) DebugVPS(alias, action, target string, port int) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.DebugVPS(alias, action, target, port)
}

func (a *App) GetTrafficRank(alias string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.GetTrafficRank(alias)
}

func (a *App) UploadAllJSON(alias string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.UploadAllJSON(alias)
}

func (a *App) UploadAllJSONAndDeleteRemoteNodes(alias string, filenames []string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.UploadAllJSONAndDeleteRemoteNodes(alias, filenames)
}

func (a *App) SyncAllJSON(alias string) core.CommandResult {
	if err := a.requireAccess(); err != nil {
		return core.CommandResult{Success: false, Message: err.Error()}
	}
	return a.service.SyncAllJSON(alias)
}

func (a *App) GetNodeRecords() ([]core.NodeRecord, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetNodeRecords()
}

func (a *App) SaveNodeRecord(record core.NodeRecord) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.SaveNodeRecord(record)
}

func (a *App) DeleteNodeRecord(id int) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.DeleteNodeRecord(id)
}

func (a *App) GetDeletedRecords() ([]core.DeletedRecord, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetDeletedRecords()
}

func (a *App) RestoreDeletedRecord(id int) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.RestoreDeletedRecord(id)
}

func (a *App) RollbackDeletedNodes(alias string, filenames []string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.RollbackDeletedNodes(alias, filenames)
}

func (a *App) GetAutoDeleteSettings() (core.AutoDeleteSettings, error) {
	if err := a.requireAccess(); err != nil {
		return core.AutoDeleteSettings{}, err
	}
	return a.service.GetAutoDeleteSettings()
}

func (a *App) SaveAutoDeleteSettings(settings core.AutoDeleteSettings) (core.AutoDeleteSettings, error) {
	if err := a.requireAccess(); err != nil {
		return core.AutoDeleteSettings{}, err
	}
	return a.service.SaveAutoDeleteSettings(settings)
}

func (a *App) RunAutoDeleteCheck(vpsAlias string) (core.AutoDeleteRunResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.AutoDeleteRunResult{}, err
	}
	return a.service.RunAutoDeleteCheck(vpsAlias)
}

func (a *App) GetHealthStatus() ([]core.HealthStatus, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetHealthStatus()
}

func (a *App) ClearHealthStatus(confirmText string) (int, error) {
	if err := a.requireAccess(); err != nil {
		return 0, err
	}
	return a.service.ClearHealthStatus(confirmText)
}

func (a *App) DeleteDeletedRecords(ids []int, confirmText string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.DeleteDeletedRecords(ids, confirmText)
}

func (a *App) RestoreDeletedRecords(ids []int) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.RestoreDeletedRecords(ids)
}

func (a *App) ImportNodeConnections(text string) (core.ImportResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.ImportResult{}, err
	}
	return a.service.ImportNodeConnections(text)
}

func (a *App) SearchNodeConnections(keyword string, limit int) ([]core.NodeConnectionRecord, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.SearchNodeConnections(keyword, limit)
}

func (a *App) DeleteNodeConnection(id int) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.DeleteNodeConnection(id)
}

func (a *App) ClearNodeConnections(confirmText string) (int, error) {
	if err := a.requireAccess(); err != nil {
		return 0, err
	}
	return a.service.ClearNodeConnections(confirmText)
}

func (a *App) ExportNodeConnections(keyword string) (string, error) {
	if err := a.requireAccess(); err != nil {
		return "", err
	}
	return a.service.ExportNodeConnections(keyword)
}

func (a *App) GetRuleGroups() ([]core.RuleGroup, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.GetRuleGroups()
}

func (a *App) SaveRuleGroup(group core.RuleGroup) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.SaveRuleGroup(group)
}

func (a *App) DeleteRuleGroup(name string) error {
	if err := a.requireAccess(); err != nil {
		return err
	}
	return a.service.DeleteRuleGroup(name)
}

func (a *App) ApplyRuleGroup(alias string, filenames []string, ruleName, action string) (core.OperationResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OperationResult{}, err
	}
	return a.service.ApplyRuleGroup(alias, filenames, ruleName, action)
}

func (a *App) QueryOutboundIP(alias, filename string) (core.OutboundIPResult, error) {
	if err := a.requireAccess(); err != nil {
		return core.OutboundIPResult{}, err
	}
	return a.service.QueryOutboundIP(alias, filename)
}

func (a *App) QueryAllOutboundIP(alias string) ([]core.OutboundIPResult, error) {
	if err := a.requireAccess(); err != nil {
		return nil, err
	}
	return a.service.QueryAllOutboundIP(alias)
}

func (a *App) GetMiddleSettingsHistory() (core.MiddleSettingsHistory, error) {
	if err := a.requireAccess(); err != nil {
		return core.MiddleSettingsHistory{}, err
	}
	return a.service.GetMiddleSettingsHistory()
}

func (a *App) SaveMiddleSettings(settings core.MiddleSettings) (core.MiddleSettingsHistory, error) {
	if err := a.requireAccess(); err != nil {
		return core.MiddleSettingsHistory{}, err
	}
	return a.service.SaveMiddleSettings(settings)
}

func downloadDirectory() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, "Downloads")
}
