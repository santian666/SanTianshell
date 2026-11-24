package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"santianshell/internal/remote"
	"santianshell/internal/sshagent"
	"santianshell/internal/sshprompt"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
	"golang.org/x/crypto/ssh"
)

func (a *App) ReplySSHInteraction(requestID string, accepted bool, answers []string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	requestID = strings.TrimSpace(requestID)
	if requestID == "" {
		return errors.New("SSH 交互请求 ID 不能为空")
	}
	if len(answers) > 20 {
		return errors.New("SSH 交互回答数量过多")
	}
	clean := make([]string, len(answers))
	for i, answer := range answers {
		if len(answer) > 4096 {
			return errors.New("SSH 交互回答过长")
		}
		clean[i] = answer
	}
	if !a.sshPrompts.Resolve(requestID, sshprompt.Response{Accepted: accepted, Answers: clean}) {
		return errors.New("SSH 交互请求已失效")
	}
	return nil
}

func (a *App) askSSHInteraction(request sshprompt.Request) (sshprompt.Response, error) {
	if a.ctx == nil {
		return sshprompt.Response{}, errors.New("SSH 交互界面尚未就绪")
	}
	timeout := 5 * time.Minute
	if request.Kind == "password-retry" {
		timeout = 30 * time.Minute
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return a.sshPrompts.Ask(ctx, request, func(current sshprompt.Request) {
		runtime2.EventsEmit(a.ctx, "ssh-interaction", current)
	})
}

func (a *App) wireSSHCallbacks(h remote.Host, persistHostKey bool) remote.Host {
	h.MFACode = ""
	if h.AuthType == "agent" || h.AuthType == "agent_mfa" {
		h.AgentSigners = func(ctx context.Context) ([]ssh.Signer, io.Closer, error) {
			session, err := sshagent.Open(ctx, sshagent.DefaultMaxSigners)
			if err != nil {
				return nil, nil, err
			}
			return session.Signers(), session, nil
		}
	}
	hostID, hostName, address, port := h.ID, h.Name, h.Address, h.Port
	if port == 0 {
		port = 22
	}
	if (h.AuthType == "key" || h.AuthType == "key_mfa") && h.KeyPassphrase == "" {
		h.KeyPassphrasePrompt = func() (string, error) {
			response, err := a.askSSHInteraction(sshprompt.Request{
				Kind: "key-passphrase", HostID: hostID, HostName: hostName,
				Address: net.JoinHostPort(address, strconv.Itoa(port)),
			})
			if err != nil {
				return "", fmt.Errorf("等待私钥口令输入失败: %w", err)
			}
			if !response.Accepted || len(response.Answers) != 1 {
				return "", errors.New("用户取消了私钥口令输入")
			}
			return response.Answers[0], nil
		}
	}
	if h.Temporary && hostID != "" {
		h.RememberHostKey = func(fingerprint string) error {
			return a.rememberTemporaryHostKey(hostID, address, port, fingerprint)
		}
	} else if persistHostKey && hostID != "" {
		h.RememberHostKey = func(fingerprint string) error {
			return a.rememberHostKey(hostID, address, port, fingerprint)
		}
	} else {
		h.RememberHostKey = nil
	}
	h.ConfirmHostKey = func(fingerprint string) error {
		response, err := a.askSSHInteraction(sshprompt.Request{
			Kind: "hostkey", HostID: hostID, HostName: hostName,
			Address: net.JoinHostPort(address, strconv.Itoa(port)), Fingerprint: fingerprint,
		})
		if err != nil {
			return fmt.Errorf("等待主机指纹确认失败: %w", err)
		}
		if !response.Accepted {
			return errors.New("用户拒绝信任主机指纹")
		}
		return nil
	}
	password := h.Password
	h.KeyboardInteractive = func(user, instruction string, questions []string, echos []bool) ([]string, error) {
		answers := make([]string, len(questions))
		dynamicIndexes := make([]int, 0, len(questions))
		dynamicQuestions := make([]sshprompt.Question, 0, len(questions))
		for i, question := range questions {
			if password != "" && storedPasswordQuestion(question) {
				answers[i] = password
				continue
			}
			echo := i < len(echos) && echos[i]
			dynamicIndexes = append(dynamicIndexes, i)
			dynamicQuestions = append(dynamicQuestions, sshprompt.Question{Text: question, Echo: echo})
		}
		if len(dynamicQuestions) == 0 {
			return answers, nil
		}
		response, err := a.askSSHInteraction(sshprompt.Request{
			Kind: "keyboard-interactive", HostID: hostID, HostName: hostName,
			Address: net.JoinHostPort(address, strconv.Itoa(port)), Name: user,
			Instruction: instruction, Questions: dynamicQuestions,
		})
		if err != nil {
			return nil, fmt.Errorf("等待 MFA 输入失败: %w", err)
		}
		if !response.Accepted {
			return nil, errors.New("用户取消了 MFA 输入")
		}
		if len(response.Answers) != len(dynamicIndexes) {
			return nil, errors.New("MFA 回答数量不匹配")
		}
		for i, target := range dynamicIndexes {
			answers[target] = response.Answers[i]
		}
		return answers, nil
	}
	h.ConnectionProgress = func(stage remote.ConnectionStage) {
		slog.Info("SSH 连接阶段", "主机ID", hostID, "主机", hostName, "地址", address, "阶段", stage)
		if a.ctx != nil {
			runtime2.EventsEmit(a.ctx, "ssh-diagnostic-stage", map[string]any{"hostId": hostID, "address": address, "stage": stage})
		}
	}
	h.ConnectionRetry = func(attempt, maximum int, delay time.Duration, err error) {
		slog.Warn("SSH 连接准备重试", "主机ID", hostID, "主机", hostName, "地址", address, "次数", attempt, "最大次数", maximum, "等待", delay, "错误", err)
	}
	return a.wireSSHJumpDial(h, persistHostKey, true)
}

func storedPasswordQuestion(question string) bool {
	return remote.IsPasswordPromptQuestion(question)
}

func logSSHFailure(operation string, h remote.Host, err error) {
	if err == nil {
		return
	}
	stage := ""
	raw := err
	var connectionErr *remote.ConnectionError
	if errors.As(err, &connectionErr) {
		stage = string(connectionErr.Stage)
		if connectionErr.Err != nil {
			raw = connectionErr.Err
		}
	}
	slog.Error("SSH 操作失败", "操作", operation, "主机ID", h.ID, "地址", h.Address, "阶段", stage, "原始错误", raw)
}

func (a *App) host(id string) (remote.Host, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Host{}, err
	}
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, h := range a.settings.Hosts {
		if h.ID == id {
			return a.wireSSHCallbacks(a.resolveCredential(h), true), nil
		}
	}
	if h, exists := a.temporaryHosts[id]; exists {
		return a.wireSSHCallbacks(a.resolveCredential(h), false), nil
	}
	return remote.Host{}, errors.New("主机不存在")
}

