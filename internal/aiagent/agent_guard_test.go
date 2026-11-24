package aiagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestOpenAIURLAndHTMLResponseDiagnostics(t *testing.T) {
	tests := []struct {
		endpoint string
		resource string
		want     string
	}{
		{"https://api.example.com", "chat/completions", "https://api.example.com/v1/chat/completions"},
		{"https://api.example.com/v1/", "models", "https://api.example.com/v1/models"},
		{"https://api.example.com/v1/chat/completions", "models", "https://api.example.com/v1/models"},
	}
	for _, test := range tests {
		got, err := BuildOpenAIURL(test.endpoint, test.resource)
		if err != nil || got != test.want {
			t.Fatalf("OpenAI 地址构建错误 endpoint=%q resource=%q got=%q err=%v", test.endpoint, test.resource, got, err)
		}
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><html></html>"))
	}))
	defer server.Close()
	_, err := NewController().complete(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test"}, []chatMessage{{Role: "user", Content: "测试"}})
	if err == nil || !strings.Contains(err.Error(), "返回网页而不是 OpenAI JSON") || strings.Contains(err.Error(), "invalid character '<'") {
		t.Fatalf("HTML 响应诊断不正确：%v", err)
	}
}

func TestAITransportErrorDoesNotExposeSecrets(t *testing.T) {
	const endpoint = "https://api.example.com/v1/chat/completions?api_key=query-secret"
	err := &url.Error{Op: "Post", URL: endpoint, Err: errors.New("代理连接失败：query-secret")}
	message := safeAITransportError(err, endpoint, "header-secret")
	for _, secret := range []string{"query-secret", "header-secret", "api_key="} {
		if strings.Contains(message, secret) {
			t.Fatalf("AI 网络错误泄露敏感信息 %q：%s", secret, message)
		}
	}
	if !strings.Contains(message, "代理连接失败") {
		t.Fatalf("AI 网络错误丢失有效原因：%s", message)
	}
}

func TestExtractAIErrorMessageSupportsCommonShapes(t *testing.T) {
	for raw, want := range map[string]string{
		`{"error":{"message":"密钥无效"}}`: "密钥无效",
		`{"error":"请求被拒绝"}`:            "请求被拒绝",
		`{"message":"模型不存在"}`:          "模型不存在",
	} {
		if got := extractAIErrorMessage([]byte(raw)); got != want {
			t.Fatalf("错误信息解析失败 raw=%s got=%q want=%q", raw, got, want)
		}
	}
}

func TestFinishOnSuccessCompletesAfterReliableExitZero(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if call := calls.Add(1); call != 1 {
			t.Errorf("最后一步成功后仍调用模型: call=%d", call)
			writeModelResponse(t, w, "不应继续调用模型")
			return
		}
		writeModelResponse(t, w, "", toolCall{ID: "change", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"chmod 600 /etc/example-app.conf","purpose":"修改配置文件权限","finish_on_success":true}`}})
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "把配置文件权限改为 600", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "AK-日本", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if command != "chmod 600 /etc/example-app.conf" {
				t.Fatalf("执行了非预期命令: %q", command)
			}
			return CommandResult{ExitCode: 0, Duration: 200}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动升级任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("通用最后一步成功后未直接收尾: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待升级任务完成超时")
	}
	if commands.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("最后一步成功后仍有多余调用: commands=%d modelCalls=%d", commands.Load(), calls.Load())
	}
}

func TestEmptyResponseAfterOrdinaryRestartCompletesWithoutVerification(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "restart", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl restart nginx","purpose":"重启 nginx","finish_on_success":true}`}})
		default:
			writeModelResponse(t, w, "")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	var verificationPrompts atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "重启 nginx", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if command != "systemctl restart nginx" {
				t.Errorf("普通重启后执行了多余验证命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
				verificationPrompts.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动普通重启任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("普通重启成功后未直接完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待普通重启任务完成超时")
	}
	if commands.Load() != 1 || calls.Load() != 2 || verificationPrompts.Load() != 0 {
		t.Fatalf("普通重启仍进入验证流程: commands=%d modelCalls=%d prompts=%d", commands.Load(), calls.Load(), verificationPrompts.Load())
	}
}

