package core

import (
	"context"
	"fmt"
	"strings"
	"time"
)

const EventEnvelopeSchemaVersion = "rencrow.event/v1"

type EventAppender interface {
	Append(context.Context, EventEnvelope) error
}

// SequencedEventAppender assigns the storage-owned event sequence and returns
// the envelope exactly as persisted.
type SequencedEventAppender interface {
	AppendSequenced(context.Context, EventEnvelope) (EventEnvelope, error)
}

type EventReader interface {
	GetByID(context.Context, EventID) (EventEnvelope, bool, error)
	ListByComponent(context.Context, string, int) ([]EventEnvelope, error)
}

type EventStore interface {
	EventAppender
	EventReader
}

// EventEnvelope is the single canonical representation of an occurred fact.
// IDs in this envelope are assigned by the owning runtime, never accepted from
// an LLM or an untrusted external payload.
type EventEnvelope struct {
	SchemaVersion string `json:"schema_version"`

	EventID            EventID   `json:"event_id"`
	EventSeq           EventSeq  `json:"event_seq"`
	TraceID            TraceID   `json:"trace_id"`
	CausationEventID   EventID   `json:"causation_event_id,omitempty"`
	DependencyEventIDs []EventID `json:"dependency_event_ids,omitempty"`

	EventType   string    `json:"event_type"`
	ComponentID string    `json:"component_id"`
	OccurredAt  time.Time `json:"occurred_at"`

	SessionID SessionID `json:"session_id,omitempty"`
	ThreadID  ThreadID  `json:"thread_id,omitempty"`
	TurnID    TurnID    `json:"turn_id,omitempty"`

	WorkstreamID WorkstreamID `json:"workstream_id,omitempty"`
	GoalID       GoalID       `json:"goal_id,omitempty"`
	TaskID       TaskID       `json:"task_id,omitempty"`
	RunID        RunID        `json:"run_id,omitempty"`
	ActionID     ActionID     `json:"action_id,omitempty"`
	AttemptID    AttemptID    `json:"attempt_id,omitempty"`

	MessageID   MessageID   `json:"message_id,omitempty"`
	UtteranceID UtteranceID `json:"utterance_id,omitempty"`
	RequestID   RequestID   `json:"request_id,omitempty"`
	ResponseID  ResponseID  `json:"response_id,omitempty"`

	ActorKind string `json:"actor_kind,omitempty"`
	ActorID   string `json:"actor_id,omitempty"`

	ArtifactID   ArtifactID   `json:"artifact_id,omitempty"`
	EvidenceID   EvidenceID   `json:"evidence_id,omitempty"`
	MemoryID     MemoryID     `json:"memory_id,omitempty"`
	RelationID   RelationID   `json:"relation_id,omitempty"`
	ScheduleID   ScheduleID   `json:"schedule_id,omitempty"`
	QueueItemID  QueueItemID  `json:"queue_item_id,omitempty"`
	CheckpointID CheckpointID `json:"checkpoint_id,omitempty"`
	ReceiptID    ReceiptID    `json:"receipt_id,omitempty"`

	Payload map[string]any `json:"payload,omitempty"`
}

func NewRootEventEnvelope(componentID, eventType string, occurredAt time.Time, payload map[string]any) EventEnvelope {
	return NewEventEnvelope(NewTraceID(), "", nil, componentID, eventType, occurredAt, payload)
}

func NewEventEnvelope(traceID TraceID, causationEventID EventID, dependencyEventIDs []EventID, componentID, eventType string, occurredAt time.Time, payload map[string]any) EventEnvelope {
	return EventEnvelope{
		SchemaVersion:      EventEnvelopeSchemaVersion,
		EventID:            NewEventID(),
		TraceID:            traceID,
		CausationEventID:   causationEventID,
		DependencyEventIDs: append([]EventID(nil), dependencyEventIDs...),
		EventType:          eventType,
		ComponentID:        componentID,
		OccurredAt:         occurredAt.UTC(),
		Payload:            payload,
	}
}

