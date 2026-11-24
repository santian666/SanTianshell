package rdp

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

const (
	defaultRDPPort             = 3389
	defaultRDPWidth            = 1280
	defaultRDPHeight           = 1024
	defaultRDPPreflightTimeout = 10 * time.Second
	maxRDPNegotiationResponse  = 4096
)

var rdpNegotiationRequest = []byte{
	0x03, 0x00, 0x00, 0x13,
	0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00,
}

// DisplayMode 控制 mstsc 启动时的显示方式。
type DisplayMode string

const (
	DisplayModeWindow     DisplayMode = "window"
	DisplayModeFullscreen DisplayMode = "fullscreen"
)

// AudioPlaybackMode 控制远程声音的播放位置，值与 .rdp 的 audiomode 一致。
type AudioPlaybackMode int

const (
	AudioPlaybackLocal AudioPlaybackMode = iota
	AudioPlaybackRemote
	AudioPlaybackDisabled
)

// GatewayUsageMethod 控制何时使用 RD Gateway，值与 Windows API 一致。
type GatewayUsageMethod int

const (
	GatewayUsageNoneDirect GatewayUsageMethod = iota
	GatewayUsageAlways
	GatewayUsageFallback
	GatewayUsageSystemDefault
	GatewayUsageNoneDetect
)

// State 表示 mstsc 实例的生命周期状态。
type State string

const (
	StateLaunching State = "launching"
	StateRunning   State = "running"
	StateExited    State = "exited"
	StateFailed    State = "failed"
)

// ErrCredentialInUse 表示同一 TERMSRV 目标已有不同凭证的活动会话。
var ErrCredentialInUse = errors.New("同一 RDP 目标已有使用不同凭证的活动会话")

// StatusEvent 用唯一会话 ID 区分同时打开的多个 RDP 窗口。
type StatusEvent struct {
	SessionID string `json:"sessionId"`
	State     State  `json:"state"`
	PID       int    `json:"pid,omitempty"`
	Error     string `json:"error,omitempty"`
	Err       error  `json:"-"`
}

// PreflightFunc 必须对 mstsc 将要使用的同一条目标链路做真实连接预检。
type PreflightFunc func(ctx context.Context, address string) error

// Options 是单个 mstsc 实例的启动参数。Tunnel 的所有权在 Start 调用后转交给 Runner。
type Options struct {
	Address           string
	Username          string
	Password          string
	DesktopWidth      int
	DesktopHeight     int
	DisplayMode       DisplayMode
	SmartSizing       bool
	UseMultimon       bool
	RedirectDrives    bool
	RedirectDriveIDs  []string
	RedirectClipboard bool
	AudioPlayback     AudioPlaybackMode
	AudioCapture      bool
	RedirectPrinters  bool
	GatewayAddress    string
	GatewayUsername   string
	GatewayPassword   string
	GatewayUsage      GatewayUsageMethod
	Tunnel            io.Closer
	Preflight         PreflightFunc
	ImmediateLaunch   bool
	OnStatus          func(StatusEvent)
}

type managedProcess interface {
	PID() int
	Wait() error
	Kill() error
}

type processStarter func(rdpPath string) (managedProcess, error)

type credentialBackend interface {
	Write(target, username, password string) error
	Delete(target string) error
}

type fileCreator func(content []byte) (path string, cleanup func() error, err error)

type runnerDeps struct {
	startProcess  processStarter
	credentials   credentialBackend
	createFile    fileCreator
	newID         func() (string, error)
	protocolProbe PreflightFunc
}

// Runner 启动并跟踪独立的 mstsc 进程实例。
type Runner struct {
	startProcess  processStarter
	createFile    fileCreator
	newID         func() (string, error)
	protocolProbe PreflightFunc
	credentials   *credentialPool
}

// NewRunner 创建使用系统 mstsc 和 Windows Credential Manager 的运行器。
func NewRunner() *Runner {
	return newRunner(runnerDeps{})
}

