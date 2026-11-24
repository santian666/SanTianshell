package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"santianshell/internal/remote"
)

type queueRunState struct {
	cancelled bool
	cancels   map[string]context.CancelFunc
}

func (a *App) BeginQueue(runID string) error {
	if err := a.requireUnlocked(); err != nil {
		return err
	}
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return errors.New("队列运行 ID 不能为空")
	}
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if a.shuttingDown.Load() {
		return errors.New("软件正在退出")
	}
	if a.queueRuns == nil {
		a.queueRuns = make(map[string]*queueRunState)
	}
	if _, exists := a.queueRuns[runID]; exists {
		return errors.New("队列已经在运行")
	}
	a.queueRuns[runID] = &queueRunState{cancels: make(map[string]context.CancelFunc)}
	return nil
}

func (a *App) StopQueue(runID string) bool {
	if a.requireUnlocked() != nil {
		return false
	}
	a.queueMu.Lock()
	run := a.queueRuns[strings.TrimSpace(runID)]
	if run == nil {
		a.queueMu.Unlock()
		return false
	}
	run.cancelled = true
	cancels := make([]context.CancelFunc, 0, len(run.cancels))
	for _, cancel := range run.cancels {
		cancels = append(cancels, cancel)
	}
	a.queueMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	return true
}

func (a *App) FinishQueue(runID string) {
	if a.requireUnlocked() != nil {
		return
	}
	runID = strings.TrimSpace(runID)
	a.queueMu.Lock()
	run := a.queueRuns[runID]
	delete(a.queueRuns, runID)
	cancels := make([]context.CancelFunc, 0)
	if run != nil {
		cancels = make([]context.CancelFunc, 0, len(run.cancels))
		for _, cancel := range run.cancels {
			cancels = append(cancels, cancel)
		}
	}
	a.queueMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (a *App) cancelAllQueues() {
	a.queueMu.Lock()
	runs := a.queueRuns
	a.queueRuns = make(map[string]*queueRunState)
	cancels := make([]context.CancelFunc, 0)
	for _, run := range runs {
		run.cancelled = true
		for _, cancel := range run.cancels {
			cancels = append(cancels, cancel)
		}
	}
	a.queueMu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (a *App) registerQueueStep(runID, hostID string, cancel context.CancelFunc) (*queueRunState, error) {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	run := a.queueRuns[runID]
	if run == nil {
		return nil, errors.New("队列运行状态不存在")
	}
	if run.cancelled {
		return nil, context.Canceled
	}
	if _, exists := run.cancels[hostID]; exists {
		return nil, errors.New("该主机已有队列步骤正在执行")
	}
	run.cancels[hostID] = cancel
	return run, nil
}

func (a *App) unregisterQueueStep(run *queueRunState, hostID string) {
	a.queueMu.Lock()
	defer a.queueMu.Unlock()
	if run != nil {
		delete(run.cancels, hostID)
	}
}

func (a *App) RunQueueStep(id string, clientEpoch int, command, runID string, timeoutSeconds int) (remote.Result, error) {
	if err := a.requireUnlocked(); err != nil {
		return remote.Result{}, err
	}
	if strings.TrimSpace(command) == "" {
		return remote.Result{}, errors.New("队列命令不能为空")
	}
	if timeoutSeconds < 0 {
		return remote.Result{}, errors.New("队列单步超时不能小于 0 秒")
	}
	if timeoutSeconds == 0 {
		timeoutSeconds = 300
	}
	if timeoutSeconds > 3600 {
		return remote.Result{}, errors.New("队列单步超时不能超过 3600 秒")
	}
	a.mu.RLock()
	shell := a.shells[id]
	epochMatches := a.shellClientEpochs[id] == clientEpoch
	a.mu.RUnlock()
	if shell == nil || !epochMatches {
		return remote.Result{}, errors.New("SSH 会话已变化，队列已停止")
	}
	ctx, cancel := context.WithTimeout(a.lifecycleContext(), time.Duration(timeoutSeconds)*time.Second)
	run, err := a.registerQueueStep(strings.TrimSpace(runID), id, cancel)
	if err != nil {
		cancel()
		if errors.Is(err, context.Canceled) {
			return remote.Result{ExitCode: -1}, errors.New("队列已停止")
		}
		return remote.Result{ExitCode: -1}, err
	}
	defer func() {
		a.unregisterQueueStep(run, id)
		cancel()
	}()
	started := time.Now()
	runErr := shell.SendInteractiveQueueStep(ctx, command)
	result := remote.Result{Duration: time.Since(started).Milliseconds()}
	if errors.Is(runErr, context.DeadlineExceeded) {
		result.ExitCode = -1
		return result, fmt.Errorf("队列步骤执行超时（%d 秒）", timeoutSeconds)
	}
	if errors.Is(runErr, context.Canceled) {
		result.ExitCode = -1
		return result, errors.New("队列已停止")
	}
	if errors.Is(runErr, remote.ErrInteractiveShellBusy) {
		result.ExitCode = -1
		return result, errors.New("当前终端未处于空闲命令提示符，请先结束当前程序后再执行队列")
	}
	if runErr != nil {
		result.ExitCode = -1
		return result, runErr
	}
	return result, nil
}
