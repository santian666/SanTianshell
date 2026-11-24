package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"santianshell/internal/remote"

	runtime2 "github.com/wailsapp/wails/v2/pkg/runtime"
)

func (a *App) CheckHostLatency(id string) (int64, error) {
	h, err := a.host(id)
	if err != nil {
		return -1, err
	}
	if strings.TrimSpace(h.SSHJumpHostID) != "" {
		if err := a.ensureSSHJumpChainTrusted(h, false); err != nil {
			return -1, err
		}
		h = a.wireSSHJumpDial(h, false, false)
	}
	return bestLatency(2, func() (int64, error) { return hostListLatency(h, true) })
}

func (a *App) CheckConnectedHostLatency(id string, clientEpoch int) (int64, error) {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return -1, err
	}
	return shell.SSHLatency(5 * time.Second)
}

func (a *App) CheckHostRTT(id string) (int64, error) {
	h, err := a.host(id)
	if err != nil {
		return -1, err
	}
	if strings.TrimSpace(h.SSHJumpHostID) != "" {
		if err := a.ensureSSHJumpChainTrusted(h, false); err != nil {
			return -1, err
		}
		h = a.wireSSHJumpDial(h, false, false)
	}
	return bestLatency(3, func() (int64, error) { return hostListLatency(h, false) })
}

func hostListLatency(h remote.Host, allowCache bool) (int64, error) {
	if supportsLocalProxyQuickLatency(h) {
		if latency, err := localProxyQuickLatency(allowCache); err == nil {
			return latency, nil
		}
	}
	return remote.Latency(h)
}

func supportsLocalProxyQuickLatency(h remote.Host) bool {
	if !h.ProxyEnabled {
		return false
	}
	host := strings.ToLower(strings.TrimSpace(h.ProxyHost))
	if host == "" {
		host = "127.0.0.1"
	}
	port := h.ProxyPort
	if port == 0 {
		port = 20808
	}
	return port == 20808 && (host == "127.0.0.1" || host == "localhost" || host == "::1")
}

func localProxyQuickLatency(allowCache bool) (int64, error) {
	localProxyLatencyCache.Lock()
	defer localProxyLatencyCache.Unlock()
	client := &http.Client{Timeout: 7 * time.Second}
	var proxies struct {
		Proxies map[string]struct {
			Now string `json:"now"`
		} `json:"proxies"`
	}
	if err := getLocalProxyJSON(client, "http://127.0.0.1:20990/proxies", &proxies); err != nil {
		return -1, err
	}
	selector := strings.TrimSpace(proxies.Proxies["proxy"].Now)
	if selector == "" {
		return -1, errors.New("本地代理没有当前节点")
	}
	if allowCache && selector == localProxyLatencyCache.selector && time.Now().Before(localProxyLatencyCache.expires) {
		return localProxyLatencyCache.value, nil
	}
	endpoint := "http://127.0.0.1:20990/proxies/" + url.PathEscape(selector) + "/delay?timeout=5000&url=" + url.QueryEscape("https://www.gstatic.com/generate_204")
	var result struct {
		Delay int64 `json:"delay"`
	}
	if err := getLocalProxyJSON(client, endpoint, &result); err != nil {
		return -1, err
	}
	if result.Delay < 1 {
		return -1, errors.New("本地代理返回的延迟无效")
	}
	localProxyLatencyCache.selector = selector
	localProxyLatencyCache.value = result.Delay
	localProxyLatencyCache.expires = time.Now().Add(5 * time.Second)
	return result.Delay, nil
}

func getLocalProxyJSON(client *http.Client, endpoint string, target any) error {
	response, err := client.Get(endpoint)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("本地代理 API 返回 HTTP %d", response.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target)
}

func bestLatency(attempts int, probe func() (int64, error)) (int64, error) {
	if attempts < 1 {
		attempts = 1
	}
	best := int64(-1)
	var lastErr error
	for range attempts {
		latency, err := probe()
		if err != nil {
			lastErr = err
			continue
		}
		if best < 0 || latency < best {
			best = latency
		}
	}
	if best >= 0 {
		return best, nil
	}
	if lastErr == nil {
		lastErr = errors.New("TCP 延迟检测失败")
	}
	return -1, lastErr
}

