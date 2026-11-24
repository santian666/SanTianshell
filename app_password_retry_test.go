package main

import (
	"errors"
	"testing"

	"santianshell/internal/remote"
)

func TestResolveCredentialPrefersTransientPassword(t *testing.T) {
	host := remote.Host{ID: "host-1", AuthType: "password", CredentialID: "credential-1", Password: "host-old"}
	app := &App{
		settings: Settings{Credentials: []Credential{{ID: "credential-1", Type: "password", Password: "credential-old"}}},
		transientPasswords: map[string]string{
			"host-1": "temporary-new",
		},
	}
	if password := app.resolveCredential(host).Password; password != "temporary-new" {
		t.Fatalf("临时密码未覆盖凭证密码: %q", password)
	}
	if password := app.resolveStoredCredential(host).Password; password != "credential-old" {
		t.Fatalf("配置编辑不应被临时密码覆盖: %q", password)
	}
	delete(app.transientPasswords, host.ID)
	if password := app.resolveCredential(host).Password; password != "credential-old" {
		t.Fatalf("清理临时密码后未恢复凭证密码: %q", password)
	}
}

func TestTemporaryHostUsesTransientPassword(t *testing.T) {
	host := remote.Host{ID: "quick-1", Address: "192.0.2.9", Username: "root", AuthType: "password", Temporary: true}
	app := &App{
		unlocked:           true,
		temporaryHosts:     map[string]remote.Host{host.ID: host},
		transientPasswords: map[string]string{host.ID: "temporary-new"},
	}
	resolved, err := app.host(host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Password != "temporary-new" {
		t.Fatalf("临时主机未使用本次连接密码: %q", resolved.Password)
	}
}

func TestPasswordRetryDetailsReportsSharedCredential(t *testing.T) {
	app := &App{settings: Settings{
		Hosts: []remote.Host{
			{ID: "host-1", CredentialID: "credential-1"},
			{ID: "host-2", CredentialID: "credential-1"},
			{ID: "host-deleted", CredentialID: "credential-1", Deleted: true},
		},
		Credentials: []Credential{{ID: "credential-1", Name: "生产密码", Type: "password"}},
	}}
	details := app.passwordRetryDetails("host-1")
	if !details.allowPermanent || details.credentialName != "生产密码" || details.affectedHosts != 2 {
		t.Fatalf("共享凭证提示信息错误: %#v", details)
	}
	app.temporaryHosts = map[string]remote.Host{"quick-1": {ID: "quick-1", Temporary: true}}
	if details := app.passwordRetryDetails("quick-1"); details.allowPermanent || details.affectedHosts != 0 {
		t.Fatalf("临时主机错误允许永久保存: %#v", details)
	}
}

func TestPersistSuccessfulTerminalPasswordUpdatesHost(t *testing.T) {
	store := &memorySettingsStore{}
	host := remote.Host{ID: "host-1", Address: "192.0.2.10", Port: 22, Username: "root", AuthType: "password", Password: "old"}
	app := &App{
		store:              store,
		settings:           Settings{Hosts: []remote.Host{host}},
		transientPasswords: map[string]string{host.ID: "new"},
	}
	attempted := host
	attempted.Password = "new"
	if err := app.persistSuccessfulTerminalPassword(host.ID, attempted, "new"); err != nil {
		t.Fatal(err)
	}
	if got := app.settings.Hosts[0].Password; got != "new" {
		t.Fatalf("主机密码未永久更新: %q", got)
	}
	if _, exists := app.transientPasswords[host.ID]; exists {
		t.Fatal("永久保存成功后仍保留当前主机临时密码")
	}
	if store.saves != 1 {
		t.Fatalf("配置保存次数 = %d，期望 1", store.saves)
	}
}

func TestPersistSuccessfulTerminalPasswordFailureKeepsSessionOverride(t *testing.T) {
	store := &memorySettingsStore{err: errors.New("磁盘写入失败")}
	host := remote.Host{ID: "host-1", Address: "192.0.2.10", Port: 22, Username: "root", AuthType: "password", Password: "old"}
	app := &App{
		store:              store,
		settings:           Settings{Hosts: []remote.Host{host}},
		transientPasswords: map[string]string{host.ID: "new"},
	}
	attempted := host
	attempted.Password = "new"
	if err := app.persistSuccessfulTerminalPassword(host.ID, attempted, "new"); err == nil {
		t.Fatal("配置保存失败时错误返回 nil")
	}
	if got := app.settings.Hosts[0].Password; got != "old" {
		t.Fatalf("保存失败后内存配置未回滚: %q", got)
	}
	if got := app.transientPasswords[host.ID]; got != "new" {
		t.Fatalf("保存失败后本次会话密码丢失: %q", got)
	}
}

func TestPersistSharedCredentialProtectsOtherConnectedShell(t *testing.T) {
	store := &memorySettingsStore{}
	current := remote.Host{ID: "host-1", Address: "192.0.2.11", Port: 22, Username: "root", AuthType: "password", CredentialID: "credential-1"}
	other := remote.Host{ID: "host-2", Address: "192.0.2.12", Port: 22, Username: "root", AuthType: "password", CredentialID: "credential-1"}
	app := &App{
		store:              store,
		settings:           Settings{Hosts: []remote.Host{current, other}, Credentials: []Credential{{ID: "credential-1", Type: "password", Password: "old"}}},
		shells:             map[string]*remote.Shell{other.ID: {}},
		transientPasswords: map[string]string{current.ID: "new"},
	}
	attempted := current
	attempted.Password = "new"
	if err := app.persistSuccessfulTerminalPassword(current.ID, attempted, "new"); err != nil {
		t.Fatal(err)
	}
	if got := app.settings.Credentials[0].Password; got != "new" {
		t.Fatalf("共享凭证密码未更新: %q", got)
	}
	if _, exists := app.transientPasswords[current.ID]; exists {
		t.Fatal("当前主机永久保存后仍保留临时密码")
	}
	if got := app.transientPasswords[other.ID]; got != "old" {
		t.Fatalf("其他已连接主机未保留旧密码: %q", got)
	}
	if got := app.resolveCredential(other).Password; got != "old" {
		t.Fatalf("其他已连接主机解析成新密码，可能导致连接被关闭: %q", got)
	}
}

func TestCloseTerminalClearsTransientPassword(t *testing.T) {
	app := &App{
		unlocked:           true,
		shells:             map[string]*remote.Shell{},
		shellEpochs:        map[string]uint64{},
		shellClientEpochs:  map[string]int{},
		shellHostRevisions: map[string]string{},
		transientPasswords: map[string]string{"host-1": "temporary"},
		connections:        remote.NewConnectionManager(),
	}
	app.CloseTerminal("host-1")
	if _, exists := app.transientPasswords["host-1"]; exists {
		t.Fatal("显式关闭终端后仍保留本次连接密码")
	}
}

func TestCacheConfirmedHostKeyForPasswordRetry(t *testing.T) {
	confirmations := 0
	host := cacheConfirmedHostKeyForRetry(remote.Host{ConfirmHostKey: func(string) error {
		confirmations++
		return nil
	}})
	if err := host.ConfirmHostKey("SHA256:first"); err != nil {
		t.Fatal(err)
	}
	if err := host.ConfirmHostKey("SHA256:first"); err != nil {
		t.Fatal(err)
	}
	if err := host.ConfirmHostKey("SHA256:changed"); err != nil {
		t.Fatal(err)
	}
	if confirmations != 2 {
		t.Fatalf("相同指纹在一次密码重试中重复确认，确认次数 = %d", confirmations)
	}
}

func TestTerminalStartFailureClassifiesPasswordRejection(t *testing.T) {
	err := &remote.ConnectionError{Stage: remote.StageAuthentication, Err: &remote.PasswordRejectedError{Err: errors.New("password rejected")}}
	result := terminalStartFailure(err, true)
	if result.ErrorCode != "password_auth_failed" || !result.PasswordPrompted || result.Connected {
		t.Fatalf("密码错误结构化结果错误: %#v", result)
	}
}

func TestPasswordRetryRejectsExpiredTerminalEpoch(t *testing.T) {
	app := &App{shellEpochs: map[string]uint64{"host-1": 2}}
	err := app.startTerminalWithHostGuarded("host-1", 7, remote.Host{ID: "host-1"}, 1, nil)
	if err == nil || err.Error() != "SSH 连接已取消" {
		t.Fatalf("过期密码重试未被拒绝: %v", err)
	}
	if app.shellEpochs["host-1"] != 2 {
		t.Fatalf("过期密码重试修改了连接代次: %d", app.shellEpochs["host-1"])
	}
}