func equalSSHPort(left, right int) bool {
	if left == 0 {
		left = 22
	}
	if right == 0 {
		right = 22
	}
	return left == right
}

func (a *App) rememberHostKey(id, address string, port int, fingerprint string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		host := &candidate.Hosts[i]
		if host.ID != id {
			continue
		}
		if host.Address != address || !equalSSHPort(host.Port, port) {
			return errors.New("主机连接信息已变化")
		}
		if host.HostKey != "" {
			if host.HostKey != fingerprint {
				return errors.New("SSH 主机密钥与已保存记录不一致")
			}
			return nil
		}
		host.HostKey = fingerprint
		return a.commitSettingsLocked(candidate)
	}
	return errors.New("主机不存在")
}

func (a *App) rememberTemporaryHostKey(id, address string, port int, fingerprint string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	host, exists := a.temporaryHosts[id]
	if !exists {
		return errors.New("临时主机已关闭")
	}
	if host.Address != address || !equalSSHPort(host.Port, port) {
		return errors.New("临时主机连接信息已变化")
	}
	if host.HostKey != "" && host.HostKey != fingerprint {
		return errors.New("SSH 主机密钥与本次临时会话记录不一致")
	}
	host.HostKey = fingerprint
	a.temporaryHosts[id] = host
	return nil
}

func (a *App) ResetHostKey(id string) (Settings, error) {
	if err := a.requireUnlocked(); err != nil {
		return Settings{}, err
	}
	a.mu.Lock()
	if a.shells[id] != nil {
		a.mu.Unlock()
		return Settings{}, errors.New("请先断开该 SSH 会话，再重新信任主机密钥")
	}
	candidate := cloneSettings(a.settings)
	for i := range candidate.Hosts {
		if candidate.Hosts[i].ID != id {
			continue
		}
		candidate.Hosts[i].HostKey = ""
		if err := a.commitSettingsLocked(candidate); err != nil {
			a.mu.Unlock()
			return Settings{}, err
		}
		settings := settingsForClient(a.settings)
		a.mu.Unlock()
		if a.aiAgent != nil {
			a.aiAgent.Close()
		}
		a.connections.Invalidate(id)
		a.invalidateSSHJumpDependents(id)
		return settings, nil
	}
	a.mu.Unlock()
	return Settings{}, errors.New("主机不存在")
}

func (a *App) acquireNodeSSH(hostID, alias string, timeoutSeconds int) (*ssh.Client, func(), error) {
	return a.acquireNodeSSHWithTrust(hostID, alias, timeoutSeconds, false)
}

func (a *App) acquireNodeSSHInteractive(hostID, alias string, timeoutSeconds int) (*ssh.Client, func(), error) {
	return a.acquireNodeSSHWithTrust(hostID, alias, timeoutSeconds, true)
}

func (a *App) acquireNodeSSHWithTrust(hostID, alias string, timeoutSeconds int, allowHostKeyPrompt bool) (*ssh.Client, func(), error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, nil, err
	}
	host, err := a.nodeSSHHost(hostID, alias)
	if err != nil {
		return nil, nil, err
	}
	if !allowHostKeyPrompt && strings.TrimSpace(host.HostKey) == "" {
		return nil, nil, errors.New("待首次连接确认指纹，请先手动连接、测试连接或刷新状态")
	}
	if err := a.ensureSSHJumpChainTrusted(host, allowHostKeyPrompt); err != nil {
		return nil, nil, err
	}
	if timeoutSeconds > host.Timeout {
		host.Timeout = timeoutSeconds
	}
	host = a.wireSSHCallbacks(host, true)
	return a.connections.AcquireContext(a.lifecycleContext(), host.ID, host)
}

