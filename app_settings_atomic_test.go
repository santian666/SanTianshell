package main

import (
	"errors"
	"reflect"
	"testing"

	"santianshell/internal/remote"
)

func atomicSettingsFixture(store settingsSaver) *App {
	clipboard := true
	return &App{
		store:    store,
		unlocked: true,
		settings: Settings{
			Hosts: []remote.Host{
				{ID: "host-1", Name: "主机一", Address: "192.0.2.1", Username: "root", Password: "旧密码", Protocol: "ssh", AuthType: "password", Port: 22, HostKey: "SHA256:old", RDPDriveIDs: []string{"C"}, RDPRedirectClipboard: &clipboard},
				{ID: "host-2", Name: "主机二", Address: "192.0.2.2", Username: "root", Protocol: "ssh", AuthType: "password", Port: 22, Deleted: true},
				{ID: "host-3", Name: "主机三", Address: "192.0.2.3", Username: "root", Protocol: "ssh", AuthType: "password", Port: 22},
				{ID: "host-4", Name: "主机四", Address: "192.0.2.4", Username: "root", Protocol: "ssh", AuthType: "password", Port: 22, CredentialID: "credential-1"},
			},
			AIProviders: []AIConfig{
				{ID: "ai-1", Name: "供应商一", Endpoint: "https://one.example/v1", APIKey: "key-1", Model: "model-1", Models: []string{"model-1"}, Mode: "review"},
				{ID: "ai-2", Name: "供应商二", Endpoint: "https://two.example/v1", APIKey: "key-2", Model: "model-2", Models: []string{"model-2"}, Mode: "suggest"},
			},
			CurrentProviderID: "ai-1",
			QuickCommands:     []QuickCommand{{ID: "command-1", Name: "查看系统", Command: "uname -a", Type: "single"}},
			Groups:            []string{"默认分组"},
			Credentials: []Credential{
				{ID: "credential-1", Name: "共用密码", Type: "password", Password: "旧凭证密码"},
				{ID: "credential-2", Name: "未使用密码", Type: "password", Password: "待删除密码"},
			},
		},
		shells:             map[string]*remote.Shell{},
		shellEpochs:        map[string]uint64{},
		shellClientEpochs:  map[string]int{},
		shellHostRevisions: map[string]string{},
		temporaryHosts:     map[string]remote.Host{},
		transientPasswords: map[string]string{},
	}
}

func TestUserSettingsWritesKeepMemoryUnchangedWhenSaveFails(t *testing.T) {
	saveErr := errors.New("模拟保存失败")
	tests := []struct {
		name string
		run  func(*App) error
	}{
		{name: "保存主机", run: func(app *App) error {
			_, err := app.SaveHost(remote.Host{ID: "host-1", Name: "修改后的主机", Address: "198.51.100.1", Username: "root", Protocol: "ssh", AuthType: "password", Port: 22})
			return err
		}},
		{name: "设置国家", run: func(app *App) error {
			_, err := app.SetHostCountry("host-1", "UK")
			return err
		}},
		{name: "保存凭证", run: func(app *App) error {
			_, err := app.SaveCredential(Credential{ID: "credential-2", Name: "修改后的凭证", Type: "password", Password: "新密码"})
			return err
		}},
		{name: "删除凭证", run: func(app *App) error {
			_, err := app.DeleteCredential("credential-2")
			return err
		}},
		{name: "删除主机", run: func(app *App) error {
			_, err := app.DeleteHost("host-1")
			return err
		}},
		{name: "恢复主机", run: func(app *App) error {
			_, err := app.RestoreHost("host-2")
			return err
		}},
		{name: "保存快捷命令", run: func(app *App) error {
			_, err := app.SaveQuickCommand(QuickCommand{ID: "command-1", Name: "修改后的命令", Command: "uptime"})
			return err
		}},
		{name: "删除快捷命令", run: func(app *App) error {
			_, err := app.DeleteQuickCommand("command-1")
			return err
		}},
		{name: "保存分组", run: func(app *App) error {
			_, err := app.SaveGroup("新分组")
			return err
		}},
		{name: "保存 AI 供应商", run: func(app *App) error {
			_, err := app.SaveAI(AIConfig{ID: "ai-1", Name: "修改后的供应商", Endpoint: "https://one.example/v1", APIKey: "key-1", Model: "model-1", Mode: "managed"})
			return err
		}},
		{name: "选择 AI 供应商", run: func(app *App) error {
			return app.SelectAIProvider("ai-2")
		}},
		{name: "选择 AI 模型", run: func(app *App) error {
			_, err := app.SelectAIModel("ai-1", "model-new")
			return err
		}},
		{name: "选择 AI 权限", run: func(app *App) error {
			_, err := app.SelectAIPermission("ai-1", "managed")
			return err
		}},
		{name: "删除 AI 供应商", run: func(app *App) error {
			_, err := app.DeleteAIProvider("ai-1")
			return err
		}},
		{name: "记住主机指纹", run: func(app *App) error {
			return app.rememberHostKey("host-3", "192.0.2.3", 22, "SHA256:new")
		}},
		{name: "重置主机指纹", run: func(app *App) error {
			_, err := app.ResetHostKey("host-1")
			return err
		}},
		{name: "永久保存独立密码", run: func(app *App) error {
			return app.persistSuccessfulTerminalPassword("host-1", app.settings.Hosts[0], "新密码")
		}},
		{name: "永久保存凭证库密码", run: func(app *App) error {
			return app.persistSuccessfulTerminalPassword("host-4", app.settings.Hosts[3], "新凭证密码")
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memorySettingsStore{err: saveErr}
			app := atomicSettingsFixture(store)
			before := cloneSettings(app.settings)

			err := tt.run(app)
			if !errors.Is(err, saveErr) {
				t.Fatalf("错误 = %v，期望 %v", err, saveErr)
			}
			if store.saves != 1 {
				t.Fatalf("保存次数 = %d，期望 1", store.saves)
			}
			if !reflect.DeepEqual(app.settings, before) {
				t.Fatalf("保存失败后内存配置发生变化\n实际: %#v\n期望: %#v", app.settings, before)
			}
		})
	}
}

func TestCloneSettingsDetachesNestedValues(t *testing.T) {
	app := atomicSettingsFixture(&memorySettingsStore{})
	cloned := cloneSettings(app.settings)

	cloned.Hosts[0].RDPDriveIDs[0] = "D"
	*cloned.Hosts[0].RDPRedirectClipboard = false
	cloned.AIProviders[0].Models[0] = "changed"
	cloned.QuickCommands[0].Name = "changed"
	cloned.Groups[0] = "changed"
	cloned.Credentials[0].Password = "changed"

	if app.settings.Hosts[0].RDPDriveIDs[0] != "C" || !*app.settings.Hosts[0].RDPRedirectClipboard ||
		app.settings.AIProviders[0].Models[0] != "model-1" || app.settings.QuickCommands[0].Name != "查看系统" ||
		app.settings.Groups[0] != "默认分组" || app.settings.Credentials[0].Password != "旧凭证密码" {
		t.Fatal("cloneSettings 仍与原配置共享可变数据")
	}
}
