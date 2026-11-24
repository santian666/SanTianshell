package main

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"santianshell/internal/aiagent"
	"santianshell/internal/remote"
)

func TestSaveTemporaryHostPreservesIDAndMakesPersistent(t *testing.T) {
	store := &memorySettingsStore{}
	temporary := remote.Host{ID: "temporary-1", Name: "临时主机", Address: "192.0.2.11", Username: "root", Protocol: "ssh", Temporary: true}
	app := &App{
		store:          store,
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{}},
		temporaryHosts: map[string]remote.Host{temporary.ID: temporary},
	}
	settings, err := app.SaveHost(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 1 || settings.Hosts[0].ID != temporary.ID || settings.Hosts[0].Temporary {
		t.Fatalf("临时主机持久化结果错误: %#v", settings.Hosts)
	}
	if _, exists := app.temporaryHosts[temporary.ID]; exists {
		t.Fatal("持久化后临时主机仍保留在 temporaryHosts")
	}
}

func TestSupportsLocalProxyQuickLatency(t *testing.T) {
	for _, host := range []remote.Host{
		{ProxyEnabled: true, ProxyHost: "127.0.0.1", ProxyPort: 20808},
		{ProxyEnabled: true, ProxyHost: "localhost", ProxyPort: 20808},
		{ProxyEnabled: true},
	} {
		if !supportsLocalProxyQuickLatency(host) {
			t.Fatalf("应识别本地快速代理配置: %#v", host)
		}
	}
	if supportsLocalProxyQuickLatency(remote.Host{ProxyEnabled: true, ProxyHost: "192.0.2.10", ProxyPort: 1080}) {
		t.Fatal("普通 SOCKS5 代理不应使用 SanTian-Singbox 快速延迟")
	}
}

func TestCreateQuickHostCandidateKeepsPasswordInBackend(t *testing.T) {
	app := &App{unlocked: true, temporaryHosts: map[string]remote.Host{}}
	host, err := app.CreateQuickHostCandidate("192.0.2.29 root secret ----UK-DO", "option-1")
	if err != nil {
		t.Fatal(err)
	}
	if host.Name != "UK-DO" || host.Username != "root" || host.Port != 22 || !host.Temporary {
		t.Fatalf("快速连接主机字段错误: %#v", host)
	}
	if host.Password != "" {
		t.Fatal("临时密码不应返回前端")
	}
	if stored := app.temporaryHosts[host.ID]; stored.Password != "secret" {
		t.Fatalf("后端未保留临时连接密码: %#v", stored)
	}
}

func TestSaveConnectedTemporaryHostUsesVerifiedPassword(t *testing.T) {
	store := &memorySettingsStore{}
	host := remote.Host{
		ID: "quick-connected", Name: "UK-DO", Address: "192.0.2.30", Port: 22,
		Username: "root", Protocol: "ssh", AuthType: "password", Password: "old", Temporary: true,
		HostKey: "SHA256:trusted",
	}
	app := &App{
		store:              store,
		unlocked:           true,
		settings:           Settings{Hosts: []remote.Host{}},
		temporaryHosts:     map[string]remote.Host{host.ID: host},
		transientPasswords: map[string]string{host.ID: "verified"},
		shells:             map[string]*remote.Shell{host.ID: {}},
		shellHostRevisions: map[string]string{},
	}
	settings, err := app.SaveConnectedTemporaryHost(host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 1 {
		t.Fatalf("保存后主机数量 = %d，期望 1", len(settings.Hosts))
	}
	saved := settings.Hosts[0]
	if saved.ID != host.ID || saved.Temporary || saved.Password != "verified" || saved.HostKey != host.HostKey {
		t.Fatalf("连接成功后的保存内容错误: %#v", saved)
	}
	if saved.LastUsed == "" || store.saves != 1 {
		t.Fatalf("最近使用时间或保存次数错误: %#v，保存次数=%d", saved, store.saves)
	}
	if _, exists := app.temporaryHosts[host.ID]; exists {
		t.Fatal("保存后仍保留临时主机")
	}
	if _, exists := app.transientPasswords[host.ID]; exists {
		t.Fatal("保存后仍保留一次性密码")
	}
	if app.shellHostRevisions[host.ID] == "" {
		t.Fatal("保存后未同步当前 SSH 会话的主机版本")
	}
}

func TestSaveConnectedTemporaryHostRejectsBeforeSSHSuccess(t *testing.T) {
	host := remote.Host{ID: "quick-pending", Address: "192.0.2.31", Protocol: "ssh", Temporary: true}
	app := &App{
		store:          &memorySettingsStore{},
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{}},
		temporaryHosts: map[string]remote.Host{host.ID: host},
		shells:         map[string]*remote.Shell{},
	}
	if _, err := app.SaveConnectedTemporaryHost(host.ID); err == nil || !strings.Contains(err.Error(), "尚未连接成功") {
		t.Fatalf("SSH 未成功时错误允许保存: %v", err)
	}
}

