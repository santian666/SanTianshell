package quickconnect

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestParseCandidatesRecommendedFormats(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantHost string
		wantPort int
		wantUser string
		wantPass string
		wantName string
	}{
		{
			name: "常用格式和主机名", input: "192.0.2.10 root secret ----UK-DO",
			wantHost: "192.0.2.10", wantPort: 22, wantUser: "root", wantPass: "secret", wantName: "UK-DO",
		},
		{
			name: "端口在用户名之前", input: "192.0.2.10 2222 ubuntu secret",
			wantHost: "192.0.2.10", wantPort: 2222, wantUser: "ubuntu", wantPass: "secret", wantName: "192.0.2.10",
		},
		{
			name: "用户名嵌入地址", input: "root@192.0.2.10:2200 secret ----伦敦",
			wantHost: "192.0.2.10", wantPort: 2200, wantUser: "root", wantPass: "secret", wantName: "伦敦",
		},
		{
			name: "IP 在末尾", input: "root secret 192.0.2.10 ----SG",
			wantHost: "192.0.2.10", wantPort: 22, wantUser: "root", wantPass: "secret", wantName: "SG",
		},
		{
			name: "URL 内含密码", input: "ssh://root:s%40cret@example.com:2022 ----Git",
			wantHost: "example.com", wantPort: 2022, wantUser: "root", wantPass: "s@cret", wantName: "Git",
		},
		{
			name: "冒号紧凑格式", input: "example.com:2222:root:p:a:ss ----紧凑",
			wantHost: "example.com", wantPort: 2222, wantUser: "root", wantPass: "p:a:ss", wantName: "紧凑",
		},
		{
			name: "带空格密码", input: `192.0.2.10 root "two words" ----测试机`,
			wantHost: "192.0.2.10", wantPort: 22, wantUser: "root", wantPass: "two words", wantName: "测试机",
		},
		{
			name: "完整 IPv6", input: "2001:db8:0:0:0:0:0:1",
			wantHost: "2001:db8:0:0:0:0:0:1", wantPort: 22, wantUser: "root", wantName: "2001:db8:0:0:0:0:0:1",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates, err := ParseCandidates(test.input)
			if err != nil {
				t.Fatal(err)
			}
			if len(candidates) == 0 {
				t.Fatal("没有返回连接候选")
			}
			got := candidates[0]
			if got.Host != test.wantHost || got.Port != test.wantPort || got.User != test.wantUser || got.Password != test.wantPass || got.Name != test.wantName {
				t.Fatalf("推荐候选 = %#v，期望 host=%q port=%d user=%q password=%q name=%q", got, test.wantHost, test.wantPort, test.wantUser, test.wantPass, test.wantName)
			}
			if !got.Recommended || got.ID != "option-1" {
				t.Fatalf("首项未标记为推荐候选: %#v", got)
			}
		})
	}
}

func TestParseCandidatesAmbiguousSingleField(t *testing.T) {
	candidates, err := ParseCandidates("192.0.2.10 secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) < 2 {
		t.Fatalf("歧义输入应返回多个候选，实际 %d 个", len(candidates))
	}
	if candidates[0].User != "root" || candidates[0].Password != "secret" || !candidates[0].HasPassword {
		t.Fatalf("推荐候选应把 secret 识别为 root 密码: %#v", candidates[0])
	}

	portCandidates, err := ParseCandidates("192.0.2.10 2200")
	if err != nil {
		t.Fatal(err)
	}
	if portCandidates[0].Port != 2200 || portCandidates[0].User != "root" || portCandidates[0].Password != "" {
		t.Fatalf("单个端口未被优先识别: %#v", portCandidates[0])
	}

	userCandidates, err := ParseCandidates("192.0.2.10 ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	if userCandidates[0].User != "ubuntu" || userCandidates[0].Password != "" {
		t.Fatalf("常见用户名未被优先识别: %#v", userCandidates[0])
	}
}

