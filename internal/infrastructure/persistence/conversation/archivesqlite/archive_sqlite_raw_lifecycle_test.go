package archivesqlite

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	_ "modernc.org/sqlite"
)

func TestArchiveSQLiteStore_RawLifecycleArchiveIsAtomicIdempotentAndConflicting(t *testing.T) {
	ctx := context.Background()
	store, err := NewArchiveSQLiteStore(":memory:")
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer store.Close()

	event := rawLifecycleArchiveTestEvent()
	hash, err := l1sqlite.CanonicalL1MemoryEventSHA256(event)
	if err != nil {
		t.Fatalf("CanonicalL1MemoryEventSHA256 failed: %v", err)
	}
	receipt := l1sqlite.L1RawLifecycleArchiveReceipt{
		OutboxID:    l1sqlite.L1RawLifecycleOutboxID(event.ID, hash),
		EventID:     event.ID,
		EventSHA256: hash,
		CreatedAt:   time.Date(2026, 8, 16, 1, 2, 3, 4, time.UTC),
	}
	if err := store.ArchiveL1RawLifecycleEvent(ctx, event, receipt); err != nil {
		t.Fatalf("ArchiveL1RawLifecycleEvent failed: %v", err)
	}
	assertRawLifecycleArchiveCounts(t, ctx, store, 1, 1)

	if err := store.ArchiveL1RawLifecycleEvent(ctx, event, receipt); err != nil {
		t.Fatalf("idempotent ArchiveL1RawLifecycleEvent failed: %v", err)
	}
	assertRawLifecycleArchiveCounts(t, ctx, store, 1, 1)
	nilMetaEvent := event
	nilMetaEvent.ID = "raw-archive-event-nil-meta"
	nilMetaEvent.Meta = nil
	nilMetaHash, err := l1sqlite.CanonicalL1MemoryEventSHA256(nilMetaEvent)
	if err != nil {
		t.Fatalf("hash nil-meta event: %v", err)
	}
	nilMetaReceipt := l1sqlite.L1RawLifecycleArchiveReceipt{
		OutboxID:    l1sqlite.L1RawLifecycleOutboxID(nilMetaEvent.ID, nilMetaHash),
		EventID:     nilMetaEvent.ID,
		EventSHA256: nilMetaHash,
		CreatedAt:   receipt.CreatedAt,
	}
	if err := store.ArchiveL1RawLifecycleEvent(ctx, nilMetaEvent, nilMetaReceipt); err != nil {
		t.Fatalf("nil-meta archive failed: %v", err)
	}
	if err := store.ArchiveL1RawLifecycleEvent(ctx, nilMetaEvent, nilMetaReceipt); err != nil {
		t.Fatalf("nil-meta idempotent archive failed: %v", err)
	}

	conflictingEvent := event
	conflictingEvent.Message = "same ID but different archive content"
	if err := store.ArchiveL1RawLifecycleEvent(ctx, conflictingEvent, receipt); !errors.Is(err, l1sqlite.ErrL1RawLifecycleArchiveConflict) {
		t.Fatalf("conflicting event error=%v, want raw archive conflict", err)
	}
	conflictingTupleEvent := event
	conflictingTupleEvent.ThreadSeq = event.ThreadSeq + 1
	conflictingTupleHash, err := l1sqlite.CanonicalL1MemoryEventSHA256(conflictingTupleEvent)
	if err != nil {
		t.Fatalf("hash conflicting thread tuple event: %v", err)
	}
	if conflictingTupleHash == hash {
		t.Fatal("canonical raw event hash ignored the thread tuple")
	}
	if err := store.ArchiveL1RawLifecycleEvent(ctx, conflictingTupleEvent, receipt); !errors.Is(err, l1sqlite.ErrL1RawLifecycleArchiveConflict) {
		t.Fatalf("conflicting thread tuple error=%v, want raw archive conflict", err)
	}
	assertRawLifecycleArchiveCounts(t, ctx, store, 1, 1)

	invalidReceipt := receipt
	invalidReceipt.EventSHA256 = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := store.ArchiveL1RawLifecycleEvent(ctx, event, invalidReceipt); !errors.Is(err, l1sqlite.ErrL1RawLifecycleArchiveConflict) {
		t.Fatalf("invalid hash receipt error=%v, want raw archive conflict", err)
	}
	assertRawLifecycleArchiveCounts(t, ctx, store, 1, 1)

	atomicEvent := rawLifecycleArchiveTestEvent()
	atomicEvent.ID = "raw-atomic-event"
	atomicHash, err := l1sqlite.CanonicalL1MemoryEventSHA256(atomicEvent)
	if err != nil {
		t.Fatalf("hash atomic event: %v", err)
	}
	atomicReceipt := l1sqlite.L1RawLifecycleArchiveReceipt{
		OutboxID:    l1sqlite.L1RawLifecycleOutboxID(atomicEvent.ID, atomicHash),
		EventID:     atomicEvent.ID,
		EventSHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		CreatedAt:   receipt.CreatedAt,
	}
	if err := store.ArchiveL1RawLifecycleEvent(ctx, atomicEvent, atomicReceipt); !errors.Is(err, l1sqlite.ErrL1RawLifecycleArchiveConflict) {
		t.Fatalf("atomic invalid archive error=%v, want raw archive conflict", err)
	}
	var eventCount, receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive WHERE id = ?`, atomicEvent.ID).Scan(&eventCount); err != nil {
		t.Fatalf("atomic event count query failed: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_lifecycle_raw_archive_receipt WHERE event_id = ?`, atomicEvent.ID).Scan(&receiptCount); err != nil {
		t.Fatalf("atomic receipt count query failed: %v", err)
	}
	if eventCount != 0 || receiptCount != 0 {
		t.Fatalf("invalid archive left partial rows event=%d receipt=%d", eventCount, receiptCount)
	}
}