func (a *App) nodeSSHHost(hostID, alias string) (remote.Host, error) {
	hostID = strings.TrimSpace(hostID)
	alias = strings.TrimSpace(alias)
	a.mu.RLock()
	defer a.mu.RUnlock()
	if hostID != "" {
		for _, item := range a.settings.Hosts {
			if item.ID == hostID && !item.Deleted {
				if !isSSHHostProtocol(item.Protocol) {
					return remote.Host{}, errors.New("节点绑定的主机不是 SSH 主机")
				}
				return a.resolveCredential(item), nil
			}
		}
		return remote.Host{}, fmt.Errorf("节点绑定的主机不存在：%s", hostID)
	}
	if alias == "" {
		return remote.Host{}, errors.New("节点主机绑定信息为空")
	}
	var host remote.Host
	matchCount := 0
	for _, item := range a.settings.Hosts {
		if item.Deleted || !isSSHHostProtocol(item.Protocol) || !strings.EqualFold(strings.TrimSpace(item.VPSAlias), alias) {
			continue
		}
		host = item
		matchCount++
	}
	if matchCount == 0 {
		return remote.Host{}, fmt.Errorf("节点主机不存在：%s", alias)
	}
	if matchCount > 1 {
		return remote.Host{}, fmt.Errorf("节点别名 %q 对应多个主机，请重新保存主机以完成绑定", alias)
	}
	return a.resolveCredential(host), nil
}

func (a *App) resolveNodePassword(hostID, alias string) string {
	if a.requireUnlocked() != nil {
		return ""
	}
	host, err := a.nodeSSHHost(hostID, alias)
	if err != nil {
		return ""
	}
	return host.Password
}

func (a *App) acquireHostSSH(id string) (*ssh.Client, func(), error) {
	host, err := a.host(id)
	if err != nil {
		return nil, nil, err
	}
	return a.connections.AcquireContext(a.lifecycleContext(), id, host)
}

const maxSSHJumpDepth = 4

type jumpTunnelConn struct {
	net.Conn
	release   func()
	closeOnce sync.Once
}

func (c *jumpTunnelConn) Close() error {
	err := c.Conn.Close()
	c.closeOnce.Do(func() {
		if c.release != nil {
			c.release()
		}
	})
	return err
}

func (a *App) resolveCredential(h remote.Host) remote.Host {
	h = a.resolveStoredCredentialOnlyLocked(h)
	if password, exists := a.transientPasswords[h.ID]; exists && (h.AuthType == "" || h.AuthType == "password") {
		h.Password = password
	}
	h.JumpFingerprint = a.sshJumpFingerprintLocked(h, true)
	return h
}

func (a *App) resolveStoredCredential(h remote.Host) remote.Host {
	h = a.resolveStoredCredentialOnlyLocked(h)
	h.JumpFingerprint = a.sshJumpFingerprintLocked(h, false)
	return h
}

func (a *App) resolveStoredCredentialOnlyLocked(h remote.Host) remote.Host {
	if h.CredentialID != "" {
		for _, c := range a.settings.Credentials {
			if c.ID != h.CredentialID {
				continue
			}
			if c.Type == "password" {
				if !strings.HasPrefix(h.AuthType, "key") {
					h.Password = c.Password
				}
			} else if strings.HasPrefix(h.AuthType, "key") {
				h.KeyContent = c.KeyContent
				h.KeyPassphrase = c.KeyPassphrase
			}
			break
		}
	}
	return h
}

func (a *App) sshJumpFingerprintLocked(h remote.Host, includeTransient bool) string {
	jumpID := strings.TrimSpace(h.SSHJumpHostID)
	if jumpID == "" {
		return ""
	}
	seen := make(map[string]struct{})
	if id := strings.TrimSpace(h.ID); id != "" {
		seen[id] = struct{}{}
	}
	var parts []string
	for depth := 0; jumpID != ""; depth++ {
		if depth >= maxSSHJumpDepth {
			parts = append(parts, "depth-exceeded:"+jumpID)
			break
		}
		if _, visited := seen[jumpID]; visited {
			parts = append(parts, "cycle:"+jumpID)
			break
		}
		seen[jumpID] = struct{}{}
		var found *remote.Host
		for i := range a.settings.Hosts {
			candidate := &a.settings.Hosts[i]
			if candidate.ID == jumpID && !candidate.Deleted {
				found = candidate
				break
			}
		}
		if found == nil {
			parts = append(parts, "missing:"+jumpID)
			break
		}
		resolved := a.resolveStoredCredentialOnlyLocked(*found)
		if includeTransient {
			if password, exists := a.transientPasswords[resolved.ID]; exists && (resolved.AuthType == "" || resolved.AuthType == "password") {
				resolved.Password = password
			}
		}
		parts = append(parts, fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s\x00%d\x00%s\x00%s",
			resolved.ID, resolved.Address, resolved.Port, resolved.Username, resolved.AuthType,
			resolved.Password, resolved.KeyPath, resolved.KeyContent, resolved.KeyPassphrase,
			resolved.ProxyEnabled, resolved.ProxyHost, resolved.ProxyPort, resolved.ProxyUsername, resolved.ProxyPassword))
		jumpID = strings.TrimSpace(resolved.SSHJumpHostID)
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x01")))
	return hex.EncodeToString(sum[:])
}

