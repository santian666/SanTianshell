package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"santianshell/internal/rdp"
	"santianshell/internal/remote"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
)

func normalizeRDPHostOptions(h *remote.Host) error {
	if h.RDPGatewayPassword != "" {
		return errors.New("RD Gateway 密码不能保存在主机配置中，请连接时在 mstsc 安全提示中输入")
	}
	if !strings.EqualFold(h.Protocol, "rdp") {
		return nil
	}
	h.RDPJumpHostID = strings.TrimSpace(h.RDPJumpHostID)
	gatewayConfigured := strings.TrimSpace(h.RDPGatewayAddress) != "" ||
		strings.TrimSpace(h.RDPGatewayUsername) != "" || h.RDPGatewayUsage != int(rdp.GatewayUsageNoneDirect)
	if h.ProxyEnabled && gatewayConfigured {
		return errors.New("RDP 目标 SOCKS5 代理不能与 RD Gateway 同时启用")
	}
	if h.RDPJumpHostID != "" && h.ProxyEnabled {
		return errors.New("RDP SSH 跳板不能与目标主机 SOCKS5 代理同时启用")
	}
	if h.RDPJumpHostID != "" && strings.TrimSpace(h.RDPGatewayAddress) != "" {
		return errors.New("RDP SSH 跳板不能与 RD Gateway 同时启用")
	}
	if h.ID != "" && h.RDPJumpHostID == h.ID {
		return errors.New("RDP 主机不能将自身设置为 SSH 跳板")
	}
	if strings.TrimSpace(h.RDPResolution) == "" {
		h.RDPResolution = "1280x1024"
	}
	if h.RDPDisplayMode == "" {
		h.RDPDisplayMode = string(rdp.DisplayModeWindow)
	}
	if h.RDPDisplayMode != string(rdp.DisplayModeWindow) && h.RDPDisplayMode != string(rdp.DisplayModeFullscreen) {
		return errors.New("RDP 显示模式必须是 window 或 fullscreen")
	}
	if h.RDPRedirectClipboard == nil {
		enabled := true
		h.RDPRedirectClipboard = &enabled
	}
	if h.RDPAudioPlayback < int(rdp.AudioPlaybackLocal) || h.RDPAudioPlayback > int(rdp.AudioPlaybackDisabled) {
		return errors.New("RDP 音频播放模式必须是 0、1 或 2")
	}
	if h.RDPGatewayUsage < int(rdp.GatewayUsageNoneDirect) || h.RDPGatewayUsage > int(rdp.GatewayUsageNoneDetect) {
		return errors.New("RD Gateway 使用方式必须在 0 到 4 之间")
	}
	return nil
}

func testRDPHostConfig(h remote.Host, startJump func(string, string, time.Duration) (string, io.Closer, error), directLatency func(remote.Host) (int64, error)) (int64, error) {
	timeout := time.Duration(max(h.Timeout, 12)) * time.Second
	if rdp.GatewayEnabled(h.RDPGatewayAddress, rdp.GatewayUsageMethod(h.RDPGatewayUsage)) {
		started := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if err := rdp.ProbeGateway(ctx, h.RDPGatewayAddress); err != nil {
			return 0, err
		}
		return time.Since(started).Milliseconds(), nil
	}
	if strings.TrimSpace(h.RDPJumpHostID) == "" {
		return directLatency(h)
	}
	port := h.Port
	if port == 0 {
		port = 3389
	}
	started := time.Now()
	address, tunnel, err := startJump(h.RDPJumpHostID, net.JoinHostPort(h.Address, strconv.Itoa(port)), timeout)
	if err != nil {
		return 0, err
	}
	if tunnel == nil {
		return 0, errors.New("RDP SSH 跳板未返回可关闭的隧道")
	}
	defer tunnel.Close()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if err := probeRDPAddress(ctx, address); err != nil {
		return 0, err
	}
	return time.Since(started).Milliseconds(), nil
}

type rdpStatusArbiter struct {
	mu              sync.Mutex
	emit            func(rdp.StatusEvent)
	waitForForward  bool
	forwardResolved bool
	forwardDone     chan struct{}
	terminalState   rdp.State
	pendingExit     *rdp.StatusEvent
}

func newRDPStatusArbiter(waitForForward bool, emit func(rdp.StatusEvent)) *rdpStatusArbiter {
	arbiter := &rdpStatusArbiter{
		emit:           emit,
		waitForForward: waitForForward,
		forwardDone:    make(chan struct{}),
	}
	if !waitForForward {
		arbiter.forwardResolved = true
		close(arbiter.forwardDone)
	}
	return arbiter
}

