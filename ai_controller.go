package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"santianshell/internal/aiagent"
	"santianshell/internal/remote"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

func (a *App) SaveAI(c AIConfig) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	c.Name = strings.TrimSpace(c.Name)
	c.Endpoint = strings.TrimSpace(c.Endpoint)
	c.Model = strings.TrimSpace(c.Model)
	c.Models = normalizeAIModels(c.Models)
	if c.Name == "" || c.Endpoint == "" || c.Model == "" {
		return Settings{}, errors.New("供应商名称、API 地址和模型不能为空")
	}
	c.Mode = strings.ToLower(strings.TrimSpace(c.Mode))
	if !slices.Contains([]string{"suggest", "review", "managed"}, c.Mode) {
		return Settings{}, errors.New("AI 权限无效")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	if c.ID == "" {
		if len(candidate.AIProviders) >= 8 {
			return Settings{}, errors.New("最多只能添加 8 个 AI 供应商")
		}
		c.ID = strconv.FormatInt(time.Now().UnixNano(), 36)
		candidate.AIProviders = append(candidate.AIProviders, c)
	} else {
		found := false
		for i := range candidate.AIProviders {
			if candidate.AIProviders[i].ID == c.ID {
				if len(c.Models) == 0 &&
					candidate.AIProviders[i].Endpoint == c.Endpoint &&
					candidate.AIProviders[i].APIKey == c.APIKey {
					c.Models = candidate.AIProviders[i].Models
				}
				candidate.AIProviders[i] = c
				found = true
				break
			}
		}
		if !found {
			if len(candidate.AIProviders) >= 8 {
				return Settings{}, errors.New("最多只能添加 8 个 AI 供应商")
			}
			candidate.AIProviders = append(candidate.AIProviders, c)
		}
	}
	candidate.CurrentProviderID = c.ID
	if err := a.commitSettingsLocked(candidate); err != nil {
		return Settings{}, err
	}
	return settingsForClient(a.settings), nil
}
func (a *App) SelectAIProvider(id string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, provider := range a.settings.AIProviders {
		if provider.ID == id {
			candidate := cloneSettings(a.settings)
			candidate.CurrentProviderID = id
			return a.commitSettingsLocked(candidate)
		}
	}
	return errors.New("AI 供应商不存在")
}

func (a *App) FetchAIModels(id string) ([]string, error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("AI 供应商不能为空")
	}
	a.mu.RLock()
	var cfg AIConfig
	for _, provider := range a.settings.AIProviders {
		if provider.ID == id {
			cfg = provider
			break
		}
	}
	a.mu.RUnlock()
	if cfg.ID == "" {
		return nil, errors.New("AI 供应商不存在")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	models, err := fetchAIModels(ctx, cfg.Endpoint, cfg.APIKey)
	if err != nil {
		return nil, err
	}

	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.AIProviders {
		provider := &candidate.AIProviders[i]
		if provider.ID != id {
			continue
		}
		if provider.Endpoint != cfg.Endpoint || provider.APIKey != cfg.APIKey {
			return nil, errors.New("AI 供应商配置已更改，请重新获取模型")
		}
		provider.Models = append([]string(nil), models...)
		if err := a.commitSettingsLocked(candidate); err != nil {
			return nil, fmt.Errorf("保存模型缓存失败: %w", err)
		}
		return models, nil
	}
	return nil, errors.New("AI 供应商不存在")
}

// FetchAIModelsByConfig 获取尚未保存的供应商模型，不修改本地配置。
func (a *App) FetchAIModelsByConfig(endpoint, apiKey string) ([]string, error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return fetchAIModels(ctx, endpoint, apiKey)
}

