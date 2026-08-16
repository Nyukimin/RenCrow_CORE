package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestL1SQLiteStore_RawLifecycleRequiresArchiveStoreBeforeDelete(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-120 * 24 * time.Hour)
	if err := store.SaveMessage(ctx, "session-raw-no-archive", 1, "conv:raw-no-archive", domconv.NewMessage(domconv.SpeakerUser, "raw source must survive", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = ?`, old, old, "conv:raw-no-archive"); err != nil {
		t.Fatalf("backdate raw memory failed: %v", err)
	}
	markProfilePromotionCompletedForNamespace(t, store, "conv:raw-no-archive")
	var eventID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM l1_memory_event WHERE namespace = ?`, "conv:raw-no-archive").Scan(&eventID); err != nil {
		t.Fatalf("read raw memory id failed: %v", err)
	}

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                      now,
		RawConversationRetention: 30 * 24 * time.Hour,
		RawCompactLimit:          1,
		CandidateReviewAfter:     0,
		MonthlyHighlightAfter:    0,
		ThreadSummarySeedAfter:   0,
		DecayAfter:               0,
		VectorCleanupLimit:       1,
	})
	if err == nil {
		t.Fatalf("RunMemoryLifecycleMaintenance succeeded without archive store: result=%+v", result)
	}
	if !errors.Is(err, domainmemory.ErrUserMemoryOwnerUnavailable) {
		t.Fatalf("RunMemoryLifecycleMaintenance error=%v, want unavailable", err)
	}
	if got := countMemoryEventsByNamespace(t, ctx, store, "conv:raw-no-archive"); got != 1 {
		t.Fatalf("raw source count=%d, want 1", got)
	}
	var outboxCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_lifecycle_raw_archive_outbox WHERE event_id = ?`, eventID).Scan(&outboxCount); err != nil {
		t.Fatalf("raw archive outbox query failed: %v", err)
	}
	if outboxCount != 1 {
		t.Fatalf("raw archive outbox count=%d, want 1", outboxCount)
	}
}

func TestL1SQLiteStore_RawLifecycleArchiveSuccessDeletesOnlyAfterReceipt(t *testing.T) {
	ctx := context.Background()
	store, eventID, now := newOldRawLifecycleStore(t, "conv:raw-success")
	defer store.Close()
	archive := &recordingRawLifecycleArchiveStub{}
	store.WithArchiveStore(archive)

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now, RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 1,
		VectorCleanupLimit: 1,
	})
	if err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.RawCompacted != 1 {
		t.Fatalf("RawCompacted=%d, want 1", result.RawCompacted)
	}
	if len(archive.events) != 1 || len(archive.receipts) != 1 {
		t.Fatalf("archive calls events=%d receipts=%d, want 1/1", len(archive.events), len(archive.receipts))
	}
	var status, hash string
	var attemptCount int
	if err := store.db.QueryRowContext(ctx, `
SELECT status, event_sha256, attempt_count
FROM conversation_lifecycle_raw_archive_outbox WHERE event_id = ?`, eventID).Scan(&status, &hash, &attemptCount); err != nil {
		t.Fatalf("read archived raw outbox failed: %v", err)
	}
	if status != L1RawLifecycleArchiveOutboxStatusArchived || attemptCount != 1 {
		t.Fatalf("raw outbox status=%q attempt_count=%d, want archived/1", status, attemptCount)
	}
	wantHash, err := CanonicalL1MemoryEventSHA256(archive.events[0])
	if err != nil {
		t.Fatalf("hash archived event: %v", err)
	}
	if hash != wantHash || archive.receipts[0].EventSHA256 != wantHash {
		t.Fatalf("archive hash=%q receipt hash=%q want %q", hash, archive.receipts[0].EventSHA256, wantHash)
	}
	if got := countMemoryEventsByNamespace(t, ctx, store, "conv:raw-success"); got != 0 {
		t.Fatalf("raw source count=%d, want 0 after verified archive", got)
	}
	var auditCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type = ? AND namespace = ?`, "memory.l1_raw_archived_compacted", "conv:lifecycle").Scan(&auditCount); err != nil {
		t.Fatalf("read raw archive finalize audit failed: %v", err)
	}
	if auditCount != 1 {
		t.Fatalf("raw archive finalize audit count=%d, want 1", auditCount)
	}
}