func TestSaveConnectedTemporaryHostRollsBackWhenStoreFails(t *testing.T) {
	store := &memorySettingsStore{err: errors.New("模拟磁盘写入失败")}
	host := remote.Host{
		ID: "quick-rollback", Name: "待保存主机", Address: "192.0.2.32", Port: 22,
		Username: "root", Protocol: "ssh", AuthType: "password", Password: "old", Temporary: true,
		HostKey: "SHA256:trusted",
	}
	baseline := Settings{Hosts: []remote.Host{{ID: "existing", Name: "已有主机", Address: "192.0.2.10"}}, Theme: "dark"}
	shell := &remote.Shell{}
	app := &App{
		store:              store,
		unlocked:           true,
		settings:           baseline,
		temporaryHosts:     map[string]remote.Host{host.ID: host},
		transientPasswords: map[string]string{host.ID: "verified"},
		shells:             map[string]*remote.Shell{host.ID: shell},
		shellHostRevisions: map[string]string{host.ID: "原会话版本"},
	}

	if _, err := app.SaveConnectedTemporaryHost(host.ID); err == nil || !strings.Contains(err.Error(), "模拟磁盘写入失败") {
		t.Fatalf("存储失败未原样返回错误: %v", err)
	}
	if store.saves != 1 {
		t.Fatalf("存储调用次数 = %d，期望 1", store.saves)
	}
	if !reflect.DeepEqual(app.settings, baseline) {
		t.Fatalf("存储失败后配置被修改: %#v", app.settings)
	}
	if savedTemporary, exists := app.temporaryHosts[host.ID]; !exists || !reflect.DeepEqual(savedTemporary, host) {
		t.Fatalf("存储失败后临时主机未完整回滚: %#v", savedTemporary)
	}
	if app.transientPasswords[host.ID] != "verified" {
		t.Fatalf("存储失败后一次性密码丢失: %#v", app.transientPasswords)
	}
	if app.shells[host.ID] != shell || app.shellHostRevisions[host.ID] != "原会话版本" {
		t.Fatalf("存储失败后 SSH 会话状态被修改: shell=%p revision=%q", app.shells[host.ID], app.shellHostRevisions[host.ID])
	}
}