func fetchAIModels(ctx context.Context, endpoint, apiKey string) ([]string, error) {
	endpoint = strings.TrimSpace(endpoint)
	apiKey = strings.TrimSpace(apiKey)
	if endpoint == "" {
		return nil, errors.New("API 地址不能为空")
	}
	if apiKey == "" {
		return nil, errors.New("API Key 不能为空")
	}

	modelsURL, err := aiagent.BuildOpenAIURL(endpoint, "models")
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if err != nil {
		return nil, errors.New("API 地址无效，请填写完整的 HTTP 或 HTTPS 地址")
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, aiModelsRequestError(ctx, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxAIModelsResponseSize+1))
	if err != nil {
		return nil, errors.New("读取模型响应失败")
	}
	if len(body) > maxAIModelsResponseSize {
		return nil, errors.New("模型响应过大，已停止读取")
	}

	var data struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	decodeErr := json.Unmarshal(body, &data)
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		message := safeAIErrorMessage(data.Error.Message, endpoint, apiKey)
		if message == "" {
			message = resp.Status
		}
		return nil, fmt.Errorf("获取模型失败（HTTP %d）：%s", resp.StatusCode, message)
	}
	if decodeErr != nil {
		return nil, errors.New("模型响应格式无效，请确认 API 地址兼容 OpenAI /models 接口")
	}

	models := make([]string, 0, len(data.Data))
	for _, model := range data.Data {
		models = append(models, model.ID)
	}
	models = normalizeAIModels(models)
	if len(models) == 0 {
		return nil, errors.New("供应商未返回可用模型")
	}
	return models, nil
}

func normalizeAIModels(models []string) []string {
	unique := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		unique[model] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for model := range unique {
		result = append(result, model)
	}
	slices.Sort(result)
	return result
}

func aiModelsRequestError(ctx context.Context, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return errors.New("获取模型超时，请检查 API 地址和网络连接")
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return errors.New("无法解析 API 域名，请检查 API 地址和网络连接")
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "certificate") || strings.Contains(message, "tls") {
		return errors.New("API 的 TLS 证书验证失败")
	}
	if strings.Contains(message, "connection refused") {
		return errors.New("AI 供应商拒绝连接，请检查 API 地址和服务状态")
	}
	return errors.New("连接 AI 供应商失败，请检查 API 地址和网络连接")
}

func safeAIErrorMessage(message, endpoint, apiKey string) string {
	message = strings.TrimSpace(message)
	if apiKey != "" {
		message = strings.ReplaceAll(message, apiKey, "[已隐藏]")
	}
	if endpoint != "" {
		message = strings.ReplaceAll(message, endpoint, "[API 地址]")
	}
	message = strings.Join(strings.Fields(message), " ")
	runes := []rune(message)
	if len(runes) > maxAIErrorMessageLength {
		message = string(runes[:maxAIErrorMessageLength]) + "…"
	}
	return message
}

func (a *App) SelectAIModel(id, model string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.AIProviders {
		if candidate.AIProviders[i].ID == id {
			candidate.AIProviders[i].Model = strings.TrimSpace(model)
			candidate.CurrentProviderID = id
			if err := a.commitSettingsLocked(candidate); err != nil {
				return Settings{}, err
			}
			return settingsForClient(a.settings), nil
		}
	}
	return Settings{}, errors.New("AI 供应商不存在")
}

