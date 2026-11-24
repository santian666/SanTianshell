package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"santianshell/internal/remote"

	"golang.org/x/crypto/ssh"
)

func TestClassifyTerminalExit(t *testing.T) {
	if msg, inv, retry := classifyTerminalExit(nil); inv || retry || msg != "SSH 会话已结束" {
		t.Fatalf("nil 退出分类异常: msg=%q inv=%v retry=%v", msg, inv, retry)
	}
	if msg, inv, retry := classifyTerminalExit(&ssh.ExitError{}); inv || retry || msg != "SSH 会话已结束" {
		t.Fatalf("ExitError 分类异常: msg=%q inv=%v retry=%v", msg, inv, retry)
	}
	msg, inv, retry := classifyTerminalExit(io.EOF)
	if !inv || !retry || !strings.Contains(msg, "中断") {
		t.Fatalf("io.EOF 应视为异常断线并触发失效与重连: msg=%q inv=%v retry=%v", msg, inv, retry)
	}
	netErr := &net.OpError{Op: "read", Net: "tcp", Err: io.ErrUnexpectedEOF}
	msg, inv, retry = classifyTerminalExit(netErr)
	if !inv || !retry || !strings.Contains(msg, "中断") {
		t.Fatalf("网络断线分类异常: msg=%q inv=%v retry=%v", msg, inv, retry)
	}
}

func TestNormalizeAndValidateSSHJumpChain(t *testing.T) {
	sshProxy := remote.Host{ID: "h1", Protocol: "ssh", ProxyEnabled: true, SSHJumpHostID: "j1"}
	if err := normalizeSSHHostOptions(&sshProxy); err == nil || !strings.Contains(err.Error(), "互斥") && !strings.Contains(err.Error(), "同时启用") {
		t.Fatalf("应拒绝同时启用 SSH 跳板与代理，实际: %v", err)
	}
	selfJump := remote.Host{ID: "h1", Protocol: "ssh", SSHJumpHostID: "h1"}
	if err := normalizeSSHHostOptions(&selfJump); err == nil || !strings.Contains(err.Error(), "自身") {
		t.Fatalf("应拒绝将自身设为 SSH 跳板，实际: %v", err)
	}
	rdpHost := remote.Host{ID: "r1", Protocol: "rdp", SSHJumpHostID: "j1"}
	if err := normalizeSSHHostOptions(&rdpHost); err != nil || rdpHost.SSHJumpHostID != "" {
		t.Fatalf("RDP 主机应忽略并清空 SSHJumpHostID: err=%v jump=%q", err, rdpHost.SSHJumpHostID)
	}

	app := &App{}
	hosts := []remote.Host{
		{ID: "j1", Name: "Jump1", Protocol: "ssh"},
		{ID: "j2", Name: "Jump2", Protocol: "ssh", SSHJumpHostID: "j1"},
		{ID: "j3", Name: "Jump3", Protocol: "ssh", SSHJumpHostID: "j2"},
		{ID: "j4", Name: "Jump4", Protocol: "ssh", SSHJumpHostID: "j3"},
		{ID: "j5", Name: "Jump5", Protocol: "ssh", SSHJumpHostID: "j4"},
		{ID: "del", Name: "Deleted", Protocol: "ssh", Deleted: true},
		{ID: "rdp", Name: "RDP", Protocol: "rdp"},
		{ID: "cycA", Name: "CycA", Protocol: "ssh", SSHJumpHostID: "cycB"},
		{ID: "cycB", Name: "CycB", Protocol: "ssh", SSHJumpHostID: "cycA"},
	}
	if err := app.validateSSHJumpChainLocked(hosts, remote.Host{ID: "t4", Protocol: "ssh", SSHJumpHostID: "j4"}); err != nil {
		t.Fatalf("4 级合法跳板链校验失败: %v", err)
	}
	if err := app.validateSSHJumpChainLocked(hosts, remote.Host{ID: "t5", Protocol: "ssh", SSHJumpHostID: "j5"}); err == nil || !strings.Contains(err.Error(), "超过 4 级") {
		t.Fatalf("超过 4 级跳板应拒绝，实际: %v", err)
	}
	if err := app.validateSSHJumpChainLocked(hosts, remote.Host{ID: "cycA", Protocol: "ssh", SSHJumpHostID: "cycB"}); err == nil || !strings.Contains(err.Error(), "循环") {
		t.Fatalf("跳板循环应拒绝，实际: %v", err)
	}
	if err := app.validateSSHJumpChainLocked(hosts, remote.Host{ID: "tdel", Protocol: "ssh", SSHJumpHostID: "del"}); err == nil {
		t.Fatalf("已删除跳板应拒绝")
	}
	if err := app.validateSSHJumpChainLocked(hosts, remote.Host{ID: "trdp", Protocol: "ssh", SSHJumpHostID: "rdp"}); err == nil || !strings.Contains(err.Error(), "SSH 协议") {
		t.Fatalf("非 SSH 跳板应拒绝，实际: %v", err)
	}
}

