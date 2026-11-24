package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

type sshTestServer struct {
	address     string
	port        int
	fingerprint string
}

func startSSHTestServer(t *testing.T, config *ssh.ServerConfig, serve func(<-chan ssh.NewChannel)) *sshTestServer {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	config.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		serverConnection, channels, requests, serveErr := ssh.NewServerConn(connection, config)
		if serveErr != nil {
			return
		}
		defer serverConnection.Close()
		go func() {
			for request := range requests {
				_ = request.Reply(false, nil)
			}
		}()
		if serve == nil {
			for channel := range channels {
				_ = channel.Reject(ssh.UnknownChannelType, "测试服务器不支持该通道")
			}
			return
		}
		serve(channels)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("SSH 测试服务器未及时退出")
		}
	})
	address := listener.Addr().(*net.TCPAddr)
	return &sshTestServer{
		address:     address.IP.String(),
		port:        address.Port,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
	}
}

func (s *sshTestServer) host(password string) Host {
	return Host{
		Address:   s.address,
		Port:      s.port,
		Username:  "test",
		Password:  password,
		AuthType:  "password",
		Timeout:   2,
		KeepAlive: 3600,
	}
}

func dialSSHTestClient(t *testing.T, server *sshTestServer) *ssh.Client {
	t.Helper()
	client, err := ssh.Dial("tcp", net.JoinHostPort(server.address, strconv.Itoa(server.port)), &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func serveShellSessions(channels <-chan ssh.NewChannel, acceptPTY bool, sftpAttempts *atomic.Int32) {
	var sessions sync.WaitGroup
	defer sessions.Wait()
	for newChannel := range channels {
		if newChannel.ChannelType() != "session" {
			_ = newChannel.Reject(ssh.UnknownChannelType, "仅支持 session")
			continue
		}
		channel, requests, err := newChannel.Accept()
		if err != nil {
			continue
		}
		sessions.Add(1)
		go func() {
			defer sessions.Done()
			defer channel.Close()
			for request := range requests {
				accepted := false
				switch request.Type {
				case "pty-req":
					accepted = acceptPTY
				case "shell":
					accepted = true
				case "subsystem":
					var payload struct{ Name string }
					if ssh.Unmarshal(request.Payload, &payload) == nil && payload.Name == "sftp" && sftpAttempts != nil {
						sftpAttempts.Add(1)
					}
				}
				_ = request.Reply(accepted, nil)
			}
		}()
	}
}

func requireConnectionStage(t *testing.T, err error, want ConnectionStage) *ConnectionError {
	t.Helper()
	if err == nil {
		t.Fatalf("期望 %s 阶段错误，实际无错误", want)
	}
	var connectionErr *ConnectionError
	if !errors.As(err, &connectionErr) {
		t.Fatalf("错误类型 = %T，期望 *ConnectionError：%v", err, err)
	}
	if connectionErr.Stage != want {
		t.Fatalf("错误阶段 = %s，期望 %s：%v", connectionErr.Stage, want, err)
	}
	if connectionErr.Err == nil {
		t.Fatal("ConnectionError 未保留底层错误")
	}
	return connectionErr
}

func TestStartShellDoesNotRequireSFTP(t *testing.T) {
	var sftpAttempts atomic.Int32
	server := startSSHTestServer(t, &ssh.ServerConfig{NoClientAuth: true}, func(channels <-chan ssh.NewChannel) {
		serveShellSessions(channels, true, &sftpAttempts)
	})
	client := dialSSHTestClient(t, server)
	var released atomic.Int32
	shell, err := StartShellWithClient(client, func() {
		released.Add(1)
		_ = client.Close()
	}, func(string) {})
	if err != nil {
		t.Fatalf("未启用 SFTP 时启动终端失败：%v", err)
	}
	defer shell.Close()
	if sftpAttempts.Load() != 0 {
		t.Fatal("启动终端时不应提前创建 SFTP")
	}
	for attempt := 0; attempt < 2; attempt++ {
		_, listErr := shell.List("/")
		requireConnectionStage(t, listErr, StageSFTP)
		if released.Load() != 0 {
			t.Fatal("SFTP 失败不应关闭 SSH 终端")
		}
	}
	if got := sftpAttempts.Load(); got != 2 {
		t.Fatalf("SFTP 失败后重试次数 = %d，期望 2", got)
	}
	shell.Close()
	if got := released.Load(); got != 1 {
		t.Fatalf("终端释放次数 = %d，期望 1", got)
	}
	select {
	case doneErr := <-shell.Done():
		if doneErr != nil {
			t.Fatalf("主动关闭终端应视为正常结束：%v", doneErr)
		}
	case <-time.After(time.Second):
		t.Fatal("主动关闭终端后未结束")
	}
}

func TestShellSSHLatencyUsesConnectedClient(t *testing.T) {
	server := startSSHTestServer(t, &ssh.ServerConfig{NoClientAuth: true}, func(channels <-chan ssh.NewChannel) {
		serveShellSessions(channels, true, nil)
	})
	client := dialSSHTestClient(t, server)
	shell, err := StartShellWithClient(client, nil, func(string) {})
	if err != nil {
		t.Fatalf("启动测试终端失败：%v", err)
	}
	defer shell.Close()

	latency, err := shell.SSHLatency(time.Second)
	if err != nil {
		t.Fatalf("通过当前 SSH 连接探测延迟失败：%v", err)
	}
	if latency < 1 {
		t.Fatalf("SSH 往返延迟 = %dms，期望至少 1ms", latency)
	}
}

func TestStartShellPTYErrorStage(t *testing.T) {
	server := startSSHTestServer(t, &ssh.ServerConfig{NoClientAuth: true}, func(channels <-chan ssh.NewChannel) {
		serveShellSessions(channels, false, nil)
	})
	client := dialSSHTestClient(t, server)
	var released atomic.Int32
	_, err := StartShellWithClient(client, func() {
		released.Add(1)
		_ = client.Close()
	}, func(string) {})
	requireConnectionStage(t, err, StagePTY)
	if got := released.Load(); got != 1 {
		t.Fatalf("PTY 失败后的释放次数 = %d，期望 1", got)
	}
}

func TestConnectionErrorPreservesOriginalError(t *testing.T) {
	raw := &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("原始错误")}
	err := wrapConnectionError(StageTCP, raw)
	if !errors.Is(err, raw) {
		t.Fatal("ConnectionError 未保留 errors.Is 错误链")
	}
	var operationErr *net.OpError
	if !errors.As(err, &operationErr) || operationErr != raw {
		t.Fatal("ConnectionError 未保留 errors.As 类型链")
	}
	if got := dialErrorStage(&net.DNSError{Err: "解析失败", Name: "invalid"}); got != StageDNS {
		t.Fatalf("DNS 错误阶段 = %s，期望 %s", got, StageDNS)
	}
	if !IsRetryableConnectionError(err) {
		t.Fatal("TCP 连接错误应允许重试")
	}
	if IsRetryableConnectionError(wrapConnectionError(StageAuthentication, raw)) {
		t.Fatal("认证错误不应自动重试")
	}
	if !IsRetryableConnectionError(io.EOF) {
		t.Fatal("SSH 传输 EOF 应允许重试")
	}
	if IsRetryableConnectionError(context.Canceled) {
		t.Fatal("主动取消不应自动重试")
	}
}

func TestMonitorKeepAliveClosesAfterThreeConsecutiveFailures(t *testing.T) {
	ticks := make(chan time.Time, 6)
	connectionClosed := make(chan struct{})
	results := []error{errors.New("失败1"), errors.New("失败2"), nil, errors.New("失败1"), errors.New("失败2"), errors.New("失败3")}
	var attempts, closed atomic.Int32
	done := make(chan struct{})
	go func() {
		defer close(done)
		monitorKeepAlive(ticks, connectionClosed, func() error {
			index := int(attempts.Add(1)) - 1
			return results[index]
		}, func() { closed.Add(1) })
	}()
	for range results {
		ticks <- time.Now()
	}
	close(ticks)
	<-done
	if attempts.Load() != int32(len(results)) || closed.Load() != 1 {
		t.Fatalf("KeepAlive 尝试次数 = %d，关闭次数 = %d，期望 %d/1", attempts.Load(), closed.Load(), len(results))
	}
}

func TestMonitorKeepAliveStopsWhenConnectionCloses(t *testing.T) {
	ticks := make(chan time.Time)
	connectionClosed := make(chan struct{})
	done := make(chan struct{})
	var attempts atomic.Int32
	go func() {
		defer close(done)
		monitorKeepAlive(ticks, connectionClosed, func() error {
			attempts.Add(1)
			return nil
		}, func() {})
	}()
	close(connectionClosed)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("SSH 连接关闭后 KeepAlive 协程未及时退出")
	}
	if attempts.Load() != 0 {
		t.Fatalf("连接关闭后仍执行了 %d 次 KeepAlive", attempts.Load())
	}
}

