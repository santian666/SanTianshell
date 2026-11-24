package core

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func testProxyLine(ctx context.Context, line string, testURL string) ProxyDetectResult {
	return testProxyLineWithDialer(ctx, line, testURL, &net.Dialer{Timeout: 8 * time.Second, KeepAlive: 30 * time.Second})
}

func TestParseProxyLineVMessBase64Variants(t *testing.T) {
	payload, err := json.Marshal(map[string]any{"add": "127.0.0.1", "port": "23456"})
	if err != nil {
		t.Fatal(err)
	}
	tests := []string{
		"vmess://" + base64.StdEncoding.EncodeToString(payload),
		"vmess://" + base64.RawURLEncoding.EncodeToString(payload),
	}
	for _, line := range tests {
		result := parseProxyLine(line)
		if result.Protocol != "vmess" || result.Host != "127.0.0.1" || result.Port != 23456 {
			t.Fatalf("VMess 解析错误: %+v", result)
		}
	}
}

func TestClampProxyConcurrency(t *testing.T) {
	tests := map[int]int{-10: 1, 0: 1, 1: 1, 25: 25, 50: 50, 51: 50, 100: 50}
	for input, want := range tests {
		if got := clampProxyConcurrency(input); got != want {
			t.Fatalf("并发 %d 应归一为 %d，实际为 %d", input, want, got)
		}
	}
}

func TestRemoteCurlQuoteEscapesConfigSyntax(t *testing.T) {
	quoted := remoteCurlQuote("a\\b\"c\tline\r\nnext")
	if strings.ContainsAny(quoted, "\t\r\n") {
		t.Fatalf("curl config 引号内不得保留真实制表或换行字符: %q", quoted)
	}
	for _, escaped := range []string{"\\\\", "\\\"", "\\t", "\\r", "\\n"} {
		if !strings.Contains(quoted, escaped) {
			t.Fatalf("curl config 缺少转义 %q: %q", escaped, quoted)
		}
	}
}

func TestProxyDetectParseFailureLatencyIsNegative(t *testing.T) {
	result := testProxyLine(context.Background(), "无法解析", "http://127.0.0.1/")
	if result.Latency != -1 || result.TCPLatency != -1 || result.URLLatency != -1 {
		t.Fatalf("解析失败不应显示延迟: %+v", result)
	}
	if result.Message != "无法解析节点地址" {
		t.Fatalf("解析失败提示错误: %q", result.Message)
	}
}

func TestHTTPProxyDetectsAuthenticationAndURLAccess(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	proxyServer := newLocalHTTPProxy(t, "tester", "secret")
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("tester", "secret")
	result := testProxyLine(context.Background(), proxyURL.String(), target.URL)
	if !result.Success || result.TCPLatency < 0 || result.URLLatency < 0 {
		t.Fatalf("HTTP 代理应验证成功: %+v", result)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("测试 URL 应通过代理访问一次，实际 %d 次", targetHits.Load())
	}

	proxyURL.User = url.UserPassword("tester", "wrong")
	result = testProxyLine(context.Background(), proxyURL.String(), target.URL)
	if result.Success || !strings.Contains(result.Message, "HTTP 407") {
		t.Fatalf("错误 HTTP 代理密码应失败: %+v", result)
	}
	if targetHits.Load() != 1 {
		t.Fatal("认证失败后不应访问测试 URL")
	}
}

func TestSOCKS5ProxyDetectsAuthenticationAndURLAccess(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	proxyAddress := newLocalSOCKS5Proxy(t, "tester", "secret")
	result := testProxyLine(context.Background(), proxyAddress+":tester:secret", target.URL)
	if !result.Success || result.Protocol != "socks5" || result.TCPLatency < 0 || result.URLLatency < 0 {
		t.Fatalf("SOCKS5 代理应验证成功: %+v", result)
	}
	if targetHits.Load() != 1 {
		t.Fatalf("测试 URL 应通过 SOCKS5 访问一次，实际 %d 次", targetHits.Load())
	}

	result = testProxyLine(context.Background(), "socks5://tester:wrong@"+proxyAddress, target.URL)
	if result.Success || !strings.Contains(result.Message, "代理验证失败") {
		t.Fatalf("错误 SOCKS5 代理密码应失败: %+v", result)
	}
	if targetHits.Load() != 1 {
		t.Fatal("SOCKS5 认证失败后不应访问测试 URL")
	}
}

