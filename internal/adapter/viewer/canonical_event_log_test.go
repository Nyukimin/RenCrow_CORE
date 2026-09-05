package viewer

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type canonicalEventLogStoreStub struct {
	events   []modulecore.EventEnvelope
	gotLimit int
}

func (s *canonicalEventLogStoreStub) Append(_ context.Context, event modulecore.EventEnvelope) error {
	_, err := s.AppendSequenced(context.Background(), event)
	return err
}

func (s *canonicalEventLogStoreStub) AppendSequenced(_ context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	event.EventSeq = modulecore.EventSeq(len(s.events) + 1)
	s.events = append(s.events, event)
	return event, nil
}

func (s *canonicalEventLogStoreStub) GetByID(_ context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	for _, event := range s.events {
		if event.EventID == eventID {
			return event, true, nil
		}
	}
	return modulecore.EventEnvelope{}, false, nil
}

func (s *canonicalEventLogStoreStub) ListByComponent(_ context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error) {
	s.gotLimit = limit
	items := make([]modulecore.EventEnvelope, 0, len(s.events))
	for _, event := range s.events {
		if event.ComponentID == componentID {
			items = append(items, event)
		}
	}
	return items, nil
}

func TestNewCanonicalEventLogRequiresEventStore(t *testing.T) {
	if _, err := NewCanonicalEventLog(nil); err == nil {
		t.Fatal("NewCanonicalEventLog(nil) error = nil, want error")
	}
}

func TestCanonicalEventLogAppendUsesCanonicalEnvelopeAndPreservesPayload(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	messageID := modulecore.NewMessageID()
	sessionID := modulecore.NewSessionID()
	threadID := modulecore.NewThreadID()
	turnID := modulecore.NewTurnID()
	traceID := modulecore.NewTraceID()
	taskID := modulecore.NewTaskID()
	event := orchestrator.OrchestratorEvent{
		EventID:    modulecore.NewEventID(),
		Type:       "agent.response",
		From:       "mio",
		To:         "user",
		Content:    "hello",
		RawContent: "raw hello",
		MessageID:  messageID,
		TurnIndex:  3,
		TurnID:     turnID,
		Category:   "topic",
		Strategy:   "direct",
		Route:      "CHAT",
		TaskID:     taskID,
		TraceID:    traceID,
		SessionID:  sessionID,
		ThreadID:   threadID,
		Channel:    "web",
		ChatID:     "chat-1",
		Timestamp:  "2026-08-29T12:34:56.789+09:00",
	}

	persisted, err := log.AppendSequenced(event)
	if err != nil {
		t.Fatalf("AppendSequenced() error = %v", err)
	}
	if persisted.EventID != event.EventID || persisted.EventSeq != 1 {
		t.Fatalf("persisted identity = event_id=%q event_seq=%d, want event_id=%q event_seq=1", persisted.EventID, persisted.EventSeq, event.EventID)
	}
	if len(store.events) != 1 {
		t.Fatalf("stored event count = %d, want 1", len(store.events))
	}
	canonical := store.events[0]
	if canonical.ComponentID != "orchestrator" {
		t.Fatalf("component_id = %q, want orchestrator", canonical.ComponentID)
	}
	if canonical.EventType != event.Type {
		t.Fatalf("event_type = %q, want %q", canonical.EventType, event.Type)
	}
	if canonical.EventID != event.EventID {
		t.Fatalf("event_id = %q, want %q", canonical.EventID, event.EventID)
	}
	if err := canonical.TraceID.Validate(); err != nil {
		t.Fatalf("canonical trace_id is invalid: %v", err)
	}
	if canonical.TraceID != traceID {
		t.Fatalf("trace_id = %q, want %q", canonical.TraceID, traceID)
	}
	if canonical.MessageID != messageID {
		t.Fatalf("message_id = %q, want %q", canonical.MessageID, messageID)
	}
	if canonical.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", canonical.SessionID, sessionID)
	}
	if canonical.TurnID != turnID || canonical.ThreadID != threadID || canonical.TaskID != taskID {
		t.Fatalf("authoritative identities = turn=%q thread=%q task=%q, want turn=%q thread=%q task=%q", canonical.TurnID, canonical.ThreadID, canonical.TaskID, turnID, threadID, taskID)
	}
	wantOccurredAt, _ := time.Parse(time.RFC3339Nano, event.Timestamp)
	if !canonical.OccurredAt.Equal(wantOccurredAt.UTC()) {
		t.Fatalf("occurred_at = %s, want %s", canonical.OccurredAt, wantOccurredAt.UTC())
	}

	projected, err := log.Query(context.Background(), LogFilter{Limit: 1})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	wantProjected := event
	wantProjected.EventSeq = 1
	if len(projected) != 1 || !reflect.DeepEqual(projected[0], wantProjected) {
		t.Fatalf("projected event = %#v, want %#v", projected, []orchestrator.OrchestratorEvent{wantProjected})
	}
	if store.gotLimit != canonicalEventLogReadLimit {
		t.Fatalf("ListByComponent limit = %d, want %d", store.gotLimit, canonicalEventLogReadLimit)
	}
	lookedUp, found, err := log.GetByEventID(context.Background(), event.EventID)
	if err != nil || !found {
		t.Fatalf("GetByEventID() found=%v error=%v, want found", found, err)
	}
	if !reflect.DeepEqual(lookedUp, wantProjected) {
		t.Fatalf("GetByEventID() = %#v, want %#v", lookedUp, wantProjected)
	}
	byID, err := log.Query(context.Background(), LogFilter{EventID: event.EventID})
	if err != nil || len(byID) != 1 || byID[0].EventID != event.EventID {
		t.Fatalf("EventID filter = %#v error=%v, want the incoming event", byID, err)
	}
}

