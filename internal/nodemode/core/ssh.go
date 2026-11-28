package core

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/net/proxy"
)

type CommandResult struct {
	Success        bool   `json:"success"`
	Stdout         string `json:"stdout"`
	Stderr         string `json:"stderr"`
	ExitCode       int    `json:"exitCode"`
	Message        string `json:"message"`
	Title          string `json:"title,omitempty"`
	Summary        string `json:"summary,omitempty"`
	Analysis       string `json:"analysis,omitempty"`
	Output         string `json:"output,omitempty"`
	InstallMessage string `json:"install_message,omitempty"`
}

const commandOutputLimit = 8 << 20

type commandBuffer struct {
	bytes.Buffer
	truncated bool
}

func (b *commandBuffer) Write(value []byte) (int, error) {
	length := len(value)
	remaining := commandOutputLimit - b.Len()
	if remaining > 0 {
		if remaining < len(value) {
			value = value[:remaining]
		}
		_, _ = b.Buffer.Write(value)
	}
	if length > remaining {
		b.truncated = true
	}
	return length, nil
}

func (b *commandBuffer) Text() string {
	if b.truncated {
		return b.Buffer.String() + "\n[输出超过 8 MB，已截断]\n"
	}
	return b.Buffer.String()
}

type SSHConnector func(VPS, int) (*ssh.Client, func(), error)

func (s *Service) acquireSSH(vps VPS, timeoutSeconds int) (*ssh.Client, func(), error) {
	s.sshConnectorMu.RLock()
	connector := s.sshConnector
	s.sshConnectorMu.RUnlock()
	if connector != nil {
		return connector(vps, timeoutSeconds)
	}
	client, err := s.connectSSH(vps, timeoutSeconds)
	if err != nil {
		return nil, nil, err
	}
	return client, func() { _ = client.Close() }, nil
}

func (s *Service) acquireInteractiveSSH(vps VPS, timeoutSeconds int) (*ssh.Client, func(), error) {
	s.sshConnectorMu.RLock()
	connector := s.interactiveConnector
	s.sshConnectorMu.RUnlock()
	if connector != nil {
		return connector(vps, timeoutSeconds)
	}
	return s.acquireSSH(vps, timeoutSeconds)
}

func (s *Service) runSSHCommandWithVPS(vps VPS, command string, timeoutSeconds int) CommandResult {
	client, release, err := s.acquireSSH(vps, timeoutSeconds)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	defer release()
	return executeSSH(client, command, timeoutSeconds)
}

func (s *Service) RunSSHCommand(alias string, command string, timeoutSeconds int) CommandResult {
	vps, err := s.getVPS(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	elevatedCmd := s.wrapRootCommand(vps, command)
	return s.runSSHCommandWithVPS(vps, elevatedCmd, timeoutSeconds)
}

func (s *Service) GetSingboxStatus(alias string) CommandResult {
	result := s.RunSSHCommand(alias, remoteSBCommand("s"), 30)
	status := singboxStatusFromResult(result)
	result.Message = status
	result.Success = status == "running"
	return result
}

func (s *Service) RefreshVPSState(alias string, _ bool) VPSRuntimeState {
	return s.refreshVPSState(alias, false)
}

func (s *Service) RefreshVPSStateInteractive(alias string, _ bool) VPSRuntimeState {
	return s.refreshVPSState(alias, true)
}

func classifySSHErrorCategory(err error) (string, string) {
	if err == nil {
		return "", ""
	}
	msg := strings.ToLower(err.Error())
	switch {
	case isSSHAuthError(err):
		return "auth_failed", "SSH 认证失败（请检查密码或私钥）"
	case strings.Contains(msg, "host key") || strings.Contains(msg, "主机密钥") || strings.Contains(msg, "knownhosts"):
		return "hostkey_failed", "SSH 主机指纹校验未通过（请在主机设置中重置或确认指纹）"
	case strings.Contains(msg, "socks") || strings.Contains(msg, "代理"):
		return "proxy_failed", "SSH 代理通道不可达（请检查本地/节点代理配置）"
	case strings.Contains(msg, "timeout") || strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout", "SSH 连接超时（请检查服务器网络、端口或防火墙）"
	default:
		return "network_failed", "SSH 连接失败"
	}
}

func (s *Service) refreshVPSState(alias string, interactive bool) VPSRuntimeState {
	state := VPSRuntimeState{Alias: strings.TrimSpace(alias), Status: "ssh_failed", StartPort: 20000}
	vps, err := s.getVPS(alias)
	if err != nil {
		state.Message = err.Error()
		state.ErrorCategory = "config_error"
		return state
	}
	canonicalAlias := firstNonEmpty(strings.TrimSpace(vps.Alias), strings.TrimSpace(vps.ID))
	state.Alias = canonicalAlias

	localConfigs, _ := s.loadNodeConfigMap(canonicalAlias)
	state.NodeCount, state.StartPort = nodeConfigRuntimeSummary(localConfigs)

	var client *ssh.Client
	var release func()
	if interactive {
		client, release, err = s.acquireInteractiveSSH(vps, 60)
	} else {
		client, release, err = s.acquireSSH(vps, 60)
	}
	if err != nil {
		cat, hint := classifySSHErrorCategory(err)
		state.ErrorCategory = cat
		if err.Error() != "" {
			state.Message = err.Error()
		} else {
			state.Message = hint
		}
		return state
	}
	defer release()

	// 单次 SSH 同时探测 sing-box 状态、内核版本、运行时长与远端 all.json 哈希，避免多次往返且不静默删除本地配置
	probeScript := strings.Join([]string{
		`echo "===SB_STATUS==="`,
		remoteSBCommand("s") + ` || true`,
		`echo "===SB_VERSION==="`,
		`sb_bin="/etc/sing-box/bin/sing-box"; [ -x "$sb_bin" ] || sb_bin=$(command -v sing-box 2>/dev/null || true); if [ -n "$sb_bin" ]; then "$sb_bin" version 2>/dev/null | head -n 1; fi`,
		`echo "===SB_UPTIME==="`,
		`if command -v systemctl >/dev/null 2>&1; then systemctl show -p ActiveEnterTimestamp --value sing-box 2>/dev/null || true; fi`,
		`echo "===SB_HASH==="`,
		`if [ -f /etc/sing-box/conf/all.json ]; then if command -v sha256sum >/dev/null 2>&1; then sha256sum /etc/sing-box/conf/all.json | awk '{print $1}'; else wc -c < /etc/sing-box/conf/all.json; fi; else echo "__MISSING__"; fi`,
	}, "\n")
	probeRes := executeSSH(client, s.wrapRootCommand(vps, probeScript), 30)
	sections := splitProbeSections(probeRes.Stdout)
	statusOut := sections["SB_STATUS"]
	state.Status = parseSingboxStatus(statusOut, probeRes.Stderr, probeRes.ExitCode)
	if verLine := strings.TrimSpace(sections["SB_VERSION"]); verLine != "" {
		verLine = strings.TrimPrefix(verLine, "sing-box version ")
		state.CoreVersion = strings.TrimSpace(verLine)
	}
	state.Uptime = strings.TrimSpace(sections["SB_UPTIME"])

	remoteHash := strings.TrimSpace(sections["SB_HASH"])
	localHasNodes := len(localConfigs) > 0
	if remoteHash == "" || remoteHash == "__MISSING__" {
		if localHasNodes {
			state.ConfigDiff = true
			state.Message = fmt.Sprintf("状态刷新完成（核心：%s），远端未检测到 all.json，本地保留 %d 个节点（未自动删除）", firstNonEmpty(state.CoreVersion, "未知"), state.NodeCount)
		} else {
			state.Message = "状态刷新完成，服务器无节点配置"
		}
		return state
	}

	if !localHasNodes {
		state.ConfigDiff = true
		state.Message = fmt.Sprintf("状态刷新完成（核心：%s），检测到远端存在配置而本地为空，可点击「同步配置」拉取", firstNonEmpty(state.CoreVersion, "未知"))
		return state
	}

	localHash := fmt.Sprintf("%x", sha256.Sum256(buildAllJSON(localConfigs)))
	if len(remoteHash) == 64 && !strings.EqualFold(remoteHash, localHash) {
		state.ConfigDiff = true
		state.Message = fmt.Sprintf("状态刷新完成（本地 %d 个节点，核心：%s），远端配置与本地存在差异，可点击「同步配置」对齐", state.NodeCount, firstNonEmpty(state.CoreVersion, "未知"))
		return state
	}
	state.Synced = true
	state.Message = fmt.Sprintf("状态刷新完成，节点数：%d，下一可用端口：%d", state.NodeCount, state.StartPort)
	if state.CoreVersion != "" {
		state.Message += fmt.Sprintf("，核心：%s", state.CoreVersion)
	}
	return state
}

func splitProbeSections(raw string) map[string]string {
	sections := map[string]string{}
	current := ""
	var buf strings.Builder
	flush := func() {
		if current != "" {
			sections[current] = strings.TrimSpace(buf.String())
		}
		buf.Reset()
	}
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "===") && strings.HasSuffix(trimmed, "===") && len(trimmed) > 6 {
			flush()
			current = strings.Trim(trimmed, "=")
			continue
		}
		if current != "" {
			buf.WriteString(line)
			buf.WriteByte('\n')
		}
	}
	flush()
	return sections
}

func singboxStatusFromResult(result CommandResult) string {
	if result.ExitCode == -1 && strings.TrimSpace(result.Stdout) == "" && strings.TrimSpace(result.Stderr) == "" {
		return "ssh_failed"
	}
	return parseSingboxStatus(result.Stdout, result.Stderr, result.ExitCode)
}

func nodeConfigRuntimeSummary(configs map[string]map[string]any) (int, int) {
	return len(configs), nextStartPort(maxListenPort(configs), 20000)
}

func (s *Service) ControlSingbox(alias string, action string) CommandResult {
	switch action {
	case "start", "stop", "restart", "status":
	default:
		return CommandResult{Success: false, ExitCode: -1, Message: "无效的 sing-box 操作"}
	}
	if action == "restart" {
		cmd := strings.Join([]string{
			`sb_bin="/etc/sing-box/bin/sing-box"; [ -x "$sb_bin" ] || sb_bin=$(command -v sing-box 2>/dev/null || true)`,
			`if [ -n "$sb_bin" ] && [ -x "$sb_bin" ] && [ -f /etc/sing-box/config.json ]; then`,
			`  if ! "$sb_bin" check -c /etc/sing-box/config.json -C /etc/sing-box/conf; then`,
			`    echo "sing-box 配置预检失败，已取消重启（原服务未中断）" >&2`,
			`    exit 1`,
			`  fi`,
			`fi`,
			remoteSBCommand("restart"),
			`sleep 1`,
			`if command -v systemctl >/dev/null 2>&1; then`,
			`  if ! systemctl is-active --quiet sing-box 2>/dev/null; then`,
			`    echo "=== sing-box 重启后未处于运行状态，最近错误日志 ===" >&2`,
			`    journalctl -u sing-box -n 30 --no-pager 2>&1 || tail -n 30 /var/log/sing-box/service.log 2>&1 || true`,
			`    exit 1`,
			`  fi`,
			`fi`,
		}, "\n")
		res := s.RunSSHCommand(alias, cmd, 60)
		if res.Success {
			res.Message = "sing-box 配置预检通过，服务已重启并验证运行正常"
		} else if res.Message == "" {
			res.Message = "sing-box 重启或健康自检失败，请查看错误日志"
		}
		return res
	}
	sbAction := action
	if action == "status" {
		sbAction = "s"
	}
	return s.RunSSHCommand(alias, remoteSBCommand(sbAction), 60)
}

func (s *Service) DeploySingbox(alias string) CommandResult {
	return s.RunSSHCommand(alias, deploySingboxCommand(), 900)
}

func (s *Service) GetSingboxLog(alias string, maxLines int) CommandResult {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return CommandResult{Success: false, ExitCode: -1, Message: "VPS 别名不能为空"}
	}
	if maxLines <= 0 || maxLines > 2000 {
		maxLines = 200
	}
	script := getSingboxLogScript(maxLines)
	result := s.RunSSHCommand(alias, script, 30)
	result.Title = fmt.Sprintf("%s 日志", alias)
	output := strings.TrimSpace(result.Stdout)
	if output == "" && strings.TrimSpace(result.Stderr) != "" {
		output = strings.TrimSpace(result.Stderr)
	}
	result.Output = output
	if result.ExitCode == 0 {
		result.Success = true
		if result.Message == "" {
			result.Message = "获取日志成功"
		}
	}
	return result
}

func getSingboxLogScript(maxLines int) string {
	return fmt.Sprintf(`n=%d
found=0
if [ -s /var/log/sing-box/service.log ]; then
  echo "=== [系统服务日志 /var/log/sing-box/service.log (最后 $n 行)] ==="
  tail -n "$n" /var/log/sing-box/service.log
  echo ""
  found=1
fi
if [ -s /var/log/sing-box/sing-box.log ]; then
  echo "=== [内核运行日志 /var/log/sing-box/sing-box.log (最后 $n 行)] ==="
  tail -n "$n" /var/log/sing-box/sing-box.log
  echo ""
  found=1
fi
if [ "$found" -eq 0 ]; then
  if command -v journalctl >/dev/null 2>&1; then
    echo "=== [systemd journalctl -u sing-box (最后 $n 行)] ==="
    journalctl -u sing-box -n "$n" --no-pager 2>&1 || echo "无法读取 journalctl 日志"
  else
    echo "未检测到 sing-box 日志文件 (/var/log/sing-box/*.log)，且系统未安装 journalctl。"
  fi
fi`, maxLines)
}

func isNonRootUser(username string) bool {
	u := strings.TrimSpace(strings.ToLower(username))
	return u != "" && u != "root"
}

