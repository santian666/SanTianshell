// Package portforward 提供基于 SSH 连接的本地、远程和动态端口转发。
package portforward

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	defaultConnectTimeout   = 15 * time.Second
	defaultHandshakeTimeout = 10 * time.Second
	forwardErrorBuffer      = 8
)

// Kind 表示端口转发类型。
type Kind string

const (
	KindLocal   Kind = "local"
	KindRemote  Kind = "remote"
	KindDynamic Kind = "dynamic"
)

// State 表示端口转发生命周期状态。
type State string

const (
	StateRunning State = "running"
	StateClosing State = "closing"
	StateClosed  State = "closed"
	StateFailed  State = "failed"
)

// Options 是三类端口转发共用的参数。
// ListenPort 为 0 时由系统分配空闲端口。
type Options struct {
	ListenHost       string
	ListenPort       int
	TargetHost       string
	TargetPort       int
	AllowExternal    bool
	ConnectTimeout   time.Duration
	HandshakeTimeout time.Duration
}

// SSHDialer 是 *ssh.Client 已实现的目标连接能力。
type SSHDialer interface {
	Dial(network, address string) (net.Conn, error)
}

// SSHContextDialer 可选地提供可取消的目标连接能力。
type SSHContextDialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// SSHListener 是 *ssh.Client 已实现的远程监听能力。
type SSHListener interface {
	Listen(network, address string) (net.Listener, error)
}

// Snapshot 是可安全用于界面展示的转发状态快照。
type Snapshot struct {
	ID            string `json:"id"`
	Kind          Kind   `json:"kind"`
	State         State  `json:"state"`
	ListenAddress string `json:"listenAddress"`
	TargetAddress string `json:"targetAddress,omitempty"`
	LastError     string `json:"lastError,omitempty"`
}

// Forward 表示一个正在运行或已经结束的端口转发。
type Forward struct {
	ID   string
	Kind Kind

	listener         net.Listener
	listenAddress    string
	targetAddress    string
	connectTimeout   time.Duration
	handshakeTimeout time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	state    State
	lastErr  error
	active   map[net.Conn]struct{}
	handlers sync.WaitGroup
	stopOnce sync.Once
	doneOnce sync.Once
	done     chan struct{}
	errors   chan error
}

type preparedOptions struct {
	listenAddress    string
	targetAddress    string
	connectTimeout   time.Duration
	handshakeTimeout time.Duration
}

// Validate 在建立 SSH 连接前校验转发类型和参数。
func Validate(kind Kind, options Options) error {
	switch kind {
	case KindLocal, KindRemote, KindDynamic:
	default:
		return errors.New("端口转发类型必须是 local、remote 或 dynamic")
	}
	_, err := prepareOptions(kind, options)
	return err
}

var forwardSequence atomic.Uint64

// StartLocal 启动本地端口转发：本地监听 -> SSH -> 目标地址。
func StartLocal(client SSHDialer, options Options) (*Forward, error) {
	if client == nil {
		return nil, errors.New("SSH 客户端不能为空")
	}
	prepared, err := prepareOptions(KindLocal, options)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", prepared.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("创建本地转发监听失败: %w", err)
	}
	forward := newForward(KindLocal, listener, prepared)
	go forward.accept(func(local net.Conn) {
		forward.handleLocal(client, local)
	})
	return forward, nil
}

// StartRemote 启动远程端口转发：SSH 服务端监听 -> 当前电脑的目标地址。
func StartRemote(client SSHListener, options Options) (*Forward, error) {
	if client == nil {
		return nil, errors.New("SSH 客户端不能为空")
	}
	prepared, err := prepareOptions(KindRemote, options)
	if err != nil {
		return nil, err
	}
	listener, err := client.Listen("tcp", prepared.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("创建远程转发监听失败: %w", err)
	}
	if listener == nil {
		return nil, errors.New("创建远程转发监听失败: SSH 客户端返回了空监听器")
	}
	forward := newForward(KindRemote, listener, prepared)
	go forward.accept(func(remote net.Conn) {
		forward.handleRemote(remote)
	})
	return forward, nil
}

