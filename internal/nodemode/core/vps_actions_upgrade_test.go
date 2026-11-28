package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploySingboxCommandRemovesHardcodedPrivateDNS(t *testing.T) {
	cmd := deploySingboxCommand()
	if strings.Contains(cmd, "10.20.30.40") {
		t.Fatalf("deploy command must not hardcode private DNS 10.20.30.40")
	}
	if !strings.Contains(cmd, "DNS=1.1.1.1 8.8.8.8") {
		t.Fatalf("deploy command should set public DNS servers")
	}
	if !strings.Contains(cmd, "install_mirror_url=") {
		t.Fatalf("deploy command should include mirror fallback URL")
	}
}

func TestTrafficParsingWithoutPython(t *testing.T) {
	portSet := map[int]bool{20001: true, 20002: true}
	ports := []int{20001, 20002}
	ssFirst := `ESTAB 0 0 10.0.0.2:20001 1.2.3.4:55555
	 cubic wscale:7,7 rto:204 bytes_sent:1000 bytes_received:2000
ESTAB 0 0 10.0.0.2:20002 5.6.7.8:44444
	 cubic bytes_acked:500 bytes_received:500`
	ssSecond := `ESTAB 0 0 10.0.0.2:20001 1.2.3.4:55555
	 cubic wscale:7,7 rto:204 bytes_sent:6000 bytes_received:12000
ESTAB 0 0 10.0.0.2:20002 5.6.7.8:44444
	 cubic bytes_acked:1500 bytes_received:1500`

	c1, _, h1 := parseSSConnections(ssFirst, portSet)
	c2, counts, h2 := parseSSConnections(ssSecond, portSet)
	if !h1 || !h2 || counts[20001] != 1 || counts[20002] != 1 {
		t.Fatalf("unexpected ss parse counts: %+v", counts)
	}
	rt := computeRealtimeTraffic(c1, c2, counts, ports, 0.5)
	if len(rt) != 2 || rt[0]["port"].(int) != 20001 {
		t.Fatalf("expected port 20001 top realtime, got %+v", rt)
	}
	if rt[0]["rx_bytes_per_second"].(int64) != 20000 || rt[0]["tx_bytes_per_second"].(int64) != 10000 {
		t.Fatalf("unexpected bps calculation: %+v", rt[0])
	}

	nftJSON := `{"nftables":[{"counter":{"name":"nsb_p20001_rx","bytes":12345}},{"counter":{"name":"nsb_p20001_tx","bytes":67890}}]}`
	totals, ok := parseNftTrafficJSON(nftJSON, portSet, ports)
	if !ok || len(totals) != 2 || totals[0]["total_bytes"].(int64) != 80235 {
		t.Fatalf("unexpected nft parse: ok=%v totals=%+v", ok, totals)
	}
}

func TestAddNodesRandomPortAndDeduplicateOutbounds(t *testing.T) {
	svc := NewServiceWithDataDir(t.TempDir())
	_ = svc.SaveVPS(VPS{Alias: "us-1", Address: "1.2.3.4", Port: 22, Username: "root"})
	res, err := svc.AddNodes(AddNodeRequest{
		VPSAlias:         "us-1",
		GroupName:        "rnd",
		InboundProtocol:  "vless",
		OutboundProtocol: "socks",
		RandomPort:       true,
		Count:            3,
		OutboundLines:    "9.9.9.1:1080:u:p\n9.9.9.1:1080:u:p\n9.9.9.2:1080:u:p\n9.9.9.3:1080:u:p",
	})
	if err != nil || !res.Success || res.Count != 3 {
		t.Fatalf("AddNodes(RandomPort) failed: %+v, err=%v", res, err)
	}
}

func TestModifyOutboundMappingPreviewAndBackupDiff(t *testing.T) {
	svc := NewServiceWithDataDir(t.TempDir())
	_ = svc.SaveVPS(VPS{Alias: "jp-1", Address: "2.3.4.5", Port: 22, Username: "root"})
	if _, err := svc.AddNodes(AddNodeRequest{
		VPSAlias:         "jp-1",
		GroupName:        "g1",
		InboundProtocol:  "vless",
		OutboundProtocol: "socks",
		StartPort:        21000,
		Count:            1,
		OutboundLines:    "198.51.100.10:1080:olduser:oldpass",
	}); err != nil {
		t.Fatal(err)
	}

	// Test "oldIP => newOutbound" cross-IP preview & apply
	preview, err := svc.PreviewModifyNodeOutbounds("jp-1", nil, "198.51.100.10 => 203.0.113.20:2080:newuser:newpass", "socks")
	if err != nil || !preview.Success || len(preview.Changes) != 1 {
		t.Fatalf("PreviewModifyNodeOutbounds failed: %+v err=%v", preview, err)
	}
	if !strings.Contains(preview.Changes[0].NewOutbound, "203.0.113.20:2080") {
		t.Fatalf("expected mapped new outbound, got %+v", preview.Changes[0])
	}

	bk, err := svc.BackupNow("jp-1")
	if err != nil {
		t.Fatal(err)
	}
	modRes, err := svc.ModifyNodeOutbounds("jp-1", nil, "198.51.100.10 => 203.0.113.20:2080:newuser:newpass", "socks")
	if err != nil || !modRes.Success || modRes.Count != 1 {
		t.Fatalf("ModifyNodeOutbounds mapping failed: %+v err=%v", modRes, err)
	}

	diff, err := svc.PreviewBackupDiff("jp-1", bk.Path)
	if err != nil || !diff.Success || len(diff.Modified) != 1 {
		t.Fatalf("expected 1 modified node in backup diff, got %+v err=%v", diff, err)
	}
}

func TestPruneOldBackupsKeepsLatest30(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 35; i++ {
		p := filepath.Join(dir, fmt.Sprintf("~vps_20260101_0000%02d_all.json", i))
		if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	pruneOldBackups(dir, 30)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 30 {
		t.Fatalf("expected 30 backups retained after prune, got %d", len(entries))
	}
}