func (s *Service) wrapRootCommand(vps VPS, command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}
	password := ""
	if s != nil {
		password = s.resolvePassword(vps)
	} else {
		password = vps.Password
	}
	return buildRootCommand(command, password, isNonRootUser(vps.Username))
}

func buildRootCommand(command string, password string, forceElevation bool) string {
	command = strings.TrimSpace(command)
	if command == "" || !forceElevation {
		return command
	}
	passQuoted := shellQuote(password)
	hasPassword := strings.TrimSpace(password) != ""

	if strings.Contains(command, "\n") || strings.Contains(command, "<<") {
		var b strings.Builder
		b.WriteString("set -e\n")
		b.WriteString("__sb_script=$(mktemp /tmp/santianshell-cmd.XXXXXX)\n")
		b.WriteString("trap 'rm -f \"$__sb_script\"' EXIT\n")
		b.WriteString("cat << 'SANTIANSHELL_SCRIPT_PAYLOAD' > \"$__sb_script\"\n")
		b.WriteString(command)
		b.WriteString("\nSANTIANSHELL_SCRIPT_PAYLOAD\n")
		b.WriteString("if [ \"$(id -u)\" -eq 0 ]; then\n")
		b.WriteString("  bash \"$__sb_script\"\n")
		b.WriteString("elif command -v sudo >/dev/null 2>&1; then\n")
		b.WriteString("  if sudo -n true 2>/dev/null; then\n")
		b.WriteString("    sudo -n bash \"$__sb_script\"\n")
		if hasPassword {
			b.WriteString(fmt.Sprintf("  else\n    printf '%%s\\n' %s | sudo -S -p '' -- bash \"$__sb_script\"\n", passQuoted))
		} else {
			b.WriteString("  else\n    sudo bash \"$__sb_script\"\n")
		}
		b.WriteString("  fi\n")
		b.WriteString("elif command -v su >/dev/null 2>&1; then\n")
		if hasPassword {
			b.WriteString(fmt.Sprintf("  printf '%%s\\n' %s | su - -c \"bash \\\"$__sb_script\\\"\"\n", passQuoted))
		} else {
			b.WriteString("  su - -c \"bash \\\"$__sb_script\\\"\"\n")
		}
		b.WriteString("else\n")
		b.WriteString("  bash \"$__sb_script\"\n")
		b.WriteString("fi")
		return b.String()
	}

	cmdQuoted := shellQuote(command)
	var b strings.Builder
	b.WriteString("if [ \"$(id -u)\" -eq 0 ]; then\n")
	b.WriteString("  " + command + "\n")
	b.WriteString("elif command -v sudo >/dev/null 2>&1; then\n")
	b.WriteString("  if sudo -n true 2>/dev/null; then\n")
	b.WriteString("    sudo -n bash -c " + cmdQuoted + "\n")
	if hasPassword {
		b.WriteString(fmt.Sprintf("  else\n    printf '%%s\\n' %s | sudo -S -p '' -- bash -c %s\n", passQuoted, cmdQuoted))
	} else {
		b.WriteString("  else\n    sudo bash -c " + cmdQuoted + "\n")
	}
	b.WriteString("  fi\n")
	b.WriteString("elif command -v su >/dev/null 2>&1; then\n")
	if hasPassword {
		b.WriteString(fmt.Sprintf("  printf '%%s\\n' %s | su - -c %s\n", passQuoted, cmdQuoted))
	} else {
		b.WriteString("  su - -c " + cmdQuoted + "\n")
	}
	b.WriteString("else\n")
	b.WriteString("  " + command + "\n")
	b.WriteString("fi")
	return b.String()
}

func (s *Service) downloadRemoteConfig(client *ssh.Client, vps VPS, remotePath string) ([]byte, error) {
	content, err := downloadText(client, remotePath)
	if err == nil {
		return content, nil
	}
	if isNonRootUser(vps.Username) || (err != nil && strings.Contains(strings.ToLower(err.Error()), "permission")) {
		cmd := s.wrapRootCommand(vps, "cat "+shellQuote(remotePath))
		res := executeSSH(client, cmd, 30)
		if res.Success && len(strings.TrimSpace(res.Stdout)) > 0 {
			return []byte(res.Stdout), nil
		}
	}
	return nil, err
}

func deploySingboxCommand() string {
	return strings.TrimSpace(`
cd /
export TERM=xterm
set -e
install_url='https://github.com/233boy/sing-box/raw/main/install.sh'
tmp_file=$(mktemp /tmp/vps-management-singbox.XXXXXX)
install_status=0
trap 'rm -f "$tmp_file"' EXIT
protect_singbox_logs() {
  if command -v jq >/dev/null 2>&1; then
    if [ -f /etc/sing-box/config.json ]; then
      config_tmp=$(mktemp /tmp/vps-management-singbox-config.XXXXXX)
      if jq '.log = {"disabled": false, "level": "warn", "output": "/var/log/sing-box/sing-box.log", "timestamp": true} | if (.dns == null or .dns == {} or (.dns.servers | length == 0)) then .dns = {"servers": [{"type": "local", "tag": "dns-local", "prefer_go": true}]} else . end | .dns.servers |= unique_by(.tag) | (if (.dns.servers | length > 0) then .dns.servers[0].tag else "dns-local" end) as $tag | if .route == null then .route = {"default_domain_resolver": $tag} elif .route.default_domain_resolver == null then .route.default_domain_resolver = $tag else . end' /etc/sing-box/config.json > "$config_tmp"; then
        chmod --reference="/etc/sing-box/config.json" "$config_tmp" 2>/dev/null || chmod 600 "$config_tmp"
        chown --reference="/etc/sing-box/config.json" "$config_tmp" 2>/dev/null || true
        mv -f "$config_tmp" /etc/sing-box/config.json
      else
        rm -f "$config_tmp"
      fi
    fi
    if [ -f /etc/sing-box/conf/all.json ]; then
      config_tmp=$(mktemp /tmp/vps-management-singbox-config.XXXXXX)
      if jq '.log = {"disabled": false, "level": "warn", "output": "/var/log/sing-box/sing-box.log", "timestamp": true} | del(.dns) | if .route then del(.route.default_domain_resolver) else . end' /etc/sing-box/conf/all.json > "$config_tmp"; then
        chmod --reference="/etc/sing-box/conf/all.json" "$config_tmp" 2>/dev/null || chmod 600 "$config_tmp"
        chown --reference="/etc/sing-box/conf/all.json" "$config_tmp" 2>/dev/null || true
        mv -f "$config_tmp" /etc/sing-box/conf/all.json
      else
        rm -f "$config_tmp"
      fi
    fi
    mkdir -p /etc/sing-box /etc/sing-box/conf 2>/dev/null || true
    chmod 755 /etc/sing-box /etc/sing-box/conf 2>/dev/null || true
    chmod 644 /etc/sing-box/config.json /etc/sing-box/conf/all.json 2>/dev/null || true
  else
    echo '警告：远端缺少 jq，无法修改 sing-box 配置日志。' >&2
  fi

  mkdir -p /etc/logrotate.d
  cat > /etc/logrotate.d/vps-management-singbox <<'EOF'
/var/log/sing-box.log /var/log/sing-box/*.log {
  daily
  maxsize 10M
  rotate 7
  compress
  dateext
  dateformat -%Y%m%d%H%M%S
  missingok
  notifempty
  copytruncate
}
EOF

  mkdir -p /usr/local/sbin
  cat > /usr/local/sbin/vps-management-singbox-log-clean <<'EOF'
#!/bin/sh
set -eu
if command -v logrotate >/dev/null 2>&1; then
  mkdir -p /var/lib/logrotate
  logrotate -s /var/lib/logrotate/vps-management-singbox.status /etc/logrotate.d/vps-management-singbox
  exit 0
fi
max_bytes=10485760
for log_file in /var/log/sing-box.log /var/log/sing-box/*.log; do
  [ -f "$log_file" ] || continue
  log_size=$(wc -c < "$log_file" 2>/dev/null || echo 0)
  if [ "$log_size" -gt "$max_bytes" ]; then
    cp -f "$log_file" "$log_file.$(date +%Y%m%d%H%M%S)" 2>/dev/null || true
    : > "$log_file"
  fi
done
find /var/log/sing-box -maxdepth 1 -type f -name '*.log.*' -mtime +7 -delete 2>/dev/null || true
find /var/log -maxdepth 1 -type f -name 'sing-box.log.*' -mtime +7 -delete 2>/dev/null || true
EOF
  chmod 755 /usr/local/sbin/vps-management-singbox-log-clean

  if command -v systemctl >/dev/null 2>&1; then
    if command -v apt-get >/dev/null 2>&1 && ! systemctl is-active --quiet systemd-resolved 2>/dev/null; then
      DEBIAN_FRONTEND=noninteractive apt-get update -qq >/dev/null 2>&1 || true
      DEBIAN_FRONTEND=noninteractive apt-get install -y -qq systemd-resolved >/dev/null 2>&1 || true
      mkdir -p /etc/systemd/resolved.conf.d
      cat << 'RESEOF' > /etc/systemd/resolved.conf.d/dns.conf 2>/dev/null || true
[Resolve]
DNS=1.1.1.1 8.8.8.8
FallbackDNS=8.8.4.4 1.0.0.1
RESEOF
      systemctl enable --now systemd-resolved >/dev/null 2>&1 || true
      systemctl restart systemd-resolved >/dev/null 2>&1 || true
    fi
    mkdir -p /var/log/sing-box
    mkdir -p /etc/systemd/system/sing-box.service.d
    cat > /etc/systemd/system/sing-box.service.d/10-santianshell-exec.conf <<'EOF'
[Service]
Environment=ENABLE_DEPRECATED_MISSING_DOMAIN_RESOLVER=true
ExecStart=
ExecStart=/etc/sing-box/bin/sing-box run -c /etc/sing-box/config.json -C /etc/sing-box/conf
EOF
    cat > /etc/systemd/system/sing-box.service.d/90-vps-management-log.conf <<'EOF'
[Service]
StandardOutput=append:/var/log/sing-box/service.log
StandardError=append:/var/log/sing-box/service.log
EOF
    cat > /etc/systemd/system/vps-management-singbox-log-clean.service <<'EOF'
[Unit]
Description=清理并轮转 sing-box 日志

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/vps-management-singbox-log-clean
StandardOutput=null
StandardError=null
EOF
    cat > /etc/systemd/system/vps-management-singbox-log-clean.timer <<'EOF'
[Unit]
Description=每 6 小时清理并轮转 sing-box 日志

[Timer]
OnBootSec=10min
OnUnitActiveSec=6h
Persistent=true
Unit=vps-management-singbox-log-clean.service

[Install]
WantedBy=timers.target
EOF
  fi

  if command -v pgrep >/dev/null 2>&1; then
    for pid in $(pgrep -x sing-box 2>/dev/null || true); do
      if [ -r "/proc/$pid/cmdline" ] && tr '\000' ' ' < "/proc/$pid/cmdline" | grep -q '/tmp/sb_node_ip/'; then
        kill "$pid" >/dev/null 2>&1 || true
      fi
    done
  fi
  rm -rf /tmp/sb_node_ip
  rm -f /var/log/sing-box.log /var/log/sing-box.log.*
  if [ -d /var/log/sing-box ]; then
    find /var/log/sing-box -maxdepth 1 -type f -name '*.log*' -delete
  fi
  if [ -x /etc/sing-box/bin/sing-box ]; then
    mkdir -p /usr/local/bin /usr/bin 2>/dev/null || true
    ln -sf /etc/sing-box/bin/sing-box /usr/local/bin/sing-box 2>/dev/null || true
    ln -sf /etc/sing-box/bin/sing-box /usr/bin/sing-box 2>/dev/null || true
  fi
  if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload || true
    systemctl enable --now vps-management-singbox-log-clean.timer >/dev/null 2>&1 || true
    systemctl start vps-management-singbox-log-clean.service >/dev/null 2>&1 || echo '警告：sing-box 日志轮转执行失败。' >&2
    nohup sh -c 'sleep 10; systemctl try-restart sing-box >/dev/null 2>&1 || true' >/dev/null 2>&1 &
  fi
  if command -v iptables >/dev/null 2>&1; then
    iptables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || true
    iptables -C INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || iptables -I INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || true
  fi
  if command -v ip6tables >/dev/null 2>&1; then
    ip6tables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || ip6tables -I INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || true
    ip6tables -C INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || ip6tables -I INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || true
  fi
  if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qw "active"; then
    ufw allow 10000:65000/tcp 2>/dev/null || true
    ufw allow 10000:65000/udp 2>/dev/null || true
  fi
  if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then
    firewall-cmd --permanent --add-port=10000-65000/tcp >/dev/null 2>&1 || true
    firewall-cmd --permanent --add-port=10000-65000/udp >/dev/null 2>&1 || true
    firewall-cmd --reload >/dev/null 2>&1 || true
  fi
}
install_mirror_url='https://ghfast.top/https://github.com/233boy/sing-box/raw/main/install.sh'
if command -v curl >/dev/null 2>&1; then
  curl -fsSL --connect-timeout 15 --max-time 60 "$install_url" -o "$tmp_file" || curl -fsSL --connect-timeout 15 --max-time 60 "$install_mirror_url" -o "$tmp_file"
elif command -v wget >/dev/null 2>&1; then
  wget -q --timeout=30 -O "$tmp_file" "$install_url" || wget -q --timeout=30 -O "$tmp_file" "$install_mirror_url"
else
  echo '远端缺少 curl/wget，无法下载 sing-box 安装脚本。' >&2
  exit 127
fi
bash "$tmp_file" || install_status=$?
sb_path=$(command -v sb 2>/dev/null || true)
if [ -z "$sb_path" ]; then
  for p in /usr/local/bin/sb /usr/bin/sb /bin/sb /etc/sing-box/sb; do
    if [ -x "$p" ]; then
      sb_path="$p"
      break
    fi
  done
fi
if [ -n "$sb_path" ]; then
  if [ "$install_status" -ne 0 ]; then
    echo "安装脚本返回退出码 $install_status，已检测到 sb，继续检查服务状态。"
  fi
  protect_singbox_logs
  "$sb_path" bbr || echo 'BBR 设置失败或系统不支持，已跳过。'
  "$sb_path" s || true
  echo 'sing-box 部署完成。'
elif command -v sing-box >/dev/null 2>&1 || [ -x /etc/sing-box/bin/sing-box ]; then
  if [ "$install_status" -ne 0 ]; then
    echo "安装脚本返回退出码 $install_status，已检测到 sing-box 可执行文件，按部署成功处理。"
  fi
  protect_singbox_logs
  echo 'sing-box 已安装，但未找到 sb 管理命令；部署已完成，BBR 已跳过。'
else
  echo "安装脚本执行完成，退出码 $install_status，但未找到 sing-box 或 sb，请查看安装日志。" >&2
  exit 127
fi
`)
}

