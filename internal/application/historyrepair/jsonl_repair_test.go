package historyrepair

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJSONLRepairServiceQuarantinesInvalidLinesAndAudits(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "logs", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{\"ok\":1}\n{\"broken\"\n{\"ok\":2}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewJSONLRepairService(root, filepath.Join(root, "logs", "history_repair.jsonl")).WithNow(func() time.Time {
		return time.Date(2026, 6, 22, 7, 20, 0, 0, time.UTC)
	})

	report, err := svc.RepairJSONL(context.Background(), JSONLRepairRequest{Path: "logs/session.jsonl", Reason: "test", RequestedBy: "coder"})
	if err != nil {
		t.Fatalf("RepairJSONL() error = %v", err)
	}
	if report.Status != "repaired" || report.ValidLines != 2 || report.InvalidLines != 1 || report.QuarantinePath == "" || report.RepairedPath == "" {
		t.Fatalf("report=%#v", report)
	}
	repaired, err := os.ReadFile(report.RepairedPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(repaired), "broken") || !strings.Contains(string(repaired), `{"ok":1}`) || !strings.Contains(string(repaired), `{"ok":2}`) {
		t.Fatalf("unexpected repaired content: %s", repaired)
	}
	quarantined, err := os.ReadFile(report.QuarantinePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(quarantined), "broken") {
		t.Fatalf("unexpected quarantine content: %s", quarantined)
	}
	audit, err := os.ReadFile(report.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(audit), `"repair_id"`) || !strings.Contains(string(audit), `"requested_by":"coder"`) {
		t.Fatalf("unexpected audit content: %s", audit)
	}
}

func TestJSONLRepairServiceRejectsOutsideWorkspace(t *testing.T) {
	svc := NewJSONLRepairService(t.TempDir(), "")
	if _, err := svc.RepairJSONL(context.Background(), JSONLRepairRequest{Path: "/etc/passwd"}); err == nil {
		t.Fatal("expected outside workspace path to be rejected")
	}
}