func TestOrdinarySuccessfulCommandsCompleteWithoutVerification(t *testing.T) {
	tests := []struct {
		name     string
		question string
		command  string
		purpose  string
		output   string
	}{
		{name: "安装", question: "安装 curl", command: "apt-get install -y curl", purpose: "安装 curl", output: "Setting up curl (7.88.1) ...\n"},
		{name: "更新", question: "刷新 apt 软件包索引", command: "apt-get update", purpose: "刷新软件包索引", output: "Reading package lists... Done\n"},
		{name: "文件修改", question: "修改应用配置中的 worker 数量", command: "sed -i 's/^workers=.*/workers=4/' /etc/example-app.conf", purpose: "修改 worker 数量", output: ""},
		{name: "权限修改", question: "把应用配置权限改为 600", command: "chmod 600 /etc/example-app.conf", purpose: "修改配置文件权限", output: ""},
		{name: "单项内核参数", question: "将 swappiness 设置为 10", command: "sysctl -w vm.swappiness=10", purpose: "设置 swappiness", output: "vm.swappiness = 10\n"},
		{name: "普通优化脚本", question: "执行现有的系统优化脚本", command: "/opt/tools/optimize-system.sh", purpose: "执行系统优化脚本", output: "optimization completed\n"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			arguments, err := json.Marshal(commandArguments{HostID: "host-1", Command: test.command, Purpose: test.purpose, FinishOnSuccess: true})
			if err != nil {
				t.Fatalf("构造命令参数失败: %v", err)
			}
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					writeModelResponse(t, w, "", toolCall{ID: "run", Type: "function", Function: toolFunction{Name: "run_command", Arguments: string(arguments)}})
					return
				}
				writeModelResponse(t, w, "")
			}))
			defer server.Close()

			controller := NewController()
			finished := make(chan Event, 1)
			var commands atomic.Int32
			var verificationPrompts atomic.Int32
			_, err = controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", test.question, Environment{
				ListHosts: func() []Host {
					return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", OSRelease: "ID=debian\nVERSION_ID=12", Connected: true, Trusted: true}}
				},
				RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
					commands.Add(1)
					if command != test.command {
						t.Errorf("执行了非预期命令: got=%q want=%q", command, test.command)
						return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
					}
					return CommandResult{Output: test.output, ExitCode: 0}, nil
				},
				Emit: func(event Event) {
					if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
						verificationPrompts.Add(1)
					}
					if event.Type == "completed" || event.Type == "failed" {
						finished <- event
					}
				},
			})
			if err != nil {
				t.Fatalf("启动普通命令任务失败: %v", err)
			}

			select {
			case event := <-finished:
				if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
					t.Fatalf("退出码 0 的普通命令未直接完成: %#v", event)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等待普通命令完成超时")
			}
			if commands.Load() != 1 || calls.Load() != 1 || verificationPrompts.Load() != 0 {
				t.Fatalf("普通命令进入了多余验证: commands=%d modelCalls=%d prompts=%d", commands.Load(), calls.Load(), verificationPrompts.Load())
			}
		})
	}
}

func TestSuccessfulCommandWarningIsReportedWithoutFailing(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) == 1 {
			writeModelResponse(t, w, "", toolCall{ID: "update", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get update","purpose":"刷新软件包索引","finish_on_success":true}`}})
			return
		}
		writeModelResponse(t, w, "")
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "刷新 apt 软件包索引", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", OSRelease: "ID=debian\nVERSION_ID=12", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			return CommandResult{Output: "WARNING: apt does not have a stable CLI interface.\nReading package lists... Done\n", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动警告命令任务失败: %v", err)
	}

	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "警告") {
			t.Fatalf("退出码 0 的警告被错误判为失败或未反馈: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待警告命令完成超时")
	}
}

