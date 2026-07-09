package superagent

import (
	"context"
	"errors"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
)

func TestRunQueueSchedulerRunOnceClaimsAndCompletesDueItem(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &recordingRunQueueStore{
		items: []domainsuperagent.RunQueueItem{
			{
				QueueID:   "q-low",
				Goal:      "later",
				Action:    "resume",
				Status:    "queued",
				Priority:  1,
				CreatedAt: now.Add(-2 * time.Minute),
			},
			{
				QueueID:   "q-high",
				Goal:      "run this",
				Action:    "resume",
				Status:    "queued",
				Priority:  10,
				CreatedAt: now.Add(-time.Minute),
			},
			{
				QueueID:   "q-future",
				Goal:      "not yet",
				Action:    "resume",
				Status:    "queued",
				Priority:  100,
				NotBefore: now.Add(time.Hour),
				CreatedAt: now.Add(-time.Minute),
			},
		},
	}
	var processed domainsuperagent.RunQueueItem
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem) (string, error) {
		processed = item
		return "ok", nil
	}), RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() error = %v", err)
	}
	if count != 1 {
		t.Fatalf("RunOnce() count = %d, want 1", count)
	}
	if processed.QueueID != "q-high" {
		t.Fatalf("processed queue = %q, want q-high", processed.QueueID)
	}
	item := store.item("q-high")
	if item.Status != "completed" || item.Reason != "ok" || item.ClaimedAt.IsZero() || item.CompletedAt.IsZero() {
		t.Fatalf("completed item = %#v", item)
	}
	if len(store.traces) != 2 || store.traces[0].EventType != "run_queue_claimed" || store.traces[1].EventType != "run_queue_completed" {
		t.Fatalf("unexpected traces = %#v", store.traces)
	}
}

func TestRunQueueSchedulerRunOnceMarksFailure(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &recordingRunQueueStore{
		items: []domainsuperagent.RunQueueItem{{
			QueueID:   "q1",
			Goal:      "run",
			Action:    "resume",
			Status:    "queued",
			CreatedAt: now,
		}},
	}
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, _ domainsuperagent.RunQueueItem) (string, error) {
		return "", errors.New("worker failed")
	}), RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1})

	count, err := scheduler.RunOnce(context.Background())
	if err == nil {
		t.Fatal("RunOnce() error = nil, want error")
	}
	if count != 0 {
		t.Fatalf("RunOnce() count = %d, want 0", count)
	}
	item := store.item("q1")
	if item.Status != "failed" || item.Reason != "worker failed" || item.CompletedAt.IsZero() {
		t.Fatalf("failed item = %#v", item)
	}
	if len(store.traces) != 2 || store.traces[1].EventType != "run_queue_failed" {
		t.Fatalf("unexpected traces = %#v", store.traces)
	}
}

type recordingRunQueueStore struct {
	items  []domainsuperagent.RunQueueItem
	traces []domainsuperagent.TraceEvent
}

func (s *recordingRunQueueStore) ListRunQueueItems(context.Context, int) ([]domainsuperagent.RunQueueItem, error) {
	return append([]domainsuperagent.RunQueueItem{}, s.items...), nil
}

func (s *recordingRunQueueStore) SaveRunQueueItem(_ context.Context, item domainsuperagent.RunQueueItem) error {
	for idx := range s.items {
		if s.items[idx].QueueID == item.QueueID {
			s.items[idx] = item
			return nil
		}
	}
	s.items = append(s.items, item)
	return nil
}

func (s *recordingRunQueueStore) SaveTraceEvent(_ context.Context, item domainsuperagent.TraceEvent) error {
	s.traces = append(s.traces, item)
	return nil
}

func (s *recordingRunQueueStore) item(queueID string) domainsuperagent.RunQueueItem {
	for _, item := range s.items {
		if item.QueueID == queueID {
			return item
		}
	}
	return domainsuperagent.RunQueueItem{}
}
