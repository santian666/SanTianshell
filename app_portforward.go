package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"santianshell/internal/portforward"
)

// PortForwardRequest 是本地、远程和动态 SSH 转发的统一输入。
type PortForwardRequest struct {
	HostID                  string `json:"hostId"`
	Kind                    string `json:"kind"`
	ListenHost              string `json:"listenHost"`
	ListenPort              int    `json:"listenPort"`
	TargetHost              string `json:"targetHost"`
	TargetPort              int    `json:"targetPort"`
	AllowExternal           bool   `json:"allowExternal"`
	ConnectTimeoutSeconds   int    `json:"connectTimeoutSeconds"`
	HandshakeTimeoutSeconds int    `json:"handshakeTimeoutSeconds"`
}

// PortForwardInfo 是供界面展示的并发安全快照。
type PortForwardInfo struct {
	ID            string `json:"id"`
	HostID        string `json:"hostId"`
	Kind          string `json:"kind"`
	State         string `json:"state"`
	ListenAddress string `json:"listenAddress"`
	TargetAddress string `json:"targetAddress,omitempty"`
	LastError     string `json:"lastError,omitempty"`
	CreatedAt     string `json:"createdAt"`
}

type appPortForward struct {
	hostID    string
	forward   *portforward.Forward
	lease     *sshForwardLease
	createdAt time.Time
}

type sshForwardLease struct {
	forward     *portforward.Forward
	release     func()
	releaseOnce sync.Once
}

func newSSHForwardLease(forward *portforward.Forward, release func()) *sshForwardLease {
	lease := &sshForwardLease{forward: forward, release: release}
	if forward != nil {
		go func() {
			<-forward.Done()
			lease.releaseConnection()
		}()
	}
	return lease
}

func (l *sshForwardLease) Close() error {
	if l == nil || l.forward == nil {
		return nil
	}
	err := l.forward.Close()
	<-l.forward.Done()
	l.releaseConnection()
	return err
}

func (l *sshForwardLease) Errors() <-chan error {
	if l == nil || l.forward == nil {
		closed := make(chan error)
		close(closed)
		return closed
	}
	return l.forward.Errors()
}

func (l *sshForwardLease) releaseConnection() {
	if l == nil {
		return
	}
	l.releaseOnce.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

// StartPortForward 启动一条主机级 SSH 端口转发，并持有复用连接直到转发关闭。
func (a *App) StartPortForward(request PortForwardRequest) (PortForwardInfo, error) {
	if err := a.requireUnlocked(); err != nil {
		return PortForwardInfo{}, err
	}
	kind, options, err := normalizePortForwardRequest(request)
	if err != nil {
		return PortForwardInfo{}, err
	}
	host, err := a.host(strings.TrimSpace(request.HostID))
	if err != nil {
		return PortForwardInfo{}, err
	}
	if host.Deleted {
		return PortForwardInfo{}, errors.New("主机已删除，不能启动端口转发")
	}
	if !isSSHHostProtocol(host.Protocol) {
		return PortForwardInfo{}, errors.New("只有 SSH 主机支持端口转发")
	}
	client, release, err := a.acquireHostSSH(host.ID)
	if err != nil {
		return PortForwardInfo{}, fmt.Errorf("建立端口转发 SSH 连接失败: %w", err)
	}

	var forward *portforward.Forward
	switch kind {
	case portforward.KindLocal:
		forward, err = portforward.StartLocal(client, options)
	case portforward.KindRemote:
		forward, err = portforward.StartRemote(client, options)
	case portforward.KindDynamic:
		forward, err = portforward.StartDynamic(client, options)
	}
	if err != nil {
		release()
		return PortForwardInfo{}, err
	}
	entry := &appPortForward{
		hostID:    host.ID,
		forward:   forward,
		lease:     newSSHForwardLease(forward, release),
		createdAt: time.Now(),
	}
	if err := a.trackPortForward(entry); err != nil {
		_ = entry.lease.Close()
		return PortForwardInfo{}, err
	}
	return portForwardInfo(entry), nil
}

// ListPortForwards 返回当前仍在运行或清理中的端口转发。
func (a *App) ListPortForwards() ([]PortForwardInfo, error) {
	if err := a.requireUnlocked(); err != nil {
		return nil, err
	}
	a.portForwardMu.RLock()
	entries := make([]*appPortForward, 0, len(a.portForwards))
	for _, entry := range a.portForwards {
		entries = append(entries, entry)
	}
	a.portForwardMu.RUnlock()
	sort.Slice(entries, func(i, j int) bool { return entries[i].createdAt.Before(entries[j].createdAt) })
	result := make([]PortForwardInfo, 0, len(entries))
	for _, entry := range entries {
		result = append(result, portForwardInfo(entry))
	}
	return result, nil
}

// StopPortForward 停止指定转发；重复停止同一 ID 不会报错。
func (a *App) StopPortForward(id string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("端口转发 ID 不能为空")
	}
	a.portForwardMu.RLock()
	entry := a.portForwards[id]
	a.portForwardMu.RUnlock()
	if entry == nil {
		return nil
	}
	err := entry.lease.Close()
	a.removeTrackedPortForward(entry)
	return err
}