func (a *rdpStatusArbiter) Report(event rdp.StatusEvent) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.terminalState != "" {
		return
	}
	if event.State == rdp.StateExited && a.waitForForward && !a.forwardResolved {
		pending := event
		a.pendingExit = &pending
		return
	}
	if event.State == rdp.StateExited || event.State == rdp.StateFailed {
		a.terminalState = event.State
	}
	if a.emit != nil {
		a.emit(event)
	}
}

func (a *rdpStatusArbiter) ResolveForward(sessionID string, pid int, runtimeErr error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.forwardResolved {
		return
	}
	a.forwardResolved = true
	close(a.forwardDone)
	if a.terminalState != "" {
		a.pendingExit = nil
		return
	}
	if runtimeErr != nil {
		event := rdp.StatusEvent{
			SessionID: sessionID,
			State:     rdp.StateFailed,
			PID:       pid,
			Error:     runtimeErr.Error(),
			Err:       runtimeErr,
		}
		a.terminalState = rdp.StateFailed
		a.pendingExit = nil
		if a.emit != nil {
			a.emit(event)
		}
		return
	}
	if a.pendingExit != nil {
		event := *a.pendingExit
		a.pendingExit = nil
		a.terminalState = rdp.StateExited
		if a.emit != nil {
			a.emit(event)
		}
	}
}

func (a *rdpStatusArbiter) ForwardDone() <-chan struct{} {
	if a == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return a.forwardDone
}

// waitRDPForwardError 在会话结束时再读取一次已缓冲错误，避免随机 select 丢失原始原因。
func waitRDPForwardError(done <-chan struct{}, forwardErrors <-chan error) error {
	for {
		select {
		case runtimeErr, ok := <-forwardErrors:
			if !ok {
				return nil
			}
			if runtimeErr != nil {
				return runtimeErr
			}
		default:
		}

		select {
		case runtimeErr, ok := <-forwardErrors:
			if !ok {
				return nil
			}
			if runtimeErr != nil {
				return runtimeErr
			}
		case <-done:
			for {
				select {
				case runtimeErr, ok := <-forwardErrors:
					if !ok {
						return nil
					}
					if runtimeErr != nil {
						return runtimeErr
					}
				default:
					return nil
				}
			}
		}
	}
}

func validateRDPHostForOpen(host remote.Host) error {
	if host.Deleted {
		return errors.New("RDP 主机已删除")
	}
	if !strings.EqualFold(strings.TrimSpace(host.Protocol), "rdp") {
		return errors.New("只能为 RDP 主机打开远程桌面")
	}
	return nil
}

func (a *App) rdpStatusForUI(event rdp.StatusEvent) rdp.StatusEvent {
	if event.State != rdp.StateExited || strings.TrimSpace(event.SessionID) == "" {
		return event
	}
	a.mu.Lock()
	_, intentional := a.rdpSessionClosing[event.SessionID]
	if intentional {
		delete(a.rdpSessionClosing, event.SessionID)
	}
	a.mu.Unlock()
	if intentional {
		event.Error = ""
		event.Err = nil
	}
	return event
}

func (a *App) hostRecordLocked(id string) (remote.Host, bool) {
	for _, host := range a.settings.Hosts {
		if host.ID == id {
			return host, true
		}
	}
	host, exists := a.temporaryHosts[id]
	return host, exists
}

// validateAndRegisterRDPSessionLocked 必须在 a.mu 写锁内调用，使启动后的复核与登记不可被删除操作打断。
func (a *App) validateAndRegisterRDPSessionLocked(hostID, jumpHostID string, session *rdp.Session) error {
	if a.shuttingDown.Load() {
		return errors.New("软件正在退出")
	}
	if session == nil || strings.TrimSpace(session.ID) == "" {
		return errors.New("RDP 启动器未返回有效会话")
	}
	host, exists := a.hostRecordLocked(hostID)
	if !exists {
		return errors.New("RDP 主机不存在或已被移除")
	}
	if err := validateRDPHostForOpen(host); err != nil {
		return err
	}
	jumpHostID = strings.TrimSpace(jumpHostID)
	if strings.TrimSpace(host.RDPJumpHostID) != jumpHostID {
		return errors.New("RDP 主机的 SSH 跳板配置已在启动期间变化")
	}
	if jumpHostID != "" {
		jumpHost, jumpExists := a.hostRecordLocked(jumpHostID)
		if !jumpExists || jumpHost.Deleted || !isSSHHostProtocol(jumpHost.Protocol) {
			return errors.New("RDP SSH 跳板主机不存在、已删除或不再是 SSH 主机")
		}
	}
	if a.rdpSessions == nil {
		a.rdpSessions = make(map[string]*rdp.Session)
	}
	if current := a.rdpSessions[session.ID]; current != nil && current != session {
		return errors.New("RDP 会话 ID 冲突")
	}
	if a.rdpSessionHosts == nil {
		a.rdpSessionHosts = make(map[string]string)
	}
	if a.rdpSessionJumps == nil {
		a.rdpSessionJumps = make(map[string]string)
	}
	if a.rdpSessionClosing == nil {
		a.rdpSessionClosing = make(map[string]struct{})
	}
	a.rdpSessions[session.ID] = session
	a.rdpSessionHosts[session.ID] = hostID
	if jumpHostID != "" {
		a.rdpSessionJumps[session.ID] = jumpHostID
	} else {
		delete(a.rdpSessionJumps, session.ID)
	}
	delete(a.rdpSessionClosing, session.ID)
	return nil
}

