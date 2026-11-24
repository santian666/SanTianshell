package remote

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

var (
	ErrInteractiveShellBusy      = errors.New("当前交互终端不在空闲命令提示符")
	ErrInteractiveInputBusy      = errors.New("AI 正在使用当前终端，仅允许 Ctrl+C 中断")
	ErrInteractiveShellUncertain = errors.New("AI 命令中断后无法确认终端状态，SSH 会话已关闭，请重连")
)

var terminalEscapeSequence = regexp.MustCompile(`\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[()][A-Za-z0-9]|[=>])`)

type interactiveOutputTap struct {
	command   string
	output    cappedBuffer
	onOutput  func(string)
	aborted   bool
	trimEcho  bool
	remainder string
}

func (tap *interactiveOutputTap) feed(raw string) {
	if tap == nil || tap.aborted || raw == "" {
		return
	}
	clean := terminalEscapeSequence.ReplaceAllString(raw, "")
	clean = strings.ReplaceAll(clean, "\r", "")
	if clean == "" {
		return
	}
	if !tap.trimEcho && tap.command != "" {
		tap.remainder += clean
		nl := strings.IndexByte(tap.remainder, '\n')
		if nl < 0 {
			if len(tap.remainder) < len(tap.command)+32 {
				return
			}
			clean = tap.remainder
			tap.remainder = ""
			tap.trimEcho = true
		} else {
			firstLine := strings.TrimSpace(tap.remainder[:nl])
			rest := tap.remainder[nl+1:]
			tap.remainder = ""
			tap.trimEcho = true
			if firstLine == strings.TrimSpace(tap.command) || strings.HasSuffix(firstLine, tap.command) {
				clean = rest
			} else {
				clean = firstLine + "\n" + rest
			}
		}
	}
	if clean == "" {
		return
	}
	_, _ = tap.output.Write([]byte(clean))
	if tap.onOutput != nil {
		tap.onOutput(clean)
	}
}

func (tap *interactiveOutputTap) resultOutput() string {
	if tap == nil {
		return ""
	}
	text := tap.output.String()
	lines := strings.Split(text, "\n")
	if len(lines) > 0 && terminalEndsAtPrompt(lines[len(lines)-1]) {
		lines = lines[:len(lines)-1]
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + func() string {
		if len(lines) > 0 && strings.TrimSpace(strings.Join(lines, "\n")) != "" {
			return "\n"
		}
		return ""
	}()
}

// RunInteractiveRealPTYContext 直接向交互式 PTY 写入真实命令（不套用 subshell 包装脚本），
// 实时旁路采集输出并允许用户在交互提示时直接键盘输入，待回到空闲提示符后返回。
func (s *Shell) RunInteractiveRealPTYContext(ctx context.Context, command string, onOutput func(string)) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, err
	}
	if s == nil || s.stdin == nil || s.isClosed() {
		return Result{ExitCode: -1}, errors.New("SSH 会话已关闭")
	}
	if strings.TrimSpace(command) == "" || strings.ContainsRune(command, 0) || strings.ContainsRune(command, '\r') {
		return Result{ExitCode: -1}, errors.New("交互命令无效")
	}
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	if !s.waitPromptReady(ctx, 1500*time.Millisecond) {
		if ctx.Err() != nil {
			return Result{ExitCode: -1}, ctx.Err()
		}
		if s.isClosed() {
			return Result{ExitCode: -1}, errors.New("SSH 会话已关闭")
		}
		return Result{ExitCode: -1}, ErrInteractiveShellBusy
	}
	tap := &interactiveOutputTap{command: strings.TrimSpace(command), onOutput: onOutput}
	s.writeMu.Lock()
	s.interactiveMu.Lock()
	if s.interactive != nil || s.ptyTap != nil {
		s.interactiveMu.Unlock()
		s.writeMu.Unlock()
		return Result{ExitCode: -1}, ErrInteractiveShellBusy
	}
	s.ptyTap = tap
	s.interactiveMu.Unlock()
	start := time.Now()
	s.markTerminalInput()
	_, err := io.WriteString(s.stdin, command+"\r")
	s.writeMu.Unlock()
	defer func() {
		s.interactiveMu.Lock()
		if s.ptyTap == tap {
			s.ptyTap = nil
		}
		tap.aborted = true
		s.interactiveMu.Unlock()
	}()
	if err != nil {
		return Result{Output: tap.resultOutput(), ExitCode: -1, Duration: time.Since(start).Milliseconds()}, err
	}

	const promptSettleDelay = 120 * time.Millisecond
	for {
		ready, closed, ch := s.promptState()
		if closed {
			return Result{Output: tap.resultOutput(), ExitCode: -1, Duration: time.Since(start).Milliseconds()}, errors.New("SSH 会话已关闭")
		}
		if !ready {
			select {
			case <-ch:
			case <-ctx.Done():
				_ = s.writeTerminal("\x03", true)
				return Result{Output: tap.resultOutput(), ExitCode: -1, Duration: time.Since(start).Milliseconds()}, ctx.Err()
			}
			continue
		}
		settle := time.NewTimer(promptSettleDelay)
		select {
		case <-ch:
			settle.Stop()
		case <-ctx.Done():
			settle.Stop()
			_ = s.writeTerminal("\x03", true)
			return Result{Output: tap.resultOutput(), ExitCode: -1, Duration: time.Since(start).Milliseconds()}, ctx.Err()
		case <-settle.C:
			if s.interactiveShellReady() {
				return Result{Output: tap.resultOutput(), ExitCode: 0, Duration: time.Since(start).Milliseconds()}, nil
			}
		}
	}
}