// StartDynamic 启动只支持无认证 CONNECT 的本地 SOCKS5 动态转发。
func StartDynamic(client SSHDialer, options Options) (*Forward, error) {
	if client == nil {
		return nil, errors.New("SSH 客户端不能为空")
	}
	prepared, err := prepareOptions(KindDynamic, options)
	if err != nil {
		return nil, err
	}
	listener, err := net.Listen("tcp", prepared.listenAddress)
	if err != nil {
		return nil, fmt.Errorf("创建动态转发监听失败: %w", err)
	}
	forward := newForward(KindDynamic, listener, prepared)
	go forward.accept(func(local net.Conn) {
		forward.handleDynamic(client, local)
	})
	return forward, nil
}

func newForward(kind Kind, listener net.Listener, options preparedOptions) *Forward {
	ctx, cancel := context.WithCancel(context.Background())
	return &Forward{
		ID:               newForwardID(kind),
		Kind:             kind,
		listener:         listener,
		listenAddress:    listener.Addr().String(),
		targetAddress:    options.targetAddress,
		connectTimeout:   options.connectTimeout,
		handshakeTimeout: options.handshakeTimeout,
		ctx:              ctx,
		cancel:           cancel,
		state:            StateRunning,
		active:           make(map[net.Conn]struct{}),
		done:             make(chan struct{}),
		errors:           make(chan error, forwardErrorBuffer),
	}
}

func newForwardID(kind Kind) string {
	return fmt.Sprintf("%s-%x-%x", kind, time.Now().UnixNano(), forwardSequence.Add(1))
}

// Address 返回实际监听地址；监听端口为 0 时可从这里取得系统分配的端口。
func (f *Forward) Address() string {
	if f == nil {
		return ""
	}
	return f.listenAddress
}

// Target 返回本地或远程转发的目标地址。
func (f *Forward) Target() string {
	if f == nil {
		return ""
	}
	return f.targetAddress
}

