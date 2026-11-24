package remote

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
)

func TestTransferBetweenClientsCopiesDirectoryStreaming(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "source", "nested"), 0o750); err != nil {
		t.Fatal(err)
	}
	first := []byte(strings.Repeat("SanTianShell", 20000))
	second := []byte("嵌套文件内容")
	firstPath := filepath.Join(sourceRoot, "source", "first.bin")
	secondPath := filepath.Join(sourceRoot, "source", "nested", "second.txt")
	if err := os.WriteFile(firstPath, first, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondPath, second, 0o600); err != nil {
		t.Fatal(err)
	}
	wantTime := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
	if err := os.Chtimes(firstPath, wantTime, wantTime); err != nil {
		t.Fatal(err)
	}

	sourceClient := newTransferTestClient(t, sourceRoot)
	targetClient := newTransferTestClient(t, targetRoot)
	var progress []TransferStats
	stats, err := TransferBetweenClients(context.Background(), sourceClient, targetClient, "source", "copied", false, func(current TransferStats) {
		progress = append(progress, current)
	})
	if err != nil {
		t.Fatalf("复制目录失败：%v", err)
	}
	if stats.Files != 2 || stats.Directories != 2 || stats.Bytes != int64(len(first)+len(second)) {
		t.Fatalf("传输统计不正确：%+v", stats)
	}
	if len(progress) == 0 || progress[len(progress)-1] != stats {
		t.Fatalf("最终进度 = %+v，期望 %+v", progress, stats)
	}
	assertFileContent(t, filepath.Join(targetRoot, "copied", "first.bin"), first)
	assertFileContent(t, filepath.Join(targetRoot, "copied", "nested", "second.txt"), second)
	if runtime.GOOS != "windows" {
		info, statErr := os.Stat(filepath.Join(targetRoot, "copied", "first.bin"))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if got := info.Mode().Perm(); got != 0o640 {
			t.Fatalf("目标文件权限 = %o，期望 640", got)
		}
		if delta := info.ModTime().Sub(wantTime); delta < -time.Second || delta > time.Second {
			t.Fatalf("目标文件修改时间 = %s，期望接近 %s", info.ModTime(), wantTime)
		}
	}
}

func TestTransferBetweenClientsDoesNotOverwriteByDefault(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceRoot, "source.txt"), []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(targetRoot, "target.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceClient := newTransferTestClient(t, sourceRoot)
	targetClient := newTransferTestClient(t, targetRoot)

	stats, err := TransferBetweenClients(context.Background(), sourceClient, targetClient, "source.txt", "target.txt", false, nil)
	if err == nil || !strings.Contains(err.Error(), "未启用覆盖") {
		t.Fatalf("默认覆盖错误 = %v，期望明确拒绝", err)
	}
	if stats != (TransferStats{}) {
		t.Fatalf("失败传输统计不应增加：%+v", stats)
	}
	assertFileContent(t, filepath.Join(targetRoot, "target.txt"), []byte("old"))

	stats, err = TransferBetweenClients(context.Background(), sourceClient, targetClient, "source.txt", "target.txt", true, nil)
	if err != nil {
		t.Fatalf("启用覆盖后复制失败：%v", err)
	}
	if stats.Files != 1 || stats.Bytes != 3 {
		t.Fatalf("覆盖传输统计不正确：%+v", stats)
	}
	assertFileContent(t, filepath.Join(targetRoot, "target.txt"), []byte("new"))
}

func TestTransferDirectoryPreflightFindsConflictBeforeAnyWrite(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "source", "middle"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(sourceRoot, "source", "a-new.txt"):         "不应写入",
		filepath.Join(sourceRoot, "source", "middle", "new.txt"): "也不应写入",
		filepath.Join(sourceRoot, "source", "z-conflict.txt"):    "新内容",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(targetRoot, "copied"), 0o755); err != nil {
		t.Fatal(err)
	}
	conflictPath := filepath.Join(targetRoot, "copied", "z-conflict.txt")
	if err := os.WriteFile(conflictPath, []byte("保留原内容"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceClient := newTransferTestClient(t, sourceRoot)
	targetClient := newTransferTestClient(t, targetRoot)
	progressCalls := 0

	stats, err := TransferBetweenClients(context.Background(), sourceClient, targetClient, "source", "copied", false, func(TransferStats) {
		progressCalls++
	})
	if err == nil || !strings.Contains(err.Error(), "未启用覆盖") {
		t.Fatalf("预检错误 = %v，期望发现后部目标文件冲突", err)
	}
	if stats != (TransferStats{}) || progressCalls != 0 {
		t.Fatalf("预检失败前不应产生进度：stats=%+v calls=%d", stats, progressCalls)
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "copied", "a-new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("预检失败前不应写入前部文件：%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "copied", "middle")); !os.IsNotExist(statErr) {
		t.Fatalf("预检失败前不应创建目录：%v", statErr)
	}
	assertFileContent(t, conflictPath, []byte("保留原内容"))
}