func (a *App) resolveSSHJumpChain(targetID, jumpHostID string) ([]remote.Host, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	jumpHostID = strings.TrimSpace(jumpHostID)
	if jumpHostID == "" {
		return nil, nil
	}
	seen := make(map[string]struct{})
	if id := strings.TrimSpace(targetID); id != "" {
		seen[id] = struct{}{}
	}
	var reverseChain []remote.Host
	for jumpHostID != "" {
		if len(reverseChain) >= maxSSHJumpDepth {
			return nil, fmt.Errorf("SSH 跳板链深度不能超过 %d 级", maxSSHJumpDepth)
		}
		if _, visited := seen[jumpHostID]; visited {
			return nil, errors.New("SSH 跳板链存在循环引用")
		}
		seen[jumpHostID] = struct{}{}
		var found *remote.Host
		for i := range a.settings.Hosts {
			candidate := &a.settings.Hosts[i]
			if candidate.ID == jumpHostID && !candidate.Deleted {
				found = candidate
				break
			}
		}
		if found == nil {
			return nil, fmt.Errorf("SSH 跳板主机不存在或已删除：%s", jumpHostID)
		}
		if !isSSHHostProtocol(found.Protocol) {
			return nil, fmt.Errorf("SSH 跳板主机必须是 SSH 协议：%s", found.Name)
		}
		resolved := a.resolveCredential(*found)
		reverseChain = append(reverseChain, resolved)
		jumpHostID = strings.TrimSpace(resolved.SSHJumpHostID)
	}
	chain := make([]remote.Host, len(reverseChain))
	for i := range reverseChain {
		chain[i] = reverseChain[len(reverseChain)-1-i]
	}
	return chain, nil
}

func (a *App) ensureSSHJumpChainTrusted(h remote.Host, allowHostKeyPrompt bool) error {
	if allowHostKeyPrompt || strings.TrimSpace(h.SSHJumpHostID) == "" {
		return nil
	}
	chain, err := a.resolveSSHJumpChain(h.ID, h.SSHJumpHostID)
	if err != nil {
		return err
	}
	for _, hop := range chain {
		if strings.TrimSpace(hop.HostKey) == "" {
			name := strings.TrimSpace(hop.Name)
			if name == "" {
				name = hop.Address
			}
			return fmt.Errorf("跳板主机 %s 待首次连接确认指纹，请先手动连接或测试连接", name)
		}
	}
	return nil
}

func (a *App) wireSSHJumpDial(h remote.Host, persistHostKey, interactive bool) remote.Host {
	jumpHostID := strings.TrimSpace(h.SSHJumpHostID)
	if jumpHostID == "" {
		h.JumpDial = nil
		return h
	}
	targetID := h.ID
	h.JumpDial = func(ctx context.Context, address string, timeout time.Duration) (net.Conn, error) {
		return a.dialSSHJumpChain(ctx, targetID, jumpHostID, address, timeout, persistHostKey, interactive)
	}
	return h
}

func (a *App) dialSSHJumpChain(ctx context.Context, targetID, jumpHostID, address string, timeout time.Duration, persistHostKey, interactive bool) (net.Conn, error) {
	chain, err := a.resolveSSHJumpChain(targetID, jumpHostID)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, errors.New("未找到可用的 SSH 跳板主机")
	}
	parent := chain[len(chain)-1]
	if interactive {
		parent = a.wireSSHCallbacks(parent, persistHostKey)
	} else {
		parent = a.wireSSHJumpDial(parent, false, false)
	}
	jumpClient, release, err := a.connections.AcquireContext(ctx, parent.ID, parent)
	if err != nil {
		return nil, err
	}
	dialCtx := ctx
	var cancel context.CancelFunc
	if timeout > 0 {
		dialCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	conn, err := jumpClient.DialContext(dialCtx, "tcp", address)
	if err != nil {
		release()
		return nil, err
	}
	return &jumpTunnelConn{Conn: conn, release: release}, nil
}

func (a *App) StartTerminal(id string, clientEpoch int) error {
	h, err := a.host(id)
	if err != nil {
		return err
	}
	return a.startTerminalWithHost(id, clientEpoch, h)
}

func (a *App) StartTerminalInteractive(id string, clientEpoch int, allowPasswordPrompt bool) TerminalStartResult {
	return a.StartTerminalInteractiveSized(id, clientEpoch, allowPasswordPrompt, 0, 0)
}