func TestArchiveSQLiteStore_RawLifecycleArchiveDoesNotMutateL1BeforeFinalize(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	l1Store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(dir, "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer l1Store.Close()
	archiveStore, err := NewArchiveSQLiteStore(filepath.Join(dir, "archive.db"))
	if err != nil {
		t.Fatalf("NewArchiveSQLiteStore failed: %v", err)
	}
	defer archiveStore.Close()
	l1Store.WithArchiveStore(archiveStore)

	old := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := l1Store.SaveMessage(ctx, archiveTestSessionID("raw-archive-source"), archiveTestThreadID("raw-source"), 1, domconv.ThreadKindUserConversation, "conv:archive-source", domconv.Message{
		Speaker:   domconv.SpeakerUser,
		Msg:       "source remains until L1 finalize",
		Timestamp: old,
		Meta:      map[string]interface{}{"kind": "raw"},
	}, l1sqlite.MemoryStateObserved); err != nil {
		t.Fatalf("SaveMessage failed: %v", err)
	}
	batch, err := l1Store.ClaimProfilePromotionBatch(ctx, 1, 5, time.Minute, old.Add(time.Minute))
	if err != nil || batch == nil {
		t.Fatalf("claim profile promotion batch=%+v err=%v", batch, err)
	}
	if _, err := l1Store.CompleteProfilePromotionBatch(ctx, *batch, nil, "ren", old.Add(2*time.Minute)); err != nil {
		t.Fatalf("complete profile promotion batch failed: %v", err)
	}
	events, err := l1Store.RecentByNamespace(ctx, "conv:archive-source", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("read L1 source before archive events=%d err=%v", len(events), err)
	}
	event := events[0]
	hash, err := l1sqlite.CanonicalL1MemoryEventSHA256(event)
	if err != nil {
		t.Fatalf("hash source event: %v", err)
	}
	receipt := l1sqlite.L1RawLifecycleArchiveReceipt{
		OutboxID:    l1sqlite.L1RawLifecycleOutboxID(event.ID, hash),
		EventID:     event.ID,
		EventSHA256: hash,
		CreatedAt:   time.Now().UTC(),
	}
	if err := archiveStore.ArchiveL1RawLifecycleEvent(ctx, event, receipt); err != nil {
		t.Fatalf("archive source event failed: %v", err)
	}
	if events, err := l1Store.RecentByNamespace(ctx, "conv:archive-source", 10); err != nil || len(events) != 1 {
		t.Fatalf("L1 source changed before finalize events=%d err=%v", len(events), err)
	}
	var archiveCount, receiptCount int
	if err := archiveStore.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive WHERE id = ?`, event.ID).Scan(&archiveCount); err != nil {
		t.Fatalf("archive event query failed: %v", err)
	}
	if err := archiveStore.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_lifecycle_raw_archive_receipt WHERE outbox_id = ?`, receipt.OutboxID).Scan(&receiptCount); err != nil {
		t.Fatalf("archive receipt query failed: %v", err)
	}
	if archiveCount != 1 || receiptCount != 1 {
		t.Fatalf("archive rows event=%d receipt=%d, want 1/1", archiveCount, receiptCount)
	}
	result, err := l1Store.RunMemoryLifecycleMaintenance(ctx, l1sqlite.MemoryLifecycleOptions{
		Now: old.Add(120 * 24 * time.Hour), RawConversationRetention: 30 * 24 * time.Hour,
		RawCompactLimit: 1, VectorCleanupLimit: 1,
	})
	if err != nil {
		t.Fatalf("L1 finalize replay failed: %v", err)
	}
	if result.RawCompacted != 1 {
		t.Fatalf("L1 finalize RawCompacted=%d, want 1", result.RawCompacted)
	}
	if events, err := l1Store.RecentByNamespace(ctx, "conv:archive-source", 10); err != nil || len(events) != 0 {
		t.Fatalf("L1 source events after finalize=%d err=%v, want 0", len(events), err)
	}
}

