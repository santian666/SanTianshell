package sessionlog

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"
)

func TestLoggerCleansANSIAndControls(t *testing.T) {
	logger, err := newLogger(t.TempDir(), SessionInfo{
		Name:     "测试主机",
		Address:  "192.0.2.10",
		Port:     22,
		Username: "root",
	}, 4096, time.Date(2026, 8, 15, 9, 30, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}

	parts := [][]byte{
		[]byte("普通\x1b[31"),
		[]byte("m红色\x1b[0m\n"),
		[]byte("标题\x1b]0;秘密标题"),
		[]byte("\x07完成\tOK\r\b\x00\n"),
		[]byte("链接\x1b]8;;https://example.invalid/secret\x1b\\正文\n"),
		{0xe4, 0xb8},
		{0xad, 0xc2, 0x85, '\n'},
	}
	for _, part := range parts {
		if _, err := logger.Write(part); err != nil {
			t.Fatal(err)
		}
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	content := readFile(t, logger.Path())
	if !utf8.Valid(content) {
		t.Fatal("日志不是有效 UTF-8")
	}
	if strings.Contains(string(content), "\x1b") || strings.Contains(string(content), "秘密标题") || strings.Contains(string(content), "example.invalid") {
		t.Fatalf("ANSI/OSC 未清理: %q", content)
	}
	if !strings.Contains(string(content), "普通红色\n标题完成\tOK\n链接正文\n中\n") {
		t.Fatalf("日志正文不符合预期: %q", content)
	}
	if !strings.Contains(string(content), "主机: 测试主机") || !strings.Contains(string(content), "地址: 192.0.2.10:22") {
		t.Fatalf("缺少主机信息: %q", content)
	}
	if _, err := logger.Write([]byte("关闭后写入")); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("关闭后写入错误 = %v", err)
	}
	if err := logger.Close(); err != nil {
		t.Fatalf("重复关闭失败: %v", err)
	}
}

func TestLoggerConcurrentWrites(t *testing.T) {
	logger, err := newLogger(t.TempDir(), SessionInfo{Name: "并发主机"}, 1<<20, time.Now())
	if err != nil {
		t.Fatal(err)
	}

	const count = 200
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			line := fmt.Sprintf("并发行-%03d\n", index)
			if _, writeErr := logger.Write([]byte(line)); writeErr != nil {
				t.Errorf("并发写入失败: %v", writeErr)
			}
		}(i)
	}
	wg.Wait()
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	content := string(readFile(t, logger.Path()))
	for i := 0; i < count; i++ {
		line := fmt.Sprintf("并发行-%03d\n", i)
		if strings.Count(content, line) != 1 {
			t.Fatalf("%q 出现次数不是 1", line)
		}
	}
}

func TestLoggerRotatesAndKeepsFilesWithinDirectory(t *testing.T) {
	dir := t.TempDir()
	info := SessionInfo{Name: `..\\CON:<非法>/名称?* 很长`, Address: "203.0.113.8", Username: "root"}
	logger, err := newLogger(dir, info, 512, time.Date(2026, 8, 15, 10, 0, 0, 0, time.Local))
	if err != nil {
		t.Fatal(err)
	}

	payload := bytes.Repeat([]byte("一行终端输出\n"), 120)
	if n, err := logger.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("写入结果 n=%d err=%v", n, err)
	}
	if err := logger.Close(); err != nil {
		t.Fatal(err)
	}

	assertInsideDirectory(t, dir, logger.Path())
	assertInsideDirectory(t, dir, logger.RotatedPath())
	base := filepath.Base(logger.Path())
	if strings.ContainsAny(base, `<>:"/\|?*`) || strings.Contains(base, "..") {
		t.Fatalf("文件名不安全: %q", base)
	}
	for _, path := range []string{logger.Path(), logger.RotatedPath()} {
		stat, err := os.Stat(path)
		if err != nil {
			t.Fatalf("轮转文件不存在 %s: %v", path, err)
		}
		if stat.Size() > 512 {
			t.Fatalf("文件 %s 超过限制: %d", path, stat.Size())
		}
		if content := readFile(t, path); !utf8.Valid(content) {
			t.Fatalf("轮转文件不是有效 UTF-8: %s", path)
		}
	}
}

func TestReservedAndEmptyFileNames(t *testing.T) {
	for _, test := range []struct {
		name string
		want string
	}{
		{name: "CON", want: "_CON"},
		{name: "lpt9.txt", want: "_lpt9.txt"},
		{name: "../..", want: "_"},
		{name: "", want: ""},
	} {
		if got := safeFileName(test.name); got != test.want {
			t.Errorf("safeFileName(%q) = %q, want %q", test.name, got, test.want)
		}
	}
}

func assertInsideDirectory(t *testing.T, dir, path string) {
	t.Helper()
	absDir, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		t.Fatalf("路径越界: dir=%s path=%s rel=%s err=%v", absDir, absPath, rel, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}
