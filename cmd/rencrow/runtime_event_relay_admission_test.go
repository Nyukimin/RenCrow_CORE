package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/conversationruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type relayAdmissionMarker struct{ calls atomic.Int32 }

func (m *relayAdmissionMarker) MarkConversationTurnResponsePublished(context.Context, modulecore.TurnID, modulecore.TraceID, modulecore.SessionID, time.Time) error {
	m.calls.Add(1)
	return nil
}

type relayReplayTrapStore struct {
	*eventstore.SQLiteStore
	replayCalls atomic.Int32
}

func (s *relayReplayTrapStore) ReadComponentPage(context.Context, string, modulecore.EventSeq, modulecore.EventSeq, int) ([]modulecore.EventEnvelope, modulecore.EventSeq, error) {
	s.replayCalls.Add(1)
	return nil, 0, errors.New("BindChat must not read the database")
}

type relayBlockingReplayStore struct {
	*eventstore.SQLiteStore
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	replayCalls atomic.Int32
}

func (s *relayBlockingReplayStore) ReadComponentPage(ctx context.Context, componentID string, after, through modulecore.EventSeq, limit int) ([]modulecore.EventEnvelope, modulecore.EventSeq, error) {
	s.replayCalls.Add(1)
	s.startOnce.Do(func() { close(s.started) })
	select {
	case <-ctx.Done():
		return nil, 0, ctx.Err()
	case <-s.release:
	}
	return s.SQLiteStore.ReadComponentPage(ctx, componentID, after, through, limit)
}

func (s *relayBlockingReplayStore) unblock() {
	s.releaseOnce.Do(func() { close(s.release) })
}

type relayObservedErrContext struct {
	context.Context
	checked  chan struct{}
	waiting  chan struct{}
	errOnce  sync.Once
	doneOnce sync.Once
}

func (c *relayObservedErrContext) Err() error {
	c.errOnce.Do(func() { close(c.checked) })
	return c.Context.Err()
}

func (c *relayObservedErrContext) Done() <-chan struct{} {
	c.doneOnce.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func newRelayAdmissionFixture(t *testing.T, reservedCommands int, expectQueueFailure ...bool) (*persistencequeue.Queue[persistencequeue.PersistenceCommand], *eventstore.SQLiteStore, *persistencequeue.Admission, context.Context) {
	t.Helper()
	queue, err := persistencequeue.NewPersistenceQueue(16, 1<<22)
	if err != nil {
		t.Fatalf("NewPersistenceQueue: %v", err)
	}
	writeQueue, err := eventstore.NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		t.Fatalf("NewPersistenceQueueWriteQueue: %v", err)
	}
	store, err := eventstore.NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "relay-admission.sqlite"), writeQueue)
	if err != nil {
		_ = queue.Shutdown(context.Background())
		t.Fatalf("NewSQLiteStoreWithWriteQueue: %v", err)
	}
	admission, err := persistencequeue.Admit(queue, reservedCommands, 1<<20)
	if err != nil {
		_ = store.Close()
		_ = queue.Shutdown(context.Background())
		t.Fatalf("persistencequeue.Admit: %v", err)
	}
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	allowExpectedFailure := len(expectQueueFailure) > 0 && expectQueueFailure[0]
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close EventStore: %v", err)
		}
	})
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil && !(allowExpectedFailure && errors.Is(err, persistencequeue.ErrFailed)) {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})
	t.Cleanup(func() {
		if err := admission.Release(); err != nil {
			t.Errorf("release persistence admission: %v", err)
		}
	})
	return queue, store, admission, ctx
}

func blockRelayQueueHead(t *testing.T, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) func() {
	t.Helper()
	started := make(chan struct{})
	release := make(chan struct{})
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerEvent, Kind: "relay.test.block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue queue blocker: %v", err)
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("queue blocker did not start")
	}
	var once sync.Once
	return func() { once.Do(func() { close(release) }) }
}

