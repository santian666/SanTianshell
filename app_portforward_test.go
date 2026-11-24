package main

import (
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"santianshell/internal/portforward"
	"santianshell/internal/rdp"
	"santianshell/internal/remote"
)

func TestNormalizePortForwardRequest(t *testing.T) {
	tests := []struct {
		name    string
		request PortForwardRequest
		want    portforward.Kind
		wantErr bool
	}{
		{name: "本地转发", request: PortForwardRequest{HostID: "host-1", Kind: "LOCAL", TargetHost: "server.example", TargetPort: 22, ConnectTimeoutSeconds: 15}, want: portforward.KindLocal},
		{name: "远程转发", request: PortForwardRequest{HostID: "host-1", Kind: "remote", TargetHost: "127.0.0.1", TargetPort: 8080}, want: portforward.KindRemote},
		{name: "动态转发", request: PortForwardRequest{HostID: "host-1", Kind: "dynamic", ListenPort: 0}, want: portforward.KindDynamic},
		{name: "缺少主机", request: PortForwardRequest{Kind: "dynamic"}, wantErr: true},
		{name: "类型无效", request: PortForwardRequest{HostID: "host-1", Kind: "udp"}, wantErr: true},
		{name: "目标端口无效", request: PortForwardRequest{HostID: "host-1", Kind: "local", TargetHost: "server.example"}, wantErr: true},
		{name: "外部监听未确认", request: PortForwardRequest{HostID: "host-1", Kind: "local", ListenHost: "0.0.0.0", TargetHost: "server.example", TargetPort: 22}, wantErr: true},
		{name: "连接超时过大", request: PortForwardRequest{HostID: "host-1", Kind: "dynamic", ConnectTimeoutSeconds: 3601}, wantErr: true},
		{name: "握手超时为负", request: PortForwardRequest{HostID: "host-1", Kind: "dynamic", HandshakeTimeoutSeconds: -1}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			kind, options, err := normalizePortForwardRequest(test.request)
			if test.wantErr {
				if err == nil {
					t.Fatalf("危险或无效参数未被拒绝: %#v", options)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind != test.want {
				t.Fatalf("转发类型 = %q，期望 %q", kind, test.want)
			}
		})
	}
}

func TestPortForwardConcurrentStopReleasesSSHOnce(t *testing.T) {
	app := newPortForwardTestApp("host-1")
	var releases atomic.Int32
	entry := trackTestDynamicForward(t, app, "host-1", func() { releases.Add(1) })

	const workers = 20
	var group sync.WaitGroup
	errorsFound := make(chan error, workers)
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			if err := app.StopPortForward(entry.forward.ID); err != nil {
				errorsFound <- err
			}
		}()
	}
	group.Wait()
	close(errorsFound)
	for err := range errorsFound {
		t.Errorf("并发停止失败: %v", err)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("SSH release 调用次数 = %d，期望 1", got)
	}
	items, err := app.ListPortForwards()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("停止后仍有端口转发: %#v", items)
	}
}

func TestPortForwardCleanupByHostAndShutdown(t *testing.T) {
	app := newPortForwardTestApp("host-a", "host-b")
	var releases atomic.Int32
	trackTestDynamicForward(t, app, "host-a", func() { releases.Add(1) })
	trackTestDynamicForward(t, app, "host-a", func() { releases.Add(1) })
	trackTestDynamicForward(t, app, "host-b", func() { releases.Add(1) })

	app.stopPortForwardsForHost("host-a")
	if got := releases.Load(); got != 2 {
		t.Fatalf("按主机清理后的 SSH release 次数 = %d，期望 2", got)
	}
	items, err := app.ListPortForwards()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].HostID != "host-b" {
		t.Fatalf("按主机清理结果错误: %#v", items)
	}

	app.closeAllPortForwards()
	if got := releases.Load(); got != 3 {
		t.Fatalf("全局清理后的 SSH release 次数 = %d，期望 3", got)
	}
	items, err = app.ListPortForwards()
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Fatalf("全局清理后仍有端口转发: %#v", items)
	}
}