func rawLifecycleArchiveTestEvent() l1sqlite.L1MemoryEvent {
	created := time.Date(2026, 1, 1, 0, 0, 0, 123456789, time.FixedZone("JST", 9*60*60))
	updated := time.Date(2026, 1, 2, 3, 4, 5, 987654321, time.FixedZone("JST", 9*60*60))
	return l1sqlite.L1MemoryEvent{
		ID:          "raw-archive-event-1",
		Namespace:   "conv:archive-test",
		SessionID:   archiveTestSessionID("archive-session"),
		ThreadID:    archiveTestThreadID("raw-lifecycle"),
		ThreadSeq:   1,
		ThreadKind:  domconv.ThreadKindUserConversation,
		Speaker:     domconv.SpeakerUser,
		Message:     "exact raw event",
		Meta:        map[string]interface{}{"b": "two", "a": float64(1)},
		MemoryState: l1sqlite.MemoryStateObserved,
		Layer:       l1sqlite.MemoryLayerL1,
		Source:      "conversation",
		CreatedAt:   created,
		UpdatedAt:   updated,
	}
}

func assertRawLifecycleArchiveCounts(t *testing.T, ctx context.Context, store *ArchiveSQLiteStore, wantEvents, wantReceipts int) {
	t.Helper()
	var eventCount, receiptCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event_archive WHERE id = ?`, "raw-archive-event-1").Scan(&eventCount); err != nil {
		t.Fatalf("archive event count query failed: %v", err)
	}
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_lifecycle_raw_archive_receipt WHERE event_id = ?`, "raw-archive-event-1").Scan(&receiptCount); err != nil {
		t.Fatalf("archive receipt count query failed: %v", err)
	}
	if eventCount != wantEvents || receiptCount != wantReceipts {
		t.Fatalf("archive row counts event=%d receipt=%d, want %d/%d", eventCount, receiptCount, wantEvents, wantReceipts)
	}
}
