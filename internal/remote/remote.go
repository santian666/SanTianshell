package remote

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

type Host struct {
	ID                   string                                                 `json:"id"`
	Name                 string                                                 `json:"name"`
	Group                string                                                 `json:"group"`
	Country              string                                                 `json:"country"`
	CountryManual        bool                                                   `json:"countryManual"`
	VPSAlias             string                                                 `json:"vpsAlias"`
	VPSDomain            string                                                 `json:"vpsDomain"`
	VPSMiddleDomain      string                                                 `json:"vpsMiddleDomain"`
	Address              string                                                 `json:"address"`
	Username             string                                                 `json:"username"`
	Password             string                                                 `json:"password"`
	KeyPath              string                                                 `json:"keyPath"`
	Tags                 string                                                 `json:"tags"`
	Remark               string                                                 `json:"remark"`
	SystemVersion        string                                                 `json:"systemVersion"`
	Protocol             string                                                 `json:"protocol"`
	AuthType             string                                                 `json:"authType"`
	KeyPassphrase        string                                                 `json:"keyPassphrase"`
	MFACode              string                                                 `json:"mfaCode"`
	Encoding             string                                                 `json:"encoding"`
	CredentialID         string                                                 `json:"credentialId"`
	KeyContent           string                                                 `json:"keyContent"`
	HostKey              string                                                 `json:"hostKey,omitempty"`
	ConfirmHostKey       func(string) error                                     `json:"-"`
	RememberHostKey      func(string) error                                     `json:"-"`
	ConnectionProgress   func(ConnectionStage)                                  `json:"-"`
	ConnectionRetry      func(int, int, time.Duration, error)                   `json:"-"`
	KeyboardInteractive  ssh.KeyboardInteractiveChallenge                       `json:"-"`
	AgentSigners         func(context.Context) ([]ssh.Signer, io.Closer, error) `json:"-"`
	KeyPassphrasePrompt  func() (string, error)                                 `json:"-"`
	ProxyHost            string                                                 `json:"proxyHost"`
	ProxyUsername        string                                                 `json:"proxyUsername"`
	ProxyPassword        string                                                 `json:"proxyPassword"`
	Port                 int                                                    `json:"port"`
	ProxyPort            int                                                    `json:"proxyPort"`
	ProxyEnabled         bool                                                   `json:"proxyEnabled"`
	RDPMapDrives         bool                                                   `json:"rdpMapDrives"`
	RDPResolution        string                                                 `json:"rdpResolution"`
	RDPDisplayMode       string                                                 `json:"rdpDisplayMode,omitempty"`
	RDPSmartSizing       bool                                                   `json:"rdpSmartSizing,omitempty"`
	RDPUseMultimon       bool                                                   `json:"rdpUseMultimon,omitempty"`
	RDPDriveIDs          []string                                               `json:"rdpDriveIds,omitempty"`
	RDPRedirectClipboard *bool                                                  `json:"rdpRedirectClipboard,omitempty"`
	RDPAudioPlayback     int                                                    `json:"rdpAudioPlayback,omitempty"`
	RDPAudioCapture      bool                                                   `json:"rdpAudioCapture,omitempty"`
	RDPRedirectPrinters  bool                                                   `json:"rdpRedirectPrinters,omitempty"`
	RDPGatewayAddress    string                                                 `json:"rdpGatewayAddress,omitempty"`
	RDPGatewayUsername   string                                                 `json:"rdpGatewayUsername,omitempty"`
	RDPGatewayPassword   string                                                 `json:"rdpGatewayPassword,omitempty"`
	RDPGatewayUsage      int                                                    `json:"rdpGatewayUsage,omitempty"`
	RDPJumpHostID        string                                                 `json:"rdpJumpHostId,omitempty"`
	SSHJumpHostID        string                                                 `json:"sshJumpHostId,omitempty"`
	JumpFingerprint      string                                                 `json:"-"`
	JumpDial             func(context.Context, string, time.Duration) (net.Conn, error) `json:"-"`
	Timeout              int                                                    `json:"timeout"`
	KeepAlive            int                                                    `json:"keepAlive"`
	Starred              bool                                                   `json:"starred"`
	Deleted              bool                                                   `json:"deleted"`
	LastUsed             string                                                 `json:"lastUsed"`
	Temporary            bool                                                   `json:"temporary,omitempty"`
}

type ConnectionStage string

const (
	StageDNS            ConnectionStage = "DNS"
	StageTCP            ConnectionStage = "TCP"
	StageSOCKS5         ConnectionStage = "SOCKS5"
	StageJumpHost       ConnectionStage = "SSH 跳板"
	StageSSHHandshake   ConnectionStage = "SSH 握手"
	StageHostKey        ConnectionStage = "主机指纹"
	StageAuthentication ConnectionStage = "认证"
	StagePTY            ConnectionStage = "PTY"
	StageSFTP           ConnectionStage = "SFTP"
)

// ConnectionError 保留连接失败阶段和底层原始错误。
type ConnectionError struct {
	Stage ConnectionStage
	Err   error
}

// PasswordRejectedError 表示服务器实际尝试并拒绝了普通密码认证。
type PasswordRejectedError struct {
	Err error
}

func (e *PasswordRejectedError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return "SSH 密码认证被服务器拒绝"
	}
	return e.Err.Error()
}

func (e *PasswordRejectedError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// IsPasswordAuthenticationFailure 判断错误是否为普通 SSH 密码被服务器拒绝。
func IsPasswordAuthenticationFailure(err error) bool {
	if err == nil || isAuthenticationTransportError(err) {
		return false
	}
	var rejectedErr *PasswordRejectedError
	return errors.As(err, &rejectedErr)
}

func isAuthenticationTransportError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) {
		return true
	}
	var networkErr net.Error
	return errors.As(err, &networkErr)
}

func isStandardPasswordRejection(err error) bool {
	return isAuthMethodRejection(err, "password")
}

func isKeyboardInteractiveAuthRejection(err error) bool {
	return isAuthMethodRejection(err, "keyboard-interactive")
}

func isAuthMethodRejection(err error, targetMethod string) bool {
	if err == nil || isAuthenticationTransportError(err) {
		return false
	}
	const prefix = "ssh: unable to authenticate, attempted methods ["
	const suffix = "], no supported methods remain"
	for current := err; current != nil; current = errors.Unwrap(current) {
		message := current.Error()
		if !strings.HasPrefix(message, prefix) || !strings.HasSuffix(message, suffix) {
			continue
		}
		methods := strings.TrimSuffix(strings.TrimPrefix(message, prefix), suffix)
		for _, method := range strings.Fields(methods) {
			if method == targetMethod {
				return true
			}
		}
	}
	return false
}

// IsPasswordPromptQuestion 判断 keyboard-interactive 挑战问题是否为普通登录密码提示。
func IsPasswordPromptQuestion(question string) bool {
	q := strings.ToLower(strings.TrimSpace(question))
	if q == "" {
		return false
	}
	for _, nonPassword := range []string{"one-time", "otp", "token", "code", "verification", "passcode", "2fa", "mfa", "totp", "duo", "pin", "验证码", "动态", "令牌"} {
		if strings.Contains(q, nonPassword) {
			return false
		}
	}
	return strings.Contains(q, "password") || strings.Contains(q, "密码") || strings.Contains(q, "口令")
}

type handshakeWatchdog struct {
	ctx      context.Context
	conn     net.Conn
	timeout  time.Duration
	mu       sync.Mutex
	timer    *time.Timer
	gen      uint64
	paused   int
	stopped  bool
	timedOut atomic.Bool
}

func newHandshakeWatchdog(ctx context.Context, conn net.Conn, timeout time.Duration) *handshakeWatchdog {
	w := &handshakeWatchdog{
		ctx:     ctx,
		conn:    conn,
		timeout: timeout,
	}
	w.mu.Lock()
	w.armLocked()
	w.mu.Unlock()
	return w
}

func (w *handshakeWatchdog) armLocked() {
	if w.stopped || w.paused > 0 {
		return
	}
	deadline := connectionDeadline(w.ctx, w.timeout)
	_ = w.conn.SetDeadline(deadline)
	wait := time.Until(deadline)
	if wait <= 0 {
		wait = time.Millisecond
	}
	if w.timer != nil {
		w.timer.Stop()
	}
	w.gen++
	gen := w.gen
	w.timer = time.AfterFunc(wait, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		if w.stopped || w.paused > 0 || w.gen != gen {
			return
		}
		w.stopped = true
		w.timedOut.Store(true)
		_ = w.conn.Close()
	})
}

func (w *handshakeWatchdog) pause() func() {
	w.mu.Lock()
	if !w.stopped {
		w.paused++
		w.gen++
		if w.timer != nil {
			w.timer.Stop()
			w.timer = nil
		}
		_ = w.conn.SetDeadline(time.Time{})
	}
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			if w.paused > 0 {
				w.paused--
			}
			w.armLocked()
			w.mu.Unlock()
		})
	}
}

func (w *handshakeWatchdog) stop() {
	w.mu.Lock()
	w.stopped = true
	w.gen++
	if w.timer != nil {
		w.timer.Stop()
		w.timer = nil
	}
	w.mu.Unlock()
}

func (e *ConnectionError) Error() string {
	if e == nil {
		return ""
	}
	if e.Err == nil {
		return fmt.Sprintf("%s 阶段失败", e.Stage)
	}
	return fmt.Sprintf("%s 阶段失败: %v", e.Stage, e.Err)
}