func TestDialContextTCPAndHandshakeStages(t *testing.T) {
	t.Run("TCP", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().(*net.TCPAddr)
		_ = listener.Close()
		_, err = DialContext(context.Background(), Host{
			Address: address.IP.String(), Port: address.Port, Username: "test", Password: "test", Timeout: 1,
		})
		requireConnectionStage(t, err, StageTCP)
		var operationErr *net.OpError
		if !errors.As(err, &operationErr) {
			t.Fatalf("TCP 错误未保留 net.OpError：%v", err)
		}
	})

	t.Run("SSH握手", func(t *testing.T) {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		done := make(chan struct{})
		go func() {
			defer close(done)
			connection, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			_, _ = io.WriteString(connection, "HTTP/1.1 200 OK\r\n\r\n")
			_ = connection.Close()
		}()
		t.Cleanup(func() {
			_ = listener.Close()
			<-done
		})
		address := listener.Addr().(*net.TCPAddr)
		_, err = DialContext(context.Background(), Host{
			Address: address.IP.String(), Port: address.Port, Username: "test", Password: "test", Timeout: 1,
		})
		requireConnectionStage(t, err, StageSSHHandshake)
	})
}

func TestDialContextSOCKS5Stage(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		header := make([]byte, 2)
		if _, readErr := io.ReadFull(connection, header); readErr != nil {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, readErr := io.ReadFull(connection, methods); readErr != nil {
			return
		}
		_, _ = connection.Write([]byte{5, 0})
		request := make([]byte, 10)
		if _, readErr := io.ReadFull(connection, request); readErr != nil {
			return
		}
		_, _ = connection.Write([]byte{5, 5, 0, 1, 0, 0, 0, 0, 0, 0})
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	proxyAddress := listener.Addr().(*net.TCPAddr)
	_, err = DialContext(context.Background(), Host{
		Address:      "127.0.0.1",
		Port:         22,
		Username:     "test",
		Password:     "test",
		ProxyEnabled: true,
		ProxyHost:    proxyAddress.IP.String(),
		ProxyPort:    proxyAddress.Port,
		Timeout:      1,
	})
	requireConnectionStage(t, err, StageSOCKS5)
}

func TestLatencyMeasuresSOCKS5TargetConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		header := make([]byte, 2)
		if _, readErr := io.ReadFull(connection, header); readErr != nil {
			return
		}
		methods := make([]byte, int(header[1]))
		if _, readErr := io.ReadFull(connection, methods); readErr != nil {
			return
		}
		if _, writeErr := connection.Write([]byte{5, 0}); writeErr != nil {
			return
		}
		request := make([]byte, 10)
		if _, readErr := io.ReadFull(connection, request); readErr != nil {
			return
		}
		if _, writeErr := connection.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 22}); writeErr != nil {
			return
		}
		probe := make([]byte, len("SSH-2.0-SanTianShell-Latency\r\n"))
		_ = connection.SetReadDeadline(time.Now().Add(time.Second))
		if _, readErr := io.ReadFull(connection, probe); readErr != nil {
			return
		}
		time.Sleep(80 * time.Millisecond)
		_, _ = connection.Write([]byte("SSH-2.0-test-server\r\n"))
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		<-done
	})
	proxyAddress := listener.Addr().(*net.TCPAddr)
	latency, err := Latency(Host{
		Address:      "192.0.2.10",
		Port:         22,
		Protocol:     "ssh",
		ProxyEnabled: true,
		ProxyHost:    proxyAddress.IP.String(),
		ProxyPort:    proxyAddress.Port,
	})
	if err != nil {
		t.Fatal(err)
	}
	if latency < 70 {
		t.Fatalf("延迟未包含 SOCKS5 CONNECT 到目标端口的建立时间：%dms", latency)
	}
}