func (a *App) CheckHostSingboxStatus(id string) (string, error) {
	h, err := a.host(id)
	if err != nil {
		return "", err
	}
	if !strings.EqualFold(h.Protocol, "ssh") {
		return "", errors.New("仅 SSH 主机支持 Sing-box 状态检测")
	}
	command := `if (command -v systemctl >/dev/null 2>&1 && systemctl is-active --quiet sing-box) || pgrep -x sing-box >/dev/null 2>&1; then echo running; elif command -v sing-box >/dev/null 2>&1 || [ -x /etc/sing-box/bin/sing-box ] || (command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files sing-box.service --no-legend 2>/dev/null | grep -q sing-box); then echo stopped; else echo not_installed; fi`
	result, err := a.RunCommand(id, command)
	if err != nil {
		return "", err
	}
	status := strings.TrimSpace(result.Output)
	switch status {
	case "running", "stopped", "not_installed":
		return status, nil
	default:
		return "unknown", nil
	}
}

func (a *App) runMonitorCommand(id string, clientEpoch int, command string, timeout time.Duration) (remote.Result, error) {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return remote.Result{}, err
	}
	return shell.RunMonitorTimeout(command, timeout)
}

func (a *App) GetMonitorFast(id string, clientEpoch int) (remote.Result, error) {
	command := `export LANG=C LC_ALL=C
echo __UPTIME__; uptime
echo __CPU__; awk '/^cpu / { idle=$5+$6; for (i=2;i<=NF && i<=9;i++) total+=$i; printf "%.0f %.0f\n", total, idle; exit }' /proc/stat
echo __MEM__
awk '/^MemTotal:/ { total=int($2/1024) } /^MemAvailable:/ { available=int($2/1024); haveavailable=1 } /^MemFree:/ { memfree=int($2/1024) } /^Buffers:/ { buffers=int($2/1024) } /^Cached:/ { cached=int($2/1024) } /^SwapTotal:/ { swaptotal=int($2/1024) } /^SwapFree:/ { swapfree=int($2/1024) } END { if (!haveavailable) available=memfree+buffers+cached; if (available>total) available=total; used=total-available; swapused=swaptotal-swapfree; if (swapused<0) swapused=0; printf "Mem: %d %d 0 0 0 %d\nSwap: %d %d %d\n", total, used, available, swaptotal, swapused, swapfree }' /proc/meminfo
default_if=$(awk '$2 == "00000000" && $8 == "00000000" && $4 != "0000" { metric=$7+0; if (!found || metric < best) { iface=$1; best=metric; found=1 } } END { print iface }' /proc/net/route 2>/dev/null)
if [ -z "$default_if" ]; then
  default_if=$(awk '$1 == "00000000000000000000000000000000" && $2 == "00" { print $6, $10 }' /proc/net/ipv6_route 2>/dev/null | sort | head -1 | awk '{ print $2 }')
fi
if [ -z "$default_if" ]; then
  default_if=$(awk 'NR>2 { gsub(":"," "); if ($1 !~ /^(lo|docker|veth|br-|virbr|tun|tap|wg|tailscale|zt)/) { total=$2+$10; if (total>best) { best=total; iface=$1 } } } END { print iface }' /proc/net/dev)
fi
sample_time=$(awk '{ print $1; exit }' /proc/uptime 2>/dev/null)
echo __NETWORK__; awk -v iface="$default_if" -v sampled="$sample_time" 'NR>2 { gsub(":"," "); if ($1 == iface) { rx=$2; tx=$10 } } END { printf "%s %.0f %.0f %s\n", iface, rx, tx, sampled }' /proc/net/dev`
	return a.runMonitorCommand(id, clientEpoch, command, 4*time.Second)
}

const monitorProcessesReduceAWK = `$1 == "__SECOND__" {
  second = 1
  next
}
$1 == "__TOTAL__" {
  if (second) {
    total2 = $2 + 0
    cpu_count = $3 + 0
    mem_total = $4 + 0
  } else {
    total1 = $2 + 0
  }
  next
}
$1 != "__PROC__" { next }
!second {
  first_ticks[$2] = ($3 + 0) + ($4 + 0)
  first_start[$2] = $5
  next
}
{
  pid = $2
  if (!(pid in first_ticks) || first_start[pid] != $5) next
  total_delta = total2 - total1
  process_delta = (($3 + 0) + ($4 + 0)) - first_ticks[pid]
  if (total_delta <= 0 || process_delta < 0) next
  if (cpu_count < 1) cpu_count = 1
  cpu = process_delta * cpu_count * 100 / total_delta
  if (cpu > cpu_count * 100) cpu = cpu_count * 100
  printf "%s %.1f %s %.0f\n", pid, cpu, $5, mem_total
}`