func remoteSBCommand(action string) string {
	return "cd / && sb_action=" + shellQuote(action) + "; sb_path=$(command -v sb 2>/dev/null || true); " +
		"if [ -z \"$sb_path\" ]; then for p in /usr/local/bin/sb /usr/bin/sb /bin/sb /etc/sing-box/sb; do if [ -x \"$p\" ]; then sb_path=\"$p\"; break; fi; done; fi; " +
		"if [ -n \"$sb_path\" ]; then \"$sb_path\" \"$sb_action\"; exit $?; fi; " +
		"if command -v systemctl >/dev/null 2>&1; then case \"$sb_action\" in s|status) systemctl is-active sing-box; exit $? ;; start|stop|restart) systemctl \"$sb_action\" sing-box; exit $? ;; esac; fi; " +
		"if command -v service >/dev/null 2>&1; then case \"$sb_action\" in s|status) service sing-box status; exit $? ;; start|stop|restart) service sing-box \"$sb_action\"; exit $? ;; esac; fi; " +
		"if [ -z \"$sb_path\" ]; then echo '未找到 sb 命令，请先部署 sing-box。' >&2; exit 127; fi; " +
		"\"$sb_path\" \"$sb_action\""
}

func (s *Service) RebootVPS(alias string) CommandResult {
	cmd := "nohup sh -c 'sleep 2; (systemctl reboot || /sbin/reboot || reboot)' >/tmp/vps-management-reboot.log 2>&1 &"
	res := s.RunSSHCommand(alias, cmd, 12)
	if res.Success {
		res.Message = "已发送重启指令，服务器将在 2 秒后重启"
	}
	return res
}

func (s *Service) DebugVPS(alias string, action string, target string, port int) CommandResult {
	action = strings.ToLower(strings.TrimSpace(action))
	target = strings.TrimSpace(target)
	if action == "iperf3" {
		return s.debugIperf3(alias, target)
	}
	if action != "ping" && action != "mtr" && action != "port" && action != "dns" && action != "exitip" {
		return CommandResult{Success: false, ExitCode: -1, Message: "调试动作无效"}
	}
	if action == "dns" && target == "" {
		target = "google.com"
	}
	if action != "exitip" {
		if _, err := validateDebugTarget(target); err != nil {
			return CommandResult{Success: false, ExitCode: -1, Message: err.Error(), Summary: err.Error()}
		}
	}
	if action == "port" && (port < 1 || port > 65535) {
		return CommandResult{Success: false, ExitCode: -1, Message: "端口必须是 1-65535 的数字。", Summary: "端口必须是 1-65535 的数字。"}
	}
	vps, err := s.getVPS(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error(), Summary: err.Error()}
	}
	client, release, err := s.acquireSSH(vps, 30)
	if err != nil {
		msg := "SSH 连接失败：请检查 VPS 地址、端口、密码/密钥，或全局代理配置。"
		return CommandResult{Success: false, ExitCode: -1, Message: msg + " " + err.Error(), Summary: msg}
	}
	defer release()
	installMessage := ""
	if action == "ping" || action == "mtr" || action == "port" {
		var ready bool
		ready, installMessage = s.ensureDebugTool(client, vps, action)
		if !ready {
			return CommandResult{Success: false, ExitCode: -1, Message: installMessage, Summary: installMessage, Output: installMessage, InstallMessage: installMessage}
		}
	}
	var command string
	timeoutSeconds := 30
	switch action {
	case "ping":
		command = "LANG=C ping -c 4 -W 2 " + shellQuote(target)
		timeoutSeconds = 25
	case "mtr":
		command = "LANG=C mtr -r -c 8 -w " + shellQuote(target)
		timeoutSeconds = 80
	case "port":
		tcpProbe := shellQuote("cat < /dev/null > /dev/tcp/" + target + "/" + strconv.Itoa(port))
		command = "if command -v nc >/dev/null 2>&1; then nc -vz -w 5 " + shellQuote(target) + " " + shellQuote(strconv.Itoa(port)) + "; else bash -lc " + tcpProbe + "; fi"
		timeoutSeconds = 20
	case "dns":
		tQ := shellQuote(target)
		command = "if command -v dig >/dev/null 2>&1; then dig +time=3 +tries=2 " + tQ + "; elif command -v getent >/dev/null 2>&1; then getent ahosts " + tQ + "; elif command -v nslookup >/dev/null 2>&1; then nslookup " + tQ + "; else echo '缺少 dig/getent/nslookup' >&2; exit 127; fi"
		timeoutSeconds = 15
	case "exitip":
		command = "echo '=== IPv4 出口 ==='; curl -4 -fsS --max-time 6 https://api.ipify.org 2>&1 || echo '不可用'; echo ''; echo '=== IPv6 出口 ==='; curl -6 -fsS --max-time 6 https://api64.ipify.org 2>&1 || echo '不可用'; echo ''; echo '=== Cloudflare 204 连通性 ==='; curl -fsS -o /dev/null -w 'HTTP %{http_code}, 建连 %{time_connect}s, 总耗时 %{time_total}s\n' --max-time 8 https://cp.cloudflare.com/generate_204 2>&1"
		timeoutSeconds = 25
	}
	result := executeSSH(client, command, timeoutSeconds)
	output := mergeCommandOutput(result.Stdout, result.Stderr)
	result.Output = output
	result.InstallMessage = installMessage
	result.Title = map[string]string{
		"ping":   "Ping 连通性检测",
		"mtr":    "MTR 路由追踪",
		"port":   "IP + 端口检测",
		"dns":    "DNS 解析检测",
		"exitip": "出口 IP 与网络连通检测",
	}[action]
	switch action {
	case "ping":
		result.Summary = pingDebugSummary(target, output, result.ExitCode)
	case "mtr":
		result.Summary = mtrDebugSummary(target, result.ExitCode)
		result.Analysis = analyzeMtrOutput(output, target, result.ExitCode)
	case "port":
		result.Summary = portDebugSummary(target, port, result.ExitCode)
		result.Analysis = result.Summary
	case "dns":
		if result.Success {
			result.Summary = fmt.Sprintf("DNS 解析 %s 成功", target)
		} else {
			result.Summary = fmt.Sprintf("DNS 解析 %s 失败", target)
		}
		result.Analysis = result.Summary
	case "exitip":
		if result.Success {
			result.Summary = "出口 IP 与外网连通性检测完成"
		} else {
			result.Summary = "出口 IP 检测失败或外网受限"
		}
		result.Analysis = result.Summary
	}
	result.Message = result.Summary
	return result
}

func (s *Service) debugIperf3(clientAlias string, serverAlias string) CommandResult {
	clientAlias = strings.TrimSpace(clientAlias)
	serverAlias = strings.TrimSpace(serverAlias)
	useLocalClient := clientAlias == "__local_windows__"
	if serverAlias == "" {
		return CommandResult{Success: false, ExitCode: -1, Message: "请选择目标 VPS。", Summary: "请选择目标 VPS。"}
	}
	if !useLocalClient && strings.EqualFold(clientAlias, serverAlias) {
		return CommandResult{Success: false, ExitCode: -1, Message: "目标 VPS 不能选择当前 VPS。", Summary: "目标 VPS 不能选择当前 VPS。"}
	}
	serverVPS, err := s.getVPS(serverAlias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "没有找到目标 VPS。", Summary: "没有找到目标 VPS。"}
	}
	if _, err := validateDebugTarget(serverVPS.Address); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "目标 VPS 地址格式不正确。", Summary: "目标 VPS 地址格式不正确。"}
	}

	server, releaseServer, err := s.acquireSSH(serverVPS, 45)
	if err != nil {
		msg := "目标 VPS SSH 连接失败。"
		return CommandResult{Success: false, ExitCode: -1, Message: msg + " " + err.Error(), Summary: msg}
	}
	defer releaseServer()

	clientInstall := "本地 Windows：iperf3 可用"
	var client *ssh.Client
	var releaseClient func()
	if !useLocalClient {
		clientVPS, err := s.getVPS(clientAlias)
		if err != nil {
			return CommandResult{Success: false, ExitCode: -1, Message: "没有找到当前 VPS。", Summary: "没有找到当前 VPS。"}
		}
		client, releaseClient, err = s.acquireSSH(clientVPS, 45)
		if err != nil {
			msg := "当前 VPS SSH 连接失败。"
			return CommandResult{Success: false, ExitCode: -1, Message: msg + " " + err.Error(), Summary: msg}
		}
		defer releaseClient()
		clientReady, installMessage := s.ensureDebugTool(client, clientVPS, "iperf3")
		clientInstall = installMessage
		if !clientReady {
			return CommandResult{Success: false, ExitCode: -1, Message: clientInstall, Summary: "当前 VPS 安装 IPerf3 失败：" + clientInstall, Output: clientInstall, InstallMessage: clientInstall}
		}
	} else if _, err := localIperf3Path(); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error(), Summary: err.Error(), Output: err.Error(), InstallMessage: err.Error()}
	}
	serverReady, serverInstall := s.ensureDebugTool(server, serverVPS, "iperf3")
	if !serverReady {
		return CommandResult{Success: false, ExitCode: -1, Message: serverInstall, Summary: "目标 VPS 安装 IPerf3 失败：" + serverInstall, Output: serverInstall, InstallMessage: serverInstall}
	}

	runID := fmt.Sprintf("vps_mgmt_iperf3_%d", time.Now().UnixNano())
	runIDQ := shellQuote(runID)
	iperfPort := 15201 + int(time.Now().UnixNano()%95)
	portStr := strconv.Itoa(iperfPort)
	portQ := shellQuote(portStr)
	startCmd := "rm -f /tmp/" + runIDQ + ".log /tmp/" + runIDQ + ".pid; nohup sh -c 'if command -v timeout >/dev/null 2>&1; then exec timeout 120 iperf3 -s -p " + portQ + "; else exec iperf3 -s -p " + portQ + "; fi' > /tmp/" + runIDQ + ".log 2>&1 & echo $! > /tmp/" + runIDQ + ".pid; cat /tmp/" + runIDQ + ".pid"
	startResult := executeSSH(server, startCmd, 10)
	if !startResult.Success {
		output := mergeCommandOutput(startResult.Stdout, startResult.Stderr)
		return CommandResult{Success: false, Stdout: startResult.Stdout, Stderr: startResult.Stderr, ExitCode: startResult.ExitCode, Message: "目标 VPS " + serverAlias + " 启动 IPerf3 服务端失败。", Summary: "目标 VPS " + serverAlias + " 启动 IPerf3 服务端失败。", Output: output}
	}
	defer executeSSH(server, "if [ -f /tmp/"+runIDQ+".pid ]; then kill $(cat /tmp/"+runIDQ+".pid) >/dev/null 2>&1 || true; fi; rm -f /tmp/"+runIDQ+".pid /tmp/"+runIDQ+".log", 8)
	time.Sleep(700 * time.Millisecond)

	var result CommandResult
	if useLocalClient {
		forwardHost, forwardPort, stopForward, err := startLocalTCPForward(server.Dial, "127.0.0.1:"+portStr)
		if err != nil {
			return CommandResult{Success: false, ExitCode: -1, Message: "建立 IPerf3 SSH 转发失败：" + err.Error(), Summary: "建立 IPerf3 SSH 转发失败。"}
		}
		defer stopForward()
		result = executeLocalIperf3Pair(forwardHost, forwardPort)
	} else {
		result = executeRemoteIperf3Pair(client, serverVPS.Address, portStr)
	}
	output := "当前 VPS：" + clientInstall + "\n目标 VPS：" + serverInstall + "\n\n" + mergeCommandOutput(result.Stdout, result.Stderr)
	result.Title = "IPerf3 测速"
	result.Output = output
	result.InstallMessage = clientInstall + "\n" + serverInstall
	result.Summary = iperf3DebugSummary(output, result.ExitCode, serverAlias)
	result.Analysis = result.Summary
	result.Message = result.Summary
	return result
}

