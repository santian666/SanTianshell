package main

import (
	"errors"
	"strings"
	"sync"
	"testing"

	"santianshell/internal/rdp"
	"santianshell/internal/remote"
)

func TestRDPOptionsForHostMapsAdvancedSettings(t *testing.T) {
	clipboard := false
	host := remote.Host{
		Protocol:             "rdp",
		Username:             "administrator",
		Password:             "RDP-Secret",
		RDPResolution:        "1920x1080",
		RDPDisplayMode:       string(rdp.DisplayModeFullscreen),
		RDPSmartSizing:       true,
		RDPUseMultimon:       true,
		RDPMapDrives:         true,
		RDPDriveIDs:          []string{"C", "D:"},
		RDPRedirectClipboard: &clipboard,
		RDPAudioPlayback:     int(rdp.AudioPlaybackDisabled),
		RDPAudioCapture:      true,
		RDPRedirectPrinters:  true,
		RDPGatewayAddress:    "gateway.example:443",
		RDPGatewayUsername:   `DOMAIN\gateway-user`,
		RDPGatewayUsage:      int(rdp.GatewayUsageFallback),
	}
	options, err := rdpOptionsForHost(host, "server.example:3389")
	if err != nil {
		t.Fatal(err)
	}
	if options.Address != "server.example:3389" || options.Username != host.Username || options.Password != host.Password {
		t.Fatalf("基础 RDP 映射错误: %#v", options)
	}
	if options.DesktopWidth != 1920 || options.DesktopHeight != 1080 || options.DisplayMode != rdp.DisplayModeFullscreen {
		t.Fatalf("显示设置映射错误: %#v", options)
	}
	if !options.SmartSizing || !options.UseMultimon || !options.RedirectDrives || options.RedirectClipboard {
		t.Fatalf("显示或重定向开关映射错误: %#v", options)
	}
	if len(options.RedirectDriveIDs) != 2 || options.RedirectDriveIDs[0] != "C" || options.RedirectDriveIDs[1] != "D:" {
		t.Fatalf("指定磁盘映射错误: %#v", options.RedirectDriveIDs)
	}
	if options.AudioPlayback != rdp.AudioPlaybackDisabled || !options.AudioCapture || !options.RedirectPrinters {
		t.Fatalf("音频或打印机映射错误: %#v", options)
	}
	if options.GatewayAddress != host.RDPGatewayAddress || options.GatewayUsername != host.RDPGatewayUsername || options.GatewayUsage != rdp.GatewayUsageFallback {
		t.Fatalf("RD Gateway 映射错误: %#v", options)
	}
	if options.GatewayPassword != "" {
		t.Fatal("RD Gateway 密码进入了 rdp.Options")
	}
}

func TestRDPOptionsForLegacyHostEnablesClipboard(t *testing.T) {
	options, err := rdpOptionsForHost(remote.Host{Protocol: "rdp"}, "server.example:3389")
	if err != nil {
		t.Fatal(err)
	}
	if !options.RedirectClipboard {
		t.Fatal("旧 RDP 主机未保持剪贴板默认开启")
	}
	if options.AudioPlayback != rdp.AudioPlaybackLocal {
		t.Fatal("旧 RDP 主机未保持音频本地播放")
	}
	if options.DisplayMode != rdp.DisplayModeWindow || options.DesktopWidth != 1280 || options.DesktopHeight != 1024 {
		t.Fatalf("旧 RDP 主机显示默认值错误: %#v", options)
	}
}

func TestSaveHostRejectsRDPGatewayPassword(t *testing.T) {
	store := &memorySettingsStore{}
	app := &App{store: store, unlocked: true, settings: Settings{Hosts: []remote.Host{}}}
	_, err := app.SaveHost(remote.Host{
		Name: "RDP", Address: "server.example", Username: "administrator", Protocol: "rdp",
		RDPGatewayAddress: "gateway.example", RDPGatewayPassword: "Gateway-Secret",
	})
	if err == nil || !strings.Contains(err.Error(), "不能保存在主机配置中") {
		t.Fatalf("RD Gateway 密码未被明确拒绝: %v", err)
	}
	if store.saves != 0 || len(app.settings.Hosts) != 0 {
		t.Fatal("RD Gateway 密码被写入了主机配置")
	}
}

func TestRDPRejectsSOCKS5AndGatewayTogether(t *testing.T) {
	host := remote.Host{
		Protocol:          "rdp",
		ProxyEnabled:      true,
		ProxyHost:         "127.0.0.1",
		ProxyPort:         20808,
		RDPGatewayAddress: "gateway.example",
		RDPGatewayUsage:   int(rdp.GatewayUsageAlways),
	}
	if err := normalizeRDPHostOptions(&host); err == nil || !strings.Contains(err.Error(), "不能与 RD Gateway 同时启用") {
		t.Fatalf("SOCKS5 与 RD Gateway 混合配置未被拒绝: %v", err)
	}
}