func TestL1SQLiteStore_RawLifecycleArchiveFailureRetainsSourceAndMarksOutbox(t *testing.T) {
	ctx := context.Background()
	store, eventID, now := newOldRawLifecycleStore(t, "conv:raw-failure")
	defer store.Close()
	archiveErr := errors.New("archive write failed")
	archive := &recordingRawLifecycleArchiveStub{err: archiveErr}
	store.WithArchiveStore(archive)

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now, RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 1,
		VectorCleanupLimit: 1,
	})
	if err == nil {
		t.Fatalf("RunMemoryLifecycleMaintenance succeeded: result=%+v", result)
	}
	if result != nil && result.RawCompacted != 0 {
		t.Fatalf("RawCompacted=%d after archive failure, want 0", result.RawCompacted)
	}
	if got := countMemoryEventsByNamespace(t, ctx, store, "conv:raw-failure"); got != 1 {
		t.Fatalf("raw source count=%d, want 1", got)
	}
	var status, lastError string
	var attemptCount int
	if err := store.db.QueryRowContext(ctx, `
SELECT status, last_error, attempt_count
FROM conversation_lifecycle_raw_archive_outbox WHERE event_id = ?`, eventID).Scan(&status, &lastError, &attemptCount); err != nil {
		t.Fatalf("read failed raw outbox failed: %v", err)
	}
	if status != L1RawLifecycleArchiveOutboxStatusFailed || attemptCount != 1 || !strings.Contains(lastError, archiveErr.Error()) {
		t.Fatalf("raw outbox status=%q attempt_count=%d last_error=%q", status, attemptCount, lastError)
	}
	if len(archive.receipts) != 0 {
		t.Fatalf("failed archive unexpectedly recorded %d receipts", len(archive.receipts))
	}
}

func TestL1SQLiteStore_RawLifecycleArchiveFailedOutboxReplaysAfterRestart(t *testing.T) {
	ctx := context.Background()
	store, eventID, now := newOldRawLifecycleStore(t, "conv:raw-replay")
	defer store.Close()
	archive := &recordingRawLifecycleArchiveStub{err: errors.New("temporary archive failure")}
	store.WithArchiveStore(archive)
	if _, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now, RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 1,
		VectorCleanupLimit: 1,
	}); err == nil {
		t.Fatal("first lifecycle run unexpectedly succeeded")
	}
	archive.err = nil
	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now.Add(time.Minute), RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 1,
		VectorCleanupLimit: 1,
	})
	if err != nil {
		t.Fatalf("replay lifecycle run failed: %v", err)
	}
	if result.RawCompacted != 1 {
		t.Fatalf("replay RawCompacted=%d, want 1", result.RawCompacted)
	}
	if len(archive.events) != 1 || archive.receipts[0].EventID != eventID {
		t.Fatalf("replay archive calls=%d receipt=%+v", len(archive.events), archive.receipts)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM conversation_lifecycle_raw_archive_outbox WHERE event_id = ?`, eventID).Scan(&status); err != nil {
		t.Fatalf("read replay outbox failed: %v", err)
	}
	if status != L1RawLifecycleArchiveOutboxStatusArchived {
		t.Fatalf("replay outbox status=%q, want archived", status)
	}
}

func TestL1SQLiteStore_RawLifecycleArchiveHashDriftNeverDeletesSource(t *testing.T) {
	ctx := context.Background()
	store, eventID, now := newOldRawLifecycleStore(t, "conv:raw-drift")
	defer store.Close()
	archive := &recordingRawLifecycleArchiveStub{}
	archive.beforeReturn = func(_ L1MemoryEvent, _ L1RawLifecycleArchiveReceipt) {
		if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET message = ? WHERE id = ?`, "drifted source", eventID); err != nil {
			t.Fatalf("mutate source in archive stub: %v", err)
		}
	}
	store.WithArchiveStore(archive)

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now: now, RawConversationRetention: 30 * 24 * time.Hour, RawCompactLimit: 1,
		VectorCleanupLimit: 1,
	})
	if err == nil {
		t.Fatalf("hash drift lifecycle run succeeded: result=%+v", result)
	}
	if !errors.Is(err, ErrL1RawLifecycleArchiveConflict) {
		t.Fatalf("hash drift error=%v, want conflict", err)
	}
	if got := countMemoryEventsByNamespace(t, ctx, store, "conv:raw-drift"); got != 1 {
		t.Fatalf("hash drift source count=%d, want 1", got)
	}
	var status string
	if err := store.db.QueryRowContext(ctx, `SELECT status FROM conversation_lifecycle_raw_archive_outbox WHERE event_id = ?`, eventID).Scan(&status); err != nil {
		t.Fatalf("read drift outbox failed: %v", err)
	}
	if status != L1RawLifecycleArchiveOutboxStatusFailed {
		t.Fatalf("hash drift outbox status=%q, want failed", status)
	}
}

