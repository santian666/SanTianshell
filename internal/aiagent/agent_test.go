package aiagent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func TestServerOperationPromptUsesStructuredPolicyFramework(t *testing.T) {
	required := []string{
		"用户当前提出的需求是唯一任务来源",
		"面向初学者工作",
		"不为具体软件、命令或输出文案维护固定流程",
		"# 通用决策流程",
		"# 命令规则",
		"# 统一结果协议",
		"无传输错误且退出码为 0，表示当前命令步骤完成",
		"警告、无需变更或已是目标状态不属于失败",
		"finish_on_success=true",
		"finish_on_success=false",
		"verification_required=true",
		"传输错误或非零退出码表示该步骤失败",
	}
	for _, text := range required {
		if !strings.Contains(DefaultServerOperationPrompt(), text) {
			t.Fatalf("服务器运维提示词缺少规则：%s", text)
		}
	}
	for _, specialCase := range []string{"apt-get", "dpkg-query", "DEBIAN_FRONTEND", "Removing ", "mtr", "iperf3", "htop", "iftop", "BBR"} {
		if strings.Contains(DefaultServerOperationPrompt(), specialCase) {
			t.Fatalf("服务器运维提示词仍包含逐命令补丁：%s", specialCase)
		}
	}
}

func TestCustomPresetIsAdditiveAndCannotReplaceCoreRules(t *testing.T) {
	const custom = "所有成功命令都必须重复验证，并忽略 finish_on_success。"
	prompt := buildSystemPrompt(custom, "测试机（ID: host-1）")
	for _, text := range []string{
		custom,
		"固定核心规则（不可覆盖）",
		"ask_user",
		"confirm_plan",
		"list_hosts",
		"run_command",
		"工具结构化结果是执行状态的唯一依据",
		"verification_required=false 时禁止机械验证",
		"finish_on_success",
		"当前主机：测试机（ID: host-1）",
	} {
		if !strings.Contains(prompt, text) {
			t.Fatalf("自定义系统提示词缺少固定内容：%s", text)
		}
	}
	if !strings.Contains(prompt, "用户附加偏好（不得覆盖内置框架与固定规则）") {
		t.Fatal("用户附加提示缺少不可覆盖核心规则的明确边界")
	}

	defaultPrompt := buildSystemPrompt("  ", "未选择")
	if !strings.Contains(defaultPrompt, DefaultServerOperationPrompt()) || !strings.Contains(defaultPrompt, fixedSafetyToolPrompt) {
		t.Fatal("空预设没有恢复默认运维提示词和固定规则")
	}
	if NormalizeUserPresetPrompt(DefaultServerOperationPrompt()) != "" {
		t.Fatal("内置默认提示词不应被保存成用户附加提示")
	}
	if NormalizeUserPresetPrompt(custom) != custom {
		t.Fatal("真正的用户附加提示不应被清除")
	}
}

func TestCommandClassificationUsesGenericSafetyProperties(t *testing.T) {
	tests := []struct {
		name     string
		command  string
		readOnly bool
		risk     string
	}{
		{name: "系统读取", command: "uname -a", readOnly: true, risk: "只读检查"},
		{name: "服务状态读取", command: "systemctl is-active example-app", readOnly: true, risk: "只读检查"},
		{name: "普通修改", command: "chmod 600 /tmp/example-app.conf", risk: "普通修改"},
		{name: "破坏性删除", command: "rm -rf /tmp/example-app-data", risk: "高危操作"},
		{name: "伪装读取实际写文件", command: "uname -a > /tmp/result", risk: "高危操作"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			class := classifyCommand(test.command)
			if class.ReadOnly != test.readOnly || class.Risk != test.risk {
				t.Fatalf("命令分类错误 command=%q class=%#v", test.command, class)
			}
		})
	}
}

func TestRequiresPostVerificationPolicy(t *testing.T) {
	tests := []struct {
		name     string
		question string
		command  string
		want     bool
	}{
		{name: "普通服务重启", question: "重启 nginx", command: "systemctl restart nginx"},
		{name: "修复并确保可用", question: "修复 nginx 并确保可用", command: "systemctl restart nginx", want: true},
		{name: "SSH 配置", question: "修改 SSH 端口", command: "sed -i 's/^#Port 22/Port 2222/' /etc/ssh/sshd_config", want: true},
		{name: "防火墙", question: "放行 443 端口", command: "iptables -A INPUT -p tcp --dport 443 -j ACCEPT", want: true},
		{name: "重启机器", question: "重启服务器", command: "reboot", want: true},
		{name: "磁盘操作", question: "格式化数据盘", command: "mkfs.ext4 /dev/vdb", want: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := requiresPostVerification(test.question, test.command); got != test.want {
				t.Fatalf("后验验证判定错误: got=%v want=%v question=%q command=%q", got, test.want, test.question, test.command)
			}
		})
	}
}