func TestEventRelayPublishesBoundChatAndPrivateGapBeforeDurableCommit(t *testing.T) {
	queue, store, admission, ctx := newRelayAdmissionFixture(t, 4)
	releaseQueue := blockRelayQueueHead(t, queue)
	defer releaseQueue()
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	monitor := viewer.NewMonitorStore(nil, archive)
	marker := &relayAdmissionMarker{}
	relay := &idleAwareEventListener{hub: hub, monitor: monitor, archive: archive, responseMarker: marker}
	traceID, sessionID, turnID := modulecore.NewTraceID(), modulecore.NewSessionID(), modulecore.NewTurnID()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	releaseChat, err := relay.BindChat(traceID, requestCtx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}

	first := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "first response", "CHAT", "", string(sessionID), "viewer", "chat-1")
	first.TurnID = turnID
	first.MessageID = modulecore.NewMessageID()
	firstDone := make(chan error, 1)
	started := time.Now()
	go func() { firstDone <- relay.OnEvent(first) }()
	select {
	case err := <-firstDone:
		if err != nil {
			t.Fatalf("OnEvent(first CHAT): %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("CHAT OnEvent waited for the blocked durable writer")
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("CHAT publication took %s with a blocked writer", elapsed)
	}
	firstProjected := hub.History()
	if len(firstProjected) != 1 || firstProjected[0].EventID != first.EventID || firstProjected[0].EventSeq != 1 {
		t.Fatalf("first live projection=%+v, want assigned EventID and EventSeq 1", firstProjected)
	}
	if marker.calls.Load() != 0 {
		t.Fatalf("durable response marker calls=%d, want none on the RAM-only CHAT response path", marker.calls.Load())
	}
	if got := relay.DurableEventSeq(); got != 0 {
		t.Fatalf("durable relay cursor=%d before writer commit, want 0", got)
	}

	private := modulecore.NewEventEnvelope(traceID, first.EventID, nil, "superagent", "superagent.run.finished", time.Now().UTC(), map[string]any{"status": "completed"})
	private.SessionID, private.TurnID = sessionID, turnID
	privateAccepted, _, err := store.AdmitSequenced(ctx, private)
	if err != nil {
		t.Fatalf("AdmitSequenced(private SuperAgent event): %v", err)
	}
	second := orchestrator.NewEventWithTraceID(traceID, "routing.decision", "mio", "shiro", "second event", "CHAT", "", string(sessionID), "viewer", "chat-1")
	if err := relay.OnEvent(second); err != nil {
		t.Fatalf("OnEvent(second CHAT): %v", err)
	}
	visible := hub.History()
	if len(visible) != 2 || visible[0].EventID != first.EventID || visible[0].EventSeq != 1 || visible[1].EventID != second.EventID || visible[1].EventSeq != 3 {
		t.Fatalf("visible Hub events=%+v, want CHAT seq 1 then 3 around private seq 2", visible)
	}
	monitorEvents := monitor.Logs(viewer.LogFilter{Limit: 10})
	if len(monitorEvents) != 2 || monitorEvents[0].EventID != second.EventID || monitorEvents[1].EventID != first.EventID {
		t.Fatalf("Monitor events=%+v, want only the two admitted CHAT events", monitorEvents)
	}
	if privateAccepted.EventSeq != 2 {
		t.Fatalf("private SuperAgent EventSeq=%d, want sequence gap 2", privateAccepted.EventSeq)
	}
	if got := store.OrchestratorCommittedHighWater(); got != 0 {
		t.Fatalf("committed orchestrator high-water while queue blocked=%d, want 0", got)
	}

	releaseChat()
	// The prior request is already visible in RAM but not durable yet. The next
	// lease can still start synchronously because its acquisition snapshot is
	// behind the live publication cursor, which proves the missing predecessor
	// check is intentionally one-sided.
	nextTraceID := modulecore.NewTraceID()
	releaseNextChat, err := relay.BindChat(nextTraceID, ctx)
	if err != nil {
		t.Fatalf("BindChat while prior accepted CHAT is still queued: %v", err)
	}
	releaseNextChat()
	// Once queue admission accepted the commands, cancellation belongs to the
	// caller only. The queue worker still owns and commits all accepted events.
	cancelRequest()
	if err := admission.Release(); err != nil {
		t.Fatalf("release unused admission capacity: %v", err)
	}
	releaseQueue()
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("wait for admitted EventStore writes: %v", err)
	}
	if got := store.OrchestratorCommittedHighWater(); got != 3 {
		t.Fatalf("committed orchestrator high-water=%d after queue drain, want 3", got)
	}
	if err := relay.PublishCommitted(context.Background(), conversationruntime.CommittedBundle{}); err != nil {
		t.Fatalf("PublishCommitted after admitted writes: %v", err)
	}
	if got := relay.DurableEventSeq(); got != 3 {
		t.Fatalf("durable relay cursor=%d after catch-up, want 3 including private global gap", got)
	}
	visible = hub.History()
	if len(visible) != 2 || visible[0].EventID != first.EventID || visible[1].EventID != second.EventID {
		t.Fatalf("durable catch-up duplicated or changed live events: %+v", visible)
	}
}