func TestEncryptedProxyProtocolsOnlyReportEndpointReachability(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer endpoint.Close()
	u, err := url.Parse(endpoint.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(map[string]any{"add": host, "port": port})
	if err != nil {
		t.Fatal(err)
	}
	lines := []string{
		"vmess://" + base64.RawURLEncoding.EncodeToString(payload),
		fmt.Sprintf("trojan://password@%s", u.Host),
	}
	for _, line := range lines {
		result := testProxyLine(context.Background(), line, endpoint.URL)
		if result.Success || result.TCPLatency < 0 || result.URLLatency != -1 {
			t.Fatalf("加密协议不应冒充完整验证成功: %+v", result)
		}
		if !strings.Contains(result.Message, "端口可达") || !strings.Contains(result.Message, "未验证") {
			t.Fatalf("端点检测提示不清楚: %q", result.Message)
		}
	}
}

func TestRemoteProxyDetectRunsCurlInsideVPSAndReusesSSHConnection(t *testing.T) {
	var commandMu sync.Mutex
	commands := []string{}
	configs := []string{}
	sshClient := newLocalSSHExecClient(t, func(command string, stdin string) sshExecResponse {
		commandMu.Lock()
		commands = append(commands, command)
		commandMu.Unlock()
		switch {
		case strings.Contains(command, "command -v curl"):
			return sshExecResponse{}
		case strings.Contains(command, "hostname"):
			return sshExecResponse{Stdout: "probe-vps\n"}
		case strings.Contains(command, "api.ipify.org"):
			return sshExecResponse{Stdout: "203.0.113.20\n"}
		case strings.Contains(command, "curl -q --config -"):
			commandMu.Lock()
			configs = append(configs, stdin)
			commandMu.Unlock()
			if strings.Contains(stdin, "tester:wrong") {
				return sshExecResponse{
					Stdout:   remoteProxyMetricPrefix + "\t407\t0.014\t0.100\n",
					Stderr:   "curl: (22) The requested URL returned error: 407\n",
					ExitCode: 22,
				}
			}
			return sshExecResponse{Stdout: remoteProxyMetricPrefix + "\t204\t0.012\t0.345\n"}
		default:
			return sshExecResponse{Stderr: "unexpected command", ExitCode: 127}
		}
	})
	var acquireCount atomic.Int32
	var releaseCount atomic.Int32
	service := NewServiceWithConnector(t.TempDir(), func(vps VPS, _ int) (*ssh.Client, func(), error) {
		if vps.Alias != "test-vps" {
			t.Fatalf("检测 VPS 错误: %+v", vps)
		}
		acquireCount.Add(1)
		return sshClient, func() { releaseCount.Add(1) }, nil
	})
	if err := service.SaveVPS(VPS{Alias: "test-vps", Address: "127.0.0.1", Port: 22, Username: "tester"}); err != nil {
		t.Fatal(err)
	}

	results, err := service.testProxyNodes(context.Background(), ProxyDetectRequest{
		Target: "test-vps",
		Nodes: strings.Join([]string{
			"http://tester:secret@192.0.2.10:8080",
			"198.51.100.20:1080:tester:secret",
			"http://tester:wrong@192.0.2.30:8080",
		}, "\n"),
		Concurrency: 3,
	}, "https://example.test/generate_204")
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("检测结果数量错误: %+v", results)
	}
	if !results[0].Success || results[0].Protocol != "http" {
		t.Fatalf("远端 HTTP 代理应检测成功: %+v", results[0])
	}
	if !results[1].Success || results[1].Protocol != "socks5" {
		t.Fatalf("远端 SOCKS5 代理应检测成功: %+v", results[1])
	}
	if results[2].Success || !strings.Contains(results[2].Message, "HTTP 407") {
		t.Fatalf("远端 HTTP 错误认证应失败: %+v", results[2])
	}
	for _, result := range results {
		if result.Line == "" || strings.Contains(result.Line, "tester") || strings.Contains(result.Line, "secret") || strings.Contains(result.Line, "wrong") {
			t.Fatalf("检测结果不得返回凭据: %+v", result)
		}
		if result.DetectTarget != "test-vps" || !strings.Contains(result.DetectLocation, "probe-vps") || result.DetectExitIP != "203.0.113.20" {
			t.Fatalf("应返回可核验的远程检测位置: %+v", result)
		}
		if result.TCPLatency != 12 && result.TCPLatency != 14 {
			t.Fatalf("TCP 延迟应来自远程 curl: %+v", result)
		}
	}
	if acquireCount.Load() != 1 || releaseCount.Load() != 1 {
		t.Fatalf("整批检测应只申请并释放一次 SSH，acquire=%d release=%d", acquireCount.Load(), releaseCount.Load())
	}
	commandMu.Lock()
	defer commandMu.Unlock()
	if len(configs) != 3 {
		t.Fatalf("每个节点应在 VPS 内执行一次 curl，实际 %d 次", len(configs))
	}
	for _, command := range commands {
		if strings.Contains(command, "tester") || strings.Contains(command, "secret") || strings.Contains(command, "wrong") {
			t.Fatalf("SSH 命令行不得包含凭据: %q", command)
		}
	}
	for _, config := range configs {
		if !strings.Contains(config, "noproxy = \"\"") {
			t.Fatalf("远程 curl 必须禁止 NO_PROXY 绕过代理: %q", config)
		}
	}
}

