package superagent

import (
	"context"
	"errors"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRunQueueSchedulerRunOnceClaimsAndCompletesDueItem(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &recordingRunQueueStore{
		runs: []domainsuperagent.AgentRun{{
			RunID: "run-high", AgentType: "LeadAgent", Goal: "run this", Status: "queued", StartedAt: now.Add(-time.Minute),
			ResumePolicy: "checkpoint", CheckpointRevision: 1, CheckpointSummary: "request committed", NextAction: "run this", LastCheckpointAt: now.Add(-time.Minute),
		}},
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
				RunID:     "run-high",
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
	var processedTrace modulecore.TraceID
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		processed = item
		processedTrace = traceID
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
	if run := store.runs[0]; run.Status != "completed" || run.Summary != "ok" || run.CompletedAt.IsZero() {
		t.Fatalf("completed run = %#v", run)
	}
	if len(store.traces) != 2 || store.traces[0].EventType != "run_queue.claimed" || store.traces[1].EventType != "run_queue.completed" || store.traces[1].CausationEventID != store.traces[0].EventID {
		t.Fatalf("unexpected traces = %#v", store.traces)
	}
	if processedTrace.Validate() != nil || processedTrace != store.traces[0].TraceID || processedTrace != store.traces[1].TraceID {
		t.Fatalf("processor trace=%q events=%q/%q", processedTrace, store.traces[0].TraceID, store.traces[1].TraceID)
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
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, _ domainsuperagent.RunQueueItem, _ modulecore.TraceID) (string, error) {
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
	if len(store.traces) != 2 || store.traces[1].EventType != "run_queue.failed" || store.traces[1].CausationEventID != store.traces[0].EventID {
		t.Fatalf("unexpected traces = %#v", store.traces)
	}
}

func TestRunQueueSchedulerRecoversOnlyExpiredClaimWithSameCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 23, 15, 0, 0, 0, time.UTC)
	store := &recordingRunQueueStore{items: []domainsuperagent.RunQueueItem{
		{QueueID: "expired", RunID: "run-1", Goal: "continue", Action: "resume", Status: "claimed", LeaseToken: "dead-owner", LeaseUntil: now.Add(-time.Second), CheckpointRevision: 4, AttemptCount: 1, CreatedAt: now.Add(-time.Hour)},
		{QueueID: "active", RunID: "run-2", Goal: "do not duplicate", Action: "resume", Status: "claimed", LeaseToken: "live-owner", LeaseUntil: now.Add(time.Minute), CheckpointRevision: 2, AttemptCount: 1, CreatedAt: now.Add(-time.Hour)},
	}}
	var processed domainsuperagent.RunQueueItem
	var recoveredTrace modulecore.TraceID
	scheduler := NewRunQueueScheduler(store, RunQueueProcessorFunc(func(_ context.Context, item domainsuperagent.RunQueueItem, traceID modulecore.TraceID) (string, error) {
		processed = item
		recoveredTrace = traceID
		return "resumed", nil
	}), RunQueueSchedulerOptions{Now: func() time.Time { return now }, ClaimLimit: 1, LeaseDuration: 3 * time.Minute})

	count, err := scheduler.RunOnce(context.Background())
	if err != nil || count != 1 {
		t.Fatalf("RunOnce() count=%d err=%v", count, err)
	}
	if processed.QueueID != "expired" || processed.CheckpointRevision != 4 || processed.AttemptCount != 2 {
		t.Fatalf("recovered item=%#v", processed)
	}
	if recoveredTrace.Validate() != nil || len(store.traces) == 0 || recoveredTrace != store.traces[0].TraceID {
		t.Fatalf("recovered processor trace=%q events=%#v", recoveredTrace, store.traces)
	}
	if got := store.item("active"); got.LeaseToken != "live-owner" || got.Status != "claimed" {
		t.Fatalf("unexpired claim changed: %#v", got)
	}
}

type recordingRunQueueStore struct {
	runs   []domainsuperagent.AgentRun
	items  []domainsuperagent.RunQueueItem
	traces []modulecore.EventEnvelope
}

func (s *recordingRunQueueStore) ListAgentRuns(context.Context, int) ([]domainsuperagent.AgentRun, error) {
	return append([]domainsuperagent.AgentRun{}, s.runs...), nil
}

func (s *recordingRunQueueStore) SaveAgentRun(_ context.Context, item domainsuperagent.AgentRun) error {
	for index := range s.runs {
		if s.runs[index].RunID == item.RunID {
			s.runs[index] = item
			return nil
		}
	}
	s.runs = append(s.runs, item)
	return nil
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

func (s *recordingRunQueueStore) Append(_ context.Context, item modulecore.EventEnvelope) error {
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

func TestRecoverInterruptedAgentRunsQueuesOnlyDurableCheckpoint(t *testing.T) {
	now := time.Date(2026, 8, 23, 16, 0, 0, 0, time.UTC)
	store := &recordingRunQueueStore{runs: []domainsuperagent.AgentRun{
		{RunID: "run-resumable", WorkstreamID: "thread-1", AgentType: "LeadAgent", Goal: "continue", Status: "running", StartedAt: now.Add(-time.Hour), ResumePolicy: "checkpoint", CheckpointRevision: 5, CheckpointSummary: "step four committed", NextAction: "step five", LastCheckpointAt: now.Add(-time.Minute)},
		{RunID: "run-legacy", AgentType: "LeadAgent", Goal: "unknown position", Status: "running", StartedAt: now.Add(-time.Hour)},
		{RunID: "run-finished", AgentType: "LeadAgent", Goal: "done", Status: "completed", StartedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute), Summary: "receipt committed", ResumePolicy: "checkpoint", CheckpointRevision: 1, CheckpointSummary: "dispatch", NextAction: "execute", LastCheckpointAt: now.Add(-time.Hour)},
		{RunID: "run-queue-finished", AgentType: "LeadAgent", Goal: "done by queue", Status: "queued", StartedAt: now.Add(-time.Hour), ResumePolicy: "checkpoint", CheckpointRevision: 2, CheckpointSummary: "dispatch", NextAction: "execute", LastCheckpointAt: now.Add(-time.Hour)},
	}, items: []domainsuperagent.RunQueueItem{
		{QueueID: "resume:run-finished:1", RunID: "run-finished", Goal: "done", Action: "resume", Status: "claimed", ClaimedAt: now.Add(-2 * time.Minute), LeaseToken: "dead", LeaseUntil: now.Add(time.Minute), CheckpointRevision: 1, CreatedAt: now.Add(-2 * time.Minute)},
		{QueueID: "resume:run-queue-finished:2", RunID: "run-queue-finished", Goal: "done by queue", Action: "resume", Status: "completed", Reason: "queue receipt", CompletedAt: now.Add(-time.Minute), CheckpointRevision: 2, CreatedAt: now.Add(-2 * time.Minute)},
	}}
	queued, blocked, err := RecoverInterruptedAgentRuns(context.Background(), store, now)
	if err != nil || queued != 1 || blocked != 1 {
		t.Fatalf("RecoverInterruptedAgentRuns queued=%d blocked=%d err=%v", queued, blocked, err)
	}
	if len(store.items) != 3 || store.item("resume:run-resumable:5").CheckpointSummary != "step four committed" || store.item("resume:run-resumable:5").NextAction != "step five" || store.item("resume:run-finished:1").Status != "completed" {
		t.Fatalf("recovery queue=%#v", store.items)
	}
	if run := store.runs[3]; run.Status != "completed" || run.Summary != "queue receipt" {
		t.Fatalf("queue terminal did not reconcile run: %#v", run)
	}
	queued, blocked, err = RecoverInterruptedAgentRuns(context.Background(), store, now.Add(time.Second))
	if err != nil || queued != 0 || blocked != 0 || len(store.items) != 3 {
		t.Fatalf("idempotent recovery queued=%d blocked=%d items=%#v err=%v", queued, blocked, store.items, err)
	}
}
