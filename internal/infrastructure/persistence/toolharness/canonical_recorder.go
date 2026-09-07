package toolharness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/toolharness"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	canonicalMediationComponentID  = "tool_harness"
	canonicalMediationEventType    = "tool_input_mediated"
	canonicalMediationDefaultLimit = 50
	canonicalMediationMaxLimit     = 500
)

var canonicalMediationPayloadFields = map[string]struct{}{
	"tool_name":                 {},
	"raw_input_hash":            {},
	"validation_status":         {},
	"repairs_applied":           {},
	"relation_defaults_applied": {},
}

// CanonicalRecorder persists tool mediation events in the CORE Event Store.
// The Event Store is the only persistence path owned by this recorder.
type CanonicalRecorder struct {
	store modulecore.EventStore
}

var _ domain.Recorder = (*CanonicalRecorder)(nil)

func NewCanonicalRecorder(store modulecore.EventStore) (*CanonicalRecorder, error) {
	if isNilEventStore(store) {
		return nil, errors.New("canonical event store is required")
	}
	return &CanonicalRecorder{store: store}, nil
}

func (r *CanonicalRecorder) RecordToolMediationEvent(ctx context.Context, event domain.Event) error {
	if r == nil || isNilEventStore(r.store) {
		return errors.New("canonical event store is required")
	}
	if err := requireActiveContext(ctx); err != nil {
		return err
	}

	identity, err := execution.IdentityFromContext(ctx)
	if err != nil {
		return fmt.Errorf("execution identity: %w", err)
	}
	if identity.TraceID == "" {
		return errors.New("execution trace_id is required")
	}
	if err := identity.TraceID.Validate(); err != nil {
		return fmt.Errorf("execution trace_id: %w", err)
	}

	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		return errors.New("tool execution scope is required")
	}
	if err := scope.Validate(); err != nil {
		return fmt.Errorf("tool execution scope: %w", err)
	}

	if err := rejectCallerLineage(event); err != nil {
		return err
	}
	eventID := modulecore.EventID(event.EventID)
	if err := eventID.Validate(); err != nil {
		return fmt.Errorf("event_id: %w", err)
	}
	if err := domain.ValidateEvent(event); err != nil {
		return err
	}

	envelope := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       eventID,
		TraceID:       identity.TraceID,
		EventType:     canonicalMediationEventType,
		ComponentID:   canonicalMediationComponentID,
		OccurredAt:    event.CreatedAt,
		TaskID:        identity.TaskID,
		RunID:         identity.RunID,
		ActorKind:     string(scope.ActorKind),
		ActorID:       scope.ActorID,
		Payload: map[string]any{
			"tool_name":                 event.ToolName,
			"raw_input_hash":            event.RawInputHash,
			"validation_status":         string(event.ValidationStatus),
			"repairs_applied":           event.Repairs,
			"relation_defaults_applied": event.RelationDefaults,
		},
	}
	if actionID, attemptID, bound := execution.BoundActionAttemptFromContext(ctx); bound {
		envelope.ActionID = actionID
		envelope.AttemptID = attemptID
	}
	if err := modulecore.ValidateEventEnvelope(envelope); err != nil {
		return fmt.Errorf("event envelope: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return r.store.Append(ctx, envelope)
}

func (r *CanonicalRecorder) ListRecent(ctx context.Context, limit int) ([]domain.Event, error) {
	if r == nil || isNilEventStore(r.store) {
		return nil, errors.New("canonical event store is required")
	}
	if err := requireActiveContext(ctx); err != nil {
		return nil, err
	}
	boundedLimit, err := boundedMediationLimit(limit)
	if err != nil {
		return nil, err
	}
	envelopes, err := r.store.ListByComponent(ctx, canonicalMediationComponentID, boundedLimit)
	if err != nil {
		return nil, err
	}

	events := make([]domain.Event, 0, len(envelopes))
	for _, envelope := range envelopes {
		if envelope.ComponentID != canonicalMediationComponentID {
			return nil, fmt.Errorf("stored event %q component_id %q is not %q", envelope.EventID, envelope.ComponentID, canonicalMediationComponentID)
		}
		if envelope.EventType != canonicalMediationEventType {
			return nil, fmt.Errorf("stored event %q event_type %q is not %q", envelope.EventID, envelope.EventType, canonicalMediationEventType)
		}
		if err := modulecore.ValidateEventEnvelope(envelope); err != nil {
			return nil, fmt.Errorf("stored event %q: %w", envelope.EventID, err)
		}
		if err := validateStoredMediationEnvelope(envelope); err != nil {
			return nil, fmt.Errorf("stored event %q: %w", envelope.EventID, err)
		}
		if err := validateMediationPayload(envelope.Payload); err != nil {
			return nil, fmt.Errorf("stored event %q payload: %w", envelope.EventID, err)
		}

		payload, err := json.Marshal(envelope.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode stored event %q payload: %w", envelope.EventID, err)
		}
		var event domain.Event
		if err := json.Unmarshal(payload, &event); err != nil {
			return nil, fmt.Errorf("decode stored event %q payload: %w", envelope.EventID, err)
		}
		event.EventID = string(envelope.EventID)
		event.EventSeq = int64(envelope.EventSeq)
		event.TaskID = string(envelope.TaskID)
		event.RunID = string(envelope.RunID)
		event.TraceID = string(envelope.TraceID)
		event.ActorKind = envelope.ActorKind
		event.ActorID = envelope.ActorID
		event.CreatedAt = envelope.OccurredAt
		if err := domain.ValidateEvent(event); err != nil {
			return nil, fmt.Errorf("stored event %q: %w", envelope.EventID, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func requireActiveContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("request context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func rejectCallerLineage(event domain.Event) error {
	if event.EventSeq != 0 {
		return errors.New("tool harness event_seq must be empty")
	}
	for name, value := range map[string]string{
		"task_id":    event.TaskID,
		"run_id":     event.RunID,
		"trace_id":   event.TraceID,
		"actor_kind": event.ActorKind,
		"actor_id":   event.ActorID,
	} {
		if value != "" {
			return fmt.Errorf("tool harness %s must be empty; lineage is bound to execution context", name)
		}
	}
	return nil
}

func validateMediationPayload(payload map[string]any) error {
	for key := range payload {
		if _, ok := canonicalMediationPayloadFields[key]; !ok {
			return fmt.Errorf("field %q is not mediation data", key)
		}
	}
	return nil
}

func boundedMediationLimit(limit int) (int, error) {
	if limit == 0 {
		return canonicalMediationDefaultLimit, nil
	}
	if limit < 0 {
		return 0, errors.New("tool harness recent limit must not be negative")
	}
	if limit > canonicalMediationMaxLimit {
		return 0, fmt.Errorf("tool harness recent limit exceeds %d", canonicalMediationMaxLimit)
	}
	return limit, nil
}

func validateStoredMediationEnvelope(envelope modulecore.EventEnvelope) error {
	if err := envelope.EventSeq.Validate(); err != nil {
		return fmt.Errorf("event_seq: %w", err)
	}
	if err := envelope.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if err := envelope.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id: %w", err)
	}
	if envelope.TraceID == "" {
		return errors.New("trace_id is required")
	}
	if strings.TrimSpace(envelope.ActorID) == "" {
		return errors.New("actor_id is required")
	}
	switch envelope.ActorKind {
	case string(domaintool.ActorKindUser), string(domaintool.ActorKindAgent):
	default:
		return fmt.Errorf("actor_kind %q is invalid", envelope.ActorKind)
	}
	return nil
}

func isNilEventStore(store modulecore.EventStore) bool {
	if store == nil {
		return true
	}
	value := reflect.ValueOf(store)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}