func TestExplainProxySSHHandshakeEOF(t *testing.T) {
	raw := fmt.Errorf("ssh: handshake failed: %w", io.EOF)
	err := explainProxySSHHandshakeError("192.0.2.10:22", raw)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("应保留原始 EOF 错误链：%v", err)
	}
	message := err.Error()
	for _, expected := range []string{"SOCKS5 隧道已建立", "192.0.2.10:22", "发送 SSH 服务标识前关闭连接", "ssh: handshake failed: EOF"} {
		if !strings.Contains(message, expected) {
			t.Fatalf("错误提示缺少 %q：%s", expected, message)
		}
	}

	other := errors.New("ssh: handshake failed: connection reset")
	if got := explainProxySSHHandshakeError("192.0.2.10:22", other); got != other {
		t.Fatalf("非 EOF 握手错误不应改写：%v", got)
	}
}

func TestExplainSSHHandshakeMaxStartups(t *testing.T) {
	raw := fmt.Errorf("ssh: handshake failed: %w", io.EOF)
	err := explainSSHHandshakeRejection("192.0.2.10:22", "Exceeded MaxStartups\r\n", raw, true)
	if !errors.Is(err, io.EOF) {
		t.Fatalf("应保留原始 EOF 错误链：%v", err)
	}
	for _, expected := range []string{"SOCKS5 隧道已建立", "192.0.2.10:22", "Exceeded MaxStartups", "未认证并发连接"} {
		if !strings.Contains(err.Error(), expected) {
			t.Fatalf("错误提示缺少 %q：%s", expected, err)
		}
	}
}

