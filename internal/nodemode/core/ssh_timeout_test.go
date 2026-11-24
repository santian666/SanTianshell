package core

import (
	"errors"
	"testing"
	"time"
)

func TestWaitSSHCommandDoneIsBounded(t *testing.T) {
	blocked := make(chan error)
	started := time.Now()
	if waitSSHCommandDone(blocked, 20*time.Millisecond) {
		t.Fatal("未结束的 SSH 命令被误判为已清理")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("SSH 命令清理等待未按时返回: %v", elapsed)
	}

	done := make(chan error, 1)
	done <- errors.New("命令已结束")
	if !waitSSHCommandDone(done, time.Second) {
		t.Fatal("已结束的 SSH 命令未被识别")
	}
}
