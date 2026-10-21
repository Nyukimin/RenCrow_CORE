package storagehost

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// GroupEvent serves the canonical event envelope store. The storage host
// opens the owner SQLite store on its own disk; the CORE execution host only
// sends closed typed operations with natural keys (event id, component id).
const GroupEvent = "event"

// EventGroupOwner is the durable owner contract behind the event group.
// AppendSequenced is optional: without it the sequenced operation is rejected
// as operation_unsupported instead of silently degrading.
type EventGroupOwner interface {
	modulecore.EventStore
	ListByTraceID(context.Context, modulecore.TraceID, int) ([]modulecore.EventEnvelope, error)
	HasPublicConversationFinalResponse(context.Context, modulecore.TraceID, modulecore.TurnID, modulecore.SessionID) (bool, error)
}

type eventEnvelopePayload struct {
	Event modulecore.EventEnvelope `json:"event"`
}

type eventGetPayload struct {
	EventID string `json:"event_id"`
}

type eventListPayload struct {
	ComponentID string `json:"component_id"`
	Limit       int    `json:"limit"`
}

type eventListTracePayload struct {
	TraceID modulecore.TraceID `json:"trace_id"`
	Limit   int                `json:"limit"`
}

type eventPublicConversationFinalPayload struct {
	TraceID   modulecore.TraceID   `json:"trace_id"`
	TurnID    modulecore.TurnID    `json:"turn_id"`
	SessionID modulecore.SessionID `json:"session_id"`
}

type eventResultPayload struct {
	Event modulecore.EventEnvelope `json:"event"`
}

type eventGetResult struct {
	Found bool                      `json:"found"`
	Event *modulecore.EventEnvelope `json:"event,omitempty"`
}

type eventListResult struct {
	Events []modulecore.EventEnvelope `json:"events"`
}

type eventPublicConversationFinalResult struct {
	Found bool `json:"found"`
}

// RegisterEventGroup registers the closed event operations against the owner
// store. Only request/envelope validation completed before the owner is called
// proves that no owner mutation could have occurred. Owner errors, including
// commit errors, leave the operation begun for read-only reconciliation.
func RegisterEventGroup(h *Handler, store EventGroupOwner) error {
	if h == nil || store == nil {
		return errors.New("storagehost: event group needs a handler and an owner store")
	}
	if err := h.RegisterRecoverable(GroupEvent, "append", func(ctx context.Context, mutation MutationMetadata) (any, error) {
		var p eventEnvelopePayload
		if err := decodeEventPayload(mutation.Payload, &p); err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "event append payload rejected"))
		}
		if err := validateLiveEventEnvelope(p.Event); err != nil {
			return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "event envelope rejected: "+err.Error()))
		}
		if err := store.Append(ctx, p.Event); err != nil {
			return nil, err
		}
		return nil, nil
	}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
		return reconcileEventAppend(ctx, store, mutation, false)
	}); err != nil {
		return err
	}
	sequecer, hasSequencer := store.(modulecore.SequencedEventAppender)
	if hasSequencer {
		if err := h.RegisterRecoverable(GroupEvent, "append_sequenced", func(ctx context.Context, mutation MutationMetadata) (any, error) {
			var p eventEnvelopePayload
			if err := decodeEventPayload(mutation.Payload, &p); err != nil {
				return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "event append_sequenced payload rejected"))
			}
			if err := validateLiveEventEnvelope(p.Event); err != nil {
				return nil, ownerRolledBack(NewError(ErrorCodeSchemaRejected, "event envelope rejected: "+err.Error()))
			}
			persisted, err := sequecer.AppendSequenced(ctx, p.Event)
			if err != nil {
				return nil, err
			}
			return eventResultPayload{Event: persisted}, nil
		}, func(ctx context.Context, mutation MutationMetadata) (ReconcileDecision, error) {
			return reconcileEventAppend(ctx, store, mutation, true)
		}); err != nil {
			return err
		}
	}
	if err := h.Register(GroupEvent, "get", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p eventGetPayload
		if err := decodeEventPayload(raw, &p); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "event get payload rejected")
		}
		id := modulecore.EventID(p.EventID)
		if err := id.Validate(); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "event_id is not canonical")
		}
		env, found, err := store.GetByID(ctx, id)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "event store read failed")
		}
		if !found {
			return eventGetResult{Found: false}, nil
		}
		return eventGetResult{Found: true, Event: &env}, nil
	}); err != nil {
		return err
	}
	if err := h.Register(GroupEvent, "list_component", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p eventListPayload
		if err := decodeEventPayload(raw, &p); err != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "event list payload rejected")
		}
		events, err := store.ListByComponent(ctx, p.ComponentID, p.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "event store read failed")
		}
		return eventListResult{Events: events}, nil
	}); err != nil {
		return err
	}
	if err := h.Register(GroupEvent, "public_conversation_final", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p eventPublicConversationFinalPayload
		if err := decodeEventPayload(raw, &p); err != nil || p.TraceID.Validate() != nil || p.TurnID.Validate() != nil || p.SessionID.Validate() != nil {
			return nil, NewError(ErrorCodeSchemaRejected, "public conversation final lookup payload rejected")
		}
		found, err := store.HasPublicConversationFinalResponse(ctx, p.TraceID, p.TurnID, p.SessionID)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "public conversation final lookup failed")
		}
		return eventPublicConversationFinalResult{Found: found}, nil
	}); err != nil {
		return err
	}
	return h.Register(GroupEvent, "list_trace", false, func(ctx context.Context, raw json.RawMessage) (any, error) {
		var p eventListTracePayload
		if err := decodeEventPayload(raw, &p); err != nil || p.TraceID.Validate() != nil || p.Limit <= 0 || p.Limit > 1000 {
			return nil, NewError(ErrorCodeSchemaRejected, "event trace lookup payload rejected")
		}
		events, err := store.ListByTraceID(ctx, p.TraceID, p.Limit)
		if err != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "event trace lookup failed")
		}
		return eventListResult{Events: events}, nil
	})
}

