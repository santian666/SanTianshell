package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBuildAllJSONUsesBoundedWarningLog(t *testing.T) {
	var config map[string]any
	if err := json.Unmarshal(buildAllJSON(nil), &config); err != nil {
		t.Fatal(err)
	}
	logConfig, ok := config["log"].(map[string]any)
	if !ok || logConfig["disabled"] != false || logConfig["level"] != "warn" {
		t.Fatalf("all.json should only retain warning logs: %#v", config["log"])
	}
	if logConfig["output"] != "/var/log/sing-box/sing-box.log" {
		t.Fatalf("all.json should use the managed log file: %#v", logConfig)
	}
}

func TestReadAllJSONBuildsPortableRuntimeConfig(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "AK-日本"
	config := map[string]any{
		"log":       map[string]any{"disabled": true},
		"inbounds":  []any{map[string]any{"type": "vless", "tag": "node-a", "listen": "::", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "node-a-out"}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{"node-a"}, "outbound": "node-a-out"}}},
		"share":     map[string]any{"link": "local-only"},
		"metadata":  map[string]any{"created_at": "local-only"},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node-a.json": config}); err != nil {
		t.Fatal(err)
	}
	content, err := service.ReadAllJSON(alias)
	if err != nil {
		t.Fatal(err)
	}
	var all map[string]any
	if err := json.Unmarshal([]byte(content), &all); err != nil {
		t.Fatal(err)
	}
	if got := len(all["inbounds"].([]any)); got != 1 {
		t.Fatalf("inbounds 数量 = %d", got)
	}
	if got := len(all["outbounds"].([]any)); got != 1 {
		t.Fatalf("outbounds 数量 = %d", got)
	}
	route := all["route"].(map[string]any)
	if got := len(route["rules"].([]any)); got != 1 {
		t.Fatalf("route.rules 数量 = %d", got)
	}
	if strings.Contains(content, "\"share\"") || strings.Contains(content, "\"metadata\"") {
		t.Fatalf("迁移 all.json 不应包含本地 share/metadata: %s", content)
	}
}

func TestResolvedNodeGroupNameUsesOutboundOrVPSFallback(t *testing.T) {
	if got := resolvedNodeGroupName("", map[string]any{"server": "212.114.47.46"}, "156.239.14.106"); got != "212.114.47.46" {
		t.Fatalf("empty prefix should use outbound server, got %q", got)
	}
	if got := resolvedNodeGroupName("8G", map[string]any{"server": "212.114.47.46"}, "156.239.14.106"); got != "8G-212.114.47.46" {
		t.Fatalf("prefix should be joined with outbound server, got %q", got)
	}
	if got := resolvedNodeGroupName("", map[string]any{"type": "direct"}, "156.239.14.106"); got != "156.239.14.106" {
		t.Fatalf("direct outbound should use VPS address fallback, got %q", got)
	}
	if got := resolvedNodeGroupName("8G", map[string]any{"type": "direct"}, "156.239.14.106"); got != "8G-156.239.14.106" {
		t.Fatalf("prefix with direct outbound should use VPS address fallback, got %q", got)
	}
}

func TestExtractGroupFromTagAndGroupName(t *testing.T) {
	cases := []struct {
		tag       string
		createdAt string
		want      string
	}{
		{"TC--1", "", "TC"},
		{"TC--2", "", "TC"},
		{"TC--13", "", "TC"},
		{"qline.wg3-202.160.82.90", "", "qline.wg3"},
		{"qline.wg3-202.160.87.103", "", "qline.wg3"},
		{"8G-212.114.47.46", "", "8G"},
		{"HK-01", "", "HK"},
		{"HK-02", "", "HK"},
		{"US_01", "", "US"},
		{"HY-1", "", "HY"},
		{"香港专线-155.117.96.50", "", "香港专线"},
		{"155.117.96.50", "2026-11-15T00:00:00Z", "2026-11-15T00:00:00Z"},
		{"155.117.96.50-vless", "2026-11-15T00:00:00Z", "2026-11-15T00:00:00Z"},
		{"standalone", "", "standalone"},
	}
	for _, c := range cases {
		got := extractGroupFromTag(c.tag, c.createdAt)
		if got != c.want {
			t.Errorf("extractGroupFromTag(%q, %q) = %q, want %q", c.tag, c.createdAt, got, c.want)
		}
	}

	// Test metadata group_name override
	cfgWithMeta := map[string]any{
		"inbounds": []any{map[string]any{"tag": "TC--1"}},
		"metadata": map[string]any{"group_name": "CustomGroup"},
	}
	if got := extractGroupName(cfgWithMeta); got != "CustomGroup" {
		t.Fatalf("extractGroupName should respect metadata group_name, got %q", got)
	}
}