func newOldRawLifecycleStore(t *testing.T, namespace string) (*L1SQLiteStore, string, time.Time) {
	t.Helper()
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-120 * 24 * time.Hour)
	if err := store.SaveMessage(ctx, "raw-lifecycle-session", 1, namespace, domconv.NewMessage(domconv.SpeakerUser, "raw lifecycle source", nil), MemoryStateObserved); err != nil {
		store.Close()
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = ?`, old, old, namespace); err != nil {
		store.Close()
		t.Fatalf("backdate raw source failed: %v", err)
	}
	markProfilePromotionCompletedForNamespace(t, store, namespace)
	var eventID string
	if err := store.db.QueryRowContext(ctx, `SELECT id FROM l1_memory_event WHERE namespace = ?`, namespace).Scan(&eventID); err != nil {
		store.Close()
		t.Fatalf("read raw source id failed: %v", err)
	}
	return store, eventID, now
}

func TestL1SQLiteStore_RunMemoryLifecycleMaintenance(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	store.WithArchiveStore(&recordingRawLifecycleArchiveStub{})

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-120 * 24 * time.Hour)
	if err := store.SaveMessage(ctx, "session-old", 1, "conv:1", domconv.NewMessage(domconv.SpeakerUser, "old raw", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = 'conv:1'`, old, old); err != nil {
		t.Fatalf("backdate conv memory failed: %v", err)
	}
	markProfilePromotionCompletedForNamespace(t, store, "conv:1")

	candidate, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:      "ren",
		Type:        domainmemory.UserMemoryTypePreference,
		Statement:   "candidate review",
		State:       MemoryStateCandidate,
		Sensitivity: "normal",
		Scope:       "all_personas",
	})
	if err != nil {
		t.Fatalf("Create candidate failed: %v", err)
	}
	backdateMemory(t, store, candidate.ID, old)

	confirmed, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "old confirmed",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-confirmed"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create confirmed failed: %v", err)
	}
	backdateMemory(t, store, confirmed.ID, old)

	pinned, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypeConstraint,
		Statement:        "old pinned",
		State:            MemoryStatePinned,
		EvidenceEventIDs: []string{"evt-pinned"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create pinned failed: %v", err)
	}
	backdateMemory(t, store, pinned.ID, old)

	forgotten, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "forgotten",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-forgotten"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create forgotten failed: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, forgotten.ID, "test"); err != nil {
		t.Fatalf("ForgetUserMemory failed: %v", err)
	}

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                      now,
		RawConversationRetention: 30 * 24 * time.Hour,
		CandidateReviewAfter:     7 * 24 * time.Hour,
		DecayAfter:               90 * 24 * time.Hour,
		RawCompactLimit:          10,
		CandidateReviewLimit:     10,
		DecayLimit:               10,
		VectorCleanupLimit:       10,
	})
	if err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.RawCompacted != 1 || result.CandidatesQueued != 1 || result.Decayed != 1 || result.VectorCleanupQueued != 1 {
		t.Fatalf("unexpected lifecycle result: %+v", result)
	}

	candidateEvent, err := store.memoryByID(ctx, candidate.ID)
	if err != nil {
		t.Fatalf("candidate memory missing: %v", err)
	}
	if got := metaStringValue(candidateEvent.Meta, "review_status"); got != "queued" {
		t.Fatalf("candidate review_status=%q, want queued", got)
	}
	confirmedEvent, err := store.memoryByID(ctx, confirmed.ID)
	if err != nil {
		t.Fatalf("confirmed memory missing: %v", err)
	}
	confirmedMemory := l1EventToUserMemory(*confirmedEvent)
	if confirmedMemory.LifecycleStatus != "decayed" || domainmemory.IsUserMemoryPromptInjectable(*confirmedMemory, "mio") {
		t.Fatalf("confirmed memory should be decayed and not prompt-injectable: %+v", confirmedMemory)
	}
	pinnedEvent, err := store.memoryByID(ctx, pinned.ID)
	if err != nil {
		t.Fatalf("pinned memory missing: %v", err)
	}
	if got := metaStringValue(pinnedEvent.Meta, "lifecycle_status"); got != "" {
		t.Fatalf("pinned memory should not decay, got lifecycle_status=%q", got)
	}
	forgottenEvent, err := store.memoryByID(ctx, forgotten.ID)
	if err != nil {
		t.Fatalf("forgotten memory missing: %v", err)
	}
	if got := metaStringValue(forgottenEvent.Meta, "vector_cleanup_status"); got != "queued" {
		t.Fatalf("forgotten memory vector_cleanup_status=%q, want queued", got)
	}
}

