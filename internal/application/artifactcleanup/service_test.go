package artifactcleanup

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCleanupDryRunDoesNotMoveCandidate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tmp", "reindex.partial")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("partial"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	report, err := NewService(root, filepath.Join(root, "logs", "cleanup.jsonl")).Cleanup(context.Background(), Request{
		Paths:       []string{"tmp/reindex.partial"},
		MaxAgeHours: 24,
		DryRun:      true,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if len(report.Candidates) != 1 || report.Quarantined != 0 {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("dry-run must keep file: %v", err)
	}
}

func TestCleanupQuarantinesCandidate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "cache", "broken.tmp")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("cache"), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-48 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	report, err := NewService(root, filepath.Join(root, "logs", "cleanup.jsonl")).Cleanup(context.Background(), Request{
		Paths:       []string{"cache/broken.tmp"},
		MaxAgeHours: 24,
	})
	if err != nil {
		t.Fatalf("Cleanup() error = %v", err)
	}
	if report.Quarantined != 1 || report.Candidates[0].Quarantine == "" {
		t.Fatalf("report=%#v", report)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("original should be moved, stat err=%v", err)
	}
}