func TestParseCandidatesKeepsDelimiterInsidePassword(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		wantPass string
		wantName string
	}{
		{name: "密码内连续短横线无空白边界", input: "192.0.2.10 root ab----cd", wantPass: "ab----cd", wantName: "192.0.2.10"},
		{name: "引号内分隔符", input: `192.0.2.10 root "ab ---- cd" ----测试机`, wantPass: "ab ---- cd", wantName: "测试机"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidates, err := ParseCandidates(test.input)
			if err != nil {
				t.Fatal(err)
			}
			got := candidates[0]
			if got.Password != test.wantPass || got.Name != test.wantName {
				t.Fatalf("推荐候选 = %#v，期望 password=%q name=%q", got, test.wantPass, test.wantName)
			}
		})
	}
}

func TestSplitQuickConnectNameUsesLastDelimiter(t *testing.T) {
	body, name, err := splitQuickConnectName("server root secret ----旧名 ----新名")
	if err != nil {
		t.Fatal(err)
	}
	if body != "server root secret ----旧名" || name != "新名" {
		t.Fatalf("splitQuickConnectName() = body %q, name %q", body, name)
	}
}

func TestParseCandidatesPrefersNumericPasswordAfterUsername(t *testing.T) {
	candidates, err := ParseCandidates("192.0.2.10 root 1234")
	if err != nil {
		t.Fatal(err)
	}
	if candidates[0].Port != 22 || candidates[0].User != "root" || candidates[0].Password != "1234" {
		t.Fatalf("数字密码被错误优先识别为端口: %#v", candidates[0])
	}
	foundPort := false
	for _, candidate := range candidates {
		if candidate.Port == 1234 && candidate.Password == "" {
			foundPort = true
			break
		}
	}
	if !foundPort {
		t.Fatal("歧义输入未保留数字字段作为端口的候选")
	}
}

func TestParseCandidatesCoversMultipleBareHostTokens(t *testing.T) {
	candidates, err := ParseCandidates("root secret server")
	if err != nil {
		t.Fatal(err)
	}
	hosts := make(map[string]bool)
	for _, candidate := range candidates {
		hosts[candidate.Host] = true
	}
	if len(hosts) < 2 || !hosts["server"] {
		t.Fatalf("未覆盖多个裸词主机解释: %#v", candidates)
	}
}

func TestCandidateJSONDoesNotExposePassword(t *testing.T) {
	candidates, err := ParseCandidates("192.0.2.10 root top-secret")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(candidates)
	if err != nil {
		t.Fatal(err)
	}
	jsonText := string(raw)
	if strings.Contains(jsonText, "top-secret") || strings.Contains(jsonText, `"Password"`) {
		t.Fatalf("候选 JSON 泄露密码: %s", raw)
	}
	if !strings.Contains(jsonText, `"passwordHint":"第 3 项"`) {
		t.Fatalf("候选 JSON 缺少安全的密码位置提示: %s", raw)
	}
}

func TestParseCandidatesDeduplicatesEquivalentPasswordCandidates(t *testing.T) {
	candidates, err := ParseCandidates("192.0.2.10 root secret")
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 {
		t.Fatalf("界面等价候选未去重，实际 %d 项: %#v", len(candidates), candidates)
	}
	got := candidates[0]
	if got.Password != "secret" || got.PasswordHint != "第 3 项" || !got.HasPassword {
		t.Fatalf("去重后未保留得分最高的正确密码解释: %#v", got)
	}
	if !strings.Contains(got.Reason, "密码：已填写（第 3 项）") || strings.Contains(got.Reason, "其余字段作为密码") {
		t.Fatalf("密码说明不明确: %q", got.Reason)
	}
}

func TestSelectCandidateIsStable(t *testing.T) {
	input := "192.0.2.10 root secret"
	first, err := ParseCandidates(input)
	if err != nil {
		t.Fatal(err)
	}
	selected, err := SelectCandidate(input, first[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected, first[0]) {
		t.Fatalf("重新选择候选发生变化: %#v != %#v", selected, first[0])
	}
	if _, err := SelectCandidate(input, "missing"); err == nil {
		t.Fatal("失效候选 ID 应返回错误")
	}
}

func TestParseCandidatesRejectsInvalidInput(t *testing.T) {
	inputs := []string{
		"",
		"192.0.2.10 root secret ----",
		"192.0.2.10\nroot secret",
		`192.0.2.10 root "unterminated`,
		"example.com:70000:root:secret",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if _, err := ParseCandidates(input); err == nil {
				t.Fatalf("ParseCandidates(%q) 应返回错误", input)
			}
		})
	}
}