func (a *App) SelectAIPermission(id, permission string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	permission = strings.ToLower(strings.TrimSpace(permission))
	if !slices.Contains([]string{"suggest", "review", "managed"}, permission) {
		return Settings{}, errors.New("AI 权限无效")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.AIProviders {
		if candidate.AIProviders[i].ID == id {
			candidate.AIProviders[i].Mode = permission
			candidate.CurrentProviderID = id
			if err := a.commitSettingsLocked(candidate); err != nil {
				return Settings{}, err
			}
			return settingsForClient(a.settings), nil
		}
	}
	return Settings{}, errors.New("AI 供应商不存在")
}

func (a *App) DeleteAIProvider(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	providers := candidate.AIProviders[:0]
	for _, provider := range candidate.AIProviders {
		if provider.ID != id {
			providers = append(providers, provider)
		}
	}
	candidate.AIProviders = providers
	if candidate.CurrentProviderID == id {
		candidate.CurrentProviderID = ""
		if len(providers) > 0 {
			candidate.CurrentProviderID = providers[0].ID
		}
	}
	if err := a.commitSettingsLocked(candidate); err != nil {
		return Settings{}, err
	}
	return settingsForClient(a.settings), nil
}

func (a *App) StartAIAgent(currentHostID, question string) (string, error) {
	if a.shuttingDown.Load() {
		return "", errors.New("软件正在退出")
	}
	a.mu.RLock()
	if !a.unlocked {
		a.mu.RUnlock()
		return "", errors.New("软件已锁定，请先输入密保")
	}
	var provider AIConfig
	presetPrompt := a.settings.AIPresetPrompt
	for _, item := range a.settings.AIProviders {
		if item.ID == a.settings.CurrentProviderID {
			provider = item
			break
		}
	}
	a.mu.RUnlock()
	if provider.ID == "" {
		return "", errors.New("请先添加并选择 AI 供应商")
	}
	if provider.APIKey == "" {
		return "", errors.New("当前 AI 供应商未配置 API Key")
	}
	hosts, revisions, connectionHosts := a.aiHostSnapshots()
	if len(hosts) == 0 {
		return "", errors.New("没有可供 AI 操作的主机")
	}
	env := aiagent.Environment{
		ListHosts: func() []aiagent.Host {
			result := append([]aiagent.Host(nil), hosts...)
			a.mu.RLock()
			defer a.mu.RUnlock()
			for i := range result {
				result[i].Connected = a.shells[result[i].ID] != nil
			}
			return result
		},
		ReadOSRelease: func(ctx context.Context, hostID string) (string, error) {
			host, exists := connectionHosts[hostID]
			if !exists {
				return "", errors.New("目标主机不在本次 AI 任务快照中")
			}
			return a.readAIAgentOSRelease(ctx, host, revisions[hostID], 0, false)
		},
		RunCommand: func(ctx context.Context, hostID, command string, timeout time.Duration, onOutput func(string)) (aiagent.CommandResult, error) {
			host, exists := connectionHosts[hostID]
			if !exists {
				return aiagent.CommandResult{}, errors.New("目标主机不在本次 AI 任务快照中")
			}
			return a.runAIAgentCommand(ctx, host, revisions[hostID], command, timeout, onOutput)
		},
		TransferPath: func(ctx context.Context, sourceHostID, sourcePath, targetHostID, targetPath string, overwrite bool, _ time.Duration, onProgress func(aiagent.TransferResult)) (aiagent.TransferResult, error) {
			sourceHost, sourceExists := connectionHosts[sourceHostID]
			targetHost, targetExists := connectionHosts[targetHostID]
			if !sourceExists || !targetExists {
				return aiagent.TransferResult{}, errors.New("源主机或目标主机不在本次 AI 任务快照中")
			}
			return a.runAIAgentTransfer(ctx, sourceHost, revisions[sourceHostID], sourcePath, targetHost, revisions[targetHostID], targetPath, overwrite, onProgress)
		},
		Emit: func(event aiagent.Event) {
			if a.ctx != nil {
				runtime2.EventsEmit(a.ctx, "ai-agent-event", event)
			}
		},
	}
	if a.shuttingDown.Load() {
		return "", errors.New("软件正在退出")
	}
	return a.aiAgent.Start(a.lifecycleContext(), aiagent.Config{Endpoint: provider.Endpoint, APIKey: provider.APIKey, Model: provider.Model, Mode: provider.Mode, PresetPrompt: presetPrompt}, currentHostID, question, env)
}

const aiHostOSReleaseCommand = `if [ -r /etc/os-release ]; then cat /etc/os-release; fi
printf '\nSANTIANSHELL_PACKAGE_MANAGER='
if command -v apt-get >/dev/null 2>&1; then printf 'apt\n'
elif command -v apk >/dev/null 2>&1; then printf 'apk\n'
elif command -v dnf >/dev/null 2>&1; then printf 'dnf\n'
elif command -v yum >/dev/null 2>&1; then printf 'yum\n'
elif command -v zypper >/dev/null 2>&1; then printf 'zypper\n'
elif command -v pacman >/dev/null 2>&1; then printf 'pacman\n'
else printf 'unknown\n'
fi`

func (a *App) preloadHostOSRelease(hostID string, host remote.Host, revision string, epoch uint64) {
	ctx, cancel := context.WithTimeout(a.lifecycleContext(), 4*time.Second)
	defer cancel()
	if _, err := a.readAIAgentOSRelease(ctx, host, revision, epoch, true); err != nil && ctx.Err() == nil {
		slog.Debug("后台识别主机系统失败", "主机ID", hostID, "错误", err)
	}
}

func (a *App) readAIAgentOSRelease(ctx context.Context, host remote.Host, expectedRevision string, expectedEpoch uint64, refresh bool) (string, error) {
	a.mu.RLock()
	currentRevision := a.aiCurrentHostRevisionLocked(host.ID)
	cached := a.hostOSReleases[host.ID]
	epochMatches := expectedEpoch == 0 || a.shellEpochs[host.ID] == expectedEpoch
	a.mu.RUnlock()
	if currentRevision == "" {
		return "", errors.New("目标主机已不存在")
	}
	if currentRevision != expectedRevision || !epochMatches {
		return "", errors.New("主机连接信息已变化")
	}
	if cached != "" && !refresh {
		return cached, nil
	}

	client, release, err := a.connections.AcquireContext(ctx, host.ID, host)
	if err != nil {
		return "", err
	}
	defer release()
	result, err := remote.RunWithClientContext(ctx, client, aiHostOSReleaseCommand)
	if err != nil {
		return "", err
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("系统信息采集失败，退出码 %d", result.ExitCode)
	}
	output := strings.TrimSpace(result.Output)
	if output == "" {
		return "", errors.New("系统信息采集结果为空")
	}

	a.mu.Lock()
	currentRevision = a.aiCurrentHostRevisionLocked(host.ID)
	epochMatches = expectedEpoch == 0 || a.shellEpochs[host.ID] == expectedEpoch
	if currentRevision == expectedRevision && epochMatches {
		if a.hostOSReleases == nil {
			a.hostOSReleases = make(map[string]string)
		}
		a.hostOSReleases[host.ID] = output
	}
	a.mu.Unlock()
	if currentRevision != expectedRevision || !epochMatches {
		return "", errors.New("主机连接信息在系统采集期间发生变化")
	}
	return output, nil
}

func (a *App) runAIAgentTransfer(ctx context.Context, sourceHost remote.Host, sourceRevision, sourcePath string, targetHost remote.Host, targetRevision, targetPath string, overwrite bool, onProgress func(aiagent.TransferResult)) (aiagent.TransferResult, error) {
	if sourceHost.ID == targetHost.ID {
		return aiagent.TransferResult{}, errors.New("源主机和目标主机不能相同")
	}
	if sameSSHServer(sourceHost, targetHost) {
		return aiagent.TransferResult{}, errors.New("源主机和目标主机指向同一台 SSH 服务器，请改用服务器内复制命令")
	}
	a.mu.RLock()
	sourceCurrent := a.aiCurrentHostRevisionLocked(sourceHost.ID)
	targetCurrent := a.aiCurrentHostRevisionLocked(targetHost.ID)
	sourceShell := a.shells[sourceHost.ID]
	targetShell := a.shells[targetHost.ID]
	sourceShellRevision := a.shellHostRevisions[sourceHost.ID]
	targetShellRevision := a.shellHostRevisions[targetHost.ID]
	a.mu.RUnlock()
	if sourceCurrent == "" || targetCurrent == "" {
		return aiagent.TransferResult{}, errors.New("源主机或目标主机已不存在")
	}
	if sourceCurrent != sourceRevision || targetCurrent != targetRevision {
		return aiagent.TransferResult{}, errors.New("主机连接信息已在任务期间发生变化，已阻止文件传输")
	}
	if sourceShell != nil && sourceShellRevision != sourceRevision {
		return aiagent.TransferResult{}, errors.New("源主机 SSH 窗口使用的是旧连接配置，请先重连")
	}
	if targetShell != nil && targetShellRevision != targetRevision {
		return aiagent.TransferResult{}, errors.New("目标主机 SSH 窗口使用的是旧连接配置，请先重连")
	}
	if sourceShell == nil && strings.TrimSpace(sourceHost.HostKey) == "" {
		return aiagent.TransferResult{}, errors.New("源主机待首次连接确认指纹")
	}
	if targetShell == nil && strings.TrimSpace(targetHost.HostKey) == "" {
		return aiagent.TransferResult{}, errors.New("目标主机待首次连接确认指纹")
	}

	sourceClient, releaseSource, err := a.connections.AcquireContext(ctx, sourceHost.ID, sourceHost)
	if err != nil {
		return aiagent.TransferResult{}, fmt.Errorf("连接源主机失败: %w", err)
	}
	defer releaseSource()
	targetClient, releaseTarget, err := a.connections.AcquireContext(ctx, targetHost.ID, targetHost)
	if err != nil {
		return aiagent.TransferResult{}, fmt.Errorf("连接目标主机失败: %w", err)
	}
	defer releaseTarget()

	a.mu.RLock()
	sourceLatest := a.aiCurrentHostRevisionLocked(sourceHost.ID)
	targetLatest := a.aiCurrentHostRevisionLocked(targetHost.ID)
	a.mu.RUnlock()
	if sourceLatest != sourceRevision || targetLatest != targetRevision {
		a.connections.Invalidate(sourceHost.ID)
		a.connections.Invalidate(targetHost.ID)
		return aiagent.TransferResult{}, errors.New("主机连接信息已在连接期间发生变化，已阻止文件传输")
	}

	started := time.Now()
	stats, err := remote.TransferBetweenClients(ctx, sourceClient, targetClient, sourcePath, targetPath, overwrite, func(progress remote.TransferStats) {
		if onProgress != nil {
			onProgress(aiagent.TransferResult{Files: progress.Files, Directories: progress.Directories, Bytes: progress.Bytes, Duration: time.Since(started).Milliseconds()})
		}
	})
	result := aiagent.TransferResult{Files: stats.Files, Directories: stats.Directories, Bytes: stats.Bytes, Duration: time.Since(started).Milliseconds()}
	return result, err
}

func sameSSHServer(first, second remote.Host) bool {
	firstAddress := strings.TrimSpace(first.Address)
	secondAddress := strings.TrimSpace(second.Address)
	if firstAddress != "" && strings.EqualFold(firstAddress, secondAddress) && equalSSHPort(first.Port, second.Port) {
		return true
	}
	firstKey := strings.TrimSpace(first.HostKey)
	secondKey := strings.TrimSpace(second.HostKey)
	return firstKey != "" && firstKey == secondKey
}

func (a *App) ApproveAIAgent(runID, actionID string, approved bool) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	return a.aiAgent.Approve(runID, actionID, approved)
}

