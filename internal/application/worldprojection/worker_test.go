package worldprojection

import (
	"context"
	"errors"
	"testing"
	"time"

	domainprojection "github.com/Nyukimin/RenCrow_CORE/internal/domain/worldprojection"
)

type fakeOutbox struct {
	claimed   []OutboxRecord
	completed []string
	failed    []fakeFailure
}

type fakeFailure struct {
	id       string
	retryAt  time.Time
	terminal bool
	message  string
}

func (f *fakeOutbox) Enqueue(context.Context, OutboxRecord, time.Time) error { return nil }
func (f *fakeOutbox) Claim(context.Context, time.Time, int) ([]OutboxRecord, error) {
	return append([]OutboxRecord(nil), f.claimed...), nil
}
func (f *fakeOutbox) Complete(_ context.Context, id string, _ PublishResult, _ time.Time) error {
	f.completed = append(f.completed, id)
	return nil
}
func (f *fakeOutbox) Fail(_ context.Context, id string, retryAt time.Time, terminal bool, message string, _ time.Time) error {
	f.failed = append(f.failed, fakeFailure{id: id, retryAt: retryAt, terminal: terminal, message: message})
	return nil
}

type fakePublisher struct {
	results map[string]PublishResult
	errs    map[string]error
	calls   int
}

func (f *fakePublisher) Publish(_ context.Context, _ domainprojection.Target, _ string, envelope []byte, _ bool) (PublishResult, error) {
	f.calls++
	id := string(envelope)
	return f.results[id], f.errs[id]
}

func TestWorkerCompletesSuccessfulAndSchedulesRetryableFailures(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	outbox := &fakeOutbox{claimed: []OutboxRecord{
		{ID: "accepted", Envelope: []byte("accepted"), Attempts: 0},
		{ID: "retry", Envelope: []byte("retry"), Attempts: 2},
		{ID: "terminal", Envelope: []byte("terminal"), Attempts: 0},
	}}
	publisher := &fakePublisher{
		results: map[string]PublishResult{
			"accepted": {Status: "accepted"},
			"retry":    {Status: "", Retryable: true},
			"terminal": {Status: "revision_conflict"},
		},
		errs: map[string]error{
			"retry": errors.New("network unavailable"),
		},
	}
	worker := &Worker{Outbox: outbox, Publisher: publisher, Now: func() time.Time { return now }, Limit: 10}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Claimed != 3 || result.Sent != 1 || result.Retried != 1 || result.Failed != 1 {
		t.Fatalf("Drain() result = %+v", result)
	}
	if len(outbox.completed) != 1 || outbox.completed[0] != "accepted" {
		t.Fatalf("completed = %+v", outbox.completed)
	}
	if len(outbox.failed) != 2 {
		t.Fatalf("failed = %+v", outbox.failed)
	}
	if outbox.failed[0].id != "retry" || outbox.failed[0].terminal || !outbox.failed[0].retryAt.After(now) {
		t.Fatalf("retry failure = %+v", outbox.failed[0])
	}
	if outbox.failed[1].id != "terminal" || !outbox.failed[1].terminal {
		t.Fatalf("terminal failure = %+v", outbox.failed[1])
	}
	if publisher.calls != 3 {
		t.Fatalf("publisher calls = %d", publisher.calls)
	}
}

func TestWorkerTreatsRequiresReconcileAsTerminal(t *testing.T) {
	outbox := &fakeOutbox{claimed: []OutboxRecord{{ID: "reconcile", Envelope: []byte("reconcile")}}}
	publisher := &fakePublisher{results: map[string]PublishResult{
		"reconcile": {Status: "epoch_requires_reconcile", RequiresReconcile: true},
	}}
	worker := &Worker{Outbox: outbox, Publisher: publisher}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 || len(outbox.failed) != 1 || !outbox.failed[0].terminal {
		t.Fatalf("result = %+v, failures = %+v", result, outbox.failed)
	}
}

func TestWorkerRetryBackoffIsBounded(t *testing.T) {
	worker := &Worker{RetryBase: time.Second, RetryMax: 8 * time.Second}
	for attempts, want := range map[int]time.Duration{0: time.Second, 1: 2 * time.Second, 2: 4 * time.Second, 4: 8 * time.Second, 20: 8 * time.Second} {
		if got := worker.retryDelay(attempts); got != want {
			t.Fatalf("retryDelay(%d) = %s, want %s", attempts, got, want)
		}
	}
}