func TestL1SQLiteStore_RunMemoryLifecycleMaintenancePreservesChatGPTL3Episodes(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	store.WithArchiveStore(&recordingRawLifecycleArchiveStub{})

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	old := now.Add(-120 * 24 * time.Hour)
	if err := store.SaveMessage(ctx, "session-old", 1, "conv:l1", domconv.NewMessage(domconv.SpeakerUser, "old L1 raw", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage L1 failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = 'conv:l1'`, old, old); err != nil {
		t.Fatalf("backdate L1 memory failed: %v", err)
	}
	markProfilePromotionCompletedForNamespace(t, store, "conv:l1")

	if err := store.SaveMessage(ctx, "session-chatgpt", 1, "conv:chatgpt-l3", domconv.NewMessage(domconv.SpeakerSystem, "immutable ChatGPT L3 episode", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage ChatGPT episode failed: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `
UPDATE l1_memory_event
SET layer = ?, source = ?, created_at = ?, updated_at = ?
WHERE namespace = 'conv:chatgpt-l3'`, "L3", "chatgpt_export", old, old); err != nil {
		t.Fatalf("prepare ChatGPT L3 episode failed: %v", err)
	}

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                      now,
		RawConversationRetention: 30 * 24 * time.Hour,
		RawCompactLimit:          10,
		VectorCleanupLimit:       10,
	})
	if err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.RawCompacted != 1 {
		t.Fatalf("unexpected raw compacted count: %d, want 1", result.RawCompacted)
	}

	if got := countMemoryEventsByNamespace(t, ctx, store, "conv:l1"); got != 0 {
		t.Fatalf("old L1 raw event count=%d, want 0", got)
	}
	var layer, source string
	if err := store.db.QueryRowContext(ctx, `
SELECT layer, source FROM l1_memory_event WHERE namespace = 'conv:chatgpt-l3'`).Scan(&layer, &source); err != nil {
		t.Fatalf("ChatGPT L3 episode should remain: %v", err)
	}
	if layer != "L3" || source != "chatgpt_export" {
		t.Fatalf("ChatGPT episode layer/source=(%q, %q), want (L3, chatgpt_export)", layer, source)
	}
}

func TestL1SQLiteStore_RunMemoryLifecycleMaintenanceExecutesVectorCleanup(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	sink := &stubVectorCleanupSink{}
	store.WithVectorCleanupSink(sink)

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	forgotten, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "forgotten vector memory",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-forgotten"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create forgotten failed: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, forgotten.ID, "test"); err != nil {
		t.Fatalf("ForgetUserMemory failed: %v", err)
	}

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                    now,
		RawCompactLimit:        10,
		CandidateReviewLimit:   10,
		MonthlyHighlightLimit:  10,
		ThreadSummarySeedLimit: 10,
		DecayLimit:             10,
		VectorCleanupLimit:     10,
	})
	if err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.VectorCleanupQueued != 1 || result.VectorCleanupExecuted != 1 {
		t.Fatalf("unexpected vector cleanup result: %+v", result)
	}
	if len(sink.items) != 1 || sink.items[0].MemoryID != forgotten.ID {
		t.Fatalf("unexpected cleanup sink items: %+v", sink.items)
	}
	forgottenEvent, err := store.memoryByID(ctx, forgotten.ID)
	if err != nil {
		t.Fatalf("forgotten memory missing: %v", err)
	}
	if got := metaStringValue(forgottenEvent.Meta, "vector_cleanup_status"); got != "done" {
		t.Fatalf("vector cleanup status=%q, want done", got)
	}
}

func TestL1SQLiteStore_RunMemoryLifecycleMaintenanceBuildsMonthlyHighlightsAndThreadSeeds(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	insertDailyDigest(t, store, "digest:2026-05-01:ai", "2026-05-01", "ai", "day", "AI daily one")
	insertDailyDigest(t, store, "digest:2026-05-02:ai", "2026-05-02", "ai", "day", "AI daily two")
	insertThreadSummary(t, store, "summary-1", "conv:thread", 42, "thread summary for May", now.Add(-20*24*time.Hour))

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                    now,
		MonthlyHighlightAfter:  7 * 24 * time.Hour,
		ThreadSummarySeedAfter: 7 * 24 * time.Hour,
		MonthlyHighlightLimit:  10,
		ThreadSummarySeedLimit: 10,
		RawCompactLimit:        10,
		CandidateReviewLimit:   10,
		DecayLimit:             10,
		VectorCleanupLimit:     10,
	})
	if err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.MonthlyHighlightsBuilt != 1 || result.ThreadSummarySeedsQueued != 1 {
		t.Fatalf("unexpected lifecycle result: %+v", result)
	}
	var highlight string
	if err := store.db.QueryRowContext(ctx, `SELECT highlight_text FROM l1_monthly_highlight WHERE month = ? AND category = ?`, "2026-05", "ai").Scan(&highlight); err != nil {
		t.Fatalf("monthly highlight missing: %v", err)
	}
	if !strings.Contains(highlight, "AI daily one") || !strings.Contains(highlight, "AI daily two") {
		t.Fatalf("monthly highlight did not include daily digests: %q", highlight)
	}
	summaryEvent, err := store.memoryByID(ctx, "summary-1")
	if err != nil {
		t.Fatalf("thread summary missing: %v", err)
	}
	if got := metaStringValue(summaryEvent.Meta, "monthly_highlight_seed_status"); got != "queued" {
		t.Fatalf("thread summary seed status=%q, want queued", got)
	}

	result, err = store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                    now,
		MonthlyHighlightAfter:  7 * 24 * time.Hour,
		ThreadSummarySeedAfter: 7 * 24 * time.Hour,
		MonthlyHighlightLimit:  10,
		ThreadSummarySeedLimit: 10,
		RawCompactLimit:        10,
		CandidateReviewLimit:   10,
		DecayLimit:             10,
		VectorCleanupLimit:     10,
	})
	if err != nil {
		t.Fatalf("second RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.MonthlyHighlightsBuilt != 0 || result.ThreadSummarySeedsQueued != 0 {
		t.Fatalf("monthly highlight and thread seed should be idempotent, got %+v", result)
	}
}

func TestL1SQLiteStore_RunMemoryLifecycleMaintenanceUsesDomainDecayPolicy(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	project, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypeProject,
		Statement:        "project memory survives normal decay",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-project"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create project failed: %v", err)
	}
	backdateMemory(t, store, project.ID, now.Add(-120*24*time.Hour))
	episode, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypeEpisode,
		Statement:        "episode memory decays sooner",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-episode"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create episode failed: %v", err)
	}
	backdateMemory(t, store, episode.ID, now.Add(-45*24*time.Hour))

	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
		Now:                    now,
		DecayAfter:             90 * 24 * time.Hour,
		RawCompactLimit:        10,
		CandidateReviewLimit:   10,
		MonthlyHighlightLimit:  10,
		ThreadSummarySeedLimit: 10,
		DecayLimit:             10,
		VectorCleanupLimit:     10,
	})
	if err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance failed: %v", err)
	}
	if result.Decayed != 1 {
		t.Fatalf("unexpected decay count: %+v", result)
	}
	projectEvent, err := store.memoryByID(ctx, project.ID)
	if err != nil {
		t.Fatalf("project memory missing: %v", err)
	}
	if got := metaStringValue(projectEvent.Meta, "lifecycle_status"); got != "" {
		t.Fatalf("project memory should not decay at 120 days, got %q", got)
	}
	episodeEvent, err := store.memoryByID(ctx, episode.ID)
	if err != nil {
		t.Fatalf("episode memory missing: %v", err)
	}
	if got := metaStringValue(episodeEvent.Meta, "lifecycle_status"); got != "decayed" {
		t.Fatalf("episode lifecycle_status=%q, want decayed", got)
	}
	if got := metaStringValue(episodeEvent.Meta, "decay_policy"); got != "short" {
		t.Fatalf("episode decay_policy=%q, want short", got)
	}
}

func TestL1SQLiteStore_AcceleratedVerificationDB(t *testing.T) {
	dbPath := strings.TrimSpace(os.Getenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_VERIFY_DB"))
	if dbPath == "" {
		t.Skip("set RENCROW_MEMORY_LIFECYCLE_ACCEL_VERIFY_DB to create a persistent accelerated lifecycle verification DB")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		t.Fatalf("create verification DB dir failed: %v", err)
	}
	if err := os.Remove(dbPath); err != nil && !os.IsNotExist(err) {
		t.Fatalf("remove old verification DB failed: %v", err)
	}

	ctx := context.Background()
	store, err := NewL1SQLiteStore(dbPath)
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	store.WithArchiveStore(&recordingRawLifecycleArchiveStub{})
	store.WithVectorCleanupSink(&stubVectorCleanupSink{})

	base := time.Now().UTC()
	if err := store.SaveMessage(ctx, "accel-session", 1, "conv:accel", domconv.NewMessage(domconv.SpeakerUser, "accelerated raw memory", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	if _, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:      "accel-user",
		Type:        domainmemory.UserMemoryTypePreference,
		Statement:   "accelerated candidate review target",
		State:       MemoryStateCandidate,
		Sensitivity: "normal",
		Scope:       "all_personas",
	}); err != nil {
		t.Fatalf("Create candidate failed: %v", err)
	}
	if _, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "accel-user",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "accelerated decay target",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-accel-decay"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	}); err != nil {
		t.Fatalf("Create confirmed failed: %v", err)
	}
	forgotten, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "accel-user",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "accelerated vector cleanup target",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-accel-cleanup"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create forgotten target failed: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, forgotten.ID, "accelerated verification"); err != nil {
		t.Fatalf("ForgetUserMemory failed: %v", err)
	}
	insertDailyDigest(t, store, "digest:accel:ai", base.Format("2006-01-02"), "ai", "day", "accelerated daily digest")
	insertThreadSummary(t, store, "summary-accel", "conv:thread", 99, "accelerated thread summary", base)

	totals := MemoryLifecycleResult{}
	months := acceleratedVerificationMonths(t)
	for tick := 0; tick <= months; tick++ {
		simNow := base.Add(time.Duration(tick) * 30 * 24 * time.Hour)
		result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
			Now:                      simNow,
			RawConversationRetention: 30 * 24 * time.Hour,
			CandidateReviewAfter:     7 * 24 * time.Hour,
			MonthlyHighlightAfter:    14 * 24 * time.Hour,
			ThreadSummarySeedAfter:   14 * 24 * time.Hour,
			DecayAfter:               90 * 24 * time.Hour,
			RawCompactLimit:          100,
			CandidateReviewLimit:     100,
			MonthlyHighlightLimit:    100,
			ThreadSummarySeedLimit:   100,
			DecayLimit:               100,
			VectorCleanupLimit:       100,
		})
		if err != nil {
			t.Fatalf("accelerated tick %d failed: %v", tick, err)
		}
		totals.RawCompacted += result.RawCompacted
		totals.CandidatesQueued += result.CandidatesQueued
		totals.MonthlyHighlightsBuilt += result.MonthlyHighlightsBuilt
		totals.ThreadSummarySeedsQueued += result.ThreadSummarySeedsQueued
		totals.Decayed += result.Decayed
		totals.VectorCleanupQueued += result.VectorCleanupQueued
		totals.VectorCleanupExecuted += result.VectorCleanupExecuted
		time.Sleep(100 * time.Millisecond)
	}
	if totals.RawCompacted == 0 || totals.CandidatesQueued == 0 || totals.MonthlyHighlightsBuilt == 0 || totals.ThreadSummarySeedsQueued == 0 || totals.Decayed == 0 || totals.VectorCleanupQueued == 0 || totals.VectorCleanupExecuted == 0 {
		t.Fatalf("accelerated verification did not exercise every lifecycle path: %+v", totals)
	}
	t.Logf("accelerated lifecycle verification DB=%s months=%d totals=%+v", dbPath, months, totals)
}

func TestL1SQLiteStore_MemoryRetentionQualityEvalOneYear(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	store.WithArchiveStore(&recordingRawLifecycleArchiveStub{})
	store.WithVectorCleanupSink(&stubVectorCleanupSink{})

	base := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	mustKeepConstraint, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypeConstraint,
		Statement:        "must_keep: ユーザーは結論から短く答えることを継続指示している",
		State:            MemoryStatePinned,
		EvidenceEventIDs: []string{"evt-keep-constraint"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create must_keep constraint failed: %v", err)
	}
	mustKeepProject, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypeProject,
		Statement:        "must_keep: RenCrow の記憶品質評価を継続改善している",
		State:            MemoryStatePinned,
		EvidenceEventIDs: []string{"evt-keep-project"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create must_keep project failed: %v", err)
	}
	if err := store.SaveMessage(ctx, "quality-session", 1, "conv:quality-noise", domconv.NewMessage(domconv.SpeakerUser, "must_compact_or_forget: 一時的なCPU確認ログ", nil), MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage noise failed: %v", err)
	}
	mustDecayEpisode, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypeEpisode,
		Statement:        "must_compact_or_forget: その場限りの障害調査メモ",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-decay-episode"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create must_decay episode failed: %v", err)
	}
	mustNotInjectSensitive, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:      "ren",
		Type:        domainmemory.UserMemoryTypeSensitive,
		Statement:   "must_not_inject: sensitive candidate",
		State:       MemoryStateCandidate,
		Sensitivity: "sensitive",
		Scope:       "all_personas",
	})
	if err != nil {
		t.Fatalf("Create sensitive candidate failed: %v", err)
	}
	mustNotInjectForgotten, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "must_not_inject: user forgot this preference",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-forgotten-quality"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create forgotten quality target failed: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, mustNotInjectForgotten.ID, "quality eval"); err != nil {
		t.Fatalf("ForgetUserMemory failed: %v", err)
	}
	mustNotInjectSuperseded, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "must_not_inject: old superseded preference",
		State:            MemoryStateConfirmed,
		EvidenceEventIDs: []string{"evt-old-quality"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create superseded old target failed: %v", err)
	}
	replacement, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID:           "ren",
		Type:             domainmemory.UserMemoryTypePreference,
		Statement:        "replacement: newer preference",
		State:            MemoryStatePinned,
		EvidenceEventIDs: []string{"evt-new-quality"},
		Sensitivity:      "normal",
		Scope:            "all_personas",
		Source:           "user_explicit",
	})
	if err != nil {
		t.Fatalf("Create supersede replacement failed: %v", err)
	}
	if _, err := store.SupersedeUserMemory(ctx, mustNotInjectSuperseded.ID, replacement.ID, "quality eval"); err != nil {
		t.Fatalf("SupersedeUserMemory failed: %v", err)
	}

	for _, id := range []string{mustKeepConstraint.ID, mustKeepProject.ID, mustDecayEpisode.ID, mustNotInjectSensitive.ID, mustNotInjectSuperseded.ID, replacement.ID} {
		backdateMemory(t, store, id, base)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE namespace = ?`, base, base, "conv:quality-noise"); err != nil {
		t.Fatalf("backdate noise memory failed: %v", err)
	}
	markProfilePromotionCompletedForNamespace(t, store, "conv:quality-noise")

	runLifecycleMonths(t, ctx, store, base, 12)

	eval := evaluateMemoryRetentionQuality(t, ctx, store, memoryRetentionQualityFixture{
		UserID:                 "ren",
		Persona:                "mio",
		MustKeepIDs:            []string{mustKeepConstraint.ID, mustKeepProject.ID, replacement.ID},
		MustCompactNamespaces:  []string{"conv:quality-noise"},
		MustDecayIDs:           []string{mustDecayEpisode.ID},
		MustNotInjectIDs:       []string{mustDecayEpisode.ID, mustNotInjectSensitive.ID, mustNotInjectForgotten.ID, mustNotInjectSuperseded.ID},
		MustCleanupVectorIDs:   []string{mustNotInjectForgotten.ID, mustNotInjectSuperseded.ID},
		ExpectedMinimumQuality: 1.0,
	})
	if !eval.Passed {
		t.Fatalf("memory retention quality eval failed: score=%.2f failures=%v", eval.Score, eval.Failures)
	}
	t.Logf("memory retention quality eval passed: score=%.2f", eval.Score)
}

func markProfilePromotionCompletedForNamespace(t *testing.T, store *L1SQLiteStore, namespace string) {
	t.Helper()
	if _, err := store.db.Exec(`
UPDATE l1_profile_promotion_job
SET state = ?
WHERE evidence_event_id IN (
	SELECT id FROM l1_memory_event WHERE namespace = ?
)`, domainmemory.ProfilePromotionCompleted, namespace); err != nil {
		t.Fatalf("mark profile promotion completed for %s: %v", namespace, err)
	}
}

func acceleratedVerificationMonths(t *testing.T) int {
	t.Helper()
	raw := strings.TrimSpace(os.Getenv("RENCROW_MEMORY_LIFECYCLE_ACCEL_VERIFY_MONTHS"))
	if raw == "" {
		return 4
	}
	months, err := strconv.Atoi(raw)
	if err != nil || months <= 0 {
		t.Fatalf("invalid RENCROW_MEMORY_LIFECYCLE_ACCEL_VERIFY_MONTHS=%q", raw)
	}
	return months
}

type memoryRetentionQualityFixture struct {
	UserID                 string
	Persona                string
	MustKeepIDs            []string
	MustCompactNamespaces  []string
	MustDecayIDs           []string
	MustNotInjectIDs       []string
	MustCleanupVectorIDs   []string
	ExpectedMinimumQuality float64
}

type memoryRetentionQualityResult struct {
	Passed   bool
	Score    float64
	Failures []string
}

func runLifecycleMonths(t *testing.T, ctx context.Context, store *L1SQLiteStore, base time.Time, months int) {
	t.Helper()
	for tick := 0; tick <= months; tick++ {
		simNow := base.Add(time.Duration(tick) * 30 * 24 * time.Hour)
		if _, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{
			Now:                      simNow,
			RawConversationRetention: 30 * 24 * time.Hour,
			CandidateReviewAfter:     7 * 24 * time.Hour,
			MonthlyHighlightAfter:    14 * 24 * time.Hour,
			ThreadSummarySeedAfter:   14 * 24 * time.Hour,
			DecayAfter:               90 * 24 * time.Hour,
			RawCompactLimit:          100,
			CandidateReviewLimit:     100,
			MonthlyHighlightLimit:    100,
			ThreadSummarySeedLimit:   100,
			DecayLimit:               100,
			VectorCleanupLimit:       100,
		}); err != nil {
			t.Fatalf("RunMemoryLifecycleMaintenance month %d failed: %v", tick, err)
		}
	}
}

