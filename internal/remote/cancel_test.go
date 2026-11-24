package remote

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestAcquireContextCancelsWhileWaiting(t *testing.T) {
	host := Host{Address: "127.0.0.1", Port: 22, Username: "root", Password: "test"}
	entry := &managedConnection{
		fingerprint: connectionFingerprint(host),
		connecting:  true,
		ready:       make(chan struct{}),
	}
	manager := NewConnectionManager()
	manager.entries["host-1"] = entry
	t.Cleanup(entry.signalReady)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client, release, err := manager.AcquireContext(ctx, "host-1", host)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("等待连接取消错误 = %v，期望 context deadline exceeded", err)
	}
	if client != nil || release != nil {
		t.Fatal("取消后不应返回客户端或释放函数")
	}
}

func TestAcquireContextCancelsSSHHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("测试服务器未及时退出")
		}
	})

	address := listener.Addr().(*net.TCPAddr)
	host := Host{Address: address.IP.String(), Port: address.Port, Username: "root", Password: "test", Timeout: 5}
	manager := NewConnectionManager()
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	client, release, err := manager.AcquireContext(ctx, "host-1", host)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("握手取消错误 = %v，期望 context deadline exceeded", err)
	}
	if client != nil || release != nil {
		t.Fatal("取消后不应返回客户端或释放函数")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("握手取消耗时过长：%s", elapsed)
	}
	manager.mu.Lock()
	_, exists := manager.entries["host-1"]
	manager.mu.Unlock()
	if exists {
		t.Fatal("取消的连接不应留在连接池")
	}
}

func TestConnectionManagerCloseAllCancelsSSHHandshake(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	accepted := make(chan struct{})
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		close(accepted)
		defer connection.Close()
		_, _ = io.Copy(io.Discard, connection)
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("测试服务器未及时退出")
		}
	})

	address := listener.Addr().(*net.TCPAddr)
	host := Host{Address: address.IP.String(), Port: address.Port, Username: "root", Password: "test", Timeout: 30}
	manager := NewConnectionManager()
	type acquireResult struct {
		client  *ssh.Client
		release func()
		err     error
	}
	result := make(chan acquireResult, 1)
	go func() {
		client, release, acquireErr := manager.AcquireContext(context.Background(), "host-1", host)
		result <- acquireResult{client: client, release: release, err: acquireErr}
	}()
	select {
	case <-accepted:
	case <-time.After(time.Second):
		t.Fatal("SSH 测试握手未开始")
	}

	manager.CloseAll()
	select {
	case acquired := <-result:
		if acquired.err == nil || acquired.client != nil || acquired.release != nil {
			t.Fatalf("关闭连接池后的握手结果异常：client=%v release存在=%t err=%v", acquired.client, acquired.release != nil, acquired.err)
		}
	case <-time.After(time.Second):
		t.Fatal("关闭连接池后 SSH 握手未及时取消")
	}
}