func TestSaveConnectedTemporaryHostRejectsDuplicateAndReleaseIsSafe(t *testing.T) {
	store := &memorySettingsStore{}
	host := remote.Host{
		ID: "quick-once", Name: "仅保存一次", Address: "192.0.2.33", Port: 22,
		Username: "root", Protocol: "ssh", AuthType: "password", Password: "secret", Temporary: true,
		HostKey: "SHA256:trusted",
	}
	shell := &remote.Shell{}
	app := &App{
		store:              store,
		unlocked:           true,
		settings:           Settings{Hosts: []remote.Host{}},
		temporaryHosts:     map[string]remote.Host{host.ID: host},
		transientPasswords: map[string]string{},
		shells:             map[string]*remote.Shell{host.ID: shell},
		shellHostRevisions: map[string]string{},
		connections:        remote.NewConnectionManager(),
		portForwards:       map[string]*appPortForward{},
	}
	settings, err := app.SaveConnectedTemporaryHost(host.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 1 || store.saves != 1 {
		t.Fatalf("首次保存结果错误: 主机=%#v，保存次数=%d", settings.Hosts, store.saves)
	}
	revision := app.shellHostRevisions[host.ID]
	if revision == "" {
		t.Fatal("首次保存未记录 SSH 会话版本")
	}
	if _, err := app.SaveConnectedTemporaryHost(host.ID); err == nil || !strings.Contains(err.Error(), "已经保存") {
		t.Fatalf("重复保存未被拒绝: %v", err)
	}
	app.ReleaseTemporaryHost(host.ID)
	app.ReleaseTemporaryHost(host.ID)
	if len(app.settings.Hosts) != 1 || app.settings.Hosts[0].ID != host.ID || store.saves != 1 {
		t.Fatalf("重复释放影响了已保存主机: 主机=%#v，保存次数=%d", app.settings.Hosts, store.saves)
	}
	if app.shells[host.ID] != shell || app.shellHostRevisions[host.ID] != revision {
		t.Fatalf("重复保存或释放破坏了现有 SSH 会话: shell=%p revision=%q", app.shells[host.ID], app.shellHostRevisions[host.ID])
	}
}

func TestSaveTemporaryHostPreservesAuthenticatedFingerprint(t *testing.T) {
	store := &memorySettingsStore{}
	temporary := remote.Host{
		ID: "temporary-key", Name: "临时主机", Address: "192.0.2.12", Port: 22,
		Username: "root", Protocol: "ssh", Temporary: true, HostKey: "SHA256:trusted",
	}
	app := &App{
		store:          store,
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{}},
		temporaryHosts: map[string]remote.Host{temporary.ID: temporary},
	}
	settings, err := app.SaveHost(remote.Host{
		ID: temporary.ID, Name: temporary.Name, Address: temporary.Address, Port: temporary.Port,
		Username: temporary.Username, Protocol: temporary.Protocol,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 1 || settings.Hosts[0].HostKey != temporary.HostKey {
		t.Fatalf("保存临时主机丢失已认证指纹: %#v", settings.Hosts)
	}
}

func TestSaveTemporaryHostPreservesFingerprintWithDefaultPort(t *testing.T) {
	temporary := remote.Host{
		ID: "temporary-default-port", Name: "临时主机", Address: "192.0.2.15",
		Username: "root", Protocol: "ssh", Temporary: true, HostKey: "SHA256:trusted",
	}
	app := &App{
		store:          &memorySettingsStore{},
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{}},
		temporaryHosts: map[string]remote.Host{temporary.ID: temporary},
	}
	settings, err := app.SaveHost(temporary)
	if err != nil {
		t.Fatal(err)
	}
	if settings.Hosts[0].Port != 22 || settings.Hosts[0].HostKey != temporary.HostKey {
		t.Fatalf("默认端口临时主机保存结果错误: %#v", settings.Hosts[0])
	}
}

func TestSaveTemporaryHostDoesNotReuseFingerprintAfterAddressChange(t *testing.T) {
	temporary := remote.Host{
		ID: "temporary-changed", Address: "192.0.2.13", Port: 22,
		Username: "root", Protocol: "ssh", Temporary: true, HostKey: "SHA256:old",
	}
	app := &App{
		store:          &memorySettingsStore{},
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{}},
		temporaryHosts: map[string]remote.Host{temporary.ID: temporary},
	}
	settings, err := app.SaveHost(remote.Host{
		ID: temporary.ID, Name: "已修改", Address: "192.0.2.14", Port: 22,
		Username: "root", Protocol: "ssh",
	})
	if err != nil {
		t.Fatal(err)
	}
	if settings.Hosts[0].HostKey != "" {
		t.Fatalf("地址变化后错误复用了旧指纹: %q", settings.Hosts[0].HostKey)
	}
}

