package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type shutdownOrderEventStore struct {
	mu                    sync.Mutex
	closed                bool
	appends               int
	appendCalls           int
	appendErr             error
	appendErrOnce         bool
	appendHook            func(int)
	replayErr             error
	events                []modulecore.EventEnvelope
	hub                   *viewer.EventHub
	projectedBeforeAppend bool
}

var errRecordTest = errors.New("record failed")

type responsePublicationMarkerStub struct {
	store       *shutdownOrderEventStore
	calls       int
	turnID      modulecore.TurnID
	traceID     modulecore.TraceID
	sessionID   modulecore.SessionID
	afterAppend bool
	delay       time.Duration
	err         error
}

func (m *responsePublicationMarkerStub) MarkConversationTurnResponsePublished(ctx context.Context, turnID modulecore.TurnID, traceID modulecore.TraceID, sessionID modulecore.SessionID, _ time.Time) error {
	m.calls++
	m.turnID = turnID
	m.traceID = traceID
	m.sessionID = sessionID
	m.store.mu.Lock()
	m.afterAppend = m.store.appends > 0
	m.store.mu.Unlock()
	if m.delay > 0 {
		timer := time.NewTimer(m.delay)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return m.err
}

func (s *shutdownOrderEventStore) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	_, err := s.AppendSequenced(ctx, event)
	return err
}

func (s *shutdownOrderEventStore) AppendSequenced(_ context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	s.mu.Lock()
	if s.hub != nil {
		for _, projected := range s.hub.History() {
			if projected.EventID == event.EventID {
				s.projectedBeforeAppend = true
				break
			}
		}
	}
	s.appendCalls++
	call := s.appendCalls
	hook := s.appendHook
	if s.appendErr != nil {
		err := s.appendErr
		if s.appendErrOnce {
			s.appendErr = nil
		}
		s.mu.Unlock()
		if hook != nil {
			hook(call)
		}
		return modulecore.EventEnvelope{}, err
	}
	if s.closed {
		s.mu.Unlock()
		if hook != nil {
			hook(call)
		}
		return modulecore.EventEnvelope{}, errRecordTest
	}
	s.appends++
	sequence := s.appends
	event.EventSeq = modulecore.EventSeq(sequence)
	s.events = append(s.events, event)
	s.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	return event, nil
}

