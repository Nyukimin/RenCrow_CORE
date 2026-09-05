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

func TestEventExecutionIdentityBindingIsExactIdempotentAndConflictSafe(t *testing.T) {
	binding := newEventExecutionIdentityBindings()
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	if _, ok := binding.Resolve(taskID); ok {
		t.Fatal("unbound task unexpectedly resolved an execution identity")
	}
	if err := binding.Bind(taskID, runID, canonicalExecutionActorKind, "shiro"); err != nil {
		t.Fatalf("Bind() error = %v", err)
	}
	if err := binding.Bind(taskID, runID, canonicalExecutionActorKind, "shiro"); err != nil {
		t.Fatalf("idempotent Bind() error = %v", err)
	}
	if err := binding.Bind(taskID, modulecore.NewRunID(), canonicalExecutionActorKind, "shiro"); err == nil {
		t.Fatal("conflicting RunID rebind was accepted")
	}
	identity, ok := binding.Resolve(taskID)
	if !ok || identity.TaskID != taskID || identity.RunID != runID || identity.ActorKind != canonicalExecutionActorKind || identity.ActorID != "shiro" {
		t.Fatalf("resolved identity = %#v, ok=%v", identity, ok)
	}
	binding.Release(taskID)
	if _, ok := binding.Resolve(taskID); ok {
		t.Fatal("released execution identity was reused")
	}
}

func TestEventExecutionIdentityValidationRejectsNonCanonicalClaims(t *testing.T) {
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	for _, tc := range []struct {
		name  string
		event OrchestratorEvent
	}{
		{name: "run without task", event: OrchestratorEvent{RunID: runID}},
		{name: "run without actor", event: OrchestratorEvent{TaskID: taskID, RunID: runID}},
		{name: "half actor", event: OrchestratorEvent{TaskID: taskID, ActorKind: canonicalExecutionActorKind}},
		{name: "noncanonical actor kind", event: OrchestratorEvent{TaskID: taskID, RunID: runID, ActorKind: "coder", ActorID: "shiro"}},
		{name: "noncanonical actor id", event: OrchestratorEvent{TaskID: taskID, RunID: runID, ActorKind: canonicalExecutionActorKind, ActorID: "coder"}},
		{name: "invalid run", event: OrchestratorEvent{TaskID: taskID, RunID: modulecore.RunID("not-a-run"), ActorKind: canonicalExecutionActorKind, ActorID: "shiro"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateOrchestratorEventExecutionIdentity(tc.event); err == nil {
				t.Fatal("invalid execution identity was accepted")
			}
		})
	}
	if err := ValidateOrchestratorEventExecutionIdentity(OrchestratorEvent{TaskID: taskID, Type: "routing.decision"}); err != nil {
		t.Fatalf("pre-Run Task-only event rejected: %v", err)
	}
}

func TestEventPortBindsOnlyPostRunEvents(t *testing.T) {
	listener := &phase11RecordingEventListener{}
	port := newMessageEventPort(listener)
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	port.BindTrace(taskID.String(), modulecore.NewTraceID())
	if _, err := port.Publish("routing.decision", "mio", "", "route", "CODE", taskID.String(), "session", "viewer", "chat", "", nil); err != nil {
		t.Fatalf("pre-Run Publish() error = %v", err)
	}
	if got := listener.events[0]; got.RunID != "" || got.ActorKind != "" || got.ActorID != "" {
		t.Fatalf("pre-Run event claimed execution identity: %#v", got)
	}
	if err := port.BindExecutionIdentity(taskID, runID, canonicalExecutionActorKind, "shiro"); err != nil {
		t.Fatalf("BindExecutionIdentity() error = %v", err)
	}
	if _, err := port.Publish("agent.response", "shiro", "user", "done", "CODE", taskID.String(), "session", "viewer", "chat", "", nil); err != nil {
		t.Fatalf("post-Run Publish() error = %v", err)
	}
	got := listener.events[1]
	if got.TaskID != taskID || got.RunID != runID || got.ActorKind != canonicalExecutionActorKind || got.ActorID != "shiro" {
		t.Fatalf("post-Run event identity = %#v", got)
	}
}
