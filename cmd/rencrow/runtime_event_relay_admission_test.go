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
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
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

type relayObservedWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *relayObservedWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

type relayIdleEventGate struct {
	ctx     context.Context
	current atomic.Bool
}

func (g *relayIdleEventGate) EventContext(modulecore.TraceID, modulecore.SessionID) (context.Context, bool) {
	if g == nil || !g.current.Load() || g.ctx == nil || g.ctx.Err() != nil {
		return nil, false
	}
	return g.ctx, true
}

func (g *relayIdleEventGate) IsCurrentEvent(modulecore.TraceID, modulecore.SessionID) bool {
	return g != nil && g.current.Load() && g.ctx != nil && g.ctx.Err() == nil
}

func (g *relayIdleEventGate) PublishIfCurrentEvent(_ modulecore.TraceID, _ modulecore.SessionID, publish func() error) (bool, error) {
	if !g.IsCurrentEvent("", "") {
		return false, nil
	}
	return true, publish()
}

func (g *relayIdleEventGate) NotifyActivity() {}

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
	monitor := newPublicRelayFixtureMonitor(archive)
	marker := &relayAdmissionMarker{}
	relay := &idleAwareEventListener{hub: hub, monitor: monitor, archive: archive, responseMarker: marker}
	traceID, sessionID, turnID := modulecore.NewTraceID(), modulecore.NewSessionID(), modulecore.NewTurnID()
	requestCtx, cancelRequest := context.WithCancel(ctx)
	defer cancelRequest()
	releaseChat, err := relay.BindChat(traceID, requestCtx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}

	first := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "first response", "CHAT", "", string(sessionID), "viewer", "chat-1"))
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
	second := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "routing.decision", "mio", "shiro", "second event", "CHAT", "", string(sessionID), "viewer", "chat-1"))
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

type guardedMioRelayTestQualification struct{}

func (guardedMioRelayTestQualification) Use(claim func() error) error { return claim() }
func (q guardedMioRelayTestQualification) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	return q
}
func (guardedMioRelayTestQualification) Release() {}

type guardedRelayQualificationProbeState struct {
	relay           *idleAwareEventListener
	guardActive     atomic.Bool
	releases        atomic.Int32
	releasedInGuard atomic.Bool
	releasedInRelay atomic.Bool
}

type guardedRelayQualificationProbe struct {
	state      *guardedRelayQualificationProbeState
	releaseOne sync.Once
}

func (q *guardedRelayQualificationProbe) Use(claim func() error) error { return claim() }

func (q *guardedRelayQualificationProbe) Retain() domainconversation.PreparedChatTurnPublicationGuard {
	return &guardedRelayQualificationProbe{state: q.state}
}

func (q *guardedRelayQualificationProbe) Release() {
	if q == nil || q.state == nil {
		return
	}
	q.releaseOne.Do(func() {
		if q.state.guardActive.Load() {
			q.state.releasedInGuard.Store(true)
		}
		if q.state.relay != nil {
			if !q.state.relay.publicationMu.TryLock() {
				q.state.releasedInRelay.Store(true)
			} else {
				q.state.relay.publicationMu.Unlock()
			}
		}
		q.state.releases.Add(1)
	})
}

