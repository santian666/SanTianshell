package main

import "testing"

func TestDefaultSettingsContainNoUserData(t *testing.T) {
	settings := defaultSettings()
	if len(settings.Hosts) != 0 {
		t.Fatal("默认配置不应包含主机")
	}
	if len(settings.AIProviders) != 0 || settings.CurrentProviderID != "" ||
		settings.AI.ID != "" || settings.AI.Name != "" || settings.AI.Endpoint != "" ||
		settings.AI.APIKey != "" || settings.AI.Model != "" || len(settings.AI.Models) != 0 || settings.AI.Mode != "" {
		t.Fatal("默认配置不应包含 AI 供应商、模型缓存或 API Key")
	}
	if len(settings.Credentials) != 0 {
		t.Fatal("默认配置不应包含密码、私钥或凭证")
	}
	if len(settings.QuickCommands) != 0 || len(settings.Groups) != 0 {
		t.Fatal("默认配置不应包含快捷命令或用户分组")
	}
	if settings.SecurityHash != "" || settings.SecuritySalt != "" {
		t.Fatal("默认配置不应包含密保数据")
	}
}
