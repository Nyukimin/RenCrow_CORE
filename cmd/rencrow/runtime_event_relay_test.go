package main

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type shutdownOrderEventStore struct {
	mu                    sync.Mutex
	closed                bool
	appends               int
	appendErr             error
	hub                   *viewer.EventHub
	projectedBeforeAppend bool
}

var errRecordTest = errors.New("record failed")

func (s *shutdownOrderEventStore) Append(_ context.Context, _ modulecore.EventEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hub != nil && len(s.hub.History()) != 0 {
		s.projectedBeforeAppend = true
	}
	if s.appendErr != nil {
		return s.appendErr
	}
	if s.closed {
		return errRecordTest
	}
	s.appends++
	return nil
}

func TestEventRelayPersistsBeforeProjection(t *testing.T) {
	hub := viewer.NewEventHub(4)
	store := &shutdownOrderEventStore{hub: hub}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	relay := &idleAwareEventListener{hub: hub, archive: archive}

	if err := relay.OnEvent(orchestrator.NewEvent("message.received", "user", "mio", "hello", "CHAT", "job-1", "session-1", "viewer", "chat-1")); err != nil {
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
}

func TestEventRelayAppendFailureReturnsErrorAndDoesNotProject(t *testing.T) {
	hub := viewer.NewEventHub(4)
	store := &shutdownOrderEventStore{appendErr: errRecordTest, hub: hub}
	archive, err := viewer.NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	relay := &idleAwareEventListener{hub: hub, archive: archive}

	err = relay.OnEvent(orchestrator.NewEvent("message.received", "user", "mio", "hello", "CHAT", "job-1", "session-1", "viewer", "chat-1"))
	if !errors.Is(err, errRecordTest) {
		t.Fatalf("OnEvent() error = %v, want %v", err, errRecordTest)
	}
	if got := len(hub.History()); got != 0 {
		t.Fatalf("failed canonical event was projected: %d", got)
	}
}

func TestEventRelayRequiresCanonicalArchive(t *testing.T) {
	hub := viewer.NewEventHub(4)
	relay := &idleAwareEventListener{hub: hub}

	err := relay.OnEvent(orchestrator.NewEvent("message.received", "user", "mio", "hello", "CHAT", "job-1", "session-1", "viewer", "chat-1"))
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