func (a *App) AnswerAIAgent(runID, actionID, answer string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	return a.aiAgent.Answer(runID, actionID, answer)
}

func (a *App) StopAIAgent(runID string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	return a.aiAgent.Stop(runID)
}

func (a *App) ClearAIAgentChat(hostID string) {
	if a.requireUnlocked() != nil || a.aiAgent == nil {
		return
	}
	a.aiAgent.ClearConversation(hostID)
}

func (a *App) aiHostSnapshots() ([]aiagent.Host, map[string]string, map[string]remote.Host) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	hosts := make([]aiagent.Host, 0, len(a.settings.Hosts))
	revisions := make(map[string]string, len(a.settings.Hosts))
	connectionHosts := make(map[string]remote.Host, len(a.settings.Hosts))
	for _, stored := range a.settings.Hosts {
		if stored.Deleted {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(stored.Protocol))
		if protocol == "" {
			protocol = "ssh"
		}
		resolved := a.wireSSHCallbacks(a.resolveCredential(stored), true)
		hosts = append(hosts, aiagent.Host{ID: stored.ID, Name: stored.Name, Address: stored.Address, Alias: stored.VPSAlias, Group: stored.Group, Country: stored.Country, Protocol: protocol, Username: stored.Username, OSRelease: a.hostOSReleases[stored.ID], Connected: a.shells[stored.ID] != nil, Trusted: stored.HostKey != ""})
		revisions[stored.ID] = aiHostRevision(resolved)
		connectionHosts[stored.ID] = resolved
	}
	for _, temporary := range a.temporaryHosts {
		if temporary.Deleted {
			continue
		}
		protocol := strings.ToLower(strings.TrimSpace(temporary.Protocol))
		if protocol == "" {
			protocol = "ssh"
		}
		resolved := a.wireSSHCallbacks(a.resolveCredential(temporary), false)
		hosts = append(hosts, aiagent.Host{ID: temporary.ID, Name: temporary.Name, Address: temporary.Address, Alias: temporary.VPSAlias, Group: temporary.Group, Country: temporary.Country, Protocol: protocol, Username: temporary.Username, OSRelease: a.hostOSReleases[temporary.ID], Connected: a.shells[temporary.ID] != nil, Trusted: temporary.HostKey != "" || a.shells[temporary.ID] != nil})
		revisions[temporary.ID] = aiHostRevision(resolved)
		connectionHosts[temporary.ID] = resolved
	}
	return hosts, revisions, connectionHosts
}

