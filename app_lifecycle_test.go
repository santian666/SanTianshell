package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"santianshell/internal/remote"
)

func TestWaitForShutdownCleanupHasGlobalTimeout(t *testing.T) {
	completed := make(chan struct{})
	close(completed)
	blocked := make(chan struct{})
	started := time.Now()
	if waitForShutdownCleanup([]<-chan struct{}{completed, blocked}, 30*time.Millisecond) {
		t.Fatal("未完成的 RDP 清理被误判为完成")
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("等待 RDP 清理未按全局超时返回: %v", elapsed)
	}
	close(blocked)
	if !waitForShutdownCleanup([]<-chan struct{}{completed, blocked}, time.Second) {
		t.Fatal("已完成的 RDP 清理被误判为超时")
	}
}

func TestShutdownCancelsLifecycleAndRejectsNewResources(t *testing.T) {
	lifecycleCtx, lifecycleCancel := context.WithCancel(context.Background())
	app := &App{
		lifecycleCtx:    lifecycleCtx,
		lifecycleCancel: lifecycleCancel,
		queueRuns:       make(map[string]*queueRunState),
		shellEpochs:     make(map[string]uint64),
	}
	app.shutdown(context.Background())
	select {
	case <-lifecycleCtx.Done():
	default:
		t.Fatal("关机未取消应用生命周期 context")
	}
	if err := app.BeginQueue("queue-after-shutdown"); err == nil || !strings.Contains(err.Error(), "退出") {
		t.Fatalf("关机后仍能创建队列: %v", err)
	}
	if err := app.startTerminalWithHostGuarded("host-1", 1, remote.Host{}, 0, nil); err == nil || !strings.Contains(err.Error(), "退出") {
		t.Fatalf("关机后仍能启动 SSH: %v", err)
	}
	if err := app.validateAndRegisterRDPSessionLocked("host-1", "", nil); err == nil || !strings.Contains(err.Error(), "退出") {
		t.Fatalf("关机后仍能登记 RDP: %v", err)
	}
}

func TestCollectJSONFilesEnforcesBoundsAndSkipsSymlink(t *testing.T) {
	t.Run("rejects excessive directory depth", func(t *testing.T) {
		deepRoot := t.TempDir()
		cur := deepRoot
		for i := 0; i <= maxHostImportDirDepth+1; i++ {
			cur = filepath.Join(cur, "d")
		}
		if err := os.MkdirAll(cur, 0o700); err != nil {
			t.Fatal(err)
		}
		if _, err := collectJSONFiles(deepRoot); err == nil || !strings.Contains(err.Error(), "层级过深") {
			t.Fatalf("expected depth limit error containing 层级过深, got %v", err)
		}
	})

	t.Run("rejects excessive file count", func(t *testing.T) {
		manyRoot := t.TempDir()
		for i := 0; i <= maxHostImportFiles; i++ {
			name := fmt.Sprintf("host-%04d.json", i)
			if err := os.WriteFile(filepath.Join(manyRoot, name), []byte("{}"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := collectJSONFiles(manyRoot); err == nil {
			t.Fatalf("expected error when file count exceeds %d, got nil", maxHostImportFiles)
		}
	})

	t.Run("skips directory symlink", func(t *testing.T) {
		base := t.TempDir()
		targetDir := filepath.Join(base, "target")
		if err := os.MkdirAll(targetDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(targetDir, "linked.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		scanRoot := filepath.Join(base, "scan")
		if err := os.MkdirAll(scanRoot, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scanRoot, "direct.json"), []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(targetDir, filepath.Join(scanRoot, "link")); err != nil {
			t.Skipf("symlink not supported in this environment: %v", err)
		}
		files, err := collectJSONFiles(scanRoot)
		if err != nil {
			t.Fatalf("collectJSONFiles unexpected error: %v", err)
		}
		if len(files) != 1 || filepath.Base(files[0]) != "direct.json" {
			t.Fatalf("expected only direct.json without following symlink, got %v", files)
		}
	})

	t.Run("rejects oversized import file", func(t *testing.T) {
		dir := t.TempDir()
		oversized := filepath.Join(dir, "huge.json")
		if err := os.WriteFile(oversized, make([]byte, maxHostImportFileSize+1), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readBoundedImportFile(oversized, maxHostImportFileSize); err == nil || !strings.Contains(err.Error(), "大小限制") {
			t.Fatalf("expected size limit error containing 大小限制, got %v", err)
		}
	})
}

func TestMarkHostUsedRollbackOnSaveError(t *testing.T) {
	const initialLastUsed = "2020-01-01T00:00:00Z"
	app := &App{
		unlocked: true,
		store:    &memorySettingsStore{err: errors.New("save failed")},
		settings: Settings{
			Hosts: []remote.Host{{ID: "h1", LastUsed: initialLastUsed}},
		},
	}
	app.MarkHostUsed("h1")
	if got := app.settings.Hosts[0].LastUsed; got != initialLastUsed {
		t.Fatalf("MarkHostUsed should not persist LastUsed when store fails: got %q, want %q", got, initialLastUsed)
	}
}

func TestSameSSHServerTreatsDefaultZeroAnd22Equal(t *testing.T) {
	if !sameSSHServer(
		remote.Host{Address: "example.com", Port: 0},
		remote.Host{Address: "EXAMPLE.COM", Port: 22},
	) {
		t.Fatal("expected Port 0 and Port 22 with matching host address to be treated as equal")
	}
	if sameSSHServer(
		remote.Host{Address: "example.com", Port: 22},
		remote.Host{Address: "example.com", Port: 2222},
	) {
		t.Fatal("expected different ports 22 and 2222 not to match")
	}
}
