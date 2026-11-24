package quickconnect

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

const (
	SchemeSSH = "ssh"
	SchemeRDP = "rdp"
)

// Target 是快速连接输入解析后的目标。
type Target struct {
	Scheme string `json:"scheme"`
	User   string `json:"user"`
	Host   string `json:"host"`
	Port   int    `json:"port"`
}

// Parse 解析 user@host:port、ssh:// 和 rdp:// 形式的快速连接地址。
// 未指定协议时默认使用 SSH，未指定端口时使用协议默认端口。
func Parse(raw string) (Target, error) {
	if hasControl(raw) {
		return Target{}, fmt.Errorf("连接地址包含控制字符")
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Target{}, fmt.Errorf("连接地址不能为空")
	}

	if strings.Contains(raw, "://") {
		return parseURLTarget(raw)
	}
	return parseBareTarget(raw)
}

func parseURLTarget(raw string) (Target, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Target{}, fmt.Errorf("连接地址格式错误: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != SchemeSSH && scheme != SchemeRDP {
		return Target{}, fmt.Errorf("不支持的连接协议 %q", u.Scheme)
	}
	if u.Opaque != "" || u.Host == "" {
		return Target{}, fmt.Errorf("连接地址缺少主机")
	}
	if u.Path != "" && u.Path != "/" {
		return Target{}, fmt.Errorf("连接地址不能包含路径")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return Target{}, fmt.Errorf("连接地址不能包含查询参数或片段")
	}

	user := ""
	if u.User != nil {
		if _, hasPassword := u.User.Password(); hasPassword {
			return Target{}, fmt.Errorf("连接地址不能包含密码")
		}
		user = u.User.Username()
		if user == "" {
			return Target{}, fmt.Errorf("用户名不能为空")
		}
	}
	host := u.Hostname()
	if err := validateUser(user); err != nil {
		return Target{}, err
	}
	if err := validateHost(host); err != nil {
		return Target{}, err
	}

	port := defaultPort(scheme)
	if portText := u.Port(); portText != "" {
		port, err = parsePort(portText)
		if err != nil {
			return Target{}, err
		}
	}
	return Target{Scheme: scheme, User: user, Host: host, Port: port}, nil
}

func parseBareTarget(raw string) (Target, error) {
	user := ""
	authority := raw
	if at := strings.LastIndexByte(raw, '@'); at >= 0 {
		if at == 0 {
			return Target{}, fmt.Errorf("用户名不能为空")
		}
		if strings.Contains(raw[:at], "@") {
			return Target{}, fmt.Errorf("用户名中的 @ 需要使用 ssh:// 或 rdp:// URL 编码")
		}
		user = raw[:at]
		authority = raw[at+1:]
	}
	if err := validateUser(user); err != nil {
		return Target{}, err
	}

	host, portText, hasPort, err := splitBareAuthority(authority)
	if err != nil {
		return Target{}, err
	}
	if err := validateHost(host); err != nil {
		return Target{}, err
	}
	port := defaultPort(SchemeSSH)
	if hasPort {
		port, err = parsePort(portText)
		if err != nil {
			return Target{}, err
		}
	}
	return Target{Scheme: SchemeSSH, User: user, Host: host, Port: port}, nil
}

func splitBareAuthority(authority string) (host, port string, hasPort bool, err error) {
	if authority == "" {
		return "", "", false, fmt.Errorf("连接地址缺少主机")
	}
	if strings.HasPrefix(authority, "[") {
		closeBracket := strings.IndexByte(authority, ']')
		if closeBracket < 0 {
			return "", "", false, fmt.Errorf("IPv6 地址缺少右方括号")
		}
		host = authority[1:closeBracket]
		remainder := authority[closeBracket+1:]
		switch {
		case remainder == "":
			return host, "", false, nil
		case strings.HasPrefix(remainder, ":"):
			if len(remainder) == 1 {
				return "", "", false, fmt.Errorf("端口不能为空")
			}
			return host, remainder[1:], true, nil
		default:
			return "", "", false, fmt.Errorf("IPv6 地址格式错误")
		}
	}

	switch strings.Count(authority, ":") {
	case 0:
		return authority, "", false, nil
	case 1:
		host, port, err = net.SplitHostPort(authority)
		if err != nil {
			return "", "", false, fmt.Errorf("主机或端口格式错误: %w", err)
		}
		return host, port, true, nil
	default:
		// 多个冒号视为未加方括号、未指定端口的 IPv6 地址，避免误判末段为端口。
		return authority, "", false, nil
	}
}

func validateUser(user string) error {
	if user == "" {
		return nil
	}
	if hasControl(user) {
		return fmt.Errorf("用户名包含控制字符")
	}
	if strings.TrimSpace(user) != user {
		return fmt.Errorf("用户名首尾不能包含空白")
	}
	return nil
}

func validateHost(host string) error {
	if host == "" {
		return fmt.Errorf("连接地址缺少主机")
	}
	if hasControl(host) {
		return fmt.Errorf("主机包含控制字符")
	}
	if strings.IndexFunc(host, unicode.IsSpace) >= 0 {
		return fmt.Errorf("主机不能包含空白")
	}
	if strings.ContainsAny(host, `/\\?#@[]`) {
		return fmt.Errorf("主机包含无效字符")
	}
	if strings.Contains(host, ":") {
		ipText := host
		if percent := strings.LastIndexByte(ipText, '%'); percent >= 0 {
			if percent == len(ipText)-1 || strings.Contains(ipText[:percent], "%") {
				return fmt.Errorf("IPv6 区域标识格式错误")
			}
			ipText = ipText[:percent]
		}
		if net.ParseIP(ipText) == nil {
			return fmt.Errorf("IPv6 地址格式错误")
		}
	}
	return nil
}

func parsePort(text string) (int, error) {
	port, err := strconv.Atoi(text)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("端口必须是 1 到 65535 的整数")
	}
	return port, nil
}

func defaultPort(scheme string) int {
	if scheme == SchemeRDP {
		return 3389
	}
	return 22
}

func hasControl(value string) bool {
	return strings.IndexFunc(value, unicode.IsControl) >= 0
}
