//go:build windows

package sshagent

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh/agent"
)

func TestPageantConnectionTransportsOneAgentPacket(t *testing.T) {
	original := pageantQuery
	t.Cleanup(func() { pageantQuery = original })
	request := []byte{0, 0, 0, 1, 11}
	response := []byte{0, 0, 0, 2, 12, 34}
	pageantQuery = func(packet []byte) ([]byte, error) {
		if !bytes.Equal(packet, request) {
			t.Fatalf("Pageant 请求 = %v，期望 %v", packet, request)
		}
		return response, nil
	}
	connection := newPageantConnection(context.Background())
	if n, err := connection.Write(request); err != nil || n != len(request) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	got, err := io.ReadAll(connection)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, response) {
		t.Fatalf("Pageant 响应 = %v，期望 %v", got, response)
	}
}

func TestPageantConnectionHonoursCloseAndContext(t *testing.T) {
	connection := newPageantConnection(context.Background())
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := connection.Write([]byte{0, 0, 0, 1, 11}); err == nil {
		t.Fatal("关闭后的 Pageant 连接仍可写入")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	connection = newPageantConnection(ctx)
	if _, err := connection.Write([]byte{0, 0, 0, 1, 11}); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误 = %v，期望 context.Canceled", err)
	}
}

func TestQueryPageantRejectsMalformedPacketBeforeIPC(t *testing.T) {
	if _, err := queryPageant([]byte{0, 0, 0}); err == nil {
		t.Fatal("过短请求应被拒绝")
	}
	packet := make([]byte, 5)
	binary.BigEndian.PutUint32(packet, 10)
	if _, err := queryPageant(packet); err == nil {
		t.Fatal("长度前缀不一致应被拒绝")
	}

	overlong := make([]byte, pageantMaxMessage+1)
	binary.BigEndian.PutUint32(overlong, uint32(len(overlong)-4))
	if _, err := queryPageant(overlong); err == nil || !strings.Contains(err.Error(), "请求长度无效") {
		t.Fatalf("超长请求错误 = %v，期望请求长度无效", err)
	}
}

func TestQueryPageantAcceptsMaximumPacketBeforeIPC(t *testing.T) {
	if findPageantWindow() != 0 {
		t.Skip("检测到真实 Pageant，跳过伪造的 8192 字节边界请求")
	}
	packet := make([]byte, pageantMaxMessage)
	binary.BigEndian.PutUint32(packet, uint32(len(packet)-4))
	_, err := queryPageant(packet)
	if err == nil || !strings.Contains(err.Error(), "未检测到 Pageant") {
		t.Fatalf("完整 8192 字节请求未通过格式检查: %v", err)
	}
}

func TestPageantConnectionSupportsConsecutiveAgentRPCs(t *testing.T) {
	original := pageantQuery
	t.Cleanup(func() { pageantQuery = original })
	request := []byte{0, 0, 0, 1, 11}
	response := []byte{0, 0, 0, 5, 12, 0, 0, 0, 0}
	queries := 0
	pageantQuery = func(packet []byte) ([]byte, error) {
		queries++
		if !bytes.Equal(packet, request) {
			return nil, errors.New("收到非预期的 SSH Agent 请求")
		}
		return append([]byte(nil), response...), nil
	}

	connection := newPageantConnection(context.Background())
	defer connection.Close()
	client := agent.NewClient(connection)
	for index := 0; index < 2; index++ {
		signers, err := client.Signers()
		if err != nil {
			t.Fatalf("第 %d 次 Signers 失败: %v", index+1, err)
		}
		if len(signers) != 0 {
			t.Fatalf("第 %d 次 Signers 返回 %d 个密钥，期望 0", index+1, len(signers))
		}
	}
	if queries != 2 {
		t.Fatalf("Pageant 查询次数 = %d，期望 2", queries)
	}
}

func TestPageantConnectionConcurrentCloseDuringWrite(t *testing.T) {
	original := pageantQuery
	t.Cleanup(func() { pageantQuery = original })
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWrite := func() { releaseOnce.Do(func() { close(release) }) }
	defer releaseWrite()
	pageantQuery = func([]byte) ([]byte, error) {
		close(started)
		<-release
		return []byte{0, 0, 0, 1, 12}, nil
	}

	connection := newPageantConnection(context.Background())
	type writeResult struct {
		count int
		err   error
	}
	written := make(chan writeResult, 1)
	request := []byte{0, 0, 0, 1, 11}
	go func() {
		count, err := connection.Write(request)
		written <- writeResult{count: count, err: err}
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("在途 Pageant Write 未启动")
	}

	const closers = 16
	var closeWG sync.WaitGroup
	closeWG.Add(closers)
	closed := make(chan struct{})
	for index := 0; index < closers; index++ {
		go func() {
			defer closeWG.Done()
			_ = connection.Close()
		}()
	}
	go func() {
		closeWG.Wait()
		close(closed)
	}()
	select {
	case <-closed:
		t.Fatal("在途 Write 完成前 Close 不应越过连接互斥锁")
	default:
	}

	releaseWrite()
	select {
	case result := <-written:
		if result.err != nil || result.count != len(request) {
			t.Fatalf("在途 Write = %d, %v", result.count, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("释放 mock 后在途 Write 未完成")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("在途 Write 完成后并发 Close 未完成")
	}
	if _, err := connection.Write(request); err == nil {
		t.Fatal("并发 Close 完成后 Pageant 连接仍可写入")
	}
}