func (a *App) StartTerminalInteractiveSized(id string, clientEpoch int, allowPasswordPrompt bool, rows, cols int) TerminalStartResult {
	h, err := a.host(id)
	if err != nil {
		return terminalStartFailure(err, false)
	}
	h = cacheConfirmedHostKeyForRetry(h)
	var retryEpoch uint64
	firstErr := a.startTerminalWithHostGuardedSized(id, clientEpoch, h, rows, cols, 0, &retryEpoch)
	if firstErr == nil {
		return TerminalStartResult{Connected: true}
	}
	if !allowPasswordPrompt || !ordinaryPasswordHost(h) || !remote.IsPasswordAuthenticationFailure(firstErr) {
		return terminalStartFailure(firstErr, false)
	}
	details := a.passwordRetryDetails(id)
	response, promptErr := a.askSSHInteraction(sshprompt.Request{
		Kind:           "password-retry",
		HostID:         id,
		HostName:       h.Name,
		Address:        net.JoinHostPort(h.Address, strconv.Itoa(normalizedSSHPort(h.Port))),
		Instruction:    "服务器拒绝密码认证，可能是用户名或密码错误，也可能是服务器已禁用密码登录。",
		Error:          firstErr.Error(),
		AllowPermanent: details.allowPermanent,
		CredentialName: details.credentialName,
		AffectedHosts:  details.affectedHosts,
	})
	if promptErr != nil {
		result := terminalStartFailure(firstErr, true)
		result.Warning = "无法打开密码输入框：" + promptErr.Error()
		return result
	}
	if !response.Accepted || len(response.Answers) == 0 || response.Answers[0] == "" {
		return terminalStartFailure(firstErr, true)
	}
	password := response.Answers[0]
	scope := "once"
	if len(response.Answers) > 1 {
		scope = strings.ToLower(strings.TrimSpace(response.Answers[1]))
	}
	if scope != "permanent" || !details.allowPermanent {
		scope = "once"
	}
	if err := a.setTransientPassword(id, h, password); err != nil {
		result := terminalStartFailure(firstErr, true)
		result.Warning = err.Error()
		return result
	}
	retryHost := h
	retryHost.Password = password
	a.connections.Invalidate(id)
	if retryErr := a.startTerminalWithHostGuardedSized(id, clientEpoch, retryHost, rows, cols, retryEpoch, nil); retryErr != nil {
		a.clearTransientPassword(id)
		return terminalStartFailure(retryErr, true)
	}
	result := TerminalStartResult{Connected: true, PasswordPrompted: true}
	if scope == "permanent" {
		if err := a.persistSuccessfulTerminalPassword(id, retryHost, password); err != nil {
			result.Warning = "新密码已用于本次会话，但永久保存失败：" + err.Error()
		} else {
			result.PasswordSaved = true
		}
	}
	return result
}

func (a *App) startTerminalWithHost(id string, clientEpoch int, h remote.Host) error {
	return a.startTerminalWithHostGuardedSized(id, clientEpoch, h, 0, 0, 0, nil)
}

func (a *App) startTerminalWithHostGuarded(id string, clientEpoch int, h remote.Host, expectedEpoch uint64, startedEpoch *uint64) error {
	return a.startTerminalWithHostGuardedSized(id, clientEpoch, h, 0, 0, expectedEpoch, startedEpoch)
}