func (a *App) aiCurrentHostRevisionLocked(id string) string {
	for _, stored := range a.settings.Hosts {
		if stored.ID == id && !stored.Deleted {
			return aiHostRevision(a.resolveCredential(stored))
		}
	}
	if temporary, exists := a.temporaryHosts[id]; exists && !temporary.Deleted {
		return aiHostRevision(a.resolveCredential(temporary))
	}
	return ""
}

func aiHostRevision(host remote.Host) string {
	value := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s\x00%d\x00%s\x00%s\x00%t\x00%s\x00%s",
		host.ID, host.Address, host.Port, host.Protocol, host.Username, host.AuthType, host.Password,
		host.KeyPath, host.KeyContent, host.KeyPassphrase, host.ProxyEnabled, host.ProxyHost,
		host.ProxyPort, host.ProxyUsername, host.ProxyPassword, host.Deleted,
		host.SSHJumpHostID, host.JumpFingerprint)
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (a *App) runAIAgentCommand(ctx context.Context, host remote.Host, expectedRevision, command string, timeout time.Duration, onOutput func(string)) (aiagent.CommandResult, error) {
	hostID := host.ID
	a.mu.RLock()
	shell := a.shells[hostID]
	shellRevision := a.shellHostRevisions[hostID]
	currentRevision := a.aiCurrentHostRevisionLocked(hostID)
	a.mu.RUnlock()
	if currentRevision == "" {
		return aiagent.CommandResult{}, errors.New("目标主机已不存在")
	}
	if currentRevision != expectedRevision {
		return aiagent.CommandResult{}, errors.New("主机连接信息已在任务期间发生变化，已阻止继续执行")
	}
	if shell != nil && shellRevision != expectedRevision {
		return aiagent.CommandResult{}, errors.New("当前 SSH 窗口使用的是旧连接配置，请先重连后再让 AI 操作")
	}
	if shell == nil && strings.TrimSpace(host.HostKey) == "" {
		return aiagent.CommandResult{}, errors.New("待首次连接确认指纹，请先手动连接、测试连接或刷新状态")
	}
	if shell == nil {
		if err := a.ensureSSHJumpChainTrusted(host, false); err != nil {
			return aiagent.CommandResult{}, err
		}
	}
	streamOutput := func(output string) {
		if onOutput != nil {
			onOutput(output)
		}
	}
	var result remote.Result
	var err error
	if shell != nil {
		result, err = shell.RunInteractiveRealPTYContext(ctx, command, onOutput)
		if errors.Is(err, remote.ErrInteractiveShellBusy) {
			err = errors.New("当前终端未处于空闲命令提示符，请先结束当前程序后再让 AI 操作")
		}
	} else {
		client, release, acquireErr := a.connections.AcquireContext(ctx, hostID, host)
		if acquireErr != nil {
			return aiagent.CommandResult{}, acquireErr
		}
		defer release()
		a.mu.RLock()
		latestRevision := a.aiCurrentHostRevisionLocked(hostID)
		a.mu.RUnlock()
		if latestRevision != expectedRevision {
			a.connections.Invalidate(hostID)
			return aiagent.CommandResult{}, errors.New("主机连接信息已在连接期间发生变化，已阻止执行命令")
		}
		result, err = remote.RunWithClientContextStreaming(ctx, client, command, streamOutput)
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		err = nil
	}
	return aiagent.CommandResult{Output: result.Output, ExitCode: result.ExitCode, Duration: result.Duration}, err
}