type interactiveCommandCapture struct {
	command     string
	wrapper     string
	startMarker string
	endPrefix   string
	endSuffix   string
	phase       int
	pending     string
	output      cappedBuffer
	onOutput    func(string)
	done        chan interactiveCommandResult
	aborted     bool
}

type interactiveCommandResult struct {
	exitCode int
	output   string
	err      error
}

func (s *Shell) RunInteractiveContext(ctx context.Context, command string, onOutput func(string)) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return Result{ExitCode: -1}, err
	}
	if strings.TrimSpace(command) == "" || strings.ContainsRune(command, 0) || strings.ContainsRune(command, '\r') {
		return Result{ExitCode: -1}, errors.New("交互命令无效")
	}
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	if !s.interactiveShellReady() {
		return Result{ExitCode: -1}, ErrInteractiveShellBusy
	}
	capture, err := newInteractiveCommandCapture(command, onOutput)
	if err != nil {
		return Result{ExitCode: -1}, err
	}
	s.writeMu.Lock()
	s.interactiveMu.Lock()
	if s.interactive != nil {
		s.interactiveMu.Unlock()
		s.writeMu.Unlock()
		return Result{ExitCode: -1}, ErrInteractiveShellBusy
	}
	s.interactive = capture
	s.interactiveMu.Unlock()
	start := time.Now()
	written, err := io.WriteString(s.stdin, capture.wrapper+"\r")
	if written > 0 {
		s.markTerminalInput()
	}
	s.writeMu.Unlock()
	if err != nil {
		s.clearInteractive(capture)
		return Result{ExitCode: -1, Duration: time.Since(start).Milliseconds()}, err
	}
	select {
	case completed := <-capture.done:
		return Result{Output: completed.output, ExitCode: completed.exitCode, Duration: time.Since(start).Milliseconds()}, completed.err
	case <-ctx.Done():
		select {
		case completed := <-capture.done:
			return Result{Output: completed.output, ExitCode: completed.exitCode, Duration: time.Since(start).Milliseconds()}, completed.err
		default:
		}
		_ = s.writeTerminal("\x03", true)
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case completed := <-capture.done:
			return Result{Output: completed.output, ExitCode: completed.exitCode, Duration: time.Since(start).Milliseconds()}, ctx.Err()
		case <-timer.C:
			s.Close()
			return Result{ExitCode: -1, Duration: time.Since(start).Milliseconds()}, fmt.Errorf("%w：%v", ErrInteractiveShellUncertain, ctx.Err())
		}
	}
}

