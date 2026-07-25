package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func newProfilePromotionTestStore(t *testing.T) *L1SQLiteStore {
	t.Helper()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestProfilePromotionPersistsUserRawJobsAndCompletesAtomically(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 10, "conv:10", domconv.NewMessage(domconv.SpeakerUser, "私はGoが好き", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveMessage(ctx, "ren", 10, "conv:10", domconv.NewMessage(domconv.SpeakerMio, "覚えました", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}

	batch, err := store.ClaimProfilePromotionBatch(ctx, 24, 5, time.Minute, now)
	if err != nil {
		t.Fatal(err)
	}
	if batch == nil || len(batch.Messages) != 1 || batch.Messages[0].Text != "私はGoが好き" {
		t.Fatalf("batch=%#v", batch)
	}
	saved, err := store.CompleteProfilePromotionBatch(ctx, *batch, []domainmemory.ProfileCandidate{{
		Type: domainmemory.UserMemoryTypePreference, Statement: "Goが好き", Confidence: 0.8,
	}}, "ren", now)
	if err != nil {
		t.Fatal(err)
	}
	if saved != 1 {
		t.Fatalf("saved=%d", saved)
	}
	memories, err := store.ListUserMemories(ctx, "ren", MemoryStateCandidate, true, 10)
	if err != nil || len(memories) != 1 {
		t.Fatalf("memories=%#v err=%v", memories, err)
	}
	if len(memories[0].EvidenceEventIDs) != 1 || memories[0].EvidenceEventIDs[0] != batch.Messages[0].EventID {
		t.Fatalf("evidence=%v", memories[0].EvidenceEventIDs)
	}
	jobs, err := store.ListProfilePromotionJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].State != domainmemory.ProfilePromotionCompleted {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}

	// Deterministic candidate identity prevents duplicate candidates on replay.
	if err := store.DeferProfilePromotionBatch(ctx, *batch, now); err == nil {
		t.Fatal("completed lease must not be deferred")
	}
}

func TestProfilePromotionCancelDoesNotConsumeAttemptAndFailureIsFinite(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	if err := store.SaveMessage(ctx, "ren", 11, "conv:11", domconv.NewMessage(domconv.SpeakerUser, "毎朝コーヒーを飲む", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	batch, err := store.ClaimProfilePromotionBatch(ctx, 1, 2, time.Minute, now)
	if err != nil || batch == nil {
		t.Fatalf("batch=%#v err=%v", batch, err)
	}
	if err := store.DeferProfilePromotionBatch(ctx, *batch, now); err != nil {
		t.Fatal(err)
	}
	jobs, _ := store.ListProfilePromotionJobs(ctx, 10)
	if jobs[0].State != domainmemory.ProfilePromotionPending || jobs[0].AttemptCount != 0 {
		t.Fatalf("after cancel=%#v", jobs[0])
	}

	for attempt := 1; attempt <= 2; attempt++ {
		batch, err = store.ClaimProfilePromotionBatch(ctx, 1, 2, time.Minute, now.Add(time.Duration(attempt)*time.Hour))
		if err != nil || batch == nil {
			t.Fatalf("attempt=%d batch=%#v err=%v", attempt, batch, err)
		}
		if err := store.FailProfilePromotionBatch(ctx, *batch, 2, now.Add(time.Duration(attempt)*time.Hour), "extract failed"); err != nil {
			t.Fatal(err)
		}
	}
	jobs, _ = store.ListProfilePromotionJobs(ctx, 10)
	if jobs[0].State != domainmemory.ProfilePromotionFailed || jobs[0].AttemptCount != 2 {
		t.Fatalf("after failures=%#v", jobs[0])
	}
}

func TestMemoryLifecycleKeepsRawWhileProfilePromotionIsPending(t *testing.T) {
	store := newProfilePromotionTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-60 * 24 * time.Hour)
	if err := store.SaveMessage(ctx, "ren", 12, "conv:12", domconv.NewMessage(domconv.SpeakerUser, "未処理の根拠", nil), MemoryStateObserved); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = 'conv:12'`, old, old); err != nil {
		t.Fatal(err)
	}
	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now, RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 10,
		CandidateReviewAfter: 0, MonthlyHighlightAfter: 0, ThreadSummarySeedAfter: 0,
		DecayAfter: 0, VectorCleanupLimit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RawCompacted != 0 {
		t.Fatalf("pending evidence was compacted: %+v", result)
	}
	if got := countL1Rows(t, ctx, store, `SELECT count(*) FROM l1_memory_event WHERE namespace = ?`, "conv:12"); got != 1 {
		t.Fatalf("raw rows=%d", got)
	}
}
