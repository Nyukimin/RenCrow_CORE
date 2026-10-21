package worldprojection

import (
	"context"
	"errors"
	"net/http"
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

type fakeHeadReader struct {
	head  ProjectionHead
	err   error
	calls int
}

func (f *fakeHeadReader) ReadHead(context.Context, domainprojection.Target, string) (ProjectionHead, error) {
	f.calls++
	return f.head, f.err
}

func TestWorkerCompletesEpochUnchangedOnlyAfterHeadMatches(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	record := OutboxRecord{
		ID: "unchanged", Envelope: []byte("same"), Reconcile: true,
		Target: domainprojection.Target{
			WorldID: "world", TimelineID: "timeline", TimelineEpoch: "timeline-epoch",
			ActorID: "mio", ActorInstanceID: "instance-mio", ActorTransportID: "mio",
		},
		Kind: domainprojection.KindWorkSet, SourceEpoch: "core-epoch", SourceRevision: 7, Digest: "digest-7",
	}
	head := &fakeHeadReader{head: ProjectionHead{
		ActorID: "mio", ActorInstanceID: "instance-mio", Kind: domainprojection.KindWorkSet,
		SourceEpoch: "core-epoch", SourceRevision: 7, Digest: "digest-7",
	}}
	outbox := &fakeOutbox{claimed: []OutboxRecord{record}}
	publisher := &fakePublisher{results: map[string]PublishResult{
		"same": {Status: "epoch_unchanged", HTTPStatus: http.StatusConflict},
	}}
	worker := &Worker{Outbox: outbox, Publisher: publisher, HeadReader: head, Now: func() time.Time { return now }}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Sent != 1 || result.Failed != 0 || head.calls != 1 {
		t.Fatalf("result = %+v, head calls = %d", result, head.calls)
	}
	if len(outbox.completed) != 1 || outbox.completed[0] != "unchanged" {
		t.Fatalf("completed = %+v", outbox.completed)
	}
}

func TestWorkerRejectsEpochUnchangedWithMismatchedHead(t *testing.T) {
	record := OutboxRecord{
		ID: "unchanged", Envelope: []byte("same"), Reconcile: true,
		Target: domainprojection.Target{
			WorldID: "world", TimelineID: "timeline", TimelineEpoch: "timeline-epoch",
			ActorID: "mio", ActorInstanceID: "instance-mio", ActorTransportID: "mio",
		},
		Kind: domainprojection.KindWorkSet, SourceEpoch: "core-epoch", SourceRevision: 7, Digest: "digest-7",
	}
	for name, head := range map[string]ProjectionHead{
		"revision": {ActorID: "mio", ActorInstanceID: "instance-mio", Kind: domainprojection.KindWorkSet,
			SourceEpoch: "core-epoch", SourceRevision: 8, Digest: "digest-7"},
		"digest": {ActorID: "mio", ActorInstanceID: "instance-mio", Kind: domainprojection.KindWorkSet,
			SourceEpoch: "core-epoch", SourceRevision: 7, Digest: "other"},
		"instance": {ActorID: "mio", ActorInstanceID: "other-instance", Kind: domainprojection.KindWorkSet,
			SourceEpoch: "core-epoch", SourceRevision: 7, Digest: "digest-7"},
	} {
		t.Run(name, func(t *testing.T) {
			outbox := &fakeOutbox{claimed: []OutboxRecord{record}}
			publisher := &fakePublisher{results: map[string]PublishResult{
				"same": {Status: "epoch_unchanged", HTTPStatus: http.StatusConflict},
			}}
			worker := &Worker{Outbox: outbox, Publisher: publisher, HeadReader: &fakeHeadReader{head: head}}
			result, err := worker.Drain(context.Background())
			if err != nil {
				t.Fatalf("Drain() error = %v", err)
			}
			if result.Sent != 0 || result.Failed != 1 || len(outbox.completed) != 0 {
				t.Fatalf("result = %+v, completed = %+v", result, outbox.completed)
			}
			if len(outbox.failed) != 1 || !outbox.failed[0].terminal {
				t.Fatalf("failed = %+v", outbox.failed)
			}
		})
	}
}

func TestWorkerRetriesEpochUnchangedWhenHeadReadIsTransient(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	record := OutboxRecord{ID: "unchanged", Envelope: []byte("same"), Reconcile: true}
	outbox := &fakeOutbox{claimed: []OutboxRecord{record}}
	publisher := &fakePublisher{results: map[string]PublishResult{
		"same": {Status: "epoch_unchanged"},
	}}
	worker := &Worker{
		Outbox: outbox, Publisher: publisher,
		HeadReader: &fakeHeadReader{err: &HeadReadError{HTTPStatus: http.StatusServiceUnavailable, Retryable: true}},
		Now:        func() time.Time { return now },
	}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Retried != 1 || result.Sent != 0 || len(outbox.failed) != 1 || outbox.failed[0].terminal {
		t.Fatalf("result = %+v, failed = %+v", result, outbox.failed)
	}
}

func TestWorkerFailsEpochUnchangedWithoutHeadReader(t *testing.T) {
	record := OutboxRecord{ID: "unchanged", Envelope: []byte("same"), Reconcile: true}
	outbox := &fakeOutbox{claimed: []OutboxRecord{record}}
	publisher := &fakePublisher{results: map[string]PublishResult{
		"same": {Status: "epoch_unchanged"},
	}}
	worker := &Worker{Outbox: outbox, Publisher: publisher}
	result, err := worker.Drain(context.Background())
	if err != nil {
		t.Fatalf("Drain() error = %v", err)
	}
	if result.Failed != 1 || result.Sent != 0 || len(outbox.failed) != 1 || !outbox.failed[0].terminal {
		t.Fatalf("result = %+v, failed = %+v", result, outbox.failed)
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