// State 返回当前生命周期状态。
func (f *Forward) State() State {
	if f == nil {
		return StateClosed
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.state
}

// LastError 返回最近一次连接错误或监听失败错误。
func (f *Forward) LastError() error {
	if f == nil {
		return nil
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.lastErr
}

// Snapshot 返回并发安全的状态快照。
func (f *Forward) Snapshot() Snapshot {
	if f == nil {
		return Snapshot{State: StateClosed}
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	snapshot := Snapshot{
		ID:            f.ID,
		Kind:          f.Kind,
		State:         f.state,
		ListenAddress: f.listenAddress,
		TargetAddress: f.targetAddress,
	}
	if f.lastErr != nil {
		snapshot.LastError = f.lastErr.Error()
	}
	return snapshot
}

// Done 在监听停止且所有已接收连接清理后关闭。
func (f *Forward) Done() <-chan struct{} {
	if f == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return f.done
}

// Errors 返回转发运行期错误。主动关闭引起的取消不会写入，通道在转发完全结束后关闭。
func (f *Forward) Errors() <-chan error {
	if f == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return f.errors
}

// Close 停止监听并关闭这个转发的所有活动连接；可重复调用。
func (f *Forward) Close() error {
	if f == nil {
		return nil
	}
	return f.beginShutdown(StateClosing, nil)
}

func (f *Forward) accept(handler func(net.Conn)) {
	var retryDelay time.Duration
	for {
		conn, err := f.listener.Accept()
		if err != nil {
			if f.ctx.Err() != nil {
				break
			}
			var networkErr net.Error
			if errors.As(err, &networkErr) && networkErr.Temporary() {
				if retryDelay == 0 {
					retryDelay = 5 * time.Millisecond
				} else {
					retryDelay *= 2
				}
				if retryDelay > time.Second {
					retryDelay = time.Second
				}
				timer := time.NewTimer(retryDelay)
				select {
				case <-timer.C:
					continue
				case <-f.ctx.Done():
					timer.Stop()
				}
				break
			}
			_ = f.beginShutdown(StateFailed, fmt.Errorf("接受转发连接失败: %w", err))
			break
		}
		retryDelay = 0
		if !f.track(conn) {
			_ = conn.Close()
			break
		}
		f.handlers.Add(1)
		go func() {
			defer f.handlers.Done()
			handler(conn)
		}()
	}

	f.handlers.Wait()
	f.finish()
}

func (f *Forward) handleLocal(client SSHDialer, local net.Conn) {
	defer f.release(local)
	remote, err := f.dialSSH(client, f.targetAddress)
	if err != nil {
		f.recordConnectionError(fmt.Errorf("本地转发连接目标失败: %w", err))
		return
	}
	if remote == nil {
		f.recordConnectionError(errors.New("本地转发连接目标失败: SSH 客户端返回了空连接"))
		return
	}
	if !f.track(remote) {
		_ = remote.Close()
		return
	}
	defer f.release(remote)
	bridge(local, remote)
}

func (f *Forward) handleRemote(remote net.Conn) {
	defer f.release(remote)
	dialer := net.Dialer{Timeout: f.connectTimeout}
	local, err := dialer.DialContext(f.ctx, "tcp", f.targetAddress)
	if err != nil {
		f.recordConnectionError(fmt.Errorf("远程转发连接本地目标失败: %w", err))
		return
	}
	if !f.track(local) {
		_ = local.Close()
		return
	}
	defer f.release(local)
	bridge(remote, local)
}

func (f *Forward) handleDynamic(client SSHDialer, local net.Conn) {
	defer f.release(local)
	if err := local.SetDeadline(time.Now().Add(f.handshakeTimeout)); err != nil {
		f.recordConnectionError(fmt.Errorf("设置 SOCKS5 握手超时失败: %w", err))
		return
	}
	target, err := readSOCKS5Request(local)
	if err != nil {
		f.recordConnectionError(err)
		return
	}
	remote, err := f.dialSSH(client, target)
	if err != nil {
		_ = writeSOCKS5Reply(local, 0x01)
		f.recordConnectionError(fmt.Errorf("SOCKS5 连接目标失败: %w", err))
		return
	}
	if remote == nil {
		_ = writeSOCKS5Reply(local, 0x01)
		f.recordConnectionError(errors.New("SOCKS5 连接目标失败: SSH 客户端返回了空连接"))
		return
	}
	if !f.track(remote) {
		_ = remote.Close()
		return
	}
	defer f.release(remote)
	if err := writeSOCKS5Reply(local, 0x00); err != nil {
		f.recordConnectionError(fmt.Errorf("写入 SOCKS5 成功响应失败: %w", err))
		return
	}
	if err := local.SetDeadline(time.Time{}); err != nil {
		f.recordConnectionError(fmt.Errorf("清除 SOCKS5 握手超时失败: %w", err))
		return
	}
	bridge(local, remote)
}

type dialResult struct {
	conn net.Conn
	err  error
}

func (f *Forward) dialSSH(client SSHDialer, target string) (net.Conn, error) {
	if dialer, ok := client.(SSHContextDialer); ok {
		ctx, cancel := context.WithTimeout(f.ctx, f.connectTimeout)
		defer cancel()
		conn, err := dialer.DialContext(ctx, "tcp", target)
		if f.ctx.Err() != nil {
			if conn != nil {
				_ = conn.Close()
			}
			return nil, context.Canceled
		}
		if err != nil && errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, fmt.Errorf("连接超时: %w", context.DeadlineExceeded)
		}
		return conn, err
	}

	result := make(chan dialResult)
	go func() {
		conn, err := client.Dial("tcp", target)
		// 旧式 Dial 无法单独取消；共享 SSH 客户端必须由连接池管理，不能在这里关闭。
		select {
		case result <- dialResult{conn: conn, err: err}:
		case <-f.ctx.Done():
			if conn != nil {
				_ = conn.Close()
			}
		}
	}()

	timer := time.NewTimer(f.connectTimeout)
	defer timer.Stop()
	select {
	case resolved := <-result:
		if f.ctx.Err() != nil {
			if resolved.conn != nil {
				_ = resolved.conn.Close()
			}
			return nil, context.Canceled
		}
		return resolved.conn, resolved.err
	case <-timer.C:
		timeoutErr := fmt.Errorf("连接超时: %w", context.DeadlineExceeded)
		failure := fmt.Errorf("SSH 转发连接目标 %s 失败: %w", target, timeoutErr)
		_ = f.beginShutdown(StateFailed, failure)
		return nil, timeoutErr
	case <-f.ctx.Done():
		return nil, context.Canceled
	}
}

func (f *Forward) track(conn net.Conn) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.state != StateRunning {
		return false
	}
	f.active[conn] = struct{}{}
	return true
}

func (f *Forward) release(conn net.Conn) {
	_ = conn.Close()
	f.mu.Lock()
	delete(f.active, conn)
	f.mu.Unlock()
}

func (f *Forward) recordConnectionError(err error) {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, net.ErrClosed) {
		return
	}
	f.mu.Lock()
	if f.state == StateRunning && f.ctx.Err() == nil {
		f.lastErr = err
		f.reportErrorLocked(err)
	}
	f.mu.Unlock()
}

