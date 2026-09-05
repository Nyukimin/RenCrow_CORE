package execution

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLReportStore_SaveAndListRecent(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	taskID1 := modulecore.NewTaskID()
	taskID2 := modulecore.NewTaskID()
	r1 := domain.ExecutionReport{
		TaskID:     taskID1,
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC().Add(-1 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-30 * time.Second),
	}
	r2 := domain.ExecutionReport{
		TaskID:     taskID2,
		Goal:       "ログ確認して",
		Status:     "failed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}
	if err := store.Save(context.Background(), r1); err != nil {
		t.Fatalf("Save r1 failed: %v", err)
	}
	if err := store.Save(context.Background(), r2); err != nil {
		t.Fatalf("Save r2 failed: %v", err)
	}

	items, err := store.ListRecent(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if items[0].TaskID != taskID2 {
		t.Fatalf("expected most recent task, got %s", items[0].TaskID)
	}
}

func TestJSONLReportStoreSkipsJSONWithoutCanonicalTaskID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "execution_report.jsonl")
	store, err := NewJSONLReportStore(path)
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}
	legacyField := "job" + "_" + "id"
	payload := []byte(`{"` + legacyField + `":"legacy","goal":"goal","status":"passed","created_at":"2026-09-05T00:00:00Z","finished_at":"2026-09-05T00:00:01Z"}` + "\n")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	items, err := store.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("noncanonical JSON must not cross the persistence boundary: %+v", items)
	}
}

func TestJSONLReportStore_GetByTaskID(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	taskID := modulecore.NewTaskID()
	r1 := domain.ExecutionReport{
		TaskID:     taskID,
		Goal:       "first",
		Status:     "failed",
		CreatedAt:  time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-2 * time.Minute),
	}
	r2 := domain.ExecutionReport{
		TaskID:     taskID,
		Goal:       "second",
		Status:     "passed",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}
	if err := store.Save(context.Background(), r1); err != nil {
		t.Fatalf("Save r1 failed: %v", err)
	}
	if err := store.Save(context.Background(), r2); err != nil {
		t.Fatalf("Save r2 failed: %v", err)
	}

	got, err := store.GetByTaskID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetByTaskID failed: %v", err)
	}
	if got.Goal != "second" || got.Status != "passed" {
		t.Fatalf("unexpected report: %+v", got)
	}
}