func (e *ConnectionError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func wrapConnectionError(stage ConnectionStage, err error) error {
	if err == nil {
		return nil
	}
	var connectionErr *ConnectionError
	if errors.As(err, &connectionErr) {
		return err
	}
	return &ConnectionError{Stage: stage, Err: err}
}

// IsRetryableConnectionError 仅依据错误类型和明确阶段判断是否值得重连。
func IsRetryableConnectionError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var connectionErr *ConnectionError
	if errors.As(err, &connectionErr) {
		switch connectionErr.Stage {
		case StageDNS, StageTCP, StageSOCKS5, StageSSHHandshake:
			return true
		case StagePTY, StageJumpHost:
			return isRetryableTransportError(connectionErr.Err)
		default:
			return false
		}
	}
	return isRetryableTransportError(err)
}

func isRetryableTransportError(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	var exitErr *ssh.ExitError
	if errors.As(err, &exitErr) {
		return false
	}
	var missingExit *ssh.ExitMissingError
	if errors.As(err, &missingExit) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed)
}

type connectionProgress struct {
	callback func(ConnectionStage)
	mu       sync.Mutex
	seen     map[ConnectionStage]struct{}
}

type handshakeCaptureConn struct {
	net.Conn
	mu       sync.Mutex
	received []byte
}

func (c *handshakeCaptureConn) Read(buffer []byte) (int, error) {
	count, err := c.Conn.Read(buffer)
	if count > 0 {
		c.mu.Lock()
		remaining := 4096 - len(c.received)
		if remaining > 0 {
			c.received = append(c.received, buffer[:min(count, remaining)]...)
		}
		c.mu.Unlock()
	}
	return count, err
}

func (c *handshakeCaptureConn) receivedText() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return string(c.received)
}

func newConnectionProgress(callback func(ConnectionStage)) *connectionProgress {
	return &connectionProgress{callback: callback, seen: make(map[ConnectionStage]struct{})}
}

func (p *connectionProgress) report(stage ConnectionStage) {
	if p == nil || p.callback == nil {
		return
	}
	p.mu.Lock()
	if _, exists := p.seen[stage]; exists {
		p.mu.Unlock()
		return
	}
	p.seen[stage] = struct{}{}
	p.mu.Unlock()
	p.callback(stage)
}

func Latency(h Host) (int64, error) {
	port := h.Port
	if port == 0 {
		if strings.EqualFold(h.Protocol, "rdp") {
			port = 3389
		} else {
			port = 22
		}
	}
	const timeout = 4 * time.Second
	start := time.Now()
	address := net.JoinHostPort(h.Address, strconv.Itoa(port))
	var conn net.Conn
	var err error
	if h.JumpDial != nil {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		conn, err = h.JumpDial(ctx, address, timeout)
	} else if h.ProxyEnabled {
		conn, err = dialSOCKS5(h, address, timeout)
	} else {
		conn, err = net.DialTimeout("tcp", address, timeout)
	}
	if err != nil {
		return -1, err
	}
	defer conn.Close()
	if h.ProxyEnabled && (h.Protocol == "" || strings.EqualFold(h.Protocol, "ssh")) {
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		if _, err = conn.Write([]byte("SSH-2.0-SanTianShell-Latency\r\n")); err != nil {
			return -1, fmt.Errorf("SOCKS5 隧道已建立，但无法向目标 SSH 服务发送延迟探测: %w", err)
		}
		var firstByte [1]byte
		if _, err = io.ReadFull(conn, firstByte[:]); err != nil {
			return -1, fmt.Errorf("SOCKS5 隧道已建立，但目标 SSH 服务未返回响应: %w", err)
		}
	}
	latency := time.Since(start).Milliseconds()
	if latency < 1 {
		latency = 1
	}
	return latency, nil
}

type TCPForward struct {
	listener net.Listener
	done     chan struct{}
	errors   chan error
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	conns    map[net.Conn]struct{}
	workers  sync.WaitGroup
	close    sync.Once
	closed   bool
}

const tcpForwardErrorBuffer = 4

func StartSOCKS5Forward(h Host, target string) (*TCPForward, int, error) {
	if !h.ProxyEnabled || strings.TrimSpace(h.ProxyHost) == "" {
		return nil, 0, errors.New("未启用 SOCKS5 代理")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, fmt.Errorf("创建 RDP 本地转发失败: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	forward := &TCPForward{
		listener: listener,
		done:     make(chan struct{}),
		errors:   make(chan error, tcpForwardErrorBuffer),
		ctx:      ctx,
		cancel:   cancel,
		conns:    make(map[net.Conn]struct{}),
	}
	go forward.serve(h, target)
	return forward, listener.Addr().(*net.TCPAddr).Port, nil
}

func (f *TCPForward) serve(h Host, target string) {
	defer func() {
		f.workers.Wait()
		close(f.errors)
		close(f.done)
	}()
	for {
		local, err := f.listener.Accept()
		if err != nil {
			return
		}
		if !f.track(local) {
			_ = local.Close()
			return
		}
		f.workers.Add(1)
		go func(local net.Conn) {
			defer f.workers.Done()
			defer f.untrack(local)
			proxy, err := dialSOCKS5Context(f.ctx, h, target, time.Duration(max(h.Timeout, 12))*time.Second)
			if err != nil {
				f.reportError(fmt.Errorf("RDP SOCKS5 转发连接失败: %w", err))
				_ = local.Close()
				return
			}
			if !f.track(proxy) {
				_ = proxy.Close()
				return
			}
			defer local.Close()
			defer proxy.Close()
			defer f.untrack(proxy)
			var wg sync.WaitGroup
			wg.Add(2)
			go f.copy(&wg, proxy, local, "客户端到 SOCKS5 代理")
			go f.copy(&wg, local, proxy, "SOCKS5 代理到客户端")
			wg.Wait()
		}(local)
	}
}

// Errors 返回转发运行期错误。通道在转发完全停止后关闭，调用方只能读取。
func (f *TCPForward) Errors() <-chan error {
	if f == nil {
		return nil
	}
	return f.errors
}

func (f *TCPForward) copy(wg *sync.WaitGroup, destination io.Writer, source io.Reader, direction string) {
	defer wg.Done()
	if _, err := io.Copy(destination, source); err != nil {
		f.reportError(fmt.Errorf("RDP 本地转发%s失败: %w", direction, err))
	}
}

func (f *TCPForward) reportError(err error) {
	if f == nil || err == nil {
		return
	}
	f.mu.Lock()
	closed := f.closed
	f.mu.Unlock()
	if closed {
		return
	}
	select {
	case f.errors <- err:
	default:
	}
}

func (f *TCPForward) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return false
	}
	f.conns[conn] = struct{}{}
	return true
}

func (f *TCPForward) untrack(conn net.Conn) {
	f.mu.Lock()
	delete(f.conns, conn)
	f.mu.Unlock()
}

