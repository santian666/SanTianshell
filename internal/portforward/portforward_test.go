package portforward

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

var (
	_ SSHDialer   = (*ssh.Client)(nil)
	_ SSHListener = (*ssh.Client)(nil)
	_ io.Closer   = (*ssh.Client)(nil)
)

func TestPrepareOptionsRestrictsListenAddress(t *testing.T) {
	tests := []struct {
		name    string
		options Options
		wantErr bool
	}{
		{name: "默认回环地址", options: Options{TargetHost: "example.com", TargetPort: 22}},
		{name: "IPv4 回环地址", options: Options{ListenHost: "127.0.0.2", TargetHost: "example.com", TargetPort: 22}},
		{name: "IPv6 回环地址", options: Options{ListenHost: "::1", TargetHost: "example.com", TargetPort: 22}},
		{name: "拒绝任意地址", options: Options{ListenHost: "0.0.0.0", TargetHost: "example.com", TargetPort: 22}, wantErr: true},
		{name: "显式允许任意地址", options: Options{ListenHost: "0.0.0.0", AllowExternal: true, TargetHost: "example.com", TargetPort: 22}},
		{name: "拒绝局域网地址", options: Options{ListenHost: "192.168.1.10", TargetHost: "example.com", TargetPort: 22}, wantErr: true},
		{name: "监听端口越界", options: Options{ListenPort: 65536, TargetHost: "example.com", TargetPort: 22}, wantErr: true},
		{name: "目标端口为零", options: Options{TargetHost: "example.com", TargetPort: 0}, wantErr: true},
		{name: "目标主机带端口", options: Options{TargetHost: "example.com:22", TargetPort: 22}, wantErr: true},
		{name: "连接超时为负数", options: Options{TargetHost: "example.com", TargetPort: 22, ConnectTimeout: -time.Second}, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			prepared, err := prepareOptions(KindLocal, test.options)
			if test.wantErr {
				if err == nil {
					t.Fatalf("预期参数校验失败，实际通过: %+v", prepared)
				}
				return
			}
			if err != nil {
				t.Fatalf("参数校验不应失败: %v", err)
			}
			if prepared.listenAddress == "" || prepared.targetAddress == "" {
				t.Fatalf("校验后地址不完整: %+v", prepared)
			}
		})
	}
}

func TestStartLocalForwardsAndCloses(t *testing.T) {
	target := newEchoServer(t)
	targetHost, targetPort := splitAddress(t, target.Addr().String())
	client := directDialer{}

	forward, err := StartLocal(client, Options{
		TargetHost: targetHost,
		TargetPort: targetPort,
	})
	if err != nil {
		t.Fatalf("启动本地转发失败: %v", err)
	}
	if forward.ID == "" || forward.State() != StateRunning {
		t.Fatalf("本地转发初始状态错误: %+v", forward.Snapshot())
	}

	assertEcho(t, forward.Address(), "local-forward")
	if err := forward.Close(); err != nil {
		t.Fatalf("关闭本地转发失败: %v", err)
	}
	waitDone(t, forward)
	if forward.State() != StateClosed {
		t.Fatalf("关闭后的状态应为 closed，实际 %s", forward.State())
	}
	if err := forward.Close(); err != nil {
		t.Fatalf("重复关闭应成功: %v", err)
	}
}