func (a *App) OpenRDP(id string) (string, error) {
	h, err := a.host(id)
	if err != nil {
		return "", err
	}
	if err := validateRDPHostForOpen(h); err != nil {
		return "", err
	}
	port := h.Port
	if port == 0 {
		port = 3389
	}
	target := net.JoinHostPort(h.Address, strconv.Itoa(port))
	rdpOptions, err := rdpOptionsForHost(h, target)
	if err != nil {
		return "", err
	}
	address := target
	var tunnel *remote.TCPForward
	var tunnelResource io.Closer
	var forwardErrors <-chan error
	jumpHostID := strings.TrimSpace(h.RDPJumpHostID)
	gatewayEnabled := rdp.GatewayEnabled(h.RDPGatewayAddress, rdp.GatewayUsageMethod(h.RDPGatewayUsage))
	if jumpHostID != "" {
		address, tunnelResource, err = a.startRDPJumpForward(jumpHostID, target, time.Duration(max(h.Timeout, 12))*time.Second)
		if err != nil {
			return "", err
		}
		if source, ok := tunnelResource.(interface{ Errors() <-chan error }); ok {
			forwardErrors = source.Errors()
		}
	} else if h.ProxyEnabled {
		var localPort int
		tunnel, localPort, err = remote.StartSOCKS5Forward(h, target)
		if err != nil {
			return "", fmt.Errorf("RDP SOCKS5 隧道创建失败: %w", err)
		}
		tunnelResource = closeFunc(tunnel.Close)
		forwardErrors = tunnel.Errors()
		address = net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort))
	}
	rdpOptions.Address = address
	emitStatus := func(event rdp.StatusEvent) {
		event = a.rdpStatusForUI(event)
		slog.Info("RDP 会话状态", "主机ID", id, "会话ID", event.SessionID, "状态", event.State, "PID", event.PID, "错误", event.Error)
		if ctx, err := a.runtimeContext(); err == nil {
			runtime2.EventsEmit(ctx, "rdp-status", map[string]any{
				"hostId": id, "sessionId": event.SessionID, "state": event.State,
				"pid": event.PID, "error": event.Error,
			})
		}
	}
	status := newRDPStatusArbiter(forwardErrors != nil, emitStatus)
	preflightHost := h
	preflightHost.Port = port
	ctx, cancel := context.WithTimeout(a.lifecycleContext(), time.Duration(max(h.Timeout, 12))*time.Second)
	defer cancel()
	rdpOptions.Tunnel = tunnelResource
	rdpOptions.ImmediateLaunch = true
	rdpOptions.Preflight = func(ctx context.Context, _ string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if gatewayEnabled || jumpHostID != "" {
			return nil
		}
		_, probeErr := remote.Latency(preflightHost)
		return probeErr
	}
	rdpOptions.OnStatus = status.Report
	session, err := a.rdpRunner.Start(ctx, rdpOptions)
	if err != nil {
		return "", err
	}
	a.mu.Lock()
	registerErr := a.validateAndRegisterRDPSessionLocked(id, jumpHostID, session)
	a.mu.Unlock()
	if registerErr != nil {
		launchErr := fmt.Errorf("RDP 会话启动后目标失效: %w", registerErr)
		status.Report(rdp.StatusEvent{SessionID: session.ID, State: rdp.StateFailed, PID: session.PID, Error: launchErr.Error(), Err: launchErr})
		status.ResolveForward(session.ID, session.PID, nil)
		resultErr := launchErr
		if closeErr := session.Close(); closeErr != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("关闭已启动的 mstsc 失败: %w", closeErr))
		}
		if tunnelResource != nil {
			if closeErr := tunnelResource.Close(); closeErr != nil {
				resultErr = errors.Join(resultErr, fmt.Errorf("关闭已启动的 RDP 隧道失败: %w", closeErr))
			}
		}
		return "", resultErr
	}
	if forwardErrors != nil {
		a.monitorRDPForward(id, session, forwardErrors, tunnelResource, status)
	}
	go func() {
		<-session.Done()
		<-status.ForwardDone()
		a.mu.Lock()
		if a.rdpSessions[session.ID] == session {
			delete(a.rdpSessions, session.ID)
			delete(a.rdpSessionHosts, session.ID)
			delete(a.rdpSessionJumps, session.ID)
			delete(a.rdpSessionClosing, session.ID)
		}
		a.mu.Unlock()
	}()
	return session.ID, nil
}

