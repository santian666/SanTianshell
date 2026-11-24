package remote

import (
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestTCPForwardReportsSOCKS5DialError(t *testing.T) {
	closedListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxyPort := closedListener.Addr().(*net.TCPAddr).Port
	if err = closedListener.Close(); err != nil {
		t.Fatal(err)
	}

	forward, localPort, err := StartSOCKS5Forward(Host{
		ProxyEnabled: true,
		ProxyHost:    "127.0.0.1",
		ProxyPort:    proxyPort,
		Timeout:      1,
	}, "127.0.0.1:3389")
	if err != nil {
		t.Fatal(err)
	}
	defer forward.Close()

	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	select {
	case runtimeErr := <-forward.Errors():
		if runtimeErr == nil || !strings.Contains(runtimeErr.Error(), "RDP SOCKS5 转发连接失败") {
			t.Fatalf("未保留 SOCKS5 拨号错误上下文: %v", runtimeErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("未收到 SOCKS5 拨号错误")
	}

	forward.Close()
	assertForwardErrorsClosed(t, forward.Errors())
}

func TestTCPForwardReportsCopyErrorUnlessClosed(t *testing.T) {
	sentinel := errors.New("双向复制原始错误")
	forward := &TCPForward{errors: make(chan error, tcpForwardErrorBuffer)}
	var wg sync.WaitGroup
	wg.Add(1)
	go forward.copy(&wg, io.Discard, failingReader{err: sentinel}, "测试方向")
	wg.Wait()

	select {
	case runtimeErr := <-forward.Errors():
		if !errors.Is(runtimeErr, sentinel) {
			t.Fatalf("复制错误未保留原始错误: %v", runtimeErr)
		}
		if !strings.Contains(runtimeErr.Error(), "测试方向") {
			t.Fatalf("复制错误缺少方向: %v", runtimeErr)
		}
	default:
		t.Fatal("未收到双向复制错误")
	}

	forward.mu.Lock()
	forward.closed = true
	forward.mu.Unlock()
	wg.Add(1)
	go forward.copy(&wg, io.Discard, failingReader{err: sentinel}, "主动关闭")
	wg.Wait()
	select {
	case runtimeErr := <-forward.Errors():
		t.Fatalf("主动关闭仍报告复制错误: %v", runtimeErr)
	default:
	}
}

func TestTCPForwardErrorReportingIsConcurrentAndNonBlocking(t *testing.T) {
	forward := &TCPForward{errors: make(chan error, tcpForwardErrorBuffer)}
	const reporters = 100
	var wg sync.WaitGroup
	wg.Add(reporters)
	for index := 0; index < reporters; index++ {
		go func() {
			defer wg.Done()
			forward.reportError(errors.New("并发错误"))
		}()
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("错误通道写满后阻塞了转发协程")
	}
	if got := len(forward.errors); got != tcpForwardErrorBuffer {
		t.Fatalf("错误缓冲数量 = %d，期望 %d", got, tcpForwardErrorBuffer)
	}
}

func TestTCPForwardConcurrentCloseSuppressesCancellationError(t *testing.T) {
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	accepted := make(chan net.Conn, 1)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := proxyListener.Accept()
		if acceptErr != nil {
			return
		}
		accepted <- connection
		_, _ = io.Copy(io.Discard, connection)
		_ = connection.Close()
	}()

	proxyPort := proxyListener.Addr().(*net.TCPAddr).Port
	forward, localPort, err := StartSOCKS5Forward(Host{
		ProxyEnabled: true,
		ProxyHost:    "127.0.0.1",
		ProxyPort:    proxyPort,
		Timeout:      1,
	}, "127.0.0.1:3389")
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(localPort)))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	var proxyConnection net.Conn
	select {
	case proxyConnection = <-accepted:
	case <-time.After(2 * time.Second):
		forward.Close()
		t.Fatal("SOCKS5 测试连接未建立")
	}
	defer proxyConnection.Close()

	const closers = 32
	var closeWG sync.WaitGroup
	closeWG.Add(closers)
	for index := 0; index < closers; index++ {
		go func() {
			defer closeWG.Done()
			forward.Close()
		}()
	}
	closeWG.Wait()
	assertForwardErrorsClosed(t, forward.Errors())

	select {
	case <-serverDone:
	case <-time.After(2 * time.Second):
		t.Fatal("主动关闭后 SOCKS5 连接未及时结束")
	}
}

func assertForwardErrorsClosed(t *testing.T, runtimeErrors <-chan error) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for {
		select {
		case runtimeErr, open := <-runtimeErrors:
			if !open {
				return
			}
			t.Fatalf("主动关闭仍报告运行期错误: %v", runtimeErr)
		case <-deadline.C:
			t.Fatal("转发关闭后错误通道未关闭")
		}
	}
}
