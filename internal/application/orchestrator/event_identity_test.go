package orchestrator

import (
	"encoding/json"
	"strings"
	"testing"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestOrchestratorEventUsesCanonicalEventAndTaskIdentities(t *testing.T) {
	traceID := modulecore.NewTraceID()
	sessionID := modulecore.NewSessionID()
	taskID := modulecore.NewTaskID()
	messageID := modulecore.NewMessageID()
	turnID := modulecore.NewTurnID()
	event := NewEventWithTraceID(traceID, "routing.decision", "mio", "shiro", "route=CODE", "CODE", taskID.String(), string(sessionID), "viewer", "chat")
	event.MessageID = messageID
	event.TurnID = turnID

	if err := event.EventID.Validate(); err != nil {
		t.Fatalf("event_id = %q: %v", event.EventID, err)
	}
	if event.TaskID != taskID {
		t.Fatalf("task_id = %q, want %q", event.TaskID, taskID)
	}
	if event.TraceID != traceID {
		t.Fatalf("trace_id = %q, want %q", event.TraceID, traceID)
	}
	if event.SessionID != sessionID {
		t.Fatalf("session_id = %q, want %q", event.SessionID, sessionID)
	}
	if err := event.TraceID.Validate(); err != nil {
		t.Fatalf("trace_id is not canonical: %v", err)
	}
	if err := event.SessionID.Validate(); err != nil {
		t.Fatalf("session_id is not canonical: %v", err)
	}
	if event.MessageID != messageID || event.TurnID != turnID {
		t.Fatalf("conversation identities = message=%q turn=%q, want message=%q turn=%q", event.MessageID, event.TurnID, messageID, turnID)
	}
	if event.EventSeq != 0 {
		t.Fatalf("new event_seq = %d, want canonical store assignment", event.EventSeq)
	}

	encoded, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{`"event_id"`, `"task_id"`, `"trace_id"`, `"session_id"`} {
		if !strings.Contains(string(encoded), required) {
			t.Fatalf("canonical event JSON is missing %s: %s", required, encoded)
		}
	}
}

func TestOrchestratorEventReferencesRoutingCause(t *testing.T) {
	traceID := modulecore.NewTraceID()
	sessionID := modulecore.NewSessionID()
	taskID := modulecore.NewTaskID()
	routingEvent := NewEventWithTraceID(traceID, "routing.decision", "mio", "shiro", "route=RESEARCH", "RESEARCH", taskID.String(), string(sessionID), "viewer", "chat")
	assignmentEvent := NewEventWithTraceID(traceID, "agent.assignment", "mio", "shiro", "assigned", "RESEARCH", taskID.String(), string(sessionID), "viewer", "chat")
	assignmentEvent.CausationEventID = routingEvent.EventID
	assignmentEvent.DependencyEventIDs = []modulecore.EventID{routingEvent.EventID}

	if assignmentEvent.CausationEventID != routingEvent.EventID || len(assignmentEvent.DependencyEventIDs) != 1 {
		t.Fatalf("assignment event does not reference routing event: %#v", assignmentEvent)
	}
}
