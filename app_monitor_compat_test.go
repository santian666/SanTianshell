package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestBestLatencyUsesFastestSuccessfulProbe(t *testing.T) {
	values := []int64{325, 82, 83}
	index := 0
	latency, err := bestLatency(len(values), func() (int64, error) {
		value := values[index]
		index++
		return value, nil
	})
	if err != nil || latency != 82 {
		t.Fatalf("最优延迟 = %d, %v，期望 82ms", latency, err)
	}
}

func TestBestLatencyOnlyFailsWhenEveryProbeFails(t *testing.T) {
	want := errors.New("最后一次失败")
	index := 0
	latency, err := bestLatency(3, func() (int64, error) {
		index++
		if index == 2 {
			return 91, nil
		}
		return -1, want
	})
	if err != nil || latency != 91 {
		t.Fatalf("部分成功时结果 = %d, %v，期望 91ms", latency, err)
	}
	latency, err = bestLatency(2, func() (int64, error) { return -1, want })
	if latency != -1 || !errors.Is(err, want) {
		t.Fatalf("全部失败时结果 = %d, %v", latency, err)
	}
}

func monitorTestShell(t *testing.T) string {
	t.Helper()
	for _, name := range []string{"sh", "bash"} {
		if shell, err := exec.LookPath(name); err == nil {
			return shell
		}
	}
	t.Skip("当前环境没有 POSIX shell")
	return ""
}

func runMonitorProcessesWithTopFixture(t *testing.T, fixture string) string {
	t.Helper()
	shell := monitorTestShell(t)
	wrapped := `
awk() {
  case "$*" in
    *"/proc/stat /proc/meminfo"*) return 0 ;;
  esac
  command awk "$@"
}
sleep() { :; }
top() { printf '%s\n' "$MONITOR_TOP_FIXTURE"; }
sort() {
  case " $* " in
    *" --sort "*) echo '不支持 GNU --sort' >&2; return 64 ;;
  esac
  command sort "$@"
}
ps() { return 127; }
` + monitorProcessesCommand
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, shell, "-c", wrapped)
	command.Env = append(os.Environ(), "MONITOR_TOP_FIXTURE="+fixture)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("执行进程兼容夹具失败: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestMonitorProcessesCommandSupportsCommandHeaderVariants(t *testing.T) {
	tests := []struct {
		name   string
		header string
		want   string
	}{
		{name: "procps-command", header: "COMMAND", want: "worker procps"},
		{name: "busybox-cmd", header: "CMD", want: "worker busybox"},
		{name: "toybox-args", header: "ARGS", want: "worker args"},
		{name: "command-line", header: "COMMAND_LINE", want: "worker command line"},
		{name: "toybox-cmdline", header: "CMDLINE", want: "worker cmdline"},
		{name: "name", header: "NAME", want: "worker-name"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pid := 100 + index
			fixture := "PID USER PR NI VIRT RES SHR S %CPU %MEM TIME+ " + test.header + "\n" +
				strconv.Itoa(pid) + " root 20 0 100 50 10 R 80.0 1.0 0:01 old\n" +
				"PID USER PR NI VIRT RES SHR S %CPU %MEM TIME+ " + test.header + "\n" +
				strconv.Itoa(pid) + " root 20 0 100 50 10 R 25.5 1.2 0:02 " + test.want
			output := runMonitorProcessesWithTopFixture(t, fixture)
			want := "__PROCESS__\nPID CPU MEM COMMAND\n" + strconv.Itoa(pid) + " 25.5 1.2 " + test.want
			if output != want {
				t.Fatalf("%s 表头解析结果不符合预期:\n%s", test.header, output)
			}
		})
	}
}

func TestMonitorProcessesCommandRunsProcMainPath(t *testing.T) {
	shell := monitorTestShell(t)
	probe := exec.Command(shell, "-c", `[ -r /proc/stat ] || exit 1
set -- /proc/[0-9]*/stat
[ -r "$1" ]`)
	if err := probe.Run(); err != nil {
		t.Skip("当前 POSIX shell 没有可读取的 /proc")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, shell, "-c", monitorProcessesCommand)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("/proc 主路径采集失败: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 || lines[0] != "__PROCESS__" || lines[1] != "PID CPU MEM COMMAND" {
		t.Fatalf("/proc 主路径输出格式错误: %q", output)
	}
	fields := strings.Fields(lines[2])
	if len(fields) < 4 {
		t.Fatalf("/proc 主路径进程字段不足: %q", lines[2])
	}
	if _, err := strconv.Atoi(fields[0]); err != nil {
		t.Fatalf("PID 不是数字: %q", fields[0])
	}
	for _, value := range fields[1:3] {
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			t.Fatalf("CPU/内存不是数字: %q", value)
		}
	}
}
