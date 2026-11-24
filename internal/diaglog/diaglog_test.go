package diaglog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRotatesOversizedLog(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "app.log")
	if err := os.WriteFile(path, make([]byte, maxLogSize), 0600); err != nil {
		t.Fatal(err)
	}
	file, err := Open(directory)
	if err != nil {
		t.Fatal(err)
	}
	if err = file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(path + ".1"); err != nil {
		t.Fatalf("轮转日志不存在：%v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 0 {
		t.Fatalf("新日志大小 = %d，期望 0", info.Size())
	}
}