func TestManagedConnectionRetryDelay(t *testing.T) {
	ordinary := managedConnectionRetryDelay(io.EOF, 1)
	limited := managedConnectionRetryDelay(errors.New("Exceeded MaxStartups"), 1)
	if ordinary != 500*time.Millisecond {
		t.Fatalf("普通握手重试等待 = %s", ordinary)
	}
	if limited != 800*time.Millisecond {
		t.Fatalf("MaxStartups 重试等待 = %s", limited)
	}
	if got := managedConnectionRetryDelay(io.EOF, 2); got != time.Second {
		t.Fatalf("第二次重试未指数退避：%s", got)
	}
}

func TestHostKeyConfirmationAndSaveTiming(t *testing.T) {
	t.Run("未确认时拒绝", func(t *testing.T) {
		var authenticated atomic.Int32
		server := startSSHTestServer(t, passwordServerConfig("secret", &authenticated), nil)
		host := server.host("secret")
		var remembered atomic.Int32
		host.RememberHostKey = func(string) error {
			remembered.Add(1)
			return nil
		}
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageHostKey)
		if authenticated.Load() != 0 {
			t.Fatal("主机指纹确认前不应进入认证")
		}
		if remembered.Load() != 0 {
			t.Fatal("未确认的主机指纹不应保存")
		}
	})

	t.Run("认证失败不保存", func(t *testing.T) {
		server := startSSHTestServer(t, passwordServerConfig("secret", nil), nil)
		host := server.host("wrong")
		var confirmed, remembered atomic.Int32
		host.ConfirmHostKey = func(fingerprint string) error {
			confirmed.Add(1)
			if fingerprint != server.fingerprint {
				t.Fatalf("确认指纹 = %s，期望 %s", fingerprint, server.fingerprint)
			}
			return nil
		}
		host.RememberHostKey = func(string) error {
			remembered.Add(1)
			return nil
		}
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageAuthentication)
		if confirmed.Load() != 1 || remembered.Load() != 0 {
			t.Fatalf("确认次数 = %d，保存次数 = %d，期望 1/0", confirmed.Load(), remembered.Load())
		}
	})

	t.Run("认证成功后保存", func(t *testing.T) {
		var authenticated atomic.Int32
		server := startSSHTestServer(t, passwordServerConfig("secret", &authenticated), nil)
		host := server.host("secret")
		var confirmedFingerprint, rememberedFingerprint string
		host.ConfirmHostKey = func(fingerprint string) error {
			confirmedFingerprint = fingerprint
			return nil
		}
		host.RememberHostKey = func(fingerprint string) error {
			rememberedFingerprint = fingerprint
			return nil
		}
		client, err := DialContext(context.Background(), host)
		if err != nil {
			t.Fatalf("首次确认后连接失败：%v", err)
		}
		_ = client.Close()
		if authenticated.Load() != 1 {
			t.Fatalf("认证次数 = %d，期望 1", authenticated.Load())
		}
		if confirmedFingerprint != server.fingerprint || rememberedFingerprint != server.fingerprint {
			t.Fatalf("确认指纹 = %q，保存指纹 = %q，期望 %q", confirmedFingerprint, rememberedFingerprint, server.fingerprint)
		}
	})

	t.Run("保存错误保留原始链", func(t *testing.T) {
		server := startSSHTestServer(t, passwordServerConfig("secret", nil), nil)
		host := server.host("secret")
		host.ConfirmHostKey = func(string) error { return nil }
		saveErr := errors.New("保存失败")
		host.RememberHostKey = func(string) error { return saveErr }
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageHostKey)
		if !errors.Is(err, saveErr) {
			t.Fatalf("保存错误链丢失：%v", err)
		}
	})
}