func TestEventRelayBindChatRejectsUnpublishedV2PredecessorWithoutReplayRead(t *testing.T) {
	_, store, admission, ctx := newRelayAdmissionFixture(t, 8)
	syncPredecessor, err := store.AppendSequenced(context.Background(), modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "orchestrator", "legacy.sync", time.Now().UTC(), map[string]any{"content": "synchronous predecessor"}))
	if err != nil {
		t.Fatalf("AppendSequenced synchronous predecessor: %v", err)
	}
	traceID := modulecore.NewTraceID()
	sessionID, turnID := modulecore.NewSessionID(), modulecore.NewTurnID()
	input := modulecore.NewEventEnvelope(traceID, "", nil, "orchestrator", "conversation.user_message", time.Now().UTC(), map[string]any{
		"from": "user", "source_actor_id": "user-1", "to": "mio", "content": "v2 input", "text": "v2 input",
	})
	input.SessionID, input.TurnID, input.MessageID = sessionID, turnID, modulecore.NewMessageID()
	accepted, err := store.AcceptTurn(context.Background(), eventstore.AcceptInput{
		OperationID: "relay-gap-accept", OwnerID: "user-1", RuntimeVersion: "v2",
		ClientSubmissionID: "relay-gap-submission", ActorID: "user-1", TriggerKind: "user_message",
		GenerationEpoch: 1, CreateNewSession: true, InputEvent: input,
		InputMessage: eventstore.MessageIndex{
			MessageID: input.MessageID, EventID: input.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "user-1", ToKind: "agent", ToActor: "mio", Kind: "user_message", Visibility: "public",
		},
	})
	if err != nil {
		t.Fatalf("AcceptTurn v2 input: %v", err)
	}
	reply := modulecore.NewEventEnvelope(traceID, accepted.InputEvent.EventID, nil, "orchestrator", "agent.response", time.Now().UTC(), map[string]any{
		"from": "mio", "source_actor_id": "mio", "to": "user", "content": "v2 response", "text": "v2 response",
	})
	reply.SessionID, reply.TurnID, reply.MessageID = sessionID, turnID, modulecore.NewMessageID()
	committed, err := store.CommitTurnBundle(context.Background(), eventstore.CommitInput{
		OperationID: "relay-gap-commit", SessionID: sessionID, TurnID: turnID,
		GenerationEpoch: 1, ResultState: "completed", Events: []modulecore.EventEnvelope{reply},
		MessageIndexes: []eventstore.MessageIndex{{
			MessageID: reply.MessageID, EventID: reply.EventID, SessionID: sessionID, TurnID: turnID,
			FromActor: "mio", ToKind: "user", ToActor: "user-1", Kind: "assistant_message", Visibility: "public",
		}},
	})
	if err != nil {
		t.Fatalf("CommitTurnBundle v2 response: %v", err)
	}
	if len(committed.Events) != 1 {
		t.Fatalf("v2 committed events=%d, want 1", len(committed.Events))
	}
	if got := store.OrchestratorCommittedHighWater(); got != committed.Events[0].EventSeq || got <= syncPredecessor.EventSeq {
		t.Fatalf("committed high-water=%d, want v2 response seq %d after sync predecessor %d", got, committed.Events[0].EventSeq, syncPredecessor.EventSeq)
	}

	trap := &relayReplayTrapStore{SQLiteStore: store}
	archive, err := viewer.NewCanonicalEventLog(trap)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	relay := &idleAwareEventListener{archive: archive}
	started := time.Now()
	if _, err := relay.BindChat(modulecore.NewTraceID(), ctx); !errors.Is(err, errChatPredecessorUnpublished) {
		t.Fatalf("BindChat after committed but unpublished v2 output=%v, want fail-closed predecessor error", err)
	}
	if time.Since(started) >= 300*time.Millisecond {
		t.Fatalf("BindChat performed a slow read or waited for durable replay: %s", time.Since(started))
	}
	if got := trap.replayCalls.Load(); got != 0 {
		t.Fatalf("BindChat called durable ReadComponentPage %d times, want zero", got)
	}
	if got := relay.DurableEventSeq(); got != 0 {
		t.Fatalf("relay durable cursor=%d after fail-closed bind, want unchanged 0", got)
	}
	_ = admission.Release()
}