func (f *TCPForward) Close() {
	if f == nil {
		return
	}
	f.close.Do(func() {
		f.mu.Lock()
		f.closed = true
		connections := make([]net.Conn, 0, len(f.conns))
		for conn := range f.conns {
			connections = append(connections, conn)
		}
		f.conns = make(map[net.Conn]struct{})
		f.mu.Unlock()
		if f.cancel != nil {
			f.cancel()
		}
		if f.listener != nil {
			_ = f.listener.Close()
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
}

type Result struct {
	Output   string `json:"output"`
	ExitCode int    `json:"exitCode"`
	Duration int64  `json:"duration"`
}

const maxCommandOutput = 4 << 20

type cappedBuffer struct {
	bytes.Buffer
	truncated bool
}

type commandStreamWriter struct {
	buffer  *cappedBuffer
	chunks  chan<- string
	pending []byte
}

func (w *commandStreamWriter) Write(value []byte) (int, error) {
	written, err := w.buffer.Write(value)
	value = append(w.pending, value...)
	emitLength := completeUTF8Length(value)
	if emitLength > 0 {
		w.chunks <- string(value[:emitLength])
	}
	w.pending = append(w.pending[:0], value[emitLength:]...)
	return written, err
}

func (w *commandStreamWriter) Flush() {
	if len(w.pending) > 0 {
		w.chunks <- string(w.pending)
		w.pending = nil
	}
}

func completeUTF8Length(value []byte) int {
	emitLength := len(value)
	for index := max(0, len(value)-(utf8.UTFMax-1)); index < len(value); index++ {
		if utf8.RuneStart(value[index]) && !utf8.FullRune(value[index:]) {
			return index
		}
	}
	return emitLength
}

func (b *cappedBuffer) Write(value []byte) (int, error) {
	length := len(value)
	remaining := maxCommandOutput - b.Len()
	if remaining > 0 {
		if remaining < len(value) {
			value = value[:remaining]
		}
		_, _ = b.Buffer.Write(value)
	}
	if length > remaining {
		b.truncated = true
	}
	return length, nil
}

func (b *cappedBuffer) Text() string {
	if b.truncated {
		return b.Buffer.String() + "\n[输出超过 4 MB，已截断]\n"
	}
	return b.Buffer.String()
}

type FileInfo struct {
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	Mode     string `json:"mode"`
	Modified string `json:"modified"`
	IsDir    bool   `json:"isDir"`
	IsLink   bool   `json:"isLink"`
}

type Shell struct {
	client         *ssh.Client
	sftp           *sftp.Client
	session        *ssh.Session
	stdin          io.WriteCloser
	done           chan error
	probeStop      chan struct{}
	probeStopOnce  sync.Once
	lastRxUnixNano atomic.Int64
	probeRunning   atomic.Bool
	release        func()
	output         func(string)
	close          sync.Once
	writeMu        sync.Mutex
	commandMu      sync.Mutex
	monitorSemOnce sync.Once
	monitorSem     chan struct{}
	streamMu       sync.Mutex
	streamCancel   context.CancelFunc
	streamSeq      uint64
	interactiveMu  sync.Mutex
	interactive    *interactiveCommandCapture
	ptyTap         *interactiveOutputTap
	promptMu       sync.RWMutex
	promptTail     string
	promptReady    bool
	promptNotify   chan struct{}
	sftpMu         sync.Mutex
	sftpPtr        sync.Mutex
	closed         bool
}

func StartShell(h Host, output func(string)) (*Shell, error) {
	client, err := Dial(h)
	if err != nil {
		return nil, err
	}
	return StartShellWithClient(client, func() { _ = client.Close() }, output)
}

func normalizePTYSize(rows, columns int) (int, int) {
	if rows < 1 || rows > 65535 {
		rows = 40
	}
	if columns < 1 || columns > 65535 {
		columns = 120
	}
	return rows, columns
}

func StartShellWithClient(client *ssh.Client, release func(), output func(string)) (*Shell, error) {
	return StartShellWithClientSize(client, 40, 120, release, output)
}

func StartShellWithClientSize(client *ssh.Client, rows, columns int, release func(), output func(string)) (*Shell, error) {
	rows, columns = normalizePTYSize(rows, columns)
	session, err := client.NewSession()
	if err != nil {
		if release != nil {
			release()
		}
		return nil, wrapConnectionError(StagePTY, err)
	}
	stdin, err := session.StdinPipe()
	if err != nil {
		_ = session.Close()
		if release != nil {
			release()
		}
		return nil, wrapConnectionError(StagePTY, err)
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		if release != nil {
			release()
		}
		return nil, wrapConnectionError(StagePTY, err)
	}
	stderr, err := session.StderrPipe()
	if err != nil {
		_ = session.Close()
		if release != nil {
			release()
		}
		return nil, wrapConnectionError(StagePTY, err)
	}
	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 115200,
		ssh.TTY_OP_OSPEED: 115200,
	}
	if err = session.RequestPty("xterm-256color", rows, columns, modes); err != nil {
		_ = session.Close()
		if release != nil {
			release()
		}
		return nil, wrapConnectionError(StagePTY, fmt.Errorf("创建交互终端失败: %w", err))
	}
	if err = session.Shell(); err != nil {
		_ = session.Close()
		if release != nil {
			release()
		}
		return nil, wrapConnectionError(StagePTY, fmt.Errorf("启动交互终端失败: %w", err))
	}
	shell := &Shell{
		client:    client,
		session:   session,
		stdin:     stdin,
		done:      make(chan error, 1),
		probeStop: make(chan struct{}),
		release:   release,
		output:    output,
	}
	shell.recordRx()
	output("[连接] 连接成功\r\n")
	chunks := make(chan string, 64)
	var readers sync.WaitGroup
	var batcher sync.WaitGroup
	batcher.Add(1)
	go func() {
		defer batcher.Done()
		runAdaptiveOutputBatcher(chunks, nil, shell.handleShellOutput)
	}()
	read := func(reader io.Reader) {
		defer readers.Done()
		buffer := make([]byte, 4096)
		pending := make([]byte, 0, utf8.UTFMax)
		for {
			n, readErr := reader.Read(buffer)
			if n > 0 {
				shell.recordRx()
				value := append(pending, buffer[:n]...)
				emitLength := completeUTF8Length(value)
				if emitLength > 0 {
					chunks <- string(value[:emitLength])
				}
				pending = append(pending[:0], value[emitLength:]...)
			}
			if readErr != nil {
				if len(pending) > 0 {
					chunks <- string(pending)
				}
				return
			}
		}
	}
	readers.Add(2)
	go read(stdout)
	go read(stderr)
	go func() {
		waitErr := session.Wait()
		shell.stopProbe()
		readers.Wait()
		close(chunks)
		batcher.Wait()
		if shell.isClosed() {
			waitErr = nil
		}
		shell.done <- waitErr
		close(shell.done)
	}()
	return shell, nil
}

const (
	adaptiveBatchShortWindow = 4 * time.Millisecond
	adaptiveBatchLongWindow  = 16 * time.Millisecond
	adaptiveBatchLargeChunk  = 8 * 1024
	adaptiveBatchFlushLimit  = 64 * 1024
)

func adaptiveOutputWindow(bytesSent int) time.Duration {
	if bytesSent >= adaptiveBatchLargeChunk {
		return adaptiveBatchLongWindow
	}
	return adaptiveBatchShortWindow
}

// runAdaptiveOutputBatcher 在空闲态首包零等待立即刷新以消除回显延迟，
// 在连续大量输出时使用短窗口动态合并，兼顾交互响应与高吞吐渲染。
func runAdaptiveOutputBatcher(chunks <-chan string, abandon <-chan struct{}, emit func(string)) {
	var pending strings.Builder
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	defer timer.Stop()
	timerActive := false

	resetTimer := func(delay time.Duration) {
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
		timer.Reset(delay)
		timerActive = true
	}

	emitIfActive := func(text string) bool {
		if text == "" {
			return true
		}
		if abandon != nil {
			select {
			case <-abandon:
				return false
			default:
			}
		}
		emit(text)
		return true
	}

	flush := func() int {
		if pending.Len() == 0 {
			return 0
		}
		text := pending.String()
		pending.Reset()
		if !emitIfActive(text) {
			return 0
		}
		return len(text)
	}

	for {
		var timerC <-chan time.Time
		if timerActive {
			timerC = timer.C
		}
		select {
		case chunk, ok := <-chunks:
			if !ok {
				flush()
				return
			}
			if !timerActive && pending.Len() == 0 {
				if !emitIfActive(chunk) {
					continue
				}
				resetTimer(adaptiveOutputWindow(len(chunk)))
				continue
			}
			pending.WriteString(chunk)
			if pending.Len() >= adaptiveBatchFlushLimit {
				sent := flush()
				if sent > 0 {
					resetTimer(adaptiveOutputWindow(sent))
				}
			}
		case <-timerC:
			if pending.Len() > 0 {
				sent := flush()
				if sent > 0 {
					resetTimer(adaptiveOutputWindow(sent))
				} else {
					timerActive = false
				}
			} else {
				timerActive = false
			}
		}
	}
}

func (s *Shell) recordRx() {
	if s == nil {
		return
	}
	now := time.Now().UnixNano()
	for {
		current := s.lastRxUnixNano.Load()
		if current >= now || s.lastRxUnixNano.CompareAndSwap(current, now) {
			return
		}
	}
}

func (s *Shell) stopProbe() {
	if s == nil || s.probeStop == nil {
		return
	}
	s.probeStopOnce.Do(func() {
		close(s.probeStop)
	})
}

const (
	writeIdleProbeThreshold = 10 * time.Second
	writeProbeWait          = 1500 * time.Millisecond
	writeProbeTimeout       = 3 * time.Second
)

func (s *Shell) maybeProbeAfterWrite() {
	if s == nil || s.client == nil || s.isClosed() {
		return
	}
	lastRx := s.lastRxUnixNano.Load()
	if lastRx > 0 && time.Since(time.Unix(0, lastRx)) <= writeIdleProbeThreshold {
		return
	}
	if !s.probeRunning.CompareAndSwap(false, true) {
		return
	}
	go s.runWriteProbe(lastRx)
}

func (s *Shell) runWriteProbe(baselineRx int64) {
	defer s.probeRunning.Store(false)
	timer := time.NewTimer(writeProbeWait)
	defer timer.Stop()
	select {
	case <-s.probeStop:
		return
	case <-timer.C:
	}
	if s.isClosed() || s.lastRxUnixNano.Load() > baselineRx {
		return
	}
	for attempt := 0; attempt < 2; attempt++ {
		if s.isClosed() || s.lastRxUnixNano.Load() > baselineRx {
			return
		}
		if err := sendKeepAlive(s.client, writeProbeTimeout); err == nil {
			s.recordRx()
			return
		}
	}
	if !s.isClosed() && s.lastRxUnixNano.Load() <= baselineRx {
		_ = s.client.Close()
	}
}

func (s *Shell) Done() <-chan error { return s.done }

func (s *Shell) Write(text string) error {
	if onlyTerminalInterrupts(text) {
		return s.writeTerminal(text, true)
	}
	s.interactiveMu.Lock()
	realPTYActive := s.ptyTap != nil
	s.interactiveMu.Unlock()
	if realPTYActive {
		return s.writeTerminal(text, true)
	}
	if !s.commandMu.TryLock() {
		return ErrInteractiveInputBusy
	}
	defer s.commandMu.Unlock()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.interactiveMu.Lock()
	busy := s.interactive != nil
	s.interactiveMu.Unlock()
	if busy {
		written, err := io.WriteString(s.stdin, text)
		if written > 0 {
			s.markTerminalInput()
			s.maybeProbeAfterWrite()
		}
		return err
	}
	written, err := io.WriteString(s.stdin, text)
	if written > 0 {
		s.markTerminalInput()
		s.maybeProbeAfterWrite()
	}
	return err
}

func (s *Shell) writeTerminal(text string, markInput bool) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	written, err := io.WriteString(s.stdin, text)
	if markInput && written > 0 {
		s.markTerminalInput()
		s.maybeProbeAfterWrite()
	}
	return err
}

func onlyTerminalInterrupts(text string) bool {
	if text == "" {
		return false
	}
	for index := 0; index < len(text); index++ {
		if text[index] != 0x03 {
			return false
		}
	}
	return true
}

func (s *Shell) Resize(rows, columns int) error {
	if s == nil || s.session == nil {
		return errors.New("SSH 会话未初始化")
	}
	if rows < 1 || rows > 65535 || columns < 1 || columns > 65535 {
		return errors.New("终端尺寸无效")
	}
	return s.session.WindowChange(rows, columns)
}

func (s *Shell) Close() {
	if s == nil {
		return
	}
	s.close.Do(func() {
		s.stopProbe()
		s.StopMonitorStream()
		s.abortInteractive(errors.New("SSH 会话已关闭"))
		s.sftpPtr.Lock()
		s.closed = true
		sftpClient := s.sftp
		s.sftp = nil
		s.sftpPtr.Unlock()
		s.promptMu.Lock()
		s.promptReady = false
		s.promptChangedLocked()
		s.promptMu.Unlock()
		if s.session != nil {
			_ = s.session.Close()
		}
		if sftpClient != nil {
			_ = sftpClient.Close()
		}
		if s.release != nil {
			s.release()
		}
	})
}

func (s *Shell) isClosed() bool {
	s.sftpPtr.Lock()
	defer s.sftpPtr.Unlock()
	return s.closed
}

func (s *Shell) Run(command string) (Result, error) {
	return s.run(command, nil)
}

func (s *Shell) RunTimeout(command string, timeout time.Duration) (Result, error) {
	return RunWithClientTimeout(s.client, command, timeout)
}

const maxMonitorConcurrency = 4

func (s *Shell) acquireMonitorSlot(timeout time.Duration) (func(), error) {
	if s == nil || s.client == nil || s.isClosed() {
		return nil, errors.New("SSH 会话未初始化")
	}
	s.monitorSemOnce.Do(func() {
		s.monitorSem = make(chan struct{}, maxMonitorConcurrency)
	})
	if timeout > 0 {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case s.monitorSem <- struct{}{}:
		case <-timer.C:
			return nil, errors.New("监控采集队列繁忙")
		}
	} else {
		s.monitorSem <- struct{}{}
	}
	if s.isClosed() {
		<-s.monitorSem
		return nil, errors.New("SSH 会话已关闭")
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			<-s.monitorSem
		})
	}, nil
}

