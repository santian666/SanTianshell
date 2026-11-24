package core

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strings"
)

func outboundFromShareLink(line string) (map[string]any, error) {
	line = strings.TrimSpace(line)
	lower := strings.ToLower(line)
	if strings.HasPrefix(lower, "vmess://") {
		return outboundFromVMess(line)
	}
	u, err := url.Parse(line)
	if err != nil {
		return nil, errors.New("协议链接解析失败")
	}
	switch strings.ToLower(u.Scheme) {
	case "socks", "socks5":
		return outboundFromSocksURL(u)
	case "ss", "shadowsocks":
		return outboundFromSS(line, u)
	case "trojan":
		return outboundFromTrojanURL(u), nil
	case "vless":
		return outboundFromVlessURL(u), nil
	case "anytls":
		return outboundFromAnyTLSURL(u), nil
	default:
		return nil, errors.New("暂不支持该协议: " + u.Scheme)
	}
}

func outboundBaseFromURL(u *url.URL, typ string) map[string]any {
	return map[string]any{
		"type":        typ,
		"server":      u.Hostname(),
		"server_port": asInt(u.Port()),
	}
}

func outboundFromSocksURL(u *url.URL) (map[string]any, error) {
	out := outboundBaseFromURL(u, "socks")
	out["network"] = "tcp"
	if user := u.User.Username(); user != "" {
		out["username"] = user
	}
	if pass, ok := u.User.Password(); ok {
		out["password"] = pass
	}
	return out, nil
}

func outboundFromTrojanURL(u *url.URL) map[string]any {
	out := outboundBaseFromURL(u, "trojan")
	out["password"] = u.User.Username()
	applyTLS(out, u.Query())
	return out
}

func outboundFromVlessURL(u *url.URL) map[string]any {
	out := outboundBaseFromURL(u, "vless")
	out["uuid"] = u.User.Username()
	if flow := u.Query().Get("flow"); flow != "" {
		out["flow"] = flow
	}
	applyTLS(out, u.Query())
	return out
}

func outboundFromAnyTLSURL(u *url.URL) map[string]any {
	out := outboundBaseFromURL(u, "anytls")
	out["password"] = u.User.Username()
	applyTLS(out, u.Query())
	return out
}

func outboundFromSS(line string, u *url.URL) (map[string]any, error) {
	out := outboundBaseFromURL(u, "shadowsocks")
	method := ""
	password := ""
	if u.User.Username() != "" {
		method = u.User.Username()
		password, _ = u.User.Password()
	} else {
		raw := strings.TrimPrefix(line, "ss://")
		raw = strings.Split(raw, "#")[0]
		if strings.Contains(raw, "@") {
			parts := strings.SplitN(raw, "@", 2)
			decoded, err := decodeShareBase64(parts[0])
			if err == nil {
				mp := strings.SplitN(decoded, ":", 2)
				if len(mp) == 2 {
					method, password = mp[0], mp[1]
				}
			}
		} else if decoded, err := decodeShareBase64(raw); err == nil {
			du, err := url.Parse("ss://" + decoded)
			if err == nil {
				out = outboundBaseFromURL(du, "shadowsocks")
				method = du.User.Username()
				password, _ = du.User.Password()
			}
		}
	}
	out["method"] = method
	out["password"] = password
	return out, nil
}

func outboundFromVMess(line string) (map[string]any, error) {
	raw := strings.TrimPrefix(line, "vmess://")
	decoded, err := decodeShareBase64(raw)
	if err != nil {
		return nil, errors.New("VMess 链接解析失败")
	}
	var obj map[string]any
	if err := json.Unmarshal([]byte(decoded), &obj); err != nil {
		return nil, err
	}
	out := map[string]any{
		"type":        "vmess",
		"server":      asString(firstAny(obj["add"], obj["server"])),
		"server_port": asInt(firstAny(obj["port"], obj["server_port"])),
		"uuid":        asString(firstAny(obj["id"], obj["uuid"])),
		"security":    firstNonEmpty(asString(obj["scy"]), "auto"),
	}
	if aid := asInt(obj["aid"]); aid > 0 {
		out["alter_id"] = aid
	}
	if strings.EqualFold(asString(obj["tls"]), "tls") {
		out["tls"] = map[string]any{"enabled": true, "server_name": firstNonEmpty(asString(obj["sni"]), asString(obj["host"]))}
	}
	return out, nil
}

func applyTLS(out map[string]any, q url.Values) {
	security := strings.ToLower(firstNonEmpty(q.Get("security"), q.Get("tls")))
	serverName := firstNonEmpty(q.Get("sni"), q.Get("peer"), q.Get("host"))
	if security == "" && serverName == "" {
		return
	}
	if security == "none" || security == "false" {
		return
	}
	tls := map[string]any{"enabled": true}
	if truthyQueryValue(firstNonEmpty(q.Get("insecure"), q.Get("allowInsecure"), q.Get("allow_insecure"), q.Get("skip-cert-verify"))) {
		tls["insecure"] = true
	}
	if serverName != "" {
		tls["server_name"] = serverName
	}
	if security == "reality" {
		reality := map[string]any{"enabled": true}
		if pbk := q.Get("pbk"); pbk != "" {
			reality["public_key"] = pbk
		}
		if sid := q.Get("sid"); sid != "" {
			reality["short_id"] = sid
		}
		tls["reality"] = reality
	}
	if fp := q.Get("fp"); fp != "" {
		tls["utls"] = map[string]any{"enabled": true, "fingerprint": fp}
	}
	out["tls"] = tls
}

func truthyQueryValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func decodeShareBase64(value string) (string, error) {
	value = strings.TrimSpace(value)
	value = strings.TrimRight(value, "=")
	if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
		return string(decoded), nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(value); err == nil {
		return string(decoded), nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
		return string(decoded), nil
	}
	return "", errors.New("base64 解码失败")
}
