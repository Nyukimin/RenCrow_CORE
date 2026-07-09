package execution

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
)

func TestJSONLReportStore_SaveAndListRecent(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	r1 := domain.ExecutionReport{
		JobID:      "j1",
		Goal:       "TTS実装して",
		Status:     "passed",
		CreatedAt:  time.Now().UTC().Add(-1 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-30 * time.Second),
	}
	r2 := domain.ExecutionReport{
		JobID:      "j2",
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
	if items[0].JobID != "j2" {
		t.Fatalf("expected most recent j2, got %s", items[0].JobID)
	}
}

func TestJSONLReportStore_GetByJobID(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	r1 := domain.ExecutionReport{
		JobID:      "job-x",
		Goal:       "first",
		Status:     "failed",
		CreatedAt:  time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-2 * time.Minute),
	}
	r2 := domain.ExecutionReport{
		JobID:      "job-x",
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

	got, err := store.GetByJobID(context.Background(), "job-x")
	if err != nil {
		t.Fatalf("GetByJobID failed: %v", err)
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
		{JobID: "j1", Goal: "a", Status: "passed", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{JobID: "j2", Goal: "b", Status: "failed", ErrorKind: "verify", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
		{JobID: "j3", Goal: "c", Status: "failed", ErrorKind: "apply", CreatedAt: time.Now().UTC(), FinishedAt: time.Now().UTC()},
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

	in := domain.ExecutionReport{
		JobID:        "tts-job",
		Goal:         "TTS実装して",
		Status:       "passed",
		TTSProvider:  "sbv2",
		TTSVoiceID:   "mio",
		TTSAudioFile: "/tmp/sbv2.wav",
		TTSDuration:  1234,
		PlaybackCmd:  "ffplay -autoexit -nodisp /tmp/sbv2.wav",
		PlaybackCode: 0,
		CreatedAt:    time.Now().UTC(),
		FinishedAt:   time.Now().UTC(),
	}
	if err := store.Save(context.Background(), in); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	got, err := store.GetByJobID(context.Background(), "tts-job")
	if err != nil {
		t.Fatalf("GetByJobID failed: %v", err)
	}
	if got.TTSProvider != "sbv2" || got.PlaybackCode != 0 {
		t.Fatalf("unexpected tts evidence: %+v", got)
	}
}

func TestJSONLReportStore_ListRecentUnique(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	// Job1: failed -> passed (retry success)
	r1Failed := domain.ExecutionReport{
		JobID:      "job-1",
		Goal:       "ops task",
		Status:     "failed",
		ErrorKind:  "apply",
		CreatedAt:  time.Now().UTC().Add(-3 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-3 * time.Minute),
	}
	r1Passed := domain.ExecutionReport{
		JobID:        "job-1",
		Goal:         "ops task",
		Status:       "passed",
		AttemptCount: 2,
		RepairCount:  1,
		CreatedAt:    time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt:   time.Now().UTC().Add(-2 * time.Minute),
	}
	// Job2: simple success
	r2 := domain.ExecutionReport{
		JobID:      "job-2",
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

	// ListRecentUnique returns only 2 (latest for each job)
	unique, err := store.ListRecentUnique(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListRecentUnique failed: %v", err)
	}
	if len(unique) != 2 {
		t.Fatalf("ListRecentUnique: expected 2 unique jobs, got %d", len(unique))
	}

	// Verify job-1 shows the latest (passed) report
	var job1Report domain.ExecutionReport
	for _, r := range unique {
		if r.JobID == "job-1" {
			job1Report = r
			break
		}
	}
	if job1Report.Status != "passed" {
		t.Fatalf("job-1 should show passed status, got %s", job1Report.Status)
	}
	if job1Report.AttemptCount != 2 {
		t.Fatalf("job-1 should show attempt_count=2, got %d", job1Report.AttemptCount)
	}
}

func TestJSONLReportStore_SummaryUnique(t *testing.T) {
	store, err := NewJSONLReportStore(filepath.Join(t.TempDir(), "execution_report.jsonl"))
	if err != nil {
		t.Fatalf("NewJSONLReportStore failed: %v", err)
	}

	// Job1: failed -> passed (should count as passed only)
	r1Failed := domain.ExecutionReport{
		JobID:      "job-1",
		Goal:       "ops",
		Status:     "failed",
		ErrorKind:  "apply",
		CreatedAt:  time.Now().UTC().Add(-2 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-2 * time.Minute),
	}
	r1Passed := domain.ExecutionReport{
		JobID:      "job-1",
		Goal:       "ops",
		Status:     "passed",
		CreatedAt:  time.Now().UTC().Add(-1 * time.Minute),
		FinishedAt: time.Now().UTC().Add(-1 * time.Minute),
	}
	// Job2: failed (no retry)
	r2 := domain.ExecutionReport{
		JobID:      "job-2",
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

	// SummaryUnique counts only 2 unique jobs (job-1=passed, job-2=failed)
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
		t.Fatalf("SummaryUnique: job-1 final status is passed, so apply error should not be counted, got %d", su["error_kind"]["apply"])
	}
}