func newInteractiveCommandCapture(command string, onOutput func(string)) (*interactiveCommandCapture, error) {
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return nil, fmt.Errorf("生成交互命令标识失败: %w", err)
	}
	token := hex.EncodeToString(random)
	startMarker := "\x1eSANTIANSHELL_START_" + token + "\x1f"
	endPrefix := "\x1eSANTIANSHELL_END_" + token + ":"
	endSuffix := "\x1f"
	commandVariable := "__santianshell_command_" + token
	statusVariable := "__santianshell_status_" + token
	encodedCommand := base64.StdEncoding.EncodeToString([]byte(command))
	wrapper := fmt.Sprintf(
		"printf '\\036SANTIANSHELL_START_%s\\037'; %s=$(printf '%%s' '%s' | base64 -d); %s=$?; if [ \"$%s\" -eq 0 ]; then ( eval \"$%s\" ); %s=$?; fi; printf '\\036SANTIANSHELL_END_%s:%%s\\037' \"$%s\"; unset %s %s",
		token,
		commandVariable,
		encodedCommand,
		statusVariable,
		statusVariable,
		commandVariable,
		statusVariable,
		token,
		statusVariable,
		commandVariable,
		statusVariable,
	)
	return &interactiveCommandCapture{
		command:     command,
		wrapper:     wrapper,
		startMarker: startMarker,
		endPrefix:   endPrefix,
		endSuffix:   endSuffix,
		onOutput:    onOutput,
		done:        make(chan interactiveCommandResult, 1),
	}, nil
}

func (s *Shell) handleShellOutput(value string) {
	if value == "" {
		return
	}
	terminalOutput := value
	commandOutput := ""
	var callback func(string)
	var completed *interactiveCommandResult
	var done chan interactiveCommandResult
	s.interactiveMu.Lock()
	if capture := s.interactive; capture != nil {
		terminalOutput, commandOutput, completed = capture.consume(value)
		callback = capture.onOutput
		if completed != nil {
			s.interactive = nil
			done = capture.done
		}
	} else if tap := s.ptyTap; tap != nil {
		tap.feed(terminalOutput)
	}
	s.interactiveMu.Unlock()
	if terminalOutput != "" {
		s.updateTerminalPrompt(terminalOutput)
		if s.output != nil {
			s.output(terminalOutput)
		}
	}
	if completed != nil {
		s.markTerminalPromptReady()
	}
	if commandOutput != "" && callback != nil {
		callback(commandOutput)
	}
	if completed != nil {
		done <- *completed
	}
}