func (a *App) startTerminalWithHostGuardedSized(id string, clientEpoch int, h remote.Host, rows, cols int, expectedEpoch uint64, startedEpoch *uint64) error {
	if a.shuttingDown.Load() {
		return errors.New("软件正在退出")
	}
	hostRevision := aiHostRevision(h)
	a.mu.Lock()
	if a.shuttingDown.Load() || expectedEpoch != 0 && a.shellEpochs[id] != expectedEpoch {
		a.mu.Unlock()
		if a.shuttingDown.Load() {
			return errors.New("软件正在退出")
		}
		return errors.New("SSH 连接已取消")
	}
	a.shellEpochs[id]++
	epoch := a.shellEpochs[id]
	if startedEpoch != nil {
		*startedEpoch = epoch
	}
	old := a.shells[id]
	delete(a.shells, id)
	delete(a.shellClientEpochs, id)
	delete(a.shellHostRevisions, id)
	a.mu.Unlock()
	a.pauseSSHSessionLog(id)
	terminalReady := false
	defer func() {
		if !terminalReady {
			a.finishSSHSessionLog(id, epoch)
		}
	}()
	baseProgress := h.ConnectionProgress
	baseRetry := h.ConnectionRetry
	h.ConnectionProgress = func(stage remote.ConnectionStage) {
		if baseProgress != nil {
			baseProgress(stage)
		}
		if a.ctx != nil && a.shellEpochCurrent(id, epoch) {
			runtime2.EventsEmit(a.ctx, "ssh-connection-stage", map[string]any{"id": id, "epoch": clientEpoch, "stage": stage})
		}
	}
	h.ConnectionRetry = func(attempt, maximum int, delay time.Duration, retryErr error) {
		if baseRetry != nil {
			baseRetry(attempt, maximum, delay, retryErr)
		}
		if a.ctx != nil && a.shellEpochCurrent(id, epoch) {
			runtime2.EventsEmit(a.ctx, "ssh-connection-retry", map[string]any{"id": id, "epoch": clientEpoch, "attempt": attempt, "maximum": maximum, "delayMs": delay.Milliseconds()})
		}
	}
	if old != nil {
		old.Close()
		a.connections.Invalidate(id)
	}
	client, release, err := a.connections.AcquireContext(a.lifecycleContext(), id, h)
	if err != nil {
		logSSHFailure("启动终端", h, err)
		return err
	}
	if !a.shellEpochCurrent(id, epoch) {
		release()
		return errors.New("SSH 连接已取消")
	}
	output := func(output string) {
		a.writeSSHSessionOutput(id, epoch, output)
		if a.ctx != nil && a.shellEpochCurrent(id, epoch) {
			runtime2.EventsEmit(a.ctx, "terminal-output", map[string]any{"id": id, "epoch": clientEpoch, "output": output})
		}
	}
	if err := a.beginSSHSessionLog(id, epoch, h); err != nil {
		slog.Warn("启动 SSH 会话日志失败", "主机ID", id, "错误", err)
	}
	h.ConnectionProgress(remote.StagePTY)
	shell, err := remote.StartShellWithClientSize(client, rows, cols, release, output)
	if err != nil {
		if !a.shellEpochCurrent(id, epoch) {
			return errors.New("SSH 连接已取消")
		}
		a.connections.Invalidate(id)
		client, release, err = a.connections.AcquireContext(a.lifecycleContext(), id, h)
		if err != nil {
			logSSHFailure("PTY 重试连接", h, err)
			return err
		}
		if !a.shellEpochCurrent(id, epoch) {
			release()
			return errors.New("SSH 连接已取消")
		}
		h.ConnectionProgress(remote.StagePTY)
		shell, err = remote.StartShellWithClientSize(client, rows, cols, release, output)
		if err != nil {
			if a.shellEpochCurrent(id, epoch) {
				a.connections.Invalidate(id)
			}
			logSSHFailure("创建 PTY", h, err)
			return err
		}
	}
	a.mu.Lock()
	if a.shuttingDown.Load() || a.shellEpochs[id] != epoch {
		a.mu.Unlock()
		shell.Close()
		if a.shuttingDown.Load() {
			return errors.New("软件正在退出")
		}
		return errors.New("SSH 连接已取消")
	}
	a.shells[id] = shell
	a.shellClientEpochs[id] = clientEpoch
	a.shellHostRevisions[id] = hostRevision
	a.mu.Unlock()
	terminalReady = true
	go a.preloadHostOSRelease(id, h, hostRevision, epoch)
	go func() {
		waitErr := <-shell.Done()
		shell.Close()
		a.finishSSHSessionLog(id, epoch)
		a.mu.Lock()
		if a.shells[id] != shell || a.shellEpochs[id] != epoch {
			a.mu.Unlock()
			return
		}
		delete(a.shells, id)
		delete(a.shellClientEpochs, id)
		delete(a.shellHostRevisions, id)
		a.mu.Unlock()
		message, invalidate, retryable := classifyTerminalExit(waitErr)
		if invalidate {
			a.connections.Invalidate(id)
		}
		if a.ctx != nil {
			if waitErr != nil {
				logSSHFailure("终端会话中断", h, waitErr)
			}
			runtime2.EventsEmit(a.ctx, "terminal-closed", map[string]any{"id": id, "epoch": clientEpoch, "message": message, "retryable": retryable})
		}
	}()
	return nil
}

func classifyTerminalExit(waitErr error) (message string, invalidate bool, retryable bool) {
	if waitErr == nil {
		return "SSH 会话已结束", false, false
	}
	var exitErr *ssh.ExitError
	if errors.As(waitErr, &exitErr) {
		return "SSH 会话已结束", false, false
	}
	return "SSH 连接中断: " + waitErr.Error(), true, remote.IsRetryableConnectionError(waitErr)
}

type passwordRetryInfo struct {
	allowPermanent bool
	credentialName string
	affectedHosts  int
}

func terminalStartFailure(err error, passwordPrompted bool) TerminalStartResult {
	result := TerminalStartResult{ErrorCode: "connection_failed", PasswordPrompted: passwordPrompted}
	if err != nil {
		result.Error = err.Error()
		if remote.IsPasswordAuthenticationFailure(err) {
			result.ErrorCode = "password_auth_failed"
		}
	}
	return result
}

func ordinaryPasswordHost(h remote.Host) bool {
	authType := strings.ToLower(strings.TrimSpace(h.AuthType))
	return authType == "" || authType == "password"
}

func normalizedSSHPort(port int) int {
	if port == 0 {
		return 22
	}
	return port
}

func cacheConfirmedHostKeyForRetry(h remote.Host) remote.Host {
	if h.HostKey != "" || h.ConfirmHostKey == nil {
		return h
	}
	confirm := h.ConfirmHostKey
	var mu sync.Mutex
	confirmedFingerprint := ""
	h.ConfirmHostKey = func(fingerprint string) error {
		mu.Lock()
		if confirmedFingerprint == fingerprint {
			mu.Unlock()
			return nil
		}
		mu.Unlock()
		if err := confirm(fingerprint); err != nil {
			return err
		}
		mu.Lock()
		confirmedFingerprint = fingerprint
		mu.Unlock()
		return nil
	}
	return h
}

