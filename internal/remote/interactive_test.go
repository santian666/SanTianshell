package remote

import (
	"bytes"
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"
)

type testWriteCloser struct {
	bytes.Buffer
}

func (*testWriteCloser) Close() error { return nil }

func TestInteractiveCommandCaptureSplitMarkersAndNaturalEcho(t *testing.T) {
	command := "printf '你好\\n'"
	capture, err := newInteractiveCommandCapture(command, nil)
	if err != nil {
		t.Fatal(err)
	}

	prompt := "root@test:~# "
	commandOutput := "你好\r\n"
	wire := prompt + capture.wrapper + "\r\n" +
		capture.startMarker + commandOutput +
		capture.endPrefix + "0" + capture.endSuffix + "\r\n" + prompt

	var terminal strings.Builder
	var streamed strings.Builder
	var completed *interactiveCommandResult
	// 逐字节输入，覆盖包装命令、起始标记、结束标记和 UTF-8 字符跨分块。
	for index := 0; index < len(wire); index++ {
		terminalPart, streamPart, result := capture.consume(wire[index : index+1])
		terminal.WriteString(terminalPart)
		streamed.WriteString(streamPart)
		if result != nil {
			if completed != nil {
				t.Fatal("交互命令被重复标记为完成")
			}
			completed = result
		}
	}

	if completed == nil {
		t.Fatal("跨分块结束标记未被识别")
	}
	if completed.err != nil || completed.exitCode != 0 {
		t.Fatalf("命令结果异常：exitCode=%d err=%v", completed.exitCode, completed.err)
	}
	if completed.output != commandOutput || streamed.String() != commandOutput {
		t.Fatalf("命令输出不一致：result=%q stream=%q", completed.output, streamed.String())
	}
	wantTerminal := prompt + command + "\r\n" + commandOutput + "\r\n" + prompt
	if terminal.String() != wantTerminal {
		t.Fatalf("终端自然回显不一致：\n实际 %q\n期望 %q", terminal.String(), wantTerminal)
	}
	for _, value := range []string{terminal.String(), streamed.String(), completed.output} {
		if strings.Contains(value, "SANTIANSHELL_") || strings.Contains(value, capture.wrapper) {
			t.Fatalf("协议标记或包装命令泄漏到可见输出：%q", value)
		}
	}
}

func TestInteractiveCommandCaptureReturnsNonZeroExitCode(t *testing.T) {
	capture, err := newInteractiveCommandCapture("false", nil)
	if err != nil {
		t.Fatal(err)
	}

	terminal, streamed, completed := capture.consume(
		capture.startMarker + "执行失败\r\n" + capture.endPrefix + strconv.Itoa(17) + capture.endSuffix,
	)
	if completed == nil {
		t.Fatal("未识别命令结束标记")
	}
	if completed.err != nil || completed.exitCode != 17 {
		t.Fatalf("非零退出码解析异常：exitCode=%d err=%v", completed.exitCode, completed.err)
	}
	if completed.output != "执行失败\r\n" || streamed != "执行失败\r\n" {
		t.Fatalf("非零命令输出异常：result=%q stream=%q", completed.output, streamed)
	}
	if strings.Contains(terminal, "SANTIANSHELL_") {
		t.Fatalf("协议标记泄漏到终端：%q", terminal)
	}
}