func newRunner(deps runnerDeps) *Runner {
	if deps.startProcess == nil {
		deps.startProcess = startMSTSC
	}
	if deps.credentials == nil {
		deps.credentials = windowsCredentialBackend{}
	}
	if deps.createFile == nil {
		deps.createFile = createRDPFile
	}
	if deps.newID == nil {
		deps.newID = newSessionID
	}
	if deps.protocolProbe == nil {
		deps.protocolProbe = probeRDPAddress
	}
	return &Runner{
		startProcess:  deps.startProcess,
		createFile:    deps.createFile,
		newID:         deps.newID,
		protocolProbe: deps.protocolProbe,
		credentials:   newCredentialPool(deps.credentials),
	}
}

// Session 封装一个已启动的 mstsc 进程及其清理完成信号。
type Session struct {
	ID      string
	PID     int
	Address string

	process   managedProcess
	done      chan struct{}
	mu        sync.RWMutex
	waitErr   error
	closeOnce sync.Once
	closeErr  error
}

// Done 在进程退出且临时凭证、配置文件和隧道均已清理后关闭。
func (s *Session) Done() <-chan struct{} {
	return s.done
}

// Wait 等待进程退出和资源清理，并返回进程或清理错误。
func (s *Session) Wait() error {
	<-s.done
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.waitErr
}

// Close 请求关闭该 mstsc 进程。资源在进程实际退出后清理。
func (s *Session) Close() error {
	select {
	case <-s.done:
		return nil
	default:
	}
	s.closeOnce.Do(func() {
		if err := s.process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			s.closeErr = fmt.Errorf("关闭 mstsc 进程失败: %w", err)
		}
	})
	return s.closeErr
}

func (s *Session) finish(err error) {
	s.mu.Lock()
	s.waitErr = err
	close(s.done)
	s.mu.Unlock()
}

// Start 先执行预检，再写入临时凭证、创建安全配置并启动 mstsc。
func (r *Runner) Start(ctx context.Context, options Options) (*Session, error) {
	sessionID, err := r.newID()
	if err != nil {
		finalErr := closeTunnel(options.Tunnel, fmt.Errorf("创建 RDP 会话 ID 失败: %w", err))
		notify(options.OnStatus, statusEvent("", StateFailed, 0, finalErr))
		return nil, finalErr
	}
	notify(options.OnStatus, statusEvent(sessionID, StateLaunching, 0, nil))

	fail := func(startErr error) (*Session, error) {
		finalErr := closeTunnel(options.Tunnel, startErr)
		notify(options.OnStatus, statusEvent(sessionID, StateFailed, 0, finalErr))
		return nil, finalErr
	}

	config, err := normalizeOptions(options)
	if err != nil {
		return fail(fmt.Errorf("RDP 配置无效: %w", err))
	}
	if !options.ImmediateLaunch {
		if ctx == nil {
			return fail(errors.New("RDP 预检失败: context 不能为空"))
		}
		if options.Preflight == nil {
			return fail(errors.New("RDP 预检失败: 未提供连接预检函数"))
		}
		if err := ctx.Err(); err != nil {
			return fail(fmt.Errorf("RDP 预检失败: %w", err))
		}
		if err := options.Preflight(ctx, config.address); err != nil {
			return fail(fmt.Errorf("RDP 预检失败: %w", err))
		}
		if GatewayEnabled(config.gatewayAddress, config.gatewayUsage) {
			if err := ProbeGateway(ctx, config.gatewayAddress); err != nil {
				return fail(fmt.Errorf("RD Gateway 预检失败: %w", err))
			}
		} else if err := r.protocolProbe(ctx, config.address); err != nil {
			return fail(fmt.Errorf("RDP 协议预检失败: %w", err))
		}
	}

	path, removeFile, err := r.createFile(buildRDPContent(config))
	if err != nil {
		return fail(fmt.Errorf("创建 RDP 配置失败: %w", err))
	}
	failWithFile := func(startErr error) (*Session, error) {
		finalErr := joinCleanup(startErr, "清理 RDP 配置失败", removeFile())
		return fail(finalErr)
	}

	releaseCredential := func() error { return nil }
	if config.password != "" {
		releaseCredential, err = r.credentials.acquire(config.credentialTarget, config.username, config.password)
		if err != nil {
			return failWithFile(fmt.Errorf("写入 RDP 临时凭证失败: %w", err))
		}
	}
	failWithCredential := func(startErr error) (*Session, error) {
		finalErr := joinCleanup(startErr, "清理 RDP 临时凭证失败", releaseCredential())
		return failWithFile(finalErr)
	}

	process, err := r.startProcess(path)
	if err != nil {
		return failWithCredential(fmt.Errorf("启动 mstsc 失败: %w", err))
	}
	if process == nil {
		return failWithCredential(errors.New("启动 mstsc 失败: 未返回进程实例"))
	}
	session := &Session{
		ID:      sessionID,
		PID:     process.PID(),
		Address: config.address,
		process: process,
		done:    make(chan struct{}),
	}
	notify(options.OnStatus, statusEvent(session.ID, StateRunning, session.PID, nil))

	go func() {
		waitErr := process.Wait()
		waitErr = joinCleanup(waitErr, "清理 RDP 临时凭证失败", releaseCredential())
		waitErr = joinCleanup(waitErr, "清理 RDP 配置失败", removeFile())
		waitErr = joinCleanup(waitErr, "关闭 RDP 隧道失败", closeOnly(options.Tunnel))
		session.finish(waitErr)
		notify(options.OnStatus, statusEvent(session.ID, StateExited, session.PID, waitErr))
	}()

	return session, nil
}

