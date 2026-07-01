package complexity

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestScanDetectsReportOnlyHotspots(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	code := `package demo

func Match(users []User, orders []Order) {
	for _, order := range orders {
		for _, user := range users {
			_ = user
		}
		_ = order
	}
}
`
	if err := os.WriteFile(filepath.Join(src, "demo.go"), []byte(code), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	analyzer := NewAnalyzer()
	analyzer.now = func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) }
	result, err := analyzer.Scan(ScanRequest{
		ScanID:      "scan_1",
		Repo:        "repo",
		RootPath:    tmp,
		ScanScope:   []string{"src"},
		MaxHotspots: 5,
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Scan.Mode != "report_only" || result.Scan.HotspotsFound == 0 {
		t.Fatalf("unexpected scan: %#v", result.Scan)
	}
	if len(result.Hotspots) != 1 || result.Hotspots[0].HotspotType != "nested_loop" {
		t.Fatalf("hotspots=%#v", result.Hotspots)
	}
	if len(result.Evidence) != 1 || result.Evidence[0].Snippet == "" {
		t.Fatalf("evidence=%#v", result.Evidence)
	}
}

func TestScanFiltersCandidateFilesBeforeDetailedAnalysis(t *testing.T) {
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	matching := `package demo

func Match(users []User, orders []Order) {
	for _, order := range orders {
		for _, user := range users {
			_ = user
		}
		_ = order
	}
}
`
	nonMatching := `package demo

func Ignore(users []User, orders []Order) {
	for _, order := range orders {
		for _, user := range users {
			_ = user
		}
		_ = order
	}
}
`
	if err := os.WriteFile(filepath.Join(src, "matching.go"), []byte(matching), 0644); err != nil {
		t.Fatalf("write matching: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, "non_matching.go"), []byte(nonMatching), 0644); err != nil {
		t.Fatalf("write non matching: %v", err)
	}
	analyzer := NewAnalyzer()
	analyzer.now = func() time.Time { return time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC) }
	result, err := analyzer.Scan(ScanRequest{
		ScanID:            "scan_1",
		Repo:              "repo",
		RootPath:          tmp,
		ScanScope:         []string{"src"},
		MaxHotspots:       5,
		CandidatePatterns: []string{"func Match"},
	})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Scan.FilesScanned != 1 {
		t.Fatalf("expected only candidate file to be scanned, scan=%#v", result.Scan)
	}
	if len(result.Hotspots) != 1 || result.Hotspots[0].FilePath != "src/matching.go" {
		t.Fatalf("hotspots=%#v", result.Hotspots)
	}
}

func TestScanExcludesBuildDirectories(t *testing.T) {
	tmp := t.TempDir()
	build := filepath.Join(tmp, "node_modules")
	if err := os.MkdirAll(build, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(build, "bad.js"), []byte("items.map((x) => users.find((u) => u.id === x.id));"), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	result, err := NewAnalyzer().Scan(ScanRequest{ScanID: "scan_1", Repo: "repo", RootPath: tmp})
	if err != nil {
		t.Fatalf("Scan() error = %v", err)
	}
	if result.Scan.FilesScanned != 0 || len(result.Hotspots) != 0 {
		t.Fatalf("expected excluded scan, got %#v", result)
	}
}
