package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"santianshell/internal/remote"
	"santianshell/internal/sessionlog"
)

func TestSSHSessionLogRemovesChunkedInputEcho(t *testing.T) {
	state := &sshSessionLogState{pendingEcho: []byte("secret command\r")}
	if output := state.removePendingEcho("secret "); output != "" {
		t.Fatalf("第一段仍含回显: %q", output)
	}
	if output := state.removePendingEcho("command\r\n执行完成\r\n"); output != "\n执行完成\r\n" {
		t.Fatalf("第二段清理结果错误: %q", output)
	}
	if len(state.pendingEcho) != 0 {
		t.Fatalf("仍残留待清理回显: %q", state.pendingEcho)
	}
}

func TestSSHSessionLogKeepsOutputWhenServerDoesNotEcho(t *testing.T) {
	state := &sshSessionLogState{pendingEcho: []byte("一次性验证码")}
	const output = "认证成功\r\n"
	if got := state.removePendingEcho(output); got != output {
		t.Fatalf("无回显输出被误删: %q", got)
	}
	if len(state.pendingEcho) != 0 {
		t.Fatal("服务器未回显时未清空待匹配输入")
	}
}

func TestSSHSessionLogWritesOutputWithoutUserInput(t *testing.T) {
	logger, err := sessionlog.New(t.TempDir(), sessionlog.SessionInfo{
		Name: "测试主机", Address: "192.0.2.20", Port: 22, Username: "root",
	})
	if err != nil {
		t.Fatal(err)
	}
	path := logger.Path()
	app := &App{sessionLogs: map[string]*sshSessionLogState{
		"host-1": {enabled: true, epoch: 7, logger: logger, path: path},
	}}
	const input = "不能写入日志的命令\r"
	app.rememberSSHSessionInput("host-1", 7, input)
	app.writeSSHSessionOutput("host-1", 7, input+"\n服务器输出\r\n")
	app.finishSSHSessionLog("host-1", 7)

	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(content)
	if strings.Contains(text, "不能写入日志的命令") {
		t.Fatalf("日志记录了用户输入: %q", text)
	}
	if !strings.Contains(text, "服务器输出\n") {
		t.Fatalf("缺少服务器输出: %q", text)
	}
}

func TestSSHSessionLogDefaultsDisabled(t *testing.T) {
	app := &App{unlocked: true}
	status, err := app.GetSSHSessionLogStatus("host-1")
	if err != nil {
		t.Fatal(err)
	}
	if status.Enabled || status.Active || status.Path != "" {
		t.Fatalf("默认状态不是关闭: %#v", status)
	}
	if !strings.HasSuffix(filepath.Clean(status.Directory), filepath.Join("logs", "ssh-sessions")) {
		t.Fatalf("日志目录不在应用日志体系内: %q", status.Directory)
	}
}

func TestEnableSSHSessionLogCannotOutliveClosingEpoch(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	const hostID = "host-1"
	const epoch = uint64(7)
	app := &App{
		unlocked:     true,
		settings:     Settings{Hosts: []remote.Host{{ID: hostID, Name: "测试主机", Address: "192.0.2.20", Port: 22, Username: "root"}}},
		shells:       map[string]*remote.Shell{hostID: {}},
		shellEpochs:  map[string]uint64{hostID: epoch},
		sessionLogs:  make(map[string]*sshSessionLogState),
		sshPrompts:   nil,
		queueRuns:    make(map[string]*queueRunState),
		portForwards: make(map[string]*appPortForward),
	}

	app.sessionLogMu.Lock()
	logLocked := true
	defer func() {
		if logLocked {
			app.sessionLogMu.Unlock()
		}
	}()
	setDone := make(chan error, 1)
	go func() {
		_, err := app.SetSSHSessionLogging(hostID, true)
		setDone <- err
	}()

	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for {
		if app.mu.TryLock() {
			app.mu.Unlock()
		} else {
			continuouslyHeld := true
			for range 1000 {
				runtime.Gosched()
				if app.mu.TryLock() {
					app.mu.Unlock()
					continuouslyHeld = false
					break
				}
			}
			if continuouslyHeld {
				break
			}
		}
		select {
		case <-deadline.C:
			t.Fatal("开启日志未在等待日志锁时保持 SSH 会话读锁")
		default:
			runtime.Gosched()
		}
	}

	closed := make(chan struct{})
	go func() {
		app.mu.Lock()
		app.shellEpochs[hostID]++
		delete(app.shells, hostID)
		app.mu.Unlock()
		app.finishSSHSessionLog(hostID, epoch)
		close(closed)
	}()
	app.sessionLogMu.Unlock()
	logLocked = false

	if err := <-setDone; err != nil {
		t.Fatal(err)
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("关闭 SSH 会话未完成")
	}
	app.sessionLogMu.Lock()
	state := app.sessionLogs[hostID]
	active := state != nil && state.logger != nil
	app.sessionLogMu.Unlock()
	if active {
		t.Fatal("已结束的 SSH epoch 仍残留打开的会话日志")
	}
}