func (s *Service) GetTrafficRank(alias string) CommandResult {
	nodeMap, ports := s.trafficNodeMap(alias)
	if len(ports) == 0 {
		data := map[string]any{"success": true, "realtime": []any{}, "total": []any{}, "total_available": false, "total_message": "本地没有可匹配端口的节点配置"}
		raw, _ := json.Marshal(data)
		return CommandResult{Success: true, Stdout: string(raw), Message: "本地没有可匹配端口的节点配置"}
	}
	// 零远端 python3 依赖：直接由 POSIX shell 采样 ss -tinH（间隔 0.5s）+ nftables 计数器，本地 Go 解析
	cmd := strings.Join([]string{
		`echo "===SS_FIRST==="`,
		`ss -tinH 2>/dev/null || true`,
		`sleep 0.5`,
		`echo "===SS_SECOND==="`,
		`ss -tinH 2>/dev/null || true`,
		`echo "===NFT_TOTAL==="`,
		`nft -j list table inet nsb_traffic 2>/dev/null || true`,
	}, "\n")
	result := s.RunSSHCommand(alias, cmd, 20)
	if !result.Success {
		return result
	}
	sections := splitProbeSections(result.Stdout)
	portSet := make(map[int]bool, len(ports))
	for _, p := range ports {
		portSet[p] = true
	}
	firstConns, _, has1 := parseSSConnections(sections["SS_FIRST"], portSet)
	secondConns, counts, has2 := parseSSConnections(sections["SS_SECOND"], portSet)
	realtimeRows := []map[string]any{}
	if has1 || has2 || len(counts) > 0 {
		realtimeRows = computeRealtimeTraffic(firstConns, secondConns, counts, ports, 0.5)
	}
	totalRows, totalAvailable := parseNftTrafficJSON(sections["NFT_TOTAL"], portSet, ports)
	totalMsg := ""
	if !totalAvailable {
		totalMsg = "端口总流量统计未启用（点击上方「启用总流量统计」即可初始化 TCP+UDP 累计计数）"
	}
	data := map[string]any{
		"success":         true,
		"source":          "ss_tcp_go",
		"sample_seconds":  0.5,
		"realtime":        realtimeRows,
		"total":           totalRows,
		"total_available": totalAvailable,
		"total_message":   totalMsg,
	}
	attachTrafficNodeInfo(data, nodeMap)
	raw, _ := json.Marshal(data)
	result.Stdout = string(raw)
	result.Message = "流量采集完成"
	return result
}

func (s *Service) InitTrafficCounter(alias string) CommandResult {
	_, ports := s.trafficNodeMap(alias)
	if len(ports) == 0 {
		return CommandResult{Success: false, ExitCode: -1, Message: "本地没有可统计端口的节点配置"}
	}
	var rules strings.Builder
	rules.WriteString("if ! command -v nft >/dev/null 2>&1; then echo '服务器未安装 nftables (nft)' >&2; exit 127; fi\n")
	rules.WriteString("nft -f - <<'EOF'\n")
	rules.WriteString("table inet nsb_traffic\n")
	rules.WriteString("flush table inet nsb_traffic\n")
	rules.WriteString("table inet nsb_traffic {\n")
	for _, p := range ports {
		rules.WriteString(fmt.Sprintf("  counter nsb_p%d_rx {}\n", p))
		rules.WriteString(fmt.Sprintf("  counter nsb_p%d_tx {}\n", p))
	}
	rules.WriteString("  chain input {\n    type filter hook input priority 0; policy accept;\n")
	for _, p := range ports {
		rules.WriteString(fmt.Sprintf("    tcp dport %d counter name \"nsb_p%d_rx\"\n", p, p))
		rules.WriteString(fmt.Sprintf("    udp dport %d counter name \"nsb_p%d_rx\"\n", p, p))
	}
	rules.WriteString("  }\n")
	rules.WriteString("  chain output {\n    type filter hook output priority 0; policy accept;\n")
	for _, p := range ports {
		rules.WriteString(fmt.Sprintf("    tcp sport %d counter name \"nsb_p%d_tx\"\n", p, p))
		rules.WriteString(fmt.Sprintf("    udp sport %d counter name \"nsb_p%d_tx\"\n", p, p))
	}
	rules.WriteString("  }\n}\nEOF\n")
	res := s.RunSSHCommand(alias, rules.String(), 30)
	if res.Success {
		res.Message = fmt.Sprintf("已为 %d 个节点端口初始化 nftables TCP+UDP 总流量统计", len(ports))
	}
	return res
}

type ssConnSample struct {
	Port    int
	RxBytes int64
	TxBytes int64
}

var (
	reSSPort   = regexp.MustCompile(`:(\d+)$`)
	reSSSent   = regexp.MustCompile(`\bbytes_sent:(\d+)`)
	reSSAcked  = regexp.MustCompile(`\bbytes_acked:(\d+)`)
	reSSRecv   = regexp.MustCompile(`\bbytes_received:(\d+)`)
	reNftCount = regexp.MustCompile(`^nsb_p(\d+)_(rx|tx)$`)
)

func parseSSMetricsLine(text string) (int64, int64) {
	var rx, tx int64
	if m := reSSRecv.FindStringSubmatch(text); len(m) == 2 {
		rx, _ = strconv.ParseInt(m[1], 10, 64)
	}
	if m := reSSSent.FindStringSubmatch(text); len(m) == 2 {
		tx, _ = strconv.ParseInt(m[1], 10, 64)
	} else if m := reSSAcked.FindStringSubmatch(text); len(m) == 2 {
		tx, _ = strconv.ParseInt(m[1], 10, 64)
	}
	return rx, tx
}

func parseSSConnections(raw string, portSet map[int]bool) (map[string]ssConnSample, map[int]int, bool) {
	conns := map[string]ssConnSample{}
	counts := map[int]int{}
	hasMetrics := false
	currentKey := ""
	for _, line := range strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if currentKey != "" {
				rx, tx := parseSSMetricsLine(line)
				if rx > 0 || tx > 0 {
					hasMetrics = true
				}
				sample := conns[currentKey]
				if rx > sample.RxBytes {
					sample.RxBytes = rx
				}
				if tx > sample.TxBytes {
					sample.TxBytes = tx
				}
				conns[currentKey] = sample
			}
			continue
		}
		parts := strings.Fields(line)
		currentKey = ""
		if len(parts) < 5 {
			continue
		}
		m := reSSPort.FindStringSubmatch(parts[3])
		if len(m) != 2 {
			continue
		}
		p, err := strconv.Atoi(m[1])
		if err != nil || !portSet[p] {
			continue
		}
		currentKey = parts[3] + "|" + parts[4]
		counts[p]++
		rx, tx := parseSSMetricsLine(line)
		if rx > 0 || tx > 0 {
			hasMetrics = true
		}
		conns[currentKey] = ssConnSample{Port: p, RxBytes: rx, TxBytes: tx}
	}
	return conns, counts, hasMetrics
}

func computeRealtimeTraffic(first, second map[string]ssConnSample, counts map[int]int, ports []int, duration float64) []map[string]any {
	if duration <= 0 {
		duration = 0.5
	}
	type agg struct {
		port  int
		rxBps int64
		txBps int64
		conns int
	}
	byPort := make(map[int]*agg, len(ports))
	for _, p := range ports {
		byPort[p] = &agg{port: p, conns: counts[p]}
	}
	for key, cur := range second {
		old := first[key]
		rxDelta := cur.RxBytes - old.RxBytes
		if rxDelta < 0 {
			rxDelta = 0
		}
		txDelta := cur.TxBytes - old.TxBytes
		if txDelta < 0 {
			txDelta = 0
		}
		if entry, ok := byPort[cur.Port]; ok {
			entry.rxBps += int64(float64(rxDelta) / duration)
			entry.txBps += int64(float64(txDelta) / duration)
		}
	}
	rows := make([]map[string]any, 0, len(ports))
	for _, entry := range byPort {
		rows = append(rows, map[string]any{
			"port":                entry.port,
			"rx_total_bytes":      int64(0),
			"tx_total_bytes":      int64(0),
			"total_bytes":         int64(0),
			"rx_bytes_per_second": entry.rxBps,
			"tx_bytes_per_second": entry.txBps,
			"bytes_per_second":    entry.rxBps + entry.txBps,
			"connection_count":    entry.conns,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		bi := rows[i]["bytes_per_second"].(int64)
		bj := rows[j]["bytes_per_second"].(int64)
		if bi != bj {
			return bi > bj
		}
		return rows[i]["connection_count"].(int) > rows[j]["connection_count"].(int)
	})
	return rows
}

func parseNftTrafficJSON(raw string, portSet map[int]bool, ports []int) ([]map[string]any, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []map[string]any{}, false
	}
	var doc struct {
		Nftables []struct {
			Counter *struct {
				Name  string `json:"name"`
				Bytes int64  `json:"bytes"`
			} `json:"counter"`
		} `json:"nftables"`
	}
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return []map[string]any{}, false
	}
	type agg struct {
		port int
		rx   int64
		tx   int64
	}
	byPort := make(map[int]*agg, len(ports))
	for _, p := range ports {
		byPort[p] = &agg{port: p}
	}
	found := false
	for _, item := range doc.Nftables {
		if item.Counter == nil {
			continue
		}
		m := reNftCount.FindStringSubmatch(item.Counter.Name)
		if len(m) != 3 {
			continue
		}
		p, err := strconv.Atoi(m[1])
		if err != nil || !portSet[p] {
			continue
		}
		found = true
		if m[2] == "rx" {
			byPort[p].rx += item.Counter.Bytes
		} else {
			byPort[p].tx += item.Counter.Bytes
		}
	}
	if !found {
		return []map[string]any{}, false
	}
	rows := make([]map[string]any, 0, len(ports))
	for _, entry := range byPort {
		rows = append(rows, map[string]any{
			"port":                entry.port,
			"rx_total_bytes":      entry.rx,
			"tx_total_bytes":      entry.tx,
			"total_bytes":         entry.rx + entry.tx,
			"rx_bytes_per_second": int64(0),
			"tx_bytes_per_second": int64(0),
			"bytes_per_second":    int64(0),
			"connection_count":    0,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i]["total_bytes"].(int64) > rows[j]["total_bytes"].(int64)
	})
	return rows, true
}

type trafficNodeInfo struct {
	Port      int    `json:"port"`
	NodeName  string `json:"node_name"`
	GroupName string `json:"group_name"`
	Protocol  string `json:"protocol"`
}

func (s *Service) trafficNodeMap(alias string) (map[int]trafficNodeInfo, []int) {
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return map[int]trafficNodeInfo{}, []int{}
	}
	result := map[int]trafficNodeInfo{}
	for _, config := range configs {
		port := extractListenPort(config)
		if port < 1 || port > 65535 {
			continue
		}
		result[port] = trafficNodeInfo{
			Port:      port,
			NodeName:  extractNodeTag(config),
			GroupName: extractGroupName(config),
			Protocol:  extractInboundProtocol(config),
		}
	}
	ports := make([]int, 0, len(result))
	for port := range result {
		ports = append(ports, port)
	}
	sort.Ints(ports)
	return result, ports
}

func extractInboundProtocol(config map[string]any) string {
	if inbounds, ok := config["inbounds"].([]any); ok && len(inbounds) > 0 {
		if inbound, ok := inbounds[0].(map[string]any); ok {
			return asString(inbound["type"])
		}
	}
	return ""
}

func attachTrafficNodeInfo(data map[string]any, nodeMap map[int]trafficNodeInfo) {
	for _, key := range []string{"realtime", "total"} {
		switch rows := data[key].(type) {
		case []any:
			for _, raw := range rows {
				row, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				if info, ok := nodeMap[asInt(row["port"])]; ok {
					row["node_name"] = info.NodeName
					row["group_name"] = info.GroupName
					row["protocol"] = info.Protocol
				}
			}
		case []map[string]any:
			for _, row := range rows {
				if info, ok := nodeMap[asInt(row["port"])]; ok {
					row["node_name"] = info.NodeName
					row["group_name"] = info.GroupName
					row["protocol"] = info.Protocol
				}
			}
		}
	}
}

