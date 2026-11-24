// Package sshagent 提供对本机 SSH Agent 的最小只读接入。
//
// 本包只返回数量受限的 ssh.Signer，不暴露 Agent 客户端，也不启用
// agent forwarding。调用方在 SSH 认证完成前必须保持 Session 未关闭。
package sshagent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/agent"
)

const DefaultMaxSigners = 6

var (
	ErrUnavailable = errors.New("SSH Agent 不可用")
	ErrNoSigners   = errors.New("SSH Agent 中没有可用密钥")
)

// Source 表示签名器来自哪个本机 Agent 入口。
type Source string

const (
	SourceEnvironment Source = "SSH_AUTH_SOCK"
	SourceOpenSSH     Source = "Windows OpenSSH Agent"
	SourcePageant     Source = "Pageant"
)

// PageantStatus 是 Pageant 的检测结果。
type PageantStatus struct {
	Available bool   `json:"available"`
	Supported bool   `json:"supported"`
	Reason    string `json:"reason"`
}

// Session 持有 Agent 连接和本次允许使用的签名器。
type Session struct {
	signers  []ssh.Signer
	source   Source
	address  string
	closer   io.Closer
	closeOne sync.Once
	closeErr error
}

// Signers 返回签名器切片的副本。默认最多返回 DefaultMaxSigners 个。
func (s *Session) Signers() []ssh.Signer {
	if s == nil {
		return nil
	}
	return append([]ssh.Signer(nil), s.signers...)
}

// Source 返回实际连接成功的 Agent 来源。
func (s *Session) Source() Source {
	if s == nil {
		return ""
	}
	return s.source
}

// Address 返回实际连接成功的本机套接字或命名管道地址。
func (s *Session) Address() string {
	if s == nil {
		return ""
	}
	return s.address
}

// Close 关闭 Agent 连接。可安全重复调用。
func (s *Session) Close() error {
	if s == nil {
		return nil
	}
	s.closeOne.Do(func() {
		if s.closer != nil {
			s.closeErr = s.closer.Close()
		}
	})
	return s.closeErr
}

type agentConnection interface {
	io.Reader
	io.Writer
	io.Closer
}

type signerSource interface {
	Signers() ([]ssh.Signer, error)
}

type dialCandidate struct {
	source  Source
	address string
	dial    func(context.Context) (agentConnection, error)
}

// Open 连接本机 SSH Agent，并读取数量受限的签名器。
// maxSigners 小于等于 0 时使用默认上限 6。ctx 用于取消管道等待及 Agent 查询。
func Open(ctx context.Context, maxSigners int) (*Session, error) {
	if ctx == nil {
		return nil, fmt.Errorf("%w: context 不能为空", ErrUnavailable)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}

	var attempts []error
	for _, candidate := range platformCandidates() {
		connection, err := candidate.dial(ctx)
		if err != nil {
			attempts = append(attempts, fmt.Errorf("%s (%s): %w", candidate.source, candidate.address, err))
			continue
		}

		session, err := loadSession(ctx, candidate, connection, agent.NewClient(connection), maxSigners)
		if err == nil {
			return session, nil
		}
		attempts = append(attempts, err)
		if ctx.Err() != nil {
			break
		}
	}

	if len(attempts) == 0 {
		return nil, ErrUnavailable
	}
	return nil, errors.Join(ErrUnavailable, errors.Join(attempts...))
}

// DetectPageant 检测 Pageant 是否存在以及当前平台能否使用其协议。
func DetectPageant() PageantStatus {
	return detectPageant()
}

func loadSession(ctx context.Context, candidate dialCandidate, connection agentConnection, source signerSource, maxSigners int) (*Session, error) {
	stopCancel := context.AfterFunc(ctx, func() {
		_ = connection.Close()
	})
	signers, err := source.Signers()
	stopped := stopCancel()
	if ctxErr := ctx.Err(); ctxErr != nil {
		if stopped {
			_ = connection.Close()
		}
		return nil, fmt.Errorf("%s (%s) 查询密钥失败: %w", candidate.source, candidate.address, ctxErr)
	}
	if err != nil {
		_ = connection.Close()
		return nil, fmt.Errorf("%s (%s) 查询密钥失败: %w", candidate.source, candidate.address, err)
	}

	signers = limitSigners(signers, maxSigners)
	if len(signers) == 0 {
		_ = connection.Close()
		return nil, fmt.Errorf("%s (%s): %w", candidate.source, candidate.address, ErrNoSigners)
	}
	return &Session{
		signers: signers,
		source:  candidate.source,
		address: candidate.address,
		closer:  connection,
	}, nil
}

func limitSigners(signers []ssh.Signer, maxSigners int) []ssh.Signer {
	if maxSigners <= 0 {
		maxSigners = DefaultMaxSigners
	}
	if len(signers) > maxSigners {
		signers = signers[:maxSigners]
	}
	return append([]ssh.Signer(nil), signers...)
}
