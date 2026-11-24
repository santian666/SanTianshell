package store

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCryptRoundTripAndEmptyInput(t *testing.T) {
	plain := []byte("Windows 10/11 DPAPI 测试-敏感配置")
	sealed, err := crypt(plain, true)
	if err != nil {
		t.Fatalf("DPAPI 加密失败: %v", err)
	}
	if len(sealed) == 0 || bytes.Equal(sealed, plain) {
		t.Fatal("DPAPI 未生成有效密文")
	}
	opened, err := crypt(sealed, false)
	if err != nil {
		t.Fatalf("DPAPI 解密失败: %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("DPAPI 往返结果 = %q，期望 %q", opened, plain)
	}
	for _, encrypt := range []bool{false, true} {
		result, err := crypt(nil, encrypt)
		if err != nil || len(result) != 0 {
			t.Fatalf("空输入处理失败: encrypt=%v result=%v err=%v", encrypt, result, err)
		}
	}
}

func TestStoreSaveReplacesExistingSettingsOnWindows(t *testing.T) {
	type settings struct {
		Name     string `json:"name"`
		Password string `json:"password"`
	}
	path := filepath.Join(t.TempDir(), "settings.dat")
	store := &Store{path: path}
	if err := store.Save(settings{Name: "第一次", Password: "旧密码"}); err != nil {
		t.Fatalf("首次保存失败: %v", err)
	}
	want := settings{Name: "第二次", Password: "新密码"}
	if err := store.Save(want); err != nil {
		t.Fatalf("覆盖保存失败: %v", err)
	}
	var got settings
	if err := store.Load(&got); err != nil {
		t.Fatalf("读取覆盖后的设置失败: %v", err)
	}
	if got != want {
		t.Fatalf("读取结果 = %#v，期望 %#v", got, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(want.Password)) {
		t.Fatal("设置文件包含明文密码")
	}
	if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("原子保存临时文件未清理: %v", err)
	}
}

func TestCryptRejectsCorruptedCiphertext(t *testing.T) {
	if _, err := crypt([]byte("not-dpapi-data"), false); err == nil {
		t.Fatal("损坏的 DPAPI 密文未被拒绝")
	}
}