func TestRemoteProxySSHFailureDoesNotFallbackToLocal(t *testing.T) {
	var acquireCount atomic.Int32
	service := NewServiceWithConnector(t.TempDir(), func(vps VPS, _ int) (*ssh.Client, func(), error) {
		acquireCount.Add(1)
		if vps.ProxyHost != "127.0.0.1" || asInt(vps.ProxyPort) != 20808 || vps.ProxyUsername != "proxy-user" || vps.ProxyPassword != "proxy-pass" {
			t.Fatal("远程检测必须把 VPS 的 SOCKS5 配置完整交给统一连接器")
		}
		return nil, nil, fmt.Errorf("测试 SSH 连接失败")
	})
	if err := service.SaveVPS(VPS{
		Alias: "test-vps", Address: "127.0.0.1", Port: 22, Username: "tester",
		ProxyHost: "127.0.0.1", ProxyPort: 20808, ProxyUsername: "proxy-user", ProxyPassword: "proxy-pass",
	}); err != nil {
		t.Fatal(err)
	}
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()
	httpProxy := newLocalHTTPProxy(t, "tester", "secret")
	proxyURL, err := url.Parse(httpProxy.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxyURL.User = url.UserPassword("tester", "secret")

	results, err := service.testProxyNodes(context.Background(), ProxyDetectRequest{
		Target: "test-vps", Nodes: proxyURL.String(), Concurrency: 1,
	}, target.URL)
	if err == nil || !strings.Contains(err.Error(), "测试 SSH 连接失败") {
		t.Fatalf("SSH 失败应直接返回详细错误，results=%+v err=%v", results, err)
	}
	if acquireCount.Load() != 1 || targetHits.Load() != 0 {
		t.Fatalf("SSH 失败不得回退本地检测，acquire=%d targetHits=%d", acquireCount.Load(), targetHits.Load())
	}
}

func TestQueryOutboundIPSSHFailureDoesNotFallbackDirect(t *testing.T) {
	var acquireCount atomic.Int32
	service := NewServiceWithConnector(t.TempDir(), func(vps VPS, _ int) (*ssh.Client, func(), error) {
		acquireCount.Add(1)
		if vps.ProxyHost != "127.0.0.1" || asInt(vps.ProxyPort) != 20808 {
			t.Fatal("查询节点出口必须保留 VPS SOCKS5 配置")
		}
		return nil, nil, errors.New("统一连接器拒绝连接")
	})
	if err := service.SaveVPS(VPS{
		Alias: "test-vps", Address: "192.0.2.10", Port: 22, Username: "tester",
		ProxyHost: "127.0.0.1", ProxyPort: 20808,
	}); err != nil {
		t.Fatal(err)
	}

	result := service.queryOutboundRealIP("test-vps", "node.json", map[string]any{
		"outbounds": []any{map[string]any{
			"type": "socks", "tag": "node-out", "server": "198.51.100.20", "server_port": 1080,
		}},
	})
	if result.Success || !strings.Contains(result.Message, "统一连接器拒绝连接") {
		t.Fatalf("统一连接失败应原样返回且不得直连兜底: %+v", result)
	}
	if acquireCount.Load() != 1 {
		t.Fatalf("查询节点出口只能通过统一连接器申请一次 SSH，实际 %d 次", acquireCount.Load())
	}
}

func TestRemoteProxyMissingCurlDoesNotFallbackToLocal(t *testing.T) {
	var commandCount atomic.Int32
	sshClient := newLocalSSHExecClient(t, func(string, string) sshExecResponse {
		commandCount.Add(1)
		return sshExecResponse{Stderr: "curl: not found", ExitCode: 127}
	})
	var releaseCount atomic.Int32
	service := NewServiceWithConnector(t.TempDir(), func(VPS, int) (*ssh.Client, func(), error) {
		return sshClient, func() { releaseCount.Add(1) }, nil
	})
	if err := service.SaveVPS(VPS{Alias: "test-vps", Address: "127.0.0.1", Port: 22, Username: "tester"}); err != nil {
		t.Fatal(err)
	}
	results, err := service.testProxyNodes(context.Background(), ProxyDetectRequest{
		Target: "test-vps", Nodes: "127.0.0.1:1080:tester:secret", Concurrency: 1,
	}, "https://example.test/generate_204")
	if err == nil || !strings.Contains(err.Error(), "未安装 curl") {
		t.Fatalf("VPS 缺少 curl 应返回明确错误，results=%+v err=%v", results, err)
	}
	if commandCount.Load() != 1 || releaseCount.Load() != 1 {
		t.Fatalf("缺少 curl 后应立即停止且释放 SSH，commands=%d release=%d", commandCount.Load(), releaseCount.Load())
	}
}

type sshExecResponse struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

func newLocalSSHExecClient(t *testing.T, handler func(command string, stdin string) sshExecResponse) *ssh.Client {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverReady := make(chan error, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverReady <- err
			return
		}
		serverConn, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			serverReady <- err
			return
		}
		serverReady <- nil
		defer serverConn.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			go serveLocalSSHExec(channel, handler)
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User:            "tester",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	if err := <-serverReady; err != nil {
		_ = client.Close()
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Error("本地 SSH 服务未按时退出")
		}
	})
	return client
}