// RunMonitorTimeout 限制单主机后台监控并发会话数（最多 4 路），避免慢指标阻塞快指标。
func (s *Shell) RunMonitorTimeout(command string, timeout time.Duration) (Result, error) {
	release, err := s.acquireMonitorSlot(timeout)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	defer release()
	return RunWithClientTimeout(s.client, command, timeout)
}

// StopMonitorStream 停止当前活动主机上的长连接监控流。
func (s *Shell) StopMonitorStream() {
	if s == nil {
		return
	}
	s.streamMu.Lock()
	s.streamSeq++
	cancel := s.streamCancel
	s.streamCancel = nil
	s.streamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StartMonitorStream 在复用 SSH 连接上启动单一持久化长连接监控会话，按帧实时回传采集数据。
func (s *Shell) StartMonitorStream(command string, onFrame func(string), onExit func(error)) error {
	if s == nil || s.client == nil || s.isClosed() {
		return errors.New("SSH 会话未初始化")
	}
	s.StopMonitorStream()
	session, err := s.client.NewSession()
	if err != nil {
		return err
	}
	stdout, err := session.StdoutPipe()
	if err != nil {
		_ = session.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.streamMu.Lock()
	if s.isClosed() {
		s.streamMu.Unlock()
		cancel()
		_ = session.Close()
		return errors.New("SSH 会话已关闭")
	}
	if s.streamCancel != nil {
		s.streamCancel()
	}
	s.streamSeq++
	seq := s.streamSeq
	s.streamCancel = cancel
	s.streamMu.Unlock()

	go func() {
		<-ctx.Done()
		_ = session.Close()
	}()

	if err := session.Start(command); err != nil {
		s.streamMu.Lock()
		if s.streamSeq == seq {
			s.streamCancel = nil
		}
		s.streamMu.Unlock()
		cancel()
		_ = session.Close()
		return err
	}

	go func() {
		const maxFrameBytes = 512 * 1024
		reader := bufio.NewReaderSize(stdout, 64*1024)
		var frameBuf strings.Builder
		inFrame := false
		var readErr error
		for {
			var line string
			line, readErr = reader.ReadString('\n')
			if line != "" {
				trimmed := strings.TrimSpace(line)
				switch trimmed {
				case "__FRAME_BEGIN__":
					frameBuf.Reset()
					inFrame = true
				case "__FRAME_END__":
					if inFrame {
						frame := strings.TrimSpace(frameBuf.String())
						frameBuf.Reset()
						inFrame = false
						if frame != "" {
							s.recordRx()
							if onFrame != nil && ctx.Err() == nil && !s.isClosed() {
								onFrame(frame)
							}
						}
					}
				default:
					if inFrame && frameBuf.Len()+len(line) <= maxFrameBytes {
						frameBuf.WriteString(line)
					}
				}
			}
			if readErr != nil {
				break
			}
		}
		waitErr := session.Wait()
		cancel()
		_ = session.Close()
		s.streamMu.Lock()
		if s.streamSeq == seq {
			s.streamCancel = nil
		}
		s.streamMu.Unlock()
		var exitErr error
		switch {
		case ctx.Err() != nil || s.isClosed():
			exitErr = nil
		case waitErr != nil:
			exitErr = waitErr
		case readErr != nil && !errors.Is(readErr, io.EOF):
			exitErr = readErr
		default:
			exitErr = errors.New("监控流已结束")
		}
		if onExit != nil {
			onExit(exitErr)
		}
	}()
	return nil
}

// SSHLatency 通过当前已建立的 SSH 连接测量一次协议往返时间，不新建 TCP 或 SSH 连接。
func (s *Shell) SSHLatency(timeout time.Duration) (int64, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	start := time.Now()
	if err := sendKeepAlive(s.client, timeout); err != nil {
		return -1, err
	}
	s.recordRx()
	latency := time.Since(start).Milliseconds()
	if latency < 1 {
		latency = 1
	}
	return latency, nil
}

// ProbeAlive 主动探测底层 SSH 连接是否仍然存活，连续两次失败则关闭连接以立刻唤醒等待中的终端会话。
func (s *Shell) ProbeAlive() bool {
	if s == nil || s.client == nil || s.isClosed() {
		return false
	}
	for attempt := 0; attempt < 2; attempt++ {
		if s.isClosed() {
			return false
		}
		if err := sendKeepAlive(s.client, writeProbeTimeout); err == nil {
			s.recordRx()
			return true
		}
	}
	if !s.isClosed() {
		_ = s.client.Close()
	}
	return false
}

func (s *Shell) RunContext(ctx context.Context, command string) (Result, error) {
	return s.runContext(ctx, command, nil)
}

func (s *Shell) RunStreaming(command string, onOutput func(string)) (Result, error) {
	return s.run(command, onOutput)
}

func (s *Shell) RunStreamingContext(ctx context.Context, command string, onOutput func(string)) (Result, error) {
	return s.runContext(ctx, command, onOutput)
}

func (s *Shell) run(command string, onOutput func(string)) (Result, error) {
	return s.runContext(context.Background(), command, onOutput)
}

func (s *Shell) runContext(ctx context.Context, command string, onOutput func(string)) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, err
	}
	start := time.Now()
	type sessionResult struct {
		session *ssh.Session
		err     error
	}
	sessionReady := make(chan sessionResult)
	go func() {
		session, err := s.client.NewSession()
		opened := sessionResult{session: session, err: err}
		select {
		case sessionReady <- opened:
		case <-ctx.Done():
			if session != nil {
				_ = session.Close()
			}
		}
	}()
	var opened sessionResult
	select {
	case opened = <-sessionReady:
	case <-ctx.Done():
		return Result{ExitCode: -1, Duration: time.Since(start).Milliseconds()}, ctx.Err()
	}
	if opened.err != nil {
		return Result{ExitCode: -1, Duration: time.Since(start).Milliseconds()}, opened.err
	}
	session := opened.session
	if err := ctx.Err(); err != nil {
		go func() { _ = session.Close() }()
		return Result{ExitCode: -1, Duration: time.Since(start).Milliseconds()}, err
	}
	var stdout, stderr cappedBuffer
	var stdoutStream, stderrStream *commandStreamWriter
	finishStreams := func() {}
	var abandonOutput chan struct{}
	if onOutput == nil {
		session.Stdout = &stdout
		session.Stderr = &stderr
	} else {
		chunks := make(chan string, 64)
		finished := make(chan struct{})
		abandonOutput = make(chan struct{})
		go func() {
			defer close(finished)
			runAdaptiveOutputBatcher(chunks, abandonOutput, onOutput)
		}()
		stdoutStream = &commandStreamWriter{buffer: &stdout, chunks: chunks}
		stderrStream = &commandStreamWriter{buffer: &stderr, chunks: chunks}
		session.Stdout = stdoutStream
		session.Stderr = stderrStream
		finishStreams = func() {
			stdoutStream.Flush()
			stderrStream.Flush()
			close(chunks)
			<-finished
		}
	}
	done := make(chan error, 1)
	go func() {
		err := session.Start(command)
		if err == nil {
			err = session.Wait()
		}
		done <- err
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		go func() { _ = session.Close() }()
		cleaned := make(chan struct{})
		go func() {
			<-done
			finishStreams()
			close(cleaned)
		}()
		timer := time.NewTimer(time.Second)
		select {
		case <-cleaned:
			timer.Stop()
		case <-timer.C:
			if abandonOutput != nil {
				close(abandonOutput)
			}
			return Result{ExitCode: -1, Duration: time.Since(start).Milliseconds()}, ctx.Err()
		}
		err = ctx.Err()
		return commandResultFromBuffers(start, err, &stdout, &stderr), err
	}
	_ = session.Close()
	finishStreams()
	return commandResultFromBuffers(start, err, &stdout, &stderr), err
}

