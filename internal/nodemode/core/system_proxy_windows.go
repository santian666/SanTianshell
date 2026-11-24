package core

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"

	"golang.org/x/sys/windows/registry"
)

func systemLocalProxyDialConfig() (proxyDialConfig, error) {
	key, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return proxyDialConfig{}, nil
	}
	defer key.Close()
	enabled, _, err := key.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return proxyDialConfig{}, nil
	}
	raw, _, err := key.GetStringValue("ProxyServer")
	if err != nil {
		return proxyDialConfig{}, nil
	}
	return parseSystemProxyServer(raw)
}

func parseSystemProxyServer(value string) (proxyDialConfig, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return proxyDialConfig{}, nil
	}
	bestPriority := 0
	var bestConfig proxyDialConfig
	for _, part := range strings.Split(value, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		priority, rawEndpoint, ok := classifySystemProxyPart(part)
		if !ok || priority < bestPriority {
			continue
		}
		cfg, err := parseSystemProxyEndpoint(rawEndpoint)
		if err != nil || cfg.Host == "" || cfg.Port <= 0 {
			continue
		}
		if priority > bestPriority || bestConfig.Host == "" {
			bestPriority = priority
			bestConfig = cfg
		}
	}
	if bestConfig.Host == "" {
		return proxyDialConfig{}, errors.New("未识别到有效的系统代理地址")
	}
	return bestConfig, nil
}

func classifySystemProxyPart(part string) (int, string, bool) {
	if strings.Contains(part, "=") && !strings.Contains(part, "://") {
		pair := strings.SplitN(part, "=", 2)
		scheme := strings.ToLower(strings.TrimSpace(pair[0]))
		endpoint := strings.TrimSpace(pair[1])
		switch scheme {
		case "socks5", "socks5h":
			return 4, endpoint, true
		case "socks":
			return 3, endpoint, true
		case "https", "http":
			return 1, endpoint, true
		default:
			return 0, "", false
		}
	}
	lower := strings.ToLower(part)
	if idx := strings.Index(lower, "://"); idx >= 0 {
		switch lower[:idx] {
		case "socks5", "socks5h":
			return 4, part, true
		case "socks":
			return 3, part, true
		case "https", "http":
			return 1, part, true
		default:
			return 0, "", false
		}
	}
	return 2, part, true
}

func parseSystemProxyEndpoint(endpoint string) (proxyDialConfig, error) {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return proxyDialConfig{}, errors.New("代理地址不能为空")
	}
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return proxyDialConfig{}, err
		}
		host := strings.TrimSpace(parsed.Hostname())
		port, err := strconv.Atoi(strings.TrimSpace(parsed.Port()))
		if err != nil || host == "" || port <= 0 || port > 65535 {
			return proxyDialConfig{}, errors.New("代理 URL 地址或端口无效")
		}
		cfg := proxyDialConfig{Host: host, Port: port}
		if parsed.User != nil {
			cfg.Username = parsed.User.Username()
			cfg.Password, _ = parsed.User.Password()
		}
		return cfg, nil
	}
	if host, portText, err := net.SplitHostPort(endpoint); err == nil {
		port, err := strconv.Atoi(strings.TrimSpace(portText))
		host = strings.TrimSpace(host)
		if err != nil || host == "" || port <= 0 || port > 65535 {
			return proxyDialConfig{}, errors.New("代理地址或端口无效")
		}
		return proxyDialConfig{Host: host, Port: port}, nil
	}
	return parseHostPortUserPass(endpoint)
}