func TestRememberHostKeyAcceptsLegacyZeroPort(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{
		store:    store,
		settings: Settings{Hosts: []remote.Host{{ID: "legacy-port", Address: "192.0.2.16", Protocol: "ssh"}}},
	}
	if err := app.rememberHostKey("legacy-port", "192.0.2.16", 22, "SHA256:trusted"); err != nil {
		t.Fatal(err)
	}
	if app.settings.Hosts[0].HostKey != "SHA256:trusted" || store.saves != 1 {
		t.Fatalf("旧配置指纹未保存: %#v，保存次数=%d", app.settings.Hosts[0], store.saves)
	}
}

func TestRememberTemporaryHostKeyAcceptsDefaultPort(t *testing.T) {
	temporary := remote.Host{ID: "temporary-zero-port", Address: "192.0.2.17", Protocol: "ssh", Temporary: true}
	app := &App{temporaryHosts: map[string]remote.Host{temporary.ID: temporary}}
	if err := app.rememberTemporaryHostKey(temporary.ID, temporary.Address, 22, "SHA256:trusted"); err != nil {
		t.Fatal(err)
	}
	if app.temporaryHosts[temporary.ID].HostKey != "SHA256:trusted" {
		t.Fatalf("临时主机指纹未保存: %#v", app.temporaryHosts[temporary.ID])
	}
}

func TestAcquireNodeSSHRejectsUntrustedHostWithoutPrompt(t *testing.T) {
	app := &App{
		unlocked: true,
		settings: Settings{Hosts: []remote.Host{{
			ID: "untrusted-node", VPSAlias: "UK-DO", Address: "192.0.2.18", Port: 22, Protocol: "ssh",
		}}},
	}
	_, _, err := app.acquireNodeSSH("", "UK-DO", 12)
	if err == nil || !strings.Contains(err.Error(), "待首次连接确认指纹") {
		t.Fatalf("未受信节点未被静默阻止: %v", err)
	}
}

func TestNodeSSHHostPrefersHostIDOverDuplicateAlias(t *testing.T) {
	app := &App{
		settings: Settings{Hosts: []remote.Host{
			{ID: "host-1", VPSAlias: "UK-DO", Address: "192.0.2.30", ProxyEnabled: true, ProxyHost: "127.0.0.1", ProxyPort: 20808},
			{ID: "host-2", VPSAlias: "UK-DO", Address: "192.0.2.31", ProxyEnabled: true, ProxyHost: "192.0.2.40", ProxyPort: 1080},
		}},
	}
	host, err := app.nodeSSHHost("host-2", "UK-DO")
	if err != nil {
		t.Fatal(err)
	}
	if host.ID != "host-2" || host.Address != "192.0.2.31" || host.ProxyHost != "192.0.2.40" || host.ProxyPort != 1080 {
		t.Fatalf("HostID 未绑定到正确主机及 SOCKS5：%#v", host)
	}
}

func TestNodeSSHHostRejectsDuplicateLegacyAlias(t *testing.T) {
	app := &App{settings: Settings{Hosts: []remote.Host{
		{ID: "host-1", VPSAlias: "UK-DO"},
		{ID: "host-2", VPSAlias: "uk-do"},
	}}}
	_, err := app.nodeSSHHost("", "UK-DO")
	if err == nil || !strings.Contains(err.Error(), "对应多个主机") {
		t.Fatalf("重复旧别名未被拒绝: %v", err)
	}
}

func TestNodeSSHHostUsesUniqueLegacyAlias(t *testing.T) {
	app := &App{settings: Settings{Hosts: []remote.Host{{
		ID: "host-legacy", VPSAlias: "UK-DO", Address: "192.0.2.32",
		ProxyEnabled: true, ProxyHost: "127.0.0.1", ProxyPort: 20808,
	}}}}
	host, err := app.nodeSSHHost("", "uk-do")
	if err != nil {
		t.Fatal(err)
	}
	if host.ID != "host-legacy" || !host.ProxyEnabled || host.ProxyPort != 20808 {
		t.Fatalf("唯一旧别名未迁移到正确主机配置: %#v", host)
	}
}

