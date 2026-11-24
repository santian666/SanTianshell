package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"santianshell/internal/aiagent"
	"santianshell/internal/nodemode"
	"santianshell/internal/nodemode/core"
	"santianshell/internal/remote"
)

type memorySettingsStore struct {
	saved Settings
	err   error
	saves int
}

func (s *memorySettingsStore) Save(value any) error {
	s.saves++
	if s.err != nil {
		return s.err
	}
	settings, ok := value.(Settings)
	if !ok {
		return errors.New("保存了非 Settings 数据")
	}
	s.saved = settings
	return nil
}

func TestSecurityPasswordHashAndLegacyCompatibility(t *testing.T) {
	hash, salt, err := newSecurityPassword("安全密码-123456")
	if err != nil {
		t.Fatalf("生成新密保失败: %v", err)
	}
	if !strings.HasPrefix(hash, "pbkdf2-sha256$") {
		t.Fatalf("新密保格式 = %q", hash)
	}
	if ok, legacy := verifyStoredSecurityPassword("安全密码-123456", hash, salt); !ok || legacy {
		t.Fatalf("新密保验证结果 ok=%v legacy=%v", ok, legacy)
	}
	if ok, _ := verifyStoredSecurityPassword("错误密码-123456", hash, salt); ok {
		t.Fatal("错误密码通过了新密保验证")
	}

	legacySalt := "legacy-salt"
	legacyHash := passwordHash("旧密码-123456", legacySalt)
	if ok, legacy := verifyStoredSecurityPassword("旧密码-123456", legacyHash, legacySalt); !ok || !legacy {
		t.Fatalf("旧密保验证结果 ok=%v legacy=%v", ok, legacy)
	}
}

func TestVerifySecurityPasswordMigratesLegacyHash(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{
		store: store,
		settings: Settings{
			SecuritySalt:  "legacy-salt",
			SecurityHash:  passwordHash("旧密码-123456", "legacy-salt"),
			LockOnStartup: true,
		},
	}
	result := app.VerifySecurityPassword("旧密码-123456")
	if !result.Success || !app.unlocked {
		t.Fatalf("旧密保未解锁: %#v", result)
	}
	if !strings.HasPrefix(app.settings.SecurityHash, "pbkdf2-sha256$") {
		t.Fatalf("旧密保未迁移: %q", app.settings.SecurityHash)
	}
	if store.saves != 1 || store.saved.SecurityHash != app.settings.SecurityHash {
		t.Fatalf("迁移未持久化: saves=%d", store.saves)
	}
}

func TestVerifyLegacySecurityWithNilStoreDoesNotPanic(t *testing.T) {
	legacyHash := passwordHash("旧密码-123456", "legacy-salt")
	app := &App{settings: Settings{SecuritySalt: "legacy-salt", SecurityHash: legacyHash}}
	result := app.VerifySecurityPassword("旧密码-123456")
	if !result.Success || !app.unlocked {
		t.Fatalf("无存储时旧密保未正常验证: %#v", result)
	}
	if app.settings.SecurityHash != legacyHash {
		t.Fatal("无存储时不应只在内存中迁移密保")
	}
}

func TestSecurityPasswordRateLimit(t *testing.T) {
	hash, salt, err := newSecurityPassword("正确密码-123456")
	if err != nil {
		t.Fatal(err)
	}
	app := &App{settings: Settings{SecurityHash: hash, SecuritySalt: salt}}
	for attempt := 1; attempt < securityMaxAttempts; attempt++ {
		result := app.VerifySecurityPassword("错误密码-123456")
		if result.Success || result.RemainingAttempts != securityMaxAttempts-attempt || result.RetryAfterSeconds != 0 {
			t.Fatalf("第 %d 次错误结果: %#v", attempt, result)
		}
	}
	blocked := app.VerifySecurityPassword("错误密码-123456")
	if blocked.Success || blocked.RetryAfterSeconds != int(securityRetryDelay/time.Second) || blocked.RemainingAttempts != 0 {
		t.Fatalf("第五次错误未触发冷却: %#v", blocked)
	}
	stillBlocked := app.VerifySecurityPassword("正确密码-123456")
	if stillBlocked.Success || stillBlocked.RetryAfterSeconds <= 0 {
		t.Fatalf("冷却期间正确密码不应通过: %#v", stillBlocked)
	}
	app.securityBlockedUntil = time.Now().Add(-time.Second)
	if result := app.VerifySecurityPassword("正确密码-123456"); !result.Success {
		t.Fatalf("冷却结束后正确密码未通过: %#v", result)
	}
}

