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
	s.events = append(s.events, event)
	return nil
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
	event := orchestrator.OrchestratorEvent{
		Seq:        7,
		Type:       "agent.response",
		From:       "mio",
		To:         "user",
		Content:    "hello",
		RawContent: "raw hello",
		MessageID:  string(messageID),
		TurnIndex:  3,
		Category:   "topic",
		Strategy:   "direct",
		Route:      "CHAT",
		JobID:      "job-1",
		TraceID:    "legacy-trace-id",
		SessionID:  string(sessionID),
		Channel:    "web",
		ChatID:     "chat-1",
		Timestamp:  "2026-08-29T12:34:56.789+09:00",
	}

	if err := log.Append(event); err != nil {
		t.Fatalf("Append() error = %v", err)
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
	if err := canonical.EventID.Validate(); err != nil {
		t.Fatalf("generated event_id is invalid: %v", err)
	}
	if err := canonical.TraceID.Validate(); err != nil {
		t.Fatalf("generated trace_id is invalid: %v", err)
	}
	if canonical.TraceID == modulecore.TraceID(event.TraceID) {
		t.Fatal("payload trace_id was promoted to canonical trace_id")
	}
	if canonical.MessageID != messageID {
		t.Fatalf("message_id = %q, want %q", canonical.MessageID, messageID)
	}
	if canonical.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", canonical.SessionID, sessionID)
	}
	wantOccurredAt, _ := time.Parse(time.RFC3339Nano, event.Timestamp)
	if !canonical.OccurredAt.Equal(wantOccurredAt.UTC()) {
		t.Fatalf("occurred_at = %s, want %s", canonical.OccurredAt, wantOccurredAt.UTC())
	}

	projected, err := log.Query(context.Background(), LogFilter{Limit: 1})
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if len(projected) != 1 || !reflect.DeepEqual(projected[0], event) {
		t.Fatalf("projected event = %#v, want %#v", projected, []orchestrator.OrchestratorEvent{event})
	}
	if store.gotLimit != canonicalEventLogReadLimit {
		t.Fatalf("ListByComponent limit = %d, want %d", store.gotLimit, canonicalEventLogReadLimit)
	}
}

func TestCanonicalEventLogQueryFiltersNewestFirstAndHonorsLimit(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	older := orchestrator.OrchestratorEvent{
		Type:      "agent.start",
		From:      "coder1",
		Content:   "older",
		JobID:     "job-1",
		Route:     "CODE",
		Timestamp: "2026-08-29T10:00:00Z",
	}
	newer := older
	newer.Content = "newer"
	newer.JobID = "job-2"
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

func TestCanonicalEventLogDoesNotPromoteInvalidPayloadIDs(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	event := orchestrator.OrchestratorEvent{
		Type:      "message.received",
		From:      "user",
		Content:   "untrusted ids stay in payload",
		MessageID: "legacy-message-id",
		SessionID: "legacy-session-id",
		TraceID:   "legacy-trace-id",
		Timestamp: "2026-08-29T12:00:00Z",
	}
	if err := log.Append(event); err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	canonical := store.events[0]
	if canonical.MessageID != "" || canonical.SessionID != "" {
		t.Fatalf("invalid typed IDs were promoted: message=%q session=%q", canonical.MessageID, canonical.SessionID)
	}
	if canonical.Payload["message_id"] != event.MessageID || canonical.Payload["session_id"] != event.SessionID || canonical.Payload["trace_id"] != event.TraceID {
		t.Fatalf("raw IDs were not retained in payload: %#v", canonical.Payload)
	}
	if canonical.TraceID == modulecore.TraceID(event.TraceID) {
		t.Fatal("invalid payload trace_id was promoted to canonical trace_id")
	}
}

func TestCanonicalEventLogPromotesValidTraceIDOnly(t *testing.T) {
	store := &canonicalEventLogStoreStub{}
	log, err := NewCanonicalEventLog(store)
	if err != nil {
		t.Fatalf("NewCanonicalEventLog() error = %v", err)
	}
	traceID := modulecore.NewTraceID()
	event := orchestrator.OrchestratorEvent{
		Type:      "routing.decision",
		From:      "mio",
		Content:   "valid trace correlation",
		TraceID:   string(traceID),
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