func TestDialContextUsesDynamicKeyboardInteractive(t *testing.T) {
	var serverChallenge atomic.Int32
	config := &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, challenge ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			serverChallenge.Add(1)
			answers, err := challenge("动态验证", "请输入本次验证码", []string{"验证码："}, []bool{false})
			if err != nil {
				return nil, err
			}
			if len(answers) != 1 || answers[0] != "654321" {
				return nil, errors.New("验证码错误")
			}
			return nil, nil
		},
	}
	server := startSSHTestServer(t, config, nil)
	host := server.host("")
	host.AuthType = "password_mfa"
	host.MFACode = "过期验证码"
	host.ConfirmHostKey = func(string) error { return nil }
	host.KeyboardInteractive = func(_, _ string, questions []string, _ []bool) ([]string, error) {
		if len(questions) != 1 {
			t.Fatalf("动态验证问题数 = %d，期望 1", len(questions))
		}
		return []string{"654321"}, nil
	}
	client, err := DialContext(context.Background(), host)
	if err != nil {
		t.Fatalf("动态 MFA 连接失败：%v", err)
	}
	_ = client.Close()
	if serverChallenge.Load() != 1 {
		t.Fatalf("动态 MFA 调用次数 = %d，期望 1", serverChallenge.Load())
	}
}

func TestMFARequiresDynamicCallbackAndIgnoresSavedCode(t *testing.T) {
	host := Host{
		Address:  "127.0.0.1",
		Port:     22,
		Username: "test",
		Password: "password",
		AuthType: "password_mfa",
		MFACode:  "123456",
	}
	_, err := DialContext(context.Background(), host)
	requireConnectionStage(t, err, StageAuthentication)

	changed := host
	changed.MFACode = "654321"
	if connectionFingerprint(host) != connectionFingerprint(changed) {
		t.Fatal("一次性 MFA 验证码不应影响连接池复用键")
	}
}