func TestRunStreamingContextCancellationKeepsSharedClient(t *testing.T) {
	client, commandStarted := newCommandTestClient(t)
	ctx, cancel := context.WithCancel(context.Background())
	outputStarted := make(chan struct{})
	unblockOutput := make(chan struct{})
	var outputOnce sync.Once
	var unblockOnce sync.Once
	unblock := func() { unblockOnce.Do(func() { close(unblockOutput) }) }
	t.Cleanup(unblock)
	type commandResult struct {
		result Result
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		result, err := RunWithClientContextStreaming(ctx, client, "hang", func(string) {
			outputOnce.Do(func() { close(outputStarted) })
			<-unblockOutput
		})
		done <- commandResult{result: result, err: err}
	}()
	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("远程测试命令未启动")
	}
	select {
	case <-outputStarted:
	case <-time.After(time.Second):
		t.Fatal("流式输出回调未启动")
	}
	cancel()
	select {
	case canceled := <-done:
		if !errors.Is(canceled.err, context.Canceled) {
			t.Fatalf("命令取消错误 = %v，期望 context canceled", canceled.err)
		}
		if canceled.result.ExitCode != -1 {
			t.Fatalf("取消后的退出码 = %d，期望 -1", canceled.result.ExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("取消远程命令后仍未返回")
	}
	unblock()

	result, err := RunWithClientContext(context.Background(), client, "echo ok")
	if err != nil {
		t.Fatalf("取消单个 Session 后共享客户端不可用：%v", err)
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Output) != "ok" {
		t.Fatalf("后续命令结果异常：%+v", result)
	}
}

func TestRunWithClientTimeoutKeepsSharedClient(t *testing.T) {
	client, commandStarted := newCommandTestClient(t)
	type commandResult struct {
		result Result
		err    error
	}
	done := make(chan commandResult, 1)
	go func() {
		result, err := RunWithClientTimeout(client, "hang", 500*time.Millisecond)
		done <- commandResult{result: result, err: err}
	}()
	select {
	case <-commandStarted:
	case <-time.After(time.Second):
		t.Fatal("远程测试命令未启动")
	}
	select {
	case timedOut := <-done:
		if !errors.Is(timedOut.err, context.DeadlineExceeded) {
			t.Fatalf("命令超时错误 = %v，期望 context deadline exceeded", timedOut.err)
		}
		if timedOut.result.ExitCode != -1 {
			t.Fatalf("超时后的退出码 = %d，期望 -1", timedOut.result.ExitCode)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("远程命令超时后仍未返回")
	}

	result, err := RunWithClientContext(context.Background(), client, "echo ok")
	if err != nil {
		t.Fatalf("超时关闭单个 Session 后共享客户端不可用：%v", err)
	}
	if result.ExitCode != 0 || strings.TrimSpace(result.Output) != "ok" {
		t.Fatalf("后续命令结果异常：%+v", result)
	}
}

func newCommandTestClient(t *testing.T) (*ssh.Client, <-chan struct{}) {
	t.Helper()
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	serverConfig := &ssh.ServerConfig{NoClientAuth: true}
	serverConfig.AddHostKey(signer)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	commandStarted := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer connection.Close()
		serverConnection, channels, requests, serveErr := ssh.NewServerConn(connection, serverConfig)
		if serveErr != nil {
			serverDone <- serveErr
			return
		}
		defer serverConnection.Close()
		go ssh.DiscardRequests(requests)
		var startOnce sync.Once
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "仅支持 session")
				continue
			}
			channel, channelRequests, acceptErr := newChannel.Accept()
			if acceptErr != nil {
				continue
			}
			go serveCommandTestSession(channel, channelRequests, commandStarted, &startOnce)
		}
		serverDone <- nil
	}()

	clientConfig := &ssh.ClientConfig{
		User:            "test",
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         time.Second,
	}
	connection, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
	if err != nil {
		_ = listener.Close()
		t.Fatal(err)
	}
	sshConnection, channels, requests, err := ssh.NewClientConn(connection, listener.Addr().String(), clientConfig)
	if err != nil {
		_ = connection.Close()
		_ = listener.Close()
		t.Fatal(err)
	}
	client := ssh.NewClient(sshConnection, channels, requests)
	t.Cleanup(func() {
		_ = client.Close()
		_ = listener.Close()
		select {
		case serveErr := <-serverDone:
			if serveErr != nil && !errors.Is(serveErr, net.ErrClosed) {
				t.Errorf("SSH 测试服务器错误：%v", serveErr)
			}
		case <-time.After(time.Second):
			t.Error("SSH 测试服务器未及时退出")
		}
	})
	return client, commandStarted
}

func serveCommandTestSession(channel ssh.Channel, requests <-chan *ssh.Request, started chan struct{}, startOnce *sync.Once) {
	defer channel.Close()
	for request := range requests {
		if request.Type != "exec" {
			_ = request.Reply(false, nil)
			continue
		}
		var payload struct{ Command string }
		if err := ssh.Unmarshal(request.Payload, &payload); err != nil {
			_ = request.Reply(false, nil)
			continue
		}
		_ = request.Reply(true, nil)
		switch payload.Command {
		case "hang":
			_, _ = io.WriteString(channel, "running\n")
			startOnce.Do(func() { close(started) })
		case "echo ok":
			_, _ = io.WriteString(channel, "ok\n")
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: 0}))
			return
		default:
			status, _ := strconv.Atoi(payload.Command)
			_, _ = channel.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{Status: uint32(status)}))
			return
		}
	}
}