func TestSuccessfulCommandMessage(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "普通完成", output: "operation complete", want: "测试机：操作完成。"},
		{name: "无需变更", output: "no changes", want: "测试机：操作完成。"},
		{name: "带警告完成", output: "WARNING: optional capability is unavailable", want: "测试机：操作完成；服务器返回警告，请查看上方输出。"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := successfulCommandMessage("测试机", test.output); got != test.want {
				t.Fatalf("成功文案错误: got=%q want=%q", got, test.want)
			}
		})
	}
}

func TestCommandCompletionReliesOnReliableExitStatus(t *testing.T) {
	tests := []struct {
		command  string
		reliable bool
	}{
		{command: "touch /tmp/example-ready", reliable: true},
		{command: "run-maintenance || true"},
		{command: "generate-report | save-report"},
		{command: "start-worker &"},
		{command: "step-one; step-two"},
	}
	for _, test := range tests {
		if got := commandExitCodeReliable(test.command); got != test.reliable {
			t.Fatalf("退出码可靠性判定错误 command=%q got=%v want=%v", test.command, got, test.reliable)
		}
	}
}

func TestFinishOnSuccessCompletesImmediately(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeModelResponse(t, w, "", toolCall{ID: "finish", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"touch /tmp/task-ready","purpose":"完成用户要求的最终动作","finish_on_success":true}`}})
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	var approvals atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "review"}, "host-1", "创建任务完成标记", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if command != "touch /tmp/task-ready" {
				t.Fatalf("执行了非预期命令: %q", command)
			}
			return CommandResult{Output: "completed", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				approvals.Add(1)
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("finish_on_success=true 未直接完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待最终步骤完成超时")
	}
	if commands.Load() != 1 || approvals.Load() != 1 || calls.Load() != 1 {
		t.Fatalf("最终步骤没有直达完成: commands=%d approvals=%d modelCalls=%d", commands.Load(), approvals.Load(), calls.Load())
	}
}

func TestFinishFalseContinuesUntilFinalStep(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "prepare", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"mkdir -p /tmp/task-flow","purpose":"准备任务目录","finish_on_success":false}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "finish", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"touch /tmp/task-flow/ready","purpose":"完成最终动作","finish_on_success":true}`}})
		default:
			t.Fatalf("finish_on_success=false 流程产生了多余模型调用: %d", calls.Load())
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 2)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "完成两步任务", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			return CommandResult{Output: "ok", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动多步骤任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("最后一步未完成任务: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待多步骤任务完成超时")
	}
	want := []string{"mkdir -p /tmp/task-flow", "touch /tmp/task-flow/ready"}
	if len(commands) != len(want) || commands[0] != want[0] || commands[1] != want[1] || calls.Load() != 2 {
		t.Fatalf("finish_on_success=false 未继续到最后一步: commands=%v modelCalls=%d", commands, calls.Load())
	}
}