func decodeEventPayload(raw json.RawMessage, destination any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return errors.New("event payload must be a JSON object")
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return errors.New("event payload schema rejected")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("event payload trailing data rejected")
	}
	return nil
}

func validateLiveEventEnvelope(event modulecore.EventEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return err
	}
	if event.EventSeq != 0 {
		return errors.New("event_seq must be zero for live append")
	}
	return nil
}

func reconcileEventAppend(ctx context.Context, store EventGroupOwner, mutation MutationMetadata, sequenced bool) (ReconcileDecision, error) {
	var request eventEnvelopePayload
	if err := decodeEventPayload(mutation.Payload, &request); err != nil {
		return UnknownOutcome(), nil
	}
	if err := validateLiveEventEnvelope(request.Event); err != nil {
		return UnknownOutcome(), nil
	}
	persisted, found, err := store.GetByID(ctx, request.Event.EventID)
	if err != nil {
		return UnknownOutcome(), nil
	}
	if !found {
		return ConfirmedNotCommitted(), nil
	}
	if !sameEventExceptAssignedSequence(request.Event, persisted) {
		return UnknownOutcome(), nil
	}
	if !sequenced {
		return Committed(nil), nil
	}
	return Committed(eventResultPayload{Event: persisted}), nil
}

func sameEventExceptAssignedSequence(requested, persisted modulecore.EventEnvelope) bool {
	if requested.EventSeq != 0 || persisted.EventSeq <= 0 {
		return false
	}
	requested.EventSeq = persisted.EventSeq
	want, err := json.Marshal(requested)
	if err != nil {
		return false
	}
	got, err := json.Marshal(persisted)
	if err != nil {
		return false
	}
	return bytes.Equal(want, got)
}

// ownerRolledBack marks an owner error whose implementation provably rolled
// back (the owner transaction defers rollback on every error path), so the
// journal may release the op_id instead of keeping it begun.
type ownerRolledBackError struct{ inner error }

func (e ownerRolledBackError) Error() string    { return e.inner.Error() }
func (e ownerRolledBackError) Unwrap() error    { return e.inner }
func (e ownerRolledBackError) RolledBack() bool { return true }

func ownerRolledBack(err error) error { return ownerRolledBackError{inner: err} }

// EventStoreClient implements the canonical event store contracts over the
// storage host protocol. It never falls back to a local store.
type EventStoreClient struct {
	client *Client
}

// NewEventStoreClient binds the event group to a client that already
// handshook the storage host contract.
func NewEventStoreClient(c *Client) *EventStoreClient {
	return &EventStoreClient{client: c}
}