func TestHostKeyPromptDoesNotConsumeNetworkDeadline(t *testing.T) {
	server := startSSHTestServer(t, passwordServerConfig("secret", nil), nil)
	host := server.host("secret")
	host.Timeout = 1
	host.ConfirmHostKey = func(string) error {
		time.Sleep(1100 * time.Millisecond)
		return nil
	}
	client, err := DialContext(context.Background(), host)
	if err != nil {
		t.Fatalf("等待指纹确认不应消耗 SSH 网络超时：%v", err)
	}
	_ = client.Close()
}

func TestConnectionProgressReportsEachStageOnce(t *testing.T) {
	server := startSSHTestServer(t, passwordServerConfig("secret", nil), nil)
	host := server.host("wrong")
	host.ConfirmHostKey = func(string) error { return nil }
	var mutex sync.Mutex
	var stages []ConnectionStage
	host.ConnectionProgress = func(stage ConnectionStage) {
		mutex.Lock()
		stages = append(stages, stage)
		mutex.Unlock()
	}
	_, err := DialContext(context.Background(), host)
	requireConnectionStage(t, err, StageAuthentication)
	mutex.Lock()
	got := append([]ConnectionStage(nil), stages...)
	mutex.Unlock()
	want := []ConnectionStage{StageDNS, StageTCP, StageSSHHandshake, StageHostKey, StageAuthentication}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("连接进度 = %v，期望 %v", got, want)
	}
}

func TestPasswordAuthenticationFailureClassification(t *testing.T) {
	t.Run("普通密码被拒绝", func(t *testing.T) {
		var attempts atomic.Int32
		server := startSSHTestServer(t, rejectingPasswordServerConfig(&attempts), nil)
		host := server.host("wrong")
		host.ConfirmHostKey = func(string) error { return nil }
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageAuthentication)
		if attempts.Load() != 1 {
			t.Fatalf("服务器密码回调次数 = %d，期望 1", attempts.Load())
		}
		if !IsPasswordAuthenticationFailure(err) {
			t.Fatalf("普通密码拒绝未被识别：%v", err)
		}
		var rejectedErr *PasswordRejectedError
		if !errors.As(err, &rejectedErr) || rejectedErr.Unwrap() == nil {
			t.Fatal("密码拒绝错误未保留原始错误链")
		}
	})

	t.Run("空认证类型沿用普通密码", func(t *testing.T) {
		var attempts atomic.Int32
		server := startSSHTestServer(t, rejectingPasswordServerConfig(&attempts), nil)
		host := server.host("wrong")
		host.AuthType = ""
		host.ConfirmHostKey = func(string) error { return nil }
		_, err := DialContext(context.Background(), host)
		if attempts.Load() != 1 || !IsPasswordAuthenticationFailure(err) {
			t.Fatalf("空认证类型的密码拒绝识别失败，回调次数 = %d，错误：%v", attempts.Load(), err)
		}
	})

	t.Run("MFA密码被拒绝不归类为普通密码", func(t *testing.T) {
		var attempts atomic.Int32
		server := startSSHTestServer(t, rejectingPasswordServerConfig(&attempts), nil)
		host := server.host("wrong")
		host.AuthType = "password_mfa"
		host.ConfirmHostKey = func(string) error { return nil }
		host.KeyboardInteractive = func(_, _ string, _ []string, _ []bool) ([]string, error) { return nil, nil }
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageAuthentication)
		if attempts.Load() != 1 {
			t.Fatalf("服务器密码回调次数 = %d，期望 1", attempts.Load())
		}
		if IsPasswordAuthenticationFailure(err) {
			t.Fatalf("MFA 认证不应归类为普通密码拒绝：%v", err)
		}
	})

	t.Run("服务器未开放密码认证", func(t *testing.T) {
		server := startSSHTestServer(t, &ssh.ServerConfig{
			PublicKeyCallback: func(ssh.ConnMetadata, ssh.PublicKey) (*ssh.Permissions, error) {
				return nil, errors.New("公钥错误")
			},
		}, nil)
		host := server.host("wrong")
		host.ConfirmHostKey = func(string) error { return nil }
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageAuthentication)
		if IsPasswordAuthenticationFailure(err) {
			t.Fatalf("服务端未开放密码认证时不应提示密码重试：%v", err)
		}
	})

	t.Run("密钥与Agent本地错误", func(t *testing.T) {
		for _, host := range []Host{
			{AuthType: "key"},
			{AuthType: "agent"},
		} {
			_, err := DialContext(context.Background(), host)
			requireConnectionStage(t, err, StageAuthentication)
			if IsPasswordAuthenticationFailure(err) {
				t.Fatalf("%s 认证错误不应归类为密码拒绝：%v", host.AuthType, err)
			}
		}
	})

	t.Run("网络和上下文错误", func(t *testing.T) {
		for _, err := range []error{
			context.Canceled,
			context.DeadlineExceeded,
			io.EOF,
			io.ErrUnexpectedEOF,
			&net.OpError{Op: "read", Net: "tcp", Err: errors.New("连接中断")},
		} {
			wrapped := wrapConnectionError(StageAuthentication, &PasswordRejectedError{Err: err})
			if IsPasswordAuthenticationFailure(wrapped) {
				t.Fatalf("传输错误不应归类为密码拒绝：%v", wrapped)
			}
		}
	})
}

