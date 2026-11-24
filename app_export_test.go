package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"santianshell/internal/remote"
)

func TestHostExportBaseName(t *testing.T) {
	tests := []struct {
		name string
		host remote.Host
		want string
	}{
		{name: "主机名称", host: remote.Host{Name: " UK:DO? "}, want: "UK-DO-"},
		{name: "VPS 别名回退", host: remote.Host{VPSAlias: "SG"}, want: "SG"},
		{name: "地址回退", host: remote.Host{Address: "192.0.2.1"}, want: "192.0.2.1"},
		{name: "Windows 保留名", host: remote.Host{Name: "CON"}, want: "_CON"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := hostExportBaseName(test.host); got != test.want {
				t.Fatalf("文件基础名 = %q，期望 %q", got, test.want)
			}
		})
	}
}

func TestWriteHostExportFilesCreatesOneObjectPerHost(t *testing.T) {
	directory := t.TempDir()
	existingPath := filepath.Join(directory, "UK-DO.json")
	if err := os.WriteFile(existingPath, []byte("保留原文件"), 0600); err != nil {
		t.Fatal(err)
	}
	hosts := []remote.Host{
		{ID: "1", Name: "UK-DO", Address: "192.0.2.10", Port: 22, RDPGatewayPassword: "Gateway-Secret"},
		{ID: "2", Name: "UK-DO", Address: "192.0.2.11", Port: 22},
	}
	if err := writeHostExportFiles(directory, hosts); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(existingPath); err != nil || string(raw) != "保留原文件" {
		t.Fatalf("已有文件被覆盖：内容 %q，错误 %v", raw, err)
	}
	for index, filename := range []string{"UK-DO-2.json", "UK-DO-3.json"} {
		raw, err := os.ReadFile(filepath.Join(directory, filename))
		if err != nil {
			t.Fatalf("读取 %s 失败：%v", filename, err)
		}
		if strings.HasPrefix(strings.TrimSpace(string(raw)), "[") {
			t.Fatalf("%s 不应导出为主机数组", filename)
		}
		if strings.Contains(string(raw), "Gateway-Secret") {
			t.Fatalf("%s 包含 RD Gateway 密码", filename)
		}
		var host remote.Host
		if err := json.Unmarshal(raw, &host); err != nil {
			t.Fatalf("解析 %s 失败：%v", filename, err)
		}
		if host.ID != hosts[index].ID || host.Address != hosts[index].Address {
			t.Fatalf("%s 主机内容不匹配：%+v", filename, host)
		}
	}
}

func TestCollectJSONFilesRecursive(t *testing.T) {
	directory := t.TempDir()
	nested := filepath.Join(directory, "子目录")
	if err := os.MkdirAll(nested, 0700); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{
		filepath.Join(directory, "A.json"),
		filepath.Join(nested, "B.JSON"),
		filepath.Join(nested, "忽略.txt"),
	} {
		if err := os.WriteFile(path, []byte("{}"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := collectJSONFiles(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || filepath.Base(paths[0]) != "A.json" || filepath.Base(paths[1]) != "B.JSON" {
		t.Fatalf("递归 JSON 文件结果不正确：%v", paths)
	}
}