func trafficCollectScript() string {
	return `
import json, os, re, subprocess, sys, time
ports = set(int(p) for p in json.loads(sys.argv[1]) if 1 <= int(p) <= 65535)
def empty():
    return {p: {"port": p, "rx_total_bytes": 0, "tx_total_bytes": 0, "total_bytes": 0, "rx_bytes_per_second": 0, "tx_bytes_per_second": 0, "bytes_per_second": 0, "connection_count": 0} for p in ports}
def safe_int(v):
    try: return int(v)
    except Exception: return 0
def endpoint_port(v):
    m = re.search(r":(\d+)$", v or "")
    if not m: return None
    p = safe_int(m.group(1))
    return p if p in ports else None
def metrics(text):
    s = re.search(r"\bbytes_sent:(\d+)", text)
    a = re.search(r"\bbytes_acked:(\d+)", text)
    r = re.search(r"\bbytes_received:(\d+)", text)
    return (safe_int(r.group(1)) if r else 0, safe_int(s.group(1)) if s else (safe_int(a.group(1)) if a else 0))
def read_ss():
    conns, counts, has = {}, {p: 0 for p in ports}, False
    try:
        proc = subprocess.run(["ss", "-tinH"], capture_output=True, text=True, timeout=4)
    except Exception:
        return conns, counts, False
    current = None
    for line in proc.stdout.splitlines():
        if not line.strip(): continue
        if line[0].isspace():
            if current:
                rx, tx = metrics(line)
                has = has or bool(rx or tx)
                conns[current]["rx_bytes"] = max(conns[current].get("rx_bytes", 0), rx)
                conns[current]["tx_bytes"] = max(conns[current].get("tx_bytes", 0), tx)
            continue
        parts = line.split()
        current = None
        if len(parts) < 5: continue
        port = endpoint_port(parts[3])
        if port is None: continue
        current = parts[3] + "|" + parts[4]
        counts[port] += 1
        rx, tx = metrics(line)
        has = has or bool(rx or tx)
        conns[current] = {"port": port, "rx_bytes": rx, "tx_bytes": tx}
    return conns, counts, has
def realtime(first, second, counts, seconds):
    rows, duration = empty(), max(float(seconds), 0.001)
    for key, item in second.items():
        port = item["port"]; old = first.get(key) or {}
        rx = max(0, item.get("rx_bytes", 0) - old.get("rx_bytes", 0))
        tx = max(0, item.get("tx_bytes", 0) - old.get("tx_bytes", 0))
        rows[port]["rx_bytes_per_second"] += int(rx / duration)
        rows[port]["tx_bytes_per_second"] += int(tx / duration)
    for p, c in counts.items():
        rows[p]["connection_count"] = c
        rows[p]["bytes_per_second"] = rows[p]["rx_bytes_per_second"] + rows[p]["tx_bytes_per_second"]
    return sorted(rows.values(), key=lambda x: (x["bytes_per_second"], x["connection_count"]), reverse=True)
def read_nft():
    cmd = ["nft", "-j", "list", "table", "inet", "nsb_traffic"]
    try:
        proc = subprocess.run(cmd, capture_output=True, text=True, timeout=4)
    except Exception:
        return None
    if proc.returncode != 0:
        try:
            chk = subprocess.run(["sudo", "-n", "true"], capture_output=True, text=True, timeout=3)
            if chk.returncode == 0:
                proc = subprocess.run(["sudo", "-n"] + cmd, capture_output=True, text=True, timeout=4)
        except Exception:
            pass
    if proc.returncode != 0 or not proc.stdout.strip(): return None
    try: data = json.loads(proc.stdout)
    except Exception: return None
    rows, found, pat = empty(), False, re.compile(r"^nsb_p(\d+)_(rx|tx)$")
    for item in data.get("nftables", []):
        c = item.get("counter") if isinstance(item, dict) else None
        if not isinstance(c, dict): continue
        m = pat.match(c.get("name") or "")
        if not m: continue
        p, d = safe_int(m.group(1)), m.group(2)
        if p not in ports: continue
        found = True
        if d == "rx": rows[p]["rx_total_bytes"] += safe_int(c.get("bytes"))
        else: rows[p]["tx_total_bytes"] += safe_int(c.get("bytes"))
    if not found: return None
    for row in rows.values(): row["total_bytes"] = row["rx_total_bytes"] + row["tx_total_bytes"]
    return sorted(rows.values(), key=lambda x: x["total_bytes"], reverse=True)
sample = 0.5
first, _, has1 = read_ss()
time.sleep(sample)
second, counts, has2 = read_ss()
rt = realtime(first, second, counts, sample) if (has1 or has2 or counts) else []
total = read_nft()
print(json.dumps({"success": True, "source": "ss_tcp", "sample_seconds": sample, "realtime": rt, "total": total or [], "total_available": total is not None, "total_message": "" if total is not None else "端口总流量未启用或无权限读取 nftables 计数器。旧版需要先启用总流量统计后才会有累计数据。"}, ensure_ascii=False))
`
}

func (s *Service) UploadAllJSON(alias string) CommandResult {
	result := s.uploadAllJSON(alias, nil)
	if !result.Success {
		return result
	}
	if backup, err := s.BackupNow(alias); err != nil {
		result.Message = firstNonEmpty(result.Message, "配置已发布") + "；自动备份失败：" + err.Error()
	} else {
		result.Message = firstNonEmpty(result.Message, "配置已发布") + "；已自动备份：" + backup.Name
	}
	return result
}

func (s *Service) UploadAllJSONAndDeleteRemoteNodes(alias string, filenames []string) CommandResult {
	result := s.uploadAllJSON(alias, filenames)
	if !result.Success {
		return result
	}
	if backup, err := s.BackupNow(alias); err != nil {
		result.Message = firstNonEmpty(result.Message, "配置已发布") + "；自动备份失败：" + err.Error()
	} else {
		result.Message = firstNonEmpty(result.Message, "配置已发布") + "；已自动备份：" + backup.Name
	}
	return result
}

func (s *Service) detectVPSIPs(alias string) ([]string, error) {
	return s.detectVPSIPsWithOptions(alias, false)
}

func (s *Service) detectVPSIPsWithOptions(alias string, includeIPv6 bool) ([]string, error) {
	vps, err := s.getVPS(alias)
	if err != nil {
		return nil, err
	}
	client, release, err := s.acquireSSH(vps, 45)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败，无法检测服务器 IP：%w", err)
	}
	defer release()
	cmd := "ip -o -4 addr show scope global | awk '{split($4,a,\"/\"); print a[1]}'"
	if includeIPv6 {
		cmd = "ip -o addr show scope global | awk '{split($4,a,\"/\"); print a[1]}'"
	}
	result := executeSSH(client, s.wrapRootCommand(vps, cmd), 20)
	if !result.Success || strings.TrimSpace(result.Stdout) == "" {
		result = executeSSH(client, s.wrapRootCommand(vps, "hostname -I | tr ' ' '\\n'"), 20)
	}
	if !result.Success {
		return nil, errors.New(firstNonEmpty(result.Message, result.Stderr, "检测服务器 IP 失败"))
	}
	seen := map[string]bool{}
	ips := []string{}
	for _, line := range strings.Split(result.Stdout, "\n") {
		ip := strings.TrimSpace(line)
		parsed := net.ParseIP(ip)
		if parsed == nil || parsed.IsLoopback() || parsed.IsUnspecified() || parsed.IsLinkLocalUnicast() || parsed.IsLinkLocalMulticast() || seen[ip] {
			continue
		}
		if !includeIPv6 && parsed.To4() == nil {
			continue
		}
		seen[ip] = true
		ips = append(ips, ip)
	}
	sort.Strings(ips)
	return ips, nil
}

// CheckAllJSON 在远端临时目录使用 sing-box check 预检配置，不覆盖现有 all.json 也不重启服务。
func (s *Service) CheckAllJSON(alias string, text string) CommandResult {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return CommandResult{Success: false, ExitCode: -1, Message: "VPS 不能为空"}
	}
	configs, err := parseNodeConfigText(text)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "JSON 解析失败: " + err.Error()}
	}
	if err := validateNodeConfigMap(configs); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "本地配置校验失败: " + err.Error()}
	}
	allJSON := buildAllJSON(configs)
	vps, err := s.getVPS(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	client, release, err := s.acquireSSH(vps, 45)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "SSH 连接失败，无法执行远端预检: " + err.Error()}
	}
	defer release()
	checkScript := strings.Join([]string{
		`set -e`,
		`tmp_dir=$(mktemp -d /tmp/santianshell-check.XXXXXX)`,
		`trap 'rm -rf "$tmp_dir"' EXIT`,
		`cat > "$tmp_dir/all.json"`,
		`sb_bin="/etc/sing-box/bin/sing-box"; [ -x "$sb_bin" ] || sb_bin=$(command -v sing-box 2>/dev/null || true)`,
		`if [ -z "$sb_bin" ] || [ ! -x "$sb_bin" ]; then echo '远端未找到 sing-box 核心，已完成本地语法校验'; exit 0; fi`,
		`if [ -f /etc/sing-box/config.json ]; then "$sb_bin" check -c /etc/sing-box/config.json -C "$tmp_dir"; else "$sb_bin" check -c "$tmp_dir/all.json"; fi`,
	}, "\n")
	res := executeSSHWithInput(client, s.wrapRootCommand(vps, checkScript), allJSON, 45)
	if res.Success {
		res.Message = fmt.Sprintf("远端 sing-box check 预检通过（共 %d 个节点），尚未覆盖或重启服务", len(configs))
	} else {
		res.Message = "远端 sing-box check 预检未通过：" + firstNonEmpty(strings.TrimSpace(res.Stderr), strings.TrimSpace(res.Stdout), res.Message)
	}
	return res
}

func (s *Service) connectSSHWithRetry(vps VPS, timeoutSeconds int, attempts int) (*ssh.Client, error) {
	if attempts <= 1 {
		return s.connectSSHOnce(vps, timeoutSeconds)
	}
	var lastErr error
	for i := 0; i < attempts; i++ {
		client, err := s.connectSSHOnce(vps, timeoutSeconds)
		if err == nil {
			return client, nil
		}
		lastErr = err
		if !isRetryableSSHConnectError(err) || i == attempts-1 {
			break
		}
		time.Sleep(time.Duration(300+i*400) * time.Millisecond)
	}
	return nil, lastErr
}

func isRetryableSSHConnectError(err error) bool {
	if err == nil {
		return false
	}
	if isSSHAuthError(err) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "forcibly closed") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "timeout") ||
		strings.Contains(text, "eof") ||
		strings.Contains(text, "handshake failed")
}

func isSSHAuthError(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "unable to authenticate") ||
		strings.Contains(text, "permission denied") ||
		strings.Contains(text, "no supported methods remain") ||
		strings.Contains(text, "too many authentication failures")
}