const monitorProcessesFormatAWK = `$1 == "__DETAIL__" {
  memory = $4 > 0 ? ($5 + 0) * 100 / $4 : 0
  if (memory > 100) memory = 100
  command = $6
  for (i = 7; i <= NF; i++) command = command " " $i
  if (command == "") command = "[" $2 "]"
  printf "%s %s %.1f %s\n", $2, $3, memory, command
}`

const monitorTopProcessesAWK = `$1 == "PID" {
  frame++
  cpu = 0
  memory = 0
  command = 0
  for (i = 1; i <= NF; i++) {
    if ($i == "%CPU" || $i == "CPU%") cpu = i
    else if ($i == "%MEM" || $i == "MEM%" || $i == "%VSZ" || $i == "VSZ%" || $i == "%RSS" || $i == "RSS%") memory = i
    else if ($i == "COMMAND" || $i == "CMD" || $i == "ARGS" || $i == "COMMAND_LINE" || $i == "CMDLINE" || $i == "NAME") command = i
  }
  next
}
frame >= wanted && cpu && memory && command && $1 ~ /^[0-9]+$/ && $command != "top" {
  cpu_value = $cpu
  memory_value = $memory
  sub(/%$/, "", cpu_value)
  sub(/%$/, "", memory_value)
  name = $command
  for (i = command + 1; i <= NF; i++) name = name " " $i
  printf "%s %s %s %s\n", $1, cpu_value, memory_value, name
}`

const monitorProcessesCommand = `export LANG=C LC_ALL=C
snapshot_processes() {
  awk '
$1 == "cpu" {
  for (i = 2; i <= NF && i <= 9; i++) total += $i
  next
}
$1 ~ /^cpu[0-9]+$/ { cpu_count++ }
$1 == "MemTotal:" { mem_total = $2 }
END {
  if (cpu_count < 1) cpu_count = 1
  printf "__TOTAL__ %.0f %d %.0f\n", total, cpu_count, mem_total
}' /proc/stat /proc/meminfo 2>/dev/null

  for snapshot_stat_file in /proc/[0-9]*/stat; do
    [ -r "$snapshot_stat_file" ] || continue
    snapshot_pid=${snapshot_stat_file#/proc/}
    snapshot_pid=${snapshot_pid%/stat}
    snapshot_stat_line=
    IFS= read -r snapshot_stat_line < "$snapshot_stat_file" 2>/dev/null || continue
    snapshot_stat_rest=${snapshot_stat_line##*) }
    set -- $snapshot_stat_rest
    [ "$#" -ge 20 ] || continue
    printf "__PROC__ %s %s %s %s\n" "$snapshot_pid" "${12}" "${13}" "${20}"
  done
}

first_sample=$(snapshot_processes)
processes=
case "$first_sample" in
  *"__PROC__ "*)
    sleep 0.2 2>/dev/null || sleep 1 2>/dev/null || :
    second_sample=$(snapshot_processes)
    selected_processes=$(printf '%s\n__SECOND__\n%s\n' "$first_sample" "$second_sample" | awk '` + monitorProcessesReduceAWK + `' | sort -k2,2nr -k1,1n | head -n 8)
    processes=$(printf '%s\n' "$selected_processes" | while read -r selected_pid selected_cpu selected_start selected_mem_total; do
      [ -n "$selected_pid" ] || continue
      selected_stat_line=
      IFS= read -r selected_stat_line < "/proc/$selected_pid/stat" 2>/dev/null || continue
      selected_stat_rest=${selected_stat_line##*) }
      set -- $selected_stat_rest
      [ "$#" -ge 20 ] || continue
      [ "${20}" = "$selected_start" ] || continue

      selected_rss=0
      if [ -r "/proc/$selected_pid/status" ]; then
        while read -r selected_key selected_value selected_rest; do
          if [ "$selected_key" = "VmRSS:" ]; then
            selected_rss=$selected_value
            break
          fi
        done < "/proc/$selected_pid/status" 2>/dev/null
      fi
      case "$selected_rss" in
        ''|*[!0-9]*) selected_rss=0 ;;
      esac

      selected_command="[$selected_pid]"
      if [ -r "/proc/$selected_pid/comm" ]; then
        IFS= read -r selected_command < "/proc/$selected_pid/comm" 2>/dev/null || selected_command="[$selected_pid]"
      fi
      [ -n "$selected_command" ] || selected_command="[$selected_pid]"
      printf "__DETAIL__ %s %s %s %s %s\n" "$selected_pid" "$selected_cpu" "$selected_mem_total" "$selected_rss" "$selected_command"
    done | awk '` + monitorProcessesFormatAWK + `')
    ;;
esac

collect_top_processes() {
  top_wanted_frame=$1
  shift
  top "$@" 2>/dev/null | awk -v wanted="$top_wanted_frame" '` + monitorTopProcessesAWK + `' | sort -k2,2nr -k1,1n | head -n 8
}

if [ -z "$processes" ]; then
  processes=$(collect_top_processes 2 -b -n 2 -d 0.2)
fi
if [ -z "$processes" ]; then
  processes=$(collect_top_processes 2 -b -n 2 -d 1)
fi
if [ -z "$processes" ]; then
  processes=$(collect_top_processes 1 -b -n 1)
fi
if [ -z "$processes" ]; then
  processes=$(ps -eo pid=,pcpu=,pmem=,comm= 2>/dev/null | sort -k2,2nr -k1,1n | head -n 8)
fi
if [ -z "$processes" ]; then
  echo '当前系统不支持进程采集' >&2
  exit 44
fi
echo __PROCESS__; printf 'PID CPU MEM COMMAND\n%s\n' "$processes"`

