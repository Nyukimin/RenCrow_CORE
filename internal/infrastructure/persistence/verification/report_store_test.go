package verification

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domainverification "github.com/Nyukimin/RenCrow_CORE/internal/domain/verification"
)

func TestJSONLReportStoreSaveListGetSummary(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "verification_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	old := testReport("job-1", domainverification.StatusWeaklySupported, time.Now().UTC().Add(-time.Minute))
	latest := testReport("job-2", domainverification.StatusConflict, time.Now().UTC())
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
	if len(items) != 1 || items[0].JobID != "job-2" {
		t.Fatalf("expected latest job-2, got %+v", items)
	}

	got, err := store.GetByJobID(context.Background(), "job-1")
	if err != nil {
		t.Fatalf("GetByJobID failed: %v", err)
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

func testReport(jobID string, status domainverification.VerificationStatus, createdAt time.Time) domainverification.VerificationReport {
	return domainverification.VerificationReport{
		ID:           "verify_" + jobID,
		JobID:        jobID,
		SessionID:    "session-1",
		Route:        "CHAT",
		Status:       status,
		TriggerLevel: domainverification.TriggerMedium,
		CreatedAt:    createdAt,
	}
}
