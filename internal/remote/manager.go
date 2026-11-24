package remote

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

type managedConnection struct {
	client        *ssh.Client
	closed        chan struct{}
	lastVerified  time.Time
	fingerprint   [32]byte
	connecting    bool
	connectCancel context.CancelFunc
	ready         chan struct{}
	readyOnce     sync.Once
	refs          int
	idleTimer     *time.Timer
}

func (c *managedConnection) isClosed() bool {
	if c == nil || c.closed == nil {
		return false
	}
	select {
	case <-c.closed:
		return true
	default:
		return false
	}
}

func (c *managedConnection) signalReady() {
	c.readyOnce.Do(func() { close(c.ready) })
}

// ConnectionManager 为每台主机复用一个 SSH 客户端，各功能通过独立 Channel 并行工作。
type ConnectionManager struct {
	mu      sync.Mutex
	entries map[string]*managedConnection
}

const (
	connectionIdleTimeout     = 30 * time.Second
	connectionVerifyInterval  = 12 * time.Second
	connectionVerifyTimeout   = 3 * time.Second
	managedConnectionAttempts = 3
)

func NewConnectionManager() *ConnectionManager {
	return &ConnectionManager{entries: make(map[string]*managedConnection)}
}

func (m *ConnectionManager) Acquire(id string, host Host) (*ssh.Client, func(), error) {
	return m.AcquireContext(context.Background(), id, host)
}

// AcquireContext 获取可复用的 SSH 客户端。取消只中止本次获取，不关闭其他调用方正在使用的客户端。
func (m *ConnectionManager) AcquireContext(ctx context.Context, id string, host Host) (*ssh.Client, func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if id == "" {
		return nil, nil, fmt.Errorf("主机 ID 不能为空")
	}
	fingerprint := connectionFingerprint(host)
	for {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		m.mu.Lock()
		entry := m.entries[id]
		if entry != nil && (entry.fingerprint != fingerprint || (entry.client != nil && entry.isClosed())) {
			delete(m.entries, id)
			if entry.idleTimer != nil {
				entry.idleTimer.Stop()
			}
			entry.signalReady()
			client := entry.client
			cancel := entry.connectCancel
			m.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			if client != nil {
				_ = client.Close()
			}
			continue
		}
		if entry != nil && entry.client != nil {
			if entry.idleTimer != nil {
				entry.idleTimer.Stop()
				entry.idleTimer = nil
			}
			check := entry.refs == 0 || entry.lastVerified.IsZero() || time.Since(entry.lastVerified) > connectionVerifyInterval
			entry.refs++
			client := entry.client
			m.mu.Unlock()
			release := m.releaseFunc(id, entry)
			if check {
				if err := sendKeepAliveContext(ctx, client, connectionVerifyTimeout); err != nil {
					release()
					if ctx.Err() != nil {
						return nil, nil, ctx.Err()
					}
					m.invalidateEntry(id, entry)
					continue
				}
				m.mu.Lock()
				if m.entries[id] == entry {
					entry.lastVerified = time.Now()
				}
				m.mu.Unlock()
			}
			return client, release, nil
		}
		if entry != nil && entry.connecting {
			ready := entry.ready
			m.mu.Unlock()
			select {
			case <-ready:
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			}
			continue
		}
		dialCtx, cancel := context.WithCancel(ctx)
		entry = &managedConnection{fingerprint: fingerprint, connecting: true, connectCancel: cancel, ready: make(chan struct{})}
		m.entries[id] = entry
		m.mu.Unlock()

		client, err := dialManagedConnection(dialCtx, host)
		cancel()
		m.mu.Lock()
		if m.entries[id] != entry {
			entry.signalReady()
			m.mu.Unlock()
			if client != nil {
				_ = client.Close()
			}
			return nil, nil, fmt.Errorf("主机连接配置已变化")
		}
		entry.connecting = false
		entry.connectCancel = nil
		if err != nil {
			delete(m.entries, id)
			entry.signalReady()
			m.mu.Unlock()
			return nil, nil, err
		}
		closed := make(chan struct{})
		entry.client = client
		entry.closed = closed
		entry.lastVerified = time.Now()
		entry.refs = 1
		entry.signalReady()
		m.mu.Unlock()
		go func(c *ssh.Client, e *managedConnection, done chan struct{}) {
			_ = c.Wait()
			close(done)
			m.evictClosed(id, e)
		}(client, entry, closed)
		return client, m.releaseFunc(id, entry), nil
	}
}