func (s *Service) uploadAllJSON(alias string, deleteFilenames []string) CommandResult {
	totalStart := time.Now()
	vps, err := s.getVPS(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	buildStart := time.Now()
	configs, err := s.loadNodeConfigMap(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	allJSON := buildAllJSON(configs)
	buildCost := time.Since(buildStart)
	connectStart := time.Now()
	client, release, err := s.acquireSSH(vps, 60)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	defer release()
	connectCost := time.Since(connectStart)
	uploadStart := time.Now()
	if len(deleteFilenames) > 0 {
		if result := s.deleteRemoteNodeFiles(client, vps, deleteFilenames); !result.Success {
			return result
		}
	}
	if result := s.cleanupRemoteJSONFiles(client, vps); !result.Success {
		result.Message = "清理服务器遗留节点配置失败: " + firstNonEmpty(result.Message, result.Stderr)
		result.Summary = uploadTimingSummary(buildCost, connectCost, time.Since(uploadStart), 0, time.Since(totalStart))
		return result
	}
	if result := s.uploadAllJSONByStream(client, vps, allJSON); !result.Success {
		result.Summary = uploadTimingSummary(buildCost, connectCost, time.Since(uploadStart), 0, time.Since(totalStart))
		return result
	}
	uploadCost := time.Since(uploadStart)
	restartStart := time.Now()
	restartCmd := buildSingboxReloadCommand()
	result := executeSSH(client, s.wrapRootCommand(vps, restartCmd), 120)
	if result.Success {
		result.Message = "配置已发布到 VPS 并重启 sing-box"
	} else {
		result.Message = firstNonEmpty(result.Message, "远端配置检查或重启失败")
	}
	result.Summary = uploadTimingSummary(buildCost, connectCost, uploadCost, time.Since(restartStart), time.Since(totalStart))
	return result
}

func buildSingboxReloadCommand() string {
	return "if command -v systemctl >/dev/null 2>&1; then " +
		"mkdir -p /etc/systemd/system/sing-box.service.d && " +
		"cat > /etc/systemd/system/sing-box.service.d/10-santianshell-exec.conf <<'EOF'\n[Service]\nEnvironment=ENABLE_DEPRECATED_MISSING_DOMAIN_RESOLVER=true\nExecStart=\nExecStart=/etc/sing-box/bin/sing-box run -c /etc/sing-box/config.json -C /etc/sing-box/conf\nEOF\n" +
		"systemctl daemon-reload >/dev/null 2>&1 || true; fi && " +
		"if [ ! -f /etc/sing-box/config.json ]; then mkdir -p /etc/sing-box && echo '{\"log\":{\"disabled\":false,\"level\":\"warn\",\"output\":\"/var/log/sing-box/sing-box.log\",\"timestamp\":true},\"dns\":{\"servers\":[{\"type\":\"local\",\"tag\":\"dns-local\",\"prefer_go\":true}]},\"route\":{\"default_domain_resolver\":\"dns-local\"}}' > /etc/sing-box/config.json; fi && " +
		"if command -v jq >/dev/null 2>&1; then " +
		"if [ -f /etc/sing-box/config.json ]; then " +
		"cfg_tmp=$(mktemp /tmp/sb_cfg.XXXXXX) && " +
		"jq 'if (.dns == null or .dns == {} or (.dns.servers | length == 0)) then .dns = {\"servers\": [{\"type\": \"local\", \"tag\": \"dns-local\", \"prefer_go\": true}]} else . end | .dns.servers |= unique_by(.tag) | (if (.dns.servers | length > 0) then .dns.servers[0].tag else \"dns-local\" end) as $dns_tag | if .route == null then .route = {\"default_domain_resolver\": $dns_tag} elif .route.default_domain_resolver == null then .route.default_domain_resolver = $dns_tag else . end' /etc/sing-box/config.json > \"$cfg_tmp\" 2>/dev/null && mv -f \"$cfg_tmp\" /etc/sing-box/config.json || rm -f \"$cfg_tmp\"; fi && " +
		"if [ -f /etc/sing-box/conf/all.json ]; then " +
		"all_tmp=$(mktemp /tmp/sb_all.XXXXXX) && " +
		"jq 'del(.dns) | if .route then del(.route.default_domain_resolver) else . end' /etc/sing-box/conf/all.json > \"$all_tmp\" 2>/dev/null && mv -f \"$all_tmp\" /etc/sing-box/conf/all.json || rm -f \"$all_tmp\"; fi; fi && " +
		"sb_bin=\"/etc/sing-box/bin/sing-box\"; [ -x \"$sb_bin\" ] || sb_bin=$(command -v sing-box 2>/dev/null || true); " +
		"if [ -n \"$sb_bin\" ] && [ -x \"$sb_bin\" ]; then " +
		"if \"$sb_bin\" check -c /etc/sing-box/config.json -C /etc/sing-box/conf; then " +
		"rm -f /etc/sing-box/conf/all.json.rollback; " +
		"else check_status=$?; " +
		"echo \"sing-box 配置预检失败 (退出码 $check_status)，正在自动回滚原有配置...\" >&2; " +
		"if [ -f /etc/sing-box/conf/all.json.rollback ]; then " +
		"cp -f /etc/sing-box/conf/all.json.rollback /etc/sing-box/conf/all.json; " +
		"if command -v jq >/dev/null 2>&1; then all_rb_tmp=$(mktemp /tmp/sb_rb.XXXXXX) && jq 'del(.dns) | if .route then del(.route.default_domain_resolver) else . end' /etc/sing-box/conf/all.json > \"$all_rb_tmp\" 2>/dev/null && mv -f \"$all_rb_tmp\" /etc/sing-box/conf/all.json || rm -f \"$all_rb_tmp\"; fi; " +
		"chmod 644 /etc/sing-box/conf/all.json 2>/dev/null || true; " +
		"rm -f /etc/sing-box/conf/all.json.rollback; " +
		"echo \"已自动恢复上一版有效配置，服务未中断\" >&2; " +
		"fi; exit $check_status; fi; fi && " +
		"if command -v iptables >/dev/null 2>&1; then " +
		"iptables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || iptables -I INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || true; " +
		"iptables -C INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || iptables -I INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || true; fi && " +
		"if command -v ip6tables >/dev/null 2>&1; then " +
		"ip6tables -C INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || ip6tables -I INPUT -p tcp --dport 10000:65000 -j ACCEPT 2>/dev/null || true; " +
		"ip6tables -C INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || ip6tables -I INPUT -p udp --dport 10000:65000 -j ACCEPT 2>/dev/null || true; fi && " +
		"if command -v ufw >/dev/null 2>&1 && ufw status 2>/dev/null | grep -qw \"active\"; then " +
		"ufw allow 10000:65000/tcp 2>/dev/null || true; ufw allow 10000:65000/udp 2>/dev/null || true; fi && " +
		"if command -v firewall-cmd >/dev/null 2>&1 && firewall-cmd --state >/dev/null 2>&1; then " +
		"firewall-cmd --permanent --add-port=10000-65000/tcp >/dev/null 2>&1 || true; firewall-cmd --permanent --add-port=10000-65000/udp >/dev/null 2>&1 || true; firewall-cmd --reload >/dev/null 2>&1 || true; fi && " +
		remoteSBCommand("restart")
}

func (s *Service) cleanupRemoteJSONFiles(client *ssh.Client, vps VPS) CommandResult {
	command := `find /etc/sing-box/conf -type f -name '*.json' ! -path '/etc/sing-box/conf/all.json' -delete`
	return executeSSH(client, s.wrapRootCommand(vps, command), 30)
}

func cleanupRemoteJSONFiles(client *ssh.Client) CommandResult {
	return (&Service{}).cleanupRemoteJSONFiles(client, VPS{})
}

func uploadAllJSONByStreamCommand(password string, isNonRoot bool) string {
	if !isNonRoot {
		return "mkdir -p /etc/sing-box/conf && ([ ! -s /etc/sing-box/conf/all.json ] || cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true) && cat > /etc/sing-box/conf/all.json.tmp && mv -f /etc/sing-box/conf/all.json.tmp /etc/sing-box/conf/all.json"
	}
	passQuoted := shellQuote(password)
	hasPassword := strings.TrimSpace(password) != ""
	var b strings.Builder
	b.WriteString("set -e\n")
	b.WriteString("tmp_file=$(mktemp /tmp/santianshell-all.XXXXXX)\n")
	b.WriteString("trap 'rm -f \"$tmp_file\"' EXIT\n")
	b.WriteString("cat > \"$tmp_file\"\n")
	b.WriteString("if [ \"$(id -u)\" -eq 0 ]; then\n")
	b.WriteString("  mkdir -p /etc/sing-box/conf && ([ ! -s /etc/sing-box/conf/all.json ] || cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true)\n")
	b.WriteString("  cp -f \"$tmp_file\" /etc/sing-box/conf/all.json && chmod 644 /etc/sing-box/conf/all.json\n")
	b.WriteString("elif command -v sudo >/dev/null 2>&1; then\n")
	b.WriteString("  if sudo -n true 2>/dev/null; then\n")
	b.WriteString("    sudo -n mkdir -p /etc/sing-box/conf\n")
	b.WriteString("    [ ! -s /etc/sing-box/conf/all.json ] || sudo -n cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true\n")
	b.WriteString("    sudo -n cp -f \"$tmp_file\" /etc/sing-box/conf/all.json && sudo -n chmod 644 /etc/sing-box/conf/all.json\n")
	if hasPassword {
		b.WriteString(fmt.Sprintf("  else\n    printf '%%s\\n' %s | sudo -S -p '' -- sh -c 'mkdir -p /etc/sing-box/conf && ([ ! -s /etc/sing-box/conf/all.json ] || cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true); cp -f \"$1\" /etc/sing-box/conf/all.json && chmod 644 /etc/sing-box/conf/all.json' -- \"$tmp_file\"\n", passQuoted))
	} else {
		b.WriteString("  else\n    sudo mkdir -p /etc/sing-box/conf\n    [ ! -s /etc/sing-box/conf/all.json ] || sudo cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true\n    sudo cp -f \"$tmp_file\" /etc/sing-box/conf/all.json && sudo chmod 644 /etc/sing-box/conf/all.json\n")
	}
	b.WriteString("  fi\n")
	b.WriteString("elif command -v su >/dev/null 2>&1; then\n")
	if hasPassword {
		b.WriteString(fmt.Sprintf("  printf '%%s\\n' %s | su - -c \"mkdir -p /etc/sing-box/conf && ([ ! -s /etc/sing-box/conf/all.json ] || cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true); cp -f \\\"$tmp_file\\\" /etc/sing-box/conf/all.json && chmod 644 /etc/sing-box/conf/all.json\"\n", passQuoted))
	} else {
		b.WriteString("  su - -c \"mkdir -p /etc/sing-box/conf && ([ ! -s /etc/sing-box/conf/all.json ] || cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true); cp -f \\\"$tmp_file\\\" /etc/sing-box/conf/all.json && chmod 644 /etc/sing-box/conf/all.json\"\n")
	}
	b.WriteString("else\n")
	b.WriteString("  mkdir -p /etc/sing-box/conf && ([ ! -s /etc/sing-box/conf/all.json ] || cp -f /etc/sing-box/conf/all.json /etc/sing-box/conf/all.json.rollback 2>/dev/null || true)\n")
	b.WriteString("  cp -f \"$tmp_file\" /etc/sing-box/conf/all.json\n")
	b.WriteString("fi")
	return b.String()
}

func (s *Service) uploadAllJSONByStream(client *ssh.Client, vps VPS, content []byte) CommandResult {
	password := s.resolvePassword(vps)
	return uploadAllJSONByStreamWithVPS(client, vps, content, password)
}

func uploadAllJSONByStream(client *ssh.Client, content []byte) CommandResult {
	return uploadAllJSONByStreamWithVPS(client, VPS{}, content, "")
}

func uploadAllJSONByStreamWithVPS(client *ssh.Client, vps VPS, content []byte, password string) CommandResult {
	session, err := client.NewSession()
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	defer session.Close()

	stdin, err := session.StdinPipe()
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}

	var stdout, stderr commandBuffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	command := uploadAllJSONByStreamCommand(password, isNonRootUser(vps.Username))
	if err := session.Start(command); err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}

	copyDone := make(chan error, 1)
	go func() {
		_, copyErr := io.Copy(stdin, bytes.NewReader(content))
		if closeErr := stdin.Close(); copyErr == nil {
			copyErr = closeErr
		}
		copyDone <- copyErr
	}()

	var copyErr error
	select {
	case copyErr = <-copyDone:
	case <-time.After(90 * time.Second):
		_ = session.Signal(ssh.SIGKILL)
		return CommandResult{Success: false, Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1, Message: "上传 all.json 超时"}
	}

	waitDone := make(chan error, 1)
	go func() { waitDone <- session.Wait() }()

	select {
	case err = <-waitDone:
		exitCode := 0
		if err != nil {
			exitCode = -1
			var exitErr *ssh.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitStatus()
			}
		}
		if copyErr != nil && err == nil {
			err = copyErr
			exitCode = -1
		}
		result := CommandResult{
			Success:  err == nil,
			Stdout:   stdout.String(),
			Stderr:   stderr.String(),
			ExitCode: exitCode,
			Message:  firstNonEmpty(stderr.String(), stdout.String(), fmt.Sprint(err)),
		}
		if !result.Success {
			result.Message = firstNonEmpty(result.Message, "上传 all.json 失败")
		}
		return result
	case <-time.After(90 * time.Second):
		_ = session.Signal(ssh.SIGKILL)
		return CommandResult{Success: false, Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1, Message: "上传 all.json 超时"}
	}
}

func uploadTimingSummary(buildCost time.Duration, connectCost time.Duration, uploadCost time.Duration, restartCost time.Duration, totalCost time.Duration) string {
	return fmt.Sprintf("耗时：本地构建 %.2fs，SSH连接 %.2fs，上传 %.2fs，检查重启 %.2fs，总计 %.2fs",
		buildCost.Seconds(), connectCost.Seconds(), uploadCost.Seconds(), restartCost.Seconds(), totalCost.Seconds())
}

func (s *Service) deleteRemoteNodeFiles(client *ssh.Client, vps VPS, filenames []string) CommandResult {
	parts := []string{}
	seen := map[string]bool{}
	for _, filename := range filenames {
		base := filepath.Base(strings.TrimSpace(filename))
		if base == "." || base == "/" || base == "" || seen[base] {
			continue
		}
		seen[base] = true
		for _, dir := range []string{"/etc/sing-box/config/nodes", "/etc/sing-box/conf/nodes", "/etc/sing-box/conf", "/etc/sing-box/config"} {
			parts = append(parts, shellQuote(filepath.ToSlash(filepath.Join(dir, base))))
		}
	}
	if len(parts) == 0 {
		return CommandResult{Success: true}
	}
	result := executeSSH(client, s.wrapRootCommand(vps, "rm -f "+strings.Join(parts, " ")), 60)
	if result.Success {
		result.Message = "远端节点文件已删除"
	}
	return result
}

func deleteRemoteNodeFiles(client *ssh.Client, filenames []string) CommandResult {
	return (&Service{}).deleteRemoteNodeFiles(client, VPS{}, filenames)
}

var remoteSingBoxConfigPaths = []string{
	"/etc/sing-box/conf/all.json",
	"/etc/sing-box/config.json",
}

func readManagedRemoteNodeConfigs(readFile func(string) ([]byte, error)) (map[string]map[string]any, string, error) {
	remotePath := remoteSingBoxConfigPaths[0]
	content, err := readFile(remotePath)
	if err != nil {
		return nil, "", fmt.Errorf("未找到 SanTianShell 节点配置 %s：%w", remotePath, err)
	}
	if len(bytes.TrimSpace(content)) == 0 {
		return map[string]map[string]any{}, remotePath, nil
	}
	configs, err := splitAllJSON(content)
	if err != nil {
		return nil, "", fmt.Errorf("SanTianShell 节点配置解析失败：%w", err)
	}
	if len(configs) == 0 {
		return configs, remotePath, nil
	}
	if err := validateNodeConfigMap(configs); err != nil {
		return nil, "", fmt.Errorf("SanTianShell 节点配置无效：%w", err)
	}
	return configs, remotePath, nil
}

func readRemoteNodeConfigs(readFile func(string) ([]byte, error)) (map[string]map[string]any, string, error) {
	errorsByPath := make([]string, 0, len(remoteSingBoxConfigPaths))
	for _, remotePath := range remoteSingBoxConfigPaths {
		content, err := readFile(remotePath)
		if err != nil {
			errorsByPath = append(errorsByPath, fmt.Sprintf("%s：%v", remotePath, err))
			continue
		}
		configs, err := splitAllJSON(content)
		if err != nil {
			errorsByPath = append(errorsByPath, fmt.Sprintf("%s：JSON 解析失败：%v", remotePath, err))
			continue
		}
		if len(configs) == 0 {
			errorsByPath = append(errorsByPath, remotePath+"：未解析到节点")
			continue
		}
		if err := validateNodeConfigMap(configs); err != nil {
			errorsByPath = append(errorsByPath, fmt.Sprintf("%s：节点配置无效：%v", remotePath, err))
			continue
		}
		return configs, remotePath, nil
	}
	return nil, "", errors.New("未找到可同步的 sing-box 节点配置；" + strings.Join(errorsByPath, "；"))
}