func TestCommandFailureReportsRealExitOrTransportError(t *testing.T) {
	tests := []struct {
		name         string
		result       CommandResult
		runErr       error
		wantMessages []string
	}{
		{
			name:         "非零退出码",
			result:       CommandResult{Output: "E: Unable to locate package missing-tool\n", ExitCode: 100},
			wantMessages: []string{"退出码", "100", "Unable to locate package missing-tool"},
		},
		{
			name:         "SSH传输错误",
			result:       CommandResult{ExitCode: 255},
			runErr:       errors.New("ssh: connection reset by peer"),
			wantMessages: []string{"connection reset by peer"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if calls.Add(1) == 1 {
					writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y missing-tool","purpose":"安装 missing-tool"}`}})
					return
				}
				writeModelResponse(t, w, "")
			}))
			defer server.Close()

			controller := NewController()
			finished := make(chan Event, 1)
			var verificationPrompts atomic.Int32
			_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 missing-tool", Environment{
				ListHosts: func() []Host {
					return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", OSRelease: "ID=debian\nVERSION_ID=12", Trusted: true}}
				},
				RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
					return test.result, test.runErr
				},
				Emit: func(event Event) {
					if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
						verificationPrompts.Add(1)
					}
					if event.Type == "completed" || event.Type == "failed" {
						finished <- event
					}
				},
			})
			if err != nil {
				t.Fatalf("启动失败命令任务失败: %v", err)
			}

			select {
			case event := <-finished:
				if event.Type != "failed" {
					t.Fatalf("失败命令被错误报告为完成: %#v", event)
				}
				for _, want := range test.wantMessages {
					if !strings.Contains(event.Message, want) {
						t.Fatalf("失败结果缺少真实错误 %q: %#v", want, event)
					}
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等待失败命令反馈超时")
			}
			if calls.Load() != 2 || verificationPrompts.Load() != 0 {
				t.Fatalf("失败命令进入了验证循环: modelCalls=%d prompts=%d", calls.Load(), verificationPrompts.Load())
			}
		})
	}
}

func TestSysctlApplyCompletesPreviousConfigurationWrite(t *testing.T) {
	const (
		configCommand = "printf '%s\\n' 'net.core.default_qdisc=fq' 'net.ipv4.tcp_congestion_control=bbr' > /etc/sysctl.d/99-santianshell-bbr.conf"
		applyCommand  = "sysctl -p /etc/sysctl.d/99-santianshell-bbr.conf"
	)
	arguments := func(command, purpose string) string {
		raw, _ := json.Marshal(commandArguments{HostID: "host-1", Command: command, Purpose: purpose})
		return string(raw)
	}

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "write", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(configCommand, "写入 BBR 持久配置")}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "apply", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(applyCommand, "加载并应用 BBR 配置")}})
		default:
			writeModelResponse(t, w, "BBR 已启用并完成持久配置。")
		}
	}))
	defer server.Close()

	results := map[string]CommandResult{
		configCommand: {ExitCode: 0},
		applyCommand: {
			Output:   "net.core.default_qdisc = fq\nnet.ipv4.tcp_congestion_control = bbr\n",
			ExitCode: 0,
		},
	}
	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 2)
	var redundantVerificationPrompts atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "启用 BBR", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Connected: true, Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			result, ok := results[command]
			if !ok {
				t.Errorf("执行了非预期的 BBR 命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return result, nil
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
				redundantVerificationPrompts.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动 BBR 配置任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "BBR 已启用并完成持久配置。" {
			t.Fatalf("sysctl -p 后任务未直接完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 BBR 配置任务完成超时")
	}
	if expected := []string{configCommand, applyCommand}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("BBR 配置命令异常: got=%v want=%v", commands, expected)
	}
	if calls.Load() != 3 || redundantVerificationPrompts.Load() != 0 {
		t.Fatalf("sysctl -p 后仍追加验证: modelCalls=%d prompts=%d", calls.Load(), redundantVerificationPrompts.Load())
	}
}