func TestSettingsForClientClearsRDPGatewayPassword(t *testing.T) {
	settings := settingsForClient(Settings{Hosts: []remote.Host{{RDPGatewayPassword: "Gateway-Secret"}}})
	if settings.Hosts[0].RDPGatewayPassword != "" {
		t.Fatal("返回给客户端的配置包含 RD Gateway 密码")
	}
}

func TestValidateRDPHostForOpenRejectsDeletedAndNonRDP(t *testing.T) {
	if err := validateRDPHostForOpen(remote.Host{Protocol: "rdp", Deleted: true}); err == nil || !strings.Contains(err.Error(), "已删除") {
		t.Fatalf("已删除主机未被拒绝: %v", err)
	}
	if err := validateRDPHostForOpen(remote.Host{Protocol: "ssh"}); err == nil || !strings.Contains(err.Error(), "RDP") {
		t.Fatalf("非 RDP 主机未被拒绝: %v", err)
	}
	if err := validateRDPHostForOpen(remote.Host{Protocol: "RDP"}); err != nil {
		t.Fatalf("有效 RDP 主机被拒绝: %v", err)
	}
}

func TestOpenRDPRejectsDeletedAndNonRDPBeforeStarting(t *testing.T) {
	app := &App{
		unlocked: true,
		settings: Settings{Hosts: []remote.Host{
			{ID: "deleted", Protocol: "rdp", Deleted: true},
			{ID: "ssh", Protocol: "ssh"},
		}},
	}
	if _, err := app.OpenRDP("deleted"); err == nil || !strings.Contains(err.Error(), "已删除") {
		t.Fatalf("OpenRDP 未直接拒绝已删除主机: %v", err)
	}
	if _, err := app.OpenRDP("ssh"); err == nil || !strings.Contains(err.Error(), "RDP") {
		t.Fatalf("OpenRDP 未直接拒绝 SSH 主机: %v", err)
	}
}

func TestValidateAndRegisterRDPSessionLockedRechecksTargetAndJump(t *testing.T) {
	tests := []struct {
		name       string
		hosts      []remote.Host
		jumpHostID string
		wantError  string
	}{
		{name: "目标不存在", wantError: "不存在"},
		{name: "目标已删除", hosts: []remote.Host{{ID: "target", Protocol: "rdp", Deleted: true}}, wantError: "已删除"},
		{name: "目标协议变化", hosts: []remote.Host{{ID: "target", Protocol: "ssh"}}, wantError: "RDP"},
		{name: "跳板配置变化", hosts: []remote.Host{{ID: "target", Protocol: "rdp", RDPJumpHostID: "jump-new"}}, jumpHostID: "jump-old", wantError: "变化"},
		{name: "跳板不存在", hosts: []remote.Host{{ID: "target", Protocol: "rdp", RDPJumpHostID: "jump"}}, jumpHostID: "jump", wantError: "跳板主机"},
		{name: "跳板已删除", hosts: []remote.Host{{ID: "target", Protocol: "rdp", RDPJumpHostID: "jump"}, {ID: "jump", Protocol: "ssh", Deleted: true}}, jumpHostID: "jump", wantError: "跳板主机"},
		{name: "跳板协议变化", hosts: []remote.Host{{ID: "target", Protocol: "rdp", RDPJumpHostID: "jump"}, {ID: "jump", Protocol: "rdp"}}, jumpHostID: "jump", wantError: "跳板主机"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := &App{settings: Settings{Hosts: test.hosts}}
			session := &rdp.Session{ID: "rdp-test"}
			app.mu.Lock()
			err := app.validateAndRegisterRDPSessionLocked("target", test.jumpHostID, session)
			app.mu.Unlock()
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("复核结果错误: %v", err)
			}
			if len(app.rdpSessions) != 0 || len(app.rdpSessionHosts) != 0 || len(app.rdpSessionJumps) != 0 {
				t.Fatal("复核失败后仍登记了 RDP 会话")
			}
		})
	}

	app := &App{settings: Settings{Hosts: []remote.Host{
		{ID: "target", Protocol: "rdp", RDPJumpHostID: "jump"},
		{ID: "jump", Protocol: "ssh"},
	}}}
	session := &rdp.Session{ID: "rdp-valid"}
	app.mu.Lock()
	err := app.validateAndRegisterRDPSessionLocked("target", "jump", session)
	app.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if app.rdpSessions[session.ID] != session || app.rdpSessionHosts[session.ID] != "target" || app.rdpSessionJumps[session.ID] != "jump" {
		t.Fatal("有效 RDP 会话未原子登记")
	}
}

