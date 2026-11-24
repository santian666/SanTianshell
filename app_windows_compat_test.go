package main

import (
	"encoding/json"
	"encoding/xml"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func TestWindowsResourceVersionMatchesAppVersion(t *testing.T) {
	content, err := os.ReadFile("wails.json")
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Info struct {
			ProductVersion string `json:"productVersion"`
		} `json:"info"`
	}
	if err := json.Unmarshal(content, &config); err != nil {
		t.Fatalf("wails.json 无效: %v", err)
	}
	if config.Info.ProductVersion != appVersion {
		t.Fatalf("Windows 资源版本 = %q，应用版本 = %q", config.Info.ProductVersion, appVersion)
	}
}

func TestAutoStartCommandQuotesPathAndEnforcesWindowsLimit(t *testing.T) {
	executable := `C:\Program Files\SanTianShell\SanTianShell.exe`
	command, err := autoStartCommand(executable)
	if err != nil {
		t.Fatal(err)
	}
	if command != `"`+executable+`"` {
		t.Fatalf("开机启动命令 = %q", command)
	}
	if len(utf16.Encode([]rune(command))) > maxAutoStartCommandUnits {
		t.Fatal("开机启动命令超过 Windows Run 键长度限制")
	}

	tooLong := strings.Repeat("a", maxAutoStartCommandUnits)
	if _, err := autoStartCommand(tooLong); err == nil {
		t.Fatal("超长开机启动路径未被拒绝")
	}
	for _, invalid := range []string{"", "bad\npath.exe", `bad"path.exe`, "bad\x00path.exe"} {
		if _, err := autoStartCommand(invalid); err == nil {
			t.Fatalf("无效开机启动路径未被拒绝: %q", invalid)
		}
	}
}

func TestWindowsKnownFoldersAreUsedForWritableRuntimeData(t *testing.T) {
	appData := appDataPath()
	if !filepath.IsAbs(appData) || filepath.Base(appData) != "SanTianShell" {
		t.Fatalf("应用数据目录无效: %q", appData)
	}

	webViewData := webViewDataPath()
	if webViewData == "" {
		t.Skip("LOCALAPPDATA 不可用，Wails 将使用默认 WebView2 数据目录")
	}
	localAppData, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(localAppData, webViewData)
	if err != nil || relative == ".." || strings.HasPrefix(relative, `..\`) {
		t.Fatalf("WebView2 数据目录不在 LOCALAPPDATA 下: %q", webViewData)
	}
	if info, err := os.Stat(webViewData); err != nil || !info.IsDir() {
		t.Fatalf("WebView2 数据目录不可用: %v", err)
	}

	downloads := defaultDownloadDirectory()
	if info, err := os.Stat(downloads); err != nil || !info.IsDir() {
		t.Fatalf("下载目录不可用: %q, %v", downloads, err)
	}
}

func TestWindowsManifestDeclaresWin10AndWin11Compatibility(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("build", "windows", "wails.exe.manifest"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		XMLName xml.Name
	}
	if err := xml.Unmarshal(content, &document); err != nil {
		t.Fatalf("Windows manifest XML 无效: %v", err)
	}
	text := string(content)
	for _, required := range []string{
		`requestedExecutionLevel level="asInvoker" uiAccess="false"`,
		`supportedOS Id="{8e0f7a12-bfb3-4fe8-b9a5-48fd50a15a9a}"`,
		`<longPathAware xmlns="http://schemas.microsoft.com/SMI/2016/WindowsSettings">true</longPathAware>`,
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Windows manifest 缺少 %q", required)
		}
	}
}

func TestWebViewMessagesAreCompleteAndChinese(t *testing.T) {
	messages := webViewMessages()
	values := []string{
		messages.InstallationRequired,
		messages.UpdateRequired,
		messages.MissingRequirements,
		messages.Webview2NotInstalled,
		messages.Error,
		messages.FailedToInstall,
		messages.DownloadPage,
		messages.PressOKToInstall,
		messages.ContactAdmin,
		messages.InvalidFixedWebview2,
		messages.WebView2ProcessCrash,
	}
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			t.Fatalf("WebView2 提示第 %d 项为空", index+1)
		}
	}
	if !strings.Contains(messages.InstallationRequired, "尚未安装") || !strings.Contains(messages.PressOKToInstall, "确定") {
		t.Fatal("WebView2 安装提示不是清晰的中文文案")
	}
}
