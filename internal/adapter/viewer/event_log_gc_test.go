package viewer

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
)

func TestEventLogGCServiceRunOnceRemovesExpiredItems(t *testing.T) {
	store, err := NewEventLogStore(filepath.Join(t.TempDir(), "orchestrator_event_log.jsonl"))
	if err != nil {
		t.Fatalf("NewEventLogStore failed: %v", err)
	}
	now := time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC)
	oldTs := now.Add(-16 * 24 * time.Hour).Format(time.RFC3339)
	newTs := now.Add(-2 * time.Hour).Format(time.RFC3339)
	_ = store.Append(orchestrator.OrchestratorEvent{Type: "agent.note", From: "mio", Timestamp: oldTs})
	_ = store.Append(orchestrator.OrchestratorEvent{Type: "agent.note", From: "shiro", Timestamp: newTs})

	gcLogPath := filepath.Join(t.TempDir(), "orchestrator_event_gc.jsonl")
	svc, err := NewEventLogGCService(store, gcLogPath, 14, 60)
	if err != nil {
		t.Fatalf("NewEventLogGCService failed: %v", err)
	}

	report, err := svc.RunOnce(context.Background(), now)
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if report.DeletedCount != 1 {
		t.Fatalf("deleted_count = %d, want 1", report.DeletedCount)
	}
	items, err := store.Query(context.Background(), LogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("Query failed: %v", err)
	}
	if len(items) != 1 || items[0].From != "shiro" {
		t.Fatalf("unexpected remaining items: %+v", items)
	}
	if report.CompressedCount != 1 || report.CompressedPath == "" {
		t.Fatalf("expected compressed expired archive, got %+v", report)
	}
	lines := readGzipLines(t, report.CompressedPath)
	if len(lines) != 1 || !strings.Contains(lines[0], `"from":"mio"`) {
		t.Fatalf("unexpected compressed expired lines: %q", lines)
	}
}

func TestEventLogGCServiceRunOnceReportsPartialError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orchestrator_event_log.jsonl")
	if err := os.WriteFile(path, []byte("{bad json}\n"+`{"type":"agent.note","from":"mio","timestamp":"broken"}`+"\n"), 0644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	store, err := NewEventLogStore(path)
	if err != nil {
		t.Fatalf("NewEventLogStore failed: %v", err)
	}
	gcLogPath := filepath.Join(dir, "orchestrator_event_gc.jsonl")
	svc, err := NewEventLogGCService(store, gcLogPath, 14, 60)
	if err != nil {
		t.Fatalf("NewEventLogGCService failed: %v", err)
	}

	report, err := svc.RunOnce(context.Background(), time.Date(2026, 3, 19, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("RunOnce failed: %v", err)
	}
	if report.Status != "partial_error" {
		t.Fatalf("status = %q, want partial_error", report.Status)
	}
	f, err := os.Open(gcLogPath)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if !sc.Scan() {
		t.Fatal("expected gc report line")
	}
	var got EventGCReport
	if err := json.Unmarshal(sc.Bytes(), &got); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if got.DecodeErrorCount == 0 || got.TimestampErrorCount == 0 {
		t.Fatalf("expected decode/timestamp error counts, got %+v", got)
	}
	if got.QuarantineCount != 2 || got.QuarantinePath == "" {
		t.Fatalf("expected quarantined invalid archive, got %+v", got)
	}
	lines := readGzipLines(t, got.QuarantinePath)
	if len(lines) != 2 || !strings.Contains(lines[0], "{bad json}") || !strings.Contains(lines[1], `"timestamp":"broken"`) {
		t.Fatalf("unexpected quarantine lines: %q", lines)
	}
}

func TestEventLogGCServiceStartRunsImmediately(t *testing.T) {
	store, err := NewEventLogStore(filepath.Join(t.TempDir(), "orchestrator_event_log.jsonl"))
	if err != nil {
		t.Fatalf("NewEventLogStore failed: %v", err)
	}
	oldTs := time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)
	_ = store.Append(orchestrator.OrchestratorEvent{Type: "agent.note", From: "mio", Timestamp: oldTs})

	gcLogPath := filepath.Join(t.TempDir(), "orchestrator_event_gc.jsonl")
	svc, err := NewEventLogGCService(store, gcLogPath, 1, 60)
	if err != nil {
		t.Fatalf("NewEventLogGCService failed: %v", err)
	}
	svc.Start()
	defer svc.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		items, err := store.Query(context.Background(), LogFilter{Limit: 10})
		if err != nil {
			t.Fatalf("Query failed: %v", err)
		}
		if len(items) == 0 {
			archives, err := filepath.Glob(filepath.Join(filepath.Dir(store.Path()), "archive", "orchestrator_event_log.expired.*.jsonl.gz"))
			if err != nil {
				t.Fatalf("glob archive: %v", err)
			}
			if len(archives) == 1 {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("GC did not run immediately after Start")
}

func readGzipLines(t *testing.T, path string) []string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open gzip archive failed: %v", err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("new gzip reader failed: %v", err)
	}
	defer zr.Close()
	data, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("read gzip archive failed: %v", err)
	}
	raw := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(raw) == 1 && raw[0] == "" {
		return []string{}
	}
	return raw
}