func commandResultFromBuffers(start time.Time, err error, stdout, stderr *cappedBuffer) Result {
	code := 0
	if err != nil {
		code = -1
		if exitErr, ok := err.(*ssh.ExitError); ok {
			code = exitErr.ExitStatus()
		}
	}
	output := stdout.Text()
	if stderr.Len() > 0 || stderr.truncated {
		output += stderr.Text()
	}
	return Result{Output: output, ExitCode: code, Duration: time.Since(start).Milliseconds()}
}

func (s *Shell) WithSFTP(fn func(*sftp.Client) error) error {
	s.sftpMu.Lock()
	defer s.sftpMu.Unlock()
	s.sftpPtr.Lock()
	if s.closed {
		s.sftpPtr.Unlock()
		return errors.New("SSH 会话已关闭")
	}
	client := s.sftp
	s.sftpPtr.Unlock()
	if client == nil {
		fresh, err := sftp.NewClient(s.client)
		if err != nil {
			return wrapConnectionError(StageSFTP, fmt.Errorf("启动 SFTP 通道失败: %w", err))
		}
		s.sftpPtr.Lock()
		if s.closed {
			s.sftpPtr.Unlock()
			_ = fresh.Close()
			return errors.New("SSH 会话已关闭")
		}
		s.sftp = fresh
		client = fresh
		s.sftpPtr.Unlock()
	}
	err := fn(client)
	if err != nil {
		s.sftpPtr.Lock()
		if s.sftp == client {
			s.sftp = nil
		}
		s.sftpPtr.Unlock()
		_ = client.Close()
	}
	return err
}

func Dial(h Host) (*ssh.Client, error) {
	return DialContext(context.Background(), h)
}

const keepAliveFailureThreshold = 3

// DialContext 建立 SSH 连接，并在取消时关闭尚未交付的底层连接。
func DialContext(ctx context.Context, h Host) (*ssh.Client, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	progress := newConnectionProgress(h.ConnectionProgress)
	h.ConnectionProgress = progress.report
	pauseForPrompt := func() func() { return func() {} }
	var passwordCallbackTried bool
	var kbdPasswordTried bool
	var nonPasswordPromptErr error
	var auth []ssh.AuthMethod
	if h.AuthType == "key" || h.AuthType == "key_mfa" {
		var raw []byte
		var err error
		if h.KeyContent != "" {
			raw = []byte(h.KeyContent)
		} else if h.KeyPath != "" {
			raw, err = os.ReadFile(h.KeyPath)
		} else {
			progress.report(StageAuthentication)
			return nil, wrapConnectionError(StageAuthentication, errors.New("未配置私钥或密钥凭证"))
		}
		if err != nil {
			progress.report(StageAuthentication)
			return nil, wrapConnectionError(StageAuthentication, fmt.Errorf("读取密钥失败: %w", err))
		}
		var signer ssh.Signer
		if h.KeyPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(h.KeyPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey(raw)
			var missingPassphrase *ssh.PassphraseMissingError
			if errors.As(err, &missingPassphrase) && h.KeyPassphrasePrompt != nil {
				passphrase, promptErr := h.KeyPassphrasePrompt()
				if promptErr != nil {
					progress.report(StageAuthentication)
					return nil, wrapConnectionError(StageAuthentication, promptErr)
				}
				signer, err = ssh.ParsePrivateKeyWithPassphrase(raw, []byte(passphrase))
			}
		}
		if err != nil {
			progress.report(StageAuthentication)
			return nil, wrapConnectionError(StageAuthentication, fmt.Errorf("解析密钥失败: %w", err))
		}
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if h.AuthType == "agent" || h.AuthType == "agent_mfa" {
		if h.AgentSigners == nil {
			progress.report(StageAuthentication)
			return nil, wrapConnectionError(StageAuthentication, errors.New("未配置 Windows SSH Agent（OpenSSH / Pageant）"))
		}
		signers, closer, err := h.AgentSigners(ctx)
		if err != nil {
			progress.report(StageAuthentication)
			return nil, wrapConnectionError(StageAuthentication, fmt.Errorf("读取 SSH Agent 密钥失败: %w", err))
		}
		if closer != nil {
			defer closer.Close()
		}
		if len(signers) == 0 {
			progress.report(StageAuthentication)
			return nil, wrapConnectionError(StageAuthentication, errors.New("SSH Agent 中没有可用密钥"))
		}
		auth = append(auth, ssh.PublicKeys(signers...))
	}
	if (h.AuthType == "" || h.AuthType == "password" || h.AuthType == "password_mfa") && h.Password != "" {
		auth = append(auth, ssh.PasswordCallback(func() (string, error) {
			passwordCallbackTried = true
			return h.Password, nil
		}))
	}
	wantMFA := h.AuthType == "password_mfa" || h.AuthType == "key_mfa" || h.AuthType == "agent_mfa"
	if wantMFA && h.KeyboardInteractive == nil {
		progress.report(StageAuthentication)
		return nil, wrapConnectionError(StageAuthentication, errors.New("MFA 需要在连接时动态输入"))
	}
	if wantMFA || ((h.AuthType == "" || h.AuthType == "password") && h.Password != "") {
		auth = append(auth, ssh.KeyboardInteractive(func(user, instruction string, questions []string, echos []bool) ([]string, error) {
			if len(questions) == 0 {
				return []string{}, nil
			}
			if (h.AuthType == "" || h.AuthType == "password" || h.AuthType == "password_mfa") &&
				h.Password != "" && len(questions) == 1 && IsPasswordPromptQuestion(questions[0]) {
				if passwordCallbackTried || kbdPasswordTried {
					return nil, &PasswordRejectedError{Err: errors.New("SSH 密码认证被服务器拒绝")}
				}
				kbdPasswordTried = true
				return []string{h.Password}, nil
			}
			if h.KeyboardInteractive == nil {
				nonPasswordPromptErr = errors.New("服务器要求交互式认证（MFA/OTP），请切换为支持 MFA 的认证方式")
				return nil, nonPasswordPromptErr
			}
			resumeDeadline := pauseForPrompt()
			defer resumeDeadline()
			return h.KeyboardInteractive(user, instruction, questions, echos)
		}))
	}
	if len(auth) == 0 {
		progress.report(StageAuthentication)
		return nil, wrapConnectionError(StageAuthentication, errors.New("未配置密码或私钥"))
	}
	port := h.Port
	if port == 0 {
		port = 22
	}
	timeout := h.Timeout
	if timeout <= 0 {
		timeout = 12
	}
	config := &ssh.ClientConfig{User: h.Username, Auth: auth, Timeout: time.Duration(timeout) * time.Second}
	currentStage := StageSSHHandshake
	pendingFingerprint := ""
	config.HostKeyCallback = func(_ string, _ net.Addr, key ssh.PublicKey) error {
		currentStage = StageHostKey
		progress.report(StageHostKey)
		fingerprint := ssh.FingerprintSHA256(key)
		if h.HostKey != "" && h.HostKey != fingerprint {
			return wrapConnectionError(StageHostKey, fmt.Errorf("SSH 主机密钥已变化，已拒绝连接（保存：%s，当前：%s）", h.HostKey, fingerprint))
		}
		if h.HostKey == "" {
			if h.ConfirmHostKey == nil {
				return wrapConnectionError(StageHostKey, errors.New("SSH 主机密钥尚未确认"))
			}
			resumeDeadline := pauseForPrompt()
			if err := h.ConfirmHostKey(fingerprint); err != nil {
				resumeDeadline()
				return wrapConnectionError(StageHostKey, fmt.Errorf("SSH 主机密钥未获信任: %w", err))
			}
			resumeDeadline()
			pendingFingerprint = fingerprint
		}
		currentStage = StageAuthentication
		progress.report(StageAuthentication)
		return nil
	}
	address := net.JoinHostPort(h.Address, strconv.Itoa(port))
	var conn net.Conn
	var err error
	connectionTimeout := time.Duration(timeout) * time.Second
	switch {
	case h.JumpDial != nil:
		progress.report(StageJumpHost)
		conn, err = h.JumpDial(ctx, address, connectionTimeout)
	case h.ProxyEnabled:
		conn, err = dialSOCKS5Context(ctx, h, address, connectionTimeout)
	default:
		progress.report(StageDNS)
		progress.report(StageTCP)
		dialer := net.Dialer{Timeout: connectionTimeout}
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		if h.JumpDial != nil {
			var connErr *ConnectionError
			if errors.As(err, &connErr) {
				return nil, err
			}
			progress.report(StageJumpHost)
			return nil, wrapConnectionError(StageJumpHost, errorWithContext(ctx, err))
		}
		if h.ProxyEnabled {
			return nil, err
		}
		stage := dialErrorStage(err)
		progress.report(stage)
		return nil, wrapConnectionError(stage, errorWithContext(ctx, err))
	}
	configureTCPConnection(conn)
	if err = ctx.Err(); err != nil {
		_ = conn.Close()
		return nil, wrapConnectionError(StageTCP, err)
	}
	stopCancelWatch := closeConnectionOnCancel(ctx, conn)
	watchdog := newHandshakeWatchdog(ctx, conn, connectionTimeout)
	pauseForPrompt = watchdog.pause
	progress.report(StageSSHHandshake)
	handshakeConn := &handshakeCaptureConn{Conn: conn}
	sshConn, channels, requests, err := ssh.NewClientConn(handshakeConn, address, config)
	watchdog.stop()
	stopCancelWatch()
	if err != nil {
		_ = conn.Close()
		if watchdog.timedOut.Load() && !errors.Is(err, context.DeadlineExceeded) && ctx.Err() == nil {
			err = fmt.Errorf("%w: %w", context.DeadlineExceeded, err)
		}
		err = errorWithContext(ctx, err)
		received := handshakeConn.receivedText()
		err = explainSSHHandshakeRejection(address, received, err, h.ProxyEnabled)
		if h.ProxyEnabled && currentStage == StageSSHHandshake && !strings.Contains(strings.ToLower(received), "exceeded maxstartups") {
			err = explainProxySSHHandshakeError(address, err)
		}
		var rejectedErr *PasswordRejectedError
		if currentStage == StageAuthentication && (h.AuthType == "" || h.AuthType == "password") && h.Password != "" &&
			nonPasswordPromptErr == nil && !errors.As(err, &rejectedErr) &&
			(isStandardPasswordRejection(err) || (kbdPasswordTried && isKeyboardInteractiveAuthRejection(err))) {
			err = &PasswordRejectedError{Err: err}
		}
		return nil, wrapConnectionError(currentStage, err)
	}
	if err = ctx.Err(); err != nil {
		_ = sshConn.Close()
		return nil, wrapConnectionError(currentStage, err)
	}
	if pendingFingerprint != "" && h.RememberHostKey != nil {
		if err = h.RememberHostKey(pendingFingerprint); err != nil {
			_ = sshConn.Close()
			return nil, wrapConnectionError(StageHostKey, fmt.Errorf("保存 SSH 主机密钥失败: %w", err))
		}
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(sshConn, channels, requests)
	keepAlive := h.KeepAlive
	if keepAlive <= 0 {
		keepAlive = 30
	}
	go func() {
		ticker := time.NewTicker(time.Duration(keepAlive) * time.Second)
		defer ticker.Stop()
		closed := make(chan struct{})
		go func() {
			_ = client.Wait()
			close(closed)
		}()
		monitorKeepAlive(ticker.C, closed, func() error { return sendKeepAlive(client, 5*time.Second) }, func() { _ = client.Close() })
	}()
	return client, nil
}

func explainSSHHandshakeRejection(address, received string, err error, proxyEnabled bool) error {
	if err == nil || !strings.Contains(strings.ToLower(received), "exceeded maxstartups") {
		return err
	}
	prefix := "已连接到"
	if proxyEnabled {
		prefix = "SOCKS5 隧道已建立到"
	}
	return fmt.Errorf("%s %s，但目标 SSH 服务返回 Exceeded MaxStartups：未认证并发连接已达到服务器上限；请稍后重试，或在服务器 sshd_config 调整 MaxStartups/PerSourceMaxStartups；原始错误: %w", prefix, address, err)
}

func explainProxySSHHandshakeError(address string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(message, "handshake failed: eof") {
		return err
	}
	return fmt.Errorf("SOCKS5 隧道已建立到 %s，但代理出口在发送 SSH 服务标识前关闭连接（EOF）；此现象通常由于当前代理节点或中转服务商屏蔽了 SSH 外部端口（常见于 22 端口防黑客爆破）。建议：1. 若服务器支持，将 SSH 端口改为非 22 端口（如 9693 等）；2. 切换其他不封锁 22 端口的代理节点；3. 使用 SSH 跳板机 (Jump Host)；原始错误: %w", address, err)
}

func monitorKeepAlive(ticks <-chan time.Time, closed <-chan struct{}, send func() error, closeClient func()) {
	failures := 0
	for {
		select {
		case <-closed:
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
		}
		if err := send(); err != nil {
			failures++
			if failures >= keepAliveFailureThreshold {
				closeClient()
				return
			}
			continue
		}
		failures = 0
	}
}

func dialErrorStage(err error) ConnectionStage {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return StageDNS
	}
	return StageTCP
}

func errorWithContext(ctx context.Context, err error) error {
	contextErr := ctx.Err()
	if contextErr == nil {
		if deadline, exists := ctx.Deadline(); exists && !time.Now().Before(deadline) {
			contextErr = context.DeadlineExceeded
		}
	}
	if contextErr == nil || errors.Is(err, contextErr) {
		return err
	}
	return fmt.Errorf("%w: %w", contextErr, err)
}

func connectionDeadline(ctx context.Context, timeout time.Duration) time.Time {
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		return contextDeadline
	}
	return deadline
}