func rejectingPasswordServerConfig(attempts *atomic.Int32) *ssh.ServerConfig {
	return &ssh.ServerConfig{
		PasswordCallback: func(ssh.ConnMetadata, []byte) (*ssh.Permissions, error) {
			attempts.Add(1)
			return nil, errors.New("密码错误")
		},
	}
}

func passwordServerConfig(password string, authenticated *atomic.Int32) *ssh.ServerConfig {
	return &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, provided []byte) (*ssh.Permissions, error) {
			if string(provided) != password {
				return nil, errors.New("密码错误")
			}
			if authenticated != nil {
				authenticated.Add(1)
			}
			return nil, nil
		},
	}
}

func TestPAMKeyboardInteractivePasswordFallback(t *testing.T) {
	t.Run("仅开放keyboard-interactive时自动填充密码成功", func(t *testing.T) {
		var attempts atomic.Int32
		server := startSSHTestServer(t, &ssh.ServerConfig{
			KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				attempts.Add(1)
				answers, err := client("", "", []string{"Password: "}, []bool{false})
				if err != nil {
					return nil, err
				}
				if len(answers) != 1 || answers[0] != "secret" {
					return nil, errors.New("认证失败")
				}
				return nil, nil
			},
		}, nil)
		host := server.host("secret")
		host.HostKey = server.fingerprint
		client, err := DialContext(context.Background(), host)
		if err != nil {
			t.Fatalf("PAM 密码回退连接失败: %v", err)
		}
		_ = client.Close()
		if attempts.Load() != 1 {
			t.Fatalf("PAM 请求次数 = %d，期望 1", attempts.Load())
		}
	})

	t.Run("仅开放keyboard-interactive时密码错误归类准确且仅尝试一次", func(t *testing.T) {
		var attempts atomic.Int32
		server := startSSHTestServer(t, &ssh.ServerConfig{
			KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				attempts.Add(1)
				answers, err := client("", "", []string{"user@host's password: "}, []bool{false})
				if err != nil {
					return nil, err
				}
				if len(answers) != 1 || answers[0] != "secret" {
					return nil, errors.New("认证失败")
				}
				return nil, nil
			},
		}, nil)
		host := server.host("wrong")
		host.HostKey = server.fingerprint
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageAuthentication)
		if attempts.Load() != 1 {
			t.Fatalf("PAM 错误密码尝试次数 = %d，期望 1", attempts.Load())
		}
		if !IsPasswordAuthenticationFailure(err) {
			t.Fatalf("PAM 密码错误应归类为普通密码拒绝：%v", err)
		}
	})

	t.Run("同时开放password和keyboard-interactive时错误密码不重复提交", func(t *testing.T) {
		var passwordAttempts atomic.Int32
		var kbdResponses atomic.Int32
		server := startSSHTestServer(t, &ssh.ServerConfig{
			PasswordCallback: func(_ ssh.ConnMetadata, _ []byte) (*ssh.Permissions, error) {
				passwordAttempts.Add(1)
				return nil, errors.New("密码错误")
			},
			KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
				answers, err := client("", "", []string{"Password: "}, []bool{false})
				if err == nil && len(answers) > 0 {
					kbdResponses.Add(1)
				}
				return nil, errors.New("密码错误")
			},
		}, nil)
		host := server.host("wrong")
		host.HostKey = server.fingerprint
		_, err := DialContext(context.Background(), host)
		requireConnectionStage(t, err, StageAuthentication)
		if passwordAttempts.Load() != 1 {
			t.Fatalf("password 尝试次数 = %d，期望 1", passwordAttempts.Load())
		}
		if kbdResponses.Load() != 0 {
			t.Fatalf("password 拒绝后 keyboard-interactive 不应再次提交密码，实际次数 = %d", kbdResponses.Load())
		}
		if !IsPasswordAuthenticationFailure(err) {
			t.Fatalf("应归类为普通密码拒绝：%v", err)
		}
	})
}

