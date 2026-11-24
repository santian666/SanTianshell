package main

import (
	"archive/zip"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"santianshell/internal/aiagent"
	"santianshell/internal/rdp"
	"santianshell/internal/remote"
	"santianshell/internal/sshprompt"
	"santianshell/internal/store"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/crypto/ssh"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const appVersion = "0.1.36"

const (
	maxAIModelsResponseSize  = 2 << 20
	maxAIErrorMessageLength  = 300
	maxAIPresetPromptSize    = 32 << 10
	maxQuickCommandNameRunes = 64
	maxQuickCommandSize      = 64 << 10
	maxQueueCommandSteps     = 100
	maxQueueCommandStepSize  = 4 << 10
	securityPBKDF2Iterations = 210000
	securityPBKDF2KeyLength  = 32
	securityMaxAttempts      = 5
	maxAutoStartCommandUnits = 260
)

const securityRetryDelay = 30 * time.Second

const shutdownCleanupTimeout = 5 * time.Second

type AppInfo struct {
	Name      string `json:"name"`
	Version   string `json:"version"`
	Platform  string `json:"platform"`
	Arch      string `json:"arch"`
	GoVersion string `json:"goVersion"`
	DataDir   string `json:"dataDir"`
	LogDir    string `json:"logDir"`
	Framework string `json:"framework"`
}

type AIConfig struct {
	ID       string   `json:"id"`
	Name     string   `json:"name"`
	Endpoint string   `json:"endpoint"`
	APIKey   string   `json:"apiKey"`
	Model    string   `json:"model"`
	Models   []string `json:"models,omitempty"`
	Mode     string   `json:"mode"`
}

type Settings struct {
	Hosts             []remote.Host  `json:"hosts"`
	AI                AIConfig       `json:"ai,omitempty"`
	AIProviders       []AIConfig     `json:"aiProviders"`
	CurrentProviderID string         `json:"currentProviderId"`
	QuickCommands     []QuickCommand `json:"quickCommands"`
	Groups            []string       `json:"groups"`
	Credentials       []Credential   `json:"credentials"`
	Theme             string         `json:"theme"`
	DownloadDirectory string         `json:"downloadDirectory"`
	AIPresetPrompt    string         `json:"aiPresetPrompt"`
	AutoStart         bool           `json:"autoStart"`
	SecurityHash      string         `json:"securityHash,omitempty"`
	SecuritySalt      string         `json:"securitySalt,omitempty"`
	LockOnStartup     bool           `json:"lockOnStartup"`
	ProtectExport     bool           `json:"protectExport"`
}

type GlobalSettings struct {
	Theme             string `json:"theme"`
	DownloadDirectory string `json:"downloadDirectory"`
	AIPresetPrompt    string `json:"aiPresetPrompt"`
	AutoStart         bool   `json:"autoStart"`
	LockOnStartup     bool   `json:"lockOnStartup"`
	ProtectExport     bool   `json:"protectExport"`
	CurrentPassword   string `json:"currentPassword"`
	NewPassword       string `json:"newPassword"`
	ConfirmPassword   string `json:"confirmPassword"`
	RemoveSecurity    bool   `json:"removeSecurity"`
}

type SecurityVerifyResult struct {
	Success           bool   `json:"success"`
	Message           string `json:"message"`
	RetryAfterSeconds int    `json:"retryAfterSeconds"`
	RemainingAttempts int    `json:"remainingAttempts"`
}

type PublicSettings struct {
	Theme              string `json:"theme"`
	DownloadDirectory  string `json:"downloadDirectory"`
	AIPresetPrompt     string `json:"aiPresetPrompt"`
	AutoStart          bool   `json:"autoStart"`
	LockOnStartup      bool   `json:"lockOnStartup"`
	ProtectExport      bool   `json:"protectExport"`
	SecurityConfigured bool   `json:"securityConfigured"`
}

type HostImportResult struct {
	Settings     Settings `json:"settings"`
	Imported     int      `json:"imported"`
	Skipped      int      `json:"skipped"`
	IgnoredFiles int      `json:"ignoredFiles"`
	FinalShell   int      `json:"finalShell"`
}

type TerminalStartResult struct {
	Connected        bool   `json:"connected"`
	ErrorCode        string `json:"errorCode,omitempty"`
	Error            string `json:"error,omitempty"`
	PasswordPrompted bool   `json:"passwordPrompted,omitempty"`
	PasswordSaved    bool   `json:"passwordSaved,omitempty"`
	Warning          string `json:"warning,omitempty"`
}

type Credential struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Type           string `json:"type"`
	Password       string `json:"password"`
	KeyContent     string `json:"keyContent"`
	KeyPassphrase  string `json:"keyPassphrase"`
	KeyFileName    string `json:"keyFileName"`
	KeyType        string `json:"keyType"`
	KeyFingerprint string `json:"keyFingerprint"`
	Remark         string `json:"remark"`
}