// GatewayEnabled 表示 mstsc 可能通过已配置的 RD Gateway 建立连接。
func GatewayEnabled(address string, usage GatewayUsageMethod) bool {
	if strings.TrimSpace(address) == "" {
		return false
	}
	switch usage {
	case GatewayUsageAlways, GatewayUsageFallback, GatewayUsageSystemDefault:
		return true
	default:
		return false
	}
}

// ProbeGateway 只验证实际 RD Gateway TCP 端点，不假装完成完整 RDG 协议。
func ProbeGateway(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("RD Gateway 预检 context 不能为空")
	}
	endpoint, err := gatewayPreflightAddress(address)
	if err != nil {
		return fmt.Errorf("RD Gateway 地址无效: %w", err)
	}
	probeCtx, cancel := context.WithTimeout(ctx, defaultRDPPreflightTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", endpoint)
	if err != nil {
		return wrapRDPProbeIOError(probeCtx, "RD Gateway TCP 连接失败", err)
	}
	_ = conn.Close()
	return nil
}

func gatewayPreflightAddress(address string) (string, error) {
	normalized, err := normalizeGatewayAddress(address)
	if err != nil {
		return "", err
	}
	if normalized == "" {
		return "", errors.New("地址不能为空")
	}
	if host, port, splitErr := net.SplitHostPort(normalized); splitErr == nil {
		return net.JoinHostPort(host, port), nil
	}
	if strings.HasPrefix(normalized, "[") && strings.HasSuffix(normalized, "]") {
		return net.JoinHostPort(normalized[1:len(normalized)-1], "443"), nil
	}
	return net.JoinHostPort(normalized, "443"), nil
}

func probeRDPAddress(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("RDP 协议预检 context 不能为空")
	}
	probeCtx, cancel := context.WithTimeout(ctx, defaultRDPPreflightTimeout)
	defer cancel()
	conn, err := (&net.Dialer{}).DialContext(probeCtx, "tcp", address)
	if err != nil {
		return wrapRDPProbeIOError(probeCtx, "RDP TCP 连接失败", err)
	}
	defer conn.Close()
	return probeRDPConn(probeCtx, conn)
}

