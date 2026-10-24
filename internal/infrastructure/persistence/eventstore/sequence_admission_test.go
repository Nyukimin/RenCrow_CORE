package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	"modernc.org/sqlite"
)

func TestQueuedAppendAndCommitTurnBundleKeepSequenceAndReplayOrder(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "queued ordering")
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "queue-order-accept", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "queue-order-submission", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{
			MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn before queue attachment: %v", err)
	}

	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	blockStarted := make(chan struct{})
	blockRelease := make(chan struct{})
	var blockOnce sync.Once
	releaseQueueBlock := func() { blockOnce.Do(func() { close(blockRelease) }) }
	t.Cleanup(func() {
		releaseQueueBlock()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})

	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.test-block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(blockStarted)
			<-blockRelease
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue blocking head: %v", err)
	}
	select {
	case <-blockStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking queue head did not start")
	}

	adapter := &eventStoreTestWriteQueue{queue: queue, tickets: make(chan uint64, 4)}
	store.writeQueue = adapter
	queuedAppend := eventFixture(inputEvent.TraceID, "queue.order.append", "dci")
	appendResult := make(chan struct {
		event modulecore.EventEnvelope
		err   error
	}, 1)
	go func() {
		event, err := store.AppendSequenced(ctx, queuedAppend)
		appendResult <- struct {
			event modulecore.EventEnvelope
			err   error
		}{event: event, err: err}
	}()
	if ticket := waitQueueTestTicket(t, adapter.tickets); ticket != 2 {
		t.Fatalf("AppendSequenced ticket = %d, want 2 after blocker", ticket)
	}

	firstReply := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	firstReply.SessionID, firstReply.TurnID = sessionID, turnID
	firstReply.MessageID = modulecore.NewMessageID()
	firstReply.CausationEventID = accepted.InputEvent.EventID
	firstReply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "first"}
	secondReply := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	secondReply.SessionID, secondReply.TurnID = sessionID, turnID
	secondReply.MessageID = modulecore.NewMessageID()
	secondReply.CausationEventID = firstReply.EventID
	secondReply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "second"}
	commitResult := make(chan struct {
		batch CommittedBatch
		err   error
	}, 1)
	go func() {
		batch, err := store.CommitTurnBundle(ctx, CommitInput{
			OperationID: "queue-order-commit", SessionID: sessionID, TurnID: turnID,
			GenerationEpoch: 1, ResultState: "completed",
			Events: []modulecore.EventEnvelope{firstReply, secondReply},
			MessageIndexes: []MessageIndex{
				{MessageID: firstReply.MessageID, EventID: firstReply.EventID, SessionID: sessionID, TurnID: turnID,
					FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public"},
				{MessageID: secondReply.MessageID, EventID: secondReply.EventID, SessionID: sessionID, TurnID: turnID,
					FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public"},
			},
		})
		commitResult <- struct {
			batch CommittedBatch
			err   error
		}{batch: batch, err: err}
	}()
	if ticket := waitQueueTestTicket(t, adapter.tickets); ticket != 3 {
		t.Fatalf("CommitTurnBundle ticket = %d, want 3 after queued append", ticket)
	}

	select {
	case result := <-appendResult:
		t.Fatalf("AppendSequenced returned before prior queue head released: seq=%d err=%v", result.event.EventSeq, result.err)
	default:
	}
	select {
	case result := <-commitResult:
		t.Fatalf("CommitTurnBundle returned before prior queue head released: events=%d err=%v", len(result.batch.Events), result.err)
	default:
	}
	releaseQueueBlock()

	appendOutcome := waitQueuedAppendResult(t, appendResult)
	if appendOutcome.err != nil || appendOutcome.event.EventSeq != 2 {
		t.Fatalf("queued AppendSequenced = (seq %d, %v), want seq 2 after durable barrier", appendOutcome.event.EventSeq, appendOutcome.err)
	}
	commitOutcome := waitQueuedCommitResult(t, commitResult)
	if commitOutcome.err != nil {
		t.Fatalf("queued CommitTurnBundle: %v", commitOutcome.err)
	}
	if len(commitOutcome.batch.Events) != 2 || commitOutcome.batch.Events[0].EventSeq != 3 || commitOutcome.batch.Events[1].EventSeq != 4 {
		t.Fatalf("queued CommitTurnBundle events = %#v, want sequences 3 and 4", commitOutcome.batch.Events)
	}

	replay, err := store.ListByTraceID(ctx, inputEvent.TraceID, 10)
	if err != nil {
		t.Fatalf("ListByTraceID after ordered writes: %v", err)
	}
	if len(replay) != 4 {
		t.Fatalf("replay count = %d, want 4 durable events", len(replay))
	}
	for index, event := range replay {
		if want := modulecore.EventSeq(index + 1); event.EventSeq != want {
			t.Fatalf("replay[%d] EventSeq = %d, want %d", index, event.EventSeq, want)
		}
	}
}

func TestQueuedAppendBatchAndTurnBundleShareFIFOSequence(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	inputEvent := liveInputEvent(sessionID, turnID, "mixed queued writers")
	accepted, err := store.AcceptTurn(ctx, AcceptInput{
		OperationID: "queue-mixed-accept", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "queue-mixed-submission", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: inputEvent,
		InputMessage: MessageIndex{
			MessageID: inputEvent.MessageID, EventID: inputEvent.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn before queue attachment: %v", err)
	}

	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	blockStarted := make(chan struct{})
	blockRelease := make(chan struct{})
	var blockOnce sync.Once
	releaseQueueBlock := func() { blockOnce.Do(func() { close(blockRelease) }) }
	adapter := &eventStoreTestWriteQueue{queue: queue, tickets: make(chan uint64, 4)}
	store.writeQueue = adapter
	t.Cleanup(func() {
		releaseQueueBlock()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("queue shutdown: %v", err)
		}
	})
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.test-block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(blockStarted)
			<-blockRelease
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue blocking head: %v", err)
	}
	select {
	case <-blockStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking queue head did not start")
	}

	legacyEvents := []modulecore.EventEnvelope{
		eventFixture(modulecore.NewTraceID(), "queue.mixed.legacy.1", "dci"),
		eventFixture(modulecore.NewTraceID(), "queue.mixed.legacy.2", "dci"),
	}
	legacyResult := make(chan error, 1)
	go func() { legacyResult <- store.AppendBatch(ctx, legacyEvents) }()
	if ticket := waitQueueTestTicket(t, adapter.tickets); ticket != 2 {
		t.Fatalf("AppendBatch ticket = %d, want 2 after blocker", ticket)
	}

	firstReply := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	firstReply.SessionID, firstReply.TurnID = sessionID, turnID
	firstReply.MessageID = modulecore.NewMessageID()
	firstReply.CausationEventID = accepted.InputEvent.EventID
	firstReply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "first"}
	secondReply := eventFixture(inputEvent.TraceID, "agent.response", "orchestrator")
	secondReply.SessionID, secondReply.TurnID = sessionID, turnID
	secondReply.MessageID = modulecore.NewMessageID()
	secondReply.CausationEventID = firstReply.EventID
	secondReply.Payload = map[string]any{"from": "mio", "source_actor_id": "mio", "to": "user", "content": "second"}
	commitResult := make(chan error, 1)
	go func() {
		_, commitErr := store.CommitTurnBundle(ctx, CommitInput{
			OperationID: "queue-mixed-commit", SessionID: sessionID, TurnID: turnID,
			GenerationEpoch: 1, ResultState: "completed",
			Events: []modulecore.EventEnvelope{firstReply, secondReply},
			MessageIndexes: []MessageIndex{
				{MessageID: firstReply.MessageID, EventID: firstReply.EventID, SessionID: sessionID, TurnID: turnID,
					FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public"},
				{MessageID: secondReply.MessageID, EventID: secondReply.EventID, SessionID: sessionID, TurnID: turnID,
					FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public"},
			},
		})
		commitResult <- commitErr
	}()
	if ticket := waitQueueTestTicket(t, adapter.tickets); ticket != 3 {
		t.Fatalf("CommitTurnBundle ticket = %d, want 3 after queued AppendBatch", ticket)
	}
	select {
	case err := <-legacyResult:
		t.Fatalf("AppendBatch returned before queue head was released: %v", err)
	default:
	}
	select {
	case err := <-commitResult:
		t.Fatalf("CommitTurnBundle returned before queue head was released: %v", err)
	default:
	}
	releaseQueueBlock()

	select {
	case err := <-legacyResult:
		if err != nil {
			t.Fatalf("queued AppendBatch: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued AppendBatch did not finish")
	}
	select {
	case err := <-commitResult:
		if err != nil {
			t.Fatalf("queued CommitTurnBundle: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued CommitTurnBundle did not finish")
	}

	for index, event := range legacyEvents {
		stored, found, err := store.GetByID(ctx, event.EventID)
		if err != nil || !found {
			t.Fatalf("GetByID(legacy %d): found=%v err=%v", index, found, err)
		}
		if want := modulecore.EventSeq(index + 2); stored.EventSeq != want {
			t.Fatalf("legacy event %d EventSeq = %d, want %d", index, stored.EventSeq, want)
		}
	}
	committed, found, err := store.LoadCommittedOutcome(ctx, turnID)
	if err != nil || !found {
		t.Fatalf("LoadCommittedOutcome: found=%v err=%v", found, err)
	}
	if len(committed.Events) != 2 || committed.Events[0].EventSeq != 4 || committed.Events[1].EventSeq != 5 {
		t.Fatalf("committed v2 events = %#v, want EventSeq 4 and 5 after v1 batch", committed.Events)
	}
}

func TestQueuedExpectedConflictDoesNotPoisonQueue(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()
	original := eventFixture(modulecore.NewTraceID(), "queue.conflict.original", "dci")
	if _, err := store.AppendSequenced(ctx, original); err != nil {
		t.Fatalf("seed original event: %v", err)
	}
	queue, adapter := attachEventStoreTestQueue(t, store, false)
	defer shutdownEventStoreTestQueue(t, queue, false)

	duplicate := cloneEventEnvelope(original)
	duplicate.EventSeq = 0
	if _, err := store.AppendSequenced(ctx, duplicate); err == nil || !strings.Contains(err.Error(), "duplicate event_id") {
		t.Fatalf("AppendSequenced(duplicate) error = %v, want duplicate event ID", err)
	}
	if len(adapter.tickets) != 1 {
		t.Fatalf("duplicate command tickets = %d, want one accepted queue command", len(adapter.tickets))
	}
	next := eventFixture(original.TraceID, "queue.conflict.next", "dci")
	stored, err := store.AppendSequenced(ctx, next)
	if err != nil {
		t.Fatalf("AppendSequenced after expected conflict: %v", err)
	}
	if stored.EventSeq <= original.EventSeq {
		t.Fatalf("next EventSeq = %d, want greater than %d", stored.EventSeq, original.EventSeq)
	}

	constraint := sqliteConstraintError(t, store.db)
	if isFatalEventStoreWriteError(ctx, constraint) {
		t.Fatalf("SQLite constraint error classified as fatal: %v", constraint)
	}
}

func TestQueuedFatalStorageErrorBlocksLaterWrites(t *testing.T) {
	store := newTestStore(t)
	queue, _ := attachEventStoreTestQueue(t, store, false)
	defer shutdownEventStoreTestQueue(t, queue, true)
	if _, err := store.db.ExecContext(context.Background(), `PRAGMA query_only=ON`); err != nil {
		t.Fatalf("enable read-only writer failure: %v", err)
	}

	failed := eventFixture(modulecore.NewTraceID(), "queue.fatal.first", "dci")
	if _, err := store.AppendSequenced(context.Background(), failed); err == nil {
		t.Fatal("AppendSequenced under query_only returned nil, want SQLite storage error")
	} else if !isFatalEventStoreWriteError(context.Background(), err) {
		t.Fatalf("actual SQLite storage error classified as nonfatal: %v", err)
	}
	if _, err := store.db.ExecContext(context.Background(), `PRAGMA query_only=OFF`); err != nil {
		t.Fatalf("disable read-only writer failure: %v", err)
	}

	blocked := eventFixture(failed.TraceID, "queue.fatal.blocked", "dci")
	if _, err := store.AppendSequenced(context.Background(), blocked); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("AppendSequenced after fatal head = %v, want blocked queue failure", err)
	}
	assertEventAbsent(t, store, failed.EventID)
	assertEventAbsent(t, store, blocked.EventID)
}

func TestCancelledQueuedSynchronousWriteDoesNotMutate(t *testing.T) {
	store := newTestStore(t)
	queue, adapter, release := attachEventStoreTestQueueWithBlocker(t, store)
	defer shutdownEventStoreTestQueue(t, queue, false)
	defer release()

	canceled := eventFixture(modulecore.NewTraceID(), "queue.cancelled", "dci")
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := store.AppendSequenced(ctx, canceled)
		result <- err
	}()
	ticket := waitQueueTestTicket(t, adapter.tickets)
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("AppendSequenced(cancelled) = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled AppendSequenced did not return")
	}
	release()
	if err := queue.Wait(context.Background(), ticket); err != nil {
		t.Fatalf("wait for cancelled queue ticket: %v", err)
	}
	assertEventAbsent(t, store, canceled.EventID)
}

func TestQueuedStoreRejectsUnadmittedSequenceTransaction(t *testing.T) {
	store := newTestStore(t)
	queue, _ := attachEventStoreTestQueue(t, store, false)
	defer shutdownEventStoreTestQueue(t, queue, false)
	if tx, unlock, err := store.beginSequenceTx(context.Background()); err == nil {
		_ = tx.Rollback()
		unlock()
		t.Fatal("beginSequenceTx outside the queue returned nil error")
	} else if !strings.Contains(err.Error(), "write queue") {
		t.Fatalf("beginSequenceTx error = %v, want ordered write queue guard", err)
	}
}

func attachEventStoreTestQueue(t *testing.T, store *SQLiteStore, block bool) (*persistencequeue.Queue[persistencequeue.PersistenceCommand], *eventStoreTestWriteQueue) {
	t.Helper()
	if block {
		t.Fatal("use attachEventStoreTestQueueWithBlocker for a blocking queue")
	}
	queue, err := persistencequeue.NewPersistenceQueue(8, 1<<20)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	adapter := &eventStoreTestWriteQueue{queue: queue, tickets: make(chan uint64, 8)}
	store.writeQueue = adapter
	return queue, adapter
}

func attachEventStoreTestQueueWithBlocker(t *testing.T, store *SQLiteStore) (*persistencequeue.Queue[persistencequeue.PersistenceCommand], *eventStoreTestWriteQueue, func()) {
	t.Helper()
	queue, adapter := attachEventStoreTestQueue(t, store, false)
	started, releaseChannel := make(chan struct{}), make(chan struct{})
	var once sync.Once
	release := func() { once.Do(func() { close(releaseChannel) }) }
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.test-block", SizeBytes: 1,
		Apply: func(context.Context) error { close(started); <-releaseChannel; return nil },
	}, 1); err != nil {
		t.Fatalf("enqueue blocking queue head: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("blocking queue head did not start")
	}
	return queue, adapter, release
}