func TestNodeSSHHostDoesNotFallbackWhenHostIDIsInvalid(t *testing.T) {
	app := &App{settings: Settings{Hosts: []remote.Host{{ID: "host-1", VPSAlias: "UK-DO"}}}}
	_, err := app.nodeSSHHost("missing-host", "UK-DO")
	if err == nil || !strings.Contains(err.Error(), "节点绑定的主机不存在") {
		t.Fatalf("无效 HostID 错误回退到了别名: %v", err)
	}
}

func TestAIAgentRejectsUntrustedHostWithoutPrompt(t *testing.T) {
	host := remote.Host{ID: "untrusted-ai", Address: "192.0.2.20", Port: 22, Protocol: "ssh"}
	app := &App{settings: Settings{Hosts: []remote.Host{host}}}
	_, err := app.runAIAgentCommand(context.Background(), host, aiHostRevision(host), "true", time.Second, nil)
	if err == nil || !strings.Contains(err.Error(), "待首次连接确认指纹") {
		t.Fatalf("AI 未受信连接未被阻止: %v", err)
	}
}

func TestSaveHostEditPreservesServerManagedMetadata(t *testing.T) {
	store := &memorySettingsStore{}
	previous := remote.Host{
		ID: "host-1", Name: "旧名称", Address: "192.0.2.20", Port: 22, Username: "root", Protocol: "ssh",
		AuthType: "password", Password: "secret", Timeout: 12, KeepAlive: 30, Encoding: "UTF-8",
		LastUsed: "2026-08-15T10:00:00+08:00", SystemVersion: "Debian 13", Tags: "生产,核心",
		Starred: true, Deleted: true,
	}
	app := &App{
		store:          store,
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{previous}},
		aiAgent:        aiagent.NewController(),
		temporaryHosts: map[string]remote.Host{},
	}
	form := remote.Host{
		ID: previous.ID, Name: "新名称", Address: previous.Address, Port: previous.Port,
		Username: previous.Username, Protocol: previous.Protocol, AuthType: previous.AuthType,
		Password: previous.Password, Timeout: previous.Timeout, KeepAlive: previous.KeepAlive, Encoding: previous.Encoding,
	}
	settings, err := app.SaveHost(form)
	if err != nil {
		t.Fatal(err)
	}
	saved := settings.Hosts[0]
	if saved.Name != form.Name {
		t.Fatalf("连接表单字段未更新: %#v", saved)
	}
	if saved.LastUsed != previous.LastUsed || saved.SystemVersion != previous.SystemVersion || saved.Tags != previous.Tags {
		t.Fatalf("编辑主机清空了服务端元数据: %#v", saved)
	}
	if !saved.Deleted {
		t.Fatalf("编辑主机清空了删除状态: %#v", saved)
	}
	if !saved.Starred {
		t.Fatalf("编辑主机清空了星标状态: %#v", saved)
	}
}

func TestSetHostStarredPersistsPreference(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{
		store:    store,
		unlocked: true,
		settings: Settings{Hosts: []remote.Host{{ID: "host-1", Name: "测试", Address: "192.0.2.1", Username: "root"}}},
	}
	settings, err := app.SetHostStarred("host-1", true)
	if err != nil {
		t.Fatal(err)
	}
	if !settings.Hosts[0].Starred || !app.settings.Hosts[0].Starred {
		t.Fatalf("星标状态未持久化: %#v", settings.Hosts[0])
	}
}