func closeConnectionOnCancel(ctx context.Context, conn net.Conn) func() {
	if ctx.Done() == nil {
		return func() {}
	}
	stopped := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-ctx.Done():
			_ = conn.Close()
		case <-stopped:
		}
	}()
	var once sync.Once
	return func() {
		once.Do(func() {
			close(stopped)
			<-finished
		})
	}
}

func sendKeepAlive(client *ssh.Client, timeout time.Duration) error {
	return sendKeepAliveContext(context.Background(), client, timeout)
}

func sendKeepAliveContext(ctx context.Context, client *ssh.Client, timeout time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		done <- err
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("SSH keep-alive 超时")
	}
}

func dialSOCKS5(h Host, target string, timeout time.Duration) (net.Conn, error) {
	return dialSOCKS5Context(context.Background(), h, target, timeout)
}

func dialSOCKS5Context(ctx context.Context, h Host, target string, timeout time.Duration) (net.Conn, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	progress := newConnectionProgress(h.ConnectionProgress)
	proxyHost := strings.TrimSpace(h.ProxyHost)
	if proxyHost == "" {
		proxyHost = "127.0.0.1"
	}
	proxyPort := h.ProxyPort
	if proxyPort == 0 {
		proxyPort = 20808
	}
	if proxyPort < 1 || proxyPort > 65535 {
		progress.report(StageSOCKS5)
		return nil, wrapConnectionError(StageSOCKS5, errors.New("SOCKS5 代理端口必须在 1-65535 之间"))
	}
	progress.report(StageDNS)
	progress.report(StageTCP)
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(proxyHost, strconv.Itoa(proxyPort)))
	if err != nil {
		stage := dialErrorStage(err)
		progress.report(stage)
		return nil, wrapConnectionError(stage, fmt.Errorf("连接 SOCKS5 代理失败: %w", errorWithContext(ctx, err)))
	}
	configureTCPConnection(conn)
	stopCancelWatch := closeConnectionOnCancel(ctx, conn)
	defer stopCancelWatch()
	progress.report(StageSOCKS5)
	fail := func(err error) (net.Conn, error) {
		_ = conn.Close()
		return nil, wrapConnectionError(StageSOCKS5, errorWithContext(ctx, err))
	}
	_ = conn.SetDeadline(connectionDeadline(ctx, timeout))
	methods := []byte{0}
	if strings.TrimSpace(h.ProxyUsername) != "" || h.ProxyPassword != "" {
		methods = append(methods, 2)
	}
	if _, err = conn.Write(append([]byte{5, byte(len(methods))}, methods...)); err != nil {
		return fail(err)
	}
	response := make([]byte, 2)
	if _, err = io.ReadFull(conn, response); err != nil {
		return fail(fmt.Errorf("读取 SOCKS5 代理认证协商响应失败: %w", err))
	}
	if response[0] != 5 || response[1] == 0xff {
		return fail(fmt.Errorf("SOCKS5 代理认证协商失败"))
	}
	if response[1] == 2 {
		user, password := []byte(h.ProxyUsername), []byte(h.ProxyPassword)
		if len(user) > 255 || len(password) > 255 {
			return fail(fmt.Errorf("SOCKS5 代理用户名或密码过长"))
		}
		request := append([]byte{1, byte(len(user))}, user...)
		request = append(request, byte(len(password)))
		request = append(request, password...)
		if _, err = conn.Write(request); err != nil {
			return fail(err)
		}
		if _, err = io.ReadFull(conn, response); err != nil {
			return fail(fmt.Errorf("读取 SOCKS5 代理认证响应失败: %w", err))
		}
		if response[1] != 0 {
			return fail(fmt.Errorf("SOCKS5 代理用户名或密码错误"))
		}
	}
	host, portText, err := net.SplitHostPort(target)
	if err != nil {
		return fail(err)
	}
	portNumber, _ := strconv.Atoi(portText)
	request := []byte{5, 1, 0}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4 := ip.To4(); ipv4 != nil {
			request = append(request, 1)
			request = append(request, ipv4...)
		} else {
			request = append(request, 4)
			request = append(request, ip.To16()...)
		}
	} else {
		if len(host) > 255 {
			return fail(fmt.Errorf("目标域名过长"))
		}
		request = append(request, 3, byte(len(host)))
		request = append(request, host...)
	}
	request = binary.BigEndian.AppendUint16(request, uint16(portNumber))
	if _, err = conn.Write(request); err != nil {
		return fail(err)
	}
	header := make([]byte, 4)
	if _, err = io.ReadFull(conn, header); err != nil {
		return fail(fmt.Errorf("读取 SOCKS5 代理响应失败: %w", err))
	}
	if header[0] != 5 {
		return fail(errors.New("SOCKS5 代理响应版本无效"))
	}
	if header[1] != 0 {
		return fail(fmt.Errorf("SOCKS5 代理连接目标失败：%s（代码 0x%02x）", socks5ReplyMessage(header[1]), header[1]))
	}
	length := 0
	switch header[3] {
	case 1:
		length = 4
	case 4:
		length = 16
	case 3:
		value := []byte{0}
		if _, err = io.ReadFull(conn, value); err != nil {
			return fail(err)
		}
		length = int(value[0])
	default:
		return fail(fmt.Errorf("SOCKS5 代理响应无效"))
	}
	if _, err = io.ReadFull(conn, make([]byte, length+2)); err != nil {
		return fail(err)
	}
	if err = ctx.Err(); err != nil {
		return fail(err)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func configureTCPConnection(conn net.Conn) {
	tcp, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcp.SetNoDelay(true)
	_ = tcp.SetKeepAlive(true)
	_ = tcp.SetKeepAlivePeriod(30 * time.Second)
}

func socks5ReplyMessage(code byte) string {
	switch code {
	case 1:
		return "代理服务器内部错误"
	case 2:
		return "代理规则拒绝连接"
	case 3:
		return "目标网络不可达"
	case 4:
		return "目标主机不可达"
	case 5:
		return "目标端口拒绝连接"
	case 6:
		return "连接 TTL 超时"
	case 7:
		return "代理不支持该命令"
	case 8:
		return "代理不支持该地址类型"
	default:
		return "未知错误"
	}
}

func Run(h Host, command string) (Result, error) {
	client, err := Dial(h)
	if err != nil {
		return Result{}, err
	}
	defer client.Close()
	return RunWithClient(client, command)
}

func RunWithClient(client *ssh.Client, command string) (Result, error) {
	return RunWithClientContext(context.Background(), client, command)
}

func RunWithClientTimeout(client *ssh.Client, command string, timeout time.Duration) (Result, error) {
	if timeout <= 0 {
		return RunWithClientContext(context.Background(), client, command)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return RunWithClientContext(ctx, client, command)
}

// RunWithClientContext 在独立 SSH Channel 中执行命令。取消时只关闭本次 Session，
// 不关闭共享 SSH Client，因此不会中断交互终端、监控或其他主机功能。
func RunWithClientContext(ctx context.Context, client *ssh.Client, command string) (Result, error) {
	return RunWithClientContextStreaming(ctx, client, command, nil)
}

func RunWithClientContextStreaming(ctx context.Context, client *ssh.Client, command string, onOutput func(string)) (Result, error) {
	return (&Shell{client: client}).runContext(ctx, command, onOutput)
}

func withSFTP(h Host, fn func(*sftp.Client) error) error {
	client, err := Dial(h)
	if err != nil {
		return err
	}
	defer client.Close()
	return withSFTPClient(client, fn)
}

func withSFTPClient(client *ssh.Client, fn func(*sftp.Client) error) error {
	s, err := sftp.NewClient(client)
	if err != nil {
		return wrapConnectionError(StageSFTP, err)
	}
	defer s.Close()
	return fn(s)
}

func listFiles(s *sftp.Client, path string) ([]FileInfo, error) {
	items, err := s.ReadDir(path)
	if err != nil {
		return nil, err
	}
	result := make([]FileInfo, 0, len(items))
	for _, item := range items {
		itemPath := pathpkg.Join(path, item.Name())
		isLink := item.Mode()&os.ModeSymlink != 0
		isDir := item.IsDir()
		if isLink {
			if target, statErr := s.Stat(itemPath); statErr == nil {
				isDir = target.IsDir()
			}
		}
		result = append(result, FileInfo{Name: item.Name(), Path: itemPath, Size: item.Size(), Mode: item.Mode().String(), Modified: item.ModTime().Format("2006-01-02 15:04"), IsDir: isDir, IsLink: isLink})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].IsDir != result[j].IsDir {
			return result[i].IsDir
		}
		return strings.ToLower(result[i].Name) < strings.ToLower(result[j].Name)
	})
	return result, nil
}