func evaluateMemoryRetentionQuality(t *testing.T, ctx context.Context, store *L1SQLiteStore, fixture memoryRetentionQualityFixture) memoryRetentionQualityResult {
	t.Helper()
	injectable, err := store.ListPromptInjectableUserMemories(ctx, fixture.UserID, fixture.Persona, 100)
	if err != nil {
		t.Fatalf("ListPromptInjectableUserMemories failed: %v", err)
	}
	injected := map[string]bool{}
	for _, mem := range injectable {
		injected[mem.ID] = true
	}
	checks := 0
	failures := []string{}
	fail := func(format string, args ...interface{}) {
		failures = append(failures, fmt.Sprintf(format, args...))
	}
	for _, id := range fixture.MustKeepIDs {
		checks++
		if !injected[id] {
			fail("must_keep memory %s is not prompt injectable", id)
		}
	}
	for _, namespace := range fixture.MustCompactNamespaces {
		checks++
		if countMemoryEventsByNamespace(t, ctx, store, namespace) != 0 {
			fail("must_compact namespace %s still has raw L1 events", namespace)
		}
	}
	for _, id := range fixture.MustDecayIDs {
		checks++
		ev, err := store.memoryByID(ctx, id)
		if err != nil {
			fail("must_decay memory %s missing: %v", id, err)
			continue
		}
		if got := metaStringValue(ev.Meta, "lifecycle_status"); got != "decayed" {
			fail("must_decay memory %s lifecycle_status=%q, want decayed", id, got)
		}
	}
	for _, id := range fixture.MustNotInjectIDs {
		checks++
		if injected[id] {
			fail("must_not_inject memory %s leaked into prompt injectable set", id)
		}
	}
	for _, id := range fixture.MustCleanupVectorIDs {
		checks++
		ev, err := store.memoryByID(ctx, id)
		if err != nil {
			fail("must_cleanup_vector memory %s missing: %v", id, err)
			continue
		}
		if got := metaStringValue(ev.Meta, "vector_cleanup_status"); got != "done" {
			fail("must_cleanup_vector memory %s vector_cleanup_status=%q, want done", id, got)
		}
	}
	passedChecks := checks - len(failures)
	score := 1.0
	if checks > 0 {
		score = float64(passedChecks) / float64(checks)
	}
	return memoryRetentionQualityResult{
		Passed:   len(failures) == 0 && score >= fixture.ExpectedMinimumQuality,
		Score:    score,
		Failures: failures,
	}
}