func (a *App) passwordRetryDetails(id string) passwordRetryInfo {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for _, host := range a.settings.Hosts {
		if host.ID != id || host.Deleted {
			continue
		}
		if host.CredentialID == "" {
			return passwordRetryInfo{allowPermanent: true, affectedHosts: 1}
		}
		for _, credential := range a.settings.Credentials {
			if credential.ID != host.CredentialID || credential.Type != "password" {
				continue
			}
			affected := 0
			for _, candidate := range a.settings.Hosts {
				if !candidate.Deleted && candidate.CredentialID == credential.ID {
					affected++
				}
			}
			return passwordRetryInfo{allowPermanent: true, credentialName: credential.Name, affectedHosts: affected}
		}
		return passwordRetryInfo{}
	}
	return passwordRetryInfo{}
}

func samePasswordRetryHost(current, attempted remote.Host) bool {
	return current.ID == attempted.ID && current.Address == attempted.Address &&
		normalizedSSHPort(current.Port) == normalizedSSHPort(attempted.Port) &&
		current.Username == attempted.Username && strings.EqualFold(current.AuthType, attempted.AuthType) &&
		current.CredentialID == attempted.CredentialID && !current.Deleted
}

func (a *App) setTransientPassword(id string, attempted remote.Host, password string) error {
	if password == "" {
		return errors.New("新密码不能为空")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	valid := false
	for _, host := range a.settings.Hosts {
		if host.ID == id {
			valid = samePasswordRetryHost(host, attempted)
			break
		}
	}
	if !valid {
		if host, exists := a.temporaryHosts[id]; exists {
			valid = samePasswordRetryHost(host, attempted)
		}
	}
	if !valid {
		return errors.New("主机连接信息已变化，请重新连接")
	}
	if a.transientPasswords == nil {
		a.transientPasswords = make(map[string]string)
	}
	a.transientPasswords[id] = password
	return nil
}

func (a *App) clearTransientPassword(id string) {
	a.mu.Lock()
	delete(a.transientPasswords, id)
	a.mu.Unlock()
}

func (a *App) persistSuccessfulTerminalPassword(id string, attempted remote.Host, password string) error {
	a.mu.Lock()
	hostIndex := -1
	for i := range a.settings.Hosts {
		if a.settings.Hosts[i].ID == id {
			hostIndex = i
			break
		}
	}
	if hostIndex < 0 || !samePasswordRetryHost(a.settings.Hosts[hostIndex], attempted) {
		a.mu.Unlock()
		return errors.New("主机连接信息已变化，未保存新密码")
	}
	host := a.settings.Hosts[hostIndex]
	candidate := cloneSettings(a.settings)
	if host.CredentialID == "" {
		candidate.Hosts[hostIndex].Password = password
		if err := a.commitSettingsLocked(candidate); err != nil {
			a.mu.Unlock()
			return err
		}
		delete(a.transientPasswords, id)
		a.mu.Unlock()
		if a.aiAgent != nil {
			a.aiAgent.Close()
		}
		return nil
	}
	credentialIndex := -1
	for i := range candidate.Credentials {
		credential := &candidate.Credentials[i]
		if credential.ID == host.CredentialID && credential.Type == "password" {
			credentialIndex = i
			break
		}
	}
	if credentialIndex < 0 {
		a.mu.Unlock()
		return errors.New("密码凭证已不存在或类型已变化")
	}
	previous := candidate.Credentials[credentialIndex].Password
	candidate.Credentials[credentialIndex].Password = password
	if err := a.commitSettingsLocked(candidate); err != nil {
		a.mu.Unlock()
		return err
	}
	if a.transientPasswords == nil {
		a.transientPasswords = make(map[string]string)
	}
	for _, candidate := range a.settings.Hosts {
		if candidate.ID == id || candidate.CredentialID != host.CredentialID || a.shells[candidate.ID] == nil {
			continue
		}
		if _, exists := a.transientPasswords[candidate.ID]; !exists {
			a.transientPasswords[candidate.ID] = previous
		}
	}
	delete(a.transientPasswords, id)
	a.mu.Unlock()
	if a.aiAgent != nil {
		a.aiAgent.Close()
	}
	return nil
}

func (a *App) shellEpochCurrent(id string, epoch uint64) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.shellEpochs[id] == epoch
}

func (a *App) WriteTerminal(id string, clientEpoch int, input string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	a.mu.RLock()
	shell := a.shells[id]
	epochMatches := a.shellClientEpochs[id] == clientEpoch
	epoch := a.shellEpochs[id]
	a.mu.RUnlock()
	if shell == nil || !epochMatches {
		return errors.New("SSH 会话已变化")
	}
	a.rememberSSHSessionInput(id, epoch, input)
	if err := shell.Write(input); err != nil {
		a.forgetSSHSessionInput(id, epoch, input)
		return err
	}
	return nil
}

