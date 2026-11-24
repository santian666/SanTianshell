package nodemode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDataDirectoryDevelopmentUsesProjectData(t *testing.T) {
	root := t.TempDir()
	projectDirectory := filepath.Join(root, "project")
	if err := os.MkdirAll(projectDirectory, 0700); err != nil {
		t.Fatal(err)
	}

	directory, warning := resolveDataDirectory(dataDirectoryOptions{
		workingDirectory:    projectDirectory,
		userConfigDirectory: filepath.Join(root, "config"),
		userHomeDirectory:   filepath.Join(root, "home"),
		tempDirectory:       filepath.Join(root, "temp"),
	})
	want := filepath.Join(projectDirectory, "data", "node-mode")
	if !sameDirectory(directory, want) {
		t.Fatalf("开发环境数据目录 = %q，期望 %q", directory, want)
	}
	if warning != "" {
		t.Fatalf("开发目录可写时不应产生警告: %s", warning)
	}
	assertDirectoryWritable(t, directory)
}

func TestResolveDataDirectoryProductionDoesNotImportNearbyData(t *testing.T) {
	root := t.TempDir()
	workingDirectory := filepath.Join(root, "working")
	executableDirectory := filepath.Join(root, "installed", "build", "bin")
	workingLegacyDirectory := filepath.Join(workingDirectory, "data", "node-mode")
	ancestorLegacyDirectory := filepath.Join(root, "installed", "data", "node-mode")
	executableLegacyDirectory := filepath.Join(executableDirectory, "data", "node-mode")
	if err := os.MkdirAll(workingLegacyDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workingLegacyDirectory, "ssh.json"), []byte("旧主机"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(ancestorLegacyDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ancestorLegacyDirectory, "sbconfig.json"), []byte("旧配置"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(executableLegacyDirectory, "bk"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(executableLegacyDirectory, "bk", "backup.json"), []byte("备份"), 0600); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(root, "config")
	destination := filepath.Join(configRoot, "SanTianShell", "node-mode")

	directory, warning := resolveDataDirectory(dataDirectoryOptions{
		production:          true,
		workingDirectory:    workingDirectory,
		userConfigDirectory: configRoot,
		userHomeDirectory:   filepath.Join(root, "home"),
		tempDirectory:       filepath.Join(root, "temp"),
	})
	if !sameDirectory(directory, destination) {
		t.Fatalf("发布环境数据目录 = %q，期望 %q", directory, destination)
	}
	if warning != "" {
		t.Fatalf("正常数据目录不应产生警告: %s", warning)
	}
	assertDirectoryWritable(t, destination)
	assertPathMissing(t, filepath.Join(destination, "ssh.json"))
	assertPathMissing(t, filepath.Join(destination, "sbconfig.json"))
	assertPathMissing(t, filepath.Join(destination, "bk", "backup.json"))
}

func TestResolveDataDirectoryProductionFallsBackToWritableUserHome(t *testing.T) {
	root := t.TempDir()
	blockedConfigRoot := filepath.Join(root, "blocked-config")
	if err := os.WriteFile(blockedConfigRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	homeDirectory := filepath.Join(root, "home")

	directory, warning := resolveDataDirectory(dataDirectoryOptions{
		production:          true,
		userConfigDirectory: blockedConfigRoot,
		userHomeDirectory:   homeDirectory,
		tempDirectory:       filepath.Join(root, "temp"),
	})
	want := filepath.Join(homeDirectory, ".santianshell", "node-mode")
	if !sameDirectory(directory, want) {
		t.Fatalf("回退数据目录 = %q，期望 %q", directory, want)
	}
	if !strings.Contains(warning, "用户配置目录不可写") || !strings.Contains(warning, "节点数据已使用可写目录") {
		t.Fatalf("回退警告不明确: %s", warning)
	}
	assertDirectoryWritable(t, directory)
}

func TestResolveDataDirectoryProductionNeverWritesLegacyInstallDirectory(t *testing.T) {
	root := t.TempDir()
	blockedConfigRoot := filepath.Join(root, "blocked-config")
	blockedHomeRoot := filepath.Join(root, "blocked-home")
	if err := os.WriteFile(blockedConfigRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockedHomeRoot, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	workingDirectory := filepath.Join(root, "portable")
	legacyDirectory := filepath.Join(workingDirectory, "data", "node-mode")
	if err := os.MkdirAll(legacyDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyDirectory, "ssh.json"), []byte("[]"), 0600); err != nil {
		t.Fatal(err)
	}
	tempRoot := filepath.Join(root, "temp")

	directory, warning := resolveDataDirectory(dataDirectoryOptions{
		production:          true,
		workingDirectory:    workingDirectory,
		userConfigDirectory: blockedConfigRoot,
		userHomeDirectory:   blockedHomeRoot,
		tempDirectory:       tempRoot,
	})
	want := filepath.Join(tempRoot, "SanTianShell", "node-mode")
	if !sameDirectory(directory, want) {
		t.Fatalf("发布环境回退目录 = %q，期望临时目录 %q", directory, want)
	}
	if sameDirectory(directory, legacyDirectory) {
		t.Fatal("发布环境不应把安装目录作为运行数据写入位置")
	}
	if !strings.Contains(warning, "节点数据已使用可写目录") || !strings.Contains(strings.ToLower(warning), strings.ToLower(filepath.Clean(want))) {
		t.Fatalf("回退警告不明确：%s", warning)
	}
	assertPathMissing(t, filepath.Join(directory, "ssh.json"))
}

func TestEnsureWritableDirectoryRejectsFilePath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := ensureWritableDirectory(path); err == nil {
		t.Fatal("文件路径不应被识别为可写数据目录")
	}
}

func TestValidateExportAllJSONRequiresUsableRuntimeSections(t *testing.T) {
	valid := []byte("{\"inbounds\":[{\"type\":\"vless\",\"tag\":\"node\",\"listen_port\":20000}],\"outbounds\":[{\"type\":\"direct\",\"tag\":\"node-out\"}],\"route\":{\"rules\":[{\"inbound\":[\"node\"],\"outbound\":\"node-out\"}]}}")
	if err := validateExportAllJSON(valid); err != nil {
		t.Fatalf("有效 all.json 不应被拒绝: %v", err)
	}
	invalid := []struct {
		name string
		data string
	}{
		{name: "empty inbounds", data: "{\"inbounds\":[],\"outbounds\":[{\"type\":\"direct\"}],\"route\":{\"rules\":[]}}"},
		{name: "missing route", data: "{\"inbounds\":[{\"type\":\"vless\",\"tag\":\"node\"}],\"outbounds\":[{\"type\":\"direct\"}]}"},
		{name: "missing inbound type", data: "{\"inbounds\":[{\"tag\":\"node\"}],\"outbounds\":[{\"type\":\"direct\"}],\"route\":{\"rules\":[]}}"},
		{name: "missing inbound port", data: "{\"inbounds\":[{\"type\":\"vless\",\"tag\":\"node\"}],\"outbounds\":[{\"type\":\"direct\",\"tag\":\"node-out\"}],\"route\":{\"rules\":[{\"inbound\":[\"node\"],\"outbound\":\"node-out\"}]}}"},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			if err := validateExportAllJSON([]byte(item.data)); err == nil {
				t.Fatalf("无效 all.json 应被拒绝")
			}
		})
	}
}

