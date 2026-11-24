package quickconnect

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestReadOpenSSHConfig(t *testing.T) {
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(filepath.Join(sshDir, "conf.d"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sshDir, "config"), `
User global-user
Host prod *.internal
    HostName 2001:db8::5
    Port 2222
    IdentityFile "~/.ssh/id prod"
Include conf.d/*.conf
Host other
    HostName other.example.com
Host *
    Port 2200
`)
	writeTestFile(t, filepath.Join(sshDir, "conf.d", "10-staging.conf"), `
Host staging
    HostName staging.example.com
    User deploy
    IdentityFile ~/.ssh/id_ed25519
`)

	hosts, err := ReadOpenSSHConfig(filepath.Join(sshDir, "config"))
	if err != nil {
		t.Fatalf("ReadOpenSSHConfig() error = %v", err)
	}
	want := []SSHConfigHost{
		{Alias: "prod", HostName: "2001:db8::5", User: "global-user", Port: 2222, IdentityFile: "~/.ssh/id prod"},
		{Alias: "staging", HostName: "staging.example.com", User: "global-user", Port: 2200, IdentityFile: "~/.ssh/id_ed25519"},
		{Alias: "other", HostName: "other.example.com", User: "global-user", Port: 2200},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("ReadOpenSSHConfig() = %#v, want %#v", hosts, want)
	}
}

func TestLoadOpenSSHConfigUsesUserProfile(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sshDir, "config"), "Host profile-host\n  User root\n")
	t.Setenv("USERPROFILE", home)

	hosts, err := LoadOpenSSHConfig()
	if err != nil {
		t.Fatalf("LoadOpenSSHConfig() error = %v", err)
	}
	want := []SSHConfigHost{{Alias: "profile-host", HostName: "profile-host", User: "root", Port: 22}}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("LoadOpenSSHConfig() = %#v, want %#v", hosts, want)
	}
}

func TestReadOpenSSHConfigIncludeCycle(t *testing.T) {
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sshDir, "config"), "Include child.conf\nHost main\n")
	writeTestFile(t, filepath.Join(sshDir, "child.conf"), "Include config\nHost child\n")

	hosts, err := ReadOpenSSHConfig(filepath.Join(sshDir, "config"))
	if err != nil {
		t.Fatalf("ReadOpenSSHConfig() error = %v", err)
	}
	want := []SSHConfigHost{
		{Alias: "child", HostName: "child", Port: 22},
		{Alias: "main", HostName: "main", Port: 22},
	}
	if !reflect.DeepEqual(hosts, want) {
		t.Fatalf("ReadOpenSSHConfig() = %#v, want %#v", hosts, want)
	}
}

func TestReadOpenSSHConfigRejectsIncludeOutsideSSHDirectory(t *testing.T) {
	base := t.TempDir()
	sshDir := filepath.Join(base, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(base, "outside.conf"), "Host outside\n")
	writeTestFile(t, filepath.Join(sshDir, "config"), "Include ../outside.conf\n")

	_, err := ReadOpenSSHConfig(filepath.Join(sshDir, "config"))
	if err == nil || !strings.Contains(err.Error(), "超出 .ssh 目录") {
		t.Fatalf("ReadOpenSSHConfig() error = %v, want outside-directory error", err)
	}
}

func TestReadOpenSSHConfigRejectsSymlinkOutsideSSHDirectory(t *testing.T) {
	base := t.TempDir()
	sshDir := filepath.Join(base, ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(base, "outside.conf")
	writeTestFile(t, outside, "Host outside\n")
	link := filepath.Join(sshDir, "linked.conf")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("当前环境不能创建符号链接: %v", err)
	}
	writeTestFile(t, filepath.Join(sshDir, "config"), "Include linked.conf\n")

	_, err := ReadOpenSSHConfig(filepath.Join(sshDir, "config"))
	if err == nil || !strings.Contains(err.Error(), "超出 .ssh 目录") {
		t.Fatalf("ReadOpenSSHConfig() error = %v, want symlink escape error", err)
	}
}

func TestReadOpenSSHConfigRejectsControlCharacter(t *testing.T) {
	sshDir := filepath.Join(t.TempDir(), ".ssh")
	if err := os.MkdirAll(sshDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(sshDir, "config"), "Host safe\x00bad\n")

	if _, err := ReadOpenSSHConfig(filepath.Join(sshDir, "config")); err == nil {
		t.Fatal("ReadOpenSSHConfig() want control-character error")
	}
}

func writeTestFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