func TestSaveGlobalSettingsRequiresCurrentPasswordAndExplicitRemoval(t *testing.T) {
	hash, salt, err := newSecurityPassword("当前密码-123456")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	store := &memorySettingsStore{}
	app := &App{
		store:    store,
		unlocked: true,
		settings: Settings{Theme: "light", DownloadDirectory: directory, LockOnStartup: true, SecurityHash: hash, SecuritySalt: salt},
	}
	input := GlobalSettings{
		Theme:             "light",
		DownloadDirectory: directory,
		LockOnStartup:     true,
		CurrentPassword:   "错误密码-123456",
		NewPassword:       "新的密码-123456",
		ConfirmPassword:   "新的密码-123456",
	}
	if _, err = app.SaveGlobalSettings(input); err == nil || !strings.Contains(err.Error(), "密保密码错误") {
		t.Fatalf("错误旧密码结果 = %v", err)
	}
	if app.settings.SecurityHash != hash {
		t.Fatal("错误旧密码污染了当前密保")
	}

	input.CurrentPassword = "当前密码-123456"
	settings, err := app.SaveGlobalSettings(input)
	if err != nil {
		t.Fatalf("修改密保失败: %v", err)
	}
	if settings.SecurityHash != "" || settings.SecuritySalt != "" {
		t.Fatal("SaveGlobalSettings 返回了密保验证材料")
	}
	if ok, _ := verifyStoredSecurityPassword("新的密码-123456", app.settings.SecurityHash, app.settings.SecuritySalt); !ok {
		t.Fatal("新密保密码未生效")
	}
	if store.saved.SecurityHash == "" {
		t.Fatal("持久化数据缺少密保哈希")
	}

	remove := GlobalSettings{
		Theme:             "light",
		DownloadDirectory: directory,
		CurrentPassword:   "新的密码-123456",
		RemoveSecurity:    true,
	}
	removed, err := app.SaveGlobalSettings(remove)
	if err != nil {
		t.Fatalf("移除密保失败: %v", err)
	}
	if app.settings.SecurityHash != "" || app.settings.SecuritySalt != "" || app.settings.LockOnStartup || app.settings.ProtectExport {
		t.Fatal("密保未完整移除")
	}
	if removed.SecurityHash != "" || removed.SecuritySalt != "" {
		t.Fatal("移除密保响应包含验证材料")
	}
}

func TestSaveGlobalSettingsDoesNotImplicitlyRemoveSecurity(t *testing.T) {
	hash, salt, err := newSecurityPassword("当前密码-123456")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	app := &App{
		store:    &memorySettingsStore{},
		unlocked: true,
		settings: Settings{Theme: "light", DownloadDirectory: directory, LockOnStartup: true, SecurityHash: hash, SecuritySalt: salt},
	}
	_, err = app.SaveGlobalSettings(GlobalSettings{
		Theme:             "light",
		DownloadDirectory: directory,
		CurrentPassword:   "当前密码-123456",
	})
	if err == nil || !strings.Contains(err.Error(), "明确选择移除密保") {
		t.Fatalf("关闭全部保护结果 = %v", err)
	}
	if app.settings.SecurityHash != hash || !app.settings.LockOnStartup {
		t.Fatal("普通保存隐式移除了密保")
	}
}

func TestSettingsResponsesHideVerifierAndSaveFailureDoesNotPolluteState(t *testing.T) {
	directory := t.TempDir()
	store := &memorySettingsStore{err: errors.New("模拟保存失败")}
	app := &App{
		store:    store,
		unlocked: true,
		settings: Settings{Theme: "light", DownloadDirectory: directory, LockOnStartup: true, SecurityHash: "secret-hash", SecuritySalt: "secret-salt"},
	}
	settings, err := app.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	if settings.SecurityHash != "" || settings.SecuritySalt != "" {
		t.Fatal("GetSettings 返回了密保验证材料")
	}
	_, err = app.SaveGlobalSettings(GlobalSettings{Theme: "dark", DownloadDirectory: directory, LockOnStartup: true})
	if err == nil || err.Error() != "模拟保存失败" {
		t.Fatalf("保存失败结果 = %v", err)
	}
	if app.settings.Theme != "light" || app.settings.SecurityHash != "secret-hash" || app.settings.SecuritySalt != "secret-salt" {
		t.Fatal("保存失败污染了内存设置")
	}

	app.store = nil
	_, err = app.SaveGlobalSettings(GlobalSettings{Theme: "dark", DownloadDirectory: directory, LockOnStartup: true})
	if err == nil || err.Error() != "配置存储未初始化" {
		t.Fatalf("空存储结果 = %v", err)
	}
	if app.settings.Theme != "light" {
		t.Fatal("空存储污染了内存设置")
	}
}