func (a *App) monitorRDPForward(hostID string, session *rdp.Session, forwardErrors <-chan error, forward io.Closer, status *rdpStatusArbiter) {
	if session == nil || forwardErrors == nil || forward == nil || status == nil {
		return
	}
	go func() {
		runtimeErr := waitRDPForwardError(session.Done(), forwardErrors)
		if runtimeErr == nil {
			status.ResolveForward(session.ID, session.PID, nil)
			return
		}

		a.mu.Lock()
		active := a.rdpSessions[session.ID] == session
		_, closing := a.rdpSessionClosing[session.ID]
		if active && !closing {
			if a.rdpSessionClosing == nil {
				a.rdpSessionClosing = make(map[string]struct{})
			}
			a.rdpSessionClosing[session.ID] = struct{}{}
		}
		a.mu.Unlock()
		if !active || closing {
			status.ResolveForward(session.ID, session.PID, nil)
			return
		}

		status.ResolveForward(session.ID, session.PID, runtimeErr)
		slog.Error("RDP 转发运行失败", "主机ID", hostID, "会话ID", session.ID, "PID", session.PID, "错误", runtimeErr)
		if closeErr := forward.Close(); closeErr != nil {
			slog.Error("RDP 转发失败后关闭隧道失败", "主机ID", hostID, "会话ID", session.ID, "错误", closeErr)
		}
		if closeErr := session.Close(); closeErr != nil {
			slog.Error("RDP 转发失败后关闭 mstsc 失败", "主机ID", hostID, "会话ID", session.ID, "错误", closeErr)
		}
	}()
}

func rdpOptionsForHost(h remote.Host, address string) (rdp.Options, error) {
	if err := normalizeRDPHostOptions(&h); err != nil {
		return rdp.Options{}, err
	}
	width, height := parseRDPResolution(h.RDPResolution)
	redirectClipboard := true
	if h.RDPRedirectClipboard != nil {
		redirectClipboard = *h.RDPRedirectClipboard
	}
	return rdp.Options{
		Address:           address,
		Username:          h.Username,
		Password:          h.Password,
		DesktopWidth:      width,
		DesktopHeight:     height,
		DisplayMode:       rdp.DisplayMode(h.RDPDisplayMode),
		SmartSizing:       h.RDPSmartSizing,
		UseMultimon:       h.RDPUseMultimon,
		RedirectDrives:    h.RDPMapDrives,
		RedirectDriveIDs:  append([]string(nil), h.RDPDriveIDs...),
		RedirectClipboard: redirectClipboard,
		AudioPlayback:     rdp.AudioPlaybackMode(h.RDPAudioPlayback),
		AudioCapture:      h.RDPAudioCapture,
		RedirectPrinters:  h.RDPRedirectPrinters,
		GatewayAddress:    h.RDPGatewayAddress,
		GatewayUsername:   h.RDPGatewayUsername,
		GatewayUsage:      rdp.GatewayUsageMethod(h.RDPGatewayUsage),
	}, nil
}

func parseRDPResolution(value string) (int, int) {
	width, height := 1280, 1024
	parts := strings.Split(strings.ToLower(strings.TrimSpace(value)), "x")
	if len(parts) != 2 {
		return width, height
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(parts[0])); err == nil && parsed >= 640 && parsed <= 7680 {
		width = parsed
	}
	if parsed, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil && parsed >= 480 && parsed <= 4320 {
		height = parsed
	}
	return width, height
}

func (a *App) CloseRDP(sessionID string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	sessionID = strings.TrimSpace(sessionID)
	a.mu.Lock()
	session := a.rdpSessions[sessionID]
	if session == nil {
		a.mu.Unlock()
		return errors.New("RDP 会话不存在或已经退出")
	}
	if a.rdpSessionClosing == nil {
		a.rdpSessionClosing = make(map[string]struct{})
	}
	_, alreadyClosing := a.rdpSessionClosing[sessionID]
	if !alreadyClosing {
		a.rdpSessionClosing[sessionID] = struct{}{}
	}
	a.mu.Unlock()
	if err := session.Close(); err != nil {
		if !alreadyClosing {
			a.mu.Lock()
			if a.rdpSessions[sessionID] == session {
				delete(a.rdpSessionClosing, sessionID)
			}
			a.mu.Unlock()
		}
		return err
	}
	return nil
}