func probeRDPConn(ctx context.Context, conn net.Conn) error {
	deadline, ok := ctx.Deadline()
	if !ok {
		deadline = time.Now().Add(defaultRDPPreflightTimeout)
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("RDP 协商设置超时失败: %w", err)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()

	for written := 0; written < len(rdpNegotiationRequest); {
		count, err := conn.Write(rdpNegotiationRequest[written:])
		written += count
		if err != nil {
			return wrapRDPProbeIOError(ctx, "RDP 协商请求发送失败", err)
		}
		if count == 0 {
			return errors.New("RDP 协商请求发送失败: 连接未写入任何数据")
		}
	}

	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		return wrapRDPProbeIOError(ctx, "RDP 协商响应读取失败", err)
	}
	length := int(binary.BigEndian.Uint16(header[2:]))
	if header[0] != 0x03 || header[1] != 0x00 || length < 11 || length > maxRDPNegotiationResponse {
		return fmt.Errorf("RDP 协议响应无效: TPKT 头或长度异常（版本=%d，保留=%d，长度=%d）", header[0], header[1], length)
	}
	payload := make([]byte, length-4)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return wrapRDPProbeIOError(ctx, "RDP 协商响应读取失败", err)
	}
	return validateRDPNegotiationResponse(payload)
}

func validateRDPNegotiationResponse(payload []byte) error {
	if len(payload) < 7 {
		return errors.New("RDP 协议响应无效: X.224 响应过短")
	}
	x224Length := int(payload[0]) + 1
	if payload[0] < 6 || x224Length > len(payload) {
		return errors.New("RDP 协议响应无效: X.224 长度异常")
	}
	if payload[1] == 0x80 {
		return errors.New("RDP 服务器拒绝协商: 收到 X.224 断开响应")
	}
	if payload[1] != 0xd0 {
		return fmt.Errorf("RDP 协议响应无效: 期望 X.224 连接确认，收到 0x%02x", payload[1])
	}
	if x224Length == 7 {
		return nil
	}
	if x224Length < 15 {
		return errors.New("RDP 协议响应无效: RDP 协商数据不完整")
	}
	negotiation := payload[7:15]
	if binary.LittleEndian.Uint16(negotiation[2:4]) != 8 {
		return errors.New("RDP 协议响应无效: RDP 协商数据长度异常")
	}
	value := binary.LittleEndian.Uint32(negotiation[4:8])
	switch negotiation[0] {
	case 0x02:
		switch value {
		case 0x00, 0x01, 0x02, 0x04, 0x08:
			return nil
		default:
			return fmt.Errorf("RDP 协议响应无效: 未知的安全协议 0x%08x", value)
		}
	case 0x03:
		return fmt.Errorf("RDP 服务器拒绝协商: %s（代码 0x%08x）", rdpNegotiationFailureMessage(value), value)
	default:
		return fmt.Errorf("RDP 协议响应无效: 未知的协商响应类型 0x%02x", negotiation[0])
	}
}

func rdpNegotiationFailureMessage(code uint32) string {
	switch code {
	case 0x01:
		return "服务器要求 TLS"
	case 0x02:
		return "服务器不允许 TLS"
	case 0x03:
		return "服务器未配置 TLS 证书"
	case 0x04:
		return "协商标志不一致"
	case 0x05:
		return "服务器要求 CredSSP/NLA"
	case 0x06:
		return "服务器要求带用户认证的 TLS"
	default:
		return "服务器返回未知拒绝原因"
	}
}

func wrapRDPProbeIOError(ctx context.Context, stage string, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		label := "已取消"
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			label = "超时"
		}
		return fmt.Errorf("%s（%s）: %w", stage, label, errors.Join(ctxErr, err))
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		cause := err
		if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) {
			cause = errors.Join(context.DeadlineExceeded, err)
		}
		return fmt.Errorf("%s（超时）: %w", stage, cause)
	}
	return fmt.Errorf("%s: %w", stage, err)
}

type normalizedOptions struct {
	address           string
	credentialTarget  string
	username          string
	password          string
	width             int
	height            int
	displayMode       DisplayMode
	smartSizing       bool
	useMultimon       bool
	redirectDrives    bool
	redirectDriveIDs  []string
	redirectClipboard bool
	audioPlayback     AudioPlaybackMode
	audioCapture      bool
	redirectPrinters  bool
	gatewayAddress    string
	gatewayUsername   string
	gatewayUsage      GatewayUsageMethod
}