func TestDeleteHostMarksDeleted(t *testing.T) {
	store := &memorySettingsStore{}
	var cleanupHostID string
	var cleanupAlias string
	app := &App{
		store:              store,
		unlocked:           true,
		settings:           Settings{Hosts: []remote.Host{{ID: "host-1", VPSAlias: "JP"}}},
		shells:             map[string]*remote.Shell{},
		shellEpochs:        map[string]uint64{},
		shellClientEpochs:  map[string]int{},
		shellHostRevisions: map[string]string{},
		connections:        remote.NewConnectionManager(),
		aiAgent:            aiagent.NewController(),
		rdpSessionHosts:    map[string]string{},
		rdpSessionJumps:    map[string]string{},
		portForwards:       map[string]*appPortForward{},
		temporaryHosts:     map[string]remote.Host{},
		nodeDataCleanup: func(hostID string, alias string) error {
			cleanupHostID = hostID
			cleanupAlias = alias
			return nil
		},
	}
	_, err := app.DeleteHost("host-1")
	if err != nil {
		t.Fatal(err)
	}
	if !app.settings.Hosts[0].Deleted {
		t.Fatalf("主机未标记为已删除: %#v", app.settings.Hosts[0])
	}
	if cleanupHostID != "host-1" || cleanupAlias != "JP" {
		t.Fatalf("未清理主机本地节点展示: hostID=%q alias=%q", cleanupHostID, cleanupAlias)
	}
}

func TestSaveHostAddsTypedGroupToExistingGroups(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{
		store:          store,
		unlocked:       true,
		settings:       Settings{Groups: []string{"生产"}},
		temporaryHosts: map[string]remote.Host{},
	}
	settings, err := app.SaveHost(remote.Host{
		Name: "测试主机", Address: "192.0.2.10", Username: "root", Group: "  中转  ",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(settings.Groups, "中转") {
		t.Fatalf("输入的新分组未保存到已有分组: %#v", settings.Groups)
	}
	if settings.Hosts[0].Group != "中转" {
		t.Fatalf("主机分组未去除首尾空格: %q", settings.Hosts[0].Group)
	}
}

func TestPermanentDeleteHostRemovesRecordAndNodeDisplay(t *testing.T) {
	store := &memorySettingsStore{}
	var cleanupHostID string
	var cleanupAlias string
	app := &App{
		store:          store,
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{{ID: "host-1", VPSAlias: "JP", Deleted: true}}},
		connections:    remote.NewConnectionManager(),
		portForwards:   map[string]*appPortForward{},
		temporaryHosts: map[string]remote.Host{},
		nodeDataCleanup: func(hostID string, alias string) error {
			cleanupHostID = hostID
			cleanupAlias = alias
			return nil
		},
	}
	settings, err := app.PermanentDeleteHost("host-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 0 || len(app.settings.Hosts) != 0 {
		t.Fatalf("永久删除后仍保留主机记录: %#v", app.settings.Hosts)
	}
	if cleanupHostID != "host-1" || cleanupAlias != "JP" {
		t.Fatalf("永久删除未清理本地节点展示: hostID=%q alias=%q", cleanupHostID, cleanupAlias)
	}
}

func TestBatchDeleteAndPermanentDeleteHosts(t *testing.T) {
	store := &memorySettingsStore{}
	cleaned := make([]string, 0, 4)
	app := &App{
		store:          store,
		unlocked:       true,
		settings:       Settings{Hosts: []remote.Host{{ID: "host-1", VPSAlias: "JP"}, {ID: "host-2", VPSAlias: "SG"}}},
		shells:         map[string]*remote.Shell{},
		shellEpochs:    map[string]uint64{},
		connections:    remote.NewConnectionManager(),
		portForwards:   map[string]*appPortForward{},
		temporaryHosts: map[string]remote.Host{},
		nodeDataCleanup: func(hostID string, _ string) error {
			cleaned = append(cleaned, hostID)
			return nil
		},
	}
	settings, err := app.DeleteHosts([]string{"host-1", "host-2", "host-1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 2 || !settings.Hosts[0].Deleted || !settings.Hosts[1].Deleted {
		t.Fatalf("批量删除未将全部主机移入已删除: %#v", settings.Hosts)
	}
	settings, err = app.PermanentDeleteHosts([]string{"host-1", "host-2"})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.Hosts) != 0 {
		t.Fatalf("批量永久删除后仍有主机记录: %#v", settings.Hosts)
	}
	if len(cleaned) != 4 {
		t.Fatalf("批量操作未清理每台主机的本地节点展示: %#v", cleaned)
	}
}