type localSSHServer struct {
	address     string
	port        int
	fingerprint string
}

func startLocalTestSSHServer(t *testing.T, handler func(<-chan ssh.NewChannel)) *localSSHServer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(_ ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if string(pass) != "secret" {
				return nil, errors.New("bad password")
			}
			return nil, nil
		},
	}
	cfg.AddHostKey(signer)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			wg.Add(1)
			go func(c net.Conn) {
				defer wg.Done()
				defer c.Close()
				sc, chans, reqs, err := ssh.NewServerConn(c, cfg)
				if err != nil {
					return
				}
				defer sc.Close()
				go ssh.DiscardRequests(reqs)
				handler(chans)
			}(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		wg.Wait()
	})
	addr := ln.Addr().(*net.TCPAddr)
	return &localSSHServer{
		address:     addr.IP.String(),
		port:        addr.Port,
		fingerprint: ssh.FingerprintSHA256(signer.PublicKey()),
	}
}

func TestSSHJumpChainTwoHopDialAndRelease(t *testing.T) {
	targetServer := startLocalTestSSHServer(t, func(chans <-chan ssh.NewChannel) {
		for nc := range chans {
			if nc.ChannelType() != "session" {
				_ = nc.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			ch, reqs, err := nc.Accept()
			if err != nil {
				continue
			}
			go func() {
				defer ch.Close()
				for req := range reqs {
					if req.Type == "exec" {
						_ = req.Reply(true, nil)
						_, _ = io.WriteString(ch, "jump-ok\n")
						_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
						return
					}
					_ = req.Reply(false, nil)
				}
			}()
		}
	})

	jumpServer := startLocalTestSSHServer(t, func(chans <-chan ssh.NewChannel) {
		for nc := range chans {
			if nc.ChannelType() != "direct-tcpip" {
				_ = nc.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			var payload struct {
				DestAddr string
				DestPort uint32
				OrigAddr string
				OrigPort uint32
			}
			if err := ssh.Unmarshal(nc.ExtraData(), &payload); err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			upstream, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", payload.DestAddr, payload.DestPort), 2*time.Second)
			if err != nil {
				_ = nc.Reject(ssh.ConnectionFailed, err.Error())
				continue
			}
			ch, reqs, err := nc.Accept()
			if err != nil {
				_ = upstream.Close()
				continue
			}
			go ssh.DiscardRequests(reqs)
			go func() {
				defer upstream.Close()
				defer ch.Close()
				_, _ = io.Copy(upstream, ch)
			}()
			go func() {
				defer upstream.Close()
				defer ch.Close()
				_, _ = io.Copy(ch, upstream)
			}()
		}
	})

	jumpHost := remote.Host{
		ID: "jump-1", Name: "Bastion", Protocol: "ssh",
		Address: jumpServer.address, Port: jumpServer.port,
		Username: "root", Password: "secret", AuthType: "password",
		HostKey: jumpServer.fingerprint, Timeout: 5, KeepAlive: 30,
	}
	targetHost := remote.Host{
		ID: "target-1", Name: "Internal", Protocol: "ssh",
		Address: targetServer.address, Port: targetServer.port,
		Username: "root", Password: "secret", AuthType: "password",
		HostKey: targetServer.fingerprint, SSHJumpHostID: jumpHost.ID,
		Timeout: 5, KeepAlive: 30,
	}

	app := &App{
		unlocked:    true,
		connections: remote.NewConnectionManager(),
		settings: Settings{
			Hosts: []remote.Host{jumpHost, targetHost},
		},
	}
	t.Cleanup(app.connections.CloseAll)

	resolved, err := app.host(targetHost.ID)
	if err != nil {
		t.Fatalf("获取目标主机配置失败: %v", err)
	}
	client, release, err := app.connections.AcquireContext(context.Background(), targetHost.ID, resolved)
	if err != nil {
		t.Fatalf("经由 SSH 跳板机连接目标主机失败: %v", err)
	}

	result, err := remote.RunWithClient(client, "echo jump-ok")
	if err != nil || !strings.Contains(result.Output, "jump-ok") {
		release()
		t.Fatalf("经跳板执行命令失败: err=%v output=%q", err, result.Output)
	}

	_ = client.Close()
	release()
}