func normalizeOptions(options Options) (normalizedOptions, error) {
	address, err := normalizeAddress(options.Address)
	if err != nil {
		return normalizedOptions{}, err
	}
	if strings.ContainsAny(options.Username, "\r\n\x00") {
		return normalizedOptions{}, errors.New("用户名不能包含回车、换行或 NUL 字符")
	}
	if strings.ContainsAny(options.Password, "\r\n\x00") {
		return normalizedOptions{}, errors.New("密码不能包含回车、换行或 NUL 字符")
	}
	if options.Password != "" && options.Username == "" {
		return normalizedOptions{}, errors.New("使用密码快捷登录时用户名不能为空")
	}
	if strings.ContainsAny(options.GatewayUsername, "\r\n\x00") {
		return normalizedOptions{}, errors.New("RD Gateway 用户名不能包含回车、换行或 NUL 字符")
	}
	if strings.ContainsAny(options.GatewayPassword, "\r\n\x00") {
		return normalizedOptions{}, errors.New("RD Gateway 密码不能包含回车、换行或 NUL 字符")
	}
	if options.GatewayPassword != "" {
		return normalizedOptions{}, errors.New("暂不支持自动注入 RD Gateway 密码，请由 mstsc 安全提示输入")
	}

	width := options.DesktopWidth
	if width == 0 {
		width = defaultRDPWidth
	}
	if width < 640 || width > 7680 {
		return normalizedOptions{}, errors.New("桌面宽度必须在 640 到 7680 之间")
	}
	height := options.DesktopHeight
	if height == 0 {
		height = defaultRDPHeight
	}
	if height < 480 || height > 4320 {
		return normalizedOptions{}, errors.New("桌面高度必须在 480 到 4320 之间")
	}
	displayMode := options.DisplayMode
	if displayMode == "" {
		displayMode = DisplayModeWindow
	}
	if displayMode != DisplayModeWindow && displayMode != DisplayModeFullscreen {
		return normalizedOptions{}, errors.New("显示模式必须是 window 或 fullscreen")
	}
	if options.AudioPlayback < AudioPlaybackLocal || options.AudioPlayback > AudioPlaybackDisabled {
		return normalizedOptions{}, errors.New("音频播放模式必须是本地播放、远程播放或禁用")
	}
	driveIDs, err := normalizeDriveIDs(options.RedirectDriveIDs)
	if err != nil {
		return normalizedOptions{}, err
	}
	gatewayAddress, err := normalizeGatewayAddress(options.GatewayAddress)
	if err != nil {
		return normalizedOptions{}, err
	}
	if options.GatewayUsage < GatewayUsageNoneDirect || options.GatewayUsage > GatewayUsageNoneDetect {
		return normalizedOptions{}, errors.New("RD Gateway 使用方式必须在 0 到 4 之间")
	}
	if gatewayAddress == "" && (options.GatewayUsername != "" || options.GatewayUsage != GatewayUsageNoneDirect) {
		return normalizedOptions{}, errors.New("设置 RD Gateway 用户名或使用方式前必须填写网关地址")
	}

	target := "TERMSRV/" + address
	if len(utf16.Encode([]rune(target))) > maxCredentialTargetLength {
		return normalizedOptions{}, errors.New("RDP 凭证目标名称过长")
	}
	if len(utf16.Encode([]rune(options.Username))) > maxCredentialUsernameLength {
		return normalizedOptions{}, errors.New("RDP 用户名过长")
	}
	if len(utf16.Encode([]rune(options.Password)))*2 > maxCredentialBlobSize {
		return normalizedOptions{}, errors.New("RDP 密码过长")
	}
	if len(utf16.Encode([]rune(options.GatewayUsername))) > maxCredentialUsernameLength {
		return normalizedOptions{}, errors.New("RD Gateway 用户名过长")
	}

	return normalizedOptions{
		address:           address,
		credentialTarget:  target,
		username:          options.Username,
		password:          options.Password,
		width:             width,
		height:            height,
		displayMode:       displayMode,
		smartSizing:       options.SmartSizing,
		useMultimon:       options.UseMultimon,
		redirectDrives:    options.RedirectDrives || len(driveIDs) > 0,
		redirectDriveIDs:  driveIDs,
		redirectClipboard: options.RedirectClipboard,
		audioPlayback:     options.AudioPlayback,
		audioCapture:      options.AudioCapture,
		redirectPrinters:  options.RedirectPrinters,
		gatewayAddress:    gatewayAddress,
		gatewayUsername:   options.GatewayUsername,
		gatewayUsage:      options.GatewayUsage,
	}, nil
}