func List(h Host, path string) ([]FileInfo, error) {
	var result []FileInfo
	err := withSFTP(h, func(s *sftp.Client) error {
		var err error
		result, err = listFiles(s, path)
		return err
	})
	return result, err
}

func (s *Shell) List(path string) ([]FileInfo, error) {
	var result []FileInfo
	err := s.WithSFTP(func(client *sftp.Client) error {
		var err error
		result, err = listFiles(client, path)
		return err
	})
	return result, err
}

func (s *Shell) ListTimeout(path string, timeout time.Duration) ([]FileInfo, error) {
	return ListWithClientTimeout(s.client, path, timeout)
}

const maxRemoteTextSize = 5 << 20

func readRemoteText(client *sftp.Client, remotePath string) (string, error) {
	file, err := client.Open(remotePath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, maxRemoteTextSize+1))
	if err != nil {
		return "", err
	}
	if len(content) > maxRemoteTextSize {
		return "", errors.New("文件超过 5 MB，请下载后编辑")
	}
	if bytes.IndexByte(content, 0) >= 0 || !utf8.Valid(content) {
		return "", errors.New("非 UTF-8 文本，请下载后查看")
	}
	return string(content), nil
}

func writeRemoteFile(client *sftp.Client, remotePath string, source io.Reader) error {
	info, err := client.Lstat(remotePath)
	if os.IsNotExist(err) {
		return errors.New("服务器原文件已不存在，请刷新目录")
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		remotePath, err = client.RealPath(remotePath)
		if err != nil {
			return fmt.Errorf("解析远程符号链接失败: %w", err)
		}
		info, err = client.Stat(remotePath)
		if err != nil {
			return err
		}
	}
	if info.IsDir() {
		return errors.New("不能将目录作为文件保存")
	}

	temporary, temporaryPath, err := createRemoteTemporaryFile(client, remotePath)
	if err != nil {
		return err
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if !committed {
			_ = client.Remove(temporaryPath)
		}
	}()
	if _, err = io.Copy(temporary, source); err != nil {
		return fmt.Errorf("写入远程临时文件失败: %w", err)
	}
	err = temporary.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("关闭远程临时文件失败: %w", err)
	}
	mode := info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)
	if err = client.Chmod(temporaryPath, mode); err != nil {
		return fmt.Errorf("保留原文件权限失败: %w", err)
	}
	if err = replaceRemoteFile(client, temporaryPath, remotePath, true); err != nil {
		return err
	}
	committed = true
	return nil
}

func uploadRemoteFile(client *sftp.Client, localPath, remotePath string) error {
	if _, err := client.Lstat(remotePath); err == nil {
		return errors.New("远程目标已存在，请先重命名或删除")
	} else if !os.IsNotExist(err) {
		return err
	}

	source, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer source.Close()
	temporary, temporaryPath, err := createRemoteTemporaryFile(client, remotePath)
	if err != nil {
		return err
	}
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if !committed {
			_ = client.Remove(temporaryPath)
		}
	}()
	if _, err = io.Copy(temporary, source); err != nil {
		return fmt.Errorf("上传文件失败: %w", err)
	}
	err = temporary.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("完成远程文件写入失败: %w", err)
	}
	if _, err = client.Lstat(remotePath); err == nil {
		return errors.New("远程目标已存在，请先重命名或删除")
	} else if !os.IsNotExist(err) {
		return err
	}
	if err = client.Rename(temporaryPath, remotePath); err != nil {
		return fmt.Errorf("完成上传失败: %w", err)
	}
	committed = true
	return nil
}

func downloadRemoteFile(client *sftp.Client, remotePath, localPath string) error {
	source, err := client.Open(remotePath)
	if err != nil {
		return err
	}
	defer source.Close()
	temporary, err := os.CreateTemp(filepath.Dir(localPath), ".santianshell-download-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	closed := false
	committed := false
	defer func() {
		if !closed {
			_ = temporary.Close()
		}
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err = io.Copy(temporary, source); err != nil {
		return fmt.Errorf("下载文件失败: %w", err)
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("保存下载文件失败: %w", err)
	}
	err = temporary.Close()
	closed = true
	if err != nil {
		return fmt.Errorf("关闭下载临时文件失败: %w", err)
	}
	if err = replaceLocalFile(temporaryPath, localPath); err != nil {
		return err
	}
	committed = true
	return nil
}

func createRemoteTemporaryFile(client *sftp.Client, target string) (*sftp.File, string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return nil, "", err
		}
		candidate := pathpkg.Join(pathpkg.Dir(target), "."+pathpkg.Base(target)+".santianshell-"+suffix+".tmp")
		file, err := client.OpenFile(candidate, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err == nil {
			return file, candidate, nil
		}
		if !os.IsExist(err) {
			return nil, "", err
		}
	}
	return nil, "", errors.New("无法创建远程临时文件")
}

func replaceRemoteFile(client *sftp.Client, temporaryPath, targetPath string, targetExists bool) error {
	if !targetExists {
		if err := client.Rename(temporaryPath, targetPath); err != nil {
			return fmt.Errorf("保存远程文件失败: %w", err)
		}
		return nil
	}
	if _, supported := client.HasExtension("posix-rename@openssh.com"); supported {
		err := client.PosixRename(temporaryPath, targetPath)
		if err == nil {
			return nil
		}
		var statusError *sftp.StatusError
		if !errors.As(err, &statusError) || statusError.FxCode() != sftp.ErrSSHFxOpUnsupported {
			return fmt.Errorf("原子替换远程文件失败: %w", err)
		}
	}
	backupPath, err := unusedRemotePath(client, targetPath, ".bak")
	if err != nil {
		return err
	}
	if err = client.Rename(targetPath, backupPath); err != nil {
		return fmt.Errorf("创建远程文件备份失败: %w", err)
	}
	if err = client.Rename(temporaryPath, targetPath); err != nil {
		rollbackErr := client.Rename(backupPath, targetPath)
		if rollbackErr != nil {
			return fmt.Errorf("替换远程文件失败: %v；回滚也失败，原文件保留在 %s: %w", err, backupPath, rollbackErr)
		}
		return fmt.Errorf("替换远程文件失败，已恢复原文件: %w", err)
	}
	_ = client.Remove(backupPath)
	return nil
}

func replaceLocalFile(temporaryPath, targetPath string) error {
	info, err := os.Lstat(targetPath)
	if os.IsNotExist(err) {
		if err = os.Rename(temporaryPath, targetPath); err != nil {
			return fmt.Errorf("保存下载文件失败: %w", err)
		}
		return nil
	}
	if err != nil {
		return err
	}
	if info.IsDir() {
		return errors.New("下载目标是目录")
	}
	backupPath, err := unusedLocalPath(targetPath)
	if err != nil {
		return err
	}
	if err = os.Rename(targetPath, backupPath); err != nil {
		return fmt.Errorf("备份原下载文件失败: %w", err)
	}
	if err = os.Rename(temporaryPath, targetPath); err != nil {
		rollbackErr := os.Rename(backupPath, targetPath)
		if rollbackErr != nil {
			return fmt.Errorf("替换下载文件失败: %v；回滚也失败，原文件保留在 %s: %w", err, backupPath, rollbackErr)
		}
		return fmt.Errorf("替换下载文件失败，已恢复原文件: %w", err)
	}
	_ = os.Remove(backupPath)
	return nil
}

func renameRemoteFile(client *sftp.Client, oldPath, newPath string) error {
	if pathpkg.Clean(oldPath) == pathpkg.Clean(newPath) {
		return nil
	}
	if _, err := client.Lstat(newPath); err == nil {
		return errors.New("目标名称已存在")
	} else if !os.IsNotExist(err) {
		return err
	}
	return client.Rename(oldPath, newPath)
}

func unusedRemotePath(client *sftp.Client, targetPath, extension string) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}
		candidate := pathpkg.Join(pathpkg.Dir(targetPath), "."+pathpkg.Base(targetPath)+".santianshell-"+suffix+extension)
		if _, err = client.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("无法创建远程备份路径")
}