func dialManagedConnection(ctx context.Context, host Host) (*ssh.Client, error) {
	var lastErr error
	for attempt := 1; attempt <= managedConnectionAttempts; attempt++ {
		client, err := DialContext(ctx, host)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if attempt == managedConnectionAttempts || !IsRetryableConnectionError(err) {
			break
		}
		delay := managedConnectionRetryDelay(err, attempt)
		if host.ConnectionRetry != nil {
			host.ConnectionRetry(attempt+1, managedConnectionAttempts, delay, err)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, lastErr
}

func managedConnectionRetryDelay(err error, failedAttempt int) time.Duration {
	base := 500 * time.Millisecond
	if strings.Contains(strings.ToLower(err.Error()), "maxstartups") {
		base = 800 * time.Millisecond
	}
	return base * time.Duration(1<<max(0, failedAttempt-1))
}

func (m *ConnectionManager) releaseFunc(id string, entry *managedConnection) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			if m.entries[id] == entry && entry.refs > 0 {
				entry.refs--
				if entry.refs == 0 {
					if entry.idleTimer != nil {
						entry.idleTimer.Stop()
					}
					entry.idleTimer = time.AfterFunc(connectionIdleTimeout, func() { m.closeIdle(id, entry) })
				}
			}
			m.mu.Unlock()
		})
	}
}

func (m *ConnectionManager) closeIdle(id string, expected *managedConnection) {
	m.mu.Lock()
	if m.entries[id] != expected || expected.refs != 0 {
		m.mu.Unlock()
		return
	}
	delete(m.entries, id)
	expected.idleTimer = nil
	client := expected.client
	m.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
}

func (m *ConnectionManager) Invalidate(id string) {
	m.mu.Lock()
	entry := m.entries[id]
	delete(m.entries, id)
	if entry != nil {
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
		}
		entry.signalReady()
	}
	m.mu.Unlock()
	if entry != nil && entry.connectCancel != nil {
		entry.connectCancel()
	}
	if entry != nil && entry.client != nil {
		_ = entry.client.Close()
	}
}

func (m *ConnectionManager) evictClosed(id string, expected *managedConnection) {
	m.mu.Lock()
	if m.entries[id] == expected {
		delete(m.entries, id)
		if expected.idleTimer != nil {
			expected.idleTimer.Stop()
			expected.idleTimer = nil
		}
		expected.signalReady()
	}
	m.mu.Unlock()
}

func (m *ConnectionManager) invalidateEntry(id string, expected *managedConnection) {
	m.mu.Lock()
	if m.entries[id] != expected {
		m.mu.Unlock()
		return
	}
	delete(m.entries, id)
	if expected.idleTimer != nil {
		expected.idleTimer.Stop()
	}
	expected.signalReady()
	m.mu.Unlock()
	if expected.connectCancel != nil {
		expected.connectCancel()
	}
	if expected.client != nil {
		_ = expected.client.Close()
	}
}

func (m *ConnectionManager) CloseAll() {
	m.mu.Lock()
	entries := m.entries
	m.entries = make(map[string]*managedConnection)
	m.mu.Unlock()
	for _, entry := range entries {
		if entry.idleTimer != nil {
			entry.idleTimer.Stop()
		}
		entry.signalReady()
		if entry.connectCancel != nil {
			entry.connectCancel()
		}
		if entry.client != nil {
			_ = entry.client.Close()
		}
	}
}

func connectionFingerprint(host Host) [32]byte {
	value := fmt.Sprintf("%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s\x00%t\x00%s\x00%d\x00%s\x00%s\x00%s\x00%s\x00%s",
		host.Address, host.Port, host.Username, host.AuthType, host.Password, host.KeyPath, host.KeyContent,
		host.ProxyEnabled, host.ProxyHost, host.ProxyPort, host.ProxyUsername, host.ProxyPassword,
		host.KeyPassphrase, host.SSHJumpHostID, host.JumpFingerprint)
	return sha256.Sum256([]byte(value))
}