type recordingRawLifecycleArchiveStub struct {
	events       []L1MemoryEvent
	receipts     []L1RawLifecycleArchiveReceipt
	err          error
	beforeReturn func(L1MemoryEvent, L1RawLifecycleArchiveReceipt)
}

func (s *recordingRawLifecycleArchiveStub) ArchiveL1MemoryEvents(context.Context, []L1MemoryEvent) error {
	return nil
}

func (s *recordingRawLifecycleArchiveStub) ArchiveL1NewsItems(context.Context, []L1NewsItem) error {
	return nil
}

func (s *recordingRawLifecycleArchiveStub) ArchiveL1KnowledgeItems(context.Context, []L1KnowledgeItem) error {
	return nil
}

func (s *recordingRawLifecycleArchiveStub) ArchiveL1StagingItems(context.Context, []L1StagingItem) error {
	return nil
}

func (s *recordingRawLifecycleArchiveStub) ArchiveL1RawLifecycleEvent(_ context.Context, event L1MemoryEvent, receipt L1RawLifecycleArchiveReceipt) error {
	if s.err != nil {
		return s.err
	}
	if s.beforeReturn != nil {
		s.beforeReturn(event, receipt)
	}
	s.events = append(s.events, event)
	s.receipts = append(s.receipts, receipt)
	return nil
}