func (capture *interactiveCommandCapture) consume(value string) (string, string, *interactiveCommandResult) {
	if capture.aborted {
		return "", "", nil
	}
	capture.pending += value
	var terminalOutput strings.Builder
	var commandOutput strings.Builder
	for {
		switch capture.phase {
		case 0:
			wrapperIndex := strings.Index(capture.pending, capture.wrapper)
			startIndex := strings.Index(capture.pending, capture.startMarker)
			if startIndex >= 0 && (wrapperIndex < 0 || startIndex < wrapperIndex) {
				terminalOutput.WriteString(capture.pending[:startIndex])
				terminalOutput.WriteString(capture.command)
				terminalOutput.WriteString("\r\n")
				capture.pending = capture.pending[startIndex+len(capture.startMarker):]
				capture.phase = 2
				continue
			}
			if wrapperIndex >= 0 {
				terminalOutput.WriteString(capture.pending[:wrapperIndex])
				terminalOutput.WriteString(capture.command)
				capture.pending = capture.pending[wrapperIndex+len(capture.wrapper):]
				capture.phase = 1
				continue
			}
			length := flushableLength(capture.pending, max(len(capture.wrapper), len(capture.startMarker)))
			terminalOutput.WriteString(capture.pending[:length])
			capture.pending = capture.pending[length:]
			return terminalOutput.String(), commandOutput.String(), nil
		case 1:
			index := strings.Index(capture.pending, capture.startMarker)
			if index >= 0 {
				terminalOutput.WriteString(capture.pending[:index])
				capture.pending = capture.pending[index+len(capture.startMarker):]
				capture.phase = 2
				continue
			}
			length := flushableLength(capture.pending, len(capture.startMarker))
			terminalOutput.WriteString(capture.pending[:length])
			capture.pending = capture.pending[length:]
			return terminalOutput.String(), commandOutput.String(), nil
		case 2:
			index := strings.Index(capture.pending, capture.endPrefix)
			if index >= 0 {
				part := capture.pending[:index]
				terminalOutput.WriteString(part)
				commandOutput.WriteString(part)
				_, _ = capture.output.Write([]byte(part))
				capture.pending = capture.pending[index+len(capture.endPrefix):]
				capture.phase = 3
				continue
			}
			length := flushableLength(capture.pending, len(capture.endPrefix))
			part := capture.pending[:length]
			terminalOutput.WriteString(part)
			commandOutput.WriteString(part)
			_, _ = capture.output.Write([]byte(part))
			capture.pending = capture.pending[length:]
			return terminalOutput.String(), commandOutput.String(), nil
		case 3:
			index := strings.Index(capture.pending, capture.endSuffix)
			if index < 0 {
				return terminalOutput.String(), commandOutput.String(), nil
			}
			exitCode, err := strconv.Atoi(strings.TrimSpace(capture.pending[:index]))
			capture.pending = capture.pending[index+len(capture.endSuffix):]
			capture.phase = 4
			if err != nil {
				completed := interactiveCommandResult{exitCode: -1, output: capture.output.Text(), err: errors.New("无法读取交互命令退出码")}
				return terminalOutput.String(), commandOutput.String(), &completed
			}
			completed := interactiveCommandResult{exitCode: exitCode, output: capture.output.Text()}
			terminalOutput.WriteString(capture.pending)
			capture.pending = ""
			return terminalOutput.String(), commandOutput.String(), &completed
		default:
			terminalOutput.WriteString(capture.pending)
			capture.pending = ""
			return terminalOutput.String(), commandOutput.String(), nil
		}
	}
}

func flushableLength(value string, tokenLength int) int {
	if tokenLength < 2 || len(value) < tokenLength {
		return 0
	}
	length := len(value) - tokenLength + 1
	for length > 0 && length < len(value) && !utf8.RuneStart(value[length]) {
		length--
	}
	return length
}

func (s *Shell) promptChangedLocked() {
	if s.promptNotify != nil {
		close(s.promptNotify)
		s.promptNotify = nil
	}
}

func (s *Shell) promptState() (ready bool, closed bool, ch <-chan struct{}) {
	if s == nil || s.isClosed() {
		return false, true, nil
	}
	s.promptMu.Lock()
	defer s.promptMu.Unlock()
	if s.promptNotify == nil {
		s.promptNotify = make(chan struct{})
	}
	return s.promptReady, false, s.promptNotify
}

func (s *Shell) waitPromptReady(ctx context.Context, maxWait time.Duration) bool {
	if maxWait <= 0 {
		ready, closed, _ := s.promptState()
		return ready && !closed
	}
	deadline := time.NewTimer(maxWait)
	defer deadline.Stop()
	for {
		ready, closed, ch := s.promptState()
		if closed {
			return false
		}
		if ready {
			return true
		}
		select {
		case <-ch:
		case <-deadline.C:
			return false
		case <-ctx.Done():
			return false
		}
	}
}