func TestWriteJSONExportBytesAddsExtensionAndReplacesExistingFile(t *testing.T) {
	directory := t.TempDir()
	path, err := writeJSONExportBytes(filepath.Join(directory, "AK-日本_all"), []byte("{\"ok\":true}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Ext(path) != ".json" {
		t.Fatalf("导出路径扩展名 = %q", filepath.Ext(path))
	}
	assertFileContent(t, path, "{\"ok\":true}\n")
	if _, err := writeJSONExportBytes(path, []byte("{\"ok\":false}\n")); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, path, "{\"ok\":false}\n")
	leftovers, err := filepath.Glob(filepath.Join(directory, ".*.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("不应遗留临时 JSON 文件: %v", leftovers)
	}
}

func TestSafeJSONExportNameRemovesWindowsInvalidCharacters(t *testing.T) {
	if got := safeJSONExportName("AK:/日本?一"); got != "AK__日本_一" {
		t.Fatalf("安全文件名 = %q", got)
	}
	if got := safeJSONExportName(" ... "); got != "vps" {
		t.Fatalf("空文件名应回退为 vps，得到 %q", got)
	}
}

func assertDirectoryWritable(t *testing.T, directory string) {
	t.Helper()
	path := filepath.Join(directory, "write-check")
	if err := os.WriteFile(path, []byte("ok"), 0600); err != nil {
		t.Fatalf("目录不可写 %q: %v", directory, err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %q 失败: %v", path, err)
	}
	if string(content) != want {
		t.Fatalf("%q 内容 = %q，期望 %q", path, content, want)
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("不应迁移临时文件 %q，stat 错误: %v", path, err)
	}
}
