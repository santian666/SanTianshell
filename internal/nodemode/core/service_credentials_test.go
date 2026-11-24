package core

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestIntegratedServiceMigratesPlaintextVPSCredentials(t *testing.T) {
	dataDir := t.TempDir()
	standalone := NewServiceWithDataDir(dataDir)
	legacy := VPS{
		Alias:         "UK-DO",
		Address:       "192.0.2.19",
		Port:          22,
		Username:      "root",
		Password:      "legacy-ssh-secret",
		ProxyHost:     "127.0.0.1",
		ProxyPort:     20808,
		ProxyUsername: "proxy-user",
		ProxyPassword: "legacy-proxy-secret",
	}
	if err := standalone.SaveVPS(legacy); err != nil {
		t.Fatal(err)
	}

	connectorErr := errors.New("统一连接器已调用")
	connectorCalled := false
	connector := func(vps VPS, _ int) (*ssh.Client, func(), error) {
		connectorCalled = true
		if vps.Alias != legacy.Alias || vps.Password != "" || vps.ProxyPassword != "" {
			t.Fatalf("融合模式仍向节点连接器传递明文凭证: %#v", vps)
		}
		return nil, nil, connectorErr
	}
	integrated := NewServiceWithConnectors(dataDir, connector, connector)
	overview, err := integrated.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.VPSList) != 1 || overview.VPSList[0].Password != "" || overview.VPSList[0].ProxyPassword != "" {
		t.Fatalf("融合模式向前端返回了凭证: %#v", overview.VPSList)
	}

	stored, err := os.ReadFile(integrated.sshPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range [][]byte{[]byte(legacy.Password), []byte(legacy.ProxyPassword)} {
		if bytes.Contains(stored, secret) {
			t.Fatalf("ssh.json 仍包含旧明文凭证: %s", stored)
		}
	}
	if bytes.Contains(stored, []byte(`"password"`)) || bytes.Contains(stored, []byte(`"proxy_password"`)) {
		t.Fatalf("融合模式不应继续持久化凭证字段: %s", stored)
	}

	result := integrated.RefreshVPSState(legacy.Alias, false)
	if !connectorCalled || result.Message != connectorErr.Error() {
		t.Fatalf("清理凭证后未继续使用统一连接器: called=%v result=%#v", connectorCalled, result)
	}
}

func TestIntegratedSaveVPSDoesNotPersistCredentials(t *testing.T) {
	connector := func(VPS, int) (*ssh.Client, func(), error) {
		return nil, nil, errors.New("不应连接")
	}
	service := NewServiceWithConnectors(t.TempDir(), connector, connector)
	if err := service.SaveVPS(VPS{
		HostID:        "host-sg",
		Alias:         "SG",
		Address:       "192.0.2.20",
		Port:          22,
		Username:      "root",
		Password:      "new-ssh-secret",
		ProxyPassword: "new-proxy-secret",
	}); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(service.sshPath())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(stored, []byte("new-ssh-secret")) || bytes.Contains(stored, []byte("new-proxy-secret")) {
		t.Fatalf("SaveVPS 写入了明文凭证: %s", stored)
	}
	if !bytes.Contains(stored, []byte(`"hostId":"host-sg"`)) {
		t.Fatalf("SaveVPS 未持久化 Shell 主机绑定: %s", stored)
	}
}

func TestStandaloneServiceStillPersistsCredentials(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	want := VPS{Alias: "standalone", Address: "192.0.2.21", Port: 22, Username: "root", Password: "ssh-secret", ProxyPassword: "proxy-secret"}
	if err := service.SaveVPS(want); err != nil {
		t.Fatal(err)
	}
	list, err := service.loadVPSList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Password != want.Password || list[0].ProxyPassword != want.ProxyPassword {
		t.Fatalf("独立模式凭证兼容性被破坏: %#v", list)
	}
}