// SendInteractiveQueueStep 直接向交互式 PTY 写入命令并回车，待终端重新回到空闲提示符后返回。
func (s *Shell) SendInteractiveQueueStep(ctx context.Context, command string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || s.stdin == nil || s.isClosed() {
		return errors.New("SSH 会话已关闭")
	}
	if strings.TrimSpace(command) == "" || strings.ContainsRune(command, 0) || strings.ContainsRune(command, '\r') {
		return errors.New("交互命令无效")
	}
	s.commandMu.Lock()
	defer s.commandMu.Unlock()
	if !s.waitPromptReady(ctx, 1500*time.Millisecond) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if s.isClosed() {
			return errors.New("SSH 会话已关闭")
		}
		return ErrInteractiveShellBusy
	}
	s.writeMu.Lock()
	s.interactiveMu.Lock()
	if s.interactive != nil {
		s.interactiveMu.Unlock()
		s.writeMu.Unlock()
		return ErrInteractiveShellBusy
	}
	s.interactiveMu.Unlock()
	s.markTerminalInput()
	_, err := io.WriteString(s.stdin, command+"\r")
	s.writeMu.Unlock()
	if err != nil {
		return err
	}

	const promptSettleDelay = 120 * time.Millisecond
	for {
		ready, closed, ch := s.promptState()
		if closed {
			return errors.New("SSH 会话已关闭")
		}
		if !ready {
			select {
			case <-ch:
			case <-ctx.Done():
				_ = s.writeTerminal("\x03", true)
				return ctx.Err()
			}
			continue
		}
		settle := time.NewTimer(promptSettleDelay)
		select {
		case <-ch:
			settle.Stop()
		case <-ctx.Done():
			settle.Stop()
			_ = s.writeTerminal("\x03", true)
			return ctx.Err()
		case <-settle.C:
			if s.interactiveShellReady() {
				return nil
			}
		}
	}
}

func (s *Shell) markTerminalInput() {
	s.promptMu.Lock()
	s.promptReady = false
	s.promptChangedLocked()
	s.promptMu.Unlock()
}

func (s *Shell) interactiveShellReady() bool {
	s.promptMu.RLock()
	ready := s.promptReady
	s.promptMu.RUnlock()
	return ready && !s.isClosed()
}

func (s *Shell) markTerminalPromptReady() {
	s.promptMu.Lock()
	s.promptReady = true
	s.promptChangedLocked()
	s.promptMu.Unlock()
}

func (s *Shell) updateTerminalPrompt(output string) {
	s.promptMu.Lock()
	s.promptTail = trimPromptTailUTF8(s.promptTail+output, 4096)
	s.promptReady = terminalEndsAtPrompt(s.promptTail)
	s.promptChangedLocked()
	s.promptMu.Unlock()
}

func trimPromptTailUTF8(tail string, maxBytes int) string {
	if len(tail) <= maxBytes {
		return tail
	}
	start := len(tail) - maxBytes
	for start < len(tail) && !utf8.RuneStart(tail[start]) {
		start++
	}
	return tail[start:]
}

func terminalEndsAtPrompt(output string) bool {
	plain := terminalEscapeSequence.ReplaceAllString(output, "")
	plain = strings.ReplaceAll(plain, "\r", "")
	if index := strings.LastIndexByte(plain, '\n'); index >= 0 {
		plain = plain[index+1:]
	}
	trimmed := strings.TrimRight(plain, " \t")
	if trimmed == "" {
		return false
	}
	for _, excluded := range []string{">>>", ">>", "=>", "->", "..."} {
		if strings.HasSuffix(trimmed, excluded) {
			return false
		}
	}
	for _, suffix := range []string{"#", "$", "❯", "➜"} {
		if strings.HasSuffix(trimmed, suffix) {
			return true
		}
	}
	trimmedLeft := strings.TrimLeft(plain, " \t")
	if strings.HasPrefix(trimmedLeft, "➜ ") && strings.HasSuffix(plain, " ") && len(trimmedLeft) <= 160 && !strings.HasSuffix(trimmed, ":") {
		return true
	}
	if strings.HasSuffix(plain, "% ") {
		return true
	}
	if strings.HasSuffix(trimmed, "%") {
		if len(trimmed) == 1 {
			return true
		}
		prev := trimmed[len(trimmed)-2]
		if prev < '0' || prev > '9' {
			return true
		}
	}
	return false
}

func (s *Shell) clearInteractive(capture *interactiveCommandCapture) {
	s.interactiveMu.Lock()
	if s.interactive == capture {
		s.interactive = nil
	}
	s.interactiveMu.Unlock()
}

func (s *Shell) abortInteractive(err error) {
	s.interactiveMu.Lock()
	capture := s.interactive
	if capture != nil {
		capture.aborted = true
		capture.pending = ""
	}
	s.interactiveMu.Unlock()
	if capture != nil {
		select {
		case capture.done <- interactiveCommandResult{exitCode: -1, output: capture.output.Text(), err: err}:
		default:
		}
	}
}