func TestEnabledBBRReadOnlyChecksCompleteWithoutVerificationLoop(t *testing.T) {
	commandsExpected := []string{
		"sysctl net.ipv4.tcp_congestion_control",
		"sysctl net.core.default_qdisc",
		"grep -RHE 'net.ipv4.tcp_congestion_control|net.core.default_qdisc' /etc/sysctl.conf /etc/sysctl.d 2>/dev/null",
	}
	results := map[string]CommandResult{
		commandsExpected[0]: {Output: "net.ipv4.tcp_congestion_control = bbr\n", ExitCode: 0},
		commandsExpected[1]: {Output: "net.core.default_qdisc = fq\n", ExitCode: 0},
		commandsExpected[2]: {Output: "/etc/sysctl.d/99-santianshell-bbr.conf:net.core.default_qdisc=fq\n/etc/sysctl.d/99-santianshell-bbr.conf:net.ipv4.tcp_congestion_control=bbr\n", ExitCode: 0},
	}
	arguments := func(command, purpose string) string {
		raw, _ := json.Marshal(commandArguments{HostID: "host-1", Command: command, Purpose: purpose})
		return string(raw)
	}

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "congestion", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(commandsExpected[0], "读取当前拥塞控制算法")}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "qdisc", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(commandsExpected[1], "读取当前默认队列算法")}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "config", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(commandsExpected[2], "读取 BBR 持久配置")}})
		default:
			writeModelResponse(t, w, "BBR 当前已启用，持久配置存在。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, len(commandsExpected))
	var redundantVerificationPrompts atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "检查 BBR 是否已启用并持久配置", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Connected: true, Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			result, ok := results[command]
			if !ok {
				t.Errorf("执行了非预期的 BBR 检查命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return result, nil
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
				redundantVerificationPrompts.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动 BBR 只读检查任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "BBR 当前已启用，持久配置存在。" {
			t.Fatalf("BBR 只读检查未正常完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 BBR 只读检查任务完成超时")
	}
	if !reflect.DeepEqual(commands, commandsExpected) {
		t.Fatalf("BBR 只读检查命令异常: got=%v want=%v", commands, commandsExpected)
	}
	if calls.Load() != 4 || redundantVerificationPrompts.Load() != 0 {
		t.Fatalf("BBR 只读检查进入了验证循环: modelCalls=%d prompts=%d", calls.Load(), redundantVerificationPrompts.Load())
	}
}

func TestToolErrorPreventsEmptyResponseFromHidingFailure(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "",
				toolCall{ID: "upgrade", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get upgrade -y","purpose":"升级软件包"}`}},
				toolCall{ID: "extra", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get update","purpose":"重复操作"}`}},
			)
		default:
			writeModelResponse(t, w, "")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "升级软件包", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			commands.Add(1)
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动多工具任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "failed" {
			t.Fatalf("工具错误被空响应掩盖: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待多工具任务结束超时")
	}
	if commands.Load() != 1 || calls.Load() != 3 {
		t.Fatalf("多工具任务执行次数异常: commands=%d modelCalls=%d", commands.Load(), calls.Load())
	}
}

func TestEarlierCommandFailurePreventsLaterEmptyAutoCompletion(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "update", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get update","purpose":"刷新索引"}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "upgrade", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get upgrade -y","purpose":"升级软件包"}`}})
		default:
			writeModelResponse(t, w, "")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "更新并升级软件包", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if command == "apt-get update" {
				return CommandResult{Output: "repository unavailable", ExitCode: 100}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动失败恢复任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "failed" {
			t.Fatalf("早期失败被后续成功和空响应掩盖: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待失败恢复任务结束超时")
	}
	if commands.Load() != 2 || calls.Load() != 4 {
		t.Fatalf("失败恢复任务执行次数异常: commands=%d modelCalls=%d", commands.Load(), calls.Load())
	}
}

func TestKnownCurrentHostOSAllowsDirectPackageInstall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y htop","purpose":"安装 htop"}`}})
		default:
			writeModelResponse(t, w, "htop 已安装。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 htop", Environment{
		ListHosts: func() []Host {
			return []Host{{
				ID:        "host-1",
				Name:      "Debian 12",
				Address:   "192.0.2.1",
				Protocol:  "ssh",
				OSRelease: "ID=debian\nVERSION_ID=12\nVERSION_CODENAME=bookworm",
				Connected: true,
				Trusted:   true,
			}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			if command != "apt-get install -y htop" {
				t.Errorf("已知系统仍执行了多余命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return CommandResult{Output: "htop is already the newest version", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动已知系统直接安装任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "htop 已安装。" {
			t.Fatalf("已知系统未直接完成安装: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待已知系统直接安装完成超时")
	}
	if expected := []string{"apt-get install -y htop"}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("已知 Debian 不应重复检测系统: got=%v want=%v", commands, expected)
	}
	if calls.Load() != 2 {
		t.Fatalf("已知系统直接安装调用次数异常: modelCalls=%d", calls.Load())
	}
}

func TestPackageRemovalAliasCompletesAfterActualPackageRemoved(t *testing.T) {
	const (
		removeAlias  = "apt-get remove -y mtr"
		queryPackage = "dpkg-query -W -f='${binary:Package}\\t${db:Status-Abbrev}\\n' mtr mtr-tiny 2>/dev/null || true"
		removeActual = "apt-get remove -y mtr-tiny"
	)
	arguments := func(command, purpose string) string {
		raw, _ := json.Marshal(commandArguments{HostID: "host-1", Command: command, Purpose: purpose})
		return string(raw)
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "remove-alias", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(removeAlias, "卸载 mtr 软件包")}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "resolve-package", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(queryPackage, "定位提供 mtr 命令的软件包")}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "remove-actual", Type: "function", Function: toolFunction{Name: "run_command", Arguments: arguments(removeActual, "卸载实际提供 mtr 的软件包")}})
		default:
			writeModelResponse(t, w, "mtr 已卸载。")
		}
	}))
	defer server.Close()

	results := map[string]CommandResult{
		removeAlias:  {Output: "Package 'mtr' is not installed, so not removed\n", ExitCode: 0},
		queryPackage: {Output: "mtr\trc \nmtr-tiny\tii \n", ExitCode: 0},
		removeActual: {Output: "The following packages will be REMOVED:\n  mtr-tiny\nRemoving mtr-tiny (0.95-1) ...\n", ExitCode: 0},
	}
	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 3)
	var redundantVerificationPrompts atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "卸载 mtr", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "isif-香港-1", Address: "192.0.2.1", Protocol: "ssh", Connected: true, Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			result, ok := results[command]
			if !ok {
				t.Errorf("执行了非预期卸载命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return result, nil
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
				redundantVerificationPrompts.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动 mtr 卸载任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "mtr 已卸载。" {
			t.Fatalf("实际软件包卸载后未直接完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 mtr 卸载任务完成超时")
	}
	if expected := []string{removeAlias, queryPackage, removeActual}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("mtr 卸载流程异常: got=%v want=%v", commands, expected)
	}
	if calls.Load() != 4 || redundantVerificationPrompts.Load() != 0 {
		t.Fatalf("实际软件包卸载后仍进入验证循环: modelCalls=%d prompts=%d", calls.Load(), redundantVerificationPrompts.Load())
	}
}

