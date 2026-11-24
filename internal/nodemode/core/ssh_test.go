package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestResolveLocalIperf3PathPrefersBundledTool(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "SanTianShell.exe")
	bundled := filepath.Join(root, "tools", "iperf3", "iperf3.exe")
	if err := os.MkdirAll(filepath.Dir(bundled), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bundled, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	path, err := resolveLocalIperf3Path(executable, func(string) (string, error) {
		return "", errors.New("不应查询 PATH")
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != bundled {
		t.Fatalf("IPerf3 路径 = %q，期望包内工具 %q", path, bundled)
	}
}

func TestResolveLocalIperf3PathFallsBackToPath(t *testing.T) {
	want := filepath.Join(t.TempDir(), "iperf3.exe")
	path, err := resolveLocalIperf3Path("", func(name string) (string, error) {
		if name == "iperf3" {
			return want, nil
		}
		return "", os.ErrNotExist
	})
	if err != nil || path != want {
		t.Fatalf("PATH 回退结果 = %q, %v，期望 %q", path, err, want)
	}
}

func TestStartLocalTCPForwardUsesProvidedDialer(t *testing.T) {
	dialed := make(chan string, 1)
	dial := func(network string, address string) (net.Conn, error) {
		dialed <- network + " " + address
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			_, _ = io.Copy(remote, remote)
		}()
		return local, nil
	}

	host, port, stop, err := startLocalTCPForward(dial, "127.0.0.1:5201")
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		t.Fatal(err)
	}
	if string(response) != "ping" {
		t.Fatalf("转发响应 = %q，期望 ping", response)
	}
	select {
	case got := <-dialed:
		if got != "tcp 127.0.0.1:5201" {
			t.Fatalf("转发目标 = %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("没有调用指定的 SSH 拨号器")
	}
}

func TestRefreshVPSStateUsesInteractiveConnectorOnlyWhenRequested(t *testing.T) {
	service := NewServiceWithConnectors(
		t.TempDir(),
		func(VPS, int) (*ssh.Client, func(), error) { return nil, nil, errors.New("静默连接") },
		func(VPS, int) (*ssh.Client, func(), error) { return nil, nil, errors.New("交互连接") },
	)
	if err := service.SaveVPS(VPS{Alias: "UK-DO", Address: "192.0.2.19", Port: 22, Username: "root"}); err != nil {
		t.Fatal(err)
	}
	if result := service.RefreshVPSState("UK-DO", false); result.Message != "静默连接" {
		t.Fatalf("静默刷新使用了错误连接器: %#v", result)
	}
	if result := service.RefreshVPSStateInteractive("UK-DO", false); result.Message != "交互连接" {
		t.Fatalf("手动刷新未使用交互连接器: %#v", result)
	}
}

func TestDeploySingboxCommandChecksInstallResultAfterScriptExit(t *testing.T) {
	command := deploySingboxCommand()
	if !strings.Contains(command, `bash "$tmp_file" || install_status=$?`) {
		t.Fatalf("deploy command should keep checking installed files after installer returns non-zero")
	}
	if !strings.Contains(command, `if [ -n "$sb_path" ]; then`) {
		t.Fatalf("deploy command should check sb after running installer")
	}
	if !strings.Contains(command, `command -v sing-box`) {
		t.Fatalf("deploy command should accept sing-box binary when sb is missing")
	}
}

func TestDeploySingboxCommandProtectsLogs(t *testing.T) {
	command := deploySingboxCommand()
	wants := []string{
		`"level": "warn"`,
		`StandardOutput=append:/var/log/sing-box/service.log`,
		`StandardError=append:/var/log/sing-box/service.log`,
		`/etc/logrotate.d/vps-management-singbox`,
		`OnUnitActiveSec=6h`,
		`systemctl start vps-management-singbox-log-clean.service`,
		`sleep 10; systemctl try-restart sing-box`,
		`maxsize 10M`,
		`rotate 7`,
		`rm -f /var/log/sing-box.log /var/log/sing-box.log.*`,
		`rm -rf /tmp/sb_node_ip`,
	}
	for _, want := range wants {
		if !strings.Contains(command, want) {
			t.Fatalf("deploy command is missing log protection %q", want)
		}
	}
}

func TestRetryableSSHConnectErrorIncludesForcedClose(t *testing.T) {
	err := errors.New("ssh: handshake failed: read tcp 192.168.0.5:29237->85.149.212.114:22: wsarecv: An existing connection was forcibly closed by the remote host.")
	if !isRetryableSSHConnectError(err) {
		t.Fatalf("expected forced close handshake error to be retryable")
	}
}

func TestRetryableSSHConnectErrorSkipsAuthFailures(t *testing.T) {
	err := errors.New("ssh: unable to authenticate, attempted methods [none password], no supported methods remain")
	if isRetryableSSHConnectError(err) {
		t.Fatalf("auth failures should not be retried")
	}
}

func TestSystemProxyFallbackErrorIncludesEOFHandshake(t *testing.T) {
	err := errors.New("ssh: handshake failed: EOF")
	if !isSystemProxyFallbackError(err) {
		t.Fatalf("expected EOF handshake error to allow direct fallback")
	}
}

func TestExplainSocksSSHHandshakeEOF(t *testing.T) {
	raw := fmt.Errorf("ssh: handshake failed: %w", io.EOF)
	err := explainSocksSSHHandshakeError("192.0.2.20:22", raw)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("应保留原始 EOF 错误链：%v", err)
	}
	for _, expected := range []string{"SOCKS5 隧道已建立", "192.0.2.20:22", "发送 SSH 服务标识前关闭连接", "ssh: handshake failed: EOF"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("错误提示缺少 %q：%v", expected, err)
		}
	}
}