func (s *shutdownOrderEventStore) ReadComponentPage(ctx context.Context, component string, after, through modulecore.EventSeq, limit int) ([]modulecore.EventEnvelope, modulecore.EventSeq, error) {
	if ctx == nil {
		return nil, 0, errors.New("event replay context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, 0, err
	}
	if after < 0 || through < 0 || limit <= 0 {
		return nil, 0, errors.New("invalid event replay query")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replayErr != nil {
		return nil, 0, s.replayErr
	}
	var latest modulecore.EventSeq
	for _, event := range s.events {
		if event.ComponentID == component && event.EventSeq > latest {
			latest = event.EventSeq
		}
	}
	if through == 0 {
		through = latest
	}
	if after > through || through > latest {
		return nil, 0, errors.New("event replay cursor is outside the canonical window")
	}
	page := make([]modulecore.EventEnvelope, 0, limit)
	for _, event := range s.events {
		if event.ComponentID == component && event.EventSeq > after && event.EventSeq <= through {
			page = append(page, event)
			if len(page) == limit {
				break
			}
		}
	}
	return page, through, nil
}

func TestEventRelayRestoresCanonicalProjectionBeforeLiveSubscription(t *testing.T) {
	const historicalEventCount = canonicalEventRelayPageLimit + 19
	const transientEventIndex = 510
	const privateSequenceGapAfter = 700

	hub := viewer.NewEventHub(200)
	store := &shutdownOrderEventStore{hub: hub}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	monitor := viewer.NewMonitorStore(nil, archive)
	marker := &responsePublicationMarkerStub{store: store}
	relay := &idleAwareEventListener{hub: hub, monitor: monitor, archive: archive, responseMarker: marker}
	taskID := modulecore.NewTaskID().String()
	sessionID := modulecore.NewSessionID()
	traceID := modulecore.NewTraceID()
	turnID := modulecore.NewTurnID()

	var watermark modulecore.EventSeq
	for index := 0; index < historicalEventCount; index++ {
		eventType := "agent.note"
		from, to := "mio", "user"
		content := fmt.Sprintf("historical-%04d", index)
		if index == transientEventIndex {
			eventType = "tts.audio_chunk"
			from = "tts"
			content = "historical transient"
		}
		event := orchestrator.NewEvent(eventType, from, to, content, "CHAT", taskID, string(sessionID), "viewer", "chat-1")
		if index == historicalEventCount-1 {
			event = orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "historical final response", "CHAT", taskID, string(sessionID), "viewer", "chat-1")
			event.TurnID = turnID
		}
		persisted, appendErr := archive.AppendSequenced(event)
		if appendErr != nil {
			t.Fatalf("AppendSequenced historical event %d: %v", index, appendErr)
		}
		watermark = persisted.EventSeq
	}
	addPrivateEventSequenceGap(t, store, privateSequenceGapAfter)
	watermark++

	if err := relay.RestoreCanonicalProjection(context.Background()); err != nil {
		t.Fatalf("RestoreCanonicalProjection() error = %v", err)
	}
	if relay.publicationCursor != watermark {
		t.Fatalf("publication cursor=%d, want canonical watermark %d", relay.publicationCursor, watermark)
	}
	assertRestoredEventProjection(t, hub, monitor, watermark, historicalEventCount-1)
	if marker.calls != 0 {
		t.Fatalf("historical response marker calls=%d, want no restore side effects", marker.calls)
	}

	if err := relay.RestoreCanonicalProjection(context.Background()); err != nil {
		t.Fatalf("second RestoreCanonicalProjection() error = %v", err)
	}
	if relay.publicationCursor != watermark {
		t.Fatalf("cursor after idempotent restore=%d, want %d", relay.publicationCursor, watermark)
	}
	assertRestoredEventProjection(t, hub, monitor, watermark, historicalEventCount-1)
	if marker.calls != 0 {
		t.Fatalf("historical response marker calls after idempotent restore=%d, want 0", marker.calls)
	}

	client, snapshot := hub.Subscribe()
	defer hub.Unsubscribe(client)
	if len(snapshot) != 200 {
		t.Fatalf("subscription snapshot events=%d, want last 200 restored events", len(snapshot))
	}
	select {
	case event := <-client:
		t.Fatalf("restoration queued historical live event for new subscriber: %s", event)
	default:
	}

	newFinal := orchestrator.NewEventWithTraceID(modulecore.NewTraceID(), "agent.response", "mio", "user", "new live response", "CHAT", taskID, string(sessionID), "viewer", "chat-1")
	newFinal.TurnID = modulecore.NewTurnID()
	if err := relay.OnEvent(newFinal); err != nil {
		t.Fatalf("OnEvent() after restore: %v", err)
	}
	select {
	case data := <-client:
		var published orchestrator.OrchestratorEvent
		if err := json.Unmarshal(data, &published); err != nil {
			t.Fatalf("decode new live event: %v", err)
		}
		if published.EventID != newFinal.EventID || published.EventSeq != watermark+1 || published.Content != "new live response" {
			t.Fatalf("live event=%+v, want new final response at seq %d", published, watermark+1)
		}
	default:
		t.Fatal("new live event was not delivered after restore")
	}
	select {
	case data := <-client:
		t.Fatalf("unexpected extra live event after new response: %s", data)
	default:
	}
	if marker.calls != 1 || !marker.afterAppend {
		t.Fatalf("new response marker=%+v, want one post-append call", marker)
	}
}

func TestEventRelayRestoreCanonicalProjectionReturnsFailure(t *testing.T) {
	hub := viewer.NewEventHub(200)
	store := &shutdownOrderEventStore{replayErr: errRecordTest}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	monitor := viewer.NewMonitorStore(nil, archive)
	relay := &idleAwareEventListener{hub: hub, monitor: monitor, archive: archive}

	if err := relay.RestoreCanonicalProjection(context.Background()); !errors.Is(err, errRecordTest) {
		t.Fatalf("RestoreCanonicalProjection() error = %v, want replay failure %v", err, errRecordTest)
	}
	if relay.publicationCursor != 0 {
		t.Fatalf("publication cursor after failed restore=%d, want 0", relay.publicationCursor)
	}
	if got := len(hub.History()); got != 0 {
		t.Fatalf("hub history after failed restore=%d, want 0", got)
	}
	if got := len(monitor.Logs(viewer.LogFilter{Limit: 10})); got != 0 {
		t.Fatalf("monitor logs after failed restore=%d, want 0", got)
	}
	store.replayErr = nil
	canceledContext, cancel := context.WithCancel(context.Background())
	cancel()
	if err := relay.RestoreCanonicalProjection(canceledContext); !errors.Is(err, context.Canceled) {
		t.Fatalf("RestoreCanonicalProjection() with canceled context error = %v, want %v", err, context.Canceled)
	}
	if relay.publicationCursor != 0 || len(hub.History()) != 0 || len(monitor.Logs(viewer.LogFilter{Limit: 10})) != 0 {
		t.Fatalf("canceled restore claimed a projection: cursor=%d hub=%d monitor=%d", relay.publicationCursor, len(hub.History()), len(monitor.Logs(viewer.LogFilter{Limit: 10})))
	}
}

func addPrivateEventSequenceGap(t *testing.T, store *shutdownOrderEventStore, after modulecore.EventSeq) {
	t.Helper()
	store.mu.Lock()
	defer store.mu.Unlock()
	index := sort.Search(len(store.events), func(index int) bool {
		return store.events[index].EventSeq > after
	})
	for current := index; current < len(store.events); current++ {
		store.events[current].EventSeq++
	}
	private := modulecore.NewRootEventEnvelope("private", "private.event", time.Now().UTC(), map[string]any{"content": "not an orchestrator event"})
	private.EventSeq = after + 1
	store.events = append(store.events, modulecore.EventEnvelope{})
	copy(store.events[index+1:], store.events[index:])
	store.events[index] = private
	store.appends++
	store.appendCalls++
}

func assertRestoredEventProjection(t *testing.T, hub *viewer.EventHub, monitor *viewer.MonitorStore, watermark modulecore.EventSeq, wantMonitorEvents int) {
	t.Helper()
	history := hub.History()
	if len(history) != 200 {
		t.Fatalf("hub history events=%d, want last 200", len(history))
	}
	if history[0].EventSeq != watermark-199 || history[len(history)-1].EventSeq != watermark {
		t.Fatalf("hub history sequence window=%d..%d, want %d..%d", history[0].EventSeq, history[len(history)-1].EventSeq, watermark-199, watermark)
	}
	logs := monitor.Logs(viewer.LogFilter{Limit: wantMonitorEvents + 1})
	if len(logs) != wantMonitorEvents {
		t.Fatalf("monitor log events=%d, want %d projected historical events", len(logs), wantMonitorEvents)
	}
	for _, event := range logs {
		if event.Type == "tts.audio_chunk" {
			t.Fatal("transient historical event was restored to MonitorStore")
		}
	}
	var mio *viewer.AgentSnapshot
	for _, agent := range monitor.Agents() {
		if agent.ID == "mio" {
			mio = &agent
			break
		}
	}
	if mio == nil || mio.State != "idle" || mio.LastEvent != "agent.response" || mio.EventSeq != watermark {
		t.Fatalf("restored Mio monitor state=%+v, want idle final response at seq %d", mio, watermark)
	}
}

func TestEventRelayPersistsBeforeProjection(t *testing.T) {
	hub := viewer.NewEventHub(4)
	store := &shutdownOrderEventStore{hub: hub}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	relay := &idleAwareEventListener{hub: hub, archive: archive}

	taskID := modulecore.NewTaskID().String()
	sessionID := string(modulecore.NewSessionID())
	if err := relay.OnEvent(orchestrator.NewEvent("message.received", "user", "mio", "hello", "CHAT", taskID, sessionID, "viewer", "chat-1")); err != nil {
		t.Fatalf("OnEvent() error = %v", err)
	}

	store.mu.Lock()
	appends := store.appends
	projectedBeforeAppend := store.projectedBeforeAppend
	store.mu.Unlock()
	if appends != 1 || projectedBeforeAppend {
		t.Fatalf("appends=%d projected_before_append=%t", appends, projectedBeforeAppend)
	}
	if got := len(hub.History()); got != 1 {
		t.Fatalf("projected events=%d, want 1", got)
	}
	if got := hub.History()[0].EventSeq; got != 1 {
		t.Fatalf("projected event_seq=%d, want persisted 1", got)
	}
}

func TestEventRelayMarksOnlyDurablyAppendedPublicFinalResponse(t *testing.T) {
	traceID := modulecore.NewTraceID()
	turnID := modulecore.NewTurnID()
	sessionID := modulecore.NewSessionID()
	messageID := modulecore.NewMessageID()
	newFinal := func() orchestrator.OrchestratorEvent {
		event := orchestrator.NewEventWithTraceID(traceID, "agent.response", "mio", "user", "the complete reply", "CHAT", modulecore.NewTaskID().String(), string(sessionID), "viewer", "chat-1")
		event.TurnID = turnID
		event.MessageID = messageID
		return event
	}

	t.Run("success", func(t *testing.T) {
		store := &shutdownOrderEventStore{}
		archive, err := viewer.NewCanonicalEventLog(store)
		if err != nil {
			t.Fatalf("NewCanonicalEventLog: %v", err)
		}
		marker := &responsePublicationMarkerStub{store: store}
		relay := &idleAwareEventListener{archive: archive, responseMarker: marker}
		if err := relay.OnEvent(newFinal()); err != nil {
			t.Fatalf("OnEvent: %v", err)
		}
		if marker.calls != 1 || !marker.afterAppend || marker.turnID != turnID || marker.traceID != traceID || marker.sessionID != sessionID {
			t.Fatalf("marker=%+v, want exact response marker after append", marker)
		}
	})

	t.Run("append failure", func(t *testing.T) {
		store := &shutdownOrderEventStore{appendErr: errRecordTest}
		archive, err := viewer.NewCanonicalEventLog(store)
		if err != nil {
			t.Fatalf("NewCanonicalEventLog: %v", err)
		}
		marker := &responsePublicationMarkerStub{store: store}
		relay := &idleAwareEventListener{archive: archive, responseMarker: marker}
		if err := relay.OnEvent(newFinal()); !errors.Is(err, errRecordTest) {
			t.Fatalf("OnEvent error=%v, want canonical append failure", err)
		}
		if marker.calls != 0 {
			t.Fatalf("marker calls=%d, want none after failed append", marker.calls)
		}
	})

	t.Run("marker failure is bounded and does not undo public final", func(t *testing.T) {
		store := &shutdownOrderEventStore{}
		archive, err := viewer.NewCanonicalEventLog(store)
		if err != nil {
			t.Fatalf("NewCanonicalEventLog: %v", err)
		}
		marker := &responsePublicationMarkerStub{store: store, delay: time.Second}
		relay := &idleAwareEventListener{archive: archive, responseMarker: marker}
		started := time.Now()
		if err := relay.OnEvent(newFinal()); err != nil {
			t.Fatalf("OnEvent after durable final: %v", err)
		}
		if elapsed := time.Since(started); elapsed >= 500*time.Millisecond {
			t.Fatalf("best-effort publication marker took %s, want under 500ms", elapsed)
		}
		if marker.calls != 1 || !marker.afterAppend {
			t.Fatalf("marker=%+v, want one attempt after canonical append", marker)
		}
		store.mu.Lock()
		appends := store.appends
		store.mu.Unlock()
		if appends != 1 {
			t.Fatalf("canonical final appends=%d, want exactly one", appends)
		}
	})
}

func TestEventRelayAppendFailureReturnsErrorAndDoesNotProject(t *testing.T) {
	hub := viewer.NewEventHub(4)
	store := &shutdownOrderEventStore{appendErr: errRecordTest, hub: hub}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	relay := &idleAwareEventListener{hub: hub, archive: archive}

	err = relay.OnEvent(orchestrator.NewEvent("message.received", "user", "mio", "hello", "CHAT", modulecore.NewTaskID().String(), string(modulecore.NewSessionID()), "viewer", "chat-1"))
	if !errors.Is(err, errRecordTest) {
		t.Fatalf("OnEvent() error = %v, want %v", err, errRecordTest)
	}
	if got := len(hub.History()); got != 0 {
		t.Fatalf("failed canonical event was projected: %d", got)
	}
}

func TestEventRelaySerializesPublicationAndPreservesCanonicalOrder(t *testing.T) {
	tests := []struct {
		name         string
		appendErr    error
		wantFirstErr error
		wantHistory  []struct {
			content  string
			eventSeq modulecore.EventSeq
		}
		wantAppends int
	}{
		{
			name: "first append succeeds",
			wantHistory: []struct {
				content  string
				eventSeq modulecore.EventSeq
			}{{content: "first", eventSeq: 1}, {content: "second", eventSeq: 2}},
			wantAppends: 2,
		},
		{
			name:         "first append fails",
			appendErr:    errRecordTest,
			wantFirstErr: errRecordTest,
			wantHistory: []struct {
				content  string
				eventSeq modulecore.EventSeq
			}{{content: "second", eventSeq: 1}},
			wantAppends: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			hub := viewer.NewEventHub(4)
			firstAppend := make(chan struct{})
			releaseFirstAppend := make(chan struct{})
			var releaseOnce sync.Once
			releaseFirst := func() {
				releaseOnce.Do(func() { close(releaseFirstAppend) })
			}
			t.Cleanup(releaseFirst)
			secondAppend := make(chan struct{})
			store := &shutdownOrderEventStore{
				appendErr:     test.appendErr,
				appendErrOnce: test.appendErr != nil,
				hub:           hub,
				appendHook: func(call int) {
					switch call {
					case 1:
						close(firstAppend)
						<-releaseFirstAppend
					case 2:
						close(secondAppend)
					}
				},
			}
			archive, err := viewer.NewCanonicalEventLog(store)
			if err != nil {
				t.Fatalf("NewCanonicalEventLog() error = %v", err)
			}
			relay := &idleAwareEventListener{hub: hub, archive: archive}
			first := orchestrator.NewEvent("message.received", "user", "mio", "first", "CHAT", modulecore.NewTaskID().String(), string(modulecore.NewSessionID()), "viewer", "chat-1")
			second := orchestrator.NewEvent("routing.decision", "mio", "shiro", "second", "CHAT", modulecore.NewTaskID().String(), string(modulecore.NewSessionID()), "viewer", "chat-1")

			firstDone := make(chan error, 1)
			go func() {
				firstDone <- relay.OnEvent(first)
			}()
			waitEventRelaySignal(t, firstAppend, "first append")

			secondStarted := make(chan struct{})
			secondDone := make(chan error, 1)
			go func() {
				close(secondStarted)
				secondDone <- relay.OnEvent(second)
			}()
			waitEventRelaySignal(t, secondStarted, "second relay")

			blockedContext, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
			publicationBlocked := true
			select {
			case <-secondAppend:
				publicationBlocked = false
			case <-blockedContext.Done():
			}
			cancel()
			releaseFirst()

			firstErr := waitEventRelayResult(t, firstDone, "first relay")
			secondErr := waitEventRelayResult(t, secondDone, "second relay")
			if !publicationBlocked {
				t.Fatalf("second append started before first append completed")
			}
			if test.wantFirstErr == nil {
				if firstErr != nil {
					t.Fatalf("first OnEvent() error = %v, want nil", firstErr)
				}
			} else if !errors.Is(firstErr, test.wantFirstErr) {
				t.Fatalf("first OnEvent() error = %v, want %v", firstErr, test.wantFirstErr)
			}
			if secondErr != nil {
				t.Fatalf("second OnEvent() error = %v", secondErr)
			}

			history := hub.History()
			if len(history) != len(test.wantHistory) {
				t.Fatalf("published events=%d, want %d", len(history), len(test.wantHistory))
			}
			for index, want := range test.wantHistory {
				if history[index].Content != want.content || history[index].EventSeq != want.eventSeq {
					t.Fatalf("published event[%d]=%+v, want content=%q event_seq=%d", index, history[index], want.content, want.eventSeq)
				}
			}
			store.mu.Lock()
			appendCalls := store.appendCalls
			appends := store.appends
			projectedBeforeAppend := store.projectedBeforeAppend
			store.mu.Unlock()
			if appendCalls != 2 || appends != test.wantAppends || projectedBeforeAppend {
				t.Fatalf("append_calls=%d appends=%d projected_before_append=%t", appendCalls, appends, projectedBeforeAppend)
			}
		})
	}
}

func waitEventRelaySignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitEventRelayResult(t *testing.T, result <-chan error, name string) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s result", name)
		return ctx.Err()
	}
}

func TestEventRelayRequiresCanonicalArchive(t *testing.T) {
	hub := viewer.NewEventHub(4)
	relay := &idleAwareEventListener{hub: hub}

	err := relay.OnEvent(orchestrator.NewEvent("message.received", "user", "mio", "hello", "CHAT", modulecore.NewTaskID().String(), string(modulecore.NewSessionID()), "viewer", "chat-1"))
	if !errors.Is(err, errCanonicalEventArchiveRequired) {
		t.Fatalf("OnEvent() error = %v, want %v", err, errCanonicalEventArchiveRequired)
	}
	if got := len(hub.History()); got != 0 {
		t.Fatalf("event was projected without canonical archive: %d", got)
	}
}

func (s *shutdownOrderEventStore) GetByID(context.Context, modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	return modulecore.EventEnvelope{}, false, nil
}

func (s *shutdownOrderEventStore) ListByComponent(context.Context, string, int) ([]modulecore.EventEnvelope, error) {
	return nil, nil
}

func (s *shutdownOrderEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

type developmentCapturingStore struct {
	shutdownOrderEventStore
	captured []modulecore.EventEnvelope
}

func (s *developmentCapturingStore) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	persisted, err := s.shutdownOrderEventStore.AppendSequenced(ctx, event)
	if err == nil {
		s.captured = append(s.captured, persisted)
	}
	return persisted, err
}
func TestDevelopmentEventSinkCanonicalIdentity(t *testing.T) {
	for _, mode := range []string{"standalone", "root", "execution", "conflicting-trace", "invalid-trace", "missing-actor", "store-failure", "missing-store"} {
		t.Run(mode, func(t *testing.T) {
			store := &developmentCapturingStore{}
			archive, err := viewer.NewCanonicalEventLog(store)
			if err != nil {
				t.Fatal(err)
			}
			sink := developmentEventLogSink{store: archive, canonicalActorID: atlasExecutionActorID}
			trace := modulecore.NewTraceID()
			task := modulecore.NewTaskID()
			run := modulecore.NewRunID()
			ctx := context.Background()
			event := backlogapp.DevelopmentEvent{Type: "development.transition", UnitID: "unit", ArtifactID: "transition:unit:DONE:caller", RequestID: "caller-correlation", TraceID: string(trace), CreatedAt: time.Now()}
			if mode == "execution" || mode == "conflicting-trace" || mode == "missing-actor" {
				ctx, err = execution.WithIdentity(ctx, task, run, trace)
				if err != nil {
					t.Fatal(err)
				}
				if mode != "missing-actor" {
					ctx, err = withTrustedAgentPublicToolScope(ctx, "caller", "shiro")
					if err != nil {
						t.Fatal(err)
					}
				}
				if mode == "conflicting-trace" {
					event.TraceID = string(modulecore.NewTraceID())
				}
			}
			if mode == "root" || mode == "execution" {
				event.TraceID = ""
			}
			if mode == "invalid-trace" {
				event.TraceID = "not-a-trace"
			}
			if mode == "store-failure" {
				store.appendErr = errRecordTest
			}
			if mode == "missing-store" {
				sink.store = nil
			}
			err = sink.AppendDevelopmentEvent(ctx, event)
			wantFailure := mode != "standalone" && mode != "execution" && mode != "root"
			if wantFailure {
				if err == nil || len(store.captured) != 0 {
					t.Fatalf("expected rejection before persistence: err=%v captured=%+v", err, store.captured)
				}
				if mode == "store-failure" && !errors.Is(err, errRecordTest) {
					t.Fatalf("lost store error: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(store.captured) != 1 {
				t.Fatal("event not persisted")
			}
			got := store.captured[0]
			if (mode != "root" && got.TraceID != trace) || got.TraceID.Validate() != nil || got.MessageID != "" {
				t.Fatalf("invalid correlation: %+v", got)
			}
			if mode == "execution" && (got.TaskID != task || got.RunID != run || got.ActorID != "shiro" || got.ActorKind != "agent") {
				t.Fatalf("execution identity lost: %+v", got)
			}
			var payload map[string]any
			content, ok := got.Payload["content"].(string)
			if !ok {
				t.Fatalf("missing event content: %+v", got.Payload)
			}
			if err := json.Unmarshal([]byte(content), &payload); err != nil {
				t.Fatal(err)
			}
			if payload["request_id"] != event.RequestID {
				t.Fatal("caller correlation lost")
			}
			if payload["artifact_id"] != event.ArtifactID {
				t.Fatal("diagnostic reference lost")
			}
		})
	}
}