func TestFinishOnSuccessDoesNotDependOnCommandNameOrOutputText(t *testing.T) {
	const commandToRun = "/opt/tools/apply-user-request --quiet"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if call := calls.Add(1); call != 1 {
			t.Errorf("通用命令成功后仍调用模型: call=%d", call)
			writeModelResponse(t, w, "不应继续调用模型")
			return
		}
		writeModelResponse(t, w, "", toolCall{ID: "apply", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"/opt/tools/apply-user-request --quiet","purpose":"完成用户要求的操作","finish_on_success":true}`}})
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "执行现有维护操作", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "isif-香港-1", Address: "192.0.2.1", Protocol: "ssh", Connected: true, Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			if command != commandToRun {
				t.Errorf("执行了非预期命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return CommandResult{Output: "arbitrary successful output\n", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动 iperf3 卸载任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("通用命令退出码 0 后未立即完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待通用命令任务完成超时")
	}
	if expected := []string{commandToRun}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("通用命令成功后仍执行了额外命令: got=%v want=%v", commands, expected)
	}
	if calls.Load() != 1 {
		t.Fatalf("通用命令成功后模型调用次数异常: %d", calls.Load())
	}
}

func TestCurrentHostAutomaticallyResolvesOSBeforeDirectPackageInstall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y htop","purpose":"安装 htop"}`}})
		default:
			writeModelResponse(t, w, "htop 已安装。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 1)
	var probes atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 htop", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "当前主机", Address: "192.0.2.1", Protocol: "ssh", Connected: true, Trusted: true}}
		},
		ReadOSRelease: func(context.Context, string) (string, error) {
			probes.Add(1)
			return "ID=debian\nVERSION_ID=12\nSANTIANSHELL_PACKAGE_MANAGER=apt", nil
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			return CommandResult{Output: "Setting up htop ...", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动自动系统识别安装任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "htop 已安装。" {
			t.Fatalf("自动识别系统后未直接完成安装: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待自动系统识别安装完成超时")
	}
	if probes.Load() != 1 {
		t.Fatalf("系统信息应由客户端静默读取一次: probes=%d", probes.Load())
	}
	if expected := []string{"apt-get install -y htop"}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("客户端静默识别后仍执行了多余命令: got=%v want=%v", commands, expected)
	}
}