func TestInteractiveCommandCaptureSupportsMultilineCommand(t *testing.T) {
	command := "printf '第一行\\n'\nprintf '第二行\\n'"
	capture, err := newInteractiveCommandCapture(command, nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(capture.wrapper, "\r\n") {
		t.Fatalf("包装命令必须保持单行，实际为 %q", capture.wrapper)
	}

	terminal, streamed, completed := capture.consume(
		capture.wrapper + "\r\n" + capture.startMarker + "第一行\r\n第二行\r\n" + capture.endPrefix + "0" + capture.endSuffix,
	)
	if completed == nil || completed.err != nil || completed.exitCode != 0 {
		t.Fatalf("多行命令结果异常：%+v", completed)
	}
	if !strings.HasPrefix(terminal, command+"\r\n") {
		t.Fatalf("终端没有显示原始多行命令：%q", terminal)
	}
	if streamed != "第一行\r\n第二行\r\n" {
		t.Fatalf("多行命令输出异常：%q", streamed)
	}
}

func TestRunInteractiveContextRejectsBusyShell(t *testing.T) {
	t.Run("终端不在提示符", func(t *testing.T) {
		shell := &Shell{}
		_, err := shell.RunInteractiveContext(context.Background(), "true", nil)
		if !errors.Is(err, ErrInteractiveShellBusy) {
			t.Fatalf("错误 = %v，期望 ErrInteractiveShellBusy", err)
		}
	})

	t.Run("已有交互命令", func(t *testing.T) {
		active, err := newInteractiveCommandCapture("sleep 1", nil)
		if err != nil {
			t.Fatal(err)
		}
		shell := &Shell{promptReady: true, interactive: active}
		_, err = shell.RunInteractiveContext(context.Background(), "true", nil)
		if !errors.Is(err, ErrInteractiveShellBusy) {
			t.Fatalf("错误 = %v，期望 ErrInteractiveShellBusy", err)
		}
	})
}

func TestInteractiveShellReadyTracksPromptAndInput(t *testing.T) {
	shell := &Shell{}
	shell.updateTerminalPrompt("\x1b[32mroot@test:~# \x1b[0m")
	if !shell.interactiveShellReady() {
		t.Fatal("带 ANSI 颜色的命令提示符应被识别为空闲")
	}

	shell.markTerminalInput()
	if shell.interactiveShellReady() {
		t.Fatal("用户输入后终端应被判定为忙碌")
	}

	shell.updateTerminalPrompt("正在执行\r\n")
	if shell.interactiveShellReady() {
		t.Fatal("普通命令输出不应被识别为空闲提示符")
	}

	shell.updateTerminalPrompt("Python 3.13\r\n>>> ")
	if shell.interactiveShellReady() {
		t.Fatal("Python REPL 提示符不应被识别为 Shell 命令提示符")
	}
}

func TestTerminalInputAllowedDuringInteractiveCommand(t *testing.T) {
	active, err := newInteractiveCommandCapture("sleep 1", nil)
	if err != nil {
		t.Fatal(err)
	}
	stdin := &testWriteCloser{}
	shell := &Shell{stdin: stdin, interactive: active}

	if err = shell.Write("echo 交互输入\r"); err != nil {
		t.Fatalf("交互输入错误 = %v", err)
	}
	if !bytes.Equal(stdin.Bytes(), []byte("echo 交互输入\r")) {
		t.Fatalf("交互输入写入异常：%q", stdin.String())
	}

	if err = shell.Write("\x03"); err != nil {
		t.Fatalf("Ctrl+C 应允许写入：%v", err)
	}
	if !bytes.Equal(stdin.Bytes(), []byte("echo 交互输入\r\x03")) {
		t.Fatalf("Ctrl+C 写入异常：%v", stdin.Bytes())
	}
}

func TestAbortInteractiveDiscardsLateProtocolOutput(t *testing.T) {
	active, err := newInteractiveCommandCapture("true", nil)
	if err != nil {
		t.Fatal(err)
	}
	var visible strings.Builder
	shell := &Shell{interactive: active, output: func(value string) { visible.WriteString(value) }, stdin: &testWriteCloser{}}
	shell.abortInteractive(errors.New("连接关闭"))
	shell.handleShellOutput(active.wrapper + active.startMarker + active.endPrefix + "0" + active.endSuffix)
	if visible.Len() != 0 {
		t.Fatalf("关闭后的协议内容不应泄漏：%q", visible.String())
	}
}

func TestTerminalEndsAtPromptBroadened(t *testing.T) {
	shouldMatch := []string{
		"root@host:~# ",
		"ubuntu@box:~$ ",
		"macbook% ",
		"user@mac ~ % ",
		"host%",
		"user@box:~$",
		"➜  ~ ",
		"~/project main ❯ ",
		"\x1b(B\x1b[32m➜\x1b[0m ",
		"\x1b]0;zsh\x07host ~ % ",
		"\x1b]133;A\x1b\\❯",
	}
	for _, sample := range shouldMatch {
		if !terminalEndsAtPrompt(sample) {
			t.Fatalf("提示符应被识别为终端就绪：%q", sample)
		}
	}

	shouldNotMatch := []string{
		"",
		"   ",
		"echo hello\r\n",
		">>> ",
		">>>",
		"sql>> ",
		"arrow => ",
		"ptr -> ",
		"...",
		"Downloading... 100%",
		"Progress: 50%",
		"normal output line",
	}
	for _, sample := range shouldNotMatch {
		if terminalEndsAtPrompt(sample) {
			t.Fatalf("非 Shell 提示符不应误判为就绪：%q", sample)
		}
	}
}

func TestSendInteractiveQueueStepTypesIntoPTYAndWaitsForPrompt(t *testing.T) {
	stdin := &testWriteCloser{}
	shell := &Shell{stdin: stdin}
	shell.updateTerminalPrompt("root@host:~# ")

	done := make(chan error, 1)
	go func() {
		done <- shell.SendInteractiveQueueStep(context.Background(), "cd /etc && ls")
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdin.String(), "cd /etc && ls\r") {
		time.Sleep(5 * time.Millisecond)
	}
	if got := stdin.String(); got != "cd /etc && ls\r" {
		t.Fatalf("PTY 写入异常 = %q，期望直接输入命令与回车", got)
	}

	shell.updateTerminalPrompt("cd /etc && ls\r\npasswd\r\n")
	select {
	case err := <-done:
		t.Fatalf("命令尚未回到提示符时不应提前结束: %v", err)
	case <-time.After(150 * time.Millisecond):
	}

	shell.updateTerminalPrompt("root@host:/etc# ")
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("回到提示符后应成功返回，实际: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("回到提示符后未及时完成队列步骤")
	}
}

func TestSendInteractiveQueueStepCancelSendsCtrlC(t *testing.T) {
	stdin := &testWriteCloser{}
	shell := &Shell{stdin: stdin}
	shell.updateTerminalPrompt("root@host:~# ")

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- shell.SendInteractiveQueueStep(ctx, "sleep 30")
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdin.String(), "sleep 30\r") {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("取消队列步骤错误 = %v，期望 context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("取消队列步骤未及时返回")
	}
	if !strings.HasSuffix(stdin.String(), "\x03") {
		t.Fatalf("取消队列步骤应发送 Ctrl+C (\\x03)，实际写入 = %q", stdin.String())
	}
}

func TestRunInteractiveRealPTYContextTypesDirectlyAndCapturesOutput(t *testing.T) {
	stdin := &testWriteCloser{}
	var terminalSeen strings.Builder
	shell := &Shell{
		stdin:  stdin,
		output: func(v string) { terminalSeen.WriteString(v) },
	}
	shell.updateTerminalPrompt("root@host:~# ")

	var streamed strings.Builder
	type resErr struct {
		res Result
		err error
	}
	done := make(chan resErr, 1)
	go func() {
		r, err := shell.RunInteractiveRealPTYContext(context.Background(), "uname -r", func(chunk string) {
			streamed.WriteString(chunk)
		})
		done <- resErr{r, err}
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && !strings.Contains(stdin.String(), "uname -r\r") {
		time.Sleep(5 * time.Millisecond)
	}
	if got := stdin.String(); got != "uname -r\r" {
		t.Fatalf("AI 真实 PTY 写入异常 = %q，期望直接写入命令与回车", got)
	}
	// 允许用户在运行过程中交互输入（不阻塞 Write）。
	if err := shell.Write("y\r"); err != nil {
		t.Fatalf("AI 真实 PTY 执行期间应允许用户交互输入: %v", err)
	}

	shell.handleShellOutput("uname -r\r\n6.1.0-28-amd64\r\nroot@host:~# ")
	select {
	case out := <-done:
		if out.err != nil {
			t.Fatalf("RunInteractiveRealPTYContext 失败: %v", out.err)
		}
		if out.res.ExitCode != 0 {
			t.Fatalf("ExitCode = %d，期望 0", out.res.ExitCode)
		}
		if !strings.Contains(out.res.Output, "6.1.0-28-amd64") {
			t.Fatalf("AI 未捕获到真实 PTY 输出: %q", out.res.Output)
		}
		if !strings.Contains(streamed.String(), "6.1.0-28-amd64") {
			t.Fatalf("AI 流式回调未收到输出: %q", streamed.String())
		}
		if !strings.Contains(terminalSeen.String(), "6.1.0-28-amd64") {
			t.Fatalf("终端未收到实时输出: %q", terminalSeen.String())
		}
	case <-time.After(time.Second):
		t.Fatal("RunInteractiveRealPTYContext 超时未返回")
	}
}
