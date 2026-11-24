//go:build windows

package sshagent

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const openSSHPipe = `\\.\pipe\openssh-ssh-agent`

var (
	user32         = windows.NewLazySystemDLL("user32.dll")
	findWindowProc = user32.NewProc("FindWindowW")
)

func platformCandidates() []dialCandidate {
	candidates := make([]dialCandidate, 0, 3)
	if address := strings.TrimSpace(os.Getenv("SSH_AUTH_SOCK")); address != "" {
		candidates = append(candidates, candidateForAddress(SourceEnvironment, address))
	}
	if len(candidates) == 0 || !strings.EqualFold(normalizePipePath(candidates[0].address), openSSHPipe) {
		candidates = append(candidates, candidateForAddress(SourceOpenSSH, openSSHPipe))
	}
	if detectPageant().Available {
		candidates = append(candidates, dialCandidate{
			source:  SourcePageant,
			address: "Pageant",
			dial: func(ctx context.Context) (agentConnection, error) {
				return newPageantConnection(ctx), nil
			},
		})
	}
	return candidates
}

func candidateForAddress(source Source, address string) dialCandidate {
	normalized := normalizePipePath(address)
	if isLocalNamedPipe(normalized) {
		return dialCandidate{
			source:  source,
			address: normalized,
			dial: func(ctx context.Context) (agentConnection, error) {
				return dialNamedPipe(ctx, normalized)
			},
		}
	}
	return dialCandidate{
		source:  source,
		address: address,
		dial: func(ctx context.Context) (agentConnection, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", address)
		},
	}
}

func normalizePipePath(address string) string {
	if strings.HasPrefix(strings.ToLower(address), `//./pipe/`) {
		return strings.ReplaceAll(address, "/", `\`)
	}
	return address
}

func isLocalNamedPipe(address string) bool {
	return strings.HasPrefix(strings.ToLower(address), `\\.\pipe\`)
}

func dialNamedPipe(ctx context.Context, path string) (agentConnection, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, fmt.Errorf("命名管道地址无效: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		handle, openErr := windows.CreateFile(
			name,
			windows.GENERIC_READ|windows.GENERIC_WRITE,
			0,
			nil,
			windows.OPEN_EXISTING,
			windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OVERLAPPED,
			0,
		)
		if openErr == nil {
			mode := uint32(windows.PIPE_READMODE_BYTE)
			if err := windows.SetNamedPipeHandleState(handle, &mode, nil, nil); err != nil {
				_ = windows.CloseHandle(handle)
				return nil, fmt.Errorf("设置 SSH Agent 管道模式失败: %w", err)
			}
			file := os.NewFile(uintptr(handle), path)
			if file == nil {
				_ = windows.CloseHandle(handle)
				return nil, errors.New("创建 SSH Agent 管道句柄失败")
			}
			return file, nil
		}
		if !errors.Is(openErr, windows.ERROR_PIPE_BUSY) {
			return nil, openErr
		}

		timer := time.NewTimer(25 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func detectPageant() PageantStatus {
	className, err := windows.UTF16PtrFromString("Pageant")
	if err != nil {
		return PageantStatus{Reason: "Pageant 检测参数无效"}
	}
	window, _, _ := findWindowProc.Call(uintptr(unsafe.Pointer(className)), 0)
	if window != 0 {
		return PageantStatus{
			Available: true,
			Supported: true,
			Reason:    "已检测到 Pageant，可直接使用其中的密钥",
		}
	}
	// FindWindowW 在未找到窗口时不设置 LastError，不能据此判断检测失败。
	return PageantStatus{Reason: "未检测到 Pageant"}
}