func TestShutdownRaceDoesNotLeaveNewPortForward(t *testing.T) {
	app := newPortForwardTestApp("host-1")
	forward, err := portforward.StartDynamic(unusedForwardDialer{}, portforward.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var releases atomic.Int32
	entry := &appPortForward{
		hostID:    "host-1",
		forward:   forward,
		lease:     newSSHForwardLease(forward, func() { releases.Add(1) }),
		createdAt: time.Now(),
	}

	app.mu.Lock()
	locked := true
	defer func() {
		if locked {
			app.mu.Unlock()
		}
	}()
	trackStarted := make(chan struct{})
	trackDone := make(chan error, 1)
	go func() {
		close(trackStarted)
		trackDone <- app.trackPortForward(entry)
	}()
	<-trackStarted
	shutdownDone := make(chan struct{})
	go func() {
		app.shutdown(context.Background())
		close(shutdownDone)
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for !app.shuttingDown.Load() {
		select {
		case <-deadline.C:
			t.Fatal("关机状态未及时建立")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	app.mu.Unlock()
	locked = false

	trackErr := <-trackDone
	if trackErr != nil {
		_ = entry.lease.Close()
	}
	select {
	case <-shutdownDone:
	case <-time.After(time.Second):
		t.Fatal("关机清理未结束")
	}
	app.portForwardMu.RLock()
	remaining := len(app.portForwards)
	app.portForwardMu.RUnlock()
	if remaining != 0 {
		t.Fatalf("关机竞态后仍残留端口转发: %d", remaining)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("关机竞态后的 SSH release 次数 = %d，期望 1", got)
	}
}

func TestSSHForwardLeaseExposesErrorsAndAutoReleasesOnce(t *testing.T) {
	sentinel := errors.New("RDP 跳板 SSH Dial 原始错误")
	forward, err := portforward.StartLocal(appFailingForwardDialer{err: sentinel}, portforward.Options{
		TargetHost: "rdp.example",
		TargetPort: 3389,
	})
	if err != nil {
		t.Fatal(err)
	}
	var releases atomic.Int32
	resource := io.Closer(newSSHForwardLease(forward, func() { releases.Add(1) }))
	errorSource, ok := resource.(interface{ Errors() <-chan error })
	if !ok {
		_ = resource.Close()
		t.Fatal("RDP SSH 跳板资源未暴露运行期错误通道")
	}
	conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
	if err != nil {
		_ = resource.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case runtimeErr := <-errorSource.Errors():
		if !errors.Is(runtimeErr, sentinel) {
			t.Fatalf("跳板错误未保留 SSH Dial 原始错误: %v", runtimeErr)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 RDP SSH 跳板运行期错误")
	}

	if err := forward.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-forward.Done():
	case <-time.After(time.Second):
		t.Fatal("RDP SSH 跳板未停止")
	}
	deadline := time.Now().Add(time.Second)
	for releases.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("转发自行结束后的 SSH release 次数 = %d，期望 1", got)
	}
	if err := resource.Close(); err != nil {
		t.Fatal(err)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("重复关闭后的 SSH release 次数 = %d，期望仍为 1", got)
	}
}

func TestSSHForwardLeaseReleasesAfterDialTimeout(t *testing.T) {
	dialer := newAppBlockingForwardDialer()
	t.Cleanup(func() { _ = dialer.Close() })
	forward, err := portforward.StartLocal(dialer, portforward.Options{
		TargetHost:     "rdp.example",
		TargetPort:     3389,
		ConnectTimeout: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	var releases atomic.Int32
	released := make(chan struct{})
	var releasedOnce sync.Once
	lease := newSSHForwardLease(forward, func() {
		releases.Add(1)
		releasedOnce.Do(func() { close(released) })
	})
	conn, err := net.DialTimeout("tcp", forward.Address(), time.Second)
	if err != nil {
		_ = lease.Close()
		t.Fatal(err)
	}
	defer conn.Close()
	select {
	case <-dialer.started:
	case <-time.After(time.Second):
		_ = lease.Close()
		t.Fatal("RDP 跳板 SSH Dial 未开始")
	}
	select {
	case <-forward.Done():
	case <-time.After(time.Second):
		_ = lease.Close()
		t.Fatal("RDP 跳板拨号超时后转发未结束")
	}
	select {
	case <-released:
	case <-time.After(time.Second):
		t.Fatal("RDP 跳板拨号超时后 lease 未释放")
	}
	runtimeErr, ok := <-lease.Errors()
	if !ok || !errors.Is(runtimeErr, context.DeadlineExceeded) {
		t.Fatalf("RDP 跳板拨号超时错误无效: %v", runtimeErr)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("RDP 跳板拨号超时后的 SSH release 次数 = %d，期望 1", got)
	}
	if err := lease.Close(); err != nil {
		t.Fatal(err)
	}
	if got := releases.Load(); got != 1 {
		t.Fatalf("重复关闭后的 SSH release 次数 = %d，期望仍为 1", got)
	}
	select {
	case <-dialer.done:
		t.Fatal("单个隧道超时不应关闭共享 SSH 客户端")
	default:
	}
	if err := dialer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-dialer.done:
	case <-time.After(time.Second):
		t.Fatal("手动解除阻塞后 SSH Dial 未退出")
	}
}

func TestProbeRDPAddressUsesNegotiationHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	requestRead := make(chan []byte, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request := make([]byte, 19)
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			return
		}
		requestRead <- request
		_, _ = conn.Write([]byte{0x03, 0x00, 0x00, 0x0b, 0x06, 0xd0, 0x00, 0x00, 0x00, 0x00, 0x00})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probeRDPAddress(ctx, listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	select {
	case request := <-requestRead:
		if len(request) != 19 || request[0] != 0x03 || request[5] != 0xe0 {
			t.Fatalf("RDP 协商请求无效: %v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("未收到 RDP 协商请求")
	}
}

func TestRDPHostConfigUsesJumpAndAlwaysClosesTunnel(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request := make([]byte, 19)
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			return
		}
		_, _ = conn.Write([]byte{0x03, 0x00, 0x00, 0x0b, 0x06, 0xd0, 0x00, 0x00, 0x00, 0x00, 0x00})
	}()

	var directCalls, closes atomic.Int32
	latency, err := testRDPHostConfig(remote.Host{
		Address: "rdp.internal", Port: 3390, Timeout: 1, RDPJumpHostID: "jump-1",
	}, func(jumpID, target string, timeout time.Duration) (string, io.Closer, error) {
		if jumpID != "jump-1" || target != "rdp.internal:3390" || timeout != 12*time.Second {
			t.Fatalf("跳板参数错误: %q %q %v", jumpID, target, timeout)
		}
		return listener.Addr().String(), closeFunc(func() { closes.Add(1) }), nil
	}, func(remote.Host) (int64, error) {
		directCalls.Add(1)
		return 0, errors.New("跳板分支不应直连目标")
	})
	if err != nil {
		t.Fatal(err)
	}
	if latency < 0 || directCalls.Load() != 0 || closes.Load() != 1 {
		t.Fatalf("跳板测试结果异常: latency=%d direct=%d closes=%d", latency, directCalls.Load(), closes.Load())
	}
}

func TestRDPHostConfigWithoutJumpUsesDirectLatency(t *testing.T) {
	var jumpCalls atomic.Int32
	latency, err := testRDPHostConfig(remote.Host{Address: "rdp.example"}, func(string, string, time.Duration) (string, io.Closer, error) {
		jumpCalls.Add(1)
		return "", nil, errors.New("不应启动跳板")
	}, func(host remote.Host) (int64, error) {
		if host.Address != "rdp.example" {
			t.Fatalf("直连主机错误: %#v", host)
		}
		return 27, nil
	})
	if err != nil || latency != 27 || jumpCalls.Load() != 0 {
		t.Fatalf("直连测试结果异常: latency=%d jumps=%d err=%v", latency, jumpCalls.Load(), err)
	}
}

func TestRDPHostConfigGatewayUsesGatewayEndpoint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		_ = conn.Close()
		close(accepted)
	}()
	var jumpCalls, directCalls atomic.Int32
	latency, err := testRDPHostConfig(remote.Host{
		Address:           "rdp.internal",
		RDPGatewayAddress: listener.Addr().String(),
		RDPGatewayUsage:   int(rdp.GatewayUsageAlways),
	}, func(string, string, time.Duration) (string, io.Closer, error) {
		jumpCalls.Add(1)
		return "", nil, errors.New("Gateway 分支不应启动 SSH 跳板")
	}, func(remote.Host) (int64, error) {
		directCalls.Add(1)
		return 0, errors.New("Gateway 分支不应直连 RDP 目标")
	})
	if err != nil || latency < 0 || jumpCalls.Load() != 0 || directCalls.Load() != 0 {
		t.Fatalf("Gateway 预检结果异常: latency=%d jumps=%d direct=%d err=%v", latency, jumpCalls.Load(), directCalls.Load(), err)
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("未连接实际 RD Gateway 地址")
	}
}

func TestRDPJumpRejectsSOCKS5AndGatewayCombination(t *testing.T) {
	for _, host := range []remote.Host{
		{Protocol: "rdp", ProxyEnabled: true, RDPJumpHostID: "jump"},
		{Protocol: "rdp", RDPGatewayAddress: "gateway.example", RDPJumpHostID: "jump"},
	} {
		if err := normalizeRDPHostOptions(&host); err == nil {
			t.Fatalf("冲突的 RDP 路由未被拒绝: %#v", host)
		}
	}
}

type unusedForwardDialer struct{}

func (unusedForwardDialer) Dial(string, string) (net.Conn, error) {
	return nil, errors.New("测试不应建立转发连接")
}

type appFailingForwardDialer struct {
	err error
}

func (d appFailingForwardDialer) Dial(string, string) (net.Conn, error) {
	return nil, d.err
}

type appBlockingForwardDialer struct {
	started   chan struct{}
	unblock   chan struct{}
	done      chan struct{}
	startOnce sync.Once
	closeOnce sync.Once
	doneOnce  sync.Once
}

func newAppBlockingForwardDialer() *appBlockingForwardDialer {
	return &appBlockingForwardDialer{
		started: make(chan struct{}),
		unblock: make(chan struct{}),
		done:    make(chan struct{}),
	}
}

func (d *appBlockingForwardDialer) Dial(string, string) (net.Conn, error) {
	d.startOnce.Do(func() { close(d.started) })
	<-d.unblock
	d.doneOnce.Do(func() { close(d.done) })
	return nil, net.ErrClosed
}

func (d *appBlockingForwardDialer) Close() error {
	d.closeOnce.Do(func() { close(d.unblock) })
	return nil
}

func newPortForwardTestApp(hostIDs ...string) *App {
	hosts := make([]remote.Host, 0, len(hostIDs))
	for _, id := range hostIDs {
		hosts = append(hosts, remote.Host{ID: id, Protocol: "ssh"})
	}
	return &App{
		unlocked:       true,
		settings:       Settings{Hosts: hosts},
		portForwards:   make(map[string]*appPortForward),
		temporaryHosts: make(map[string]remote.Host),
	}
}

func trackTestDynamicForward(t *testing.T, app *App, hostID string, release func()) *appPortForward {
	t.Helper()
	forward, err := portforward.StartDynamic(unusedForwardDialer{}, portforward.Options{})
	if err != nil {
		t.Fatal(err)
	}
	entry := &appPortForward{
		hostID:    hostID,
		forward:   forward,
		lease:     newSSHForwardLease(forward, release),
		createdAt: time.Now(),
	}
	if err := app.trackPortForward(entry); err != nil {
		_ = entry.lease.Close()
		t.Fatal(err)
	}
	return entry
}
