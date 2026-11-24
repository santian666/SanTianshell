package main

import (
	"archive/zip"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestCompareSemver(t *testing.T) {
	cases := []struct {
		v1   string
		v2   string
		want int
	}{
		{"0.1.35", "0.1.34", 1},
		{"v0.1.35", "0.1.35", 0},
		{"0.1.34", "v0.1.35", -1},
		{"0.2.0", "0.1.99", 1},
		{"1.0.0-rc1", "0.9.9", 1},
	}
	for _, tc := range cases {
		if got := compareSemver(tc.v1, tc.v2); got != tc.want {
			t.Fatalf("compareSemver(%q, %q) = %d, want %d", tc.v1, tc.v2, got, tc.want)
		}
	}
}

func TestAppUpdateCheckAndValidateZip(t *testing.T) {
	dir := t.TempDir()
	zipPath := filepath.Join(dir, "SanTianShell-windows-amd64-v9.9.9.zip")
	f, err := os.Create(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("SanTianShell.exe")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte("MZ-dummy"))
	_ = zw.Close()
	_ = f.Close()

	if !isValidUpdateZip(zipPath, 0) {
		t.Fatalf("expected valid update zip containing SanTianShell.exe")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/santian666/SanTianShell/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(githubLatestRelease{
			TagName:     "v9.9.9",
			Body:        "纯净版发布与软件更新支持",
			PublishedAt: "2026-11-24T12:00:00Z",
			Assets: []githubReleaseAsset{
				{Name: "SanTianShell-Setup-9.9.9.exe", Size: 100, BrowserDownloadURL: "https://example.com/setup.exe"},
				{Name: "SanTianShell-windows-amd64-9.9.9.zip", Size: 200, BrowserDownloadURL: "https://example.com/app.zip"},
			},
		})
	}))
	defer srv.Close()

	mgr := newAppUpdateManager()
	mgr.apiBaseURL = srv.URL
	info, err := mgr.CheckUpdate()
	if err != nil {
		t.Fatalf("CheckUpdate failed: %v", err)
	}
	if !info.Available || info.Status != "update_available" {
		t.Fatalf("expected update_available, got %+v", info)
	}
	if info.AssetName != "SanTianShell-windows-amd64-9.9.9.zip" {
		t.Fatalf("expected zip asset selected, got %q", info.AssetName)
	}
}