func TestTransferDirectoryOverwritePreflightFindsTypeConflictBeforeAnyWrite(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sourceRoot, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(sourceRoot, "source", "a-new.txt"):      "不应写入",
		filepath.Join(sourceRoot, "source", "z-conflict.txt"): "不能覆盖目录",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	conflictPath := filepath.Join(targetRoot, "copied", "z-conflict.txt")
	if err := os.MkdirAll(conflictPath, 0o755); err != nil {
		t.Fatal(err)
	}
	sourceClient := newTransferTestClient(t, sourceRoot)
	targetClient := newTransferTestClient(t, targetRoot)

	stats, err := TransferBetweenClients(context.Background(), sourceClient, targetClient, "source", "copied", true, nil)
	if err == nil || !strings.Contains(err.Error(), "类型冲突") {
		t.Fatalf("覆盖预检错误 = %v，期望发现目标类型冲突", err)
	}
	if stats != (TransferStats{}) {
		t.Fatalf("覆盖预检失败前不应产生进度：%+v", stats)
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "copied", "a-new.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("覆盖预检失败前不应写入其他文件：%v", statErr)
	}
}

func TestTransferBetweenClientsRejectsSymlink(t *testing.T) {
	sourceRoot := t.TempDir()
	targetRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(sourceRoot, "source"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceRoot, "outside.txt"), []byte("不能越界复制"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(sourceRoot, "outside.txt"), filepath.Join(sourceRoot, "source", "link.txt")); err != nil {
		t.Skipf("当前系统不能创建测试符号链接：%v", err)
	}
	sourceClient := newTransferTestClient(t, sourceRoot)
	targetClient := newTransferTestClient(t, targetRoot)

	_, err := TransferBetweenClients(context.Background(), sourceClient, targetClient, "source", "copied", false, nil)
	if err == nil || !strings.Contains(err.Error(), "符号链接") {
		t.Fatalf("符号链接错误 = %v，期望明确拒绝", err)
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "copied", "link.txt")); !os.IsNotExist(statErr) {
		t.Fatalf("符号链接不应出现在目标主机：%v", statErr)
	}
	if _, statErr := os.Stat(filepath.Join(targetRoot, "copied")); !os.IsNotExist(statErr) {
		t.Fatalf("目录预检失败前不应创建目标根目录：%v", statErr)
	}
}

func TestTransferBetweenClientsValidatesDangerousPaths(t *testing.T) {
	tests := []struct {
		name   string
		source string
		target string
	}{
		{name: "空源路径", source: " ", target: "target"},
		{name: "空目标路径", source: "source", target: ""},
		{name: "目标根目录", source: "source", target: "///"},
		{name: "目标当前目录", source: "source", target: "."},
		{name: "源路径越界", source: "../source", target: "target"},
		{name: "目标路径越界", source: "source", target: "../target"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, _, err := cleanTransferPaths(test.source, test.target); err == nil {
				t.Fatal("期望拒绝危险路径")
			}
		})
	}
	if !unsafeSameClientPath("/data", "/data", false) {
		t.Fatal("同一主机相同文件路径应被拒绝")
	}
	if !unsafeSameClientPath("/data", "/data/backup", true) {
		t.Fatal("同一主机目录复制到自身子目录应被拒绝")
	}
	if !unsafeSameClientPath("/data/source", "/data", true) {
		t.Fatal("同一主机目录复制到自身父目录应被拒绝")
	}
	if unsafeSameClientPath("/data/source", "/data/target", true) {
		t.Fatal("同一主机平级目录复制不应被拒绝")
	}
}

func TestTransferBetweenClientsHonorsCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := &ssh.Client{}
	_, err := TransferBetweenClients(ctx, client, client, "source", "target", false, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("取消错误 = %v，期望 context.Canceled", err)
	}
}

func newTransferTestClient(t *testing.T, root string) *ssh.Client {
	t.Helper()
	server := startSSHTestServer(t, &ssh.ServerConfig{NoClientAuth: true}, serveTransferSFTP(root))
	return dialSSHTestClient(t, server)
}

func serveTransferSFTP(root string) func(<-chan ssh.NewChannel) {
	return func(channels <-chan ssh.NewChannel) {
		var sessions sync.WaitGroup
		defer sessions.Wait()
		for newChannel := range channels {
			if newChannel.ChannelType() != "session" {
				_ = newChannel.Reject(ssh.UnknownChannelType, "仅支持 session")
				continue
			}
			channel, requests, err := newChannel.Accept()
			if err != nil {
				continue
			}
			sessions.Add(1)
			go func() {
				defer sessions.Done()
				defer channel.Close()
				for request := range requests {
					var payload struct{ Name string }
					if request.Type != "subsystem" || ssh.Unmarshal(request.Payload, &payload) != nil || payload.Name != "sftp" {
						_ = request.Reply(false, nil)
						continue
					}
					server, serverErr := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
					if serverErr != nil {
						_ = request.Reply(false, nil)
						return
					}
					_ = request.Reply(true, nil)
					_ = server.Serve()
					_ = server.Close()
					return
				}
			}()
		}
	}
}

func assertFileContent(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Fatalf("文件 %s 内容不正确", path)
	}
}
