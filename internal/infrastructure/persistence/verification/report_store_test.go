package verification

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLReportStoreSaveListGetSummary(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "verification_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	oldTaskID := modulecore.NewTaskID()
	latestTaskID := modulecore.NewTaskID()
	old := testReport(oldTaskID, domainverification.StatusWeaklySupported, time.Now().UTC().Add(-time.Minute))
	latest := testReport(latestTaskID, domainverification.StatusConflict, time.Now().UTC())
	if err := store.Save(context.Background(), old); err != nil {
		t.Fatalf("Save old failed: %v", err)
	}
	if err := store.Save(context.Background(), latest); err != nil {
		t.Fatalf("Save latest failed: %v", err)
	}

	items, err := store.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) != 1 || items[0].TaskID != latestTaskID {
		t.Fatalf("expected latest task, got %+v", items)
	}

	got, err := store.GetByTaskID(context.Background(), oldTaskID)
	if err != nil {
		t.Fatalf("GetByTaskID failed: %v", err)
	}
	if got.Status != domainverification.StatusWeaklySupported {
		t.Fatalf("unexpected status: %s", got.Status)
	}

	summary, err := store.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary failed: %v", err)
	}
	if summary["status"][string(domainverification.StatusConflict)] != 1 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
}

func TestJSONLReportStoreRejectsInvalidReport(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "verification_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}
	if err := store.Save(context.Background(), domainverification.VerificationReport{}); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestJSONLReportStoreRejectsInvalidLookupAndLegacyRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "verification_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetByTaskID(context.Background(), modulecore.TaskID(modulecore.NewMessageID())); err == nil {
		t.Fatal("wrong canonical ID type was accepted")
	}
	legacyKey := "job" + "_" + "id"
	legacy, err := json.Marshal(map[string]any{"id": "verify-old", legacyKey: "legacy-1", "session_id": "session-1", "status": "not_checked", "created_at": "2026-09-05T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	legacy = append(legacy, '\n')
	if err := os.WriteFile(path, legacy, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRecent(context.Background(), 10); err == nil {
		t.Fatal("legacy identity row was silently accepted")
	}
}

func testReport(taskID modulecore.TaskID, status domainverification.VerificationStatus, createdAt time.Time) domainverification.VerificationReport {
	return domainverification.VerificationReport{
		ID:           "verify_" + string(taskID),
		TaskID:       taskID,
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       status,
		TriggerLevel: domainverification.TriggerMedium,
		CreatedAt:    createdAt,
	}
}