func TestDeleteNodeGroupDeletesAllNodesInGroup(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "HY"
	configs := map[string]map[string]any{
		"tc--1.json": {
			"inbounds": []any{map[string]any{"tag": "TC--1", "type": "vless", "listen_port": 20025}},
		},
		"tc--2.json": {
			"inbounds": []any{map[string]any{"tag": "TC--2", "type": "vless", "listen_port": 20026}},
		},
		"tc--3.json": {
			"inbounds": []any{map[string]any{"tag": "TC--3", "type": "vless", "listen_port": 20027}},
		},
		"hk--1.json": {
			"inbounds": []any{map[string]any{"tag": "HK--1", "type": "vless", "listen_port": 20028}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	// 1. Delete group by passing group name "TC"
	res, err := service.DeleteNodeGroup(alias, "TC")
	if err != nil {
		t.Fatal(err)
	}
	if res.Count != 3 {
		t.Fatalf("expected 3 nodes deleted, got %d", res.Count)
	}
	remaining, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(remaining) != 1 || remaining["hk--1.json"] == nil {
		t.Fatalf("expected only hk--1.json remaining, got %v", remaining)
	}

	// 2. Add TC nodes back and test deleting by passing tag "TC--1" (resolves to TC group)
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}
	res2, err := service.DeleteNodeGroup(alias, "TC--1")
	if err != nil {
		t.Fatal(err)
	}
	if res2.Count != 3 {
		t.Fatalf("passing node tag should resolve to group and delete 3 nodes, got %d", res2.Count)
	}
}

func TestExportSelectedNodesExcelBytes(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "HY"
	configs := map[string]map[string]any{
		"tc--1.json": {
			"inbounds": []any{map[string]any{"tag": "TC--1", "type": "vless", "listen_port": 20025}},
			"outbounds": []any{map[string]any{"server": "1.2.3.4", "server_port": 1080, "type": "socks"}},
		},
		"tc--2.json": {
			"inbounds": []any{map[string]any{"tag": "TC--2", "type": "vless", "listen_port": 20026}},
			"outbounds": []any{map[string]any{"server": "1.2.3.5", "server_port": 1080, "type": "socks"}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	req := ExportSelectedNodesRequest{
		Nodes: []SelectedNodeExportTarget{
			{VPSAlias: "HY", FileName: "tc--1.json"},
			{VPSAlias: "HY", FileName: "tc--2.json"},
		},
	}
	exportBytes, err := service.ExportSelectedNodesExcelBytes(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(exportBytes.Data) == 0 {
		t.Fatal("expected non-empty excel data")
	}
	if !strings.HasSuffix(exportBytes.FileName, ".xlsx") {
		t.Fatalf("expected .xlsx suffix, got %s", exportBytes.FileName)
	}
}

func TestRollbackDeletedNodesRestoresLocalConfig(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "test"
	filename := "node.json"
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{filename: config}); err != nil {
		t.Fatal(err)
	}
	deleted, err := service.DeleteNode(alias, filename)
	if err != nil || !deleted.Success {
		t.Fatalf("delete failed: result=%+v err=%v", deleted, err)
	}
	configs, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configs[filename]; exists {
		t.Fatalf("expected node to be deleted before rollback")
	}
	restored, err := service.RollbackDeletedNodes(alias, []string{filename})
	if err != nil || !restored.Success {
		t.Fatalf("rollback failed: result=%+v err=%v", restored, err)
	}
	configs, err = service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := configs[filename]; !exists {
		t.Fatalf("expected node to be restored")
	}
}

func TestSaveVPSRenamesNodeConfigAlias(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	oldAlias := "SG1/24"
	newAlias := "BAGE-SG"
	vps := VPS{ID: oldAlias, Alias: oldAlias, Address: "85.149.212.139", Port: 22, Username: "root"}
	if err := service.SaveVPS(vps); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(oldAlias, map[string]map[string]any{"node.json": config}); err != nil {
		t.Fatal(err)
	}
	vps.OriginalAlias = oldAlias
	vps.ID = oldAlias
	vps.Alias = newAlias
	if err := service.SaveVPS(vps); err != nil {
		t.Fatal(err)
	}
	list, err := service.loadVPSList()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].Alias != newAlias || list[0].ID != newAlias {
		t.Fatalf("expected one renamed VPS, got %+v", list)
	}
	oldConfigs, err := service.loadNodeConfigMap(oldAlias)
	if err != nil {
		t.Fatal(err)
	}
	if len(oldConfigs) != 0 {
		t.Fatalf("old alias should not keep nodes: %+v", oldConfigs)
	}
	newConfigs, err := service.loadNodeConfigMap(newAlias)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := newConfigs["node.json"]; !ok {
		t.Fatalf("new alias should contain renamed node config: %+v", newConfigs)
	}
}

func TestGetOverviewDoesNotLookupCountryOnStartup(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	if err := writeJSON(service.sshPath(), []VPS{{ID: "slow-country", Alias: "slow-country", Address: "203.0.113.10", Port: 22, Username: "root"}}); err != nil {
		t.Fatal(err)
	}
	overview, err := service.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.VPSList) != 1 {
		t.Fatalf("expected one VPS, got %+v", overview.VPSList)
	}
	if overview.VPSList[0].CountryCode != "--" {
		t.Fatalf("startup overview should use local country code only, got %q", overview.VPSList[0].CountryCode)
	}
}

func TestUncreatedMultiIPsFiltersExistingBoundIPs(t *testing.T) {
	configs := map[string]map[string]any{
		"one.json": generateConfigWithTag("85.149.212.1", "vless", "85.149.212.1", 20000, randomUUID(), "direct", directOutboundForIP("85.149.212.1"), true, false, ""),
		"two.json": generateConfigWithTag("prefix-85.149.212.2", "vless", "85.149.212.2", 20001, randomUUID(), "direct", directOutboundForIP("85.149.212.2"), true, false, ""),
	}
	got := uncreatedMultiIPs([]string{"85.149.212.1", "85.149.212.2", "85.149.212.3"}, configs)
	if len(got) != 1 || got[0] != "85.149.212.3" {
		t.Fatalf("expected only uncreated IP, got %+v", got)
	}
	if start := firstFreePort(configs, len(got), 20000); start != 20002 {
		t.Fatalf("expected next port 20002, got %d", start)
	}
}

func TestNodeCacheInvalidatedAfterSave(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "cache-test"
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node.json": config}); err != nil {
		t.Fatal(err)
	}
	nodes, err := service.GetNodes(alias)
	if err != nil || len(nodes) != 1 || nodes[0].ListenPort != 20000 {
		t.Fatalf("unexpected cached nodes: nodes=%+v err=%v", nodes, err)
	}
	config["inbounds"].([]any)[0].(map[string]any)["listen_port"] = 20001
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node.json": config}); err != nil {
		t.Fatal(err)
	}
	nodes, err = service.GetNodes(alias)
	if err != nil || len(nodes) != 1 || nodes[0].ListenPort != 20001 {
		t.Fatalf("node cache was not invalidated: nodes=%+v err=%v", nodes, err)
	}
}

func TestBackupPathRejectsSiblingDirectory(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	backupDir := filepath.Join(service.dataDir, "bk")
	if err := os.MkdirAll(backupDir, 0755); err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(backupDir, "test.json")
	if err := os.WriteFile(inside, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadBackup(inside); err != nil {
		t.Fatalf("valid backup path rejected: %v", err)
	}
	siblingDir := filepath.Join(service.dataDir, "bk-other")
	if err := os.MkdirAll(siblingDir, 0755); err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(siblingDir, "test.json")
	if err := os.WriteFile(sibling, []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ReadBackup(sibling); err == nil {
		t.Fatal("sibling directory path should be rejected")
	}
}

func TestBackupNowSupportsAliasWithPathSeparator(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "SG2/24"
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node.json": config}); err != nil {
		t.Fatal(err)
	}
	backup, err := service.BackupNow(alias)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(backup.Path) != filepath.Join(service.dataDir, "bk", safeVPSDirName(alias)) {
		t.Fatalf("backup escaped its directory: %s", backup.Path)
	}
	if backup.SourceAlias != alias || backup.Name[:len(alias)] != alias {
		t.Fatalf("unexpected backup metadata: %#v", backup)
	}
	files, err := service.GetBackups(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].SourceAlias != alias || files[0].Path != backup.Path {
		t.Fatalf("backup was not listed for alias: %#v", files)
	}
	if _, err := service.ReadBackup(backup.Path); err != nil {
		t.Fatalf("backup cannot be read: %v", err)
	}
}

func TestBackupAllJSONCreatesPortableBackupListedForRestore(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "BAGE-SG"
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen": "::", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{"node"}, "outbound": "node-out"}}},
		"share":     map[string]any{"link": "local-only"},
		"metadata":  map[string]any{"created_at": "local-only"},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node.json": config}); err != nil {
		t.Fatal(err)
	}
	backup, err := service.BackupAllJSON(alias)
	if err != nil {
		t.Fatal(err)
	}
	content, err := service.ReadBackup(backup.Path)
	if err != nil {
		t.Fatal(err)
	}
	configs, err := parseNodeConfigText(content)
	if err != nil {
		t.Fatalf("生成的 all.json 不能作为还原输入: %v", err)
	}
	if err := validateNodeConfigMap(configs); err != nil {
		t.Fatalf("生成的 all.json 节点配置无效: %v", err)
	}
	if strings.Contains(content, "\"share\"") || strings.Contains(content, "\"metadata\"") {
		t.Fatalf("迁移备份不应包含本地辅助字段: %s", content)
	}
	backups, err := service.GetBackups(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 1 || backups[0].Path != backup.Path {
		t.Fatalf("生成的 all.json 未显示在备份列表: %#v", backups)
	}
}

func TestBackupNowAndRestoreJSONUseIdenticalPortableContent(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "一致性测试"
	config := map[string]any{
		"log":       map[string]any{"disabled": true},
		"inbounds":  []any{map[string]any{"type": "vless", "tag": "node-a", "listen": "::", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"type": "direct", "tag": "node-a-out"}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{"node-a"}, "outbound": "node-a-out"}}},
		"share":     map[string]any{"link": "local-only"},
		"metadata":  map[string]any{"created_at": "local-only"},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node-a.json": config}); err != nil {
		t.Fatal(err)
	}

	backup, err := service.BackupNow(alias)
	if err != nil {
		t.Fatal(err)
	}
	restoreJSON, err := service.BackupAllJSON(alias)
	if err != nil {
		t.Fatal(err)
	}
	backupContent, err := service.ReadBackup(backup.Path)
	if err != nil {
		t.Fatal(err)
	}
	restoreContent, err := service.ReadBackup(restoreJSON.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(backupContent), []byte(restoreContent)) {
		t.Fatalf("立即备份和还原 JSON 内容不一致:\nbackup=%s\nrestore=%s", backupContent, restoreContent)
	}
	readContent, err := service.ReadAllJSON(alias)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal([]byte(backupContent), []byte(readContent)) {
		t.Fatalf("在线查看 JSON 与备份内容不一致:\nbackup=%s\nread=%s", backupContent, readContent)
	}
}

