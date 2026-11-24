package core

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const remoteProxyMetricPrefix = "SANTIAN_PROXY_METRIC"

type remoteCurlMetrics struct {
	HTTPCode  int
	ConnectMS int64
	TotalMS   int64
	ExitCode  int
	Stderr    string
}

func ensureRemoteCurl(client *ssh.Client, target string) error {
	result := executeSSH(client, "command -v curl >/dev/null 2>&1", 10)
	if result.Success {
		return nil
	}
	return fmt.Errorf("检测 VPS %q 未安装 curl，无法在 VPS 内完成真实代理检测；请安装 curl 后重试", target)
}

func remoteProxyDetectLocation(client *ssh.Client, target string) string {
	location := "远程 VPS · " + target
	result := executeSSH(client, "hostname 2>/dev/null || uname -n 2>/dev/null", 8)
	if !result.Success {
		return location
	}
	hostname := strings.TrimSpace(strings.SplitN(result.Stdout, "\n", 2)[0])
	if hostname == "" || len(hostname) > 128 || strings.ContainsAny(hostname, "\r\n\x00") {
		return location
	}
	return location + " (" + hostname + ")"
}

func remoteProxyDetectExitIP(client *ssh.Client) <-chan string {
	result := make(chan string, 1)
	go func() {
		command := "LC_ALL=C curl -q --silent --show-error --noproxy '*' --max-time 5 --url https://api.ipify.org"
		response := executeSSH(client, command, 8)
		if response.Success {
			ip := strings.TrimSpace(response.Stdout)
			if net.ParseIP(ip) != nil {
				result <- ip
				return
			}
		}
		result <- ""
	}()
	return result
}

func testRemoteProxyLine(ctx context.Context, client *ssh.Client, line string, testURL string) ProxyDetectResult {
	parsed := parseProxyInput(line)
	result := parsed.Result
	if result.Host == "" || result.Port < 1 || result.Port > 65535 {
		result.Message = "无法解析节点地址"
		return result
	}

	switch result.Protocol {
	case "socks5", "http", "https":
		return testRemoteProxyWithCurl(ctx, client, parsed, testURL)
	case "vmess", "trojan":
		return testRemoteEndpointWithCurl(ctx, client, parsed)
	default:
		return testRemoteEndpointWithCurl(ctx, client, parsed)
	}
}

func testRemoteProxyWithCurl(ctx context.Context, client *ssh.Client, parsed parsedProxyLine, testURL string) ProxyDetectResult {
	result := parsed.Result
	values := []string{result.Host, parsed.Username, parsed.Password, testURL}
	for _, value := range values {
		if err := validateRemoteCurlValue(value); err != nil {
			result.Message = "代理配置无效: " + err.Error()
			return result
		}
	}

	proxyScheme := result.Protocol
	protocolName := "HTTP"
	if result.Protocol == "socks5" {
		proxyScheme = "socks5h"
		protocolName = "SOCKS5"
	}
	proxyAddress := proxyScheme + "://" + net.JoinHostPort(result.Host, strconv.Itoa(result.Port))
	configLines := []string{
		"silent",
		"show-error",
		"fail",
		"location",
		"output = \"/dev/null\"",
		"connect-timeout = 6",
		"max-time = 12",
		"noproxy = \"\"",
		"url = " + remoteCurlQuote(testURL),
		"proxy = " + remoteCurlQuote(proxyAddress),
	}
	if parsed.Username != "" || parsed.Password != "" {
		configLines = append(configLines, "proxy-user = "+remoteCurlQuote(parsed.Username+":"+parsed.Password))
	}
	configLines = append(configLines, "write-out = "+remoteCurlQuote(remoteProxyMetricPrefix+"\t%{http_code}\t%{time_connect}\t%{time_total}\n"))

	metrics, err := runRemoteCurlConfig(ctx, client, strings.Join(configLines, "\n")+"\n", 15*time.Second)
	if metrics.ConnectMS >= 0 {
		result.TCPLatency = metrics.ConnectMS
		result.Latency = metrics.ConnectMS
	}
	if metrics.TotalMS >= 0 {
		result.URLLatency = metrics.TotalMS
	}
	if err != nil {
		message := remoteCurlErrorMessage(protocolName, metrics, err)
		result.Message = redactProxyDetectText(message, parsed)
		return result
	}
	if metrics.HTTPCode < 200 || metrics.HTTPCode >= 400 {
		result.Message = fmt.Sprintf("%s 代理验证失败: 测试 URL 返回 HTTP %d", protocolName, metrics.HTTPCode)
		return result
	}
	result.Success = true
	result.Message = protocolName + " 代理验证通过（VPS 内实测）"
	return result
}

func testRemoteEndpointWithCurl(ctx context.Context, client *ssh.Client, parsed parsedProxyLine) ProxyDetectResult {
	result := parsed.Result
	if err := validateRemoteCurlValue(result.Host); err != nil {
		result.Message = "节点地址无效: " + err.Error()
		return result
	}
	endpointURL := "http://" + net.JoinHostPort(result.Host, strconv.Itoa(result.Port)) + "/"
	config := strings.Join([]string{
		"silent",
		"show-error",
		"output = \"/dev/null\"",
		"connect-timeout = 6",
		"max-time = 6",
		"noproxy = \"*\"",
		"url = " + remoteCurlQuote(endpointURL),
		"write-out = " + remoteCurlQuote(remoteProxyMetricPrefix+"\t%{http_code}\t%{time_connect}\t%{time_total}\n"),
	}, "\n") + "\n"
	metrics, err := runRemoteCurlConfig(ctx, client, config, 9*time.Second)
	if metrics.ConnectMS >= 0 {
		result.TCPLatency = metrics.ConnectMS
		result.Latency = metrics.ConnectMS
	}
	if result.TCPLatency < 0 {
		result.Message = "TCP 连接失败: " + redactProxyDetectText(remoteCurlErrorMessage("TCP", metrics, err), parsed)
		return result
	}
	name := strings.ToUpper(firstNonEmpty(result.Protocol, "未知"))
	result.Message = fmt.Sprintf("%s 服务器端口可达（VPS 内实测），但暂未验证 %s 协议", name, name)
	return result
}