func TestGuardedMioFinalWaitsForRelayOrderBeforeAdmission(t *testing.T) {
	_, store, _, admittedContext := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{hub: hub, archive: archive}
	traceID, sessionID, turnID := modulecore.NewTraceID(), modulecore.NewSessionID(), modulecore.NewTurnID()
	taskID, runID, messageID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewMessageID()
	requestCtx, cancelRequest := context.WithCancel(admittedContext)
	defer cancelRequest()
	releaseChat, err := relay.BindChat(traceID, requestCtx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	defer releaseChat()

	ev := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "answer", "CHAT", taskID.String(), string(sessionID), "viewer", "cmd:ren")
	ev.Visibility = "private"
	ev.PublicationIncarnation = "incarnation-v1"
	ev.TurnID = turnID
	ev.RunID = runID
	ev.MessageID = messageID
	ev.ActorKind = "agent"
	ev.ActorID = "mio"
	admissionStarted := make(chan struct{}, 1)
	result := make(chan struct {
		event orchestrator.OrchestratorEvent
		err   error
	}, 1)
	relay.publicationMu.Lock()
	go func() {
		accepted, publishErr := relay.PublishGuardedCanonicalEvent(ev, "ren", func(publish func() error) error {
			admissionStarted <- struct{}{}
			return publish()
		}, guardedMioRelayTestQualification{})
		result <- struct {
			event orchestrator.OrchestratorEvent
			err   error
		}{event: accepted, err: publishErr}
	}()
	select {
	case <-admissionStarted:
		relay.publicationMu.Unlock()
		t.Fatal("final event admission began before the relay acquired FIFO order")
	case <-time.After(30 * time.Millisecond):
	}
	relay.publicationMu.Unlock()
	select {
	case outcome := <-result:
		if outcome.err != nil {
			t.Fatalf("PublishGuardedCanonicalEvent: %v", outcome.err)
		}
		if outcome.event.EventSeq <= 0 || outcome.event.EventID != ev.EventID {
			t.Fatalf("accepted final identity=%+v, want positive sequence and unchanged EventID %s", outcome.event, ev.EventID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("guarded final event did not finish after relay FIFO lock was released")
	}
	if len(hub.History()) != 1 {
		t.Fatalf("canonical final history count=%d, want one", len(hub.History()))
	}
}

func TestGuardedMioFinalPreservesCanonicalAcceptanceWhenLiveFanoutFails(t *testing.T) {
	queue, store, _, admittedContext := newRelayAdmissionFixture(t, 4)
	releaseQueue := blockRelayQueueHead(t, queue)
	defer releaseQueue()
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	prior := orchestrator.NewEventWithTraceID(modulecore.NewTraceID(), "routing.decision", "mio", "user", "prior", "CHAT", "", "", "viewer", "chat-prior")
	prior.EventSeq = 1_000_000
	if published, err := hub.PublishAccepted(prior); err != nil || !published {
		t.Fatalf("seed EventHub sequence conflict: published=%t err=%v", published, err)
	}
	relay := &idleAwareEventListener{hub: hub, archive: archive}
	traceID, sessionID, turnID := modulecore.NewTraceID(), modulecore.NewSessionID(), modulecore.NewTurnID()
	taskID, runID, messageID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewMessageID()
	requestCtx, cancelRequest := context.WithCancel(admittedContext)
	defer cancelRequest()
	releaseChat, err := relay.BindChat(traceID, requestCtx)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	defer releaseChat()

	ev := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "answer", "CHAT", taskID.String(), string(sessionID), "viewer", "cmd:ren")
	ev.Visibility = "private"
	ev.PublicationIncarnation = "incarnation-v1"
	ev.TurnID = turnID
	ev.RunID = runID
	ev.MessageID = messageID
	ev.ActorKind = "agent"
	ev.ActorID = "mio"
	queueBefore := queue.Status().Pending
	admissionCalls := 0
	accepted, err := relay.PublishGuardedCanonicalEvent(ev, "ren", func(publish func() error) error {
		admissionCalls++
		return publish()
	}, guardedMioRelayTestQualification{})
	var liveErr *orchestrator.GuardedCanonicalLiveDeliveryError
	if !errors.As(err, &liveErr) || !errors.Is(err, orchestrator.ErrGuardedCanonicalLiveDeliveryFailed) {
		t.Fatalf("PublishGuardedCanonicalEvent error=%v, want typed post-admission live failure", err)
	}
	if admissionCalls != 1 || accepted.EventSeq <= 0 || accepted.EventID != ev.EventID ||
		liveErr.AcceptedEvent.EventSeq != accepted.EventSeq || liveErr.AcceptedEvent.EventID != accepted.EventID {
		t.Fatalf("canonical accepted event=%+v typed=%+v admissions=%d; want one C admission and exact accepted identity", accepted, liveErr.AcceptedEvent, admissionCalls)
	}
	if got := queue.Status().Pending; got <= queueBefore {
		t.Fatalf("EventStore queue pending=%d before=%d, want one RAM admission retained despite live W failure", got, queueBefore)
	}
	for _, queued := range hub.History() {
		if queued.EventID == ev.EventID {
			t.Fatalf("final EventID=%s reached Hub despite live sequence conflict", ev.EventID)
		}
	}
	if relay.livePublicationCursor != 0 {
		t.Fatalf("relay live cursor=%d after failed W, want unchanged cursor", relay.livePublicationCursor)
	}
}

func TestGuardedMioPublicationCleanupRunsAfterGuardAndRelayUnlock(t *testing.T) {
	for _, failLive := range []bool{false, true} {
		name := "success"
		if failLive {
			name = "hub-error-after-canonical-admission"
		}
		t.Run(name, func(t *testing.T) {
			_, store, _, admittedContext := newRelayAdmissionFixture(t, 4)
			archive, err := viewer.NewCanonicalEventLog(store)
			if err != nil {
				t.Fatalf("NewCanonicalEventLog: %v", err)
			}
			hub := viewer.NewEventHub(8)
			if failLive {
				prior := orchestrator.NewEventWithTraceID(modulecore.NewTraceID(), "routing.decision", "mio", "user", "prior", "CHAT", "", "", "viewer", "chat-prior")
				prior.EventSeq = 1_000_000
				if published, err := hub.PublishAccepted(prior); err != nil || !published {
					t.Fatalf("seed EventHub sequence conflict: published=%t err=%v", published, err)
				}
			}
			relay := &idleAwareEventListener{hub: hub, archive: archive}
			state := &guardedRelayQualificationProbeState{relay: relay}
			qualification := &guardedRelayQualificationProbe{state: state}
			traceID, sessionID, turnID := modulecore.NewTraceID(), modulecore.NewSessionID(), modulecore.NewTurnID()
			taskID, runID, messageID := modulecore.NewTaskID(), modulecore.NewRunID(), modulecore.NewMessageID()
			requestCtx, cancelRequest := context.WithCancel(admittedContext)
			defer cancelRequest()
			releaseChat, err := relay.BindChat(traceID, requestCtx)
			if err != nil {
				t.Fatalf("BindChat: %v", err)
			}
			defer releaseChat()

			ev := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "answer", "CHAT", taskID.String(), string(sessionID), "viewer", "cmd:ren")
			ev.Visibility = "private"
			ev.PublicationIncarnation = "incarnation-v1"
			ev.TurnID = turnID
			ev.RunID = runID
			ev.MessageID = messageID
			ev.ActorKind = "agent"
			ev.ActorID = "mio"
			accepted, publishErr := relay.PublishGuardedCanonicalEvent(ev, "ren", func(publish func() error) error {
				state.guardActive.Store(true)
				err := publish()
				state.guardActive.Store(false)
				return err
			}, qualification)
			if failLive {
				var liveErr *orchestrator.GuardedCanonicalLiveDeliveryError
				if !errors.As(publishErr, &liveErr) || accepted.EventSeq <= 0 || liveErr.AcceptedEvent.EventID != accepted.EventID {
					t.Fatalf("post-admission result=%+v err=%v, want accepted C with typed W failure", accepted, publishErr)
				}
			} else if publishErr != nil || accepted.EventSeq <= 0 {
				t.Fatalf("successful guarded publication result=%+v err=%v", accepted, publishErr)
			}
			if state.releasedInGuard.Load() || state.releasedInRelay.Load() {
				t.Fatalf("qualification released under guard=%t relay lock=%t", state.releasedInGuard.Load(), state.releasedInRelay.Load())
			}
			if got := state.releases.Load(); got != 1 {
				t.Fatalf("producer qualification release count=%d, want exactly one cleanup release", got)
			}
		})
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

	event := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "message.received", "user", "mio", "admitted while replay waits", "CHAT", "", "", "viewer", "chat-1"))
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
	chat := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(modulecore.NewTraceID(), "message.received", "user", "mio", "ordinary CHAT", "CHAT", "", "", "viewer", "chat-1"))
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
	foreign := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(foreignTraceID, "message.received", "user", "mio", "foreign CHAT", "CHAT", "", "", "viewer", "chat-1"))
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

	bound := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(boundTraceID, "message.received", "user", "mio", "admitted CHAT", "CHAT", "", "", "viewer", "chat-1"))
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