func serveLocalSSHExec(newChannel ssh.NewChannel, handler func(command string, stdin string) sshExecResponse) {
	if newChannel.ChannelType() != "session" {
		_ = newChannel.Reject(ssh.UnknownChannelType, "仅支持 session")
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		return
	}
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			return
		}
		_ = request.Reply(true, nil)
		stdin, _ := io.ReadAll(channel)
		response := handler(payload.Command, string(stdin))
		_, _ = io.WriteString(channel, response.Stdout)
		_, _ = io.WriteString(channel.Stderr(), response.Stderr)
		status := struct{ Status uint32 }{Status: uint32(response.ExitCode)}
		_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(&status))
		return
	}
}

func newLocalSSHForwardClient(t *testing.T) (*ssh.Client, *atomic.Int32) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostKey, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(hostKey)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var directTCPIPCount atomic.Int32
	serverReady := make(chan error, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		conn, err := listener.Accept()
		if err != nil {
			serverReady <- err
			return
		}
		serverConn, channels, requests, err := ssh.NewServerConn(conn, serverConfig)
		if err != nil {
			serverReady <- err
			return
		}
		serverReady <- nil
		defer serverConn.Close()
		go ssh.DiscardRequests(requests)
		for channel := range channels {
			go serveLocalSSHForward(channel, &directTCPIPCount)
		}
	}()
	client, err := ssh.Dial("tcp", listener.Addr().String(), &ssh.ClientConfig{
		User:            "tester",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         3 * time.Second,
	})
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	if err := <-serverReady; err != nil {
		_ = client.Close()
		_ = listener.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(2 * time.Second):
			t.Error("本地 SSH 服务未按时退出")
		}
	})
	return client, &directTCPIPCount
}