func TestKnownCurrentHostOSRejectsWrongPackageManagerBeforeDirectInstall(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "wrong-install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apk add htop","purpose":"安装 htop"}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y htop","purpose":"安装 htop"}`}})
		default:
			writeModelResponse(t, w, "htop 已安装。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 htop", Environment{
		ListHosts: func() []Host {
			return []Host{{
				ID:        "host-1",
				Name:      "Debian 12",
				Address:   "192.0.2.1",
				Protocol:  "ssh",
				OSRelease: "ID=debian\nID_LIKE=debian\nVERSION_ID=12",
				Connected: true,
				Trusted:   true,
			}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			if command != "apt-get install -y htop" {
				t.Errorf("错误包管理器通过了 Debian 元数据门禁: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return CommandResult{Output: "Setting up htop ...", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动已知系统包管理器校验任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "htop 已安装。" {
			t.Fatalf("纠正包管理器后未完成安装: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待包管理器纠正任务完成超时")
	}
	if expected := []string{"apt-get install -y htop"}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("Debian 包管理器门禁异常: got=%v want=%v", commands, expected)
	}
	if calls.Load() != 3 {
		t.Fatalf("包管理器纠正调用次数异常: modelCalls=%d", calls.Load())
	}
}

func TestPackageInstallRequiresOSReleaseInspection(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "early-install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y curl","purpose":"安装 curl"}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "os", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"cat /etc/os-release","purpose":"识别真实发行版"}`}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y curl","purpose":"安装 curl"}`}})
		default:
			writeModelResponse(t, w, "curl 已安装。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 2)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 curl", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			if command == "cat /etc/os-release" {
				return CommandResult{Output: "ID=debian\nVERSION_ID=12", ExitCode: 0}, nil
			}
			return CommandResult{Output: "curl is already the newest version", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动安装任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "curl 已安装。" {
			t.Fatalf("安装任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待安装任务完成超时")
	}
	if expected := []string{"cat /etc/os-release", "apt-get install -y curl"}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("发行版门禁未阻止提前安装: commands=%v", commands)
	}
}

func TestPackageInstallMustMatchInspectedDistribution(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "os", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"cat /etc/os-release","purpose":"识别真实发行版"}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "wrong-install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y curl","purpose":"安装 curl"}`}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apk add curl","purpose":"安装 curl"}`}})
		default:
			writeModelResponse(t, w, "curl 已安装成功。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 2)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 curl", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			if command == "cat /etc/os-release" {
				return CommandResult{Output: "ID=alpine\nVERSION_ID=3.20", ExitCode: 0}, nil
			}
			return CommandResult{Output: "OK: curl installed", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动发行版校验任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "curl 已安装成功。" {
			t.Fatalf("发行版校验任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待发行版校验任务完成超时")
	}
	if expected := []string{"cat /etc/os-release", "apk add curl"}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("错误包管理器未被阻止: commands=%v", commands)
	}
}

func TestPackageInstallExitZeroDoesNotRequireVersionProbe(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: "{}"}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y iftop","purpose":"安装 iftop","finish_on_success":true}`}})
		default:
			writeModelResponse(t, w, "")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 1)
	var redundantVerificationPrompts atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 iftop", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "AK-日本", Address: "192.0.2.1", Protocol: "ssh", OSRelease: "ID=debian\nVERSION_ID=11", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			if command != "apt-get install -y iftop" {
				t.Errorf("安装成功后执行了非预期验证命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return CommandResult{Output: "Setting up iftop (1.0~pre4-7) ...\n", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
				redundantVerificationPrompts.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动 iftop 安装任务失败: %v", err)
	}

	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("iftop 安装退出码 0 后未直接完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 iftop 安装任务完成超时")
	}
	if expected := []string{"apt-get install -y iftop"}; !reflect.DeepEqual(commands, expected) {
		t.Fatalf("iftop 安装成功后仍执行版本或包状态探测: got=%v want=%v", commands, expected)
	}
	if calls.Load() != 2 || redundantVerificationPrompts.Load() != 0 {
		t.Fatalf("iftop 安装成功后仍进入验证循环: modelCalls=%d prompts=%d", calls.Load(), redundantVerificationPrompts.Load())
	}
}