func TestCanonicalEventLogQueryFiltersNewestFirstAndHonorsLimit(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	older := orchestrator.OrchestratorEvent{
		EventID:   modulecore.NewEventID(),
		Type:      "agent.start",
		From:      "coder1",
		Content:   "older",
		TaskID:    modulecore.NewTaskID(),
		Route:     "CODE",
		Timestamp: "2026-08-29T10:00:00Z",
	}
	newer := older
	newer.EventID = modulecore.NewEventID()
	newer.Content = "newer"
	newer.TaskID = modulecore.NewTaskID()
	newer.Timestamp = "2026-08-29T11:00:00Z"
	if err := log.Append(older); err != nil {
		t.Fatalf("Append(older) error = %v", err)
	}
	if err := log.Append(newer); err != nil {
		t.Fatalf("Append(newer) error = %v", err)
	}

	items, err := log.Query(context.Background(), LogFilter{Agent: "coder1", Route: "code", Limit: 1})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(items) != 1 || items[0].Content != newer.Content {
		t.Fatalf("filtered items = %#v, want newest item only", items)
	}

	items, err = log.Query(context.Background(), LogFilter{Limit: 10})
	if err != nil {
		t.Fatalf("Query(unfiltered) error = %v", err)
	}
	if len(items) != 2 || items[0].Content != newer.Content || items[1].Content != older.Content {
		t.Fatalf("unfiltered items = %#v, want newest first", items)
	}
}

func TestCanonicalEventLogRejectsMalformedTypedIDs(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	base := orchestrator.OrchestratorEvent{
		EventID:   modulecore.NewEventID(),
		Type:      "message.received",
		From:      "user",
		Content:   "malformed typed identity is rejected",
		Timestamp: "2026-08-29T12:00:00Z",
	}
	cases := []struct {
		name   string
		mutate func(*orchestrator.OrchestratorEvent)
	}{
		{
			name: "malformed message identity",
			mutate: func(event *orchestrator.OrchestratorEvent) {
				event.MessageID = modulecore.MessageID("msg-not-a-uuid")
			},
		},
		{
			name: "cross typed session identity",
			mutate: func(event *orchestrator.OrchestratorEvent) {
				event.SessionID = modulecore.SessionID(modulecore.NewTaskID())
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			event := base
			event.EventID = modulecore.NewEventID()
			tc.mutate(&event)
			if err := log.Append(event); err == nil {
				t.Fatal("Append() error = nil, want typed identity rejection")
			}
			if len(store.events) != 0 {
				t.Fatalf("malformed event was persisted: %#v", store.events)
			}
		})
	}
}

func TestCanonicalEventLogUsesValidTraceID(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	traceID := modulecore.NewTraceID()
	event := orchestrator.OrchestratorEvent{
		EventID:   modulecore.NewEventID(),
		Type:      "routing.decision",
		From:      "mio",
		Content:   "valid trace correlation",
		TraceID:   traceID,
		Timestamp: "2026-08-29T12:00:00Z",
	}
	if err := log.Append(event); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	if got := store.events[0].TraceID; got != traceID {
		t.Fatalf("trace_id = %q, want %q", got, traceID)
	}
}

func TestCanonicalEventLogQuerySkipsNonOrchestratorEventsFromStore(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	store.events = append(store.events, modulecore.NewRootEventEnvelope("other", "other.event", time.Now().UTC(), map[string]any{
		"type":      "other.event",
		"timestamp": "2026-08-29T12:00:00Z",
	}))
	items, err := log.Query(context.Background(), LogFilter{})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(items) != 0 {
		t.Fatalf("Query() = %#v, want no non-orchestrator projections", items)
	}
}