func TestWaitRDPForwardErrorPrefersBufferedErrorWhenDone(t *testing.T) {
	rawErr := errors.New("SOCKS5 原始错误")
	done := make(chan struct{})
	forwardErrors := make(chan error, 1)
	forwardErrors <- rawErr
	close(done)
	if err := waitRDPForwardError(done, forwardErrors); !errors.Is(err, rawErr) {
		t.Fatalf("会话退出掩盖了已缓冲错误: %v", err)
	}
}

func TestRDPStatusArbiterSuppressesExitedAfterForwardFailure(t *testing.T) {
	rawErr := errors.New("代理连接被拒绝")
	var events []rdp.StatusEvent
	arbiter := newRDPStatusArbiter(true, func(event rdp.StatusEvent) {
		events = append(events, event)
	})
	arbiter.Report(rdp.StatusEvent{SessionID: "rdp-1", State: rdp.StateExited, PID: 42})
	if len(events) != 0 {
		t.Fatal("SOCKS5 结果未确定前提前发送了 exited")
	}
	arbiter.ResolveForward("rdp-1", 42, rawErr)
	arbiter.Report(rdp.StatusEvent{SessionID: "rdp-1", State: rdp.StateExited, PID: 42})
	if len(events) != 1 || events[0].State != rdp.StateFailed || !errors.Is(events[0].Err, rawErr) || !strings.Contains(events[0].Error, rawErr.Error()) {
		t.Fatalf("终态仲裁未保留原始失败或重复发送退出: %#v", events)
	}
	select {
	case <-arbiter.ForwardDone():
	default:
		t.Fatal("SOCKS5 仲裁完成信号未关闭")
	}
}

func TestRDPStatusArbiterConcurrentTerminalEventsAreSingleFailure(t *testing.T) {
	for iteration := 0; iteration < 50; iteration++ {
		rawErr := errors.New("SOCKS5 并发错误")
		var events []rdp.StatusEvent
		arbiter := newRDPStatusArbiter(true, func(event rdp.StatusEvent) {
			events = append(events, event)
		})
		start := make(chan struct{})
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			arbiter.Report(rdp.StatusEvent{SessionID: "rdp-race", State: rdp.StateExited})
		}()
		go func() {
			defer workers.Done()
			<-start
			arbiter.ResolveForward("rdp-race", 7, rawErr)
		}()
		close(start)
		workers.Wait()
		if len(events) != 1 || events[0].State != rdp.StateFailed || !errors.Is(events[0].Err, rawErr) {
			t.Fatalf("第 %d 次并发仲裁结果错误: %#v", iteration, events)
		}
	}
}

func TestRDPStatusArbiterEmitsNormalExitAfterForwardSettles(t *testing.T) {
	var events []rdp.StatusEvent
	arbiter := newRDPStatusArbiter(true, func(event rdp.StatusEvent) {
		events = append(events, event)
	})
	exit := rdp.StatusEvent{SessionID: "rdp-normal", State: rdp.StateExited, PID: 9}
	arbiter.Report(exit)
	arbiter.ResolveForward(exit.SessionID, exit.PID, nil)
	if len(events) != 1 || events[0].State != rdp.StateExited {
		t.Fatalf("正常退出未发送: %#v", events)
	}
}

func TestRDPStatusForUIClearsIntentionalCloseError(t *testing.T) {
	app := &App{rdpSessionClosing: map[string]struct{}{"rdp-close": {}}}
	rawErr := errors.New("process killed")
	event := app.rdpStatusForUI(rdp.StatusEvent{
		SessionID: "rdp-close",
		State:     rdp.StateExited,
		Error:     rawErr.Error(),
		Err:       rawErr,
	})
	if event.Error != "" || event.Err != nil {
		t.Fatalf("主动关闭仍显示进程错误: %#v", event)
	}
	if _, exists := app.rdpSessionClosing["rdp-close"]; exists {
		t.Fatal("主动关闭标记未清理")
	}

	natural := app.rdpStatusForUI(rdp.StatusEvent{
		SessionID: "rdp-natural",
		State:     rdp.StateExited,
		Error:     rawErr.Error(),
		Err:       rawErr,
	})
	if !errors.Is(natural.Err, rawErr) || natural.Error != rawErr.Error() {
		t.Fatal("自然异常退出的原始错误被清除")
	}
}