func TestEventRelayIdleChatBindingWaitCancelsWithForegroundStop(t *testing.T) {
	_, store, _, admittedContext := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{archive: archive, hub: hub}
	boundTraceID := modulecore.NewTraceID()
	releaseChat, err := relay.BindChat(boundTraceID, admittedContext)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	defer releaseChat()

	idleBaseContext, cancelIdle := context.WithCancel(context.Background())
	idleContext := &relayObservedWaitContext{Context: idleBaseContext, waiting: make(chan struct{})}
	gate := &relayIdleEventGate{ctx: idleContext}
	gate.current.Store(true)
	relay.mu.Lock()
	relay.idleChat = gate
	relay.mu.Unlock()
	idleEvent := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(
		modulecore.NewTraceID(), "idlechat.message", "mio", "shiro", "old idle line",
		"IDLECHAT", "", string(modulecore.NewSessionID()), "idlechat", "idlechat",
	))
	eventDone := make(chan error, 1)
	go func() { eventDone <- relay.OnEvent(idleEvent) }()
	select {
	case <-idleContext.waiting:
	case <-time.After(time.Second):
		cancelIdle()
		t.Fatal("IdleChat event did not enter the CHAT binding wait")
	}

	// Foreground stop cancels the owner context while the CHAT binding remains
	// active; the IdleChat relay must leave without appending or publishing.
	gate.current.Store(false)
	cancelIdle()
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("cancelled IdleChat relay event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("IdleChat event remained blocked after its owner context was cancelled")
	}
	if history := hub.History(); len(history) != 0 {
		t.Fatalf("hub history after cancelled IdleChat wait=%+v, want empty", history)
	}
	if relay.publicationCursor != 0 {
		t.Fatalf("durable publication cursor=%d, want 0 for cancelled event", relay.publicationCursor)
	}
}