func TestParseNodeConfigTextKeepsLegacyBackupCompatibility(t *testing.T) {
	legacy := map[string]map[string]any{
		"node-a.json": {
			"inbounds":  []any{map[string]any{"type": "vless", "tag": "node-a", "listen_port": 20000}},
			"outbounds": []any{map[string]any{"type": "direct", "tag": "node-a-out"}},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseNodeConfigText(string(data))
	if err != nil {
		t.Fatalf("旧版节点映射备份不应失效: %v", err)
	}
	if err := validateNodeConfigMap(parsed); err != nil {
		t.Fatalf("旧版节点映射备份解析后应保持可部署: %v", err)
	}
	if _, ok := parsed["node-a.json"]; !ok {
		t.Fatalf("旧版备份节点文件名未保留: %#v", parsed)
	}
}

func TestBackupSnapshotsAvoidSameSecondOverwrite(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "same-second"
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node.json": config}); err != nil {
		t.Fatal(err)
	}
	first, err := service.BackupNow(alias)
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.BackupAllJSON(alias)
	if err != nil {
		t.Fatal(err)
	}
	if first.Path == second.Path {
		t.Fatalf("连续备份不应覆盖同一文件: %s", first.Path)
	}
	backups, err := service.GetBackups(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 2 {
		t.Fatalf("连续备份应保留两个文件，得到 %d: %#v", len(backups), backups)
	}
}

func TestCalculateExpireDateDoesNotTreatIPAddressAsDate(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, 9, 4, 14, 30, 0, 0, location)
	createdAt := "2026-09-04T06:14:40.0187098Z"

	for _, tag := range []string{"187.12.22.115", "187.1.21.53", "177.9.3.246"} {
		if got := calculateExpireDateAt(tag, createdAt, now); got != "2026-10-04" {
			t.Fatalf("IP 节点 %q 的初次到期日期 = %q，期望 %q", tag, got, "2026-10-04")
		}
	}
}

func TestCalculateExpireDateKeepsExplicitDatePrefix(t *testing.T) {
	location := time.FixedZone("Asia/Shanghai", 8*60*60)
	now := time.Date(2026, 9, 4, 14, 30, 0, 0, location)
	if got := calculateExpireDateAt("12.22-SG节点", "", now); got != "2027-01-21" {
		t.Fatalf("明确日期前缀的初次到期日期 = %q，期望 %q", got, "2027-01-21")
	}
}

func TestConcurrentNodeConfigSavesKeepAllAliases(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	const count = 12
	var wg sync.WaitGroup
	errorsCh := make(chan error, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			alias := string(rune('A' + index))
			config := map[string]any{
				"inbounds":  []any{map[string]any{"tag": alias, "type": "vless", "listen_port": 20000 + index}},
				"outbounds": []any{map[string]any{"tag": alias + "-out", "type": "direct"}},
			}
			errorsCh <- service.saveNodeConfigMap(alias, map[string]map[string]any{alias + ".json": config})
		}(i)
	}
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	nodes, err := service.GetNodes("")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != count {
		t.Fatalf("concurrent saves lost data: got %d nodes, want %d", len(nodes), count)
	}
}

func TestConcurrentAddNodesSameAliasKeepsEveryNode(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	alias := "same-vps"
	if err := writeJSON(service.sshPath(), []VPS{{ID: alias, Alias: alias, Address: "192.0.2.10", Port: 22, Username: "root"}}); err != nil {
		t.Fatal(err)
	}

	const count = 24
	start := make(chan struct{})
	errorsCh := make(chan error, count)
	var wg sync.WaitGroup
	for index := 0; index < count; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			result, err := service.AddNodes(AddNodeRequest{
				VPSAlias:         alias,
				GroupName:        fmt.Sprintf("node-%02d", index),
				InboundProtocol:  "vless",
				OutboundProtocol: "direct",
				StartPort:        20000 + index,
				Count:            1,
			})
			if err != nil {
				errorsCh <- err
				return
			}
			if !result.Success {
				errorsCh <- fmt.Errorf("新增节点失败: %s", result.Message)
			}
		}(index)
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		t.Fatal(err)
	}

	configs, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != count {
		t.Fatalf("同一 VPS 并发新增后节点数 = %d，期望 %d", len(configs), count)
	}
	ports := make(map[int]bool, count)
	for _, config := range configs {
		ports[extractListenPort(config)] = true
	}
	for index := 0; index < count; index++ {
		port := 20000 + index
		if !ports[port] {
			t.Fatalf("同一 VPS 并发新增后缺少端口 %d", port)
		}
	}
}

func TestDeleteNodeKeepsConfigWhenDeletedRecordCannotBeSaved(t *testing.T) {
	dataDir := t.TempDir()
	service := &Service{dataDir: dataDir}
	alias := "delete-safe"
	filename := "node.json"
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "node-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{filename: config}); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dataDir, "sb_admin.db"), 0700); err != nil {
		t.Fatal(err)
	}
	result, err := service.DeleteNode(alias, filename)
	if err == nil || result.Success {
		t.Fatalf("expected deleted-record failure, result=%+v err=%v", result, err)
	}
	configs, loadErr := service.loadNodeConfigMap(alias)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, exists := configs[filename]; !exists {
		t.Fatal("node must remain when deleted record cannot be saved")
	}
}

func TestSaveNodeConfigMapRejectsDamagedJSONWithoutOverwrite(t *testing.T) {
	dataDir := t.TempDir()
	service := &Service{dataDir: dataDir}
	path := filepath.Join(dataDir, "sbconfig.json")
	original := []byte("{damaged")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	err := service.saveNodeConfigMap("new-alias", map[string]map[string]any{"node.json": {}})
	if err == nil || !strings.Contains(err.Error(), "读取 sbconfig.json 失败") {
		t.Fatalf("expected damaged JSON error, got %v", err)
	}
	content, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != string(original) {
		t.Fatalf("damaged source file was overwritten: %q", content)
	}
	original = []byte("null")
	if err := os.WriteFile(path, original, 0644); err != nil {
		t.Fatal(err)
	}
	err = service.saveNodeConfigMap("new-alias", map[string]map[string]any{"node.json": {}})
	if err == nil || !strings.Contains(err.Error(), "根对象无效") {
		t.Fatalf("expected invalid root error, got %v", err)
	}
	content, readErr = os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(content) != string(original) {
		t.Fatalf("invalid root file was overwritten: %q", content)
	}
}

