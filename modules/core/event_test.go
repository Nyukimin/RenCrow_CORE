package core

import (
	"testing"
	"time"
)

func eventFixture(eventID EventID, traceID TraceID, eventType string) EventEnvelope {
	return EventEnvelope{
		SchemaVersion: EventEnvelopeSchemaVersion,
		EventID:       eventID,
		TraceID:       traceID,
		EventType:     eventType,
		ComponentID:   "rencrow-core",
		OccurredAt:    time.Date(2026, 8, 29, 22, 30, 0, 0, time.UTC),
	}
}

func TestValidateEventEnvelopeGraphAcceptsRootChildParallelAndJoin(t *testing.T) {
	traceID := NewTraceID()
	root := eventFixture(NewEventID(), traceID, "task.created")
	left := eventFixture(NewEventID(), traceID, "attempt.started")
	left.CausationEventID = root.EventID
	right := eventFixture(NewEventID(), traceID, "attempt.started")
	right.CausationEventID = root.EventID
	join := eventFixture(NewEventID(), traceID, "verification.completed")
	join.CausationEventID = left.EventID
	join.DependencyEventIDs = []EventID{right.EventID}

	if err := ValidateEventEnvelopeGraph([]EventEnvelope{root, left, right, join}); err != nil {
		t.Fatalf("ValidateEventEnvelopeGraph() error = %v", err)
	}
}

func TestValidateEventEnvelopeGraphRejectsMissingCausation(t *testing.T) {
	traceID := NewTraceID()
	child := eventFixture(NewEventID(), traceID, "run.started")
	child.CausationEventID = NewEventID()

	if err := ValidateEventEnvelopeGraph([]EventEnvelope{child}); err == nil {
		t.Fatal("ValidateEventEnvelopeGraph() error = nil, want missing causation rejection")
	}
}

func TestValidateEventEnvelopeGraphRejectsCycle(t *testing.T) {
	traceID := NewTraceID()
	first := eventFixture(NewEventID(), traceID, "run.started")
	second := eventFixture(NewEventID(), traceID, "run.completed")
	first.CausationEventID = second.EventID
	second.CausationEventID = first.EventID

	if err := ValidateEventEnvelopeGraph([]EventEnvelope{first, second}); err == nil {
		t.Fatal("ValidateEventEnvelopeGraph() error = nil, want cycle rejection")
	}
}

func TestValidateEventEnvelopeRejectsMissingRequiredFieldAndWrongTypedID(t *testing.T) {
	valid := eventFixture(NewEventID(), NewTraceID(), "run.started")
	tests := []struct {
		name   string
		mutate func(*EventEnvelope)
	}{
		{name: "schema version", mutate: func(event *EventEnvelope) { event.SchemaVersion = "" }},
		{name: "event id", mutate: func(event *EventEnvelope) { event.EventID = "" }},
		{name: "trace id", mutate: func(event *EventEnvelope) { event.TraceID = "" }},
		{name: "event type", mutate: func(event *EventEnvelope) { event.EventType = "" }},
		{name: "component id", mutate: func(event *EventEnvelope) { event.ComponentID = "" }},
		{name: "occurred at", mutate: func(event *EventEnvelope) { event.OccurredAt = time.Time{} }},
		{name: "typed event id", mutate: func(event *EventEnvelope) { event.EventID = EventID(NewRunID()) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			item := valid
			test.mutate(&item)
			if err := ValidateEventEnvelope(item); err == nil {
				t.Fatal("ValidateEventEnvelope() error = nil")
			}
		})
	}
}

func TestValidateEventEnvelopeAllowsUnassignedEventSeqButRejectsNegative(t *testing.T) {
	valid := eventFixture(NewEventID(), NewTraceID(), "run.started")
	if valid.EventSeq != 0 {
		t.Fatalf("event fixture sequence = %d, want unassigned zero", valid.EventSeq)
	}
	if err := ValidateEventEnvelope(valid); err != nil {
		t.Fatalf("unassigned event sequence rejected: %v", err)
	}
	valid.EventSeq = -1
	if err := ValidateEventEnvelope(valid); err == nil {
		t.Fatal("negative event sequence accepted")
	}
}