func (f *Forward) reportErrorLocked(err error) {
	if err == nil {
		return
	}
	select {
	case f.errors <- err:
	default:
	}
}

func (f *Forward) beginShutdown(next State, cause error) error {
	var closeErr error
	f.stopOnce.Do(func() {
		f.mu.Lock()
		if next == StateFailed {
			f.state = StateFailed
			f.lastErr = cause
			f.reportErrorLocked(cause)
		} else {
			f.state = StateClosing
		}
		listener := f.listener
		connections := make([]net.Conn, 0, len(f.active))
		for conn := range f.active {
			connections = append(connections, conn)
		}
		f.mu.Unlock()

		f.cancel()
		if listener != nil {
			closeErr = listener.Close()
			if errors.Is(closeErr, net.ErrClosed) {
				closeErr = nil
			}
		}
		for _, conn := range connections {
			_ = conn.Close()
		}
	})
	return closeErr
}

func (f *Forward) finish() {
	f.mu.Lock()
	if f.state == StateRunning || f.state == StateClosing {
		f.state = StateClosed
	}
	f.mu.Unlock()
	f.doneOnce.Do(func() {
		close(f.errors)
		close(f.done)
	})
}

func bridge(left, right net.Conn) {
	var copies sync.WaitGroup
	copies.Add(2)
	go copyHalf(&copies, right, left)
	go copyHalf(&copies, left, right)
	copies.Wait()
	_ = left.Close()
	_ = right.Close()
}

func copyHalf(group *sync.WaitGroup, destination, source net.Conn) {
	defer group.Done()
	_, _ = io.Copy(destination, source)
	if closer, ok := destination.(interface{ CloseWrite() error }); ok {
		_ = closer.CloseWrite()
	}
	if closer, ok := source.(interface{ CloseRead() error }); ok {
		_ = closer.CloseRead()
	}
}

func readSOCKS5Request(conn net.Conn) (string, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil {
		return "", fmt.Errorf("读取 SOCKS5 认证协商失败: %w", err)
	}
	if header[0] != 0x05 || header[1] == 0 {
		_ = writeSOCKS5Method(conn, 0xff)
		return "", errors.New("SOCKS5 认证协商格式无效")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return "", fmt.Errorf("读取 SOCKS5 认证方式失败: %w", err)
	}
	if !containsByte(methods, 0x00) {
		_ = writeSOCKS5Method(conn, 0xff)
		return "", errors.New("SOCKS5 客户端不支持无认证方式")
	}
	if err := writeSOCKS5Method(conn, 0x00); err != nil {
		return "", fmt.Errorf("写入 SOCKS5 认证协商响应失败: %w", err)
	}

	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil {
		return "", fmt.Errorf("读取 SOCKS5 请求失败: %w", err)
	}
	if request[0] != 0x05 || request[2] != 0x00 {
		_ = writeSOCKS5Reply(conn, 0x01)
		return "", errors.New("SOCKS5 请求格式无效")
	}
	if request[1] != 0x01 {
		_ = writeSOCKS5Reply(conn, 0x07)
		return "", errors.New("SOCKS5 仅支持 CONNECT 命令")
	}
	host, err := readSOCKS5Host(conn, request[3])
	if err != nil {
		_ = writeSOCKS5Reply(conn, 0x08)
		return "", err
	}
	portBytes := make([]byte, 2)
	if _, err := io.ReadFull(conn, portBytes); err != nil {
		_ = writeSOCKS5Reply(conn, 0x01)
		return "", fmt.Errorf("读取 SOCKS5 目标端口失败: %w", err)
	}
	port := int(binary.BigEndian.Uint16(portBytes))
	if port == 0 {
		_ = writeSOCKS5Reply(conn, 0x01)
		return "", errors.New("SOCKS5 目标端口必须在 1-65535 之间")
	}
	host, err = normalizeHost(host, "SOCKS5 目标主机")
	if err != nil {
		_ = writeSOCKS5Reply(conn, 0x08)
		return "", err
	}
	return net.JoinHostPort(host, strconv.Itoa(port)), nil
}