func (s *Service) SyncAllJSON(alias string) CommandResult {
	vps, err := s.getVPS(alias)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	canonicalAlias := firstNonEmpty(strings.TrimSpace(vps.Alias), strings.TrimSpace(vps.ID))
	// 同步覆盖本地前自动快照备份本地已有节点，防止误覆盖
	backupNote := ""
	if localConfigs, lErr := s.loadNodeConfigMap(canonicalAlias); lErr == nil && len(localConfigs) > 0 {
		if bk, bErr := s.BackupNow(canonicalAlias); bErr == nil && bk.Name != "" {
			backupNote = fmt.Sprintf("（同步前已自动备份本地配置：%s）", bk.Name)
		}
	}
	client, release, err := s.acquireSSH(vps, 60)
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	defer release()

	checkCmd := s.wrapRootCommand(vps, "if [ -f /etc/sing-box/conf/all.json ]; then cat /etc/sing-box/conf/all.json; else echo '__SAN_TIAN_SHELL_NOT_FOUND__'; fi")
	checkRes := executeSSH(client, checkCmd, 30)
	if !checkRes.Success {
		configs, remotePath, err := readManagedRemoteNodeConfigs(func(path string) ([]byte, error) {
			return s.downloadRemoteConfig(client, vps, path)
		})
		if err != nil {
			return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
		}
		if len(configs) == 0 {
			deletedCount, _ := s.clearLocalNodesForVPS(canonicalAlias)
			msg := "同步完成，远端无节点配置" + backupNote
			if deletedCount > 0 {
				msg = fmt.Sprintf("同步完成：远端无节点配置，本地已同步删除 %d 个节点%s", deletedCount, backupNote)
			}
			return CommandResult{Success: true, Stdout: msg, Message: msg}
		}
		deletedCount, _ := s.reconcileLocalNodesWithRemote(canonicalAlias, configs)
		msg := fmt.Sprintf("同步完成，节点数：%d，来源：%s%s", len(configs), remotePath, backupNote)
		if deletedCount > 0 {
			msg = fmt.Sprintf("同步完成，节点数：%d（已清理本地不存在节点 %d 个），来源：%s%s", len(configs), deletedCount, remotePath, backupNote)
		}
		return CommandResult{Success: true, Stdout: msg, Message: msg}
	}

	stdout := strings.TrimSpace(checkRes.Stdout)
	if strings.Contains(stdout, "__SAN_TIAN_SHELL_NOT_FOUND__") {
		deletedCount, _ := s.clearLocalNodesForVPS(canonicalAlias)
		msg := "同步完成：经核验服务器无节点配置" + backupNote
		if deletedCount > 0 {
			msg = fmt.Sprintf("同步完成：经核验服务器无节点配置，本地已同步删除 %d 个节点%s", deletedCount, backupNote)
		}
		return CommandResult{Success: true, Stdout: msg, Message: msg}
	}

	if len(stdout) == 0 {
		deletedCount, _ := s.clearLocalNodesForVPS(canonicalAlias)
		msg := "同步完成：服务器配置文件为空" + backupNote
		if deletedCount > 0 {
			msg = fmt.Sprintf("同步完成：服务器配置文件为空，本地已同步删除 %d 个节点%s", deletedCount, backupNote)
		}
		return CommandResult{Success: true, Stdout: msg, Message: msg}
	}

	remoteConfigs, err := splitAllJSON([]byte(stdout))
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: "远端配置解析失败（已保留本地数据）: " + err.Error()}
	}

	if len(remoteConfigs) == 0 {
		deletedCount, _ := s.clearLocalNodesForVPS(canonicalAlias)
		msg := "同步完成：经核验服务器无节点配置" + backupNote
		if deletedCount > 0 {
			msg = fmt.Sprintf("同步完成：经核验服务器无节点配置，本地已同步删除 %d 个节点%s", deletedCount, backupNote)
		}
		return CommandResult{Success: true, Stdout: msg, Message: msg}
	}

	deletedCount, _ := s.reconcileLocalNodesWithRemote(canonicalAlias, remoteConfigs)
	msg := fmt.Sprintf("同步完成，节点数：%d%s", len(remoteConfigs), backupNote)
	if deletedCount > 0 {
		msg = fmt.Sprintf("同步完成，节点数：%d（已清理本地不存在节点 %d 个）%s", len(remoteConfigs), deletedCount, backupNote)
	}
	return CommandResult{Success: true, Stdout: msg, Message: msg}
}

type proxyDialConfig struct {
	Host     string
	Port     int
	Username string
	Password string
}

func (s *Service) connectSSH(vps VPS, timeoutSeconds int) (*ssh.Client, error) {
	return s.connectSSHWithRetry(vps, timeoutSeconds, 3)
}

func (s *Service) connectSSHOnce(vps VPS, timeoutSeconds int) (*ssh.Client, error) {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 30
	}
	authMethods := []ssh.AuthMethod{}
	if vps.Password != "" {
		authMethods = append(authMethods, ssh.Password(vps.Password))
	}
	if vps.KeyPath != "" {
		key, err := os.ReadFile(vps.KeyPath)
		if err == nil {
			signer, err := ssh.ParsePrivateKey(key)
			if err == nil {
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			}
		}
	}
	if len(authMethods) == 0 {
		return nil, errors.New("未配置 SSH 密码或密钥")
	}
	config := &ssh.ClientConfig{
		User: vps.Username,
		Auth: authMethods,
		HostKeyCallback: func(hostname string, _ net.Addr, _ ssh.PublicKey) error {
			return fmt.Errorf("拒绝未经验证的 SSH 主机密钥 (%s): 请通过主界面主机管理确认并保存主机指纹", hostname)
		},
		Timeout: time.Duration(timeoutSeconds) * time.Second,
	}
	address := net.JoinHostPort(vps.Address, strconv.Itoa(vps.Port))
	dialConfig, err := vps.proxyDialConfig()
	if err != nil {
		return nil, err
	}
	usingSystemProxy := false
	if dialConfig.Host == "" || dialConfig.Port <= 0 {
		if sysConfig, sysErr := systemLocalProxyDialConfig(); sysErr == nil {
			dialConfig = sysConfig
			usingSystemProxy = dialConfig.Host != "" && dialConfig.Port > 0
		}
	}
	if dialConfig.Host == "" || dialConfig.Port <= 0 {
		return dialSSHDirect(address, config)
	}
	client, err := dialSSHWithSocks(address, config, dialConfig)
	if err != nil && usingSystemProxy && isSystemProxyFallbackError(err) {
		fallbackClient, fallbackErr := dialSSHDirect(address, config)
		if fallbackErr == nil {
			return fallbackClient, nil
		}
		return nil, fmt.Errorf("本地系统代理连接失败: %v；直连也失败: %w", err, fallbackErr)
	}
	return client, err
}

func dialSSHDirect(address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	dialer := sshTCPDialer(config.Timeout)
	conn, err := dialer.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	return newSSHClientFromConn(conn, address, config)
}

func dialSSHWithSocks(address string, config *ssh.ClientConfig, dialConfig proxyDialConfig) (*ssh.Client, error) {
	dialer, err := socks5Dialer(dialConfig, config.Timeout)
	if err != nil {
		return nil, err
	}
	conn, err := dialer.Dial("tcp", address)
	if err != nil {
		return nil, err
	}
	client, err := newSSHClientFromConn(conn, address, config)
	if err != nil {
		return nil, explainSocksSSHHandshakeError(address, err)
	}
	return client, nil
}

func explainSocksSSHHandshakeError(address string, err error) error {
	if err == nil {
		return nil
	}
	message := strings.ToLower(err.Error())
	if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) && !strings.Contains(message, "handshake failed: eof") {
		return err
	}
	return fmt.Errorf("SOCKS5 隧道已建立到 %s，但代理出口在发送 SSH 服务标识前关闭连接（EOF）；SanTianShell 未进行直连回退，请切换代理节点或检查该出口到目标 SSH 端口的限制；原始错误: %w", address, err)
}

func newSSHClientFromConn(conn net.Conn, address string, config *ssh.ClientConfig) (*ssh.Client, error) {
	configureTCPConn(conn)
	if config.Timeout > 0 {
		_ = conn.SetDeadline(time.Now().Add(config.Timeout))
	}
	clientConn, chans, reqs, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	client := ssh.NewClient(clientConn, chans, reqs)
	startSSHKeepAlive(client)
	return client, nil
}

func sshTCPDialer(timeout time.Duration) *net.Dialer {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &net.Dialer{
		Timeout:   timeout,
		KeepAlive: 30 * time.Second,
	}
}

func configureTCPConn(conn net.Conn) {
	tcpConn, ok := conn.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tcpConn.SetNoDelay(true)
	_ = tcpConn.SetKeepAlive(true)
	_ = tcpConn.SetKeepAlivePeriod(30 * time.Second)
}

func startSSHKeepAlive(client *ssh.Client) {
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		closed := make(chan struct{})
		go func() {
			_ = client.Wait()
			close(closed)
		}()
		monitorSSHKeepAlive(ticker.C, closed, func() error {
			done := make(chan error, 1)
			go func() {
				_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
				done <- err
			}()
			timer := time.NewTimer(5 * time.Second)
			defer timer.Stop()
			select {
			case err := <-done:
				return err
			case <-timer.C:
				return errors.New("SSH keep-alive 超时")
			}
		}, func() { _ = client.Close() })
	}()
}

func monitorSSHKeepAlive(ticks <-chan time.Time, closed <-chan struct{}, send func() error, closeClient func()) {
	for {
		select {
		case <-closed:
			return
		case _, ok := <-ticks:
			if !ok {
				return
			}
		}
		if err := send(); err != nil {
			closeClient()
			return
		}
	}
}

func isSystemProxyFallbackError(err error) bool {
	if err == nil {
		return false
	}
	if isSSHAuthError(err) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "handshake failed") ||
		strings.Contains(text, "eof") ||
		strings.Contains(text, "forcibly closed") ||
		strings.Contains(text, "connection reset") ||
		strings.Contains(text, "connection refused") ||
		strings.Contains(text, "i/o timeout") ||
		strings.Contains(text, "socks connect") ||
		strings.Contains(text, "unexpected protocol")
}

func (vps VPS) proxyDialConfig() (proxyDialConfig, error) {
	host := strings.TrimSpace(vps.ProxyHost)
	if host == "" {
		return proxyDialConfig{}, nil
	}
	port := asInt(vps.ProxyPort)
	if port <= 0 {
		return proxyDialConfig{}, errors.New("VPS 代理端口无效")
	}
	return proxyDialConfig{Host: host, Port: port, Username: vps.ProxyUsername, Password: vps.ProxyPassword}, nil
}

func parseHostPortUserPass(value string) (proxyDialConfig, error) {
	parts := strings.Split(strings.TrimSpace(value), ":")
	if len(parts) < 2 {
		return proxyDialConfig{}, errors.New("代理格式错误，应为 IP:端口:用户名:密码")
	}
	port, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || port <= 0 {
		return proxyDialConfig{}, errors.New("代理端口必须是数字")
	}
	config := proxyDialConfig{Host: strings.TrimSpace(parts[0]), Port: port}
	if len(parts) >= 3 {
		config.Username = strings.TrimSpace(parts[2])
	}
	if len(parts) >= 4 {
		config.Password = strings.TrimSpace(parts[3])
	}
	if config.Host == "" {
		return proxyDialConfig{}, errors.New("代理地址不能为空")
	}
	return config, nil
}

func socks5Dialer(config proxyDialConfig, timeout time.Duration) (proxy.Dialer, error) {
	return socks5DialerWithForward(config, sshTCPDialer(timeout))
}

func socks5DialerWithForward(config proxyDialConfig, forward proxy.Dialer) (proxy.Dialer, error) {
	address := net.JoinHostPort(config.Host, strconv.Itoa(config.Port))
	var auth *proxy.Auth
	if config.Username != "" || config.Password != "" {
		auth = &proxy.Auth{User: config.Username, Password: config.Password}
	}
	return proxy.SOCKS5("tcp", address, auth, forward)
}

func executeSSH(client *ssh.Client, command string, timeoutSeconds int) CommandResult {
	return executeSSHWithInput(client, command, nil, timeoutSeconds)
}

func executeSSHWithInput(client *ssh.Client, command string, input []byte, timeoutSeconds int) CommandResult {
	if timeoutSeconds <= 0 {
		timeoutSeconds = 60
	}
	session, err := client.NewSession()
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error()}
	}
	defer session.Close()

	var stdout, stderr commandBuffer
	if len(input) > 0 {
		session.Stdin = bytes.NewReader(input)
	}
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	timer := time.NewTimer(time.Duration(timeoutSeconds) * time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		exitCode := 0
		if err != nil {
			exitCode = -1
			var exitErr *ssh.ExitError
			if errors.As(err, &exitErr) {
				exitCode = exitErr.ExitStatus()
			}
		}
		stdoutText, stderrText := stdout.Text(), stderr.Text()
		return CommandResult{
			Success:  err == nil,
			Stdout:   stdoutText,
			Stderr:   stderrText,
			ExitCode: exitCode,
			Message:  firstNonEmpty(stderrText, stdoutText, fmt.Sprint(err)),
		}
	case <-timer.C:
		_ = session.Close()
		waitSSHCommandDone(done, time.Second)
		return CommandResult{Success: false, Stdout: stdout.Text(), Stderr: stderr.Text(), ExitCode: -1, Message: "命令超时"}
	}
}

func waitSSHCommandDone(done <-chan error, timeout time.Duration) bool {
	if timeout <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}

func localIperf3Path() (string, error) {
	executable, _ := os.Executable()
	return resolveLocalIperf3Path(executable, exec.LookPath)
}

func resolveLocalIperf3Path(executable string, lookPath func(string) (string, error)) (string, error) {
	if strings.TrimSpace(executable) != "" {
		bundled := filepath.Join(filepath.Dir(executable), "tools", "iperf3", "iperf3.exe")
		if info, err := os.Stat(bundled); err == nil && !info.IsDir() {
			return bundled, nil
		}
	}
	for _, name := range []string{"iperf3", "iperf3.exe"} {
		if path, err := lookPath(name); err == nil {
			return path, nil
		}
	}
	return "", errors.New("未找到 IPerf3，请重新解压完整发布包")
}

func executeRemoteIperf3Pair(client *ssh.Client, host string, port string) CommandResult {
	base := "LANG=C iperf3 -c " + shellQuote(host) + " -p " + shellQuote(port) + " -P 8 -t 10 -f m --connect-timeout 5000"
	forward := executeSSH(client, base, 40)
	reverse := executeSSH(client, base+" -R", 40)
	return mergeIperf3PairResult(forward, reverse)
}

func executeLocalIperf3Pair(host string, port string) CommandResult {
	forward := executeLocalIperf3(host, port, false)
	reverse := executeLocalIperf3(host, port, true)
	return mergeIperf3PairResult(forward, reverse)
}