func TestStartShellWithClientSizeSendsMeasuredDimensions(t *testing.T) {
	var cols, rows atomic.Uint32
	done := make(chan struct{})
	server := startSSHTestServer(t, &ssh.ServerConfig{NoClientAuth: true}, func(channels <-chan ssh.NewChannel) {
		var sessions sync.WaitGroup
		defer sessions.Wait()
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			ch, reqs, err := newChannel.Accept()
			if err != nil {
				continue
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				defer ch.Close()
				for req := range reqs {
					switch req.Type {
					case "pty-req":
						var payload struct {
							Term     string
							Columns  uint32
							Rows     uint32
							WidthPx  uint32
							HeightPx uint32
							Modelist string
						}
						if err := ssh.Unmarshal(req.Payload, &payload); err == nil {
							cols.Store(payload.Columns)
							rows.Store(payload.Rows)
						} else {
							t.Errorf("解析 pty-req 失败: %v", err)
						}
						_ = req.Reply(true, nil)
					case "shell":
						_ = req.Reply(true, nil)
						close(done)
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()
		}
	})
	client := dialSSHTestClient(t, server)
	shell, err := StartShellWithClientSize(client, 34, 132, func() { _ = client.Close() }, func(string) {})
	if err != nil {
		t.Fatalf("StartShellWithClientSize 失败: %v", err)
	}
	defer shell.Close()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("等待 PTY 请求超时")
	}
	if rows.Load() != 34 || cols.Load() != 132 {
		t.Fatalf("PTY 初始尺寸 = (%d, %d)，期望 (34, 132)", rows.Load(), cols.Load())
	}
}

func TestConnectionManagerEvictsClosedClientImmediately(t *testing.T) {
	server := startSSHTestServer(t, passwordServerConfig("secret", nil), nil)
	manager := NewConnectionManager()
	host := server.host("secret")
	host.HostKey = server.fingerprint
	client, release, err := manager.AcquireContext(context.Background(), "h-evict", host)
	if err != nil {
		t.Fatalf("AcquireContext 失败: %v", err)
	}
	_ = client.Close()
	release()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		manager.mu.Lock()
		remaining := len(manager.entries)
		manager.mu.Unlock()
		if remaining == 0 {
			return
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatal("底层 SSH 连接断开后连接池未及时驱逐缓存条目")
}