func (a *App) GetMonitorProcesses(id string, clientEpoch int) (remote.Result, error) {
	return a.runMonitorCommand(id, clientEpoch, monitorProcessesCommand, 5*time.Second)
}

func (a *App) GetMonitorDisks(id string, clientEpoch int) (remote.Result, error) {
	command := `export LANG=C LC_ALL=C
disks=$(df -hPl 2>/dev/null)
status=$?
if [ "$status" -ne 0 ]; then
  disks=$(df -hP 2>/dev/null)
  status=$?
fi
if [ "$status" -ne 0 ]; then
  disks=$(df -h 2>/dev/null)
fi
if [ -z "$disks" ]; then
  echo '当前系统不支持磁盘采集' >&2
  exit 44
fi
echo __DISK__; printf '%s\n' "$disks"`
	return a.runMonitorCommand(id, clientEpoch, command, 6*time.Second)
}

const monitorStreamCommand = `export LANG=C LC_ALL=C
trap 'exit 0' PIPE HUP TERM INT

emit_fast() {
  echo __UPTIME__; uptime
  echo __CPU__; awk '/^cpu / { idle=$5+$6; for (i=2;i<=NF && i<=9;i++) total+=$i; printf "%.0f %.0f\n", total, idle; exit }' /proc/stat
  echo __MEM__
  awk '/^MemTotal:/ { total=int($2/1024) } /^MemAvailable:/ { available=int($2/1024); haveavailable=1 } /^MemFree:/ { memfree=int($2/1024) } /^Buffers:/ { buffers=int($2/1024) } /^Cached:/ { cached=int($2/1024) } /^SwapTotal:/ { swaptotal=int($2/1024) } /^SwapFree:/ { swapfree=int($2/1024) } END { if (!haveavailable) available=memfree+buffers+cached; if (available>total) available=total; used=total-available; swapused=swaptotal-swapfree; if (swapused<0) swapused=0; printf "Mem: %d %d 0 0 0 %d\nSwap: %d %d %d\n", total, used, available, swaptotal, swapused, swapfree }' /proc/meminfo
  default_if=$(awk '$2 == "00000000" && $8 == "00000000" && $4 != "0000" { metric=$7+0; if (!found || metric < best) { iface=$1; best=metric; found=1 } } END { print iface }' /proc/net/route 2>/dev/null)
  if [ -z "$default_if" ]; then
    default_if=$(awk '$1 == "00000000000000000000000000000000" && $2 == "00" { print $6, $10 }' /proc/net/ipv6_route 2>/dev/null | sort | head -1 | awk '{ print $2 }')
  fi
  if [ -z "$default_if" ]; then
    default_if=$(awk 'NR>2 { gsub(":"," "); if ($1 !~ /^(lo|docker|veth|br-|virbr|tun|tap|wg|tailscale|zt)/) { total=$2+$10; if (total>best) { best=total; iface=$1 } } } END { print iface }' /proc/net/dev)
  fi
  sample_time=$(awk '{ print $1; exit }' /proc/uptime 2>/dev/null)
  echo __NETWORK__; awk -v iface="$default_if" -v sampled="$sample_time" 'NR>2 { gsub(":"," "); if ($1 == iface) { rx=$2; tx=$10 } } END { printf "%s %.0f %.0f %s\n", iface, rx, tx, sampled }' /proc/net/dev
}

emit_disk() {
  disks=$(df -hPl 2>/dev/null)
  status=$?
  if [ "$status" -ne 0 ]; then
    disks=$(df -hP 2>/dev/null)
    status=$?
  fi
  if [ "$status" -ne 0 ]; then
    disks=$(df -h 2>/dev/null)
  fi
  if [ -n "$disks" ]; then
    echo __DISK__
    printf '%s\n' "$disks"
  fi
}

snapshot_processes() {
  awk '
$1 == "cpu" {
  for (i = 2; i <= NF && i <= 9; i++) total += $i
  next
}
$1 ~ /^cpu[0-9]+$/ { cpu_count++ }
$1 == "MemTotal:" { mem_total = $2 }
END {
  if (cpu_count < 1) cpu_count = 1
  printf "__TOTAL__ %.0f %d %.0f\n", total, cpu_count, mem_total
}' /proc/stat /proc/meminfo 2>/dev/null

  for snapshot_stat_file in /proc/[0-9]*/stat; do
    [ -r "$snapshot_stat_file" ] || continue
    snapshot_pid=${snapshot_stat_file#/proc/}
    snapshot_pid=${snapshot_pid%/stat}
    snapshot_stat_line=
    IFS= read -r snapshot_stat_line < "$snapshot_stat_file" 2>/dev/null || continue
    snapshot_stat_rest=${snapshot_stat_line##*) }
    set -- $snapshot_stat_rest
    [ "$#" -ge 20 ] || continue
    printf "__PROC__ %s %s %s %s\n" "$snapshot_pid" "${12}" "${13}" "${20}"
  done
}

collect_top_processes() {
  top_wanted_frame=$1
  shift
  top "$@" 2>/dev/null | awk -v wanted="$top_wanted_frame" '` + monitorTopProcessesAWK + `' | sort -k2,2nr -k1,1n | head -n 8
}

emit_processes_from_pair() {
  first_sample=$1
  second_sample=$2
  processes=
  case "$first_sample" in
    *"__PROC__ "*)
      case "$second_sample" in
        *"__PROC__ "*)
          selected_processes=$(printf '%s\n__SECOND__\n%s\n' "$first_sample" "$second_sample" | awk '` + monitorProcessesReduceAWK + `' | sort -k2,2nr -k1,1n | head -n 8)
          processes=$(printf '%s\n' "$selected_processes" | while read -r selected_pid selected_cpu selected_start selected_mem_total; do
            [ -n "$selected_pid" ] || continue
            selected_stat_line=
            IFS= read -r selected_stat_line < "/proc/$selected_pid/stat" 2>/dev/null || continue
            selected_stat_rest=${selected_stat_line##*) }
            set -- $selected_stat_rest
            [ "$#" -ge 20 ] || continue
            [ "${20}" = "$selected_start" ] || continue

            selected_rss=0
            if [ -r "/proc/$selected_pid/status" ]; then
              while read -r selected_key selected_value selected_rest; do
                if [ "$selected_key" = "VmRSS:" ]; then
                  selected_rss=$selected_value
                  break
                fi
              done < "/proc/$selected_pid/status" 2>/dev/null
            fi
            case "$selected_rss" in
              ''|*[!0-9]*) selected_rss=0 ;;
            esac

            selected_command="[$selected_pid]"
            if [ -r "/proc/$selected_pid/comm" ]; then
              IFS= read -r selected_command < "/proc/$selected_pid/comm" 2>/dev/null || selected_command="[$selected_pid]"
            fi
            [ -n "$selected_command" ] || selected_command="[$selected_pid]"
            printf "__DETAIL__ %s %s %s %s %s\n" "$selected_pid" "$selected_cpu" "$selected_mem_total" "$selected_rss" "$selected_command"
          done | awk '` + monitorProcessesFormatAWK + `')
          ;;
      esac
      ;;
  esac

  if [ -z "$processes" ]; then
    processes=$(collect_top_processes 2 -b -n 2 -d 0.2)
  fi
  if [ -z "$processes" ]; then
    processes=$(collect_top_processes 1 -b -n 1)
  fi
  if [ -z "$processes" ]; then
    processes=$(ps -eo pid=,pcpu=,pmem=,comm= 2>/dev/null | sort -k2,2nr -k1,1n | head -n 8)
  fi
  if [ -n "$processes" ]; then
    echo __PROCESS__
    printf 'PID CPU MEM COMMAND\n%s\n' "$processes"
  fi
}

prev_proc=$(snapshot_processes)
{
  printf '__FRAME_BEGIN__\n'
  emit_fast
  emit_disk
  printf '__FRAME_END__\n'
} || exit 0

if sleep 0.18 2>/dev/null; then
  fast_sleep="0.5"
  proc_mod=2
  disk_mod=6
else
  sleep 1 2>/dev/null || exit 0
  fast_sleep="1"
  proc_mod=1
  disk_mod=3
fi

curr_proc=$(snapshot_processes)
{
  printf '__FRAME_BEGIN__\n'
  emit_fast
  emit_processes_from_pair "$prev_proc" "$curr_proc"
  printf '__FRAME_END__\n'
} || exit 0
prev_proc=$curr_proc

tick=1
while :; do
  sleep "$fast_sleep" 2>/dev/null || sleep 1 || exit 0
  frame_body=$(emit_fast)
  if [ $((tick % proc_mod)) -eq 0 ]; then
    curr_proc=$(snapshot_processes)
    proc_out=$(emit_processes_from_pair "$prev_proc" "$curr_proc")
    prev_proc=$curr_proc
    if [ -n "$proc_out" ]; then
      frame_body=$(printf '%s\n%s' "$frame_body" "$proc_out")
    fi
  fi
  if [ $((tick % disk_mod)) -eq 0 ]; then
    disk_out=$(emit_disk)
    if [ -n "$disk_out" ]; then
      frame_body=$(printf '%s\n%s' "$frame_body" "$disk_out")
    fi
  fi
  printf '__FRAME_BEGIN__\n%s\n__FRAME_END__\n' "$frame_body" || exit 0
  tick=$((tick + 1))
done`