func TestSystemProxyFallbackErrorSkipsAuthFailures(t *testing.T) {
	err := errors.New("ssh: unable to authenticate, attempted methods [none password], no supported methods remain")
	if isSystemProxyFallbackError(err) {
		t.Fatalf("auth failures should not allow direct fallback")
	}
}

func TestExplicitVPSProxyFailureDoesNotFallbackDirect(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	targetHit := make(chan struct{}, 1)
	go func() {
		conn, acceptErr := target.Accept()
		if acceptErr != nil {
			return
		}
		targetHit <- struct{}{}
		_ = conn.Close()
	}()

	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	proxyDone := make(chan error, 1)
	go func() {
		conn, acceptErr := proxyListener.Accept()
		if acceptErr != nil {
			proxyDone <- acceptErr
			return
		}
		defer conn.Close()
		header := make([]byte, 2)
		if _, readErr := io.ReadFull(conn, header); readErr != nil {
			proxyDone <- readErr
			return
		}
		methods := make([]byte, int(header[1]))
		if _, readErr := io.ReadFull(conn, methods); readErr != nil {
			proxyDone <- readErr
			return
		}
		if _, writeErr := conn.Write([]byte{5, 0}); writeErr != nil {
			proxyDone <- writeErr
			return
		}
		request := make([]byte, 4)
		if _, readErr := io.ReadFull(conn, request); readErr != nil {
			proxyDone <- readErr
			return
		}
		var requestErr error
		switch request[3] {
		case 1:
			_, requestErr = io.ReadFull(conn, make([]byte, 6))
		case 3:
			length := make([]byte, 1)
			if _, requestErr = io.ReadFull(conn, length); requestErr == nil {
				_, requestErr = io.ReadFull(conn, make([]byte, int(length[0])+2))
			}
		case 4:
			_, requestErr = io.ReadFull(conn, make([]byte, 18))
		default:
			requestErr = fmt.Errorf("未知 SOCKS5 地址类型: %d", request[3])
		}
		if requestErr != nil {
			proxyDone <- requestErr
			return
		}
		_, requestErr = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		proxyDone <- requestErr
	}()

	targetAddress := target.Addr().(*net.TCPAddr)
	proxyAddress := proxyListener.Addr().(*net.TCPAddr)
	service := NewServiceWithDataDir(t.TempDir())
	client, err := service.connectSSHOnce(VPS{
		Address:       targetAddress.IP.String(),
		Port:          targetAddress.Port,
		Username:      "root",
		Password:      "test",
		ProxyHost:     proxyAddress.IP.String(),
		ProxyPort:     proxyAddress.Port,
		ProxyUsername: "",
		ProxyPassword: "",
	}, 1)
	if client != nil {
		_ = client.Close()
	}
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "handshake") {
		t.Fatalf("显式代理握手失败应原样返回，不得直连：%v", err)
	}
	if proxyErr := <-proxyDone; proxyErr != nil {
		t.Fatal(proxyErr)
	}
	select {
	case <-targetHit:
		t.Fatal("显式 VPS SOCKS5 失败后发生了静默直连")
	default:
	}
}

