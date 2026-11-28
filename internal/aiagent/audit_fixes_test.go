package aiagent

import "testing"

func TestReadOnlyCommandBlocksSensitivePaths(t *testing.T) {
	blocked := []string{
		"cat /etc/shadow",
		"sudo cat /etc/gshadow",
		"head -n 5 /root/.ssh/id_rsa",
		"cat ~/.ssh/id_ed25519",
		"grep -i key .env",
		"cat /srv/app/.env.production",
		"cat /proc/1/environ",
		"cat /root/.aws/credentials",
	}
	for _, cmd := range blocked {
		if readOnlyCommand(cmd) {
			t.Fatalf("expected sensitive read command %q to require approval (not read-only)", cmd)
		}
	}

	allowed := []string{
		"cat /etc/os-release",
		"tail -n 50 /var/log/nginx/error.log",
		"df -h && free -m",
	}
	for _, cmd := range allowed {
		if !readOnlyCommand(cmd) {
			t.Fatalf("expected normal read-only command %q to remain read-only", cmd)
		}
	}
}