func monitorErrText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func (a *App) stopMonitorStreamsExcept(keepID string) {
	a.mu.RLock()
	targets := make([]*remote.Shell, 0, len(a.shells))
	for hostID, shell := range a.shells {
		if shell != nil && hostID != keepID {
			targets = append(targets, shell)
		}
	}
	a.mu.RUnlock()
	for _, shell := range targets {
		shell.StopMonitorStream()
	}
}

func (a *App) StartMonitorStream(id string, clientEpoch int) error {
	shell, err := a.connectedShell(id, clientEpoch)
	if err != nil {
		return err
	}
	a.stopMonitorStreamsExcept(id)

	rttCtx, rttCancel := context.WithCancel(a.lifecycleContext())
	if err := shell.StartMonitorStream(
		monitorStreamCommand,
		func(frame string) {
			cur, curErr := a.connectedShell(id, clientEpoch)
			if curErr != nil || cur != shell || a.ctx == nil {
				return
			}
			runtime2.EventsEmit(a.ctx, "ssh-monitor-stream", map[string]any{
				"id":     id,
				"epoch":  clientEpoch,
				"output": frame,
			})
		},
		func(exitErr error) {
			rttCancel()
			if exitErr == nil {
				return
			}
			cur, curErr := a.connectedShell(id, clientEpoch)
			if curErr != nil || cur != shell || a.ctx == nil {
				return
			}
			runtime2.EventsEmit(a.ctx, "ssh-monitor-stream-stopped", map[string]any{
				"id":    id,
				"epoch": clientEpoch,
				"error": exitErr.Error(),
			})
		},
	); err != nil {
		rttCancel()
		return err
	}

	go func() {
		probeRTT := func() {
			cur, curErr := a.connectedShell(id, clientEpoch)
			if curErr != nil || cur != shell || rttCtx.Err() != nil {
				return
			}
			latency, latErr := shell.SSHLatency(3 * time.Second)
			if rttCtx.Err() != nil || a.ctx == nil {
				return
			}
			cur, curErr = a.connectedShell(id, clientEpoch)
			if curErr != nil || cur != shell {
				return
			}
			runtime2.EventsEmit(a.ctx, "ssh-monitor-latency", map[string]any{
				"id":      id,
				"epoch":   clientEpoch,
				"latency": latency,
				"error":   monitorErrText(latErr),
			})
		}
		probeRTT()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-rttCtx.Done():
				return
			case <-ticker.C:
				probeRTT()
			}
		}
	}()

	return nil
}

func (a *App) StopMonitorStream(id string) {
	id = strings.TrimSpace(id)
	if id == "" {
		a.stopMonitorStreamsExcept("")
		return
	}
	a.mu.RLock()
	shell := a.shells[id]
	a.mu.RUnlock()
	if shell != nil {
		shell.StopMonitorStream()
	}
}