func normalizeDriveIDs(values []string) ([]string, error) {
	if len(values) > 26 {
		return nil, errors.New("指定的本地磁盘数量不能超过 26 个")
	}
	result := make([]string, 0, len(values))
	seen := make(map[byte]struct{}, len(values))
	for _, value := range values {
		if strings.ContainsAny(value, "\r\n\x00") {
			return nil, errors.New("本地磁盘标识不能包含回车、换行或 NUL 字符")
		}
		normalized := strings.ToUpper(value)
		switch len(normalized) {
		case 1:
		case 2:
			if normalized[1] != ':' {
				return nil, fmt.Errorf("本地磁盘标识 %q 无效", value)
			}
		case 3:
			if normalized[1:] != `:\` {
				return nil, fmt.Errorf("本地磁盘标识 %q 无效", value)
			}
		default:
			return nil, fmt.Errorf("本地磁盘标识 %q 无效", value)
		}
		letter := normalized[0]
		if letter < 'A' || letter > 'Z' {
			return nil, fmt.Errorf("本地磁盘标识 %q 无效", value)
		}
		if _, exists := seen[letter]; exists {
			continue
		}
		seen[letter] = struct{}{}
		result = append(result, string(letter)+`:\`)
	}
	return result, nil
}

func normalizeGatewayAddress(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}
	if raw != strings.TrimSpace(raw) {
		return "", errors.New("RD Gateway 地址首尾不能包含空白字符")
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", errors.New("RD Gateway 地址不能包含空白或控制字符")
	}

	host := raw
	port := ""
	switch {
	case strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]"):
		host = raw[1 : len(raw)-1]
		if !validIPv6Host(host) {
			return "", errors.New("RD Gateway IPv6 地址无效")
		}
		return "[" + canonicalIPHost(host) + "]", nil
	case isIPHost(raw):
		host = canonicalIPHost(raw)
		if strings.Contains(host, ":") {
			return "[" + host + "]", nil
		}
		return host, nil
	case strings.Contains(raw, ":"):
		parsedHost, parsedPort, err := net.SplitHostPort(raw)
		if err != nil {
			return "", fmt.Errorf("RD Gateway 地址或端口无效: %w", err)
		}
		value, err := strconv.Atoi(parsedPort)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("RD Gateway 端口必须在 1 到 65535 之间")
		}
		host, port = parsedHost, parsedPort
	}
	if host == "" {
		return "", errors.New("RD Gateway 主机地址不能为空")
	}
	if strings.ContainsAny(host, "[]") {
		return "", errors.New("RD Gateway 主机地址中的方括号无效")
	}
	if isIPHost(host) {
		host = canonicalIPHost(host)
	} else {
		host = strings.ToLower(host)
	}
	if port != "" {
		return net.JoinHostPort(host, port), nil
	}
	return host, nil
}

func normalizeAddress(raw string) (string, error) {
	if raw == "" {
		return "", errors.New("RDP 地址不能为空")
	}
	if raw != strings.TrimSpace(raw) {
		return "", errors.New("RDP 地址首尾不能包含空白字符")
	}
	if strings.IndexFunc(raw, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return "", errors.New("RDP 地址不能包含空白或控制字符")
	}

	host := raw
	port := defaultRDPPort
	switch {
	case strings.HasPrefix(raw, "[") && strings.HasSuffix(raw, "]"):
		host = raw[1 : len(raw)-1]
		if !validIPv6Host(host) {
			return "", errors.New("RDP IPv6 地址无效")
		}
		host = canonicalIPHost(host)
	case isIPHost(raw):
		host = canonicalIPHost(raw)
	case strings.Contains(raw, ":"):
		parsedHost, parsedPort, err := net.SplitHostPort(raw)
		if err != nil {
			return "", fmt.Errorf("RDP 地址或端口无效: %w", err)
		}
		value, err := strconv.Atoi(parsedPort)
		if err != nil || value < 1 || value > 65535 {
			return "", errors.New("RDP 端口必须在 1 到 65535 之间")
		}
		host, port = parsedHost, value
		if strings.Contains(host, ":") && !validIPv6Host(host) {
			return "", errors.New("RDP IPv6 地址无效")
		}
		if isIPHost(host) {
			host = canonicalIPHost(host)
		} else {
			host = strings.ToLower(host)
		}
	default:
		host = strings.ToLower(raw)
	}
	if host == "" {
		return "", errors.New("RDP 主机地址不能为空")
	}
	if strings.ContainsAny(host, "[]") {
		return "", errors.New("RDP 主机地址中的方括号无效")
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func isIPHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}
	return validIPv6Host(host)
}

func validIPv6Host(host string) bool {
	address := host
	if index := strings.LastIndexByte(address, '%'); index >= 0 {
		if index == 0 || index == len(address)-1 || strings.Contains(address[index+1:], "%") {
			return false
		}
		address = address[:index]
	}
	ip := net.ParseIP(address)
	return ip != nil && ip.To4() == nil
}

func canonicalIPHost(host string) string {
	zone := ""
	address := host
	if index := strings.LastIndexByte(host, '%'); index >= 0 {
		address, zone = host[:index], host[index:]
	}
	return net.ParseIP(address).String() + zone
}

func buildRDPContent(config normalizedOptions) []byte {
	drives := "0"
	driveStore := ""
	if config.redirectDrives {
		drives = "1"
		if len(config.redirectDriveIDs) > 0 {
			driveStore = strings.Join(config.redirectDriveIDs, ";") + ";"
		} else {
			driveStore = "*"
		}
	}
	screenMode := "1"
	if config.displayMode == DisplayModeFullscreen {
		screenMode = "2"
	}
	prompt := "1"
	if config.password != "" {
		prompt = "0"
	}
	lines := []string{
		"screen mode id:i:" + screenMode,
		"smart sizing:i:" + boolRDPValue(config.smartSizing),
		"use multimon:i:" + boolRDPValue(config.useMultimon),
		"desktopwidth:i:" + strconv.Itoa(config.width),
		"desktopheight:i:" + strconv.Itoa(config.height),
		"full address:s:" + config.address,
		"username:s:" + config.username,
		"redirectclipboard:i:" + boolRDPValue(config.redirectClipboard),
		"redirectprinters:i:" + boolRDPValue(config.redirectPrinters),
		"audiomode:i:" + strconv.Itoa(int(config.audioPlayback)),
		"audiocapturemode:i:" + boolRDPValue(config.audioCapture),
		"redirectdrives:i:" + drives,
		"drivestoredirect:s:" + driveStore,
		"prompt for credentials:i:" + prompt,
		"authentication level:i:2",
		"enablecredsspsupport:i:1",
	}
	if config.gatewayAddress != "" {
		lines = append(lines,
			"gatewayhostname:s:"+config.gatewayAddress,
			"gatewayusername:s:"+config.gatewayUsername,
			"gatewayusagemethod:i:"+strconv.Itoa(int(config.gatewayUsage)),
		)
	}
	text := strings.Join(lines, "\r\n") + "\r\n"
	encoded := utf16.Encode([]rune(text))
	content := make([]byte, 2+len(encoded)*2)
	content[0], content[1] = 0xff, 0xfe
	for index, value := range encoded {
		content[2+index*2] = byte(value)
		content[3+index*2] = byte(value >> 8)
	}
	return content
}

func boolRDPValue(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

func createRDPFile(content []byte) (string, func() error, error) {
	directory, err := os.MkdirTemp("", "SanTianShell-rdp-")
	if err != nil {
		return "", nil, err
	}
	path := filepath.Join(directory, "connection.rdp")
	cleanup := func() error {
		fileErr := os.Remove(path)
		if errors.Is(fileErr, os.ErrNotExist) {
			fileErr = nil
		}
		directoryErr := os.Remove(directory)
		if errors.Is(directoryErr, os.ErrNotExist) {
			directoryErr = nil
		}
		return errors.Join(fileErr, directoryErr)
	}
	if err := os.WriteFile(path, content, 0600); err != nil {
		_ = cleanup()
		return "", nil, err
	}
	return path, cleanup, nil
}

type commandProcess struct {
	command *exec.Cmd
}

func startMSTSC(rdpPath string) (managedProcess, error) {
	systemDirectory, err := windows.GetSystemDirectory()
	if err != nil {
		return nil, fmt.Errorf("获取 Windows 系统目录失败: %w", err)
	}
	command := exec.Command(filepath.Join(systemDirectory, "mstsc.exe"), rdpPath)
	if err := command.Start(); err != nil {
		return nil, err
	}
	return &commandProcess{command: command}, nil
}

func (p *commandProcess) PID() int {
	return p.command.Process.Pid
}

func (p *commandProcess) Wait() error {
	return p.command.Wait()
}

func (p *commandProcess) Kill() error {
	return p.command.Process.Kill()
}

func newSessionID() (string, error) {
	value := make([]byte, 16)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		return "", err
	}
	return "rdp-" + hex.EncodeToString(value), nil
}

func statusEvent(sessionID string, state State, pid int, err error) StatusEvent {
	event := StatusEvent{SessionID: sessionID, State: state, PID: pid, Err: err}
	if err != nil {
		event.Error = err.Error()
	}
	return event
}

func notify(callback func(StatusEvent), event StatusEvent) {
	if callback == nil {
		return
	}
	defer func() {
		_ = recover()
	}()
	callback(event)
}

func closeTunnel(tunnel io.Closer, err error) error {
	return joinCleanup(err, "关闭 RDP 隧道失败", closeOnly(tunnel))
}

func closeOnly(closer io.Closer) error {
	if closer == nil {
		return nil
	}
	return closer.Close()
}

func joinCleanup(base error, action string, cleanupErr error) error {
	if cleanupErr == nil {
		return base
	}
	wrapped := fmt.Errorf("%s: %w", action, cleanupErr)
	if base == nil {
		return wrapped
	}
	return errors.Join(base, wrapped)
}

type credentialPool struct {
	mu      sync.Mutex
	backend credentialBackend
	active  map[string]*credentialRef
}

type credentialRef struct {
	username     string
	passwordHash [32]byte
	count        int
}

func newCredentialPool(backend credentialBackend) *credentialPool {
	return &credentialPool{backend: backend, active: make(map[string]*credentialRef)}
}

func (p *credentialPool) acquire(target, username, password string) (func() error, error) {
	hash := credentialHash(username, password)
	p.mu.Lock()
	defer p.mu.Unlock()
	if current := p.active[target]; current != nil {
		if current.username != username || current.passwordHash != hash {
			return nil, ErrCredentialInUse
		}
		current.count++
		return p.releaseFunc(target), nil
	}
	if err := p.backend.Write(target, username, password); err != nil {
		return nil, err
	}
	p.active[target] = &credentialRef{username: username, passwordHash: hash, count: 1}
	return p.releaseFunc(target), nil
}

func (p *credentialPool) releaseFunc(target string) func() error {
	var once sync.Once
	var result error
	return func() error {
		once.Do(func() {
			p.mu.Lock()
			defer p.mu.Unlock()
			current := p.active[target]
			if current == nil {
				return
			}
			current.count--
			if current.count > 0 {
				return
			}
			delete(p.active, target)
			result = p.backend.Delete(target)
		})
		return result
	}
}