func startLocalTCPForward(dial func(string, string) (net.Conn, error), remoteAddress string) (string, string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", "", nil, err
	}

	var closeOnce sync.Once
	var activeMu sync.Mutex
	stopped := false
	active := make(map[net.Conn]struct{})
	track := func(conn net.Conn) bool {
		activeMu.Lock()
		defer activeMu.Unlock()
		if stopped {
			_ = conn.Close()
			return false
		}
		active[conn] = struct{}{}
		return true
	}
	untrack := func(conn net.Conn) {
		activeMu.Lock()
		delete(active, conn)
		activeMu.Unlock()
	}
	stop := func() {
		closeOnce.Do(func() {
			_ = listener.Close()
			activeMu.Lock()
			stopped = true
			for conn := range active {
				_ = conn.Close()
			}
			activeMu.Unlock()
		})
	}

	go func() {
		for {
			localConn, acceptErr := listener.Accept()
			if acceptErr != nil {
				return
			}
			if !track(localConn) {
				return
			}
			go func() {
				defer func() {
					untrack(localConn)
					_ = localConn.Close()
				}()
				remoteConn, dialErr := dial("tcp", remoteAddress)
				if dialErr != nil {
					return
				}
				if !track(remoteConn) {
					return
				}
				defer func() {
					untrack(remoteConn)
					_ = remoteConn.Close()
				}()

				copied := make(chan struct{}, 2)
				go func() {
					_, _ = io.Copy(remoteConn, localConn)
					copied <- struct{}{}
				}()
				go func() {
					_, _ = io.Copy(localConn, remoteConn)
					copied <- struct{}{}
				}()
				<-copied
				_ = localConn.Close()
				_ = remoteConn.Close()
				<-copied
			}()
		}
	}()

	host, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		stop()
		return "", "", nil, err
	}
	return host, port, stop, nil
}

func mergeIperf3PairResult(forward CommandResult, reverse CommandResult) CommandResult {
	stdout := "===== 去程 iperf3 -c ip -P 8 -p 5201 =====\n" + mergeCommandOutput(forward.Stdout, forward.Stderr) +
		"\n\n===== 回程 iperf3 -c ip -P 8 -R -p 5201 =====\n" + mergeCommandOutput(reverse.Stdout, reverse.Stderr)
	success := forward.Success && reverse.Success
	exitCode := 0
	if !success {
		exitCode = firstNonZero(forward.ExitCode, reverse.ExitCode, -1)
	}
	return CommandResult{
		Success:  success,
		Stdout:   stdout,
		ExitCode: exitCode,
		Message:  stdout,
	}
}

func executeLocalIperf3(host string, port string, reverse bool) CommandResult {
	bin, err := localIperf3Path()
	if err != nil {
		return CommandResult{Success: false, ExitCode: -1, Message: err.Error(), Stderr: err.Error()}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	args := []string{"-c", host, "-p", port, "-P", "8", "-t", "10", "-f", "m", "--connect-timeout", "5000"}
	if reverse {
		args = append(args, "-R")
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	exitCode := 0
	if err != nil {
		exitCode = -1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		}
	}
	if ctx.Err() == context.DeadlineExceeded {
		return CommandResult{Success: false, Stdout: stdout.String(), Stderr: stderr.String(), ExitCode: -1, Message: "本地 iperf3 执行超时"}
	}
	return CommandResult{
		Success:  err == nil,
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
		Message:  firstNonEmpty(stderr.String(), stdout.String(), fmt.Sprint(err)),
	}
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func validateDebugTarget(value string) (string, error) {
	target := strings.TrimSpace(value)
	if target == "" {
		return "", errors.New("请先填写目标 IP 或域名。")
	}
	if len(target) > 253 {
		return "", errors.New("目标太长，请检查是否填错。")
	}
	if ip := net.ParseIP(target); ip != nil {
		return target, nil
	}
	hostname := strings.TrimSuffix(target, ".")
	targetRe := regexp.MustCompile(`^[A-Za-z0-9.-]+$`)
	labelRe := regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,61}[A-Za-z0-9])?$`)
	if !targetRe.MatchString(hostname) {
		return "", errors.New("目标格式不正确，只能填写 IP 或普通域名。")
	}
	for _, label := range strings.Split(hostname, ".") {
		if !labelRe.MatchString(label) {
			return "", errors.New("域名格式不正确，请检查每一段是否写完整。")
		}
	}
	return hostname, nil
}

func debugToolCheckCommand(tool string) string {
	switch tool {
	case "mtr":
		return "command -v mtr >/dev/null 2>&1"
	case "ping":
		return "command -v ping >/dev/null 2>&1"
	case "port":
		return "(command -v nc >/dev/null 2>&1 || command -v bash >/dev/null 2>&1)"
	case "iperf3":
		return "command -v iperf3 >/dev/null 2>&1"
	default:
		return "false"
	}
}

func debugToolInstallScript(tool string) string {
	pkg := map[string]map[string]string{
		"mtr":    {"apt": "mtr-tiny", "dnf": "mtr", "yum": "mtr", "apk": "mtr"},
		"ping":   {"apt": "iputils-ping", "dnf": "iputils", "yum": "iputils", "apk": "iputils"},
		"port":   {"apt": "netcat-openbsd", "dnf": "nmap-ncat", "yum": "nmap-ncat", "apk": "netcat-openbsd"},
		"iperf3": {"apt": "iperf3", "dnf": "iperf3", "yum": "iperf3", "apk": "iperf3"},
	}[tool]
	return strings.TrimSpace(fmt.Sprintf(`
set -u
if [ "$(id -u)" -eq 0 ]; then
  SUDO=""
elif command -v sudo >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1; then
  SUDO="sudo -n"
else
  echo "当前账号不是 root，也不能免密 sudo，无法自动安装缺失工具。"
  exit 42
fi
if command -v apt-get >/dev/null 2>&1; then
  export DEBIAN_FRONTEND=noninteractive
  $SUDO apt-get update >/dev/null 2>&1 || true
  $SUDO apt-get install -y %s >/dev/null 2>&1
elif command -v dnf >/dev/null 2>&1; then
  $SUDO dnf install -y %s >/dev/null 2>&1
elif command -v yum >/dev/null 2>&1; then
  $SUDO yum install -y %s >/dev/null 2>&1
elif command -v apk >/dev/null 2>&1; then
  $SUDO apk add --no-cache %s >/dev/null 2>&1
else
  echo "没有识别到 apt、dnf、yum、apk，暂时不能自动安装工具。"
  exit 44
fi
%s || { echo "自动安装后仍然找不到需要的工具。"; exit 43; }
echo "工具已准备好。"
`, pkg["apt"], pkg["dnf"], pkg["yum"], pkg["apk"], debugToolCheckCommand(tool)))
}

func (s *Service) ensureDebugTool(client *ssh.Client, vps VPS, tool string) (bool, string) {
	label := map[string]string{"mtr": "MTR", "ping": "Ping", "port": "端口检测工具", "iperf3": "IPerf3"}[tool]
	check := executeSSH(client, debugToolCheckCommand(tool), 10)
	if check.Success {
		return true, label + " 工具已存在，无需安装。"
	}
	installCmd := debugToolInstallScript(tool)
	if s != nil {
		installCmd = s.wrapRootCommand(vps, installCmd)
	}
	install := executeSSH(client, installCmd, 180)
	if install.Success {
		return true, label + " 工具缺失，已自动安装并准备好。"
	}
	detail := mergeCommandOutput(install.Stdout, install.Stderr)
	if detail != "" && detail != "命令没有返回详细内容。" {
		return false, label + " 工具自动安装失败：" + detail
	}
	return false, label + " 工具自动安装失败，请确认系统包管理器和 sudo 权限。"
}

func ensureDebugTool(client *ssh.Client, tool string) (bool, string) {
	return (&Service{}).ensureDebugTool(client, VPS{}, tool)
}

func mergeCommandOutput(stdout string, stderr string) string {
	parts := []string{}
	if strings.TrimSpace(stdout) != "" {
		parts = append(parts, strings.TrimSpace(stdout))
	}
	if strings.TrimSpace(stderr) != "" {
		parts = append(parts, strings.TrimSpace(stderr))
	}
	if len(parts) == 0 {
		return "命令没有返回详细内容。"
	}
	return strings.Join(parts, "\n")
}

func pingDebugSummary(target string, output string, exitCode int) string {
	loss := regexp.MustCompile(`(\d+(?:\.\d+)?)%\s*packet loss`).FindStringSubmatch(output)
	avg := regexp.MustCompile(`=\s*[\d.]+/([\d.]+)/[\d.]+`).FindStringSubmatch(output)
	lossText := "未知"
	avgText := "未知"
	if len(loss) > 1 {
		lossText = loss[1] + "%"
	}
	if len(avg) > 1 {
		avgText = avg[1] + " ms"
	}
	if exitCode == 0 {
		return "Ping 成功：这台 VPS 可以连到 " + target + "，丢包 " + lossText + "，平均延迟 " + avgText + "。"
	}
	if lossText == "100%" {
		return "Ping 失败：到 " + target + " 完全不通，可能是目标禁 Ping、网络阻断或地址填错。"
	}
	return "Ping 未完全成功：丢包 " + lossText + "，平均延迟 " + avgText + "，请看原始结果。"
}

func mtrDebugSummary(target string, exitCode int) string {
	if exitCode == 0 {
		return "MTR 完成：下面每一行是一跳线路。重点看最后一行 " + target + " 的 Loss% 丢包和 Avg 平均延迟。"
	}
	return "MTR 没有正常完成：可能是目标不通、工具执行失败或网络限制。"
}

func portDebugSummary(target string, port int, exitCode int) string {
	if exitCode == 0 {
		return fmt.Sprintf("端口可连接：这台 VPS 可以连到 %s:%d，说明目标端口大概率是通的。", target, port)
	}
	return fmt.Sprintf("端口不可连接：%s:%d 没连上，可能是服务没开、防火墙拦截、端口写错或网络被阻断。", target, port)
}

func iperf3DebugSummary(output string, exitCode int, serverAlias string) string {
	if exitCode != 0 {
		return "IPerf3 测速失败：可能是 " + serverAlias + " 的 5201 端口被防火墙拦截，或两台 VPS 网络不通。"
	}
	parts := strings.Split(output, "===== 回程")
	if len(parts) >= 2 {
		forward := lastIperfSpeed(parts[0])
		reverse := lastIperfSpeed(parts[1])
		if forward != "" && reverse != "" {
			return "IPerf3 双向测速完成：去程约 " + forward + "/sec，回程约 " + reverse + "/sec。"
		}
	}
	speed := lastIperfSpeed(output)
	if speed == "" {
		return "IPerf3 测速完成：请查看原始结果里的 Mbits/sec 或 Gbits/sec。"
	}
	return "IPerf3 测速完成：8 线程到 " + serverAlias + " 的速度约 " + speed + "/sec。"
}

func lastIperfSpeed(output string) string {
	re := regexp.MustCompile(`([\d.]+)\s+(Kbits|Mbits|Gbits)/sec\s+(?:sender|receiver)`)
	matches := re.FindAllStringSubmatch(output, -1)
	if len(matches) == 0 {
		return ""
	}
	last := matches[len(matches)-1]
	return last[1] + " " + last[2]
}

func analyzeMtrOutput(output string, target string, exitCode int) string {
	if exitCode != 0 {
		return "MTR 没有正常完成，暂时无法分析路径。请看原始结果中的错误信息。"
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	hops := 0
	for _, line := range lines {
		if regexp.MustCompile(`^\s*\d+[\.\|]`).MatchString(line) {
			hops++
		}
	}
	if hops == 0 {
		return "MTR 已返回结果，但未识别到标准跳点格式，请直接查看原始输出。"
	}
	return fmt.Sprintf("已识别 %d 个路由跳点。重点检查最后一跳 %s 的 Loss%% 是否为 0，以及 Avg 是否明显偏高。", hops, target)
}

func downloadText(client *ssh.Client, remotePath string) ([]byte, error) {
	sftpClient, err := sftp.NewClient(client)
	if err != nil {
		return nil, err
	}
	defer sftpClient.Close()
	file, err := sftpClient.Open(remotePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(file)
}

func shellQuote(value string) string {
	return "'" + stringsReplace(value, "'", "'\\''") + "'"
}

func stringsReplace(s, old, new string) string {
	var b bytes.Buffer
	for {
		i := bytes.Index([]byte(s), []byte(old))
		if i < 0 {
			b.WriteString(s)
			break
		}
		b.WriteString(s[:i])
		b.WriteString(new)
		s = s[i+len(old):]
	}
	return b.String()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" && value != "<nil>" {
			return value
		}
	}
	return ""
}

func parseSingboxStatus(stdout string, stderr string, exitCode int) string {
	text := strings.ToLower(stripANSI(stdout))
	errText := strings.ToLower(stripANSI(stderr))
	combined := text + "\n" + errText
	switch {
	case strings.TrimSpace(text) == "active" || strings.Contains(text, ": running") || strings.Contains(combined, "running"):
		return "running"
	case strings.Contains(text, ": stopped") || strings.Contains(text, ": inactive") || strings.Contains(combined, "inactive"):
		return "stopped"
	case strings.Contains(combined, "not installed"):
		return "not_installed"
	case strings.Contains(combined, "not found") || strings.Contains(combined, "no such file"):
		return "not_installed"
	case strings.Contains(combined, "failed"):
		return "failed"
	case exitCode == 0:
		return "unknown"
	default:
		return "error"
	}
}

func stripANSI(value string) string {
	var b strings.Builder
	inEscape := false
	for _, r := range value {
		if r == '\x1b' {
			inEscape = true
			continue
		}
		if inEscape {
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				inEscape = false
			}
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