func TestWriteJSONReplacesFileWithoutTempArtifacts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sbconfig.json")
	if err := os.WriteFile(path, []byte(`{"old":true}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := writeJSON(path, map[string]any{"new": true}); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &value); err != nil || value["new"] != true {
		t.Fatalf("replacement is invalid: value=%v err=%v", value, err)
	}
	temps, err := filepath.Glob(filepath.Join(dir, ".sbconfig.json.tmp-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temps) != 0 {
		t.Fatalf("temporary files were not cleaned: %v", temps)
	}
}

func TestRestoreDeletedRecordRejectsExistingNodeAndKeepsRecord(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "restore-conflict"
	filename := "node.json"
	deletedConfig := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "old", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "old-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{filename: deletedConfig}); err != nil {
		t.Fatal(err)
	}
	if result, err := service.DeleteNode(alias, filename); err != nil || !result.Success {
		t.Fatalf("delete failed: result=%+v err=%v", result, err)
	}
	records, err := service.GetDeletedRecords()
	if err != nil || len(records) != 1 {
		t.Fatalf("unexpected deleted records: records=%+v err=%v", records, err)
	}
	liveConfig := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "live", "type": "vless", "listen_port": 30000}},
		"outbounds": []any{map[string]any{"tag": "live-out", "type": "direct"}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{filename: liveConfig}); err != nil {
		t.Fatal(err)
	}
	rollback, err := service.RollbackDeletedNodes(alias, []string{filename})
	if err != nil || rollback.Success || !strings.Contains(rollback.Message, "同名节点已存在") {
		t.Fatalf("expected rollback conflict, result=%+v err=%v", rollback, err)
	}
	result, err := service.RestoreDeletedRecord(records[0].ID)
	if err != nil || result.Success || !strings.Contains(result.Message, "同名节点已存在") {
		t.Fatalf("expected restore conflict, result=%+v err=%v", result, err)
	}
	configs, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if extractListenPort(configs[filename]) != 30000 {
		t.Fatalf("live node was overwritten: %#v", configs[filename])
	}
	records, err = service.GetDeletedRecords()
	if err != nil || len(records) != 1 {
		t.Fatalf("deleted record must be retained: records=%+v err=%v", records, err)
	}
}

func TestDeleteDeletedRecordsRollsBackOnSQLError(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	db, err := service.openDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureRecordTables(db); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	first, err := db.Exec(`insert into deleted_records(filename,config_json) values('ok.json','{}')`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	second, err := db.Exec(`insert into deleted_records(filename,config_json) values('blocked.json','{}')`)
	if err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`create trigger block_deleted_record before delete on deleted_records when old.filename='blocked.json' begin select raise(abort,'blocked'); end`); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	firstID, _ := first.LastInsertId()
	secondID, _ := second.LastInsertId()
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	result, err := service.DeleteDeletedRecords([]int{int(firstID), int(secondID)}, "确认删除")
	if err == nil || result.Success {
		t.Fatalf("expected SQL error, result=%+v err=%v", result, err)
	}
	db, err = service.openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`select count(*) from deleted_records where id in (?,?)`, firstID, secondID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("transaction did not roll back, remaining=%d", count)
	}
}

func TestAutoDeleteSchedulerStopsWithContext(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	done := make(chan struct{})
	go func() {
		service.autoDeleteScheduler(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("自动删除调度器未随应用上下文停止")
	}
}

func TestGetNodesReturnsDamagedConfigError(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	path := filepath.Join(service.dataDir, "sbconfig.json")
	if err := os.WriteFile(path, []byte("{damaged"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := service.GetNodes(""); err == nil {
		t.Fatal("损坏的节点配置不应被当作空列表")
	}
}

func TestForgetHostLocalDataRemovesDisplayOnly(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	alias := "AK-日本"
	hostID := "host-ak-jp"
	if err := service.SaveVPS(VPS{HostID: hostID, Alias: alias, Address: "192.0.2.10", Port: 22, Username: "root"}); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "AK-日本-1", "type": "vless", "listen_port": 20000}},
		"outbounds": []any{map[string]any{"tag": "AK-日本-1-out", "type": "direct"}},
		"route":     map[string]any{"rules": []any{map[string]any{"inbound": []any{"AK-日本-1"}, "outbound": "AK-日本-1-out"}}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"ak-jp-1.json": config}); err != nil {
		t.Fatal(err)
	}
	db, err := service.openDB()
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureFeatureTables(db); err != nil {
		db.Close()
		t.Fatal(err)
	}
	var recycledBefore int
	if err := db.QueryRow(`select count(*) from deleted_records`).Scan(&recycledBefore); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()

	if err := service.ForgetHostLocalData(hostID, alias); err != nil {
		t.Fatal(err)
	}
	overview, err := service.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.VPSList) != 0 || overview.Stats.NodeCount != 0 {
		t.Fatalf("删除主机后仍显示 VPS 或节点: %#v", overview)
	}
	db, err = service.openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var recycledAfter int
	if err := db.QueryRow(`select count(*) from deleted_records`).Scan(&recycledAfter); err != nil {
		t.Fatal(err)
	}
	if recycledAfter != recycledBefore {
		t.Fatalf("清理本地节点展示不应写入节点回收站: before=%d after=%d", recycledBefore, recycledAfter)
	}
}

func TestForgetHostLocalDataAlsoMatchesLegacyAlias(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	alias := "旧版节点"
	if err := service.SaveVPS(VPS{Alias: alias, Address: "192.0.2.20", Port: 22, Username: "root"}); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"inbounds": []any{map[string]any{"tag": "legacy-1", "type": "vless", "listen_port": 20000}},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"legacy.json": config}); err != nil {
		t.Fatal(err)
	}
	if err := service.ForgetHostLocalData("new-host-id", alias); err != nil {
		t.Fatal(err)
	}
	overview, err := service.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.VPSList) != 0 || overview.Stats.NodeCount != 0 {
		t.Fatalf("旧版无主机 ID 的节点绑定未按别名清理: %#v", overview)
	}
}

func TestReconcileHostLocalDataRemovesDeletedHosts(t *testing.T) {
	service := NewServiceWithDataDir(t.TempDir())
	for _, item := range []VPS{
		{HostID: "active-host", Alias: "保留主机", Address: "192.0.2.30", Port: 22, Username: "root"},
		{HostID: "deleted-host", Alias: "删除主机", Address: "192.0.2.31", Port: 22, Username: "root"},
		{Alias: "旧版残留", Address: "192.0.2.32", Port: 22, Username: "root"},
	} {
		if err := service.SaveVPS(item); err != nil {
			t.Fatal(err)
		}
		config := map[string]any{
			"inbounds": []any{map[string]any{"tag": item.Alias, "type": "vless", "listen_port": 20000}},
		}
		if err := service.saveNodeConfigMap(item.Alias, map[string]map[string]any{item.Alias + ".json": config}); err != nil {
			t.Fatal(err)
		}
	}
	if err := service.ReconcileHostLocalData([]string{"active-host"}, []string{"保留主机"}); err != nil {
		t.Fatal(err)
	}
	overview, err := service.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.VPSList) != 1 || overview.VPSList[0].Alias != "保留主机" || overview.Stats.NodeCount != 1 {
		t.Fatalf("启动对账结果错误: %#v", overview)
	}
	if err := service.ReconcileHostLocalData(nil, nil); err != nil {
		t.Fatal(err)
	}
	overview, err = service.GetOverview()
	if err != nil {
		t.Fatal(err)
	}
	if len(overview.VPSList) != 0 || overview.Stats.NodeCount != 0 {
		t.Fatalf("没有活动主机时仍残留节点展示: %#v", overview)
	}
}

func TestBackupPerServerIsolationAndRestoreSecurity(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	aliasA := "VPS-HongKong"
	aliasB := "VPS-Tokyo"

	configA := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-hk", "type": "vless", "listen_port": 20001}},
		"outbounds": []any{map[string]any{"tag": "node-hk-out", "type": "direct"}},
	}
	configB := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-ty", "type": "vless", "listen_port": 20002}},
		"outbounds": []any{map[string]any{"tag": "node-ty-out", "type": "direct"}},
	}

	if err := service.saveNodeConfigMap(aliasA, map[string]map[string]any{"hk.json": configA}); err != nil {
		t.Fatal(err)
	}
	if err := service.saveNodeConfigMap(aliasB, map[string]map[string]any{"ty.json": configB}); err != nil {
		t.Fatal(err)
	}

	backupA, err := service.BackupNow(aliasA)
	if err != nil {
		t.Fatal(err)
	}
	backupB, err := service.BackupNow(aliasB)
	if err != nil {
		t.Fatal(err)
	}

	// 1. 验证物理目录隔离
	dirA := filepath.Dir(backupA.Path)
	dirB := filepath.Dir(backupB.Path)
	if dirA == dirB {
		t.Fatalf("不同 VPS 的备份文件未进行目录物理隔离: dirA=%s, dirB=%s", dirA, dirB)
	}
	if filepath.Base(dirA) != safeVPSDirName(aliasA) {
		t.Fatalf("VPS-A 目录命名不符合预期: %s", dirA)
	}
	if filepath.Base(dirB) != safeVPSDirName(aliasB) {
		t.Fatalf("VPS-B 目录命名不符合预期: %s", dirB)
	}

	// 2. 验证查询隔离
	listA, err := service.GetBackups(aliasA)
	if err != nil {
		t.Fatal(err)
	}
	if len(listA) != 1 || listA[0].SourceAlias != aliasA {
		t.Fatalf("查询 VPS-A 包含了非 VPS-A 备份: %#v", listA)
	}

	listB, err := service.GetBackups(aliasB)
	if err != nil {
		t.Fatal(err)
	}
	if len(listB) != 1 || listB[0].SourceAlias != aliasB {
		t.Fatalf("查询 VPS-B 包含了非 VPS-B 备份: %#v", listB)
	}

	allBackups, err := service.GetBackups("")
	if err != nil {
		t.Fatal(err)
	}
	if len(allBackups) != 2 {
		t.Fatalf("全量查询应返回两个备份，得到: %d", len(allBackups))
	}

	// 3. 验证安全硬拦截：尝试将 VPS-B 的备份还原到 VPS-A，必须被拦截拒绝
	crossRestoreResult := service.RestoreBackup(aliasA, backupB.Path)
	if crossRestoreResult.Success {
		t.Fatalf("跨服务器还原操作未被安全拦截！result=%+v", crossRestoreResult)
	}
	if !strings.Contains(crossRestoreResult.Message, "安全拦截") || !strings.Contains(crossRestoreResult.Message, aliasB) {
		t.Fatalf("拦截提示信息未明确指出来源服务器: %s", crossRestoreResult.Message)
	}

	// 4. 验证通过 MigrateBackup 跨服务器将 VPS-B 的备份还原部署到 VPS-A（允许跨服务器，不会被拦截）
	migrateResult := service.MigrateBackup(aliasA, backupB.Path)
	if strings.Contains(migrateResult.Message, "安全拦截") {
		t.Fatalf("MigrateBackup 不应该被跨服务器安全拦截: %s", migrateResult.Message)
	}
	if !strings.Contains(migrateResult.Message, "未连接") && !strings.Contains(migrateResult.Message, "失败") {
		t.Fatalf("预期未连接/部署失败，实际得到: %s", migrateResult.Message)
	}
}

func TestRollbackLocalNodes(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-TestRollback"

	config1 := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-1", "type": "vless", "listen_port": 20001}},
		"outbounds": []any{map[string]any{"tag": "out-1", "type": "direct"}},
	}
	config2 := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-2", "type": "vless", "listen_port": 20002}},
		"outbounds": []any{map[string]any{"tag": "out-2", "type": "direct"}},
	}

	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{
		"node-1.json": config1,
		"node-2.json": config2,
	}); err != nil {
		t.Fatal(err)
	}

	// 回滚 node-2（模拟发布失败时清除本地新增）
	res, err := service.RollbackLocalNodes(alias, []string{"node-2.json"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Success {
		t.Fatalf("回滚操作未成功: %+v", res)
	}

	after, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := after["node-2.json"]; ok {
		t.Fatalf("回滚后 node-2.json 仍残留在本地: %#v", after)
	}
	if _, ok := after["node-1.json"]; !ok {
		t.Fatalf("回滚误删除了无关的 node-1.json: %#v", after)
	}
}

func TestClearLocalNodesForVPS(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	aliasA := "VPS-A"
	aliasB := "VPS-B"

	configA := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-a", "type": "vless", "listen_port": 20001}},
		"outbounds": []any{map[string]any{"tag": "out-a", "type": "direct"}},
	}
	configB := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-b", "type": "vless", "listen_port": 20002}},
		"outbounds": []any{map[string]any{"tag": "out-b", "type": "direct"}},
	}

	if err := service.saveNodeConfigMap(aliasA, map[string]map[string]any{"a.json": configA}); err != nil {
		t.Fatal(err)
	}
	if err := service.saveNodeConfigMap(aliasB, map[string]map[string]any{"b.json": configB}); err != nil {
		t.Fatal(err)
	}

	deleted, err := service.clearLocalNodesForVPS(aliasA)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Fatalf("期望删除 1 个节点，实际返回: %d", deleted)
	}

	afterA, err := service.loadNodeConfigMap(aliasA)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterA) != 0 {
		t.Fatalf("VPS-A 节点未被清空: %#v", afterA)
	}

	afterB, err := service.loadNodeConfigMap(aliasB)
	if err != nil {
		t.Fatal(err)
	}
	if len(afterB) != 1 {
		t.Fatalf("VPS-B 节点被误删: %#v", afterB)
	}
}

func TestReconcileLocalNodesWithRemote(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-Reconcile"

	privKey, pubKey := realityKeyPair()
	config1 := map[string]any{
		"inbounds": []any{map[string]any{
			"tag":         "node-1",
			"type":        "vless",
			"listen_port": 20001,
			"tls": map[string]any{
				"reality": map[string]any{
					"private_key": privKey,
					"short_id":    []any{"abcd1234ef"},
				},
			},
		}},
		"outbounds": []any{map[string]any{"tag": "out-1", "type": "direct"}},
		"share": map[string]any{
			"reality_public_key":  pubKey,
			"reality_short_id":    "abcd1234ef",
			"reality_fingerprint": "chrome",
		},
		"metadata": map[string]any{
			"created_at": "2026-10-13T10:00:00.000000000Z",
			"group_name": "ReconcileGroup",
		},
	}
	config2 := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-2", "type": "vless", "listen_port": 20002}},
		"outbounds": []any{map[string]any{"tag": "out-2", "type": "direct"}},
	}
	config3 := map[string]any{
		"inbounds":  []any{map[string]any{"tag": "node-3", "type": "vless", "listen_port": 20003}},
		"outbounds": []any{map[string]any{"tag": "out-3", "type": "direct"}},
	}

	// 本地有 node-1, node-2, node-3
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{
		"node-1.json": config1,
		"node-2.json": config2,
		"node-3.json": config3,
	}); err != nil {
		t.Fatal(err)
	}

	// 远端服务器核验仅存在 node-1
	remoteConfigs := map[string]map[string]any{
		"node-1.json": {
			"inbounds": []any{map[string]any{
				"tag":         "node-1",
				"type":        "vless",
				"listen_port": 20001,
				"tls": map[string]any{
					"reality": map[string]any{
						"private_key": privKey,
						"short_id":    []any{"abcd1234ef"},
					},
				},
			}},
			"outbounds": []any{map[string]any{"tag": "out-1", "type": "direct"}},
		},
	}

	deletedCount, err := service.reconcileLocalNodesWithRemote(alias, remoteConfigs)
	if err != nil {
		t.Fatal(err)
	}
	if deletedCount != 2 {
		t.Fatalf("期望删除 2 个服务器不存在的幽灵节点，实际删除: %d", deletedCount)
	}

	after, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != 1 {
		t.Fatalf("期望本地保留 1 个节点，实际为: %d", len(after))
	}
	if _, ok := after["node-1.json"]; !ok {
		t.Fatalf("node-1.json 应被保留")
	}
	if _, ok := after["node-2.json"]; ok {
		t.Fatalf("node-2.json 应在本地被删除")
	}
	if _, ok := after["node-3.json"]; ok {
		t.Fatalf("node-3.json 应在本地被删除")
	}

	// 验证本地元数据 share 与 metadata 被成功保留与恢复
	shareMeta, _ := after["node-1.json"]["share"].(map[string]any)
	if shareMeta["reality_public_key"] != pubKey || shareMeta["reality_fingerprint"] != "chrome" {
		t.Fatalf("本地分享元数据未被正确恢复: %#v", shareMeta)
	}
	meta, _ := after["node-1.json"]["metadata"].(map[string]any)
	if meta["created_at"] != "2026-10-13T10:00:00.000000000Z" || meta["group_name"] != "ReconcileGroup" {
		t.Fatalf("本地节点元数据未被正确恢复: %#v", meta)
	}
}

func TestAddNodesFollowedByReconcileMaintainsOrder(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-OrderTest"

	// 准备一个较早创建但端口较大的老节点（端口 30000）
	oldConfig := map[string]any{
		"inbounds": []any{map[string]any{
			"tag":         "old-node",
			"type":        "vless",
			"listen_port": 30000,
		}},
		"outbounds": []any{map[string]any{"tag": "out-direct", "type": "direct"}},
		"metadata": map[string]any{
			"created_at": "2026-10-01T00:00:00.000000000Z",
			"group_name": "OldGroup",
		},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{
		"old-node.json": oldConfig,
	}); err != nil {
		t.Fatal(err)
	}

	// 模拟新增一个节点，端口较小（20000），创建时间为较新
	newConfig := map[string]any{
		"inbounds": []any{map[string]any{
			"tag":         "new-node",
			"type":        "vless",
			"listen_port": 20000,
		}},
		"outbounds": []any{map[string]any{"tag": "out-direct", "type": "direct"}},
		"metadata": map[string]any{
			"created_at": "2026-10-13T12:00:00.000000000Z",
			"group_name": "NewGroup",
		},
	}
	configs, _ := service.loadNodeConfigMap(alias)
	configs["new-node.json"] = newConfig
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	// 模拟远端 all.json 拆分出来的 remoteConfigs（不包含 metadata 和 share）
	remoteConfigs := map[string]map[string]any{
		"old-node.json": {
			"inbounds": []any{map[string]any{
				"tag":         "old-node",
				"type":        "vless",
				"listen_port": 30000,
			}},
			"outbounds": []any{map[string]any{"tag": "out-direct", "type": "direct"}},
		},
		"new-node.json": {
			"inbounds": []any{map[string]any{
				"tag":         "new-node",
				"type":        "vless",
				"listen_port": 20000,
			}},
			"outbounds": []any{map[string]any{"tag": "out-direct", "type": "direct"}},
		},
	}

	// 执行与远端对账同步（如 RefreshVPSState 内部行为）
	deletedCount, err := service.reconcileLocalNodesWithRemote(alias, remoteConfigs)
	if err != nil {
		t.Fatal(err)
	}
	if deletedCount != 0 {
		t.Fatalf("不应删除任何节点，但删除了: %d", deletedCount)
	}

	// 验证 GetNodes 排序规则：最新创建的 new-node 必须排在首位（即便其 listen_port 20000 小于 30000）
	nodes, err := service.GetNodes(alias)
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 2 {
		t.Fatalf("期望 2 个节点，实际: %d", len(nodes))
	}
	if nodes[0].Tag != "new-node" {
		t.Fatalf("最新节点未排在首位！首位节点为: %s (端口: %d, SortAt: %s)，第二位为: %s (端口: %d, SortAt: %s)",
			nodes[0].Tag, nodes[0].ListenPort, nodes[0].SortAt,
			nodes[1].Tag, nodes[1].ListenPort, nodes[1].SortAt)
	}
	if nodes[0].CreatedAt != "2026-10-13T12:00:00.000000000Z" {
		t.Fatalf("最新节点的 CreatedAt 丢失: %s", nodes[0].CreatedAt)
	}
	if nodes[1].Tag != "old-node" {
		t.Fatalf("老节点应排在第二位，实际为: %s", nodes[1].Tag)
	}
}

func TestReadManagedRemoteNodeConfigsEmpty(t *testing.T) {
	// 1. 空内容
	configs, path, err := readManagedRemoteNodeConfigs(func(p string) ([]byte, error) {
		return []byte(""), nil
	})
	if err != nil {
		t.Fatalf("空内容应返回空配置而非报错: %v", err)
	}
	if len(configs) != 0 || path != "/etc/sing-box/conf/all.json" {
		t.Fatalf("期望返回空配置，得到: %#v", configs)
	}

	// 2. 空 JSON
	configs2, path2, err2 := readManagedRemoteNodeConfigs(func(p string) ([]byte, error) {
		return []byte(`{"inbounds":[],"outbounds":[],"route":{"rules":[]}}`), nil
	})
	if err2 != nil {
		t.Fatalf("空 JSON 应返回空配置而非报错: %v", err2)
	}
	if len(configs2) != 0 || path2 != "/etc/sing-box/conf/all.json" {
		t.Fatalf("期望返回空配置，得到: %#v", configs2)
	}
}

func TestModifyNodeOutboundsReplacesExistingSocks5(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-1"
	config := map[string]any{
		"log": map[string]any{"disabled": true},
		"inbounds": []any{map[string]any{
			"type":        "vless",
			"tag":         "node-a",
			"listen":      "::",
			"listen_port": 20000,
			"users":       []any{map[string]any{"uuid": "test-uuid"}},
		}},
		"outbounds": []any{map[string]any{
			"type":        "socks",
			"tag":         "node-a-out",
			"server":      "core-residential.evomi.com",
			"server_port": 8080,
			"username":    "old-user",
			"password":    "old-pass",
			"network":     "tcp",
		}},
		"route": map[string]any{
			"rules": []any{map[string]any{"inbound": []any{"node-a"}, "outbound": "node-a-out"}},
		},
	}
	if err := service.saveNodeConfigMap(alias, map[string]map[string]any{"node-a.json": config}); err != nil {
		t.Fatal(err)
	}

	result, err := service.ModifyNodeOutbounds(alias, nil, "core-residential.evomi.com:1002:user:password", "")
	if err != nil {
		t.Fatalf("ModifyNodeOutbounds 发生错误: %v", err)
	}
	if !result.Success || result.Count != 1 {
		t.Fatalf("ModifyNodeOutbounds 结果不符合预期: %#v", result)
	}

	saved, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	nodeCfg := saved["node-a.json"]
	outbounds, _ := nodeCfg["outbounds"].([]any)
	if len(outbounds) != 1 {
		t.Fatalf("outbounds 长度应为 1, 得到: %d", len(outbounds))
	}
	outbound := outbounds[0].(map[string]any)
	if outbound["server"] != "core-residential.evomi.com" {
		t.Fatalf("server 未更新，期望 core-residential.evomi.com, 得到: %v", outbound["server"])
	}
	if asInt(outbound["server_port"]) != 1002 {
		t.Fatalf("server_port 未更新，期望 1002, 得到: %v", outbound["server_port"])
	}
	if outbound["username"] != "user" {
		t.Fatalf("username 未更新，期望 user, 得到: %v", outbound["username"])
	}
	if outbound["password"] != "password" {
		t.Fatalf("password 未更新，期望 password, 得到: %v", outbound["password"])
	}
	if outbound["type"] != "socks" {
		t.Fatalf("type 期望 socks, 得到: %v", outbound["type"])
	}
	if outbound["tag"] != "node-a-out" {
		t.Fatalf("tag 期望 node-a-out, 得到: %v", outbound["tag"])
	}

	// 确保入站端口和 UUID 未被修改
	inbounds, _ := nodeCfg["inbounds"].([]any)
	inbound := inbounds[0].(map[string]any)
	if asInt(inbound["listen_port"]) != 20000 {
		t.Fatalf("入站端口被意外修改: %v", inbound["listen_port"])
	}
}

func TestModifyNodeOutboundsReplacesExistingSocks5MatchingIP(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-1"
	configs := map[string]map[string]any{
		"node-1.json": {
			"inbounds":  []any{map[string]any{"tag": "n1", "listen_port": 20000, "users": []any{map[string]any{"uuid": "uuid-1"}}}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n1-out", "server": "1.1.1.1", "server_port": 1000, "username": "u1", "password": "p1"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n1-out"}}},
		},
		"node-2.json": {
			"inbounds":  []any{map[string]any{"tag": "n2", "listen_port": 20001, "users": []any{map[string]any{"uuid": "uuid-2"}}}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n2-out", "server": "2.2.2.2", "server_port": 2000, "username": "u2", "password": "p2"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n2-out"}}},
		},
		"node-3.json": {
			"inbounds":  []any{map[string]any{"tag": "n3", "listen_port": 20002, "users": []any{map[string]any{"uuid": "uuid-3"}}}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n3-out", "server": "3.3.3.3", "server_port": 3000, "username": "u3", "password": "p3"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n3-out"}}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	// 仅修改 2.2.2.2 这个 IP 的落地
	result, err := service.ModifyNodeOutbounds(alias, nil, "2.2.2.2:2999:u2_new:p2_new", "")
	if err != nil {
		t.Fatalf("ModifyNodeOutbounds 发生错误: %v", err)
	}
	if !result.Success || result.Count != 1 {
		t.Fatalf("应当只修改匹配 2.2.2.2 的 1 个节点: %#v", result)
	}

	saved, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}

	n1Out := saved["node-1.json"]["outbounds"].([]any)[0].(map[string]any)
	n2Out := saved["node-2.json"]["outbounds"].([]any)[0].(map[string]any)
	n3Out := saved["node-3.json"]["outbounds"].([]any)[0].(map[string]any)

	// node-1 和 node-3 的 IP 不匹配，绝对不应被修改
	if n1Out["server"] != "1.1.1.1" || asInt(n1Out["server_port"]) != 1000 || n1Out["password"] != "p1" {
		t.Fatalf("node-1 不匹配 2.2.2.2，被意外修改: %#v", n1Out)
	}
	if n3Out["server"] != "3.3.3.3" || asInt(n3Out["server_port"]) != 3000 || n3Out["password"] != "p3" {
		t.Fatalf("node-3 不匹配 2.2.2.2，被意外修改: %#v", n3Out)
	}

	// node-2 的 IP 100% 匹配，成功更新端口、用户名、密码
	if n2Out["server"] != "2.2.2.2" {
		t.Fatalf("node-2 server 应保持 2.2.2.2: %v", n2Out["server"])
	}
	if asInt(n2Out["server_port"]) != 2999 {
		t.Fatalf("node-2 端口未更新为 2999: %v", n2Out["server_port"])
	}
	if n2Out["username"] != "u2_new" || n2Out["password"] != "p2_new" {
		t.Fatalf("node-2 账密未更新: %#v", n2Out)
	}
	if n2Out["tag"] != "n2-out" {
		t.Fatalf("node-2 出站 tag 应当保持: %v", n2Out["tag"])
	}

	// 确保入站端口和 uuid 未被修改
	inbound2 := saved["node-2.json"]["inbounds"].([]any)[0].(map[string]any)
	if asInt(inbound2["listen_port"]) != 20001 {
		t.Fatalf("入站端口被意外修改: %v", inbound2["listen_port"])
	}
}

func TestModifyNodeOutboundsUnmatchedIPFailsWithoutChanging(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-Unmatched"
	configs := map[string]map[string]any{
		"node-1.json": {
			"inbounds":  []any{map[string]any{"tag": "n1", "listen_port": 20000}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n1-out", "server": "1.1.1.1", "server_port": 1000, "password": "p1"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n1-out"}}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	// 输入一个不存在于该 VPS 的 IP
	result, err := service.ModifyNodeOutbounds(alias, nil, "9.9.9.9:1080:u:p", "")
	if err != nil {
		t.Fatal(err)
	}
	if result.Success {
		t.Fatalf("未匹配到任何 IP 时不应报告成功: %#v", result)
	}
	if !strings.Contains(result.Message, "9.9.9.9") {
		t.Fatalf("提示信息应指出未匹配到的 IP: %s", result.Message)
	}

	// 确认 node-1 依然原封不动
	saved, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	n1Out := saved["node-1.json"]["outbounds"].([]any)[0].(map[string]any)
	if n1Out["server"] != "1.1.1.1" || n1Out["password"] != "p1" {
		t.Fatalf("未匹配时配置被篡改: %#v", n1Out)
	}
}

func TestModifyNodeOutboundsMultipleIPsMatching(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-MultiIP"
	configs := map[string]map[string]any{
		"node-1.json": {
			"inbounds":  []any{map[string]any{"tag": "n1", "listen_port": 20000}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n1-out", "server": "1.1.1.1", "server_port": 1000, "password": "old1"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n1-out"}}},
		},
		"node-2.json": {
			"inbounds":  []any{map[string]any{"tag": "n2", "listen_port": 20001}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n2-out", "server": "2.2.2.2", "server_port": 2000, "password": "old2"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n2-out"}}},
		},
		"node-3.json": {
			"inbounds":  []any{map[string]any{"tag": "n3", "listen_port": 20002}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n3-out", "server": "3.3.3.3", "server_port": 3000, "password": "old3"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n3-out"}}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	input := "1.1.1.1:1111:u1:new1\n3.3.3.3:3333:u3:new3\n9.9.9.9:9999:u9:new9"
	result, err := service.ModifyNodeOutbounds(alias, nil, input, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Count != 2 {
		t.Fatalf("应成功匹配修改 2 个节点, 得到: %#v", result)
	}

	saved, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	n1Out := saved["node-1.json"]["outbounds"].([]any)[0].(map[string]any)
	n2Out := saved["node-2.json"]["outbounds"].([]any)[0].(map[string]any)
	n3Out := saved["node-3.json"]["outbounds"].([]any)[0].(map[string]any)

	if n1Out["password"] != "new1" || asInt(n1Out["server_port"]) != 1111 {
		t.Fatalf("node-1 应被更新: %#v", n1Out)
	}
	if n2Out["password"] != "old2" {
		t.Fatalf("node-2 不匹配，不应被修改: %#v", n2Out)
	}
	if n3Out["password"] != "new3" || asInt(n3Out["server_port"]) != 3333 {
		t.Fatalf("node-3 应被更新: %#v", n3Out)
	}
}

func TestModifyNodeOutboundsWithTargetFilenames(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-Target"
	configs := map[string]map[string]any{
		"node-1.json": {
			"inbounds":  []any{map[string]any{"tag": "n1", "listen_port": 20000}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n1-out", "server": "stay.com", "server_port": 8888}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n1-out"}}},
		},
		"node-2.json": {
			"inbounds":  []any{map[string]any{"tag": "n2", "listen_port": 20001}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n2-out", "server": "change.com", "server_port": 7777}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n2-out"}}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	result, err := service.ModifyNodeOutbounds(alias, []string{"node-2.json"}, "newproxy.com:9999:u:p", "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success || result.Count != 1 {
		t.Fatalf("期望仅修改 1 个目标节点, 得到: %#v", result)
	}

	saved, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	n1Out := saved["node-1.json"]["outbounds"].([]any)[0].(map[string]any)
	n2Out := saved["node-2.json"]["outbounds"].([]any)[0].(map[string]any)

	if n1Out["server"] != "stay.com" {
		t.Fatalf("node-1 不应被修改: %#v", n1Out)
	}
	if n2Out["server"] != "newproxy.com" || asInt(n2Out["server_port"]) != 9999 {
		t.Fatalf("node-2 应被修改为 newproxy.com:9999: %#v", n2Out)
	}
}

func TestModifyNodeOutboundsMultipleNodesSharingSameIP(t *testing.T) {
	service := &Service{dataDir: t.TempDir()}
	alias := "VPS-SharedIP"
	configs := map[string]map[string]any{
		"node-1.json": {
			"inbounds":  []any{map[string]any{"tag": "n1", "listen_port": 20000}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n1-out", "server": "144.56.19.85", "server_port": 1000, "password": "old1"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n1-out"}}},
		},
		"node-2.json": {
			"inbounds":  []any{map[string]any{"tag": "n2", "listen_port": 20001}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n2-out", "server": "144.56.19.85", "server_port": 1000, "password": "old1"}}, // 共享同一落地 IP
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n2-out"}}},
		},
		"node-3.json": {
			"inbounds":  []any{map[string]any{"tag": "n3", "listen_port": 20002}},
			"outbounds": []any{map[string]any{"type": "socks", "tag": "n3-out", "server": "163.5.18.134", "server_port": 3000, "password": "old3"}},
			"route":     map[string]any{"rules": []any{map[string]any{"outbound": "n3-out"}}},
		},
	}
	if err := service.saveNodeConfigMap(alias, configs); err != nil {
		t.Fatal(err)
	}

	// 用户只提交 2 条落地配置
	input := "144.56.19.85:7778:u1:newpass1\n163.5.18.134:7778:u3:newpass3"
	result, err := service.ModifyNodeOutbounds(alias, nil, input, "")
	if err != nil {
		t.Fatal(err)
	}
	if !result.Success {
		t.Fatalf("应修改成功: %#v", result)
	}
	// 验证修改了 3 个节点文件，但提示中必须严格说明提交了 2 条且全部匹配
	if result.Count != 3 {
		t.Fatalf("期望修改 3 个节点文件, 得到: %d", result.Count)
	}
	expectedMsg := "提交 2 条落地已全部匹配成功（共更新 3 个节点，部分节点共享相同落地 IP）"
	if result.Message != expectedMsg {
		t.Fatalf("提示信息不符合严密核对要求, 期望: %q, 得到: %q", expectedMsg, result.Message)
	}

	saved, err := service.loadNodeConfigMap(alias)
	if err != nil {
		t.Fatal(err)
	}
	n1Out := saved["node-1.json"]["outbounds"].([]any)[0].(map[string]any)
	n2Out := saved["node-2.json"]["outbounds"].([]any)[0].(map[string]any)
	n3Out := saved["node-3.json"]["outbounds"].([]any)[0].(map[string]any)

	if n1Out["password"] != "newpass1" || asInt(n1Out["server_port"]) != 7778 {
		t.Fatalf("node-1 落地未更新: %#v", n1Out)
	}
	if n2Out["password"] != "newpass1" || asInt(n2Out["server_port"]) != 7778 {
		t.Fatalf("node-2 共享落地未更新: %#v", n2Out)
	}
	if n3Out["password"] != "newpass3" || asInt(n3Out["server_port"]) != 7778 {
		t.Fatalf("node-3 落地未更新: %#v", n3Out)
	}
}

func TestExcelColumnNameAndSimpleSheetXMLHandlesWideAndControlChars(t *testing.T) {
	cases := map[int]string{
		0:   "A",
		25:  "Z",
		26:  "AA",
		51:  "AZ",
		52:  "BA",
		701: "ZZ",
		702: "AAA",
	}
	for idx, want := range cases {
		if got := excelColumnName(idx); got != want {
			t.Fatalf("excelColumnName(%d) = %q, want %q", idx, got, want)
		}
	}

	row := make([]string, 30)
	for i := range row {
		row[i] = fmt.Sprintf("col-%d\x00\x07", i)
	}
	xmlText := simpleSheetXML([][]string{row}, false)
	if !strings.Contains(xmlText, `r="AD1"`) {
		t.Fatalf("超过 26 列时单元格引用不正确: %s", xmlText)
	}
	if strings.ContainsRune(xmlText, '\x00') || strings.ContainsRune(xmlText, '\x07') {
		t.Fatalf("XML 未过滤控制字符: %q", xmlText)
	}
}

func TestParseSheetRowsHandlesMissingRefAndEnforcesLimits(t *testing.T) {
	sheet := `<worksheet><sheetData><row><c t="inlineStr"><is><t>alpha</t></is></c><c r="1" t="inlineStr"><is><t>beta</t></is></c></row></sheetData></worksheet>`
	rows, err := parseSheetRows(sheet, nil)
	if err != nil {
		t.Fatalf("缺失/不规范 cell r 属性不应报错: %v", err)
	}
	if len(rows) != 1 || len(rows[0]) != 2 || rows[0][0] != "alpha" || rows[0][1] != "beta" {
		t.Fatalf("解析结果异常: %#v", rows)
	}

	tooManyRows := fmt.Sprintf(`<worksheet><sheetData><row r="%d"><c r="A%d"><v>1</v></c></row></sheetData></worksheet>`, maxExcelRows+1, maxExcelRows+1)
	if _, err := parseSheetRows(tooManyRows, nil); err == nil {
		t.Fatal("超过最大行数限制时应返回错误")
	}

	tooWide := fmt.Sprintf(`<worksheet><sheetData><row r="1"><c r="%s1"><v>1</v></c></row></sheetData></worksheet>`, excelColumnName(maxExcelColumns))
	if _, err := parseSheetRows(tooWide, nil); err == nil {
		t.Fatal("超过最大列数限制时应返回错误")
	}
}

