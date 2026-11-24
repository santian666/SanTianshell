package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"santianshell/internal/remote"
)

func TestPersistentSessionUsesExistingToolWithoutInstall(t *testing.T) {
	for _, expected := range []string{"tmux", "screen"} {
		t.Run(expected, func(t *testing.T) {
			calls := 0
			tool, err := detectOrInstallPersistentSessionTool(func(command string, timeout time.Duration) (remote.Result, error) {
				calls++
				if !strings.Contains(command, "command -v tmux") || timeout != 5*time.Second {
					t.Fatalf("检测命令或超时不正确: %q, %s", command, timeout)
				}
				return remote.Result{Output: expected, ExitCode: 0}, nil
			})
			if err != nil || tool != expected || calls != 1 {
				t.Fatalf("已有 %s 时仍触发安装: tool=%q calls=%d err=%v", expected, tool, calls, err)
			}
		})
	}
}

func TestPersistentSessionInstallsMissingTool(t *testing.T) {
	commands := make([]string, 0, 2)
	tool, err := detectOrInstallPersistentSessionTool(func(command string, timeout time.Duration) (remote.Result, error) {
		commands = append(commands, command)
		if len(commands) == 1 {
			return remote.Result{ExitCode: 0}, nil
		}
		if timeout != 3*time.Minute {
			t.Fatalf("安装超时 = %s", timeout)
		}
		return remote.Result{Output: "安装完成\n" + persistentSessionToolMarker + "tmux\n", ExitCode: 0}, nil
	})
	if err != nil || tool != "tmux" || len(commands) != 2 {
		t.Fatalf("自动安装结果错误: tool=%q calls=%d err=%v", tool, len(commands), err)
	}
	for _, required := range []string{"apt-get install -y --no-install-recommends tmux", "dnf install -y tmux", "yum install -y tmux", "apk add --no-cache tmux", "sudo -n true"} {
		if !strings.Contains(commands[1], required) {
			t.Fatalf("安装命令缺少 %q", required)
		}
	}
}

func TestPersistentSessionInstallReturnsRealFailure(t *testing.T) {
	calls := 0
	_, err := detectOrInstallPersistentSessionTool(func(string, time.Duration) (remote.Result, error) {
		calls++
		if calls == 1 {
			return remote.Result{ExitCode: 0}, nil
		}
		return remote.Result{Output: "当前账号不是 root，也不能免密 sudo", ExitCode: 42}, errors.New("exit status 42")
	})
	if err == nil || !strings.Contains(err.Error(), "不能免密 sudo") {
		t.Fatalf("安装失败未返回真实原因: %v", err)
	}
}

func TestParsePersistentSessionToolRejectsUnknownMarker(t *testing.T) {
	if got := parsePersistentSessionTool("log\n" + persistentSessionToolMarker + "unknown"); got != "" {
		t.Fatalf("错误接受未知工具: %q", got)
	}
}
