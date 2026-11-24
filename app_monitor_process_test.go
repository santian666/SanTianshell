package main

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runMonitorAWK(t *testing.T, program, input string, variables ...string) string {
	t.Helper()
	awk, err := exec.LookPath("awk")
	if err != nil {
		t.Skip("当前环境没有 awk")
	}
	args := make([]string, 0, len(variables)*2+1)
	for _, variable := range variables {
		args = append(args, "-v", variable)
	}
	args = append(args, program)
	command := exec.Command(awk, args...)
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("执行 awk 失败: %v\n%s", err, output)
	}
	return strings.TrimSpace(string(output))
}

func TestMonitorProcessesReduceAWKCalculatesIntervalCPU(t *testing.T) {
	input := `__TOTAL__ 1000 4 100000
__PROC__ 10 100 50 500
__PROC__ 11 200 50 600
__PROC__ 13 100 20 700
__SECOND__
__TOTAL__ 1200 4 100000
__PROC__ 10 110 50 500
__PROC__ 11 210 50 601
__PROC__ 12 10 5 800
`
	output := runMonitorAWK(t, monitorProcessesReduceAWK, input)
	if output != "10 20.0 500 100000" {
		t.Fatalf("区间采样结果不符合预期: %q", output)
	}
}

func TestMonitorProcessesFormatAWKCalculatesRSSMemory(t *testing.T) {
	input := `__DETAIL__ 10 20.0 100000 1000 worker one
__DETAIL__ 20 5.0 0 2000 fallback
`
	output := runMonitorAWK(t, monitorProcessesFormatAWK, input)
	want := "10 20.0 1.0 worker one\n20 5.0 0.0 fallback"
	if output != want {
		t.Fatalf("进程详情格式化结果不符合预期: %q", output)
	}
}

func TestMonitorTopProcessesAWKSupportsProcpsAndBusyBox(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name: "procps",
			input: `PID USER PR NI VIRT RES SHR S %CPU %MEM TIME+ COMMAND
10 root 20 0 100 50 10 R 99.0 1.0 0:01 old
PID USER PR NI VIRT RES SHR S %CPU %MEM TIME+ COMMAND
10 root 20 0 100 50 10 R 25.5 1.2 0:02 worker
`,
			want: "10 25.5 1.2 worker",
		},
		{
			name: "busybox-vsz",
			input: `PID PPID USER STAT VSZ %VSZ CPU %CPU COMMAND
20 1 root R 100 1.0 0 80.0 old
PID PPID USER STAT VSZ %VSZ CPU %CPU COMMAND
20 1 root R 100 1.5 0 40.0 worker two
`,
			want: "20 40.0 1.5 worker two",
		},
		{
			name: "busybox-rss",
			input: `PID PPID USER STAT RSS %RSS CPU CPU% CMD
30 1 root R 200 2.0 1 70.0 old
PID PPID USER STAT RSS %RSS CPU CPU% CMD
30 1 root R 200 2.5 1 35.0 worker three
`,
			want: "30 35.0 2.5 worker three",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			output := runMonitorAWK(t, monitorTopProcessesAWK, test.input, "wanted=2")
			if output != test.want {
				t.Fatalf("top 解析结果不符合预期: %q", output)
			}
		})
	}
}

func TestMonitorProcessesCommandKeepsRealtimeFallbackOrder(t *testing.T) {
	procIndex := strings.Index(monitorProcessesCommand, "first_sample=$(snapshot_processes)")
	topIndex := strings.Index(monitorProcessesCommand, "collect_top_processes 2 -b -n 2 -d 0.2")
	psIndex := strings.Index(monitorProcessesCommand, "ps -eo pid=,pcpu=,pmem=,comm=")
	if procIndex < 0 || topIndex <= procIndex || psIndex <= topIndex {
		t.Fatalf("进程采集兜底顺序错误: proc=%d top=%d ps=%d", procIndex, topIndex, psIndex)
	}
	for _, required := range []string{
		"/proc/stat",
		"/proc/[0-9]*/stat",
		"snapshot_stat_line##*) ",
		"sleep 0.2",
		"first_start[pid] != $5",
	} {
		if !strings.Contains(monitorProcessesCommand+monitorProcessesReduceAWK, required) {
			t.Fatalf("进程采集命令缺少关键逻辑 %q", required)
		}
	}
	for _, forbidden := range []string{"mktemp", "nproc"} {
		if strings.Contains(monitorProcessesCommand, forbidden) {
			t.Fatalf("进程采集命令不应依赖 %s", forbidden)
		}
	}
}

func TestMonitorProcessesCommandShellSyntax(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		shell, err = exec.LookPath("bash")
	}
	if err != nil {
		t.Skip("当前环境没有 POSIX shell")
	}
	command := exec.Command(shell, "-n", "-c", monitorProcessesCommand)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("进程采集命令语法错误: %v\n%s", err, output)
	}
}

func TestMonitorProcessesCommandOnLinux(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅在 Linux 验证真实 /proc 采样")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-c", monitorProcessesCommand)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("Linux 进程采集失败: %v\n%s", err, output)
	}
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	if len(lines) < 3 || lines[0] != "__PROCESS__" || lines[1] != "PID CPU MEM COMMAND" {
		t.Fatalf("Linux 进程采集输出格式错误: %q", output)
	}
	fields := strings.Fields(lines[2])
	if len(fields) < 4 {
		t.Fatalf("Linux 进程行字段不足: %q", lines[2])
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
