package main

import (
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"santianshell/internal/remote"
	"santianshell/internal/sessionlog"
)

const maxPendingTerminalEcho = 1 << 20

// SSHSessionLogStatus 是前端查询到的当前 SSH 会话日志状态。
type SSHSessionLogStatus struct {
	HostID    string `json:"hostId"`
	Enabled   bool   `json:"enabled"`
	Active    bool   `json:"active"`
	Path      string `json:"path,omitempty"`
	Directory string `json:"directory"`
	Error     string `json:"error,omitempty"`
}

type sshSessionLogState struct {
	enabled     bool
	epoch       uint64
	logger      *sessionlog.Logger
	path        string
	lastError   string
	pendingEcho []byte
}

func sshSessionLogDirectory() string {
	return filepath.Join(logDirectory(), "ssh-sessions")
}

// GetSSHSessionLogStatus 查询指定主机当前会话的日志状态，默认关闭。
func (a *App) GetSSHSessionLogStatus(id string) (SSHSessionLogStatus, error) {
	if err := a.requireUnlocked(); err != nil {
		return SSHSessionLogStatus{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return SSHSessionLogStatus{}, errors.New("主机 ID 不能为空")
	}
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	return a.sshSessionLogStatusLocked(id), nil
}

// SetSSHSessionLogging 开启或关闭指定主机当前 SSH 会话的日志。
func (a *App) SetSSHSessionLogging(id string, enabled bool) (SSHSessionLogStatus, error) {
	if err := a.requireUnlocked(); err != nil {
		return SSHSessionLogStatus{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return SSHSessionLogStatus{}, errors.New("主机 ID 不能为空")
	}
	if !enabled {
		return a.disableSSHSessionLog(id)
	}

	host, err := a.host(id)
	if err != nil {
		return SSHSessionLogStatus{}, err
	}
	a.mu.RLock()
	if a.shuttingDown.Load() || a.shells[id] == nil {
		a.mu.RUnlock()
		if a.shuttingDown.Load() {
			return SSHSessionLogStatus{}, errors.New("软件正在退出")
		}
		return SSHSessionLogStatus{}, errors.New("SSH 会话尚未连接")
	}
	epoch := a.shellEpochs[id]
	a.sessionLogMu.Lock()
	defer a.mu.RUnlock()
	defer a.sessionLogMu.Unlock()
	if a.sessionLogs == nil {
		a.sessionLogs = make(map[string]*sshSessionLogState)
	}
	state := a.sessionLogs[id]
	if state == nil {
		state = &sshSessionLogState{}
		a.sessionLogs[id] = state
	}
	state.enabled = true
	state.lastError = ""
	if state.logger != nil && state.epoch == epoch {
		return a.sshSessionLogStatusLocked(id), nil
	}
	if state.logger != nil {
		_ = state.logger.Close()
		state.logger = nil
		state.pendingEcho = nil
	}
	if err := a.openSSHSessionLogLocked(state, epoch, host); err != nil {
		state.enabled = false
		state.lastError = err.Error()
		return a.sshSessionLogStatusLocked(id), err
	}
	return a.sshSessionLogStatusLocked(id), nil
}

func (a *App) sshSessionLogStatusLocked(id string) SSHSessionLogStatus {
	status := SSHSessionLogStatus{HostID: id, Directory: sshSessionLogDirectory()}
	if state := a.sessionLogs[id]; state != nil {
		status.Enabled = state.enabled
		status.Active = state.logger != nil
		status.Path = state.path
		status.Error = state.lastError
	}
	return status
}

func (a *App) openSSHSessionLogLocked(state *sshSessionLogState, epoch uint64, host remote.Host) error {
	logger, err := sessionlog.New(sshSessionLogDirectory(), sessionlog.SessionInfo{
		Name: host.Name, Address: host.Address, Port: host.Port, Username: host.Username,
	})
	if err != nil {
		return fmt.Errorf("创建 SSH 会话日志失败: %w", err)
	}
	state.epoch = epoch
	state.logger = logger
	state.path = logger.Path()
	state.lastError = ""
	state.pendingEcho = nil
	return nil
}

func (a *App) beginSSHSessionLog(id string, epoch uint64, host remote.Host) error {
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	state := a.sessionLogs[id]
	if state == nil || !state.enabled {
		return nil
	}
	if state.logger != nil && state.epoch == epoch {
		return nil
	}
	if state.logger != nil {
		_ = state.logger.Close()
		state.logger = nil
	}
	if err := a.openSSHSessionLogLocked(state, epoch, host); err != nil {
		state.lastError = err.Error()
		return err
	}
	return nil
}

func (a *App) pauseSSHSessionLog(id string) {
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	state := a.sessionLogs[id]
	if state == nil {
		return
	}
	if state.logger != nil {
		if err := state.logger.Close(); err != nil {
			state.lastError = err.Error()
		}
	}
	state.logger = nil
	state.epoch = 0
	state.pendingEcho = nil
}

func (a *App) finishSSHSessionLog(id string, epoch uint64) {
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	state := a.sessionLogs[id]
	if state == nil || state.epoch != epoch {
		return
	}
	if state.logger != nil {
		if err := state.logger.Close(); err != nil {
			state.lastError = err.Error()
		}
	}
	state.logger = nil
	state.epoch = 0
	state.pendingEcho = nil
}

func (a *App) disableSSHSessionLog(id string) (SSHSessionLogStatus, error) {
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	status := SSHSessionLogStatus{HostID: id, Directory: sshSessionLogDirectory()}
	state := a.sessionLogs[id]
	if state == nil {
		return status, nil
	}
	status.Path = state.path
	var closeErr error
	if state.logger != nil {
		closeErr = state.logger.Close()
	}
	delete(a.sessionLogs, id)
	if closeErr != nil {
		return status, fmt.Errorf("关闭 SSH 会话日志失败: %w", closeErr)
	}
	return status, nil
}

func (a *App) closeAllSSHSessionLogs() {
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	for id, state := range a.sessionLogs {
		if state != nil && state.logger != nil {
			if err := state.logger.Close(); err != nil {
				slog.Warn("关闭 SSH 会话日志失败", "主机ID", id, "错误", err)
			}
		}
	}
	a.sessionLogs = nil
}

func (a *App) rememberSSHSessionInput(id string, epoch uint64, input string) {
	if input == "" {
		return
	}
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	state := a.sessionLogs[id]
	if state == nil || state.logger == nil || state.epoch != epoch {
		return
	}
	if len(state.pendingEcho)+len(input) > maxPendingTerminalEcho {
		_ = state.logger.Close()
		state.logger = nil
		state.enabled = false
		state.pendingEcho = nil
		state.lastError = "用户输入过长，为避免将终端回显写入日志，已自动停止会话日志"
		return
	}
	state.pendingEcho = append(state.pendingEcho, input...)
}

func (a *App) forgetSSHSessionInput(id string, epoch uint64, input string) {
	if input == "" {
		return
	}
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	state := a.sessionLogs[id]
	if state == nil || state.epoch != epoch || len(state.pendingEcho) < len(input) {
		return
	}
	start := len(state.pendingEcho) - len(input)
	if string(state.pendingEcho[start:]) == input {
		state.pendingEcho = state.pendingEcho[:start]
	}
}

func (a *App) writeSSHSessionOutput(id string, epoch uint64, output string) {
	if output == "" {
		return
	}
	a.sessionLogMu.Lock()
	defer a.sessionLogMu.Unlock()
	state := a.sessionLogs[id]
	if state == nil || state.logger == nil || state.epoch != epoch {
		return
	}
	output = state.removePendingEcho(output)
	if output == "" {
		return
	}
	if _, err := state.logger.Write([]byte(output)); err != nil {
		_ = state.logger.Close()
		state.logger = nil
		state.pendingEcho = nil
		state.lastError = fmt.Sprintf("写入 SSH 会话日志失败: %v", err)
	}
}

func (state *sshSessionLogState) removePendingEcho(output string) string {
	if len(state.pendingEcho) == 0 || output == "" {
		return output
	}
	raw := []byte(output)
	matched := 0
	for matched < len(raw) && matched < len(state.pendingEcho) && raw[matched] == state.pendingEcho[matched] {
		matched++
	}
	if matched > 0 {
		state.pendingEcho = state.pendingEcho[matched:]
		raw = raw[matched:]
	}
	if len(raw) > 0 && len(state.pendingEcho) > 0 {
		state.pendingEcho = nil
	}
	return string(raw)
}