func TestParseSystemProxyServerPlainHostPort(t *testing.T) {
	config, err := parseSystemProxyServer("127.0.0.1:20808")
	if err != nil {
		t.Fatalf("parse system proxy: %v", err)
	}
	if config.Host != "127.0.0.1" || config.Port != 20808 {
		t.Fatalf("unexpected proxy config: %+v", config)
	}
}

func TestParseSystemProxyServerSchemeListPrefersFirstUsable(t *testing.T) {
	config, err := parseSystemProxyServer("socks=127.0.0.1:20808;http=127.0.0.1:20809")
	if err != nil {
		t.Fatalf("parse system proxy: %v", err)
	}
	if config.Host != "127.0.0.1" || config.Port != 20808 {
		t.Fatalf("unexpected proxy config: %+v", config)
	}
}

func TestReadRemoteNodeConfigsFallsBackAfterDamagedAllJSON(t *testing.T) {
	reads := map[string]int{}
	valid := testRemoteAllJSON(t, 1)
	configs, path, err := readRemoteNodeConfigs(func(path string) ([]byte, error) {
		reads[path]++
		if path == remoteSingBoxConfigPaths[0] {
			return []byte(`{"inbounds":`), nil
		}
		return valid, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if path != remoteSingBoxConfigPaths[1] || len(configs) != 1 {
		t.Fatalf("expected fallback config with one node, path=%q nodes=%d", path, len(configs))
	}
	for _, candidate := range remoteSingBoxConfigPaths {
		if reads[candidate] != 1 {
			t.Fatalf("candidate %s should be read once, got %d", candidate, reads[candidate])
		}
	}
}

func TestReadManagedRemoteNodeConfigsDoesNotUseGenericConfig(t *testing.T) {
	reads := map[string]int{}
	_, _, err := readManagedRemoteNodeConfigs(func(path string) ([]byte, error) {
		reads[path]++
		if path == remoteSingBoxConfigPaths[0] {
			return nil, errors.New("文件不存在")
		}
		return testRemoteAllJSON(t, 1), nil
	})
	if err == nil {
		t.Fatal("SanTianShell 管理配置不存在时不应读取通用 config.json")
	}
	if reads[remoteSingBoxConfigPaths[0]] != 1 || reads[remoteSingBoxConfigPaths[1]] != 0 {
		t.Fatalf("同步读取了非 SanTianShell 配置路径：%+v", reads)
	}
}

func TestReadRemoteNodeConfigsRejectsEmptyConfigs(t *testing.T) {
	reads := map[string]int{}
	empty := []byte(`{"inbounds":[],"outbounds":[],"route":{"rules":[]}}`)
	configs, path, err := readRemoteNodeConfigs(func(path string) ([]byte, error) {
		reads[path]++
		return empty, nil
	})
	if err == nil {
		t.Fatal("empty remote configs should be rejected")
	}
	if configs != nil || path != "" {
		t.Fatalf("empty remote configs must not be returned: path=%q configs=%v", path, configs)
	}
	for _, candidate := range remoteSingBoxConfigPaths {
		if reads[candidate] != 1 {
			t.Fatalf("candidate %s should be read once, got %d", candidate, reads[candidate])
		}
	}
}

func TestRemoteSevenNodesNextStartPort(t *testing.T) {
	reads := map[string]int{}
	configs, path, err := readRemoteNodeConfigs(func(path string) ([]byte, error) {
		reads[path]++
		if path != remoteSingBoxConfigPaths[0] {
			return nil, errors.New("不应继续读取备用配置")
		}
		return testRemoteAllJSON(t, 7), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	count, startPort := nodeConfigRuntimeSummary(configs)
	if path != remoteSingBoxConfigPaths[0] || count != 7 || startPort != 20007 {
		t.Fatalf("unexpected runtime summary: path=%q count=%d startPort=%d", path, count, startPort)
	}
	if reads[remoteSingBoxConfigPaths[0]] != 1 || reads[remoteSingBoxConfigPaths[1]] != 0 {
		t.Fatalf("valid primary config should be read once without fallback: %+v", reads)
	}
}

func testRemoteAllJSON(t *testing.T, count int) []byte {
	t.Helper()
	inbounds := make([]any, 0, count)
	outbounds := make([]any, 0, count)
	rules := make([]any, 0, count)
	for index := 0; index < count; index++ {
		tag := fmt.Sprintf("UK-DO-%d", index+1)
		outboundTag := tag + "-out"
		inbounds = append(inbounds, map[string]any{"tag": tag, "type": "vless", "listen_port": 20000 + index})
		outbounds = append(outbounds, map[string]any{"tag": outboundTag, "type": "direct"})
		rules = append(rules, map[string]any{"inbound": []any{tag}, "outbound": outboundTag})
	}
	data, err := json.Marshal(map[string]any{
		"inbounds":  inbounds,
		"outbounds": outbounds,
		"route":     map[string]any{"rules": rules},
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestIsNonRootUserDetection(t *testing.T) {
	rootCases := []string{"root", "ROOT", "Root", "  root  ", "", "   "}
	for _, tc := range rootCases {
		if isNonRootUser(tc) {
			t.Errorf("isNonRootUser(%q) should be false", tc)
		}
	}

	nonRootCases := []string{"debian", "ubuntu", "admin", "ec2-user", "centos", "opc"}
	for _, tc := range nonRootCases {
		if !isNonRootUser(tc) {
			t.Errorf("isNonRootUser(%q) should be true", tc)
		}
	}
}

func TestBuildRootCommandForRootReturnsUnmodified(t *testing.T) {
	cmd := "systemctl restart sing-box"
	got := buildRootCommand(cmd, "some-pass", false)
	if got != cmd {
		t.Fatalf("buildRootCommand for root should return unmodified, got %q", got)
	}
}

func TestBuildRootCommandSingleLineElevation(t *testing.T) {
	cmd := "systemctl restart sing-box"
	got := buildRootCommand(cmd, "mypass123", true)
	if !strings.Contains(got, `if [ "$(id -u)" -eq 0 ]; then`) {
		t.Fatalf("expected root check in elevated command, got: %s", got)
	}
	if !strings.Contains(got, `sudo -n bash -c 'systemctl restart sing-box'`) {
		t.Fatalf("expected passwordless sudo attempt, got: %s", got)
	}
	if !strings.Contains(got, `printf '%s\n' 'mypass123' | sudo -S -p '' -- bash -c 'systemctl restart sing-box'`) {
		t.Fatalf("expected password piped to sudo, got: %s", got)
	}
	if !strings.Contains(got, `su - -c 'systemctl restart sing-box'`) {
		t.Fatalf("expected su fallback, got: %s", got)
	}
}

func TestBuildRootCommandMultiLineElevation(t *testing.T) {
	multiLineCmd := "echo line1\necho line2"
	got := buildRootCommand(multiLineCmd, "sec'ret", true)
	if !strings.Contains(got, `cat << 'SANTIANSHELL_SCRIPT_PAYLOAD' > "$__sb_script"`) {
		t.Fatalf("expected payload heredoc in multi-line script, got: %s", got)
	}
	if !strings.Contains(got, "echo line1\necho line2") {
		t.Fatalf("expected payload content, got: %s", got)
	}
	if !strings.Contains(got, `sudo -n bash "$__sb_script"`) {
		t.Fatalf("expected sudo execution of script, got: %s", got)
	}
	if !strings.Contains(got, `printf '%s\n' 'sec'\''ret' | sudo -S -p '' -- bash "$__sb_script"`) {
		t.Fatalf("expected safe password quoting in sudo, got: %s", got)
	}
}

func TestUploadAllJSONByStreamCommand(t *testing.T) {
	rootCmd := uploadAllJSONByStreamCommand("", false)
	if !strings.Contains(rootCmd, "all.json.rollback") {
		t.Fatalf("root upload command should backup existing config to all.json.rollback, got: %s", rootCmd)
	}
	if !strings.Contains(rootCmd, "cat > /etc/sing-box/conf/all.json.tmp") {
		t.Fatalf("root upload command should stream to all.json.tmp, got: %s", rootCmd)
	}

	nonRootCmd := uploadAllJSONByStreamCommand("debpass", true)
	if !strings.Contains(nonRootCmd, "all.json.rollback") {
		t.Fatalf("non-root upload command should backup existing config to all.json.rollback, got: %s", nonRootCmd)
	}
	if !strings.Contains(nonRootCmd, "tmp_file=$(mktemp /tmp/santianshell-all.XXXXXX)") {
		t.Fatalf("non-root upload command should stream to /tmp, got: %s", nonRootCmd)
	}
	if !strings.Contains(nonRootCmd, "sudo -n cp -f \"$tmp_file\" /etc/sing-box/conf/all.json") {
		t.Fatalf("non-root upload command should copy to all.json with sudo, got: %s", nonRootCmd)
	}
	if !strings.Contains(nonRootCmd, "chmod 644 /etc/sing-box/conf/all.json") {
		t.Fatalf("non-root upload command should set 644 permissions, got: %s", nonRootCmd)
	}
}

func TestBuildSingboxReloadCommand(t *testing.T) {
	reloadCmd := buildSingboxReloadCommand()
	wants := []string{
		"\"$sb_bin\" check -c /etc/sing-box/config.json -C /etc/sing-box/conf",
		"all.json.rollback",
		"自动回滚原有配置",
		"cp -f /etc/sing-box/conf/all.json.rollback /etc/sing-box/conf/all.json",
		"iptables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT",
		"ip6tables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT",
		"ufw allow 10000:65000/tcp",
		"firewall-cmd --permanent --add-port=10000-65000/tcp",
	}
	for _, want := range wants {
		if !strings.Contains(reloadCmd, want) {
			t.Fatalf("reload command is missing %q, got: %s", want, reloadCmd)
		}
	}
}

func TestDeploySingboxCommandIncludesFirewall(t *testing.T) {
	cmd := deploySingboxCommand()
	wants := []string{
		"iptables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT",
		"ip6tables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT",
		"ufw allow 10000:65000/tcp",
		"firewall-cmd --permanent --add-port=10000-65000/tcp",
	}
	for _, want := range wants {
		if !strings.Contains(cmd, want) {
			t.Fatalf("deploy command is missing firewall rule %q", want)
		}
	}
}

func TestGetSingboxLogScript(t *testing.T) {
	script := getSingboxLogScript(150)
	if !strings.Contains(script, "n=150") {
		t.Fatalf("log script should use maxLines 150, got: %s", script)
	}
	if !strings.Contains(script, "/var/log/sing-box/service.log") {
		t.Fatalf("log script should inspect service.log, got: %s", script)
	}
	if !strings.Contains(script, "/var/log/sing-box/sing-box.log") {
		t.Fatalf("log script should inspect sing-box.log, got: %s", script)
	}
	if !strings.Contains(script, "journalctl -u sing-box") {
		t.Fatalf("log script should fallback to journalctl, got: %s", script)
	}
}

func TestWrapRootCommandWithServiceAndVPS(t *testing.T) {
	service := NewService()
	service.SetPasswordResolver(func(hostID, alias string) string {
		if alias == "sg-node" {
			return "resolved-secret"
		}
		return ""
	})

	rootVPS := VPS{Alias: "us-node", Username: "root"}
	cmd := "rm -f /etc/sing-box/conf/node1.json"
	rootWrapped := service.wrapRootCommand(rootVPS, cmd)
	if rootWrapped != cmd {
		t.Fatalf("root VPS should not be wrapped, got: %s", rootWrapped)
	}

	debianVPS := VPS{Alias: "sg-node", Username: "debian"}
	debianWrapped := service.wrapRootCommand(debianVPS, cmd)
	if !strings.Contains(debianWrapped, "resolved-secret") {
		t.Fatalf("debian VPS should use resolved password, got: %s", debianWrapped)
	}
	if !strings.Contains(debianWrapped, "sudo -n bash -c 'rm -f /etc/sing-box/conf/node1.json'") {
		t.Fatalf("debian VPS should be wrapped with root elevation, got: %s", debianWrapped)
	}
}

func TestParseSystemProxyServerAndFallbackErrors(t *testing.T) {
	cfg, err := parseSystemProxyServer("http=127.0.0.1:7890;socks=127.0.0.1:7891")
	if err != nil {
		t.Fatalf("解析混合系统代理失败: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 7891 {
		t.Fatalf("混合系统代理未优先选择 SOCKS: %#v", cfg)
	}

	cfg, err = parseSystemProxyServer("socks5://alice:secret@127.0.0.1:1080")
	if err != nil {
		t.Fatalf("解析 URL 系统代理失败: %v", err)
	}
	if cfg.Host != "127.0.0.1" || cfg.Port != 1080 || cfg.Username != "alice" || cfg.Password != "secret" {
		t.Fatalf("URL 系统代理解析错误: %#v", cfg)
	}

	cfg, err = parseSystemProxyServer("[::1]:7890")
	if err != nil {
		t.Fatalf("解析 IPv6 系统代理失败: %v", err)
	}
	if cfg.Host != "::1" || cfg.Port != 7890 {
		t.Fatalf("IPv6 系统代理解析错误: %#v", cfg)
	}

	if !isSystemProxyFallbackError(errors.New("socks connect tcp 127.0.0.1:7890->1.2.3.4:22: unexpected protocol")) {
		t.Fatal("isSystemProxyFallbackError 未识别 socks connect / unexpected protocol 错误")
	}
}