func normalizePortForwardRequest(request PortForwardRequest) (portforward.Kind, portforward.Options, error) {
	request.HostID = strings.TrimSpace(request.HostID)
	if request.HostID == "" {
		return "", portforward.Options{}, errors.New("SSH 主机 ID 不能为空")
	}
	kind := portforward.Kind(strings.ToLower(strings.TrimSpace(request.Kind)))
	if request.ConnectTimeoutSeconds < 0 || request.ConnectTimeoutSeconds > 3600 {
		return "", portforward.Options{}, errors.New("连接超时必须在 0 到 3600 秒之间")
	}
	if request.HandshakeTimeoutSeconds < 0 || request.HandshakeTimeoutSeconds > 300 {
		return "", portforward.Options{}, errors.New("SOCKS5 握手超时必须在 0 到 300 秒之间")
	}
	options := portforward.Options{
		ListenHost:       request.ListenHost,
		ListenPort:       request.ListenPort,
		TargetHost:       request.TargetHost,
		TargetPort:       request.TargetPort,
		AllowExternal:    request.AllowExternal,
		ConnectTimeout:   time.Duration(request.ConnectTimeoutSeconds) * time.Second,
		HandshakeTimeout: time.Duration(request.HandshakeTimeoutSeconds) * time.Second,
	}
	if err := portforward.Validate(kind, options); err != nil {
		return "", portforward.Options{}, err
	}
	return kind, options, nil
}

func (a *App) trackPortForward(entry *appPortForward) error {
	if entry == nil || entry.forward == nil || entry.lease == nil {
		return errors.New("端口转发实例无效")
	}
	a.mu.RLock()
	if a.shuttingDown.Load() {
		a.mu.RUnlock()
		return errors.New("软件正在退出")
	}
	active := false
	for _, host := range a.settings.Hosts {
		if host.ID == entry.hostID && !host.Deleted && isSSHHostProtocol(host.Protocol) {
			active = true
			break
		}
	}
	if !active {
		if host, exists := a.temporaryHosts[entry.hostID]; exists && isSSHHostProtocol(host.Protocol) {
			active = true
		}
	}
	if active {
		a.portForwardMu.Lock()
		if a.portForwards == nil {
			a.portForwards = make(map[string]*appPortForward)
		}
		if a.portForwards[entry.forward.ID] != nil {
			active = false
		} else {
			a.portForwards[entry.forward.ID] = entry
		}
		a.portForwardMu.Unlock()
	}
	a.mu.RUnlock()
	if !active {
		return errors.New("SSH 主机已删除或端口转发 ID 冲突")
	}
	go func() {
		<-entry.forward.Done()
		entry.lease.releaseConnection()
		a.removeTrackedPortForward(entry)
	}()
	return nil
}

func (a *App) removeTrackedPortForward(entry *appPortForward) {
	if entry == nil || entry.forward == nil {
		return
	}
	a.portForwardMu.Lock()
	if a.portForwards[entry.forward.ID] == entry {
		delete(a.portForwards, entry.forward.ID)
	}
	a.portForwardMu.Unlock()
}