func ValidateEventEnvelope(event EventEnvelope) error {
	if event.SchemaVersion != EventEnvelopeSchemaVersion {
		return fmt.Errorf("schema_version must be %q", EventEnvelopeSchemaVersion)
	}
	if err := event.EventID.Validate(); err != nil {
		return fmt.Errorf("event_id: %w", err)
	}
	if err := event.TraceID.Validate(); err != nil {
		return fmt.Errorf("trace_id: %w", err)
	}
	if event.EventSeq < 0 {
		return fmt.Errorf("event_seq must not be negative")
	}
	if strings.TrimSpace(event.EventType) == "" {
		return fmt.Errorf("event_type is required")
	}
	if strings.TrimSpace(event.ComponentID) == "" {
		return fmt.Errorf("component_id is required")
	}
	if event.OccurredAt.IsZero() {
		return fmt.Errorf("occurred_at is required")
	}
	if !event.CausationEventID.empty() {
		if err := event.CausationEventID.Validate(); err != nil {
			return fmt.Errorf("causation_event_id: %w", err)
		}
		if event.CausationEventID == event.EventID {
			return fmt.Errorf("event cannot cause itself")
		}
	}
	seenDependencies := make(map[EventID]struct{}, len(event.DependencyEventIDs))
	for _, dependencyID := range event.DependencyEventIDs {
		if err := dependencyID.Validate(); err != nil {
			return fmt.Errorf("dependency_event_ids: %w", err)
		}
		if dependencyID == event.EventID {
			return fmt.Errorf("event cannot depend on itself")
		}
		if dependencyID == event.CausationEventID {
			return fmt.Errorf("causation event must not be duplicated as a dependency")
		}
		if _, exists := seenDependencies[dependencyID]; exists {
			return fmt.Errorf("duplicate dependency_event_id %q", dependencyID)
		}
		seenDependencies[dependencyID] = struct{}{}
	}
	return validateOptionalEventIDs(event)
}

func (id EventID) empty() bool { return id == "" }

func validateOptionalEventIDs(event EventEnvelope) error {
	validators := []struct {
		name string
		raw  string
		fn   func() error
	}{
		{"session_id", string(event.SessionID), event.SessionID.Validate},
		{"thread_id", string(event.ThreadID), event.ThreadID.Validate},
		{"turn_id", string(event.TurnID), event.TurnID.Validate},
		{"workstream_id", string(event.WorkstreamID), event.WorkstreamID.Validate},
		{"goal_id", string(event.GoalID), event.GoalID.Validate},
		{"task_id", string(event.TaskID), event.TaskID.Validate},
		{"run_id", string(event.RunID), event.RunID.Validate},
		{"action_id", string(event.ActionID), event.ActionID.Validate},
		{"attempt_id", string(event.AttemptID), event.AttemptID.Validate},
		{"message_id", string(event.MessageID), event.MessageID.Validate},
		{"utterance_id", string(event.UtteranceID), event.UtteranceID.Validate},
		{"request_id", string(event.RequestID), event.RequestID.Validate},
		{"response_id", string(event.ResponseID), event.ResponseID.Validate},
		{"artifact_id", string(event.ArtifactID), event.ArtifactID.Validate},
		{"evidence_id", string(event.EvidenceID), event.EvidenceID.Validate},
		{"memory_id", string(event.MemoryID), event.MemoryID.Validate},
		{"relation_id", string(event.RelationID), event.RelationID.Validate},
		{"schedule_id", string(event.ScheduleID), event.ScheduleID.Validate},
		{"queue_item_id", string(event.QueueItemID), event.QueueItemID.Validate},
		{"checkpoint_id", string(event.CheckpointID), event.CheckpointID.Validate},
		{"receipt_id", string(event.ReceiptID), event.ReceiptID.Validate},
	}
	for _, validator := range validators {
		if validator.raw == "" {
			continue
		}
		if err := validator.fn(); err != nil {
			return fmt.Errorf("%s: %w", validator.name, err)
		}
	}
	return nil
}

// ValidateEventEnvelopeGraph validates references within one closed event set.
// Storage owners use the same rules transactionally against their persisted set.
func ValidateEventEnvelopeGraph(events []EventEnvelope) error {
	byID := make(map[EventID]EventEnvelope, len(events))
	for _, event := range events {
		if err := ValidateEventEnvelope(event); err != nil {
			return fmt.Errorf("event %q: %w", event.EventID, err)
		}
		if _, exists := byID[event.EventID]; exists {
			return fmt.Errorf("duplicate event_id %q", event.EventID)
		}
		byID[event.EventID] = event
	}
	for _, event := range events {
		for _, reference := range eventReferences(event) {
			referenced, exists := byID[reference]
			if !exists {
				return fmt.Errorf("event %q references missing event %q", event.EventID, reference)
			}
			if referenced.TraceID != event.TraceID {
				return fmt.Errorf("event %q references event %q from another trace", event.EventID, reference)
			}
		}
	}
	visiting := make(map[EventID]bool, len(events))
	visited := make(map[EventID]bool, len(events))
	var visit func(EventID) error
	visit = func(eventID EventID) error {
		if visiting[eventID] {
			return fmt.Errorf("event graph contains a cycle at %q", eventID)
		}
		if visited[eventID] {
			return nil
		}
		visiting[eventID] = true
		for _, reference := range eventReferences(byID[eventID]) {
			if err := visit(reference); err != nil {
				return err
			}
		}
		visiting[eventID] = false
		visited[eventID] = true
		return nil
	}
	for eventID := range byID {
		if err := visit(eventID); err != nil {
			return err
		}
	}
	return nil
}

func eventReferences(event EventEnvelope) []EventID {
	references := make([]EventID, 0, 1+len(event.DependencyEventIDs))
	if !event.CausationEventID.empty() {
		references = append(references, event.CausationEventID)
	}
	return append(references, event.DependencyEventIDs...)
}