type PrivateKeyImport struct {
	FileName  string `json:"fileName"`
	Content   string `json:"content"`
	Encrypted bool   `json:"encrypted"`
}
type QuickCommand struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Command        string `json:"command"`
	Type           string `json:"type,omitempty"`
	TimeoutSeconds int    `json:"timeoutSeconds,omitempty"`
	Retries        int    `json:"retries,omitempty"`
}


type closeFunc func()

func (fn closeFunc) Close() error {
	fn()
	return nil
}

type App struct {
	mu                   sync.RWMutex
	store                settingsSaver
	settings             Settings
	ctx                  context.Context
	shells               map[string]*remote.Shell
	shellEpochs          map[string]uint64
	shellClientEpochs    map[string]int
	shellHostRevisions   map[string]string
	hostOSReleases       map[string]string
	connections          *remote.ConnectionManager
	aiAgent              *aiagent.Controller
	rdpRunner            *rdp.Runner
	rdpSessions          map[string]*rdp.Session
	rdpSessionHosts      map[string]string
	rdpSessionJumps      map[string]string
	rdpSessionClosing    map[string]struct{}
	portForwardMu        sync.RWMutex
	portForwards         map[string]*appPortForward
	queueMu              sync.Mutex
	queueRuns            map[string]*queueRunState
	sessionLogMu         sync.Mutex
	sessionLogs          map[string]*sshSessionLogState
	sshPrompts           *sshprompt.Broker
	temporaryHosts       map[string]remote.Host
	transientPasswords   map[string]string
	unlocked             bool
	securityFailures     int
	securityBlockedUntil time.Time
	lifecycleCtx         context.Context
	lifecycleCancel      context.CancelFunc
	nodeDataCleanup      func(string, string) error
	updateManager        *AppUpdateManager
	shuttingDown         atomic.Bool
}

type settingsSaver interface {
	Save(any) error
}

var localProxyLatencyCache struct {
	sync.Mutex
	selector string
	value    int64
	expires  time.Time
}

func defaultSettings() Settings {
	return Settings{
		Hosts:         []remote.Host{},
		AIProviders:   []AIConfig{},
		QuickCommands: []QuickCommand{},
		Groups:        []string{},
		Credentials:   []Credential{},
	}
}

func NewApp() (*App, error) {
	s, err := store.New()
	if err != nil {
		return nil, err
	}
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	a := &App{store: s, settings: defaultSettings(), shells: map[string]*remote.Shell{}, shellEpochs: map[string]uint64{}, shellClientEpochs: map[string]int{}, shellHostRevisions: map[string]string{}, hostOSReleases: map[string]string{}, connections: remote.NewConnectionManager(), aiAgent: aiagent.NewController(), rdpRunner: rdp.NewRunner(), rdpSessions: map[string]*rdp.Session{}, rdpSessionHosts: map[string]string{}, rdpSessionJumps: map[string]string{}, rdpSessionClosing: map[string]struct{}{}, portForwards: map[string]*appPortForward{}, queueRuns: map[string]*queueRunState{}, sshPrompts: sshprompt.New(), temporaryHosts: map[string]remote.Host{}, transientPasswords: map[string]string{}, lifecycleCtx: lifecycleCtx, lifecycleCancel: lifecycleCancel, updateManager: newAppUpdateManager()}
	if err := s.Load(&a.settings); err != nil {
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	settingsChanged := false
	for i := range a.settings.Hosts {
		if a.settings.Hosts[i].MFACode != "" {
			a.settings.Hosts[i].MFACode = ""
			settingsChanged = true
		}
		if a.settings.Hosts[i].RDPGatewayPassword != "" {
			a.settings.Hosts[i].RDPGatewayPassword = ""
			settingsChanged = true
		}
		if strings.EqualFold(a.settings.Hosts[i].Protocol, "rdp") {
			if a.settings.Hosts[i].RDPRedirectClipboard == nil {
				enabled := true
				a.settings.Hosts[i].RDPRedirectClipboard = &enabled
				settingsChanged = true
			}
			if strings.TrimSpace(a.settings.Hosts[i].RDPResolution) == "" {
				a.settings.Hosts[i].RDPResolution = "1280x1024"
				settingsChanged = true
			}
			if a.settings.Hosts[i].RDPDisplayMode == "" {
				a.settings.Hosts[i].RDPDisplayMode = string(rdp.DisplayModeWindow)
				settingsChanged = true
			}
		}
	}
	if a.settings.AI.APIKey != "" {
		legacy := a.settings.AI
		legacy.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		if legacy.Name == "" {
			legacy.Name = "旧配置"
		}
		a.settings.AIProviders = append(a.settings.AIProviders, legacy)
		a.settings.CurrentProviderID = legacy.ID
		settingsChanged = true
	}
	if a.settings.AI.ID != "" || a.settings.AI.Name != "" || a.settings.AI.Endpoint != "" || a.settings.AI.APIKey != "" || a.settings.AI.Model != "" || len(a.settings.AI.Models) > 0 || a.settings.AI.Mode != "" {
		settingsChanged = true
	}
	a.settings.AI = AIConfig{}
	if a.settings.Theme == "" {
		a.settings.Theme = "light"
		settingsChanged = true
	}
	if a.settings.DownloadDirectory == "" {
		a.settings.DownloadDirectory = defaultDownloadDirectory()
		settingsChanged = true
	}
	if settingsChanged {
		if err := s.Save(a.settings); err != nil {
			return nil, fmt.Errorf("迁移旧配置失败: %w", err)
		}
	}
	a.unlocked = !a.settings.LockOnStartup || a.settings.SecurityHash == ""
	return a, nil
}
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go cleanupOldUpdateArtifacts()
}