func TestLockedAppRejectsProtectedOperationsAndNodeMode(t *testing.T) {
	t.Setenv("APPDATA", t.TempDir())
	t.Setenv("LOCALAPPDATA", t.TempDir())

	app := &App{
		unlocked: false,
		settings: Settings{
			LockOnStartup: true,
			SecurityHash:  "hash",
			SecuritySalt:  "salt",
			Hosts: []remote.Host{{
				ID:       "host-1",
				Name:     "节点主机",
				Address:  "192.0.2.10",
				Port:     22,
				Username: "root",
				Protocol: "ssh",
				AuthType: "password",
				Password: "secret-password",
				VPSAlias: "vps-1",
			}},
		},
		aiAgent: aiagent.NewController(),
	}
	defer app.aiAgent.Close()

	checkLocked := func(name string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "软件已锁定") {
			t.Fatalf("%s 未拦截锁定状态访问: %v", name, err)
		}
	}

	_, err := app.RunCommand("host-1", "id")
	checkLocked("RunCommand", err)
	_, err = app.TestHost("host-1")
	checkLocked("TestHost", err)
	checkLocked("BeginQueue", app.BeginQueue("q1"))
	if app.StopQueue("q1") {
		t.Fatal("锁屏状态下 StopQueue 不应返回 true")
	}
	_, err = app.RunQueueStep("host-1", 1, "id", "q1", 5)
	checkLocked("RunQueueStep", err)
	checkLocked("ReplySSHInteraction", app.ReplySSHInteraction("req-1", true, nil))
	checkLocked("CloseRDP", app.CloseRDP("rdp-1"))
	checkLocked("ApproveAIAgent", app.ApproveAIAgent("r1", "a1", true))
	checkLocked("AnswerAIAgent", app.AnswerAIAgent("r1", "a1", "yes"))
	checkLocked("StopAIAgent", app.StopAIAgent("r1"))
	_, err = app.SelectDownloadDirectory()
	checkLocked("SelectDownloadDirectory", err)
	checkLocked("OpenLogDirectory", app.OpenLogDirectory())
	checkLocked("ExportLogs", app.ExportLogs())
	checkLocked("WriteTerminal", app.WriteTerminal("host-1", 1, "id\n"))
	checkLocked("ResizeTerminal", app.ResizeTerminal("host-1", 1, 24, 80))
	if pw := app.resolveNodePassword("host-1", "vps-1"); pw != "" {
		t.Fatalf("锁屏状态下 resolveNodePassword 泄露密码: %q", pw)
	}

	nm := nodemode.New(nil, nil, nil)
	nodemode.SetAccessGuard(nm, app.requireUnlocked)

	_, err = nm.GetOverview()
	checkLocked("nodemode.GetOverview", err)
	_, err = nm.GetNodes("vps-1")
	checkLocked("nodemode.GetNodes", err)
	checkLocked("nodemode.SaveVPS", nm.SaveVPS(core.VPS{Alias: "vps-1", Address: "192.0.2.10"}))
	if tcpRes := nm.TestTCP("127.0.0.1", 0); tcpRes.Success || !strings.Contains(tcpRes.Message, "软件已锁定") {
		t.Fatalf("锁屏状态下 nodemode.TestTCP 未被拦截: %#v", tcpRes)
	}
	if state := nm.RefreshVPSState("vps-1", false); !strings.Contains(state.Message, "软件已锁定") {
		t.Fatalf("锁屏状态下 nodemode.RefreshVPSState 未被拦截: %#v", state)
	}

	app.mu.Lock()
	app.unlocked = true
	app.mu.Unlock()
	if _, err := nm.GetOverview(); err != nil {
		t.Fatalf("解锁后 nodemode.GetOverview 失败: %v", err)
	}
	if _, err := nm.GetNodes("vps-1"); err != nil {
		t.Fatalf("解锁后 nodemode.GetNodes 失败: %v", err)
	}
}