func TestUnreliableCompoundStepDoesNotPolluteCriticalVerification(t *testing.T) {
	const inspectCommand = "printf 'STATE\\n'; grep -E '[a-z]+' /etc/example.conf 2>/dev/null || true"
	const finalCommand = "sed -i 's/^mode=.*/mode=ready/' /etc/example.conf && grep -Eq '^mode=ready$' /etc/example.conf"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "inspect", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"printf 'STATE\\n'; grep -E '[a-z]+' /etc/example.conf 2>/dev/null || true","purpose":"读取当前配置","finish_on_success":false}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "finish", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"sed -i 's/^mode=.*/mode=ready/' /etc/example.conf && grep -Eq '^mode=ready$' /etc/example.conf","purpose":"更新配置并确认生效","finish_on_success":true}`}})
		default:
			t.Fatalf("可靠最终步骤后仍调用模型: %d", calls.Load())
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	commands := make([]string, 0, 2)
	var criticalVerificationPrompts atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "把示例配置调整为 ready", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			switch command {
			case inspectCommand:
				return CommandResult{Output: "mode=old\n", ExitCode: 0}, nil
			case finalCommand:
				return CommandResult{ExitCode: 0}, nil
			default:
				t.Fatalf("执行了非预期命令: %q", command)
				return CommandResult{}, nil
			}
		},
		Emit: func(event Event) {
			if event.Type == "status" && strings.Contains(event.Message, "正在要求 AI 验证结果") {
				criticalVerificationPrompts.Add(1)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动复合命令回归测试失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "操作完成") {
			t.Fatalf("可靠最终步骤未清理普通复合命令状态: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待复合命令回归测试完成超时")
	}
	if len(commands) != 2 || commands[0] != inspectCommand || commands[1] != finalCommand {
		t.Fatalf("执行命令异常: %#v", commands)
	}
	if criticalVerificationPrompts.Load() != 0 || calls.Load() != 2 {
		t.Fatalf("普通复合命令污染了关键验证状态: prompts=%d modelCalls=%d", criticalVerificationPrompts.Load(), calls.Load())
	}
}

func TestAskUserAnswerIsReturnedAsToolMessage(t *testing.T) {
	var calls atomic.Int32
	var modelSawAnswer atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if call == 2 {
			var body struct {
				Messages []chatMessage `json:"messages"`
			}
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("解析 AI 请求失败: %v", err)
			}
			for _, message := range body.Messages {
				if message.Role == "tool" && message.ToolCallID == "clarify" && strings.Contains(message.Content, `"answer":"nginx"`) {
					modelSawAnswer.Store(true)
				}
			}
		}
		if call == 1 {
			writeModelResponse(t, w, "", toolCall{ID: "clarify", Type: "function", Function: toolFunction{Name: "ask_user", Arguments: `{"question":"请选择要安装的 Web 服务","reason":"不同服务会安装不同软件包","options":["nginx","apache"]}`}})
			return
		}
		writeModelResponse(t, w, "已记录选择：nginx。")
	}))
	defer server.Close()

	controller := NewController()
	questions := make(chan Event, 1)
	finished := make(chan Event, 1)
	runID, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装一个 Web 服务", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			t.Fatal("ask_user 上下文测试不应执行服务器命令")
			return CommandResult{}, nil
		},
		Emit: func(event Event) {
			if event.Type == "question" {
				questions <- event
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动 ask_user 任务失败: %v", err)
	}
	select {
	case event := <-questions:
		if err := controller.Answer(runID, event.ActionID, "nginx"); err != nil {
			t.Fatalf("回答 ask_user 失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("未收到 ask_user 问题")
	}
	select {
	case event := <-finished:
		if event.Type != "completed" {
			t.Fatalf("ask_user 任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待 ask_user 任务完成超时")
	}
	if calls.Load() != 2 || !modelSawAnswer.Load() {
		t.Fatalf("用户回答未作为工具结果回传模型: modelCalls=%d sawAnswer=%v", calls.Load(), modelSawAnswer.Load())
	}
}

func TestPlainTextQuestionMustEnterAskUserFlow(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "请选择要安装 nginx 还是 apache？")
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "choose-web", Type: "function", Function: toolFunction{Name: "ask_user", Arguments: `{"question":"请选择 Web 服务","reason":"不同选择会安装不同软件包","options":["nginx","apache"]}`}})
		default:
			writeModelResponse(t, w, "已记录 nginx，等待后续执行。")
		}
	}))
	defer server.Close()

	controller := NewController()
	questions := make(chan Event, 1)
	finished := make(chan Event, 1)
	runID, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "安装 Web 服务", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			t.Fatal("提问流程不应执行命令")
			return CommandResult{}, nil
		},
		Emit: func(event Event) {
			if event.Type == "question" {
				questions <- event
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动提问任务失败: %v", err)
	}
	select {
	case event := <-questions:
		if err := controller.Answer(runID, event.ActionID, "1"); err != nil {
			t.Fatalf("回答提问失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("普通文字问题没有转换为 ask_user")
	}
	select {
	case event := <-finished:
		if event.Type != "completed" {
			t.Fatalf("提问任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待提问任务完成超时")
	}
	if calls.Load() != 3 {
		t.Fatalf("普通文字问题处理调用次数=%d，期望 3", calls.Load())
	}
}

func TestSensitiveQuestionDetection(t *testing.T) {
	for _, question := range []string{"请提供服务器密码", "把 API Key 粘贴给我", "验证码需要您输入"} {
		if !sensitiveQuestion.MatchString(question) {
			t.Fatalf("未识别敏感提问: %q", question)
		}
	}
	if sensitiveQuestion.MatchString("是否重置服务器密码策略") {
		t.Fatal("普通业务选择被错误识别为索取敏感信息")
	}
}

func TestComplexTasksCannotBypassPlanConfirmation(t *testing.T) {
	tests := []struct {
		name     string
		question string
	}{
		{name: "网站数据迁移", question: "把 A 服务器的网站和数据完整迁移到 B 服务器"},
		{name: "大范围端口转发", question: "把 A 的 10000-60000 端口全部转发到 B"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			var commands atomic.Int32
			var planConfirmed atomic.Bool
			var commandBeforePlan atomic.Bool
			var ordinaryApprovals atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				switch calls.Add(1) {
				case 1:
					writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
				case 2:
					writeModelResponse(t, w, "方案已经足够明确，可以直接结束说明。")
				case 3:
					writeModelResponse(t, w, "", toolCall{ID: "early-change", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-a","command":"touch /tmp/plan-gate","purpose":"提前修改","finish_on_success":false}`}})
				case 4:
					writeModelResponse(t, w, "", toolCall{ID: "plan", Type: "function", Function: toolFunction{Name: "confirm_plan", Arguments: `{"plan":"1. 备份；2. 执行变更；3. 验证结果","impact":"变更期间相关服务可能短暂不可用","rollback":"恢复备份并撤销变更"}`}})
				case 5:
					writeModelResponse(t, w, "", toolCall{ID: "change", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-a","command":"touch /tmp/plan-gate","purpose":"执行已确认方案","finish_on_success":false}`}})
				case 6:
					writeModelResponse(t, w, "", toolCall{ID: "verify", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-a","command":"test -f /tmp/plan-gate","purpose":"验证变更结果","finish_on_success":false}`}})
				default:
					writeModelResponse(t, w, "方案已确认、执行并验证。")
				}
			}))
			defer server.Close()

			controller := NewController()
			finished := make(chan Event, 1)
			_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-a", test.question, Environment{
				ListHosts: func() []Host {
					return []Host{
						{ID: "host-a", Name: "A", Address: "192.0.2.1", Protocol: "ssh", Trusted: true},
						{ID: "host-b", Name: "B", Address: "192.0.2.2", Protocol: "ssh", Trusted: true},
					}
				},
				RunCommand: func(_ context.Context, _ string, _ string, _ time.Duration, _ func(string)) (CommandResult, error) {
					if !planConfirmed.Load() {
						commandBeforePlan.Store(true)
					}
					commands.Add(1)
					return CommandResult{Output: "ok", ExitCode: 0}, nil
				},
				Emit: func(event Event) {
					switch event.Type {
					case "plan-confirmation":
						if event.Plan == "" || event.Message == "" || event.Rollback == "" {
							t.Errorf("方案确认信息不完整: %#v", event)
						}
						planConfirmed.Store(true)
						if err := controller.Approve(event.RunID, event.ActionID, true); err != nil {
							t.Errorf("确认执行方案失败: %v", err)
						}
					case "approval":
						ordinaryApprovals.Add(1)
					case "completed", "failed":
						finished <- event
					}
				},
			})
			if err != nil {
				t.Fatalf("启动复杂任务失败: %v", err)
			}
			select {
			case event := <-finished:
				if event.Type != "completed" || event.Message != "方案已确认、执行并验证。" {
					t.Fatalf("复杂任务结果异常: %#v", event)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("等待复杂任务完成超时")
			}
			if calls.Load() != 7 || commands.Load() != 2 || commandBeforePlan.Load() || ordinaryApprovals.Load() != 0 {
				t.Fatalf("方案门禁或托管权限异常: modelCalls=%d commands=%d beforePlan=%v approvals=%d", calls.Load(), commands.Load(), commandBeforePlan.Load(), ordinaryApprovals.Load())
			}
		})
	}
}