type relayStopRaceGate struct {
	ctx              context.Context
	mu               sync.Mutex
	current          bool
	publishRequested chan struct{}
	allowPublish     chan struct{}
	requestOnce      sync.Once
}

func (g *relayStopRaceGate) EventContext(modulecore.TraceID, modulecore.SessionID) (context.Context, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.ctx, g.current && g.ctx != nil && g.ctx.Err() == nil
}

func (g *relayStopRaceGate) IsCurrentEvent(modulecore.TraceID, modulecore.SessionID) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current && g.ctx != nil && g.ctx.Err() == nil
}

func (g *relayStopRaceGate) PublishIfCurrentEvent(_ modulecore.TraceID, _ modulecore.SessionID, publish func() error) (bool, error) {
	g.requestOnce.Do(func() { close(g.publishRequested) })
	<-g.allowPublish
	g.mu.Lock()
	defer g.mu.Unlock()
	if !g.current || g.ctx == nil || g.ctx.Err() != nil {
		return false, nil
	}
	return true, publish()
}

func (g *relayStopRaceGate) stop() {
	g.mu.Lock()
	g.current = false
	g.mu.Unlock()
}

func (*relayStopRaceGate) NotifyActivity() {}

func TestEventRelayDoesNotPublishIdleEventAfterStopWinsFinalAcceptance(t *testing.T) {
	_, store, admission, admittedContext := newRelayAdmissionFixture(t, 4)
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog: %v", err)
	}
	hub := viewer.NewEventHub(8)
	relay := &idleAwareEventListener{archive: archive, hub: hub}
	traceID, sessionID := modulecore.NewTraceID(), modulecore.NewSessionID()
	releaseChat, err := relay.BindChat(traceID, admittedContext)
	if err != nil {
		t.Fatalf("BindChat: %v", err)
	}
	defer releaseChat()
	gate := &relayStopRaceGate{
		ctx: context.Background(), current: true,
		publishRequested: make(chan struct{}), allowPublish: make(chan struct{}),
	}
	relay.mu.Lock()
	relay.idleChat = gate
	relay.mu.Unlock()
	event := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "idlechat.message", "mio", "shiro", "stale idle line", "IDLECHAT", "", string(sessionID), "idlechat", "idlechat"))
	eventDone := make(chan error, 1)
	go func() { eventDone <- relay.OnEvent(event) }()
	select {
	case <-gate.publishRequested:
	case err := <-eventDone:
		t.Fatalf("IdleChat relay bypassed the final generation acceptance gate: err=%v history=%+v", err, hub.History())
	case <-time.After(time.Second):
		t.Fatal("IdleChat relay did not reach the final live publication boundary")
	}

	// Stop wins after the relay's early current-generation checks but before
	// final presentation acceptance. The owner gate must reject the old event.
	gate.stop()
	close(gate.allowPublish)
	select {
	case err := <-eventDone:
		if err != nil {
			t.Fatalf("stale IdleChat relay event: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("IdleChat relay did not finish after stop won publication acceptance")
	}
	if got := hub.History(); len(got) != 0 {
		t.Fatalf("Hub presented old IdleChat generation after stop: %+v", got)
	}
	if err := admission.Barrier(context.Background()); err != nil {
		t.Fatalf("wait for admitted event durability: %v", err)
	}
	releaseChat()
	if err := relay.PublishCommitted(context.Background(), conversationruntime.CommittedBundle{}); err != nil {
		t.Fatalf("drain durable stale event: %v", err)
	}
	if got := hub.History(); len(got) != 0 {
		t.Fatalf("durable replay presented old IdleChat generation: %+v", got)
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
	first := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "message.received", "user", "mio", "admitted before fallback", "CHAT", "", "", "viewer", "chat-1"))
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
	final := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "synchronous fallback response", "CHAT", "", "", "viewer", "chat-1"))
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
	first := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(identity.TraceID, "message.received", "user", "mio", "admitted before fallback", "CHAT", "", "", "viewer", "chat-1"))
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
	final := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(identity.TraceID, "agent.response", "mio", "user", "final synchronous response", "CHAT", "", "", "viewer", "chat-1"))
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
	lost := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "must fail closed", "WEB", "", "", "viewer", "chat-1"))
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
	duplicate := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "message.received", "user", "mio", "accepted but duplicate id", "CHAT", "", "", "viewer", "chat-1"))
	duplicate.EventID = first.EventID
	if err := relay.OnEvent(duplicate); err != nil {
		t.Fatalf("OnEvent duplicate accepted head: %v", err)
	}
	releaseQueue()
	if err := admission.Barrier(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate event_id") {
		t.Fatalf("fatal accepted-head barrier=%v, want duplicate event ID failure", err)
	}
	following := publicRelayFixtureEvent(orchestrator.NewEventWithTraceID(traceID, "routing.decision", "mio", "shiro", "must not publish", "CHAT", "", "", "viewer", "chat-1"))
	if err := relay.OnEvent(following); err == nil || !strings.Contains(err.Error(), "duplicate event_id") {
		t.Fatalf("OnEvent after fatal accepted queue head=%v, want fatal duplicate error", err)
	}
	if got := len(hub.History()); got != 1 || hub.History()[0].EventID != first.EventID {
		t.Fatalf("Hub history after fatal head=%+v, want only first accepted event", hub.History())
	}
	releaseChat()
	_ = admission.Release()
}
