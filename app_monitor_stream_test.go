package main

import (
	"errors"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"santianshell/internal/remote"

	"golang.org/x/crypto/ssh"
)

func TestMonitorStreamCommandShellSyntax(t *testing.T) {
	shell := monitorTestShell(t)
	command := exec.Command(shell, "-n", "-c", monitorStreamCommand)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("持久化监控流脚本语法错误: %v\n%s", err, output)
	}
}

func dialTestRemoteShell(t *testing.T, server *localSSHServer) *remote.Shell {
	t.Helper()
	client, err := remote.Dial(remote.Host{
		Address:   server.address,
		Port:      server.port,
		Username:  "root",
		Password:  "secret",
		AuthType:  "password",
		HostKey:   server.fingerprint,
		Timeout:   2,
		KeepAlive: 3600,
	})
	if err != nil {
		t.Fatalf("Dial 测试服务器失败: %v", err)
	}
	shell, err := remote.StartShellWithClient(client, func() { _ = client.Close() }, func(string) {})
	if err != nil {
		_ = client.Close()
		t.Fatalf("StartShellWithClient 失败: %v", err)
	}
	t.Cleanup(func() { shell.Close() })
	return shell
}

func TestShellMonitorStreamDeliversFramesAndStopsCleanly(t *testing.T) {
	server := startLocalTestSSHServer(t, func(chans <-chan ssh.NewChannel) {
		var wg sync.WaitGroup
		defer wg.Wait()
		for newCh := range chans {
			if newCh.ChannelType() != "session" {
				_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			ch, reqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer ch.Close()
				for req := range reqs {
					switch req.Type {
					case "pty-req", "shell":
						_ = req.Reply(true, nil)
					case "exec":
						var payload struct{ Command string }
						_ = ssh.Unmarshal(req.Payload, &payload)
						_ = req.Reply(true, nil)
						if strings.Contains(payload.Command, "__FRAME_BEGIN__") {
							_, _ = ch.Write([]byte("__FRAME_BEGIN__\n__CPU__\n100 50\n__FRAME_END__\n"))
							time.Sleep(20 * time.Millisecond)
							_, _ = ch.Write([]byte("__FRAME_BEGIN__\n__CPU__\n200 100\n__FRAME_END__\n"))
						}
						// 等待客户端主动 StopMonitorStream 关闭通道。
						buf := make([]byte, 64)
						for {
							if _, readErr := ch.Read(buf); readErr != nil {
								return
							}
						}
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()
		}
	})

	shell := dialTestRemoteShell(t, server)
	frames := make(chan string, 4)
	exits := make(chan error, 1)
	if err := shell.StartMonitorStream(
		monitorStreamCommand,
		func(frame string) { frames <- frame },
		func(err error) { exits <- err },
	); err != nil {
		t.Fatalf("StartMonitorStream 失败: %v", err)
	}

	var got []string
	deadline := time.After(2 * time.Second)
	for len(got) < 2 {
		select {
		case frame := <-frames:
			got = append(got, frame)
		case <-deadline:
			t.Fatalf("等待流帧超时，已收到 %d 帧: %v", len(got), got)
		}
	}
	if !strings.Contains(got[0], "100 50") || !strings.Contains(got[1], "200 100") {
		t.Fatalf("流帧内容异常: %v", got)
	}

	shell.StopMonitorStream()
	select {
	case exitErr := <-exits:
		if exitErr != nil {
			t.Fatalf("主动停止监控流应正常退出，实际错误: %v", exitErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("等待监控流停止回调超时")
	}
}

func TestRunMonitorTimeoutRunsConcurrentlyWithinSemaphore(t *testing.T) {
	slowStarted := make(chan struct{})
	releaseSlow := make(chan struct{})
	var slowOnce, releaseOnce sync.Once

	server := startLocalTestSSHServer(t, func(chans <-chan ssh.NewChannel) {
		var wg sync.WaitGroup
		defer wg.Wait()
		for newCh := range chans {
			if newCh.ChannelType() != "session" {
				_ = newCh.Reject(ssh.UnknownChannelType, "unsupported")
				continue
			}
			ch, reqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer ch.Close()
				for req := range reqs {
					switch req.Type {
					case "pty-req", "shell":
						_ = req.Reply(true, nil)
					case "exec":
						var payload struct{ Command string }
						_ = ssh.Unmarshal(req.Payload, &payload)
						_ = req.Reply(true, nil)
						cmd := strings.TrimSpace(payload.Command)
						go func() {
							if cmd == "slow" {
								slowOnce.Do(func() { close(slowStarted) })
								select {
								case <-releaseSlow:
								case <-time.After(2 * time.Second):
								}
								_, _ = ch.Write([]byte("slow-ok"))
							} else {
								_, _ = ch.Write([]byte("fast-ok"))
								releaseOnce.Do(func() { close(releaseSlow) })
							}
							_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
							_ = ch.CloseWrite()
							_ = ch.Close()
						}()
					default:
						_ = req.Reply(false, nil)
					}
				}
			}()
		}
	})

	shell := dialTestRemoteShell(t, server)
	slowErrCh := make(chan error, 1)
	go func() {
		res, err := shell.RunMonitorTimeout("slow", 2*time.Second)
		if err == nil && res.Output != "slow-ok" {
			err = errors.New("unexpected slow output: " + res.Output)
		}
		slowErrCh <- err
	}()

	select {
	case <-slowStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("慢监控命令未及时启动")
	}

	fastRes, fastErr := shell.RunMonitorTimeout("fast", 2*time.Second)
	if fastErr != nil || fastRes.Output != "fast-ok" {
		t.Fatalf("快监控命令并发执行失败: res=%+v err=%v", fastRes, fastErr)
	}
	if slowErr := <-slowErrCh; slowErr != nil {
		t.Fatalf("慢监控命令执行失败: %v", slowErr)
	}
}
