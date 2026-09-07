package main

import (
	"context"
	"encoding/json"
	"errors"
	backlogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/backlog"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
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
	hub                   *viewer.EventHub
	projectedBeforeAppend bool
}

var errRecordTest = errors.New("record failed")

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
	s.mu.Unlock()
	if hook != nil {
		hook(call)
	}
	event.EventSeq = modulecore.EventSeq(sequence)
	return event, nil
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
			sink := developmentEventLogSink{store: archive}
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