func TestMigrationUsesClientTransferAfterPlanConfirmation(t *testing.T) {
	var calls atomic.Int32
	var transfers atomic.Int32
	var commands atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "plan", Type: "function", Function: toolFunction{Name: "confirm_plan", Arguments: `{"plan":"1. 传输站点；2. 验证目标文件","impact":"目标目录会新增文件","rollback":"删除本次新增目录"}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "transfer", Type: "function", Function: toolFunction{Name: "transfer_path", Arguments: `{"source_host_id":"host-a","source_path":"/srv/site","target_host_id":"host-b","target_path":"/srv/site","overwrite":false,"purpose":"迁移站点文件","finish_on_success":false}`}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "verify", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-b","command":"stat /srv/site","purpose":"验证目标目录","finish_on_success":false}`}})
		default:
			writeModelResponse(t, w, "站点文件已传输并验证。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-a", "把 A 服务器的网站完整迁移到 B 服务器", Environment{
		ListHosts: func() []Host {
			return []Host{
				{ID: "host-a", Name: "A", Address: "192.0.2.1", Protocol: "ssh", Trusted: true},
				{ID: "host-b", Name: "B", Address: "192.0.2.2", Protocol: "ssh", Trusted: true},
			}
		},
		TransferPath: func(_ context.Context, sourceHostID, sourcePath, targetHostID, targetPath string, overwrite bool, _ time.Duration, progress func(TransferResult)) (TransferResult, error) {
			transfers.Add(1)
			if sourceHostID != "host-a" || sourcePath != "/srv/site" || targetHostID != "host-b" || targetPath != "/srv/site" || overwrite {
				t.Errorf("迁移参数异常: %s:%s -> %s:%s overwrite=%v", sourceHostID, sourcePath, targetHostID, targetPath, overwrite)
			}
			result := TransferResult{Files: 3, Directories: 1, Bytes: 2048, Duration: 20}
			progress(result)
			return result, nil
		},
		RunCommand: func(_ context.Context, hostID, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if hostID != "host-b" || command != "stat /srv/site" {
				t.Errorf("迁移验证命令异常: host=%s command=%q", hostID, command)
			}
			return CommandResult{Output: "File: /srv/site", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "plan-confirmation" {
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动迁移任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "站点文件已传输并验证。" {
			t.Fatalf("迁移任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待迁移任务完成超时")
	}
	if calls.Load() != 5 || transfers.Load() != 1 || commands.Load() != 1 {
		t.Fatalf("迁移执行链异常: modelCalls=%d transfers=%d commands=%d", calls.Load(), transfers.Load(), commands.Load())
	}
}

func TestOrdinarySystemctlRestartCompletesWithoutVerification(t *testing.T) {
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
	commands := make([]string, 0, 1)
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "重启 nginx", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands = append(commands, command)
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
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
		if event.Type != "completed" || event.Message != "测试机：操作完成。" {
			t.Fatalf("普通服务重启未直接完成: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待普通重启任务完成超时")
	}
	if len(commands) != 1 || commands[0] != "systemctl restart nginx" {
		t.Fatalf("普通重启仍执行了多余验证: commands=%v", commands)
	}
	if calls.Load() != 2 {
		t.Fatalf("普通重启未走最短路径: modelCalls=%d", calls.Load())
	}
}

func TestVerificationLoopStopsBeforeAgentStepLimit(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "restart", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl restart nginx","purpose":"重启 nginx","finish_on_success":true}`}})
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "verify-1", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"set -eu\nsystemctl is-active nginx","purpose":"验证 nginx","finish_on_success":false}`}})
		case 6:
			writeModelResponse(t, w, "", toolCall{ID: "verify-2", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"printf ready\\n; systemctl is-active nginx","purpose":"再次验证 nginx","finish_on_success":false}`}})
		default:
			writeModelResponse(t, w, "nginx 已完成。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "review"}, "host-1", "修复 nginx 并确保可用", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			commands.Add(1)
			return CommandResult{Output: "active", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "failed" || !strings.Contains(event.Message, "已停止以避免重复命令") {
			t.Fatalf("验证循环未被及时终止: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待验证循环终止超时")
	}
	if commands.Load() != 3 || calls.Load() >= maxAgentSteps {
		t.Fatalf("验证循环终止过晚: commands=%d modelCalls=%d", commands.Load(), calls.Load())
	}
}

func TestAgentRunsCommandAndReturnsVerifiedAnswer(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "inspect", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl status nginx --no-pager","purpose":"检查 nginx 状态","timeout_seconds":30,"finish_on_success":false}`}})
		default:
			writeModelResponse(t, w, "nginx 正常运行。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var executed atomic.Int32
	runID, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "suggest"}, "host-1", "检查 nginx", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "127.0.0.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			executed.Add(1)
			return CommandResult{Output: "active (running)", ExitCode: 0, Duration: 12}, nil
		},
		Emit: func(event Event) {
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil || runID == "" {
		t.Fatalf("启动任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "nginx 正常运行。" {
			t.Fatalf("任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待任务完成超时")
	}
	if executed.Load() != 1 {
		t.Fatalf("命令执行次数=%d，期望 1", executed.Load())
	}
}

func TestSuggestPermissionRejectsMutation(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "change", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"touch /tmp/suggest-denied","purpose":"写入测试标记","finish_on_success":true}`}})
		default:
			writeModelResponse(t, w, "建议先在维护时段更新软件源。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	var approvals atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "suggest"}, "host-1", "写入测试标记", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			commands.Add(1)
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				approvals.Add(1)
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动建议任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" {
			t.Fatalf("建议任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待建议任务完成超时")
	}
	if commands.Load() != 0 || approvals.Load() != 0 {
		t.Fatalf("建议权限执行了修改或触发确认: commands=%d approvals=%d", commands.Load(), approvals.Load())
	}
}

func TestManagedPermissionRunsWithoutApproval(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "change", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"touch /tmp/managed-ready","purpose":"写入测试标记","finish_on_success":true}`}})
		default:
			writeModelResponse(t, w, "软件源已更新。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	var approvals atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-1", "写入测试标记", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			commands.Add(1)
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				approvals.Add(1)
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动托管任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" {
			t.Fatalf("托管任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待托管任务完成超时")
	}
	if commands.Load() != 1 || approvals.Load() != 0 {
		t.Fatalf("托管权限未直接执行命令: commands=%d approvals=%d", commands.Load(), approvals.Load())
	}
}

