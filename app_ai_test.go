package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"santianshell/internal/aiagent"
	"santianshell/internal/remote"
)

func TestGetPublicSettingsReturnsUserAIPresetPrompt(t *testing.T) {
	app := &App{}
	if got := app.GetPublicSettings().AIPresetPrompt; got != "" {
		t.Fatalf("空附加偏好 = %q", got)
	}

	app.settings.AIPresetPrompt = aiagent.DefaultServerOperationPrompt()
	if got := app.GetPublicSettings().AIPresetPrompt; got != "" {
		t.Fatalf("内置默认提示词未归一化为空：%q", got)
	}

	app.settings.AIPresetPrompt = "  自定义运维策略  "
	if got := app.GetPublicSettings().AIPresetPrompt; got != "自定义运维策略" {
		t.Fatalf("自定义附加偏好 = %q", got)
	}
}

func TestSaveGlobalSettingsNormalizesAIPresetPrompt(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{store: store, unlocked: true}
	settings, err := app.SaveGlobalSettings(GlobalSettings{
		Theme:             "light",
		DownloadDirectory: t.TempDir(),
		AIPresetPrompt:    aiagent.DefaultServerOperationPrompt(),
	})
	if err != nil {
		t.Fatalf("保存全局设置失败: %v", err)
	}
	if settings.AIPresetPrompt != "" || store.saved.AIPresetPrompt != "" {
		t.Fatalf("内置默认提示词被保存为附加偏好: settings=%q saved=%q", settings.AIPresetPrompt, store.saved.AIPresetPrompt)
	}
}

func TestSaveGlobalSettingsRejectsOversizedAIPresetPrompt(t *testing.T) {
	app := &App{unlocked: true}
	_, err := app.SaveGlobalSettings(GlobalSettings{
		Theme:             "light",
		DownloadDirectory: t.TempDir(),
		AIPresetPrompt:    strings.Repeat("a", maxAIPresetPromptSize+1),
	})
	if err == nil || err.Error() != "AI 预设提示词不能超过 32KB" {
		t.Fatalf("超长预设错误 = %v", err)
	}
}

func TestSelectAIPermissionPersistsCurrentProvider(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{
		store:    store,
		unlocked: true,
		settings: Settings{AIProviders: []AIConfig{{ID: "provider-1", Name: "测试供应商", Mode: "review"}}},
	}
	settings, err := app.SelectAIPermission("provider-1", "managed")
	if err != nil {
		t.Fatalf("切换 AI 权限失败: %v", err)
	}
	if settings.CurrentProviderID != "provider-1" || settings.AIProviders[0].Mode != "managed" {
		t.Fatalf("AI 权限未正确更新: %#v", settings.AIProviders[0])
	}
	if store.saves != 1 || store.saved.AIProviders[0].Mode != "managed" {
		t.Fatalf("AI 权限未正确保存: saves=%d saved=%#v", store.saves, store.saved.AIProviders)
	}
}

func TestSelectAIPermissionRejectsInvalidValues(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{
		store:    store,
		unlocked: true,
		settings: Settings{AIProviders: []AIConfig{{ID: "provider-1", Mode: "review"}}},
	}
	if _, err := app.SelectAIPermission("provider-1", "auto"); err == nil || err.Error() != "AI 权限无效" {
		t.Fatalf("旧权限值未被拒绝: %v", err)
	}
	if _, err := app.SelectAIPermission("missing", "suggest"); err == nil || err.Error() != "AI 供应商不存在" {
		t.Fatalf("不存在的供应商错误 = %v", err)
	}
	if store.saves != 0 || app.settings.AIProviders[0].Mode != "review" {
		t.Fatalf("非法权限修改了配置: saves=%d mode=%q", store.saves, app.settings.AIProviders[0].Mode)
	}
}

func TestAIHostSnapshotsIncludesUsername(t *testing.T) {
	const osRelease = "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nID=debian\nVERSION_ID=12\nSANTIANSHELL_PACKAGE_MANAGER=apt"
	app := &App{
		settings: Settings{Hosts: []remote.Host{{
			ID:       "host-1",
			Name:     "测试主机",
			Address:  "192.0.2.1",
			Username: "root",
			Protocol: "ssh",
		}}},
		shells:         map[string]*remote.Shell{},
		hostOSReleases: map[string]string{"host-1": osRelease},
	}
	hosts, _, _ := app.aiHostSnapshots()
	if len(hosts) != 1 || hosts[0].Username != "root" || hosts[0].OSRelease != osRelease {
		t.Fatalf("AI 主机快照未包含用户名或系统缓存: %#v", hosts)
	}
}