func serveLocalSSHForward(newChannel ssh.NewChannel, directTCPIPCount *atomic.Int32) {
	if newChannel.ChannelType() != "direct-tcpip" {
		_ = newChannel.Reject(ssh.UnknownChannelType, "仅支持 direct-tcpip")
		return
	}
	var request struct {
		Host       string
		Port       uint32
		OriginHost string
		OriginPort uint32
	}
	if err := ssh.Unmarshal(newChannel.ExtraData(), &request); err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "目标地址无效")
		return
	}
	ip := net.ParseIP(request.Host)
	if ip == nil || !ip.IsLoopback() {
		_ = newChannel.Reject(ssh.Prohibited, "测试仅允许回环地址")
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(request.Host, fmt.Sprint(request.Port)), 3*time.Second)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	channel, requests, err := newChannel.Accept()
	if err != nil {
		_ = upstream.Close()
		return
	}
	directTCPIPCount.Add(1)
	go ssh.DiscardRequests(requests)
	copyDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, channel)
		if tcpConn, ok := upstream.(*net.TCPConn); ok {
			_ = tcpConn.CloseWrite()
		}
		close(copyDone)
	}()
	_, _ = io.Copy(channel, upstream)
	_ = channel.Close()
	_ = upstream.Close()
	<-copyDone
}

func newLocalHTTPProxy(t *testing.T, username, password string) *httptest.Server {
	t.Helper()
	expectedAuth := "Basic " + base64.StdEncoding.EncodeToString([]byte(username+":"+password))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Proxy-Authorization") != expectedAuth {
			w.Header().Set("Proxy-Authenticate", `Basic realm="proxy"`)
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		outgoing := request.Clone(request.Context())
		outgoing.RequestURI = ""
		outgoing.Header = request.Header.Clone()
		outgoing.Header.Del("Proxy-Authorization")
		response, err := http.DefaultTransport.RoundTrip(outgoing)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, response.Body)
	}))
	t.Cleanup(server.Close)
	return server
}

func newLocalSOCKS5Proxy(t *testing.T, username, password string) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go serveLocalSOCKS5(conn, username, password)
		}
	}()
	return listener.Addr().String()
}

