package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRegisterQueueStepRejectsSameHostReentry(t *testing.T) {
	app := &App{unlocked: true, queueRuns: make(map[string]*queueRunState)}
	if err := app.BeginQueue("queue-1"); err != nil {
		t.Fatal(err)
	}
	firstCtx, firstCancel := context.WithCancel(context.Background())
	defer firstCancel()
	if _, err := app.registerQueueStep("queue-1", "host-1", firstCancel); err != nil {
		t.Fatal(err)
	}
	secondCtx, secondCancel := context.WithCancel(context.Background())
	defer secondCancel()
	if _, err := app.registerQueueStep("queue-1", "host-1", secondCancel); err == nil || !strings.Contains(err.Error(), "已有队列步骤") {
		t.Fatalf("同一主机重复登记结果 = %v，期望明确拒绝", err)
	}

	if !app.StopQueue("queue-1") {
		t.Fatal("停止队列失败")
	}
	select {
	case <-firstCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("停止队列后首个步骤未取消")
	}
	select {
	case <-secondCtx.Done():
		t.Fatal("被拒绝的步骤不应由队列持有或取消")
	default:
	}
}

func TestBeginQueueInitializesRunMap(t *testing.T) {
	app := &App{unlocked: true}
	if err := app.BeginQueue("queue-1"); err != nil {
		t.Fatal(err)
	}
	if app.queueRuns["queue-1"] == nil {
		t.Fatal("BeginQueue 未初始化队列运行状态")
	}
}

func TestOldQueueStepUnregisterDoesNotRemoveReusedRunStep(t *testing.T) {
	app := &App{unlocked: true}
	if err := app.BeginQueue("queue-1"); err != nil {
		t.Fatal(err)
	}
	oldCtx, oldCancel := context.WithCancel(context.Background())
	defer oldCancel()
	oldRun, err := app.registerQueueStep("queue-1", "host-1", oldCancel)
	if err != nil {
		t.Fatal(err)
	}

	app.FinishQueue("queue-1")
	select {
	case <-oldCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("结束旧队列后步骤未取消")
	}
	if err := app.BeginQueue("queue-1"); err != nil {
		t.Fatal(err)
	}
	newCtx, newCancel := context.WithCancel(context.Background())
	defer newCancel()
	newRun, err := app.registerQueueStep("queue-1", "host-1", newCancel)
	if err != nil {
		t.Fatal(err)
	}
	if oldRun == newRun {
		t.Fatal("复用运行 ID 后仍使用旧队列实例")
	}

	app.unregisterQueueStep(oldRun, "host-1")
	if !app.StopQueue("queue-1") {
		t.Fatal("停止新队列失败")
	}
	select {
	case <-newCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("旧步骤注销误删了新队列取消函数")
	}
}