func (a *App) setNodeDataCleanup(cleanup func(string, string) error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.nodeDataCleanup = cleanup
}

func (a *App) shutdown(context.Context) {
	if a.shuttingDown.Swap(true) {
		return
	}
	if a.lifecycleCancel != nil {
		a.lifecycleCancel()
	}
	if a.aiAgent != nil {
		a.aiAgent.Close()
	}
	a.cancelAllQueues()
	if a.sshPrompts != nil {
		a.sshPrompts.Close()
	}

	a.mu.Lock()
	shells := make([]*remote.Shell, 0, len(a.shells))
	for id, shell := range a.shells {
		a.shellEpochs[id]++
		if shell != nil {
			shells = append(shells, shell)
		}
	}
	clear(a.shells)
	clear(a.shellClientEpochs)
	clear(a.shellHostRevisions)
	rdpSessions := make([]*rdp.Session, 0, len(a.rdpSessions))
	for _, session := range a.rdpSessions {
		if session != nil {
			rdpSessions = append(rdpSessions, session)
		}
	}
	clear(a.rdpSessions)
	clear(a.rdpSessionHosts)
	clear(a.rdpSessionJumps)
	clear(a.rdpSessionClosing)
	clear(a.temporaryHosts)
	clear(a.transientPasswords)
	a.mu.Unlock()

	a.closeAllSSHSessionLogs()
	a.closeAllPortForwards()
	for _, shell := range shells {
		shell.Close()
	}
	rdpDone := make([]<-chan struct{}, 0, len(rdpSessions))
	for _, session := range rdpSessions {
		rdpDone = append(rdpDone, session.Done())
		if err := session.Close(); err != nil {
			slog.Warn("关闭 RDP 会话失败", "会话ID", session.ID, "错误", err)
		}
	}
	if a.connections != nil {
		a.connections.CloseAll()
	}
	if !waitForShutdownCleanup(rdpDone, shutdownCleanupTimeout) {
		slog.Warn("等待 RDP 资源清理超时", "超时", shutdownCleanupTimeout, "会话数", len(rdpDone))
	}
}

func (a *App) lifecycleContext() context.Context {
	if a != nil && a.lifecycleCtx != nil {
		return a.lifecycleCtx
	}
	return context.Background()
}

func waitForShutdownCleanup(doneChannels []<-chan struct{}, timeout time.Duration) bool {
	if len(doneChannels) == 0 {
		return true
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for _, done := range doneChannels {
		if done == nil {
			continue
		}
		select {
		case <-done:
		case <-timer.C:
			return false
		}
	}
	return true
}
func (a *App) GetSettings() (Settings, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.unlocked {
		return Settings{}, errors.New("软件已锁定，请先输入密保")
	}
	return settingsForClient(a.settings), nil
}

func (a *App) GetPublicSettings() PublicSettings {
	a.mu.RLock()
	defer a.mu.RUnlock()
	presetPrompt := aiagent.NormalizeUserPresetPrompt(a.settings.AIPresetPrompt)
	return PublicSettings{Theme: a.settings.Theme, DownloadDirectory: a.settings.DownloadDirectory, AIPresetPrompt: presetPrompt, AutoStart: a.settings.AutoStart, LockOnStartup: a.settings.LockOnStartup, ProtectExport: a.settings.ProtectExport, SecurityConfigured: a.settings.SecurityHash != ""}
}

func settingsForClient(settings Settings) Settings {
	settings = cloneSettings(settings)
	settings.SecurityHash = ""
	settings.SecuritySalt = ""
	for i := range settings.Hosts {
		settings.Hosts[i].MFACode = ""
		settings.Hosts[i].RDPGatewayPassword = ""
	}
	return settings
}

func cloneSettings(settings Settings) Settings {
	settings.Hosts = append([]remote.Host(nil), settings.Hosts...)
	for i := range settings.Hosts {
		settings.Hosts[i].RDPDriveIDs = append([]string(nil), settings.Hosts[i].RDPDriveIDs...)
		if settings.Hosts[i].RDPRedirectClipboard != nil {
			enabled := *settings.Hosts[i].RDPRedirectClipboard
			settings.Hosts[i].RDPRedirectClipboard = &enabled
		}
	}
	settings.AIProviders = append([]AIConfig(nil), settings.AIProviders...)
	for i := range settings.AIProviders {
		settings.AIProviders[i].Models = append([]string(nil), settings.AIProviders[i].Models...)
	}
	settings.AI.Models = append([]string(nil), settings.AI.Models...)
	settings.QuickCommands = append([]QuickCommand(nil), settings.QuickCommands...)
	settings.Groups = append([]string(nil), settings.Groups...)
	settings.Credentials = append([]Credential(nil), settings.Credentials...)
	return settings
}

// commitSettingsLocked 仅在持久化成功后替换内存配置；调用方必须持有 a.mu 写锁。
func (a *App) commitSettingsLocked(candidate Settings) error {
	if a.store == nil {
		return errors.New("配置存储未初始化")
	}
	if err := a.store.Save(candidate); err != nil {
		return err
	}
	a.settings = candidate
	return nil
}

func passwordHash(password, salt string) string {
	sum := sha256.Sum256([]byte(salt + "\x00" + password))
	return hex.EncodeToString(sum[:])
}

func newSecurityPassword(password string) (string, string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", "", err
	}
	key := pbkdf2.Key([]byte(password), salt, securityPBKDF2Iterations, securityPBKDF2KeyLength, sha256.New)
	return fmt.Sprintf("pbkdf2-sha256$%d$%s", securityPBKDF2Iterations, hex.EncodeToString(key)), hex.EncodeToString(salt), nil
}