func serveLocalSOCKS5(conn net.Conn, username, password string) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 5 {
		return
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, methods); err != nil {
		return
	}
	wantedMethod := byte(0)
	if username != "" || password != "" {
		wantedMethod = 2
	}
	methodFound := false
	for _, method := range methods {
		if method == wantedMethod {
			methodFound = true
			break
		}
	}
	if !methodFound {
		_, _ = conn.Write([]byte{5, 0xff})
		return
	}
	if _, err := conn.Write([]byte{5, wantedMethod}); err != nil {
		return
	}
	if wantedMethod == 2 && !authenticateLocalSOCKS5(conn, username, password) {
		return
	}
	request := make([]byte, 4)
	if _, err := io.ReadFull(conn, request); err != nil || request[0] != 5 || request[1] != 1 {
		return
	}
	host, err := readLocalSOCKS5Host(conn, request[3])
	if err != nil {
		return
	}
	portBytes := make([]byte, 2)
	if _, err = io.ReadFull(conn, portBytes); err != nil {
		return
	}
	upstream, err := net.DialTimeout("tcp", net.JoinHostPort(host, fmt.Sprint(binary.BigEndian.Uint16(portBytes))), 3*time.Second)
	if err != nil {
		_, _ = conn.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
		return
	}
	defer upstream.Close()
	if _, err = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	go func() { _, _ = io.Copy(upstream, conn) }()
	_, _ = io.Copy(conn, upstream)
}

func authenticateLocalSOCKS5(conn net.Conn, username, password string) bool {
	header := make([]byte, 2)
	if _, err := io.ReadFull(conn, header); err != nil || header[0] != 1 {
		return false
	}
	userBytes := make([]byte, int(header[1]))
	if _, err := io.ReadFull(conn, userBytes); err != nil {
		return false
	}
	length := make([]byte, 1)
	if _, err := io.ReadFull(conn, length); err != nil {
		return false
	}
	passwordBytes := make([]byte, int(length[0]))
	if _, err := io.ReadFull(conn, passwordBytes); err != nil {
		return false
	}
	status := byte(0)
	if string(userBytes) != username || string(passwordBytes) != password {
		status = 1
	}
	_, _ = conn.Write([]byte{1, status})
	return status == 0
}

func readLocalSOCKS5Host(conn net.Conn, addressType byte) (string, error) {
	switch addressType {
	case 1:
		value := make([]byte, net.IPv4len)
		_, err := io.ReadFull(conn, value)
		return net.IP(value).String(), err
	case 3:
		length := make([]byte, 1)
		if _, err := io.ReadFull(conn, length); err != nil {
			return "", err
		}
		value := make([]byte, int(length[0]))
		_, err := io.ReadFull(conn, value)
		return string(value), err
	case 4:
		value := make([]byte, net.IPv6len)
		_, err := io.ReadFull(conn, value)
		return net.IP(value).String(), err
	default:
		return "", fmt.Errorf("未知 SOCKS5 地址类型: %d", addressType)
	}
}

func TestProxyDetectProgressCallbackAndCustomURL(t *testing.T) {
	// 启动一个模拟返回 IP 文本的 HTTP 服务作为 testURL
	ipServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("198.51.100.99\n"))
	}))
	defer ipServer.Close()

	// 启动一个支持 CONNECT 的 HTTP 代理
	proxyServer := newLocalHTTPProxy(t, "user", "pass")
	defer proxyServer.Close()
	u, _ := url.Parse(proxyServer.URL)
	u.User = url.UserPassword("user", "pass")

	service := &Service{}
	var progressCount int
	var receivedIndices []int
	results, err := service.TestProxyNodesWithProgress(context.Background(), ProxyDetectRequest{
		Target:      "local",
		Nodes:       u.String(),
		Concurrency: 1,
		TestURL:     ipServer.URL,
	}, func(index int, total int, item ProxyDetectResult) {
		progressCount++
		receivedIndices = append(receivedIndices, index)
	})
	if err != nil {
		t.Fatalf("检测执行失败：%v", err)
	}
	if len(results) != 1 || !results[0].Success {
		t.Fatalf("检测结果非预期：%+v", results)
	}
	if progressCount != 1 || len(receivedIndices) != 1 || receivedIndices[0] != 0 {
		t.Fatalf("进度回调次数或索引非预期：count=%d, indices=%v", progressCount, receivedIndices)
	}
	if results[0].DetectExitIP != "198.51.100.99" {
		t.Fatalf("未提取到真实出口 IP: got=%q, want=198.51.100.99", results[0].DetectExitIP)
	}
}