func countMemoryEventsByNamespace(t *testing.T, ctx context.Context, store *L1SQLiteStore, namespace string) int {
	t.Helper()
	var count int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM l1_memory_event WHERE namespace = ?`, namespace).Scan(&count); err != nil {
		t.Fatalf("count memory events for namespace %s failed: %v", namespace, err)
	}
	return count
}

type stubVectorCleanupSink struct {
	items []L1VectorCleanupItem
}

func (s *stubVectorCleanupSink) CleanupMemoryVectors(_ context.Context, items []L1VectorCleanupItem) (*L1VectorCleanupResult, error) {
	s.items = append(s.items, items...)
	return &L1VectorCleanupResult{Deleted: len(items)}, nil
}

func backdateMemory(t *testing.T, store *L1SQLiteStore, id string, at time.Time) {
	t.Helper()
	if _, err := store.db.ExecContext(context.Background(), `UPDATE l1_memory_event SET created_at = ?, updated_at = ? WHERE id = ?`, at.UTC(), at.UTC(), id); err != nil {
		t.Fatalf("backdate memory %s failed: %v", id, err)
	}
}

func insertDailyDigest(t *testing.T, store *L1SQLiteStore, id string, date string, category string, slot string, text string) {
	t.Helper()
	newsIDs, err := json.Marshal([]string{"news:" + id})
	if err != nil {
		t.Fatalf("marshal news ids failed: %v", err)
	}
	now := time.Now().UTC()
	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO l1_daily_digest (
	id, digest_date, category, digest_slot, news_ids_json, digest_text, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, id, date, category, slot, string(newsIDs), text, now, now); err != nil {
		t.Fatalf("insert daily digest failed: %v", err)
	}
}

func insertThreadSummary(t *testing.T, store *L1SQLiteStore, id string, namespace string, threadID int64, text string, at time.Time) {
	t.Helper()
	meta, err := json.Marshal(map[string]interface{}{"kind": "thread_summary"})
	if err != nil {
		t.Fatalf("marshal thread summary meta failed: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, namespace, "session-thread", threadID, string(domconv.SpeakerSystem), text, string(meta),
		MemoryStateConfirmed, "L2", "thread_summary", at.UTC(), at.UTC()); err != nil {
		t.Fatalf("insert thread summary failed: %v", err)
	}
}