func verifyStoredSecurityPassword(password, storedHash, storedSalt string) (bool, bool) {
	if len(password) > 4096 {
		return false, false
	}
	if strings.HasPrefix(storedHash, "pbkdf2-sha256$") {
		parts := strings.Split(storedHash, "$")
		if len(parts) != 3 {
			return false, false
		}
		iterations, err := strconv.Atoi(parts[1])
		if err != nil || iterations < 10000 || iterations > 2000000 {
			return false, false
		}
		salt, err := hex.DecodeString(storedSalt)
		if err != nil || len(salt) < 16 {
			return false, false
		}
		expected, err := hex.DecodeString(parts[2])
		if err != nil || len(expected) != securityPBKDF2KeyLength {
			return false, false
		}
		actual := pbkdf2.Key([]byte(password), salt, iterations, len(expected), sha256.New)
		return subtle.ConstantTimeCompare(actual, expected) == 1, false
	}
	if len(storedHash) != sha256.Size*2 {
		return false, false
	}
	actual := passwordHash(password, storedSalt)
	return subtle.ConstantTimeCompare([]byte(actual), []byte(storedHash)) == 1, true
}

func validateNewSecurityPassword(password, confirmation string) error {
	if password != confirmation {
		return errors.New("两次输入的新密保密码不一致")
	}
	if !utf8.ValidString(password) {
		return errors.New("密保密码包含无效字符")
	}
	length := utf8.RuneCountInString(password)
	if length < 8 || length > 128 {
		return errors.New("密保密码必须为 8～128 个字符")
	}
	if strings.TrimSpace(password) == "" {
		return errors.New("密保密码不能全部为空白字符")
	}
	return nil
}