func TestPackageQueryDoesNotHideFailedMutation(t *testing.T) {
	const packageCommand = "dpkg-query -s iftop"
	runArguments := func(command, purpose string) string {
		raw, _ := json.Marshal(commandArguments{HostID: "host-1", Command: command, Purpose: purpose})
		return string(raw)
	}

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: "{}"}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "os", Type: "function", Function: toolFunction{Name: "run_command", Arguments: runArguments("cat /etc/os-release", "识别 Linux 发行版")}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "update", Type: "function", Function: toolFunction{Name: "run_command", Arguments: runArguments("apt-get update", "刷新软件包索引")}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: runArguments("apt-get install -y iftop", "安装 iftop")}})
		case 5:
			writeModelResponse(t, w, "", toolCall{ID: "package", Type: "function", Function: toolFunction{Name: "run_command", Arguments: runArguments(packageCommand, "检查 iftop 软件包状态")}})
		default:
			writeModelResponse(t, w, "iftop 已安装成功。")
		}
	}))
	defer server.Close()

	results := map[string]CommandResult{
		"cat /etc/os-release": {Output: "ID=debian\nVERSION_ID=11", ExitCode: 0},
		"apt-get update":      {Output: "Reading package lists... Done", ExitCode: 0},
		"apt-get install -y iftop": {
			Output:   "E: Failed to fetch package",
			ExitCode: 100,
		},
		packageCommand: {
			Output:   "Package: iftop\nStatus: install ok installed\nVersion: 1.0~pre4-7\n",
			ExitCode: 0,
		},
	}
	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 4)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 iftop", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "AK-日本", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			result, ok := results[command]
			if !ok {
				t.Errorf("执行了非预期命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
			return result, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动失败修改保护测试失败: %v", err)
	}

	select {
	case event := <-finished:
		if event.Type != "failed" || !strings.Contains(event.Message, "退出码") || !strings.Contains(event.Message, "100") || !strings.Contains(event.Message, "Failed to fetch package") {
			t.Fatalf("失败的安装被后续包查询掩盖: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待失败修改保护测试完成超时")
	}

	expectedCommands := []string{
		"cat /etc/os-release",
		"apt-get update",
		"apt-get install -y iftop",
		packageCommand,
	}
	if !reflect.DeepEqual(commands, expectedCommands) {
		t.Fatalf("失败修改后执行了非预期命令: got=%v want=%v", commands, expectedCommands)
	}
}

func TestCompletionClaimWithoutExecutionEvidenceIsRejected(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "curl 已安装成功。")
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "os", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"cat /etc/os-release","purpose":"识别真实发行版"}`}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "install", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"apt-get install -y curl","purpose":"安装 curl"}`}})
		default:
			writeModelResponse(t, w, "curl 已安装成功。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	var evidenceWarnings atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 curl", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if command == "cat /etc/os-release" {
				return CommandResult{Output: "ID=ubuntu\nVERSION_ID=24.04", ExitCode: 0}, nil
			}
			return CommandResult{Output: "Setting up curl", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "缺少真实执行证据") {
				evidenceWarnings.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动证据门禁任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "curl 已安装成功。" {
			t.Fatalf("证据门禁任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待证据门禁任务完成超时")
	}
	if commands.Load() != 2 || evidenceWarnings.Load() != 1 || calls.Load() != 5 {
		t.Fatalf("空口成功结论未被纠正: commands=%d warnings=%d modelCalls=%d", commands.Load(), evidenceWarnings.Load(), calls.Load())
	}
}

func TestFailedMutationCannotBeReportedAsSuccess(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "restart", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl restart nginx","purpose":"重启 nginx"}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "unrelated", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"uname -a","purpose":"读取系统信息"}`}})
		case 4:
			writeModelResponse(t, w, "nginx 已修复并正常运行。")
		default:
			writeModelResponse(t, w, "nginx 修复失败：重启命令退出码为 1。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "修复 nginx", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			if command == "systemctl restart nginx" {
				return CommandResult{Output: "restart failed", ExitCode: 1}, nil
			}
			return CommandResult{Output: "Linux test", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动失败修改测试任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "failed" || !strings.Contains(event.Message, "退出码") || !strings.Contains(event.Message, "1") || !strings.Contains(event.Message, "restart failed") {
			t.Fatalf("失败修改被错误报告为成功: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待失败修改测试完成超时")
	}
	if calls.Load() > 4 {
		t.Fatalf("失败修改仍进入了多余循环: modelCalls=%d", calls.Load())
	}
}

func TestPostVerificationPolicySeparatesOrdinaryAndCriticalOperations(t *testing.T) {
	tests := []struct {
		name     string
		question string
		command  string
		want     bool
	}{
		{name: "普通安装", question: "安装 curl", command: "apt-get install -y curl", want: false},
		{name: "普通文件修改", question: "修改应用配置", command: "sed -i 's/^workers=.*/workers=4/' /etc/example-app.conf", want: false},
		{name: "普通权限修改", question: "修改配置权限", command: "chmod 600 /etc/example-app.conf", want: false},
		{name: "普通内核优化", question: "将 swappiness 设置为 10", command: "sysctl -w vm.swappiness=10", want: false},
		{name: "普通服务重启", question: "重启 nginx", command: "systemctl restart nginx", want: false},
		{name: "修复服务并确认可用", question: "修复 nginx 并确保恢复正常", command: "systemctl restart nginx", want: true},
		{name: "修改SSH配置", question: "修改 SSH 端口", command: "sed -i 's/^#Port 22/Port 2222/' /etc/ssh/sshd_config", want: true},
		{name: "重启SSH服务", question: "重启 SSH 服务", command: "systemctl restart sshd", want: true},
		{name: "防火墙", question: "开放 443 端口", command: "iptables -A INPUT -p tcp --dport 443 -j ACCEPT", want: true},
		{name: "磁盘", question: "格式化数据盘", command: "mkfs.ext4 /dev/vdb1", want: true},
		{name: "服务器重启恢复", question: "重启服务器并确认恢复", command: "reboot", want: true},
		{name: "迁移一致性", question: "迁移网站到新服务器并校验一致性", command: "rsync -a /srv/www/ /srv/migration/", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requiresPostVerification(test.question, test.command); got != test.want {
				t.Fatalf("后验验证策略错误: question=%q command=%q got=%v want=%v", test.question, test.command, got, test.want)
			}
		})
	}
}

func TestPostVerificationMustMatchPendingOperation(t *testing.T) {
	tests := []struct {
		name         string
		pending      string
		verification string
		want         bool
	}{
		{name: "服务状态匹配", pending: "systemctl restart nginx", verification: "systemctl is-active nginx", want: true},
		{name: "同主机无关信息不匹配", pending: "systemctl restart nginx", verification: "uname -a", want: false},
		{name: "迁移目录检查匹配", pending: "transfer_path /srv/site", verification: "stat /srv/site", want: true},
		{name: "SSH语法检查匹配", pending: "sed -i 's/22/2222/' /etc/ssh/sshd_config", verification: "sshd -t", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := verificationCommandMatches(test.pending, test.verification); got != test.want {
				t.Fatalf("验证命令匹配错误: pending=%q verification=%q got=%v want=%v", test.pending, test.verification, got, test.want)
			}
		})
	}
}