func TestLocalForwardReportsOriginalDialError(t *testing.T) {
	sentinel := errors.New("SSH Dial 原始错误")
	forward, err := StartLocal(failingDialer{err: sentinel}, Options{
		TargetHost: "target.example",
		TargetPort: 3389,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
	if err != nil {
		_ = forward.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	_, _ = conn.Read(make([]byte, 1))

	select {
	case runtimeErr := <-forward.Errors():
		if !errors.Is(runtimeErr, sentinel) {
			t.Fatalf("运行期错误未保留 SSH Dial 原始错误: %v", runtimeErr)
		}
		if forward.LastError() == nil || !errors.Is(forward.LastError(), sentinel) {
			t.Fatalf("LastError 未保留 SSH Dial 原始错误: %v", forward.LastError())
		}
	case <-time.After(time.Second):
		t.Fatal("未收到本地转发运行期错误")
	}

	if err := forward.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, forward)
	assertErrorsClosed(t, forward.Errors())
}

func TestLegacyDialTimeoutDoesNotCloseSharedClient(t *testing.T) {
	dialer := newBlockingDialer()
	t.Cleanup(dialer.unblockDial)
	forward, err := StartLocal(dialer, Options{
		TargetHost:     "target.example",
		TargetPort:     3389,
		ConnectTimeout: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
	if err != nil {
		_ = forward.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	waitSignal(t, dialer.started, "SSH Dial 未开始")
	waitDone(t, forward)

	if got := dialer.closeCount.Load(); got != 0 {
		t.Fatalf("单个隧道超时不应关闭共享 SSH 客户端，实际关闭 %d 次", got)
	}
	if forward.State() != StateFailed {
		t.Fatalf("拨号超时后转发状态 = %s，期望 failed", forward.State())
	}
	runtimeErr, ok := <-forward.Errors()
	if !ok || !errors.Is(runtimeErr, context.DeadlineExceeded) {
		t.Fatalf("拨号超时错误无效: %v", runtimeErr)
	}
	assertErrorsClosed(t, forward.Errors())

	dialer.unblockDial()
	waitSignal(t, dialer.done, "解除阻塞后 SSH Dial 未退出")
}

func TestCloseDoesNotCloseSharedClientDuringLegacyDial(t *testing.T) {
	dialer := newBlockingDialer()
	t.Cleanup(dialer.unblockDial)
	forward, err := StartLocal(dialer, Options{
		TargetHost:     "target.example",
		TargetPort:     3389,
		ConnectTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
	if err != nil {
		_ = forward.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	waitSignal(t, dialer.started, "SSH Dial 未开始")
	if err := forward.Close(); err != nil {
		t.Fatal(err)
	}
	waitDone(t, forward)

	if got := dialer.closeCount.Load(); got != 0 {
		t.Fatalf("停止单个隧道不应关闭共享 SSH 客户端，实际关闭 %d 次", got)
	}
	if forward.State() != StateClosed {
		t.Fatalf("主动关闭后的状态 = %s，期望 closed", forward.State())
	}
	assertErrorsClosed(t, forward.Errors())

	dialer.unblockDial()
	waitSignal(t, dialer.done, "解除阻塞后 SSH Dial 未退出")
}

func TestStartRemoteForwardsToLocalTarget(t *testing.T) {
	target := newEchoServer(t)
	targetHost, targetPort := splitAddress(t, target.Addr().String())
	client := &localRemoteListener{}

	forward, err := StartRemote(client, Options{
		TargetHost: targetHost,
		TargetPort: targetPort,
	})
	if err != nil {
		t.Fatalf("启动远程转发失败: %v", err)
	}
	t.Cleanup(func() {
		_ = forward.Close()
		waitDone(t, forward)
	})

	assertEcho(t, forward.Address(), "remote-forward")
	if client.requested != "127.0.0.1:0" {
		t.Fatalf("远程监听默认地址错误: %s", client.requested)
	}
}

func TestStartDynamicSupportsIPv4IPv6AndDomain(t *testing.T) {
	client := &recordingDialer{}
	forward, err := StartDynamic(client, Options{})
	if err != nil {
		t.Fatalf("启动动态转发失败: %v", err)
	}
	t.Cleanup(func() {
		_ = forward.Close()
		waitDone(t, forward)
	})

	tests := []struct {
		name        string
		addressType byte
		host        string
		port        int
		wantTarget  string
	}{
		{name: "IPv4", addressType: 0x01, host: "203.0.113.9", port: 8080, wantTarget: "203.0.113.9:8080"},
		{name: "IPv6", addressType: 0x04, host: "2001:db8::8", port: 443, wantTarget: "[2001:db8::8]:443"},
		{name: "域名", addressType: 0x03, host: "example.test", port: 8443, wantTarget: "example.test:8443"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
			if err != nil {
				t.Fatalf("连接动态转发失败: %v", err)
			}
			defer conn.Close()
			if err := socks5Connect(conn, test.addressType, test.host, test.port); err != nil {
				t.Fatalf("SOCKS5 CONNECT 失败: %v", err)
			}
			payload := []byte("dynamic-forward")
			if _, err := conn.Write(payload); err != nil {
				t.Fatalf("写入动态转发失败: %v", err)
			}
			response := make([]byte, len(payload))
			if _, err := io.ReadFull(conn, response); err != nil {
				t.Fatalf("读取动态转发失败: %v", err)
			}
			if string(response) != string(payload) {
				t.Fatalf("动态转发内容错误: %q", response)
			}
			if got := client.lastTarget(); got != test.wantTarget {
				t.Fatalf("SSH Dial 目标错误，实际 %q，预期 %q", got, test.wantTarget)
			}
		})
	}
}

func TestDynamicRejectsUnsupportedAuthentication(t *testing.T) {
	forward, err := StartDynamic(&recordingDialer{}, Options{})
	if err != nil {
		t.Fatalf("启动动态转发失败: %v", err)
	}
	t.Cleanup(func() {
		_ = forward.Close()
		waitDone(t, forward)
	})

	conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
	if err != nil {
		t.Fatalf("连接动态转发失败: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
		t.Fatalf("写入认证协商失败: %v", err)
	}
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("读取认证协商响应失败: %v", err)
	}
	if response[0] != 0x05 || response[1] != 0xff {
		t.Fatalf("应拒绝用户名密码认证，实际响应 %v", response)
	}
}

func TestForwardIDsAreUnique(t *testing.T) {
	first, err := StartDynamic(&recordingDialer{}, Options{})
	if err != nil {
		t.Fatalf("启动第一个动态转发失败: %v", err)
	}
	second, err := StartDynamic(&recordingDialer{}, Options{})
	if err != nil {
		_ = first.Close()
		t.Fatalf("启动第二个动态转发失败: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("转发 ID 必须唯一: %s", first.ID)
	}
	_ = first.Close()
	_ = second.Close()
	waitDone(t, first)
	waitDone(t, second)
}

type directDialer struct{}

func (directDialer) Dial(network, address string) (net.Conn, error) {
	return net.DialTimeout(network, address, time.Second)
}

type failingDialer struct {
	err error
}

func (d failingDialer) Dial(string, string) (net.Conn, error) {
	return nil, d.err
}

type blockingDialer struct {
	started    chan struct{}
	unblock    chan struct{}
	done       chan struct{}
	startOnce  sync.Once
	closeOnce  sync.Once
	doneOnce   sync.Once
	closeCount atomic.Int32
}

func newBlockingDialer() *blockingDialer {
	return &blockingDialer{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (d *blockingDialer) Dial(string, string) (net.Conn, error) {
	d.startOnce.Do(func() { close(d.started) })
	<-d.unblock
	d.doneOnce.Do(func() { close(d.done) })
	return nil, net.ErrClosed
}

func (d *blockingDialer) Close() error {
	d.closeCount.Add(1)
	d.unblockDial()
	return nil
}

func (d *blockingDialer) unblockDial() {
	d.closeOnce.Do(func() { close(d.unblock) })
}

type localRemoteListener struct {
	requested string
}

func (l *localRemoteListener) Listen(_ string, address string) (net.Listener, error) {
	l.requested = address
	return net.Listen("tcp", "127.0.0.1:0")
}

type recordingDialer struct {
	mu      sync.Mutex
	targets []string
}

func (d *recordingDialer) Dial(_ string, address string) (net.Conn, error) {
	d.mu.Lock()
	d.targets = append(d.targets, address)
	d.mu.Unlock()
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		_, _ = io.Copy(server, server)
	}()
	return client, nil
}

func (d *recordingDialer) lastTarget() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.targets) == 0 {
		return ""
	}
	return d.targets[len(d.targets)-1]
}

func newEchoServer(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("创建测试回显服务失败: %v", err)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	return listener
}

func assertEcho(t *testing.T, address, message string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatalf("连接转发地址失败: %v", err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatalf("设置测试连接超时失败: %v", err)
	}
	if _, err := conn.Write([]byte(message)); err != nil {
		t.Fatalf("写入转发连接失败: %v", err)
	}
	response := make([]byte, len(message))
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatalf("读取转发连接失败: %v", err)
	}
	if string(response) != message {
		t.Fatalf("转发内容错误: %q", response)
	}
}

func splitAddress(t *testing.T, address string) (string, int) {
	t.Helper()
	host, portText, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatalf("拆分测试地址失败: %v", err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatalf("解析测试端口失败: %v", err)
	}
	return host, port
}

func waitDone(t *testing.T, forward *Forward) {
	t.Helper()
	select {
	case <-forward.Done():
	case <-time.After(2 * time.Second):
		t.Fatalf("等待转发关闭超时: %+v", forward.Snapshot())
	}
}

func waitSignal(t *testing.T, signal <-chan struct{}, failure string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(failure)
	}
}

func assertErrorsClosed(t *testing.T, runtimeErrors <-chan error) {
	t.Helper()
	select {
	case runtimeErr, ok := <-runtimeErrors:
		if ok {
			t.Fatalf("错误通道关闭前仍有未预期错误: %v", runtimeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("转发结束后错误通道未关闭")
	}
}

func socks5Connect(conn net.Conn, addressType byte, host string, port int) error {
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return err
	}
	method := make([]byte, 2)
	if _, err := io.ReadFull(conn, method); err != nil {
		return err
	}
	if method[0] != 0x05 || method[1] != 0x00 {
		return errors.New("服务端未接受无认证方式")
	}

	request := []byte{0x05, 0x01, 0x00, addressType}
	switch addressType {
	case 0x01:
		address := net.ParseIP(host).To4()
		if address == nil {
			return errors.New("测试 IPv4 地址无效")
		}
		request = append(request, address...)
	case 0x04:
		address := net.ParseIP(host).To16()
		if address == nil {
			return errors.New("测试 IPv6 地址无效")
		}
		request = append(request, address...)
	case 0x03:
		if len(host) > 255 {
			return errors.New("测试域名过长")
		}
		request = append(request, byte(len(host)))
		request = append(request, host...)
	default:
		return errors.New("测试地址类型无效")
	}
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)
	if _, err := conn.Write(request); err != nil {
		return err
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		return err
	}
	if reply[0] != 0x05 || reply[1] != 0x00 {
		return errors.New("服务端拒绝 CONNECT 请求")
	}
	return conn.SetDeadline(time.Time{})
}