func readSOCKS5Host(conn net.Conn, addressType byte) (string, error) {
	switch addressType {
	case 0x01:
		address := make([]byte, net.IPv4len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", fmt.Errorf("读取 SOCKS5 IPv4 地址失败: %w", err)
		}
		return net.IP(address).String(), nil
	case 0x04:
		address := make([]byte, net.IPv6len)
		if _, err := io.ReadFull(conn, address); err != nil {
			return "", fmt.Errorf("读取 SOCKS5 IPv6 地址失败: %w", err)
		}
		return net.IP(address).String(), nil
	case 0x03:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", fmt.Errorf("读取 SOCKS5 域名长度失败: %w", err)
		}
		if length[0] == 0 {
			return "", errors.New("SOCKS5 目标域名不能为空")
		}
		domain := make([]byte, int(length[0]))
		if _, err := io.ReadFull(conn, domain); err != nil {
			return "", fmt.Errorf("读取 SOCKS5 域名失败: %w", err)
		}
		return string(domain), nil
	default:
		return "", fmt.Errorf("不支持的 SOCKS5 地址类型: 0x%02x", addressType)
	}
}

func writeSOCKS5Method(conn net.Conn, method byte) error {
	_, err := conn.Write([]byte{0x05, method})
	return err
}

func writeSOCKS5Reply(conn net.Conn, reply byte) error {
	_, err := conn.Write([]byte{0x05, reply, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00})
	return err
}

func containsByte(values []byte, wanted byte) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func prepareOptions(kind Kind, options Options) (preparedOptions, error) {
	if options.ListenPort < 0 || options.ListenPort > 65535 {
		return preparedOptions{}, errors.New("监听端口必须在 0-65535 之间")
	}
	if options.ConnectTimeout < 0 {
		return preparedOptions{}, errors.New("连接超时不能为负数")
	}
	if options.HandshakeTimeout < 0 {
		return preparedOptions{}, errors.New("握手超时不能为负数")
	}
	listenHost, err := normalizeListenHost(options.ListenHost, options.AllowExternal)
	if err != nil {
		return preparedOptions{}, err
	}
	prepared := preparedOptions{
		listenAddress:    net.JoinHostPort(listenHost, strconv.Itoa(options.ListenPort)),
		connectTimeout:   options.ConnectTimeout,
		handshakeTimeout: options.HandshakeTimeout,
	}
	if prepared.connectTimeout == 0 {
		prepared.connectTimeout = defaultConnectTimeout
	}
	if prepared.handshakeTimeout == 0 {
		prepared.handshakeTimeout = defaultHandshakeTimeout
	}
	if kind == KindDynamic {
		return prepared, nil
	}
	if options.TargetPort < 1 || options.TargetPort > 65535 {
		return preparedOptions{}, errors.New("目标端口必须在 1-65535 之间")
	}
	targetHost, err := normalizeHost(options.TargetHost, "目标主机")
	if err != nil {
		return preparedOptions{}, err
	}
	prepared.targetAddress = net.JoinHostPort(targetHost, strconv.Itoa(options.TargetPort))
	return prepared, nil
}

func normalizeListenHost(value string, allowExternal bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "127.0.0.1", nil
	}
	if strings.EqualFold(value, "localhost") {
		return "127.0.0.1", nil
	}
	if value == "*" {
		value = "0.0.0.0"
	}
	host, err := normalizeHost(value, "监听地址")
	if err != nil {
		return "", err
	}
	if !allowExternal && !isLoopbackHost(host) {
		return "", errors.New("非回环监听地址必须显式启用 AllowExternal")
	}
	return host, nil
}

func normalizeHost(value, field string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s不能为空", field)
	}
	if strings.HasPrefix(value, "[") && strings.HasSuffix(value, "]") {
		candidate := strings.TrimSuffix(strings.TrimPrefix(value, "["), "]")
		if _, err := netip.ParseAddr(candidate); err == nil {
			value = candidate
		}
	}
	if strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.ContainsRune(value, '\x00') {
		return "", fmt.Errorf("%s格式无效", field)
	}
	if strings.Contains(value, ":") {
		if _, err := netip.ParseAddr(value); err != nil {
			return "", fmt.Errorf("%s不能包含端口", field)
		}
	}
	return value, nil
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	address, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	return address.Unmap().IsLoopback()
}
