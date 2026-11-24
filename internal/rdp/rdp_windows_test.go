package rdp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf16"
)

func TestNormalizeAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "域名默认端口", input: "Server.Example", want: "server.example:3389"},
		{name: "域名自定义端口", input: "Server.Example:3390", want: "server.example:3390"},
		{name: "IPv4", input: "192.0.2.10:3391", want: "192.0.2.10:3391"},
		{name: "裸IPv6", input: "2001:db8::1", want: "[2001:db8::1]:3389"},
		{name: "带端口IPv6", input: "[2001:0DB8::1]:3392", want: "[2001:db8::1]:3392"},
		{name: "带区域IPv6", input: "[FE80::1%12]:3393", want: "[fe80::1%12]:3393"},
		{name: "回车注入", input: "server\r\nusername:s:attacker", wantErr: true},
		{name: "换行注入", input: "server\nfull address:s:attacker", wantErr: true},
		{name: "端口过大", input: "server:65536", wantErr: true},
		{name: "端口非数字", input: "server:rdp", wantErr: true},
		{name: "空地址", input: "", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeAddress(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalizeAddress(%q) 未返回错误", test.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeAddress(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalizeAddress(%q) = %q，期望 %q", test.input, got, test.want)
			}
		})
	}
}

func TestRDPContentDoesNotContainPassword(t *testing.T) {
	t.Parallel()
	config, err := normalizeOptions(Options{
		Address:        "[2001:db8::8]:3390",
		Username:       "域名\\管理员",
		Password:       "不能写进文件-Secret!",
		DesktopWidth:   1600,
		DesktopHeight:  900,
		RedirectDrives: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := decodeRDPContent(t, buildRDPContent(config))
	if strings.Contains(content, config.password) {
		t.Fatal("RDP 配置中包含明文密码")
	}
	for _, line := range []string{
		"screen mode id:i:1",
		"smart sizing:i:0",
		"use multimon:i:0",
		"full address:s:[2001:db8::8]:3390",
		"username:s:域名\\管理员",
		"desktopwidth:i:1600",
		"desktopheight:i:900",
		"redirectclipboard:i:0",
		"redirectprinters:i:0",
		"audiomode:i:0",
		"audiocapturemode:i:0",
		"redirectdrives:i:1",
		"drivestoredirect:s:*",
		"prompt for credentials:i:0",
		"authentication level:i:2",
	} {
		if !strings.Contains(content, line+"\r\n") {
			t.Errorf("RDP 配置缺少 %q", line)
		}
	}
}

func TestRDPContentMapsAdvancedOptions(t *testing.T) {
	t.Parallel()
	config, err := normalizeOptions(Options{
		Address:           "server.example:3390",
		Username:          `域名\管理员`,
		Password:          "RDP-Secret!",
		DesktopWidth:      1920,
		DesktopHeight:     1080,
		DisplayMode:       DisplayModeFullscreen,
		SmartSizing:       true,
		UseMultimon:       true,
		RedirectDrives:    true,
		RedirectDriveIDs:  []string{"c", `D:\`},
		RedirectClipboard: true,
		AudioPlayback:     AudioPlaybackDisabled,
		AudioCapture:      true,
		RedirectPrinters:  true,
		GatewayAddress:    "Gateway.Example:443",
		GatewayUsername:   `域名\网关用户`,
		GatewayUsage:      GatewayUsageFallback,
	})
	if err != nil {
		t.Fatal(err)
	}
	content := decodeRDPContent(t, buildRDPContent(config))
	if strings.Contains(content, "RDP-Secret!") {
		t.Fatal("高级 RDP 配置中包含登录密码")
	}
	for _, line := range []string{
		"screen mode id:i:2",
		"smart sizing:i:1",
		"use multimon:i:1",
		"redirectclipboard:i:1",
		"redirectprinters:i:1",
		"audiomode:i:2",
		"audiocapturemode:i:1",
		`drivestoredirect:s:C:\;D:\;`,
		"gatewayhostname:s:gateway.example:443",
		`gatewayusername:s:域名\网关用户`,
		"gatewayusagemethod:i:2",
	} {
		if !strings.Contains(content, line+"\r\n") {
			t.Errorf("RDP 配置缺少 %q", line)
		}
	}
}

func TestNormalizeGatewayAddress(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "空地址", input: "", want: ""},
		{name: "域名", input: "Gateway.Example", want: "gateway.example"},
		{name: "域名端口", input: "Gateway.Example:444", want: "gateway.example:444"},
		{name: "裸IPv6", input: "2001:0DB8::10", want: "[2001:db8::10]"},
		{name: "IPv6端口", input: "[2001:0DB8::10]:444", want: "[2001:db8::10]:444"},
		{name: "换行注入", input: "gateway.example\r\nusername:s:attacker", wantErr: true},
		{name: "端口无效", input: "gateway.example:70000", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := normalizeGatewayAddress(test.input)
			if test.wantErr {
				if err == nil {
					t.Fatalf("normalizeGatewayAddress(%q) 未返回错误", test.input)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeGatewayAddress(%q): %v", test.input, err)
			}
			if got != test.want {
				t.Fatalf("normalizeGatewayAddress(%q) = %q，期望 %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeOptionsRejectsLineInjection(t *testing.T) {
	t.Parallel()
	tests := []Options{
		{Address: "server\r\nfull address:s:attacker"},
		{Address: "server", Username: "admin\r\nredirectdrives:i:1"},
		{Address: "server", Username: "admin", Password: "abc\x00def"},
		{Address: "server", Username: "admin", Password: "abc\r\ndef"},
		{Address: "server", GatewayAddress: "gateway\r\nfull address:s:attacker"},
		{Address: "server", GatewayAddress: "gateway", GatewayUsername: "admin\nredirectprinters:i:1"},
		{Address: "server", RedirectDriveIDs: []string{"C", "D\r\nfull address:s:attacker"}},
		{Address: "server", DisplayMode: DisplayMode("fullscreen\r\nusername:s:attacker")},
		{Address: "server", AudioPlayback: AudioPlaybackMode(3)},
		{Address: "server", GatewayUsage: GatewayUsageMethod(5)},
		{Address: "server", GatewayUsername: "admin", GatewayUsage: GatewayUsageAlways},
	}
	for _, options := range tests {
		if _, err := normalizeOptions(options); err == nil {
			t.Fatalf("危险配置未被拒绝: %#v", options)
		}
	}
}

func TestProbeRDPAddressNegotiatesRDP(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan error, 1)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer conn.Close()
		request := make([]byte, len(rdpNegotiationRequest))
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			serverDone <- readErr
			return
		}
		if string(request) != string(rdpNegotiationRequest) {
			serverDone <- fmt.Errorf("收到的 RDP 协商请求不正确: %v", request)
			return
		}
		_, writeErr := conn.Write([]byte{
			0x03, 0x00, 0x00, 0x13,
			0x0e, 0xd0, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x02, 0x00, 0x08, 0x00, 0x02, 0x00, 0x00, 0x00,
		})
		serverDone <- writeErr
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := probeRDPAddress(ctx, listener.Addr().String()); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestProbeRDPAddressReportsServerRejection(t *testing.T) {
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
		request := make([]byte, len(rdpNegotiationRequest))
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			return
		}
		_, _ = conn.Write([]byte{
			0x03, 0x00, 0x00, 0x13,
			0x0e, 0xd0, 0x00, 0x00, 0x00, 0x00, 0x00,
			0x03, 0x00, 0x08, 0x00, 0x05, 0x00, 0x00, 0x00,
		})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = probeRDPAddress(ctx, listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "RDP 服务器拒绝协商") || !strings.Contains(err.Error(), "CredSSP/NLA") {
		t.Fatalf("服务器拒绝协商错误不明确: %v", err)
	}
}

func TestProbeRDPAddressRejectsInvalidProtocolResponse(t *testing.T) {
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
		request := make([]byte, len(rdpNegotiationRequest))
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			return
		}
		_, _ = conn.Write([]byte{0x03, 0x00, 0x00, 0x0b, 0x06, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00})
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = probeRDPAddress(ctx, listener.Addr().String())
	if err == nil || !strings.Contains(err.Error(), "RDP 协议响应无效") {
		t.Fatalf("无效协议响应未被拒绝: %v", err)
	}
}

func TestProbeRDPAddressPreservesTimeoutAndNetworkError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan struct{})
	release := make(chan struct{})
	defer close(release)
	go func() {
		conn, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer conn.Close()
		request := make([]byte, len(rdpNegotiationRequest))
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			return
		}
		close(accepted)
		<-release
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	err = probeRDPAddress(ctx, listener.Addr().String())
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("超时错误不明确或错误链丢失: %v", err)
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("底层网络超时错误未保留: %v", err)
	}
	select {
	case <-accepted:
	default:
		t.Fatal("测试连接未进入 RDP 协商阶段")
	}
}

func TestGatewayPreflightAddressDefaultsTo443(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "Gateway.Example", want: "gateway.example:443"},
		{input: "Gateway.Example:444", want: "gateway.example:444"},
		{input: "2001:db8::10", want: "[2001:db8::10]:443"},
		{input: "[2001:db8::10]:444", want: "[2001:db8::10]:444"},
	}
	for _, test := range tests {
		got, err := gatewayPreflightAddress(test.input)
		if err != nil {
			t.Fatalf("gatewayPreflightAddress(%q): %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("gatewayPreflightAddress(%q) = %q，期望 %q", test.input, got, test.want)
		}
	}
}

func TestProbeGatewayPreservesTCPError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = ProbeGateway(ctx, address)
	if err == nil || !strings.Contains(err.Error(), "RD Gateway TCP 连接失败") {
		t.Fatalf("Gateway TCP 阶段不明确: %v", err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("Gateway 底层网络错误未保留: %v", err)
	}
}

func TestRunnerGatewaySkipsTargetRDPProbe(t *testing.T) {
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
	process := newFakeProcess(4050)
	var protocolCalls atomic.Int32
	runner := newRunner(runnerDeps{
		credentials: &fakeCredentialBackend{},
		newID:       fixedID("rdp-gateway"),
		protocolProbe: func(context.Context, string) error {
			protocolCalls.Add(1)
			return errors.New("Gateway 模式不应直连 RDP 目标")
		},
		startProcess: func(string) (managedProcess, error) { return process, nil },
	})
	session, err := runner.Start(context.Background(), Options{
		Address:        "rdp.internal:3389",
		GatewayAddress: listener.Addr().String(),
		GatewayUsage:   GatewayUsageAlways,
		Preflight:      successfulProtocolProbe,
	})
	if err != nil {
		t.Fatal(err)
	}
	if protocolCalls.Load() != 0 {
		t.Fatal("Gateway 模式仍对目标执行了 X.224 协商")
	}
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("未连接实际 RD Gateway 端点")
	}
	process.Exit(nil)
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestRunnerRejectsGatewayPasswordBeforeCreatingFile(t *testing.T) {
	var created atomic.Bool
	runner := newRunner(runnerDeps{
		credentials: &fakeCredentialBackend{},
		newID:       fixedID("rdp-gateway-password"),
		createFile: func([]byte) (string, func() error, error) {
			created.Store(true)
			return "", func() error { return nil }, nil
		},
	})
	_, err := runner.Start(context.Background(), Options{
		Address:         "server.example",
		GatewayAddress:  "gateway.example",
		GatewayUsername: "gateway-user",
		GatewayPassword: "Gateway-Secret!",
		GatewayUsage:    GatewayUsageAlways,
		Preflight:       func(context.Context, string) error { return nil },
	})
	if err == nil || !strings.Contains(err.Error(), "暂不支持自动注入 RD Gateway 密码") {
		t.Fatalf("RD Gateway 密码未被明确拒绝: %v", err)
	}
	if created.Load() {
		t.Fatal("拒绝 RD Gateway 密码前已经创建了 RDP 文件")
	}
}

func TestRunnerLifecycleCleansResources(t *testing.T) {
	credentials := &fakeCredentialBackend{}
	process := newFakeProcess(4101)
	tunnel := &fakeCloser{}
	events := make(chan StatusEvent, 4)
	var path string
	var order []string
	runner := newRunner(runnerDeps{
		credentials: credentials,
		newID:       fixedID("rdp-test-1"),
		protocolProbe: func(_ context.Context, address string) error {
			order = append(order, "protocol")
			if address != "[2001:db8::7]:3389" {
				t.Fatalf("协议预检地址 = %q", address)
			}
			return nil
		},
		createFile: func(content []byte) (string, func() error, error) {
			order = append(order, "file")
			return createRDPFile(content)
		},
		startProcess: func(rdpPath string) (managedProcess, error) {
			order = append(order, "start")
			path = rdpPath
			content, err := os.ReadFile(rdpPath)
			if err != nil {
				return nil, err
			}
			text := decodeRDPContent(t, content)
			if strings.Contains(text, "Secret-123") {
				t.Fatal("启动 mstsc 前的 RDP 文件含明文密码")
			}
			return process, nil
		},
	})

	session, err := runner.Start(context.Background(), Options{
		Address:        "2001:db8::7",
		Username:       "administrator",
		Password:       "Secret-123",
		RedirectDrives: true,
		Tunnel:         tunnel,
		Preflight: func(_ context.Context, address string) error {
			order = append(order, "preflight")
			if address != "[2001:db8::7]:3389" {
				t.Fatalf("预检地址 = %q", address)
			}
			return nil
		},
		OnStatus: func(event StatusEvent) { events <- event },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(order, ","); got != "preflight,protocol,file,start" {
		t.Fatalf("启动顺序 = %s", got)
	}
	if session.ID != "rdp-test-1" || session.PID != 4101 {
		t.Fatalf("会话标识错误: ID=%q PID=%d", session.ID, session.PID)
	}
	expectState(t, events, StateLaunching)
	expectState(t, events, StateRunning)
	if tunnel.Count() != 0 {
		t.Fatal("mstsc 运行期间隧道被提前关闭")
	}
	if got := credentials.WriteCount(); got != 1 {
		t.Fatalf("临时凭证写入次数 = %d", got)
	}
	write := credentials.LastWrite()
	if write.target != "TERMSRV/[2001:db8::7]:3389" || write.username != "administrator" {
		t.Fatalf("临时凭证目标错误: %#v", write)
	}

	process.Exit(nil)
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	exitEvent := expectState(t, events, StateExited)
	if exitEvent.Err != nil || exitEvent.Error != "" {
		t.Fatalf("正常退出包含错误: %v", exitEvent.Err)
	}
	if tunnel.Count() != 1 {
		t.Fatalf("隧道关闭次数 = %d", tunnel.Count())
	}
	if credentials.DeleteCount() != 1 {
		t.Fatalf("临时凭证删除次数 = %d", credentials.DeleteCount())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("临时 RDP 文件仍存在: %v", err)
	}
}

func TestRunnerPreflightFailurePreservesError(t *testing.T) {
	preflightErr := errors.New("SOCKS5 代理拒绝连接: 原始错误")
	closeErr := errors.New("隧道关闭原始错误")
	tunnel := &fakeCloser{err: closeErr}
	events := make(chan StatusEvent, 3)
	var started atomic.Bool
	runner := newRunner(runnerDeps{
		credentials: &fakeCredentialBackend{},
		newID:       fixedID("rdp-preflight"),
		startProcess: func(string) (managedProcess, error) {
			started.Store(true)
			return nil, errors.New("不应启动")
		},
	})
	_, err := runner.Start(context.Background(), Options{
		Address:  "server.example",
		Tunnel:   tunnel,
		Password: "",
		Preflight: func(context.Context, string) error {
			return preflightErr
		},
		OnStatus: func(event StatusEvent) { events <- event },
	})
	if !errors.Is(err, preflightErr) || !errors.Is(err, closeErr) {
		t.Fatalf("错误链未保留原始错误: %v", err)
	}
	if started.Load() {
		t.Fatal("预检失败后仍启动了 mstsc")
	}
	if tunnel.Count() != 1 {
		t.Fatalf("预检失败时隧道关闭次数 = %d", tunnel.Count())
	}
	expectState(t, events, StateLaunching)
	failed := expectState(t, events, StateFailed)
	if !errors.Is(failed.Err, preflightErr) || !strings.Contains(failed.Error, preflightErr.Error()) {
		t.Fatalf("失败事件未保留原始错误: %#v", failed)
	}
}

func TestRunnerImmediateLaunchDoesNotWaitForPreflight(t *testing.T) {
	process := newFakeProcess(4102)
	var preflightCalled atomic.Bool
	var protocolProbeCalled atomic.Bool
	runner := newRunner(runnerDeps{
		credentials: &fakeCredentialBackend{},
		newID:       fixedID("rdp-immediate"),
		protocolProbe: func(context.Context, string) error {
			protocolProbeCalled.Store(true)
			return errors.New("不应执行协议预检")
		},
		startProcess: func(string) (managedProcess, error) { return process, nil },
	})
	session, err := runner.Start(context.Background(), Options{
		Address:         "server.example:3389",
		ImmediateLaunch: true,
		Preflight: func(context.Context, string) error {
			preflightCalled.Store(true)
			return errors.New("不应执行连接预检")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if preflightCalled.Load() || protocolProbeCalled.Load() {
		t.Fatal("即时启动 RDP 时仍执行了阻塞预检")
	}
	if session.PID != 4102 {
		t.Fatalf("mstsc 未立即启动: PID=%d", session.PID)
	}
	process.Exit(nil)
	<-session.Done()
}

func TestRunnerStartFailureCleansCredentialFileAndTunnel(t *testing.T) {
	startErr := errors.New("CreateProcess 原始错误")
	credentials := &fakeCredentialBackend{}
	tunnel := &fakeCloser{}
	events := make(chan StatusEvent, 3)
	var path string
	runner := newRunner(runnerDeps{
		credentials:   credentials,
		newID:         fixedID("rdp-start-fail"),
		protocolProbe: successfulProtocolProbe,
		startProcess: func(rdpPath string) (managedProcess, error) {
			path = rdpPath
			return nil, startErr
		},
	})
	_, err := runner.Start(context.Background(), Options{
		Address:   "server.example:3390",
		Username:  "admin",
		Password:  "password",
		Tunnel:    tunnel,
		Preflight: func(context.Context, string) error { return nil },
		OnStatus:  func(event StatusEvent) { events <- event },
	})
	if !errors.Is(err, startErr) {
		t.Fatalf("启动错误未保留: %v", err)
	}
	if credentials.WriteCount() != 1 || credentials.DeleteCount() != 1 {
		t.Fatalf("凭证清理不完整: write=%d delete=%d", credentials.WriteCount(), credentials.DeleteCount())
	}
	if tunnel.Count() != 1 {
		t.Fatalf("隧道关闭次数 = %d", tunnel.Count())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("启动失败后临时文件仍存在: %v", err)
	}
	expectState(t, events, StateLaunching)
	expectState(t, events, StateFailed)
}

func TestRunnerSupportsNoPassword(t *testing.T) {
	credentials := &fakeCredentialBackend{}
	process := newFakeProcess(4201)
	var promptLine bool
	runner := newRunner(runnerDeps{
		credentials:   credentials,
		newID:         fixedID("rdp-no-password"),
		protocolProbe: successfulProtocolProbe,
		startProcess: func(path string) (managedProcess, error) {
			content, err := os.ReadFile(path)
			if err != nil {
				return nil, err
			}
			promptLine = strings.Contains(decodeRDPContent(t, content), "prompt for credentials:i:1\r\n")
			return process, nil
		},
	})
	session, err := runner.Start(context.Background(), Options{
		Address:   "server.example",
		Username:  "admin",
		Preflight: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !promptLine {
		t.Fatal("无密码会话未要求 mstsc 动态询问凭证")
	}
	if credentials.WriteCount() != 0 {
		t.Fatal("无密码会话仍写入了 Credential Manager")
	}
	process.Exit(nil)
	if err := session.Wait(); err != nil {
		t.Fatal(err)
	}
	if credentials.DeleteCount() != 0 {
		t.Fatal("无密码会话错误删除了 Credential Manager 条目")
	}
}

func TestRunnerSharesSameCredentialUntilLastSessionExits(t *testing.T) {
	credentials := &fakeCredentialBackend{}
	processes := []*fakeProcess{newFakeProcess(4301), newFakeProcess(4302)}
	var processIndex int
	var idIndex int
	runner := newRunner(runnerDeps{
		credentials:   credentials,
		protocolProbe: successfulProtocolProbe,
		newID: func() (string, error) {
			idIndex++
			return fmt.Sprintf("rdp-shared-%d", idIndex), nil
		},
		startProcess: func(string) (managedProcess, error) {
			process := processes[processIndex]
			processIndex++
			return process, nil
		},
	})
	options := Options{
		Address:   "Server.Example",
		Username:  "admin",
		Password:  "same-password",
		Preflight: func(context.Context, string) error { return nil },
	}
	first, err := runner.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	options.Address = "server.example:3389"
	second, err := runner.Start(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.PID == second.PID {
		t.Fatal("多个 RDP 窗口未获得独立会话标识")
	}
	if credentials.WriteCount() != 1 {
		t.Fatalf("相同凭证重复写入 %d 次", credentials.WriteCount())
	}
	processes[0].Exit(nil)
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	if credentials.DeleteCount() != 0 {
		t.Fatal("首个窗口退出时提前删除了共享临时凭证")
	}
	processes[1].Exit(nil)
	if err := second.Wait(); err != nil {
		t.Fatal(err)
	}
	if credentials.DeleteCount() != 1 {
		t.Fatalf("最后窗口退出后凭证删除次数 = %d", credentials.DeleteCount())
	}
}

func TestRunnerRejectsDifferentCredentialForActiveTarget(t *testing.T) {
	credentials := &fakeCredentialBackend{}
	process := newFakeProcess(4401)
	firstTunnel := &fakeCloser{}
	secondTunnel := &fakeCloser{}
	runner := newRunner(runnerDeps{
		credentials:   credentials,
		protocolProbe: successfulProtocolProbe,
		newID: func() func() (string, error) {
			var index int
			return func() (string, error) {
				index++
				return fmt.Sprintf("rdp-conflict-%d", index), nil
			}
		}(),
		startProcess: func(string) (managedProcess, error) { return process, nil },
	})
	base := Options{
		Address:   "server.example",
		Username:  "admin",
		Password:  "first-password",
		Tunnel:    firstTunnel,
		Preflight: func(context.Context, string) error { return nil },
	}
	first, err := runner.Start(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	base.Password = "second-password"
	base.Tunnel = secondTunnel
	if _, err := runner.Start(context.Background(), base); err == nil || !strings.Contains(err.Error(), "不同凭证") {
		t.Fatalf("活动目标接受了不同凭证: %v", err)
	}
	if credentials.WriteCount() != 1 || credentials.DeleteCount() != 0 {
		t.Fatalf("凭证池状态错误: write=%d delete=%d", credentials.WriteCount(), credentials.DeleteCount())
	}
	if firstTunnel.Count() != 0 || secondTunnel.Count() != 1 {
		t.Fatalf("隧道隔离错误: first=%d second=%d", firstTunnel.Count(), secondTunnel.Count())
	}
	process.Exit(nil)
	if err := first.Wait(); err != nil {
		t.Fatal(err)
	}
	if firstTunnel.Count() != 1 || credentials.DeleteCount() != 1 {
		t.Fatal("活动会话退出后资源未清理")
	}
}

func TestSessionCloseKillsProcess(t *testing.T) {
	process := newFakeProcess(4501)
	tunnel := &fakeCloser{}
	runner := newRunner(runnerDeps{
		credentials:   &fakeCredentialBackend{},
		newID:         fixedID("rdp-close"),
		protocolProbe: successfulProtocolProbe,
		startProcess:  func(string) (managedProcess, error) { return process, nil },
	})
	session, err := runner.Start(context.Background(), Options{
		Address:   "server.example",
		Tunnel:    tunnel,
		Preflight: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if !process.Killed() {
		t.Fatal("Session.Close 未终止对应 mstsc 进程")
	}
	if err := session.Wait(); !errors.Is(err, errFakeKilled) {
		t.Fatalf("Wait 错误 = %v", err)
	}
	if tunnel.Count() != 1 {
		t.Fatal("终止进程后隧道未清理")
	}
}

func TestSessionConcurrentCloseKillsProcessOnce(t *testing.T) {
	process := newFakeProcess(4502)
	runner := newRunner(runnerDeps{
		credentials:   &fakeCredentialBackend{},
		newID:         fixedID("rdp-concurrent-close"),
		protocolProbe: successfulProtocolProbe,
		startProcess:  func(string) (managedProcess, error) { return process, nil },
	})
	session, err := runner.Start(context.Background(), Options{
		Address:   "server.example",
		Preflight: func(context.Context, string) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 32
	var wait sync.WaitGroup
	wait.Add(callers)
	errorsSeen := make(chan error, callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			errorsSeen <- session.Close()
		}()
	}
	wait.Wait()
	close(errorsSeen)
	for closeErr := range errorsSeen {
		if closeErr != nil {
			t.Fatalf("并发关闭 RDP 会话失败: %v", closeErr)
		}
	}
	if process.KillCount() != 1 {
		t.Fatalf("并发关闭调用 Kill 次数 = %d，期望 1", process.KillCount())
	}
	_ = session.Wait()
}

func decodeRDPContent(t *testing.T, content []byte) string {
	t.Helper()
	if len(content) < 2 || content[0] != 0xff || content[1] != 0xfe || len(content)%2 != 0 {
		t.Fatal("RDP 配置不是带 BOM 的 UTF-16LE")
	}
	values := make([]uint16, (len(content)-2)/2)
	for index := range values {
		values[index] = uint16(content[2+index*2]) | uint16(content[3+index*2])<<8
	}
	return string(utf16.Decode(values))
}

func expectState(t *testing.T, events <-chan StatusEvent, state State) StatusEvent {
	t.Helper()
	select {
	case event := <-events:
		if event.State != state {
			t.Fatalf("状态 = %q，期望 %q", event.State, state)
		}
		return event
	case <-time.After(2 * time.Second):
		t.Fatalf("等待状态 %q 超时", state)
		return StatusEvent{}
	}
}

func fixedID(value string) func() (string, error) {
	return func() (string, error) { return value, nil }
}

func successfulProtocolProbe(context.Context, string) error { return nil }

type credentialWrite struct {
	target   string
	username string
	password string
}

type fakeCredentialBackend struct {
	mu      sync.Mutex
	writes  []credentialWrite
	deletes []string
}

func (b *fakeCredentialBackend) Write(target, username, password string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.writes = append(b.writes, credentialWrite{target: target, username: username, password: password})
	return nil
}

func (b *fakeCredentialBackend) Delete(target string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.deletes = append(b.deletes, target)
	return nil
}

func (b *fakeCredentialBackend) WriteCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.writes)
}

func (b *fakeCredentialBackend) DeleteCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.deletes)
}

func (b *fakeCredentialBackend) LastWrite() credentialWrite {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.writes[len(b.writes)-1]
}

var errFakeKilled = errors.New("测试进程已终止")

type fakeProcess struct {
	pid       int
	wait      chan error
	killOnce  sync.Once
	killCalls atomic.Int32
	killed    atomic.Bool
}

func newFakeProcess(pid int) *fakeProcess {
	return &fakeProcess{pid: pid, wait: make(chan error, 1)}
}

func (p *fakeProcess) PID() int { return p.pid }

func (p *fakeProcess) Wait() error { return <-p.wait }

func (p *fakeProcess) Kill() error {
	p.killCalls.Add(1)
	p.killOnce.Do(func() {
		p.killed.Store(true)
		p.wait <- errFakeKilled
	})
	return nil
}

func (p *fakeProcess) Exit(err error) {
	p.wait <- err
}

func (p *fakeProcess) Killed() bool { return p.killed.Load() }

func (p *fakeProcess) KillCount() int { return int(p.killCalls.Load()) }

type fakeCloser struct {
	count atomic.Int32
	err   error
}

func (c *fakeCloser) Close() error {
	c.count.Add(1)
	return c.err
}

func (c *fakeCloser) Count() int { return int(c.count.Load()) }
