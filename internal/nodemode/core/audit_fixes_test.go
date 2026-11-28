package core

import (
	"fmt"
	"strings"
	"testing"
)

func TestExportNodeConnectionsNotTruncatedAndEncryptedAtRest(t *testing.T) {
	svc := NewServiceWithDataDir(t.TempDir())
	var lines []string
	for i := 1; i <= 250; i++ {
		lines = append(lines, fmt.Sprintf("198.51.100.%d:%d:user%d:secret-pass-%d:tail\t2027-01-01\tremark-%d\tUS", (i%250)+1, 10000+i, i, i, i))
	}
	res, err := svc.ImportNodeConnections(strings.Join(lines, "\n"))
	if err != nil {
		t.Fatalf("ImportNodeConnections failed: %v", err)
	}
	if res.Imported != 250 || res.Skipped != 0 {
		t.Fatalf("expected 250 imported, got %+v", res)
	}

	// Re-importing same records must deduplicate even with DPAPI encryption.
	dupRes, err := svc.ImportNodeConnections(lines[0])
	if err != nil {
		t.Fatalf("re-import failed: %v", err)
	}
	if dupRes.Imported != 0 || dupRes.Skipped != 1 {
		t.Fatalf("expected duplicate to be skipped, got %+v", dupRes)
	}

	// Verify DB stores encrypted password and connection_str, not plaintext.
	db, err := svc.openDB()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var rawPass, rawConn string
	if err := db.QueryRow(`select password, connection_str from node_connection_records limit 1`).Scan(&rawPass, &rawConn); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(rawPass, dbSecretPrefix) || strings.Contains(rawPass, "secret-pass-") {
		t.Fatalf("expected encrypted password in DB, got %q", rawPass)
	}
	if !strings.HasPrefix(rawConn, dbSecretPrefix) || strings.Contains(rawConn, "secret-pass-") {
		t.Fatalf("expected encrypted connection_str in DB, got %q", rawConn)
	}

	// Export must include all 250 rows + 1 header line, NOT silently truncated at 200.
	csvText, err := svc.ExportNodeConnections("")
	if err != nil {
		t.Fatalf("ExportNodeConnections failed: %v", err)
	}
	csvLines := strings.Split(strings.TrimSpace(csvText), "\n")
	if len(csvLines) != 251 {
		t.Fatalf("expected 251 CSV lines (header + 250 rows), got %d", len(csvLines))
	}
	if !strings.Contains(csvText, "secret-pass-1:tail") {
		t.Fatalf("expected decrypted password with colon in export")
	}

	// Search must match decrypted connection_str and treat % literally.
	pctItems, err := svc.SearchNodeConnections("%", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(pctItems) != 0 {
		t.Fatalf("expected literal %% search to return 0 items, got %d", len(pctItems))
	}
}

func TestParseNodeConnectionLineBoundsAndIPv6(t *testing.T) {
	if _, ok := parseNodeConnectionLine("1.2.3.4:65536:u:p"); ok {
		t.Fatalf("expected port 65536 to be rejected")
	}
	if _, ok := parseNodeConnectionLine("1.2.3.4:0:u:p"); ok {
		t.Fatalf("expected port 0 to be rejected")
	}
	item, ok := parseNodeConnectionLine("[2001:db8::1]:1080:alice:p@ss:word\t2027-05-01\tnote\tJP")
	if !ok {
		t.Fatalf("expected valid IPv6 connection line to parse")
	}
	if item.IP != "2001:db8::1" || item.Port != 1080 || item.Username != "alice" || item.Password != "p@ss:word" {
		t.Fatalf("unexpected parsed IPv6 record: %+v", item)
	}
}

func TestRestoreDeletedRecordsChunkedOverBatchLimit(t *testing.T) {
	svc := NewServiceWithDataDir(t.TempDir())
	items := make(map[string]map[string]any, 520)
	for i := 0; i < 520; i++ {
		name := fmt.Sprintf("node_%03d.json", i)
		items[name] = map[string]any{
			"inbounds": []any{map[string]any{"tag": name, "listen_port": 20000 + i}},
		}
	}
	if err := svc.createDeletedRecords("vps-a", items); err != nil {
		t.Fatalf("createDeletedRecords failed: %v", err)
	}
	deleted, err := svc.GetDeletedRecords()
	if err != nil {
		t.Fatalf("GetDeletedRecords failed: %v", err)
	}
	if len(deleted) != 520 {
		t.Fatalf("expected 520 deleted records, got %d", len(deleted))
	}
	ids := make([]int, 0, len(deleted))
	for _, rec := range deleted {
		ids = append(ids, rec.ID)
	}
	res, err := svc.RestoreDeletedRecords(ids)
	if err != nil {
		t.Fatalf("RestoreDeletedRecords failed: %v", err)
	}
	if !res.Success || res.Count != 520 {
		t.Fatalf("expected 520 restored, got %+v", res)
	}
}