func TestAIHostSnapshotsIncludesOpenTemporarySSH(t *testing.T) {
	temporary := remote.Host{
		ID:        "temp-1",
		Name:      "临时主机",
		Address:   "192.0.2.20",
		Username:  "debian",
		Protocol:  "ssh",
		HostKey:   "SHA256:test",
		Temporary: true,
	}
	app := &App{
		temporaryHosts: map[string]remote.Host{temporary.ID: temporary},
		shells:         map[string]*remote.Shell{},
	}
	hosts, revisions, connectionHosts := app.aiHostSnapshots()
	if len(hosts) != 1 || hosts[0].ID != temporary.ID || hosts[0].Username != "debian" || !hosts[0].Trusted {
		t.Fatalf("AI 未包含临时 SSH 主机: %#v", hosts)
	}
	if revisions[temporary.ID] == "" || connectionHosts[temporary.ID].ID != temporary.ID {
		t.Fatalf("临时 SSH 主机连接快照不完整: revisions=%#v hosts=%#v", revisions, connectionHosts)
	}
	app.mu.RLock()
	currentRevision := app.aiCurrentHostRevisionLocked(temporary.ID)
	app.mu.RUnlock()
	if currentRevision != revisions[temporary.ID] {
		t.Fatalf("临时 SSH 主机版本不一致: current=%q snapshot=%q", currentRevision, revisions[temporary.ID])
	}
}

func TestAnswerAIAgentForwardsToController(t *testing.T) {
	app := &App{unlocked: true, aiAgent: aiagent.NewController()}
	defer app.aiAgent.Close()
	if err := app.AnswerAIAgent("missing-run", "missing-action", "确认"); err == nil || err.Error() != "AI 任务已结束" {
		t.Fatalf("AI 回答接口未转发到控制器: %v", err)
	}
}

func TestFetchAIModelsNormalizesResponse(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("模型请求路径 = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":" model-z "},{"id":"model-a"},{"id":"model-z"},{"id":""}]}`)
	}))
	defer server.Close()

	models, err := fetchAIModels(context.Background(), server.URL+"/v1", "test-key")
	if err != nil {
		t.Fatalf("fetchAIModels() error = %v", err)
	}
	if got, want := strings.Join(models, ","), "model-a,model-z"; got != want {
		t.Fatalf("models = %q, want %q", got, want)
	}
}

func TestNormalizeAIModelsKeepsAllAndSorts(t *testing.T) {
	t.Parallel()
	const modelCount = 750
	input := make([]string, 0, modelCount+1)
	for i := modelCount - 1; i >= 0; i-- {
		input = append(input, fmt.Sprintf("model-%04d", i))
	}
	longModel := strings.Repeat("m", 600)
	input = append(input, "model-0001", longModel)

	models := normalizeAIModels(input)
	if len(models) != modelCount+1 {
		t.Fatalf("模型数量 = %d, want %d", len(models), modelCount+1)
	}
	if models[len(models)-1] != "model-0749" {
		t.Fatalf("模型未正确排序：末项 %q", models[len(models)-1])
	}
	foundLongModel := false
	for _, model := range models {
		if model == longModel {
			foundLongModel = true
			break
		}
	}
	if !foundLongModel {
		t.Fatal("超长模型 ID 被静默丢弃")
	}
}

func TestFetchAIModelsReportsSafeHTTPErrors(t *testing.T) {
	t.Parallel()
	const apiKey = "secret-test-key"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprintf(w, `{"error":{"message":"invalid key %s"}}`, apiKey)
	}))
	defer server.Close()

	_, err := fetchAIModels(context.Background(), server.URL, apiKey)
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("HTTP 错误 = %v", err)
	}
	if strings.Contains(err.Error(), apiKey) {
		t.Fatalf("错误信息泄露 API Key：%v", err)
	}
}

func TestFetchAIModelsValidatesEmptyValuesAndTimeout(t *testing.T) {
	t.Parallel()
	if _, err := fetchAIModels(context.Background(), "", "key"); err == nil || err.Error() != "API 地址不能为空" {
		t.Fatalf("空 API 地址错误 = %v", err)
	}
	if _, err := fetchAIModels(context.Background(), "https://example.com/v1", ""); err == nil || err.Error() != "API Key 不能为空" {
		t.Fatalf("空 API Key 错误 = %v", err)
	}

	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := fetchAIModels(ctx, server.URL, "key")
	if err == nil || !strings.Contains(err.Error(), "获取模型超时") {
		t.Fatalf("超时错误 = %v", err)
	}
}

func TestSameSSHServer(t *testing.T) {
	tests := []struct {
		name   string
		first  remote.Host
		second remote.Host
		want   bool
	}{
		{
			name:   "相同地址和端口",
			first:  remote.Host{Address: "Example.COM", Port: 22},
			second: remote.Host{Address: "example.com", Port: 22},
			want:   true,
		},
		{
			name:   "相同地址不同端口",
			first:  remote.Host{Address: "example.com", Port: 22},
			second: remote.Host{Address: "example.com", Port: 2222},
		},
		{
			name:   "不同地址但相同指纹",
			first:  remote.Host{Address: "192.0.2.1", Port: 22, HostKey: "SHA256:same"},
			second: remote.Host{Address: "server.example.com", Port: 22, HostKey: "SHA256:same"},
			want:   true,
		},
		{
			name:   "空指纹不能匹配",
			first:  remote.Host{Address: "192.0.2.1", Port: 22},
			second: remote.Host{Address: "192.0.2.2", Port: 22},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sameSSHServer(test.first, test.second); got != test.want {
				t.Fatalf("sameSSHServer() = %v，期望 %v", got, test.want)
			}
		})
	}
}