func (a *App) ResizeTerminal(id string, clientEpoch, rows, columns int) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	a.mu.RLock()
	shell := a.shells[id]
	epochMatches := a.shellClientEpochs[id] == clientEpoch
	a.mu.RUnlock()
	if shell == nil || !epochMatches {
		return errors.New("SSH 会话已变化")
	}
	return shell.Resize(rows, columns)
}

func (a *App) DetectPersistentSessionTool(id string, clientEpoch int) (string, error) {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return "", err
	}
	return detectOrInstallPersistentSessionTool(shell.RunTimeout)
}

const persistentSessionToolMarker = "__SANTIANSHELL_PERSISTENT_TOOL__="

func detectOrInstallPersistentSessionTool(run func(string, time.Duration) (remote.Result, error)) (string, error) {
	detectCommand := "if command -v tmux >/dev/null 2>&1; then printf tmux; elif command -v screen >/dev/null 2>&1; then printf screen; fi"
	result, err := run(detectCommand, 5*time.Second)
	if tool := parsePersistentSessionTool(result.Output); tool != "" {
		return tool, nil
	}
	if err != nil && result.ExitCode < 0 {
		return "", fmt.Errorf("检测 tmux/screen 失败: %w", err)
	}

	result, err = run(persistentSessionInstallCommand(), 3*time.Minute)
	if err == nil && result.ExitCode == 0 {
		if tool := parsePersistentSessionTool(result.Output); tool != "" {
			return tool, nil
		}
	}
	detail := strings.TrimSpace(result.Output)
	if detail == "" && err != nil {
		detail = err.Error()
	}
	if detail == "" {
		detail = "安装命令没有返回详细原因"
	}
	runes := []rune(detail)
	if len(runes) > 1200 {
		detail = "…" + string(runes[len(runes)-1200:])
	}
	return "", fmt.Errorf("自动安装 tmux/screen 失败：%s", detail)
}

func parsePersistentSessionTool(output string) string {
	value := strings.TrimSpace(output)
	if value == "tmux" || value == "screen" {
		return value
	}
	marker := strings.LastIndex(value, persistentSessionToolMarker)
	if marker < 0 {
		return ""
	}
	fields := strings.Fields(value[marker+len(persistentSessionToolMarker):])
	if len(fields) > 0 && (fields[0] == "tmux" || fields[0] == "screen") {
		return fields[0]
	}
	return ""
}

func persistentSessionInstallCommand() string {
	return strings.TrimSpace(`
set -u
detect_tool() {
  if command -v tmux >/dev/null 2>&1; then
    printf tmux
  elif command -v screen >/dev/null 2>&1; then
    printf screen
  fi
}
tool=$(detect_tool)
if [ -n "$tool" ]; then
  printf '` + persistentSessionToolMarker + `%s\n' "$tool"
  exit 0
fi
if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
  SUDO="sudo -n"
else
  echo '当前账号不是 root，也不能免密 sudo；请使用 root 连接或先手动安装 tmux。' >&2
  exit 42
fi
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  $SUDO apt-get update -qq >/dev/null 2>&1 || true
  $SUDO apt-get install -y --no-install-recommends tmux || $SUDO apt-get install -y --no-install-recommends screen
elif command -v dnf >/dev/null 2>&1; then
  $SUDO dnf install -y tmux || $SUDO dnf install -y screen
elif command -v yum >/dev/null 2>&1; then
  $SUDO yum install -y tmux || $SUDO yum install -y screen
elif command -v apk >/dev/null 2>&1; then
  $SUDO apk add --no-cache tmux || $SUDO apk add --no-cache screen
else
  echo '没有识别到 apt-get、dnf、yum 或 apk，无法自动安装 tmux/screen。' >&2
  exit 44
fi
tool=$(detect_tool)
if [ -z "$tool" ]; then
  echo '安装命令已结束，但仍未检测到 tmux 或 screen。' >&2
  exit 43
fi
printf '` + persistentSessionToolMarker + `%s\n' "$tool"
`)
}

func (a *App) CloseTerminal(id string) {
	if a.requireUnlocked() != nil {
		return
	}
	a.mu.Lock()
	a.shellEpochs[id]++
	shell := a.shells[id]
	delete(a.shells, id)
	delete(a.shellClientEpochs, id)
	delete(a.shellHostRevisions, id)
	delete(a.transientPasswords, id)
	a.mu.Unlock()
	if _, err := a.disableSSHSessionLog(id); err != nil {
		slog.Warn("关闭 SSH 会话日志失败", "主机ID", id, "错误", err)
	}
	if shell != nil {
		shell.Close()
	}
	a.connections.Invalidate(id)
}

func (a *App) ProbeTerminalAlive(id string, clientEpoch int) bool {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return false
	}
	return shell.ProbeAlive()
}

func (a *App) connectedShell(id string, clientEpoch int) (*remote.Shell, error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, err
	}
	a.mu.RLock()
	shell := a.shells[id]
	epochMatches := a.shellClientEpochs[id] == clientEpoch
	a.mu.RUnlock()
	if shell == nil {
		return nil, errors.New("SSH 会话未连接")
	}
	if !epochMatches {
		return nil, errors.New("SSH 会话已变化")
	}
	return shell, nil
}
