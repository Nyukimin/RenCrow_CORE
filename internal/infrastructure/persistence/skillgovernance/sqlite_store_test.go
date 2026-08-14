package skillgovernance

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domainskill "github.com/Nyukimin/RenCrow_CORE/internal/domain/skillgovernance"
)

func TestSQLiteStoreConfiguresSerializedBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "skill_governance.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("MaxOpenConnections = %d, want 1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatalf("busy_timeout query failed: %v", err)
	}
	if busyTimeout != sqliteBusyTimeoutMilliseconds {
		t.Fatalf("busy_timeout = %d, want %d", busyTimeout, sqliteBusyTimeoutMilliseconds)
	}
}

func TestSQLiteStoreConcurrentSkillManifestWrites(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "skill_governance.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	const workers = 8
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.SaveSkillManifest(context.Background(), domainskill.SkillManifest{
				SkillID:   fmt.Sprintf("core.concurrent-%d", i),
				Name:      "Concurrent skill",
				Scope:     domainskill.ScopeCore,
				Version:   "1.0.0",
				Path:      "skills/core/concurrent",
				Enabled:   true,
				UpdatedAt: time.Now().UTC(),
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SaveSkillManifest failed: %v", err)
		}
	}
	items, err := store.ListSkillManifests(context.Background(), workers)
	if err != nil || len(items) != workers {
		t.Fatalf("concurrent skill manifest count = %d, err=%v; want %d", len(items), err, workers)
	}
}

func TestSQLiteStoreSaveAndListSkillGovernanceRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "skill_governance.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	if err := store.SaveSkillManifest(ctx, domainskill.SkillManifest{
		SkillID:   "core.pr-readiness",
		Name:      "PR Readiness",
		Scope:     domainskill.ScopeCore,
		Version:   "1.0.0",
		Path:      "skills/core/pr-readiness",
		Enabled:   true,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("SaveSkillManifest failed: %v", err)
	}
	if err := store.SaveSkillTriggerLog(ctx, domainskill.SkillTriggerLog{
		EventID:     "evt_skill_1",
		SkillID:     "core.pr-readiness",
		TriggerType: "keyword",
		Status:      domainskill.TriggerStatusTriggered,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("SaveSkillTriggerLog failed: %v", err)
	}
	if err := store.SaveSkillChangeLog(ctx, domainskill.SkillChangeLog{
		ChangeID:   "chg_1",
		SkillID:    "core.pr-readiness",
		OldVersion: "1.0.0",
		NewVersion: "1.0.1",
		EvalResult: "passed",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveSkillChangeLog failed: %v", err)
	}
	if err := store.SaveContributionGateLog(ctx, domainskill.ContributionGateLog{
		EventID:             "evt_contrib_1",
		Repo:                "example/repo",
		ExistingPRsChecked:  true,
		RealProblemVerified: false,
		GateStatus:          domainskill.GateStatusBlocked,
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveContributionGateLog failed: %v", err)
	}
	if err := store.SaveExternalPRSubmitRecord(ctx, domainskill.ExternalPRSubmitRecord{
		SubmitID:            "submit_1",
		ContributionEventID: "evt_contrib_1",
		Repo:                "example/repo",
		Title:               "Fix bug",
		SubmitStatus:        domainskill.ExternalPRSubmitStatusBlocked,
		FailureReason:       "external PR adapter is not configured",
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("SaveExternalPRSubmitRecord failed: %v", err)
	}
	if err := store.SaveCoderTranscriptEntry(ctx, domainskill.CoderTranscriptEntry{
		EventID:   "evt_coder_transcript_1",
		JobID:     "job-1",
		Route:     "CODE3",
		Agent:     "Coder",
		Role:      "coder",
		Segment:   "plan",
		Text:      "complete diff と検証結果を提示する",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveCoderTranscriptEntry failed: %v", err)
	}
	manifests, err := store.ListSkillManifests(ctx, 10)
	if err != nil || len(manifests) != 1 || manifests[0].SkillID != "core.pr-readiness" {
		t.Fatalf("manifests=%#v err=%v", manifests, err)
	}
	triggers, err := store.ListSkillTriggerLogs(ctx, 10)
	if err != nil || len(triggers) != 1 || triggers[0].EventID != "evt_skill_1" {
		t.Fatalf("triggers=%#v err=%v", triggers, err)
	}
	changes, err := store.ListSkillChangeLogs(ctx, 10)
	if err != nil || len(changes) != 1 || changes[0].ChangeID != "chg_1" {
		t.Fatalf("changes=%#v err=%v", changes, err)
	}
	gates, err := store.ListContributionGateLogs(ctx, 10)
	if err != nil || len(gates) != 1 || gates[0].EventID != "evt_contrib_1" {
		t.Fatalf("gates=%#v err=%v", gates, err)
	}
	submits, err := store.ListExternalPRSubmitRecords(ctx, 10)
	if err != nil || len(submits) != 1 || submits[0].SubmitID != "submit_1" {
		t.Fatalf("submits=%#v err=%v", submits, err)
	}
	transcripts, err := store.ListCoderTranscriptEntries(ctx, 10)
	if err != nil || len(transcripts) != 1 || transcripts[0].EventID != "evt_coder_transcript_1" {
		t.Fatalf("transcripts=%#v err=%v", transcripts, err)
	}
}

func TestSQLiteStoreMissingRowsReturnEmptyLists(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "skill_governance.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	if items, err := store.ListSkillManifests(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("manifests=%#v err=%v", items, err)
	}
	if items, err := store.ListSkillTriggerLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("triggers=%#v err=%v", items, err)
	}
	if items, err := store.ListSkillChangeLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("changes=%#v err=%v", items, err)
	}
	if items, err := store.ListContributionGateLogs(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("gates=%#v err=%v", items, err)
	}
	if items, err := store.ListExternalPRSubmitRecords(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("submits=%#v err=%v", items, err)
	}
	if items, err := store.ListCoderTranscriptEntries(ctx, 10); err != nil || len(items) != 0 {
		t.Fatalf("transcripts=%#v err=%v", items, err)
	}
}

func TestSQLiteStoreFindContributionGateByIDUsesExactPrimaryKey(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "skill_governance.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	item := domainskill.ContributionGateLog{
		EventID: "event-exact", Repo: "example/repo", ProblemStatement: "problem", TestResult: "go test",
		GateStatus: domainskill.GateStatusBlocked, CreatedAt: now,
	}
	if err := store.SaveContributionGateLog(ctx, item); err != nil {
		t.Fatalf("SaveContributionGateLog failed: %v", err)
	}
	if err := store.SaveContributionGateLog(ctx, domainskill.ContributionGateLog{
		EventID: "event-exact-suffix", Repo: "example/repo", ProblemStatement: "suffix", TestResult: "go test",
		GateStatus: domainskill.GateStatusBlocked, CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("SaveContributionGateLog suffix failed: %v", err)
	}
	got, found, err := store.FindContributionGateByID(ctx, item.EventID)
	if err != nil || !found || got.EventID != item.EventID || got.ProblemStatement != item.ProblemStatement {
		t.Fatalf("FindContributionGateByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if _, found, err := store.FindContributionGateByID(ctx, "missing"); err != nil || found {
		t.Fatalf("missing FindContributionGateByID() found=%v err=%v", found, err)
	}
}
