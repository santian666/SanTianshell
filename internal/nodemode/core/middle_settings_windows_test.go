//go:build windows

package core

import (
	"bytes"
	"testing"
)

func TestMiddleSettingsDPAPIHandlesEmptyInput(t *testing.T) {
	for _, operation := range []struct {
		name string
		run  func([]byte) ([]byte, error)
	}{
		{name: "加密", run: protectMiddleSettings},
		{name: "解密", run: unprotectMiddleSettings},
	} {
		t.Run(operation.name, func(t *testing.T) {
			result, err := operation.run(nil)
			if err != nil || len(result) != 0 {
				t.Fatalf("空输入结果 = %v，错误 = %v", result, err)
			}
		})
	}
}

func TestMiddleSettingsDPAPIRoundTrip(t *testing.T) {
	plain := []byte("Windows DPAPI 中转配置")
	sealed, err := protectMiddleSettings(plain)
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if bytes.Equal(sealed, plain) {
		t.Fatal("中转配置未被加密")
	}
	opened, err := unprotectMiddleSettings(sealed)
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if !bytes.Equal(opened, plain) {
		t.Fatalf("解密结果 = %q，期望 %q", opened, plain)
	}
}