// Close implements the shared event store lifecycle without closing the
// underlying storage host client, which may be shared by other groups.
func (s *EventStoreClient) Close() error { return nil }

var (
	_ modulecore.EventStore             = (*EventStoreClient)(nil)
	_ modulecore.SequencedEventAppender = (*EventStoreClient)(nil)
)

// AppendSequenced appends one envelope and returns it with the storage-owned
// sequence. A lost response is resolved through the recorded outcome; the
// same envelope is never appended twice by this client.
func (s *EventStoreClient) AppendSequenced(ctx context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return modulecore.EventEnvelope{}, NewError(ErrorCodeSchemaRejected, "event envelope rejected: "+err.Error())
	}
	var out eventResultPayload
	if err := s.client.Call(ctx, GroupEvent, "append_sequenced", eventEnvelopePayload{Event: event}, &out); err != nil {
		return modulecore.EventEnvelope{}, err
	}
	return out.Event, nil
}

// Append appends one envelope through the owner's sequenced path.
func (s *EventStoreClient) Append(ctx context.Context, event modulecore.EventEnvelope) error {
	if err := modulecore.ValidateEventEnvelope(event); err != nil {
		return NewError(ErrorCodeSchemaRejected, "event envelope rejected: "+err.Error())
	}
	return s.client.Call(ctx, GroupEvent, "append", eventEnvelopePayload{Event: event}, nil)
}

// GetByID reads one envelope by its natural key.
func (s *EventStoreClient) GetByID(ctx context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	var out eventGetResult
	if err := s.client.Call(ctx, GroupEvent, "get", eventGetPayload{EventID: string(eventID)}, &out); err != nil {
		return modulecore.EventEnvelope{}, false, err
	}
	if !out.Found || out.Event == nil {
		return modulecore.EventEnvelope{}, false, nil
	}
	return *out.Event, true, nil
}

// ListByComponent reads the newest envelopes for one component.
func (s *EventStoreClient) ListByComponent(ctx context.Context, componentID string, limit int) ([]modulecore.EventEnvelope, error) {
	var out eventListResult
	if err := s.client.Call(ctx, GroupEvent, "list_component", eventListPayload{ComponentID: componentID, Limit: limit}, &out); err != nil {
		return nil, err
	}
	return out.Events, nil
}

// ListByTraceID reads one exact, ordered trace for bounded crash recovery.
func (s *EventStoreClient) ListByTraceID(ctx context.Context, traceID modulecore.TraceID, limit int) ([]modulecore.EventEnvelope, error) {
	if s == nil || s.client == nil {
		return nil, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if traceID.Validate() != nil || limit <= 0 || limit > 1000 {
		return nil, NewError(ErrorCodeSchemaRejected, "event trace lookup payload rejected")
	}
	var out eventListResult
	if err := s.client.Call(ctx, GroupEvent, "list_trace", eventListTracePayload{TraceID: traceID, Limit: limit}, &out); err != nil {
		return nil, err
	}
	if len(out.Events) > limit {
		return nil, NewError(ErrorCodeStoreUnavailable, "event trace lookup exceeded its bound")
	}
	for _, event := range out.Events {
		if event.TraceID != traceID || modulecore.ValidateEventEnvelope(event) != nil {
			return nil, NewError(ErrorCodeStoreUnavailable, "event trace lookup returned an invalid envelope")
		}
	}
	return out.Events, nil
}

// HasPublicConversationFinalResponse performs an exact bounded lookup on the
// storage owner; no trace enumeration or trace-size assumption is involved.
func (s *EventStoreClient) HasPublicConversationFinalResponse(ctx context.Context, traceID modulecore.TraceID, turnID modulecore.TurnID, sessionID modulecore.SessionID) (bool, error) {
	if s == nil || s.client == nil {
		return false, NewError(ErrorCodeUnreachable, "storage host client is unavailable")
	}
	if traceID.Validate() != nil || turnID.Validate() != nil || sessionID.Validate() != nil {
		return false, NewError(ErrorCodeSchemaRejected, "public conversation final lookup payload rejected")
	}
	var out eventPublicConversationFinalResult
	if err := s.client.Call(ctx, GroupEvent, "public_conversation_final", eventPublicConversationFinalPayload{
		TraceID: traceID, TurnID: turnID, SessionID: sessionID,
	}, &out); err != nil {
		return false, err
	}
	return out.Found, nil
}