func portForwardInfo(entry *appPortForward) PortForwardInfo {
	snapshot := entry.forward.Snapshot()
	return PortForwardInfo{
		ID:            snapshot.ID,
		HostID:        entry.hostID,
		Kind:          string(snapshot.Kind),
		State:         string(snapshot.State),
		ListenAddress: snapshot.ListenAddress,
		TargetAddress: snapshot.TargetAddress,
		LastError:     snapshot.LastError,
		CreatedAt:     entry.createdAt.Format(time.RFC3339),
	}
}

func (a *App) stopPortForwardsForHost(hostID string) {
	a.portForwardMu.RLock()
	entries := make([]*appPortForward, 0)
	for _, entry := range a.portForwards {
		if entry.hostID == hostID {
			entries = append(entries, entry)
		}
	}
	a.portForwardMu.RUnlock()
	for _, entry := range entries {
		_ = entry.lease.Close()
		a.removeTrackedPortForward(entry)
	}
}

func (a *App) closeAllPortForwards() {
	a.portForwardMu.RLock()
	entries := make([]*appPortForward, 0, len(a.portForwards))
	for _, entry := range a.portForwards {
		entries = append(entries, entry)
	}
	a.portForwardMu.RUnlock()
	for _, entry := range entries {
		_ = entry.lease.Close()
		a.removeTrackedPortForward(entry)
	}
}

func (a *App) startRDPJumpForward(jumpHostID, target string, timeout time.Duration) (string, io.Closer, error) {
	jumpHostID = strings.TrimSpace(jumpHostID)
	if jumpHostID == "" {
		return "", nil, errors.New("RDP SSH 跳板主机 ID 不能为空")
	}
	targetHost, targetPortText, err := net.SplitHostPort(target)
	if err != nil {
		return "", nil, fmt.Errorf("RDP 跳板目标地址无效: %w", err)
	}
	targetPort, err := strconv.Atoi(targetPortText)
	if err != nil || targetPort < 1 || targetPort > 65535 {
		return "", nil, errors.New("RDP 跳板目标端口必须在 1 到 65535 之间")
	}
	jumpHost, err := a.host(jumpHostID)
	if err != nil {
		return "", nil, fmt.Errorf("RDP SSH 跳板主机不可用: %w", err)
	}
	if jumpHost.Deleted || !isSSHHostProtocol(jumpHost.Protocol) {
		return "", nil, errors.New("RDP 跳板必须是未删除的 SSH 主机")
	}
	client, release, err := a.acquireHostSSH(jumpHostID)
	if err != nil {
		return "", nil, fmt.Errorf("连接 RDP SSH 跳板失败: %w", err)
	}
	forward, err := portforward.StartLocal(client, portforward.Options{
		ListenHost:     "127.0.0.1",
		TargetHost:     targetHost,
		TargetPort:     targetPort,
		ConnectTimeout: timeout,
	})
	if err != nil {
		release()
		return "", nil, fmt.Errorf("创建 RDP SSH 跳板转发失败: %w", err)
	}
	return forward.Address(), newSSHForwardLease(forward, release), nil
}

func isSSHHostProtocol(value string) bool {
	value = strings.TrimSpace(value)
	return value == "" || strings.EqualFold(value, "ssh")
}

func probeRDPAddress(ctx context.Context, address string) error {
	if ctx == nil {
		return errors.New("RDP 跳板预检 context 不能为空")
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return fmt.Errorf("RDP 跳板预检连接失败: %w", err)
	}
	defer conn.Close()
	deadline := time.Now().Add(10 * time.Second)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return fmt.Errorf("RDP 跳板预检设置超时失败: %w", err)
	}
	stopCancel := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stopCancel()

	request := []byte{0x03, 0x00, 0x00, 0x13, 0x0e, 0xe0, 0x00, 0x00, 0x00, 0x00, 0x00, 0x01, 0x00, 0x08, 0x00, 0x03, 0x00, 0x00, 0x00}
	if _, err := conn.Write(request); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("RDP 跳板预检写入协商请求失败: %w", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("RDP 跳板预检读取协商响应失败: %w", err)
	}
	length := int(binary.BigEndian.Uint16(header[2:]))
	if header[0] != 0x03 || header[1] != 0x00 || length < 11 {
		return errors.New("RDP 跳板预检收到无效的 RDP 协商响应")
	}
	response := make([]byte, length-4)
	if _, err := io.ReadFull(conn, response); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("RDP 跳板预检读取完整响应失败: %w", err)
	}
	return nil
}
