package quickconnect

import "testing"

func TestParse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  Target
	}{
		{
			name:  "普通 SSH 地址",
			input: "root@example.com:2222",
			want:  Target{Scheme: SchemeSSH, User: "root", Host: "example.com", Port: 2222},
		},
		{
			name:  "SSH URL 编码和 IPv6",
			input: "ssh://ops%40team@[2001:db8::1]:2200",
			want:  Target{Scheme: SchemeSSH, User: "ops@team", Host: "2001:db8::1", Port: 2200},
		},
		{
			name:  "RDP 默认端口",
			input: "rdp://DOMAIN%5Cadmin@rdp.example.com",
			want:  Target{Scheme: SchemeRDP, User: `DOMAIN\admin`, Host: "rdp.example.com", Port: 3389},
		},
		{
			name:  "带区域标识的 IPv6 URL",
			input: "ssh://[fe80::1%25Ethernet]",
			want:  Target{Scheme: SchemeSSH, Host: "fe80::1%Ethernet", Port: 22},
		},
		{
			name:  "方括号 IPv6 快速地址",
			input: "admin@[2001:db8::10]:2022",
			want:  Target{Scheme: SchemeSSH, User: "admin", Host: "2001:db8::10", Port: 2022},
		},
		{
			name:  "无方括号 IPv6 使用默认端口",
			input: "2001:db8::10",
			want:  Target{Scheme: SchemeSSH, Host: "2001:db8::10", Port: 22},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := Parse(test.input)
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("Parse() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseRejectsUnsafeOrInvalidInput(t *testing.T) {
	inputs := []string{
		"",
		"http://example.com",
		"ssh://root:secret@example.com",
		"ssh://root%0A@example.com",
		"root@example.com\r\nother",
		"ssh://example.com/path",
		"rdp://example.com?x=1",
		"root@example.com:0",
		"root@example.com:65536",
		"root@example.com:not-a-port",
		"@example.com",
		"ssh://@example.com",
		"root@[2001:db8::1",
		"root@not:an:ipv6",
	}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			if got, err := Parse(input); err == nil {
				t.Fatalf("Parse(%q) = %#v, want error", input, got)
			}
		})
	}
}