func TestEventRelayBindChatFailsFastWhileDurableReplayIsBlocked(t *testing.T) {
	_, store, admission, _ := newRelayAdmissionFixture(t, 4)
	blocking := &relayBlockingReplayStore{
		SQLiteStore: store,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	t.Cleanup(blocking.unblock)
	archive, err := viewer.NewCanonicalEventLog(blocking)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	relay := &idleAwareEventListener{archive: archive}
	drainDone := make(chan error, 1)
	go func() {
		drainDone <- relay.PublishCommitted(context.Background(), conversationruntime.CommittedBundle{})
	}()
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("durable replay did not reach the blocked EventStore read")
	}

	started := time.Now()
	if _, err := relay.BindChat(modulecore.NewTraceID(), context.Background()); !errors.Is(err, errChatPublicationBusy) {
		t.Fatalf("BindChat while replay holds publication mutex=%v, want immediate busy error", err)
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("BindChat waited %s for blocked durable replay", elapsed)
	}
	blocking.unblock()
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("PublishCommitted after unblocking replay: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("durable replay did not finish after unblocking EventStore read")
	}
	if got := blocking.replayCalls.Load(); got != 1 {
		t.Fatalf("blocked replay calls=%d, want exactly 1", got)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release unused admission capacity: %v", err)
	}
}