func TestJSONLReportStore_Summary(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	items := []domain.ExecutionReport{
		{TaskID: modulecore.NewTaskID(), Goal: "a", Status: "passed", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{TaskID: modulecore.NewTaskID(), Goal: "b", Status: "failed", ErrorKind: "verify", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{TaskID: modulecore.NewTaskID(), Goal: "c", Status: "failed", ErrorKind: "apply", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
	}
	for _, it := range items {
		if err := store.Save(context.Background(), it); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	s, err := store.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary failed: %v", err)
	}
	if s["status"]["passed"] != 1 || s["status"]["failed"] != 2 {
		t.Fatalf("unexpected status summary: %+v", s)
	}
	if s["error_kind"]["verify"] != 1 || s["error_kind"]["apply"] != 1 {
		t.Fatalf("unexpected error_kind summary: %+v", s)
	}
}

func TestJSONLReportStore_SaveWithTTSEvidence(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	taskID := modulecore.NewTaskID()
	in := domain.ExecutionReport{
		TaskID:       taskID,
		Goal:         "TTS実装して",
		Status:       "passed",
		TTSProvider:  "rencrow-tts-gateway",
		TTSVoiceID:   "mio",
		TTSAudioFile: "/tmp/tts-gateway.wav",
		TTSDuration:  1234,
		PlaybackCmd:  "ffplay -autoexit -nodisp /tmp/tts-gateway.wav",
		PlaybackCode: 0,
		CreatedAt:    time.Now().UTC(),
		FinishedAt:   time.Now().UTC(),
	}
	if err := store.Save(context.Background(), in); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := store.GetByTaskID(context.Background(), taskID)
	if err != nil {
		t.Fatalf("GetByTaskID failed: %v", err)
	}
	if got.TTSProvider != "rencrow-tts-gateway" || got.PlaybackCode != 0 {
		t.Fatalf("unexpected tts evidence: %+v", got)
	}
}

func TestJSONLReportStore_ListRecentUnique(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	firstTaskID := modulecore.NewTaskID()
	secondTaskID := modulecore.NewTaskID()
	// First task: failed -> passed (retry success)
	r1Failed := domain.ExecutionReport{
		TaskID:     firstTaskID,
		Goal:       "ops task",
		Status:     "failed",
		ErrorKind:  "apply",
		CreatedAt:  time.Now().UTC().Add(-3 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-3 * time.Minute),
	}
	r1Passed := domain.ExecutionReport{
		TaskID:       firstTaskID,
		Goal:         "ops task",
		Status:       "passed",
		AttemptCount: 2,
		RepairCount:  1,
		CreatedAt:    time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt:   time.Now().UTC().Add(-2 * time.Minute),
	}
	// Second task: simple success
	r2 := domain.ExecutionReport{
		TaskID:     secondTaskID,
		Goal:       "simple task",
		Status:     "passed",
		CreatedAt:  time.Now().UTC().Add(-1 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-1 * time.Minute),
	}

	for _, r := range []domain.ExecutionReport{r1Failed, r1Passed, r2} {
		if err := store.Save(context.Background(), r); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	// ListRecent returns all 3 reports
	all, err := store.ListRecent(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("ListRecent: expected 3 reports, got %d", len(all))
	}

	// ListRecentUnique returns only 2 (latest for each task)
	unique, err := store.ListRecentUnique(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecentUnique failed: %v", err)
	}
	if len(unique) != 2 {
		t.Fatalf("ListRecentUnique: expected 2 unique tasks, got %d", len(unique))
	}

	// Verify the first task shows the latest (passed) report
	var firstTaskReport domain.ExecutionReport
	for _, r := range unique {
		if r.TaskID == firstTaskID {
			firstTaskReport = r
			break
		}
	}
	if firstTaskReport.Status != "passed" {
		t.Fatalf("first task should show passed status, got %s", firstTaskReport.Status)
	}
	if firstTaskReport.AttemptCount != 2 {
		t.Fatalf("first task should show attempt_count=2, got %d", firstTaskReport.AttemptCount)
	}
}

func TestJSONLReportStore_SummaryUnique(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	firstTaskID := modulecore.NewTaskID()
	secondTaskID := modulecore.NewTaskID()
	// First task: failed -> passed (should count as passed only)
	r1Failed := domain.ExecutionReport{
		TaskID:     firstTaskID,
		Goal:       "ops",
		Status:     "failed",
		ErrorKind:  "apply",
		CreatedAt:  time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-2 * time.Minute),
	}
	r1Passed := domain.ExecutionReport{
		TaskID:     firstTaskID,
		Goal:       "ops",
		Status:     "passed",
		CreatedAt:  time.Now().UTC().Add(-1 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	// Second task: failed (no retry)
	r2 := domain.ExecutionReport{
		TaskID:     secondTaskID,
		Goal:       "code",
		Status:     "failed",
		ErrorKind:  "verify",
		CreatedAt:  time.Now().UTC(),
		FinishedAt: time.Now().UTC(),
	}

	for _, r := range []domain.ExecutionReport{r1Failed, r1Passed, r2} {
		if err := store.Save(context.Background(), r); err != nil {
			t.Fatalf("Save failed: %v", err)
		}
	}

	// Summary counts all 3 reports
	s, err := store.Summary(context.Background())
	if err != nil {
		t.Fatalf("Summary failed: %v", err)
	}
	if s["status"]["passed"] != 1 || s["status"]["failed"] != 2 {
		t.Fatalf("Summary: expected passed=1, failed=2, got %+v", s["status"])
	}

	// SummaryUnique counts only 2 unique tasks (first=passed, second=failed)
	su, err := store.SummaryUnique(context.Background())
	if err != nil {
		t.Fatalf("SummaryUnique failed: %v", err)
	}
	if su["status"]["passed"] != 1 {
		t.Fatalf("SummaryUnique: expected passed=1, got %d", su["status"]["passed"])
	}
	if su["status"]["failed"] != 1 {
		t.Fatalf("SummaryUnique: expected failed=1, got %d", su["status"]["failed"])
	}
	if su["error_kind"]["verify"] != 1 {
		t.Fatalf("SummaryUnique: expected verify=1, got %d", su["error_kind"]["verify"])
	}
	if su["error_kind"]["apply"] != 0 {
		t.Fatalf("SummaryUnique: first task final status is passed, so apply error should not be counted, got %d", su["error_kind"]["apply"])
	}
}