func TestSaveQuickCommandNormalizesValues(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{store: store, unlocked: true}
	settings, err := app.SaveQuickCommand(QuickCommand{
		Name:           "  队列命令  ",
		Command:        "  echo 一  \r\n\r\n\t echo 二 \r",
		Type:           "queue",
		TimeoutSeconds: 0,
		Retries:        2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(settings.QuickCommands) != 1 {
		t.Fatalf("快捷命令数量 = %d，期望 1", len(settings.QuickCommands))
	}
	got := settings.QuickCommands[0]
	if got.ID == "" || got.Name != "队列命令" || got.Command != "echo 一\necho 二" || got.Type != "queue" || got.TimeoutSeconds != 300 || got.Retries != 2 {
		t.Fatalf("归一化结果异常：%+v", got)
	}
	if len(store.saved.QuickCommands) != 1 || store.saved.QuickCommands[0] != got {
		t.Fatalf("持久化内容未使用归一化值：%+v", store.saved.QuickCommands)
	}

	settings, err = app.SaveQuickCommand(QuickCommand{
		ID:             "  " + got.ID + "  ",
		Name:           "  单条命令  ",
		Command:        " \r\n  printf ok  \r\n echo done \r\n ",
		Type:           "legacy",
		TimeoutSeconds: -1,
		Retries:        99,
	})
	if err != nil {
		t.Fatal(err)
	}
	got = settings.QuickCommands[0]
	if got.Name != "单条命令" || got.Command != "printf ok  \n echo done" || got.Type != "single" || got.TimeoutSeconds != 0 || got.Retries != 0 {
		t.Fatalf("单条命令归一化结果异常：%+v", got)
	}
}

func TestRunQueueStepRejectsNegativeTimeout(t *testing.T) {
	app := &App{unlocked: true}
	_, err := app.RunQueueStep("host-1", 1, "true", "queue-1", -1)
	if err == nil || !strings.Contains(err.Error(), "不能小于 0") {
		t.Fatalf("错误 = %v，期望拒绝负数超时", err)
	}
}

func TestSaveQuickCommandAcceptsBoundaries(t *testing.T) {
	tests := []struct {
		name    string
		command QuickCommand
	}{
		{name: "名称 64 个字符", command: QuickCommand{Name: strings.Repeat("界", 64), Command: "true"}},
		{name: "命令 65536 字节", command: QuickCommand{Name: "边界", Command: strings.Repeat("a", 65536)}},
		{name: "队列 100 步", command: QuickCommand{Name: "边界", Command: strings.TrimSuffix(strings.Repeat("true\n", 100), "\n"), Type: "queue"}},
		{name: "队列单步 4096 字节", command: QuickCommand{Name: "边界", Command: strings.Repeat("a", 4096), Type: "queue"}},
		{name: "队列参数上限", command: QuickCommand{Name: "边界", Command: "true", Type: "queue", TimeoutSeconds: 3600, Retries: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memorySettingsStore{}
			app := &App{store: store, unlocked: true}
			if _, err := app.SaveQuickCommand(tt.command); err != nil {
				t.Fatalf("边界值保存失败：%v", err)
			}
			if store.saves != 1 {
				t.Fatalf("保存次数 = %d，期望 1", store.saves)
			}
		})
	}
}

func TestSaveQuickCommandRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		command QuickCommand
		want    string
	}{
		{name: "名称含 NUL", command: QuickCommand{Name: "错误\x00名称", Command: "true"}, want: "NUL"},
		{name: "命令含 NUL", command: QuickCommand{Name: "错误", Command: "echo\x00bad"}, want: "NUL"},
		{name: "名称超过 64 个字符", command: QuickCommand{Name: strings.Repeat("界", 65), Command: "true"}, want: "64"},
		{name: "命令超过 65536 字节", command: QuickCommand{Name: "错误", Command: strings.Repeat("a", 65537)}, want: "65536"},
		{name: "队列超过 100 步", command: QuickCommand{Name: "错误", Command: strings.TrimSuffix(strings.Repeat("true\n", 101), "\n"), Type: "queue"}, want: "100"},
		{name: "队列单步超过 4096 字节", command: QuickCommand{Name: "错误", Command: strings.Repeat("a", 4097), Type: "queue"}, want: "4096"},
		{name: "队列超时为负数", command: QuickCommand{Name: "错误", Command: "true", Type: "queue", TimeoutSeconds: -1}, want: "不能小于 0"},
		{name: "队列超时超过上限", command: QuickCommand{Name: "错误", Command: "true", Type: "queue", TimeoutSeconds: 3601}, want: "3600"},
		{name: "队列重试为负数", command: QuickCommand{Name: "错误", Command: "true", Type: "queue", Retries: -1}, want: "0 到 3"},
		{name: "队列重试超过上限", command: QuickCommand{Name: "错误", Command: "true", Type: "queue", Retries: 4}, want: "0 到 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &memorySettingsStore{}
			app := &App{store: store, unlocked: true}
			_, err := app.SaveQuickCommand(tt.command)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("错误 = %v，期望包含 %q", err, tt.want)
			}
			if store.saves != 0 {
				t.Fatalf("无效快捷命令仍保存了 %d 次", store.saves)
			}
		})
	}
}