func shutdownEventStoreTestQueue(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand], wantFailure bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := queue.Shutdown(ctx)
	if wantFailure {
		if err == nil || !errors.Is(err, persistencequeue.ErrFailed) {
			t.Errorf("queue shutdown = %v, want recorded fatal failure", err)
		}
		return
	}
	if err != nil {
		t.Errorf("queue shutdown: %v", err)
	}
}

func sqliteConstraintError(t *testing.T, db *sql.DB) error {
	t.Helper()
	ctx := context.Background()
	var eventID, traceID, schema, eventType, component, occurredAt, envelope string
	var eventSeq int64
	if err := db.QueryRowContext(ctx, `SELECT event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json FROM event_envelope LIMIT 1`).Scan(
		&eventID, &eventSeq, &traceID, &schema, &eventType, &component, &occurredAt, &envelope,
	); err != nil {
		t.Fatalf("read event row for constraint probe: %v", err)
	}
	_, err := db.ExecContext(ctx, `INSERT INTO event_envelope(event_id,event_seq,trace_id,schema_version,event_type,component_id,occurred_at,envelope_json) VALUES(?,?,?,?,?,?,?,?)`,
		eventID, eventSeq, traceID, schema, eventType, component, occurredAt, envelope,
	)
	if err == nil {
		t.Fatal("duplicate event insert returned nil, want SQLite constraint error")
	}
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) || sqliteErr.Code()&0xff != 19 {
		t.Fatalf("duplicate event insert error = %T %v, want SQLite constraint", err, err)
	}
	return err
}