func TestEventRelayDurableReplayWaitsOutsidePublicationLockDuringChat(t *testing.T) {
	_, store, admission, admissionCtx := newRelayAdmissionFixture(t, 4)
	blocking := &relayBlockingReplayStore{
		SQLiteStore: store,
		started:     make(chan struct{}),
		release:     make(chan struct{}),
	}
	t.Cleanup(blocking.unblock)
	archive, err := viewer.NewCanonicalEventLog(blocking)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{archive: archive, hub: hub}
	traceID := modulecore.NewTraceID()
	releaseChat, err := relay.BindChat(traceID, admissionCtx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	defer releaseChat()

	observedCtx := &relayObservedErrContext{Context: context.Background(), checked: make(chan struct{}), waiting: make(chan struct{})}
	drainDone := make(chan error, 1)
	go func() { drainDone <- relay.PublishCommitted(observedCtx, conversationruntime.CommittedBundle{}) }()
	select {
	case <-observedCtx.checked:
	case <-time.After(time.Second):
		t.Fatal("PublishCommitted did not enter its CHAT lease gate")
	}
	select {
	case <-observedCtx.waiting:
	case <-time.After(time.Second):
		t.Fatal("PublishCommitted did not wait on the active CHAT lease")
	}
	if got := blocking.replayCalls.Load(); got != 0 {
		t.Fatalf("durable replay entered EventStore %d times while CHAT lease was active", got)
	}

	event := orchestrator.NewEventWithTraceID(traceID, "message.received", "user", "mio", "admitted while replay waits", "CHAT", "", "", "viewer", "chat-1")
	eventDone := make(chan error, 1)
	started := time.Now()
	go func() { eventDone <- relay.OnEvent(event) }()
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("CHAT OnEvent while durable replay waits: %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("CHAT OnEvent waited behind a durable replay call")
	}
	if elapsed := time.Since(started); elapsed >= 300*time.Millisecond {
		t.Fatalf("CHAT OnEvent took %s while durable replay waited", elapsed)
	}
	if got := blocking.replayCalls.Load(); got != 0 {
		t.Fatalf("durable replay entered EventStore %d times before CHAT lease release", got)
	}
	if history := hub.History(); len(history) != 1 || history[0].EventID != event.EventID || history[0].EventSeq != 1 {
		t.Fatalf("CHAT history while replay waits=%+v, want assigned seq 1", history)
	}

	releaseChat()
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("wait for admitted CHAT commit: %v", err)
	}
	select {
	case <-blocking.started:
	case <-time.After(time.Second):
		t.Fatal("durable replay did not continue after CHAT lease release")
	}
	blocking.unblock()
	select {
	case err := <-drainDone:
		if err != nil {
			t.Fatalf("PublishCommitted after CHAT release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("PublishCommitted did not finish after EventStore replay unblocked")
	}
	if got := blocking.replayCalls.Load(); got != 1 {
		t.Fatalf("durable replay calls=%d, want one after CHAT lease release", got)
	}
}

func TestEventRelayUnboundChatUsesSynchronousPublication(t *testing.T) {
	_, store, admission, _ := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	relay := &idleAwareEventListener{archive: archive, hub: viewer.NewEventHub(8)}
	chat := orchestrator.NewEventWithTraceID(modulecore.NewTraceID(), "message.received", "user", "mio", "ordinary CHAT", "CHAT", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(chat); err != nil {
		t.Fatalf("unbound CHAT OnEvent: %v", err)
	}
	history := relay.hub.History()
	if len(history) != 1 || history[0].EventID != chat.EventID || history[0].EventSeq != 1 {
		t.Fatalf("unbound CHAT history=%+v, want durable event seq 1", history)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release unused admission capacity: %v", err)
	}
}

func TestEventRelayForeignChatWaitsForActiveLeaseThenUsesSynchronousPublication(t *testing.T) {
	_, store, admission, ctx := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{archive: archive, hub: hub}
	boundTraceID, foreignTraceID := modulecore.NewTraceID(), modulecore.NewTraceID()
	releaseChat, err := relay.BindChat(boundTraceID, ctx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	foreign := orchestrator.NewEventWithTraceID(foreignTraceID, "message.received", "user", "mio", "foreign CHAT", "CHAT", "", "", "viewer", "chat-1")
	foreignDone := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		foreignDone <- relay.OnEvent(foreign)
	}()
	<-started
	select {
	case err := <-foreignDone:
		t.Fatalf("foreign CHAT returned before active lease release: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	bound := orchestrator.NewEventWithTraceID(boundTraceID, "message.received", "user", "mio", "admitted CHAT", "CHAT", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(bound); err != nil {
		t.Fatalf("bound CHAT OnEvent: %v", err)
	}
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("wait for bound CHAT commit: %v", err)
	}
	releaseChat()
	select {
	case err := <-foreignDone:
		if err != nil {
			t.Fatalf("foreign CHAT durable publication after lease release: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreign CHAT did not continue after active lease release")
	}
	history := hub.History()
	if len(history) != 2 || history[0].EventID != bound.EventID || history[0].EventSeq != 1 || history[1].EventID != foreign.EventID || history[1].EventSeq != 2 {
		t.Fatalf("bound and foreign CHAT history=%+v, want seq 1 then durable seq 2", history)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
}

func TestEventRelayFallbackTraceUsesSynchronousCHATAfterBarrierAndLeaseRelease(t *testing.T) {
	_, store, admission, ctx := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{archive: archive, hub: hub}
	traceID := modulecore.NewTraceID()
	releaseChat, err := relay.BindChat(traceID, ctx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	first := orchestrator.NewEventWithTraceID(traceID, "message.received", "user", "mio", "admitted before fallback", "CHAT", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(first); err != nil {
		t.Fatalf("bound CHAT OnEvent: %v", err)
	}
	barrierCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); err != nil {
		t.Fatalf("fallback pre-switch barrier: %v", err)
	}
	if err := relay.MarkChatSynchronous(traceID); err != nil {
		t.Fatalf("MarkChatSynchronous after fallback barrier: %v", err)
	}
	final := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "synchronous fallback response", "CHAT", "", "", "viewer", "chat-1")
	finalDone := make(chan error, 1)
	go func() { finalDone <- relay.OnEvent(final) }()
	select {
	case err := <-finalDone:
		t.Fatalf("fallback response published before lease release: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	releaseChat()
	select {
	case err := <-finalDone:
		if err != nil {
			t.Fatalf("fallback response synchronous publication: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("fallback response did not publish after lease release")
	}
	history := hub.History()
	if len(history) != 2 || history[0].EventID != first.EventID || history[0].EventSeq != 1 || history[1].EventID != final.EventID || history[1].EventSeq != 2 {
		t.Fatalf("fallback history=%+v, want admitted seq 1 and sync CHAT seq 2", history)
	}
	if got := relay.DurableEventSeq(); got != 2 {
		t.Fatalf("durable relay cursor=%d after synchronous fallback, want 2", got)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
}

func TestRuntimeLegacyFallbackPublishesFinalCHATSynchronously(t *testing.T) {
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatalf("newRuntimePersistenceQueue: %v", err)
	}
	writeQueue, err := eventstore.NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		shutdownRuntimeQueue(t, queue)
		t.Fatalf("NewPersistenceQueueWriteQueue: %v", err)
	}
	store, err := eventstore.NewSQLiteStoreWithWriteQueue(filepath.Join(t.TempDir(), "runtime-fallback.sqlite"), writeQueue)
	if err != nil {
		shutdownRuntimeQueue(t, queue)
		t.Fatalf("NewSQLiteStoreWithWriteQueue: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close EventStore: %v", err)
		}
	})
	t.Cleanup(func() { shutdownRuntimeQueue(t, queue) })
	taskStore, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true})
	if err != nil {
		t.Fatalf("NewJSONLStoreWithOptions: %v", err)
	}
	defer taskStore.Close()
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{archive: archive, hub: hub}
	identity := viewer.LegacyPreSessionAdmissionIdentity{
		TraceID: modulecore.NewTraceID(), Recipient: "mio", OperationSource: "RenCrow_CMD",
	}
	ctx, release, err := admitRuntimeLegacyViewerChat(context.Background(), queue, taskStore, true, relay, identity)
	if err != nil {
		t.Fatalf("admitRuntimeLegacyViewerChat: %v", err)
	}
	defer release()
	first := orchestrator.NewEventWithTraceID(identity.TraceID, "message.received", "user", "mio", "admitted before fallback", "CHAT", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(first); err != nil {
		t.Fatalf("bound CHAT OnEvent: %v", err)
	}
	fallback, ok := persistencequeue.SynchronousFallbackFromContext(ctx)
	if !ok {
		t.Fatal("admitted runtime context has no fallback capability")
	}
	fallbackCtx, err := fallback(ctx)
	if err != nil {
		t.Fatalf("runtime CHAT fallback: %v", err)
	}
	if _, ok := persistencequeue.AdmissionFromContext(fallbackCtx); ok {
		t.Fatal("fallback context still carries RAM admission")
	}
	final := orchestrator.NewEventWithTraceID(identity.TraceID, "agent.response", "mio", "user", "final synchronous response", "CHAT", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(final); err != nil {
		t.Fatalf("final CHAT event after fallback: %v", err)
	}
	history := hub.History()
	if len(history) != 2 || history[0].EventID != first.EventID || history[0].EventSeq != 1 || history[1].EventID != final.EventID || history[1].EventSeq != 2 {
		t.Fatalf("runtime fallback CHAT history=%+v, want RAM seq 1 then synchronous seq 2", history)
	}
	if got := relay.DurableEventSeq(); got != 2 {
		t.Fatalf("durable cursor=%d after runtime fallback, want 2", got)
	}
}

func TestEventRelayLostAdmittedTraceFailsClosed(t *testing.T) {
	_, store, admission, ctx := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	relay := &idleAwareEventListener{archive: archive, hub: viewer.NewEventHub(8)}
	traceID := modulecore.NewTraceID()
	releaseChat, err := relay.BindChat(traceID, ctx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	if _, err := relay.BindChat(traceID, ctx); !errors.Is(err, errChatTraceIDAlreadyBound) {
		t.Fatalf("duplicate BindChat=%v, want duplicate trace error", err)
	}
	relay.mu.Lock()
	relay.chatBinding = nil // Simulate an unexpected relay binding loss; trace scope remains admitted.
	relay.mu.Unlock()
	lost := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "must fail closed", "WEB", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(lost); !errors.Is(err, errChatTraceIDNotBound) {
		t.Fatalf("lost admitted CHAT OnEvent=%v, want fail-closed lost-scope error", err)
	}
	if got := len(relay.hub.History()); got != 0 {
		t.Fatalf("lost admitted trace published %d events", got)
	}
	releaseChat()
	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
}

func TestEventRelayAcceptedFatalHeadDoesNotPublishFollowingCHAT(t *testing.T) {
	queue, store, admission, ctx := newRelayAdmissionFixture(t, 4, true)
	first, err := store.AppendSequenced(context.Background(), modulecore.NewEventEnvelope(modulecore.NewTraceID(), "", nil, "superagent", "seed", time.Now().UTC(), map[string]any{"content": "seed"}))
	if err != nil {
		t.Fatalf("seed durable event: %v", err)
	}
	releaseQueue := blockRelayQueueHead(t, queue)
	defer releaseQueue()
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{hub: hub, archive: archive}
	traceID := modulecore.NewTraceID()
	releaseChat, err := relay.BindChat(traceID, ctx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	duplicate := orchestrator.NewEventWithTraceID(traceID, "message.received", "user", "mio", "accepted but duplicate id", "CHAT", "", "", "viewer", "chat-1")
	duplicate.EventID = first.EventID
	if err := relay.OnEvent(duplicate); err != nil {
		t.Fatalf("OnEvent duplicate accepted head: %v", err)
	}
	releaseQueue()
	if err := admission.Barrier(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate event_id") {
		t.Fatalf("fatal accepted-head barrier=%v, want duplicate event ID failure", err)
	}
	following := orchestrator.NewEventWithTraceID(traceID, "routing.decision", "mio", "shiro", "must not publish", "CHAT", "", "", "viewer", "chat-1")
	if err := relay.OnEvent(following); err == nil || !strings.Contains(err.Error(), "duplicate event_id") {
		t.Fatalf("OnEvent after fatal accepted queue head=%v, want fatal duplicate error", err)
	}
	if got := len(hub.History()); got != 1 || hub.History()[0].EventID != first.EventID {
		t.Fatalf("Hub history after fatal head=%+v, want only first accepted event", hub.History())
	}
	releaseChat()
	_ = admission.Release()
}