func TestUnrelatedReadOnlyCommandCannotClearPendingServiceVerification(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "restart", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl restart nginx","purpose":"修复并重启 nginx"}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "unrelated", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"uname -a","purpose":"读取系统信息"}`}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "verify", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl is-active nginx","purpose":"验证 nginx 已恢复运行"}`}})
		default:
			writeModelResponse(t, w, "nginx 已修复并恢复正常运行。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 3)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "修复 nginx 并确保可用", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			switch command {
			case "systemctl restart nginx":
				return CommandResult{ExitCode: 0}, nil
			case "uname -a":
				return CommandResult{Output: "Linux test 6.1.0 x86_64\n", ExitCode: 0}, nil
			case "systemctl is-active nginx":
				return CommandResult{Output: "active\n", ExitCode: 0}, nil
			default:
				t.Errorf("执行了非预期命令: %q", command)
				return CommandResult{Output: "unexpected command", ExitCode: 127}, nil
			}
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动服务修复验证任务失败: %v", err)
	}

	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "nginx 已修复并恢复正常运行。" {
			t.Fatalf("服务验证未按目标完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待服务修复验证完成超时")
	}
	wantCommands := []string{"systemctl restart nginx", "uname -a", "systemctl is-active nginx"}
	if !reflect.DeepEqual(commands, wantCommands) {
		t.Fatalf("无关只读命令错误清除了待验证状态: got=%v want=%v", commands, wantCommands)
	}
	if calls.Load() != 5 {
		t.Fatalf("服务修复验证模型调用次数异常: %d", calls.Load())
	}
}

func TestCompletionEvidenceRejectsNegatedFailureSuccessClaims(t *testing.T) {
	tests := []string{
		"没有失败，任务已完成。",
		"未完成项：无，已完成。",
		"没有失败，检查完成，状态正常。",
	}
	for _, content := range tests {
		if issue := completionEvidenceIssue(content, 0, 0, nil); issue == "" {
			t.Fatalf("矛盾的成功结论绕过证据门禁: %q", content)
		}
	}
}

func TestCompletionEvidenceAllowsExplicitFailureReport(t *testing.T) {
	for _, content := range []string{
		"nginx 修复失败：服务启动命令退出码为 1。",
		"任务未完成，当前无法连接目标主机。",
	} {
		if issue := completionEvidenceIssue(content, 0, 0, nil); issue != "" {
			t.Fatalf("明确失败说明不应继续索取成功证据: content=%q issue=%q", content, issue)
		}
	}
}
