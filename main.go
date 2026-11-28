package main

import (
	"context"
	"embed"
	"log/slog"
	"os"
	"time"

	"santianshell/internal/diaglog"
	"santianshell/internal/nodemode"
	"santianshell/internal/singleton"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/windows"
)

//go:embed frontend/dist
var assets embed.FS

func hasUpdateRestartArg(args []string) bool {
	for _, arg := range args {
		if arg == updateRestartArg {
			return true
		}
	}
	return false
}

func isUpdateRestart(args []string) bool {
	if hasUpdateRestartArg(args) {
		return true
	}
	if exePath, err := os.Executable(); err == nil && exePath != "" {
		if _, statErr := os.Stat(exePath + ".old"); statErr == nil {
			return true
		}
	}
	return false
}

func acquireAppSingleInstance(args []string, waitTimeout time.Duration) (singleton.Handle, bool, error) {
	waitForRestart := isUpdateRestart(args)
	deadline := time.Now().Add(waitTimeout)
	for {
		instance, alreadyRunning, err := singleton.Acquire(`SanTianShell.SingleInstance`, "SanTianShell")
		if err != nil {
			return 0, false, err
		}
		if !alreadyRunning {
			return instance, false, nil
		}
		if !waitForRestart || time.Now().After(deadline) {
			return instance, true, nil
		}
		if instance != 0 {
			singleton.Release(instance)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

func main() {
	instance, alreadyRunning, instanceErr := acquireAppSingleInstance(os.Args[1:], 15*time.Second)
	if instanceErr != nil {
		slog.Error("创建单实例锁失败", "错误", instanceErr)
		os.Exit(1)
	}
	if instance != 0 {
		defer singleton.Release(instance)
	}
	if alreadyRunning {
		singleton.Activate("SanTianShell")
		return
	}
	if logFile, logErr := diaglog.Open(logDirectory()); logErr == nil {
		defer logFile.Close()
		slog.SetDefault(diaglog.Logger(logFile))
	} else {
		slog.Error("初始化诊断日志失败", "错误", logErr)
	}
	app, err := NewApp()
	if err != nil {
		slog.Error("初始化失败", "错误", err)
		os.Exit(1)
	}
	nodeMode := nodemode.New(app.acquireNodeSSH, app.acquireNodeSSHInteractive, app.resolveNodePassword)
	nodemode.SetAccessGuard(nodeMode, app.requireUnlocked)
	app.setNodeDataCleanup(func(hostID, alias string) error {
		return nodemode.ForgetHostLocalData(nodeMode, hostID, alias)
	})
	if err := wails.Run(&options.App{
		Title: "SanTianShell", Width: 1200, Height: 800, MinWidth: 780, MinHeight: 600,
		Frameless:        true,
		Windows:          &windows.Options{DisableFramelessWindowDecorations: true, WebviewUserDataPath: webViewDataPath(), Messages: webViewMessages()},
		BackgroundColour: &options.RGBA{R: 244, G: 247, B: 251, A: 1},
		AssetServer:      &assetserver.Options{Assets: assets}, Bind: []interface{}{app, nodeMode}, OnStartup: func(ctx context.Context) {
			app.startup(ctx)
			hostIDs, aliases := app.activeNodeHosts()
			if err := nodemode.ReconcileHostLocalData(nodeMode, hostIDs, aliases); err != nil {
				slog.Error("启动时清理失效的本地节点展示失败", "错误", err)
			}
			nodemode.Startup(nodeMode, ctx)
		}, OnShutdown: func(ctx context.Context) {
			app.shutdown(ctx)
			_ = nodemode.Close(nodeMode)
		},
	}); err != nil {
		slog.Error("启动失败", "错误", err)
	}
}

func webViewMessages() *windows.Messages {
	return &windows.Messages{
		InstallationRequired: "运行 SanTianShell 需要 Microsoft WebView2 Runtime，当前电脑尚未安装。",
		UpdateRequired:       "当前 Microsoft WebView2 Runtime 版本过旧，需要更新后才能运行 SanTianShell。",
		MissingRequirements:  "缺少运行组件",
		Webview2NotInstalled: "Microsoft WebView2 Runtime 未安装",
		Error:                "SanTianShell 启动失败",
		FailedToInstall:      "Microsoft WebView2 Runtime 安装失败，请检查网络连接后重试。",
		DownloadPage:         "需要安装 Microsoft WebView2 Runtime，最低版本：",
		PressOKToInstall:     "\n\n点击“确定”开始安装。",
		ContactAdmin:         "未检测到 Microsoft WebView2 Runtime，请安装后重新打开 SanTianShell。",
		InvalidFixedWebview2: "指定的 Microsoft WebView2 Runtime 无效。",
		WebView2ProcessCrash: "WebView2 渲染进程异常退出，请重新启动 SanTianShell。",
	}
}