func (a *App) requireUnlocked() error {
	if a.shuttingDown.Load() {
		return errors.New("软件正在退出")
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	if !a.unlocked {
		return errors.New("软件已锁定，请先输入密保")
	}
	return nil
}

func (a *App) verifySecurityPasswordLocked(password string, unlock bool) SecurityVerifyResult {
	now := time.Now()
	if !a.securityBlockedUntil.IsZero() {
		if now.Before(a.securityBlockedUntil) {
			remaining := a.securityBlockedUntil.Sub(now)
			seconds := int((remaining + time.Second - 1) / time.Second)
			return SecurityVerifyResult{Message: fmt.Sprintf("尝试次数过多，请 %d 秒后再试", seconds), RetryAfterSeconds: seconds, RemainingAttempts: 0}
		}
		a.securityBlockedUntil = time.Time{}
		a.securityFailures = 0
	}
	if a.settings.SecurityHash == "" {
		if unlock {
			a.unlocked = true
		}
		a.securityFailures = 0
		return SecurityVerifyResult{Success: true, Message: "软件未设置密保", RemainingAttempts: securityMaxAttempts}
	}
	ok, legacy := verifyStoredSecurityPassword(password, a.settings.SecurityHash, a.settings.SecuritySalt)
	if !ok {
		a.securityFailures++
		remaining := securityMaxAttempts - a.securityFailures
		if remaining <= 0 {
			a.securityFailures = 0
			a.securityBlockedUntil = now.Add(securityRetryDelay)
			return SecurityVerifyResult{Message: "尝试次数过多，请 30 秒后再试", RetryAfterSeconds: int(securityRetryDelay / time.Second), RemainingAttempts: 0}
		}
		return SecurityVerifyResult{Message: fmt.Sprintf("密保密码错误，还可尝试 %d 次", remaining), RemainingAttempts: remaining}
	}
	a.securityFailures = 0
	a.securityBlockedUntil = time.Time{}
	if unlock {
		a.unlocked = true
	}
	message := "验证成功"
	if legacy {
		hash, salt, err := newSecurityPassword(password)
		if err == nil && a.store != nil {
			candidate := cloneSettings(a.settings)
			candidate.SecurityHash = hash
			candidate.SecuritySalt = salt
			if err = a.commitSettingsLocked(candidate); err == nil {
				message = "验证成功，密保已升级"
			}
		}
	}
	return SecurityVerifyResult{Success: true, Message: message, RemainingAttempts: securityMaxAttempts}
}

func (a *App) VerifySecurityPassword(password string) SecurityVerifyResult {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.verifySecurityPasswordLocked(password, true)
}

func autoStartCommand(executable string) (string, error) {
	if executable == "" || strings.ContainsAny(executable, "\"\r\n\x00") {
		return "", errors.New("程序路径无效，无法设置开机启动")
	}
	command := `"` + executable + `"`
	if len(utf16.Encode([]rune(command))) > maxAutoStartCommandUnits {
		return "", errors.New("程序路径过长，无法设置开机启动")
	}
	return command, nil
}

func setAutoStart(enabled bool) error {
	const keyPath = `Software\Microsoft\Windows\CurrentVersion\Run`
	const valueName = "SanTianShell"
	if !enabled {
		key, err := registry.OpenKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		defer key.Close()
		err = key.DeleteValue(valueName)
		if errors.Is(err, registry.ErrNotExist) {
			return nil
		}
		return err
	}
	key, _, err := registry.CreateKey(registry.CURRENT_USER, keyPath, registry.SET_VALUE)
	if err != nil {
		return err
	}
	defer key.Close()
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	command, err := autoStartCommand(exe)
	if err != nil {
		return err
	}
	return key.SetStringValue(valueName, command)
}

func (a *App) SaveGlobalSettings(input GlobalSettings) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	if input.Theme != "light" && input.Theme != "dark" {
		input.Theme = "light"
	}
	input.DownloadDirectory = filepath.Clean(strings.TrimSpace(input.DownloadDirectory))
	if input.DownloadDirectory == "." || input.DownloadDirectory == "" {
		input.DownloadDirectory = defaultDownloadDirectory()
	}
	info, err := os.Stat(input.DownloadDirectory)
	if err != nil || !info.IsDir() {
		return Settings{}, errors.New("下载保存路径不存在或不是文件夹")
	}
	presetPrompt := aiagent.NormalizeUserPresetPrompt(input.AIPresetPrompt)
	if len([]byte(presetPrompt)) > maxAIPresetPromptSize {
		return Settings{}, errors.New("AI 预设提示词不能超过 32KB")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.unlocked {
		return Settings{}, errors.New("软件已锁定，请先输入密保")
	}

	configured := a.settings.SecurityHash != ""
	flagsChanged := input.LockOnStartup != a.settings.LockOnStartup || input.ProtectExport != a.settings.ProtectExport
	newPasswordProvided := input.NewPassword != "" || input.ConfirmPassword != ""
	securityChanged := flagsChanged || newPasswordProvided || input.RemoveSecurity
	if newPasswordProvided {
		if err := validateNewSecurityPassword(input.NewPassword, input.ConfirmPassword); err != nil {
			return Settings{}, err
		}
	}
	if configured && securityChanged {
		if input.CurrentPassword == "" {
			return Settings{}, errors.New("请输入当前密保密码")
		}
		verified := a.verifySecurityPasswordLocked(input.CurrentPassword, false)
		if !verified.Success {
			return Settings{}, errors.New(verified.Message)
		}
		configured = a.settings.SecurityHash != ""
	}
	if input.RemoveSecurity {
		if !configured {
			return Settings{}, errors.New("软件尚未设置密保")
		}
		if newPasswordProvided {
			return Settings{}, errors.New("移除密保时不能同时设置新密码")
		}
	} else {
		if configured && !input.LockOnStartup && !input.ProtectExport {
			return Settings{}, errors.New("关闭全部密保功能必须明确选择移除密保")
		}
		if !configured && (input.LockOnStartup || input.ProtectExport) && !newPasswordProvided {
			return Settings{}, errors.New("启用密保功能必须设置新密码")
		}
		if newPasswordProvided && !input.LockOnStartup && !input.ProtectExport {
			return Settings{}, errors.New("设置密保密码时请至少启用一项保护")
		}
	}

	candidate := cloneSettings(a.settings)
	candidate.Theme = input.Theme
	candidate.DownloadDirectory = input.DownloadDirectory
	candidate.AIPresetPrompt = presetPrompt
	candidate.AutoStart = input.AutoStart
	if input.RemoveSecurity {
		candidate.LockOnStartup = false
		candidate.ProtectExport = false
		candidate.SecurityHash = ""
		candidate.SecuritySalt = ""
	} else {
		candidate.LockOnStartup = input.LockOnStartup
		candidate.ProtectExport = input.ProtectExport
		if newPasswordProvided {
			hash, salt, err := newSecurityPassword(input.NewPassword)
			if err != nil {
				return Settings{}, fmt.Errorf("生成密保失败: %w", err)
			}
			candidate.SecurityHash = hash
			candidate.SecuritySalt = salt
		}
	}

	autoStartChanged := candidate.AutoStart != a.settings.AutoStart
	if autoStartChanged {
		if err := setAutoStart(candidate.AutoStart); err != nil {
			return Settings{}, fmt.Errorf("设置开机启动失败: %w", err)
		}
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		if autoStartChanged {
			_ = setAutoStart(a.settings.AutoStart)
		}
		return Settings{}, err
	}
	return settingsForClient(a.settings), nil
}
func appDataPath() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_RoamingAppData, windows.KF_FLAG_CREATE)
	if err != nil || dir == "" {
		dir, err = os.UserConfigDir()
	}
	if err != nil || dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, "SanTianShell")
}
func webViewDataPath() string {
	dir, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_CREATE)
	if err != nil || dir == "" {
		return ""
	}
	dir = filepath.Join(dir, "SanTianShell", "WebView2")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return ""
	}
	return dir
}
func defaultDownloadDirectory() string {
	if dir, err := windows.KnownFolderPath(windows.FOLDERID_Downloads, windows.KF_FLAG_CREATE); err == nil && dir != "" {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			return dir
		}
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return os.TempDir()
	}
	dir := filepath.Join(home, "Downloads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return home
	}
	return dir
}
func logDirectory() string { return filepath.Join(appDataPath(), "logs") }
func (a *App) runtimeContext() (context.Context, error) {
	if a.ctx == nil {
		return nil, errors.New("应用窗口尚未初始化")
	}
	return a.ctx, nil
}
func (a *App) SelectDownloadDirectory() (string, error) {
	if err := a.requireUnlocked(); err != nil {
		return "", err
	}
	ctx, err := a.runtimeContext()
	if err != nil {
		return "", err
	}
	a.mu.RLock()
	dir := a.settings.DownloadDirectory
	a.mu.RUnlock()
	if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
		dir = defaultDownloadDirectory()
	}
	return runtime2.OpenDirectoryDialog(ctx, runtime2.OpenDialogOptions{Title: "选择下载保存文件夹", DefaultDirectory: dir})
}
func (a *App) GetAppInfo() AppInfo {
	return AppInfo{Name: "SanTianShell", Version: appVersion, Platform: goruntime.GOOS, Arch: goruntime.GOARCH, GoVersion: goruntime.Version(), DataDir: appDataPath(), LogDir: logDirectory(), Framework: "Go + Wails 2 + Vite + 原生 JavaScript/CSS"}
}
func (a *App) OpenLogDirectory() error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	dir := logDirectory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	logFile := filepath.Join(dir, "app.log")
	if _, err := os.Stat(logFile); errors.Is(err, os.ErrNotExist) {
		_ = os.WriteFile(logFile, []byte("SanTianShell 日志目录已初始化。\n"), 0600)
	}
	windowsDirectory, err := windows.GetWindowsDirectory()
	if err != nil {
		return fmt.Errorf("获取 Windows 目录失败: %w", err)
	}
	command := exec.Command(filepath.Join(windowsDirectory, "explorer.exe"), dir)
	if err := command.Start(); err != nil {
		return err
	}
	return command.Process.Release()
}
func (a *App) ExportLogs() (resultErr error) {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	dir := logDirectory()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	ctx, err := a.runtimeContext()
	if err != nil {
		return err
	}
	target, err := runtime2.SaveFileDialog(ctx, runtime2.SaveDialogOptions{Title: "导出诊断日志", DefaultFilename: "SanTianShell-logs.zip"})
	if err != nil || target == "" {
		return err
	}
	out, err := os.Create(target)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(out)
	defer func() {
		if err := zw.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("完成日志压缩失败: %w", err)
		}
		if err := out.Close(); resultErr == nil && err != nil {
			resultErr = fmt.Errorf("保存日志压缩包失败: %w", err)
		}
		if resultErr != nil {
			_ = os.Remove(target)
		}
	}()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		if err := appendLogArchiveFile(zw, filepath.Join(dir, entry.Name()), entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func appendLogArchiveFile(zw *zip.Writer, path, name string) error {
	src, err := os.Open(path)
	if err != nil {
		return err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return nil
	}
	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}
	header.Name = name
	header.Method = zip.Deflate
	dst, err := zw.CreateHeader(header)
	if err != nil {
		return err
	}
	_, err = io.Copy(dst, src)
	return err
}


