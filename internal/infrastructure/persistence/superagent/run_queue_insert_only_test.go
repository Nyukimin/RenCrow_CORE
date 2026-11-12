package superagent

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runQueueIntentStore interface {
	FindRunQueueItemByTaskIntent(context.Context, modulecore.TaskID, string) (domainsuperagent.RunQueueItem, bool, error)
	InsertRunQueueItemIfAbsent(context.Context, domainsuperagent.RunQueueItem) (domainsuperagent.RunQueueItem, bool, error)
	SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
	ListRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error)
}

type runQueueClaimStore interface {
	SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
	ClaimNextRunQueueItem(context.Context, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	ClaimRunQueueItem(context.Context, modulecore.QueueItemID, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
}

func TestSQLiteRunQueueInsertIsAtomicAndKeepsExistingReceipt(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertRunQueueInsertIsAtomicAndKeepsExistingReceipt(t, store)
}

func TestJSONLRunQueueInsertIsAtomicAndKeepsExistingReceipt(t *testing.T) {
	assertRunQueueInsertIsAtomicAndKeepsExistingReceipt(t, NewJSONLStore(t.TempDir(), 3000))
}

func TestSQLiteLegacyClaimSkipsPrivateFirstChat(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	assertLegacyClaimSkipsPrivateFirstChat(t, store)
}

func TestJSONLLegacyClaimSkipsPrivateFirstChat(t *testing.T) {
	assertLegacyClaimSkipsPrivateFirstChat(t, NewJSONLStore(t.TempDir(), 3000))
}

func assertLegacyClaimSkipsPrivateFirstChat(t *testing.T, raw any) {
	t.Helper()
	store, ok := raw.(runQueueClaimStore)
	if !ok {
		t.Fatalf("%T does not support run queue claims", raw)
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	item := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: modulecore.NewTaskID(),
		RunStartReason: domaintask.RunStartReasonFirst, Goal: domainsuperagent.RunQueueGoalPrivateFirstChat,
		Action: "chat", Status: "queued", Priority: 4,
		IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat, CreatedAt: now,
	}
	if err := store.SaveRunQueueItem(ctx, item); err != nil {
		t.Fatalf("save private queue item: %v", err)
	}
	if claimed, err := store.ClaimNextRunQueueItem(ctx, now, now.Add(time.Minute), "legacy-token"); err != nil || claimed != nil {
		t.Fatalf("legacy claim=(%+v, %v), want no private queue claim", claimed, err)
	}
	claimed, err := store.ClaimRunQueueItem(ctx, item.QueueItemID, now, now.Add(time.Minute), "actor-token")
	if err != nil || claimed == nil || claimed.QueueItemID != item.QueueItemID || claimed.Status != "reserved" {
		t.Fatalf("exact actor claim=(%+v, %v), want private item reserved", claimed, err)
	}
}

func assertRunQueueInsertIsAtomicAndKeepsExistingReceipt(t *testing.T, raw any) {
	t.Helper()
	store, ok := raw.(runQueueIntentStore)
	if !ok {
		t.Fatalf("%T does not support Task+intent lookup and insert-only queue admission", raw)
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 13, 3, 0, 0, 0, time.UTC)
	taskID := modulecore.NewTaskID()
	const intent = domainsuperagent.RunQueueIntentPrivateFirstChat
	initial := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: taskID,
		RunStartReason: domaintask.RunStartReasonFirst, Goal: "authored task intent",
		Action: "chat", Status: "queued", Priority: 4, IdempotencyKey: intent, CreatedAt: now,
	}
	got, inserted, err := store.InsertRunQueueItemIfAbsent(ctx, initial)
	if err != nil || !inserted || got.QueueItemID != initial.QueueItemID {
		t.Fatalf("first insert = (%+v, %v, %v), want original receipt and inserted=true", got, inserted, err)
	}

	changedRequest := initial
	changedRequest.QueueItemID = modulecore.NewQueueItemID()
	changedRequest.Goal = "replacement intent must be ignored"
	changedRequest.Priority = 1
	changedRequest.CreatedAt = now.Add(time.Hour)
	got, inserted, err = store.InsertRunQueueItemIfAbsent(ctx, changedRequest)
	if err != nil || inserted || got.QueueItemID != initial.QueueItemID || got.Goal != initial.Goal || got.Priority != initial.Priority || !got.CreatedAt.Equal(initial.CreatedAt) {
		t.Fatalf("duplicate insert = (%+v, %v, %v), want the unchanged original receipt", got, inserted, err)
	}

	// The same receipt must survive later claim/completion saves and remain the
	// result of a duplicate request after its lifecycle has advanced.
	completed := got
	completed.Status = "completed"
	completed.RunID = modulecore.NewRunID()
	completed.CompletedAt = now.Add(2 * time.Minute)
	if err := store.SaveRunQueueItem(ctx, completed); err != nil {
		t.Fatalf("save completed receipt: %v", err)
	}
	found, ok, err := store.FindRunQueueItemByTaskIntent(ctx, taskID, intent)
	if err != nil || !ok || found.QueueItemID != initial.QueueItemID || found.Status != "completed" {
		t.Fatalf("find completed receipt = (%+v, %v, %v), want the completed original", found, ok, err)
	}
	got, inserted, err = store.InsertRunQueueItemIfAbsent(ctx, changedRequest)
	if err != nil || inserted || got.QueueItemID != initial.QueueItemID || got.Status != "completed" || got.RunID != completed.RunID {
		t.Fatalf("post-completion duplicate = (%+v, %v, %v), want the completed original receipt", got, inserted, err)
	}

	// The uniqueness boundary is the canonical Task ID together with intent.
	otherTask := initial
	otherTask.TaskID = modulecore.NewTaskID()
	otherTask.QueueItemID = modulecore.NewQueueItemID()
	got, inserted, err = store.InsertRunQueueItemIfAbsent(ctx, otherTask)
	if err != nil || !inserted || got.QueueItemID != otherTask.QueueItemID {
		t.Fatalf("same intent on another Task = (%+v, %v, %v), want a separate insert", got, inserted, err)
	}

	// Concurrent retries for the same canonical Task+intent all return one
	// durable receipt, even when each caller proposes a fresh queue ID.
	concurrentTask := modulecore.NewTaskID()
	const callers = 24
	type result struct {
		item     domainsuperagent.RunQueueItem
		inserted bool
		err      error
	}
	results := make(chan result, callers)
	var wg sync.WaitGroup
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			candidate := initial
			candidate.TaskID = concurrentTask
			candidate.QueueItemID = modulecore.NewQueueItemID()
			candidate.Goal = "retry"
			item, wasInserted, insertErr := store.InsertRunQueueItemIfAbsent(ctx, candidate)
			results <- result{item: item, inserted: wasInserted, err: insertErr}
		}(i)
	}
	wg.Wait()
	close(results)
	var insertedCount int
	var receipt modulecore.QueueItemID
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent insert: %v", result.err)
		}
		if result.inserted {
			insertedCount++
		}
		if receipt == "" {
			receipt = result.item.QueueItemID
		} else if result.item.QueueItemID != receipt {
			t.Fatalf("concurrent receipts differ: got %s and %s", receipt, result.item.QueueItemID)
		}
	}
	if insertedCount != 1 || receipt.Validate() != nil {
		t.Fatalf("concurrent insert count=%d receipt=%q, want one canonical receipt", insertedCount, receipt)
	}
}
