package sshagent

import (
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"golang.org/x/crypto/ssh"
)

type testSigner struct{ id int }

func (s testSigner) PublicKey() ssh.PublicKey { return nil }

func (s testSigner) Sign(io.Reader, []byte) (*ssh.Signature, error) {
	return &ssh.Signature{Format: "test"}, nil
}

type testConnection struct {
	closeCount int
}

func (*testConnection) Read([]byte) (int, error)    { return 0, io.EOF }
func (*testConnection) Write(p []byte) (int, error) { return len(p), nil }
func (c *testConnection) Close() error {
	c.closeCount++
	return nil
}

type testSignerSource struct {
	signers []ssh.Signer
	err     error
}

func (s testSignerSource) Signers() ([]ssh.Signer, error) {
	return s.signers, s.err
}

func makeSigners(count int) []ssh.Signer {
	signers := make([]ssh.Signer, count)
	for i := range signers {
		signers[i] = testSigner{id: i}
	}
	return signers
}

func TestLimitSignersUsesDefaultAndCopies(t *testing.T) {
	original := makeSigners(9)
	limited := limitSigners(original, 0)
	if got := len(limited); got != DefaultMaxSigners {
		t.Fatalf("默认签名器数量 = %d，期望 %d", got, DefaultMaxSigners)
	}

	limited[0] = testSigner{id: 99}
	if reflect.DeepEqual(limited[0], original[0]) {
		t.Fatal("返回切片应与 Agent 原始切片分离")
	}
}

func TestLimitSignersUsesRequestedLimit(t *testing.T) {
	if got := len(limitSigners(makeSigners(5), 2)); got != 2 {
		t.Fatalf("签名器数量 = %d，期望 2", got)
	}
	if got := len(limitSigners(makeSigners(2), 20)); got != 2 {
		t.Fatalf("签名器数量 = %d，期望保留全部 2 个", got)
	}
}

func TestLoadSessionClosesConnectionOnSignerError(t *testing.T) {
	connection := &testConnection{}
	wantErr := errors.New("agent protocol failed")
	_, err := loadSession(
		context.Background(),
		dialCandidate{source: SourceOpenSSH, address: "pipe"},
		connection,
		testSignerSource{err: wantErr},
		0,
	)
	if !errors.Is(err, wantErr) {
		t.Fatalf("错误 = %v，期望包装 %v", err, wantErr)
	}
	if connection.closeCount != 1 {
		t.Fatalf("连接关闭次数 = %d，期望 1", connection.closeCount)
	}
}

func TestLoadSessionRejectsEmptyAgent(t *testing.T) {
	connection := &testConnection{}
	_, err := loadSession(
		context.Background(),
		dialCandidate{source: SourceOpenSSH, address: "pipe"},
		connection,
		testSignerSource{},
		0,
	)
	if !errors.Is(err, ErrNoSigners) {
		t.Fatalf("错误 = %v，期望 %v", err, ErrNoSigners)
	}
	if connection.closeCount != 1 {
		t.Fatalf("连接关闭次数 = %d，期望 1", connection.closeCount)
	}
}

func TestSessionReturnsCopyAndClosesOnce(t *testing.T) {
	connection := &testConnection{}
	session, err := loadSession(
		context.Background(),
		dialCandidate{source: SourceOpenSSH, address: "pipe"},
		connection,
		testSignerSource{signers: makeSigners(2)},
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	first := session.Signers()
	first[0] = testSigner{id: 99}
	if reflect.DeepEqual(first[0], session.Signers()[0]) {
		t.Fatal("Signers 应返回切片副本")
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	if connection.closeCount != 1 {
		t.Fatalf("连接关闭次数 = %d，期望 1", connection.closeCount)
	}
}

func TestOpenRejectsNilAndCanceledContext(t *testing.T) {
	if _, err := Open(nil, 0); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("nil context 错误 = %v，期望 %v", err, ErrUnavailable)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Open(ctx, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误 = %v，期望 context.Canceled", err)
	}
}
