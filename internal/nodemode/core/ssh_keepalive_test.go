package core

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestMonitorSSHKeepAliveStopsWhenConnectionCloses(t *testing.T) {
	ticks := make(chan time.Time)
	closed := make(chan struct{})
	done := make(chan struct{})
	var sends atomic.Int32
	var closes atomic.Int32
	go func() {
		monitorSSHKeepAlive(ticks, closed, func() error {
			sends.Add(1)
			return nil
		}, func() {
			closes.Add(1)
		})
		close(done)
	}()

	close(closed)
	select {
	case <-done:
	case <-time.After(250 * time.Millisecond):
		t.Fatal("SSH 连接主动关闭后 Keep-Alive 协程未及时退出")
	}
	if sends.Load() != 0 || closes.Load() != 0 {
		t.Fatalf("连接关闭后仍执行 Keep-Alive: send=%d close=%d", sends.Load(), closes.Load())
	}
}