type eventStoreTestWriteQueue struct {
	queue   *persistencequeue.Queue[persistencequeue.PersistenceCommand]
	tickets chan uint64
}

func (q *eventStoreTestWriteQueue) Enqueue(ctx context.Context, sizeBytes int64, apply func(context.Context) error) (uint64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	ticket, err := q.queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "eventstore.test-write", SizeBytes: sizeBytes, Apply: apply,
	}, sizeBytes)
	if err == nil && q.tickets != nil {
		q.tickets <- ticket
	}
	return ticket, err
}

func (q *eventStoreTestWriteQueue) Wait(ctx context.Context, ticket uint64) error {
	return q.queue.Wait(ctx, ticket)
}

func waitQueueTestTicket(t *testing.T, tickets <-chan uint64) uint64 {
	t.Helper()
	select {
	case ticket := <-tickets:
		return ticket
	case <-time.After(time.Second):
		t.Fatal("EventStore command was not enqueued")
		return 0
	}
}

func waitQueuedAppendResult(t *testing.T, result <-chan struct {
	event modulecore.EventEnvelope
	err   error
}) struct {
	event modulecore.EventEnvelope
	err   error
} {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("queued AppendSequenced did not finish after queue drain")
		return struct {
			event modulecore.EventEnvelope
			err   error
		}{}
	}
}

func waitQueuedCommitResult(t *testing.T, result <-chan struct {
	batch CommittedBatch
	err   error
}) struct {
	batch CommittedBatch
	err   error
} {
	t.Helper()
	select {
	case got := <-result:
		return got
	case <-time.After(time.Second):
		t.Fatal("queued CommitTurnBundle did not finish after queue drain")
		return struct {
			batch CommittedBatch
			err   error
		}{}
	}
}
