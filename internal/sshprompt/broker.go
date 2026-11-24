package sshprompt

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
)

var ErrClosed = errors.New("SSH 交互已关闭")

type Question struct {
	Text string `json:"text"`
	Echo bool   `json:"echo"`
}

type Request struct {
	ID             string     `json:"id"`
	Kind           string     `json:"kind"`
	HostID         string     `json:"hostId"`
	HostName       string     `json:"hostName"`
	Address        string     `json:"address"`
	Fingerprint    string     `json:"fingerprint,omitempty"`
	Name           string     `json:"name,omitempty"`
	Instruction    string     `json:"instruction,omitempty"`
	Questions      []Question `json:"questions,omitempty"`
	Error          string     `json:"error,omitempty"`
	AllowPermanent bool       `json:"allowPermanent"`
	CredentialName string     `json:"credentialName,omitempty"`
	AffectedHosts  int        `json:"affectedHosts,omitempty"`
}

type Response struct {
	Accepted bool
	Answers  []string
}

type Broker struct {
	mu        sync.Mutex
	waiters   map[string]chan Response
	closed    chan struct{}
	closeOnce sync.Once
}

func New() *Broker {
	return &Broker{waiters: make(map[string]chan Response), closed: make(chan struct{})}
}

func (b *Broker) Ask(ctx context.Context, request Request, emit func(Request)) (Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := requestID()
	if err != nil {
		return Response{}, err
	}
	request.ID = id
	waiter := make(chan Response, 1)
	b.mu.Lock()
	select {
	case <-b.closed:
		b.mu.Unlock()
		return Response{}, ErrClosed
	default:
	}
	b.waiters[id] = waiter
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.waiters, id)
		b.mu.Unlock()
	}()
	if emit == nil {
		return Response{}, errors.New("SSH 交互事件发送器未配置")
	}
	emit(request)
	select {
	case response := <-waiter:
		return response, nil
	case <-ctx.Done():
		return Response{}, ctx.Err()
	case <-b.closed:
		return Response{}, ErrClosed
	}
}

func (b *Broker) Resolve(id string, response Response) bool {
	b.mu.Lock()
	waiter := b.waiters[id]
	b.mu.Unlock()
	if waiter == nil {
		return false
	}
	select {
	case waiter <- response:
		return true
	default:
		return false
	}
}

func (b *Broker) Close() {
	b.closeOnce.Do(func() { close(b.closed) })
}

func requestID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