func (a *App) SaveCredential(c Credential) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	c.ID = strings.TrimSpace(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	c.Remark = strings.TrimSpace(c.Remark)
	c.KeyFileName = strings.TrimSpace(c.KeyFileName)
	if c.Name == "" || (c.Type != "password" && c.Type != "key") {
		return Settings{}, errors.New("凭证名称和类型不能为空")
	}
	if c.Type == "password" {
		if c.Password == "" {
			return Settings{}, errors.New("密码不能为空")
		}
		c.KeyContent = ""
		c.KeyPassphrase = ""
		c.KeyFileName = ""
		c.KeyType = ""
		c.KeyFingerprint = ""
	}
	if c.Type == "key" {
		if strings.TrimSpace(c.KeyContent) == "" {
			return Settings{}, errors.New("私钥内容不能为空")
		}
		signer, err := ssh.ParsePrivateKey([]byte(c.KeyContent))
		if _, encrypted := err.(*ssh.PassphraseMissingError); encrypted {
			if c.KeyPassphrase == "" {
				return Settings{}, errors.New("该私钥已加密，请输入私钥口令")
			}
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(c.KeyContent), []byte(c.KeyPassphrase))
		}
		if err != nil {
			return Settings{}, fmt.Errorf("私钥无效或口令错误: %w", err)
		}
		if signer.PublicKey().Type() == ssh.KeyAlgoDSA {
			return Settings{}, errors.New("DSA 私钥已淘汰，请使用 Ed25519、RSA 或 ECDSA")
		}
		c.Password = ""
		c.KeyType = signer.PublicKey().Type()
		c.KeyFingerprint = ssh.FingerprintSHA256(signer.PublicKey())
	}
	modifiedExisting := false
	var affectedHostIDs []string
	a.mu.Lock()
	candidate := cloneSettings(a.settings)
	if c.ID == "" {
		c.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		candidate.Credentials = append(candidate.Credentials, c)
	} else {
		found := false
		for i := range candidate.Credentials {
			if candidate.Credentials[i].ID == c.ID {
				candidate.Credentials[i] = c
				modifiedExisting = true
				found = true
				break
			}
		}
		if !found {
			candidate.Credentials = append(candidate.Credentials, c)
		}
	}
	if modifiedExisting {
		for _, h := range candidate.Hosts {
			if !h.Deleted && h.CredentialID == c.ID {
				affectedHostIDs = append(affectedHostIDs, h.ID)
			}
		}
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		a.mu.Unlock()
		return Settings{}, err
	}
	settings := settingsForClient(a.settings)
	a.mu.Unlock()
	if modifiedExisting {
		if a.connections != nil {
			for _, hostID := range affectedHostIDs {
				a.connections.Invalidate(hostID)
			}
		}
		if a.aiAgent != nil {
			a.aiAgent.Close()
		}
	}
	return settings, nil
}