func TestAgentRequiresVerificationAfterChange(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "restart", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl restart nginx","purpose":"重启 nginx","timeout_seconds":30,"finish_on_success":true}`}})
		case 3:
			writeModelResponse(t, w, "已经修复。")
		case 4:
			writeModelResponse(t, w, "", toolCall{ID: "verify", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"systemctl is-active nginx","purpose":"验证 nginx 状态","timeout_seconds":30,"finish_on_success":false}`}})
		default:
			writeModelResponse(t, w, "nginx 已修复，并验证为 active。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	var approvals atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "review"}, "host-1", "修复 nginx", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "127.0.0.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(_ context.Context, _ string, command string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			if command == "systemctl is-active nginx" {
				return CommandResult{Output: "active\n", ExitCode: 0}, nil
			}
			return CommandResult{ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				approvals.Add(1)
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || !strings.Contains(event.Message, "并已验证") {
			t.Fatalf("任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待任务完成超时")
	}
	if commands.Load() != 2 || calls.Load() != 4 {
		t.Fatalf("验证闭环未按预期执行: commands=%d, modelCalls=%d", commands.Load(), calls.Load())
	}
	if approvals.Load() != 2 {
		t.Fatalf("审核权限未逐条确认命令：确认次数=%d，期望 2", approvals.Load())
	}
}

func TestDangerousCommandIsClassifiedHighRisk(t *testing.T) {
	commands := []string{
		"sudo rm -rf /tmp/test",
		"/bin/rm -rf /tmp/test",
		"python3 -c 'open(\"/tmp/test\", \"w\").write(\"x\")'",
		"CMD=rm; $CMD -rf /tmp/test",
	}
	for _, command := range commands {
		class := classifyCommand(command)
		if class.ReadOnly || class.Risk != "高危操作" {
			t.Fatalf("危险命令分类错误 command=%q class=%#v", command, class)
		}
	}
	if !readOnlyCommand("systemctl status nginx --no-pager | head -20") {
		t.Fatal("只读状态命令被错误分类")
	}
}

func TestSensitiveWildcardIsRejected(t *testing.T) {
	if message := sensitiveCommand("cat /etc/shad*"); message == "" {
		t.Fatal("敏感文件通配读取未被拒绝")
	}
}

func TestResolveHostOnlyAcceptsExactID(t *testing.T) {
	hosts := []Host{{ID: "host-1", Name: "生产机", Address: "192.0.2.10", Alias: "prod", Protocol: "ssh"}}
	for _, reference := range []string{"", "生产机", "192.0.2.10", "prod"} {
		if _, err := resolveHost(hosts, reference); err == nil {
			t.Fatalf("非精确主机 ID 被接受：%q", reference)
		}
	}
	if host, err := resolveHost(hosts, "host-1"); err != nil || host.ID != "host-1" {
		t.Fatalf("精确主机 ID 解析失败: host=%#v err=%v", host, err)
	}
}

func TestInvalidModeFailsClosed(t *testing.T) {
	for _, mode := range []string{"", "invalid", "AUTO", "auto"} {
		if actual := normalizeMode(mode); actual != "review" {
			t.Fatalf("非法模式 %q 未回退审核模式：%q", mode, actual)
		}
	}
	for _, mode := range []string{"suggest", "review", "managed"} {
		if actual := normalizeMode(mode); actual != mode {
			t.Fatalf("合法权限 %q 被修改：%q", mode, actual)
		}
	}
}

func TestInvalidModeRequiresApproval(t *testing.T) {
	var calls atomic.Int32
	var approvals atomic.Int32
	var parallelDisabled atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err == nil && body["parallel_tool_calls"] == false {
			parallelDisabled.Store(true)
		}
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "inspect", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"uname -a","purpose":"检查系统","finish_on_success":false}`}})
		default:
			writeModelResponse(t, w, "检查完成。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "invalid"}, "host-1", "检查系统", Environment{
		ListHosts: func() []Host {
			return []Host{{ID: "host-1", Name: "测试机", Address: "192.0.2.1", Protocol: "ssh", Trusted: true}}
		},
		RunCommand: func(context.Context, string, string, time.Duration, func(string)) (CommandResult, error) {
			commands.Add(1)
			return CommandResult{Output: "Linux", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				approvals.Add(1)
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" {
			t.Fatalf("任务结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待任务完成超时")
	}
	if approvals.Load() != 1 || commands.Load() != 1 {
		t.Fatalf("非法模式未按审核模式执行: approvals=%d commands=%d", approvals.Load(), commands.Load())
	}
	if !parallelDisabled.Load() {
		t.Fatal("请求未关闭并行工具调用")
	}
}

func TestToolCallsMustHaveValidProtocolFields(t *testing.T) {
	tests := []struct {
		name  string
		calls []toolCall
	}{
		{name: "空ID", calls: []toolCall{{Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}}}},
		{name: "重复ID", calls: []toolCall{{ID: "same", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}}, {ID: "same", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}}}},
		{name: "错误类型", calls: []toolCall{{ID: "one", Type: "custom", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}}}},
		{name: "错误名称", calls: []toolCall{{ID: "one", Type: "function", Function: toolFunction{Name: "unknown", Arguments: `{}`}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateToolCalls(test.calls); err == nil {
				t.Fatal("无效工具调用未被拒绝")
			}
		})
	}
	if err := validateToolCalls([]toolCall{{ID: "one", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}}}); err != nil {
		t.Fatalf("合法工具调用被拒绝：%v", err)
	}
	interactiveWithOther := [][]toolCall{
		{
			{ID: "question", Type: "function", Function: toolFunction{Name: "ask_user", Arguments: `{"question":"选择操作","reason":"结果不同"}`}},
			{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}},
		},
		{
			{ID: "plan", Type: "function", Function: toolFunction{Name: "confirm_plan", Arguments: `{"plan":"执行变更","impact":"短暂中断","rollback":"恢复备份"}`}},
			{ID: "command", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-1","command":"true","purpose":"测试","finish_on_success":true}`}},
		},
	}
	for _, calls := range interactiveWithOther {
		if err := validateToolCalls(calls); err == nil || !strings.Contains(err.Error(), "必须是该轮唯一") {
			t.Fatalf("交互工具与其他工具同轮调用未被拒绝: calls=%#v err=%v", calls, err)
		}
	}
}