func unusedLocalPath(targetPath string) (string, error) {
	for attempt := 0; attempt < 10; attempt++ {
		suffix, err := randomSuffix()
		if err != nil {
			return "", err
		}
		candidate := filepath.Join(filepath.Dir(targetPath), "."+filepath.Base(targetPath)+".santianshell-"+suffix+".bak")
		if _, err = os.Lstat(candidate); os.IsNotExist(err) {
			return candidate, nil
		} else if err != nil {
			return "", err
		}
	}
	return "", errors.New("无法创建本地备份路径")
}

func randomSuffix() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("生成临时文件名失败: %w", err)
	}
	return hex.EncodeToString(value), nil
}

func (s *Shell) Read(path string) (string, error) {
	var content string
	err := s.WithSFTP(func(client *sftp.Client) error {
		var err error
		content, err = readRemoteText(client, path)
		return err
	})
	return content, err
}

func (s *Shell) WriteFile(path, content string) error {
	if err := validateRemoteTextContent(content); err != nil {
		return err
	}
	return s.WithSFTP(func(client *sftp.Client) error {
		return writeRemoteFile(client, path, strings.NewReader(content))
	})
}

func (s *Shell) Upload(local, remotePath string) error {
	return s.WithSFTP(func(client *sftp.Client) error {
		return uploadRemoteFile(client, local, remotePath)
	})
}

func (s *Shell) Download(remotePath, local string) error {
	return s.WithSFTP(func(client *sftp.Client) error {
		return downloadRemoteFile(client, remotePath, local)
	})
}

func (s *Shell) CreateFile(path string) error {
	return s.WithSFTP(func(c *sftp.Client) error {
		return createRemoteEmptyFile(c, path)
	})
}
func (s *Shell) CreateDirectory(path string) error {
	return s.WithSFTP(func(c *sftp.Client) error { return c.Mkdir(path) })
}
func (s *Shell) Rename(oldPath, newPath string) error {
	return s.WithSFTP(func(c *sftp.Client) error { return renameRemoteFile(c, oldPath, newPath) })
}
func (s *Shell) Remove(path string) error {
	return s.WithSFTP(func(c *sftp.Client) error { return removeSFTP(c, path) })
}
func (s *Shell) Chmod(path string, mode os.FileMode) error {
	return s.WithSFTP(func(c *sftp.Client) error { return c.Chmod(path, mode) })
}

func ListWithClient(client *ssh.Client, path string) ([]FileInfo, error) {
	var result []FileInfo
	err := withSFTPClient(client, func(s *sftp.Client) error {
		var err error
		result, err = listFiles(s, path)
		return err
	})
	return result, err
}

func ListWithClientTimeout(client *ssh.Client, path string, timeout time.Duration) ([]FileInfo, error) {
	if timeout <= 0 {
		return ListWithClient(client, path)
	}
	type listResult struct {
		files []FileInfo
		err   error
	}
	done := make(chan listResult, 1)
	ready := make(chan *sftp.Client)
	cancel := make(chan struct{})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	go func() {
		s, err := sftp.NewClient(client)
		if err != nil {
			done <- listResult{err: wrapConnectionError(StageSFTP, err)}
			return
		}
		select {
		case ready <- s:
		case <-cancel:
			_ = s.Close()
			return
		}
		defer s.Close()
		files, listErr := listFiles(s, path)
		done <- listResult{files: files, err: listErr}
	}()
	var s *sftp.Client
	for {
		select {
		case s = <-ready:
		case result := <-done:
			return result.files, result.err
		case <-timer.C:
			close(cancel)
			if s != nil {
				_ = s.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
			}
			return nil, fmt.Errorf("读取远程目录超过 %s", timeout)
		}
	}
}

func validateRemoteTextContent(content string) error {
	if len(content) > maxRemoteTextSize {
		return errors.New("文件超过 5 MB，请下载后编辑")
	}
	if strings.IndexByte(content, 0) >= 0 || !utf8.ValidString(content) {
		return errors.New("只能保存 UTF-8 文本")
	}
	return nil
}

func createRemoteEmptyFile(c *sftp.Client, path string) error {
	f, err := c.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
	if err != nil {
		return err
	}
	return f.Close()
}

func ReadWithClient(client *ssh.Client, path string) (string, error) {
	var result string
	err := withSFTPClient(client, func(s *sftp.Client) error {
		var err error
		result, err = readRemoteText(s, path)
		return err
	})
	return result, err
}

func WriteWithClient(client *ssh.Client, path, content string) error {
	if err := validateRemoteTextContent(content); err != nil {
		return err
	}
	return withSFTPClient(client, func(s *sftp.Client) error {
		return writeRemoteFile(s, path, strings.NewReader(content))
	})
}

func UploadWithClient(client *ssh.Client, local, remotePath string) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return uploadRemoteFile(s, local, remotePath) })
}

func DownloadWithClient(client *ssh.Client, remotePath, local string) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return downloadRemoteFile(s, remotePath, local) })
}

func CreateFileWithClient(client *ssh.Client, path string) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return createRemoteEmptyFile(s, path) })
}

func CreateDirectoryWithClient(client *ssh.Client, path string) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return s.Mkdir(path) })
}

func RenameWithClient(client *ssh.Client, oldPath, newPath string) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return renameRemoteFile(s, oldPath, newPath) })
}

func RemoveWithClient(client *ssh.Client, path string) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return removeSFTP(s, path) })
}

func ChmodWithClient(client *ssh.Client, path string, mode os.FileMode) error {
	return withSFTPClient(client, func(s *sftp.Client) error { return s.Chmod(path, mode) })
}

func InitialDirectory(h Host) (string, error) {
	err := withSFTP(h, func(s *sftp.Client) error {
		_, err := s.Stat("/")
		return err
	})
	if err != nil {
		return "", fmt.Errorf("访问服务器根目录失败: %w", err)
	}
	return "/", nil
}

func Read(h Host, path string) (string, error) {
	var content string
	err := withSFTP(h, func(s *sftp.Client) error {
		var err error
		content, err = readRemoteText(s, path)
		return err
	})
	return content, err
}
func Write(h Host, path, content string) error {
	if err := validateRemoteTextContent(content); err != nil {
		return err
	}
	return withSFTP(h, func(s *sftp.Client) error {
		return writeRemoteFile(s, path, strings.NewReader(content))
	})
}
func Upload(h Host, local, remotePath string) error {
	return withSFTP(h, func(s *sftp.Client) error {
		return uploadRemoteFile(s, local, remotePath)
	})
}
func Download(h Host, remotePath, local string) error {
	return withSFTP(h, func(s *sftp.Client) error {
		return downloadRemoteFile(s, remotePath, local)
	})
}

func CreateFile(h Host, path string) error {
	return withSFTP(h, func(s *sftp.Client) error {
		f, err := s.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL)
		if err != nil {
			return err
		}
		return f.Close()
	})
}

func CreateDirectory(h Host, path string) error {
	return withSFTP(h, func(s *sftp.Client) error { return s.Mkdir(path) })
}

func Rename(h Host, oldPath, newPath string) error {
	return withSFTP(h, func(s *sftp.Client) error { return renameRemoteFile(s, oldPath, newPath) })
}

func Remove(h Host, path string) error {
	return withSFTP(h, func(s *sftp.Client) error { return removeSFTP(s, path) })
}

func removeSFTP(s *sftp.Client, path string) error {
	cleanPath := pathpkg.Clean(path)
	if path == "" || cleanPath == "." || cleanPath == "/" || !pathpkg.IsAbs(cleanPath) {
		return errors.New("禁止删除服务器根目录")
	}
	return removeSFTPEntry(s, cleanPath)
}

func removeSFTPEntry(s *sftp.Client, path string) error {
	info, err := s.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return s.Remove(path)
	}
	entries, err := s.ReadDir(path)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Name() == "." || entry.Name() == ".." {
			return errors.New("远程目录包含无效条目")
		}
		if err := removeSFTPEntry(s, pathpkg.Join(path, entry.Name())); err != nil {
			return err
		}
	}
	return s.RemoveDirectory(path)
}

func Chmod(h Host, path string, mode os.FileMode) error {
	return withSFTP(h, func(s *sftp.Client) error { return s.Chmod(path, mode) })
}