func runRemoteCurlConfig(ctx context.Context, client *ssh.Client, config string, timeout time.Duration) (remoteCurlMetrics, error) {
	metrics := remoteCurlMetrics{ConnectMS: -1, TotalMS: -1, ExitCode: -1}
	session, err := client.NewSession()
	if err != nil {
		return metrics, fmt.Errorf("创建远程检测会话失败: %w", err)
	}
	defer session.Close()

	var stdout, stderr commandBuffer
	session.Stdin = strings.NewReader(config)
	session.Stdout = &stdout
	session.Stderr = &stderr
	done := make(chan error, 1)
	go func() {
		done <- session.Run("LC_ALL=C curl -q --config -")
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err = <-done:
		metrics = parseRemoteCurlMetrics(stdout.Text())
		metrics.Stderr = compactProxyDetectMessage(stderr.Text())
		metrics.ExitCode = 0
		if err == nil {
			if metrics.HTTPCode < 0 {
				return metrics, errors.New("远程 curl 未返回检测指标")
			}
			return metrics, nil
		}
		var exitErr *ssh.ExitError
		if errors.As(err, &exitErr) {
			metrics.ExitCode = exitErr.ExitStatus()
		}
		return metrics, err
	case <-ctx.Done():
		_ = session.Close()
		waitSSHCommandDone(done, time.Second)
		return metrics, errors.New("检测已取消")
	case <-timer.C:
		_ = session.Close()
		waitSSHCommandDone(done, time.Second)
		return metrics, errors.New("远程代理检测超时")
	}
}

func parseRemoteCurlMetrics(output string) remoteCurlMetrics {
	metrics := remoteCurlMetrics{HTTPCode: -1, ConnectMS: -1, TotalMS: -1, ExitCode: -1}
	for _, line := range strings.Split(output, "\n") {
		parts := strings.Split(strings.TrimSpace(line), "\t")
		if len(parts) != 4 || parts[0] != remoteProxyMetricPrefix {
			continue
		}
		metrics.HTTPCode, _ = strconv.Atoi(parts[1])
		metrics.ConnectMS = curlSecondsToMilliseconds(parts[2])
		metrics.TotalMS = curlSecondsToMilliseconds(parts[3])
	}
	return metrics
}

func curlSecondsToMilliseconds(value string) int64 {
	seconds, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || seconds < 0 {
		return -1
	}
	return int64(math.Round(seconds * 1000))
}

func validateRemoteCurlValue(value string) error {
	if strings.ContainsAny(value, "\r\n\x00") {
		return errors.New("不允许包含换行或 NUL 字符")
	}
	return nil
}

func remoteCurlQuote(value string) string {
	replacer := strings.NewReplacer("\\", "\\\\", "\"", "\\\"", "\t", "\\t", "\r", "\\r", "\n", "\\n")
	return "\"" + replacer.Replace(value) + "\""
}

func remoteCurlErrorMessage(protocol string, metrics remoteCurlMetrics, runErr error) string {
	detail := metrics.Stderr
	errorText := "远程 curl 执行失败"
	if runErr != nil {
		errorText = runErr.Error()
	}
	lower := strings.ToLower(detail)
	switch {
	case metrics.HTTPCode == http.StatusProxyAuthRequired || strings.Contains(lower, "407"):
		return protocol + " 代理认证失败: HTTP 407，请检查用户名和密码"
	case metrics.ExitCode == 60:
		return "VPS 的 CA 证书不可用，无法验证 HTTPS: " + firstNonEmpty(detail, errorText)
	case metrics.ExitCode == 67 || metrics.ExitCode == 97 || strings.Contains(lower, "socks5") && strings.Contains(lower, "reject"):
		return protocol + " 用户名或密码错误，或代理拒绝认证"
	case metrics.ExitCode == 5:
		return "VPS 无法解析代理地址: " + firstNonEmpty(detail, errorText)
	case metrics.ExitCode == 7:
		return "VPS 无法连接代理端口: " + firstNonEmpty(detail, errorText)
	case metrics.ExitCode == 28:
		return "VPS 内代理请求超时: " + firstNonEmpty(detail, errorText)
	case metrics.ExitCode == 1 && strings.Contains(lower, "protocol"):
		return "VPS 上的 curl 不支持所需代理协议: " + detail
	default:
		return protocol + " 代理验证失败: " + firstNonEmpty(detail, errorText)
	}
}

func redactProxyDetectText(message string, parsed parsedProxyLine) string {
	for _, secret := range []string{parsed.Username, parsed.Password} {
		if secret == "" {
			continue
		}
		for _, value := range []string{secret, url.QueryEscape(secret), url.PathEscape(secret)} {
			if value != "" {
				message = strings.ReplaceAll(message, value, "***")
			}
		}
	}
	return compactProxyDetectMessage(message)
}

func compactProxyDetectMessage(message string) string {
	message = strings.TrimSpace(message)
	message = strings.NewReplacer("\r\n", "；", "\n", "；", "\r", "；").Replace(message)
	runes := []rune(message)
	if len(runes) > 500 {
		message = string(runes[:500]) + "…"
	}
	return message
}