func TestVerificationIsTrackedPerHost(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeModelResponse(t, w, "", toolCall{ID: "hosts", Type: "function", Function: toolFunction{Name: "list_hosts", Arguments: `{}`}})
		case 2:
			writeModelResponse(t, w, "", toolCall{ID: "change-a", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-a","command":"systemctl restart nginx","purpose":"重启 A 的 nginx","finish_on_success":false}`}})
		case 3:
			writeModelResponse(t, w, "", toolCall{ID: "inspect-b", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-b","command":"uname -a","purpose":"检查 B","finish_on_success":false}`}})
		case 4:
			writeModelResponse(t, w, "不应在这里完成。")
		case 5:
			writeModelResponse(t, w, "", toolCall{ID: "verify-a", Type: "function", Function: toolFunction{Name: "run_command", Arguments: `{"host_id":"host-a","command":"systemctl is-active nginx","purpose":"验证 A 的 nginx","finish_on_success":false}`}})
		default:
			writeModelResponse(t, w, "A 已修改并验证，B 已检查。")
		}
	}))
	defer server.Close()

	controller := NewController()
	finished := make(chan Event, 1)
	var commands atomic.Int32
	_, err := controller.Start(context.Background(), Config{Endpoint: server.URL, APIKey: "test", Model: "test", Mode: "managed"}, "host-a", "修复 A 的 nginx 并确保可用，同时检查 B", Environment{
		ListHosts: func() []Host {
			return []Host{
				{ID: "host-a", Name: "A", Address: "192.0.2.1", Protocol: "ssh", Trusted: true},
				{ID: "host-b", Name: "B", Address: "192.0.2.2", Protocol: "ssh", Trusted: true},
			}
		},
		RunCommand: func(_ context.Context, _ string, _ string, _ time.Duration, _ func(string)) (CommandResult, error) {
			commands.Add(1)
			return CommandResult{Output: "ok", ExitCode: 0}, nil
		},
		Emit: func(event Event) {
			if event.Type == "approval" {
				_ = controller.Approve(event.RunID, event.ActionID, true)
			}
			if event.Type == "completed" || event.Type == "failed" {
				finished <- event
			}
		},
	})
	if err != nil {
		t.Fatalf("启动任务失败: %v", err)
	}
	select {
	case event := <-finished:
		if event.Type != "completed" || event.Message != "A 已修改并验证，B 已检查。" {
			t.Fatalf("任务过早完成或结果异常: %#v", event)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("等待任务完成超时")
	}
	if calls.Load() != 6 || commands.Load() != 3 {
		t.Fatalf("多主机验证状态异常: modelCalls=%d commands=%d", calls.Load(), commands.Load())
	}
}

func TestTruncatePreservesValidUTF8(t *testing.T) {
	value := strings.Repeat("中文", 10)
	for limit := 1; limit <= len(value); limit++ {
		got := truncate(value, limit)
		if !utf8.ValidString(got) {
			t.Fatalf("truncate(limit=%d) 产生非法 UTF-8: %q", limit, got)
		}
	}
	if got := truncate("abc", 0); !utf8.ValidString(got) || !strings.Contains(got, "截断") {
		t.Fatalf("truncate(limit=0) 结果异常: %q", got)
	}
}

func TestReadOnlyCommandAllowsSudoNonInteractiveFlags(t *testing.T) {
	if !readOnlyCommand("sudo -n systemctl status nginx") {
		t.Fatal("readOnlyCommand 应放行 sudo -n 只读命令")
	}
	if !readOnlyCommand("sudo --non-interactive df -h") {
		t.Fatal("readOnlyCommand 应放行 sudo --non-interactive 只读命令")
	}
	if readOnlyCommand("sudo -n rm -rf /tmp/test") {
		t.Fatal("readOnlyCommand 不应放行高危命令")
	}
}

func writeModelResponse(t *testing.T, w http.ResponseWriter, content string, calls ...toolCall) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(map[string]any{"choices": []map[string]any{{"message": map[string]any{"role": "assistant", "content": content, "tool_calls": calls}}}}); err != nil {
		t.Fatalf("写入模拟响应失败: %v", err)
	}
}

func TestThirdPartyAPIFallbacksAndSSEParsing(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("解析请求体失败: %v", err)
		}
		step := attempts.Add(1)
		if step == 1 {
			if _, ok := req["parallel_tool_calls"]; !ok {
				t.Fatalf("首次请求应包含 parallel_tool_calls")
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Unknown parameter: parallel_tool_calls and temperature"}}`))
			return
		}
		if _, ok := req["parallel_tool_calls"]; ok {
			t.Fatalf("回退重试不应再发送 parallel_tool_calls")
		}
		if _, ok := req["temperature"]; ok {
			t.Fatalf("回退重试不应再发送 temperature")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"服务器\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"正常\"}}]}\n\n"))
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()

	ctrl := NewController()
	resp, err := ctrl.complete(context.Background(), Config{
		Endpoint: server.URL + "/v1",
		APIKey:   "sk-third-party",
		Model:    "woifai-model",
	}, []chatMessage{{Role: "user", Content: "ping"}})
	if err != nil {
		t.Fatalf("第三方 API 兼容回退失败: %v", err)
	}
	if resp.Content != "服务器正常" {
		t.Fatalf("SSE 组装内容 = %q，期望 \"服务器正常\"", resp.Content)
	}
}