func (a *App) SelectPrivateKeyFile() (PrivateKeyImport, error) {
	if err := a.requireUnlocked(); err != nil {
		return PrivateKeyImport{}, err
	}
	ctx, err := a.runtimeContext()
	if err != nil {
		return PrivateKeyImport{}, err
	}
	path, err := runtime2.OpenFileDialog(ctx, runtime2.OpenDialogOptions{Title: "选择 SSH 私钥文件", Filters: []runtime2.FileFilter{{DisplayName: "SSH 私钥", Pattern: "id_*;*.pem;*.key"}, {DisplayName: "所有文件", Pattern: "*"}}})
	if err != nil || path == "" {
		return PrivateKeyImport{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return PrivateKeyImport{}, err
	}
	if info.Size() > 1024*1024 {
		return PrivateKeyImport{}, errors.New("私钥文件不能超过 1 MB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return PrivateKeyImport{}, err
	}
	if strings.Contains(string(raw), " PUBLIC KEY") || strings.HasPrefix(strings.TrimSpace(string(raw)), "ssh-") {
		return PrivateKeyImport{}, errors.New("选择的是公钥文件，请选择私钥文件")
	}
	_, parseErr := ssh.ParsePrivateKey(raw)
	_, encrypted := parseErr.(*ssh.PassphraseMissingError)
	if parseErr != nil && !encrypted {
		return PrivateKeyImport{}, fmt.Errorf("不支持或无效的 SSH 私钥: %w", parseErr)
	}
	return PrivateKeyImport{FileName: filepath.Base(path), Content: string(raw), Encrypted: encrypted}, nil
}

func (a *App) DeleteCredential(id string) (Settings, error) {
	return a.deleteCredential(id, false)
}

func (a *App) UnbindAndDeleteCredential(id string) (Settings, error) {
	return a.deleteCredential(id, true)
}

func (a *App) deleteCredential(id string, unbind bool) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	id = strings.TrimSpace(id)
	a.mu.Lock()
	candidate := cloneSettings(a.settings)
	var activeUsing []string
	var invalidatedHostIDs []string
	for i := range candidate.Hosts {
		if candidate.Hosts[i].CredentialID != id {
			continue
		}
		if candidate.Hosts[i].Deleted || unbind {
			candidate.Hosts[i].CredentialID = ""
			if !candidate.Hosts[i].Deleted {
				invalidatedHostIDs = append(invalidatedHostIDs, candidate.Hosts[i].ID)
			}
			continue
		}
		label := strings.TrimSpace(candidate.Hosts[i].Name)
		if label == "" {
			label = candidate.Hosts[i].Address
		}
		activeUsing = append(activeUsing, label)
	}
	if len(activeUsing) > 0 {
		a.mu.Unlock()
		preview := activeUsing
		if len(preview) > 3 {
			preview = append(append([]string(nil), activeUsing[:3]...), fmt.Sprintf("等共 %d 台", len(activeUsing)))
		}
		return Settings{}, fmt.Errorf("该凭证正在被 %d 台主机使用（%s）", len(activeUsing), strings.Join(preview, "、"))
	}
	items := candidate.Credentials[:0]
	for _, c := range candidate.Credentials {
		if c.ID != id {
			items = append(items, c)
		}
	}
	candidate.Credentials = items
	if err := a.commitSettingsLocked(candidate); err != nil {
		a.mu.Unlock()
		return Settings{}, err
	}
	settings := settingsForClient(a.settings)
	a.mu.Unlock()
	if len(invalidatedHostIDs) > 0 {
		if a.connections != nil {
			for _, hostID := range invalidatedHostIDs {
				a.connections.Invalidate(hostID)
			}
		}
		if a.aiAgent != nil {
			a.aiAgent.Close()
		}
	}
	return settings, nil
}



func (a *App) SaveQuickCommand(c QuickCommand) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	c.ID = strings.TrimSpace(c.ID)
	c.Name = strings.TrimSpace(c.Name)
	c.Command = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(c.Command, "\r\n", "\n"), "\r", "\n"))
	if strings.ContainsRune(c.Name, '\x00') || strings.ContainsRune(c.Command, '\x00') {
		return Settings{}, errors.New("名称和命令不能包含 NUL 字符")
	}
	if c.Name == "" || c.Command == "" {
		return Settings{}, errors.New("名称和命令不能为空")
	}
	if utf8.RuneCountInString(c.Name) > maxQuickCommandNameRunes {
		return Settings{}, errors.New("快捷命令名称不能超过 64 个字符")
	}
	if len(c.Command) > maxQuickCommandSize {
		return Settings{}, errors.New("快捷命令内容不能超过 65536 字节")
	}
	if c.Type != "queue" {
		c.Type = "single"
		c.TimeoutSeconds = 0
		c.Retries = 0
	} else {
		steps := make([]string, 0, strings.Count(c.Command, "\n")+1)
		for _, step := range strings.Split(c.Command, "\n") {
			step = strings.TrimSpace(step)
			if step == "" {
				continue
			}
			if len(step) > maxQueueCommandStepSize {
				return Settings{}, errors.New("队列单步命令不能超过 4096 字节")
			}
			steps = append(steps, step)
			if len(steps) > maxQueueCommandSteps {
				return Settings{}, errors.New("队列步骤不能超过 100 个")
			}
		}
		if len(steps) == 0 {
			return Settings{}, errors.New("名称和命令不能为空")
		}
		c.Command = strings.Join(steps, "\n")
		if c.TimeoutSeconds < 0 {
			return Settings{}, errors.New("队列单步超时不能小于 0 秒")
		}
		if c.TimeoutSeconds == 0 {
			c.TimeoutSeconds = 300
		}
		if c.TimeoutSeconds > 3600 {
			return Settings{}, errors.New("队列单步超时不能超过 3600 秒")
		}
		if c.Retries < 0 || c.Retries > 3 {
			return Settings{}, errors.New("队列失败重试次数必须在 0 到 3 之间")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	if c.ID == "" {
		c.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		candidate.QuickCommands = append(candidate.QuickCommands, c)
	} else {
		found := false
		for i := range candidate.QuickCommands {
			if candidate.QuickCommands[i].ID == c.ID {
				candidate.QuickCommands[i] = c
				found = true
				break
			}
		}
		if !found {
			return Settings{}, errors.New("快捷命令不存在")
		}
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		return Settings{}, err
	}
	return settingsForClient(a.settings), nil
}
func (a *App) DeleteQuickCommand(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Settings{}, errors.New("快捷命令 ID 不能为空")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.QuickCommands {
		if candidate.QuickCommands[i].ID == id {
			candidate.QuickCommands = append(candidate.QuickCommands[:i], candidate.QuickCommands[i+1:]...)
			if err := a.commitSettingsLocked(candidate); err != nil {
				return Settings{}, err
			}
			return settingsForClient(a.settings), nil
		}
	}
	return Settings{}, errors.New("快捷命令不存在")
}
func (a *App) SaveGroup(name string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return Settings{}, errors.New("分组名称不能为空")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, v := range a.settings.Groups {
		if v == name {
			return settingsForClient(a.settings), nil
		}
	}
	candidate := cloneSettings(a.settings)
	candidate.Groups = append(candidate.Groups, name)
	if err := a.commitSettingsLocked(candidate); err != nil {
		return Settings{}, err
	}
	return settingsForClient(a.settings), nil
}






