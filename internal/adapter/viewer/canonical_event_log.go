package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	canonicalEventComponent = "orchestrator"
	// ListByComponent has a bounded store contract. Read the full bound before
	// applying a Viewer filter so matching older events are not hidden by an
	// unfiltered limit.
	canonicalEventLogReadLimit = 1000
)

type EventLogReader interface {
	Query(context.Context, LogFilter) ([]orchestrator.OrchestratorEvent, error)
}

// CanonicalEventLog projects orchestrator events through the canonical event
// envelope store. It intentionally owns no filesystem or secondary log.
type CanonicalEventLog struct {
	store modulecore.EventStore
}

var _ EventLogReader = (*CanonicalEventLog)(nil)
var _ interface {
	Append(orchestrator.OrchestratorEvent) error
} = (*CanonicalEventLog)(nil)

// NewCanonicalEventLog creates a Viewer event log backed by the injected
// canonical EventStore.
func NewCanonicalEventLog(store modulecore.EventStore) (*CanonicalEventLog, error) {
	if store == nil {
		return nil, fmt.Errorf("canonical event store is required")
	}
	return &CanonicalEventLog{store: store}, nil
}

// Append records one orchestrator event as a server-generated root envelope.
// The event ID is always generated here. A valid incoming trace ID is safe to
// propagate for correlation; otherwise the root-generated trace ID is kept.
// The UI event remains intact in Payload; only canonical IDs that pass their
// own validators are promoted to typed envelope fields.
func (s *CanonicalEventLog) Append(event orchestrator.OrchestratorEvent) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("canonical event store is required")
	}

	payload, err := marshalOrchestratorEventPayload(event)
	if err != nil {
		return err
	}
	traceID := modulecore.TraceID(event.TraceID)
	var envelope modulecore.EventEnvelope
	if traceID.Validate() == nil {
		envelope = modulecore.NewEventEnvelope(traceID, "", nil, canonicalEventComponent, event.Type, orchestratorEventOccurredAt(event.Timestamp), payload)
	} else {
		envelope = modulecore.NewRootEventEnvelope(canonicalEventComponent, event.Type, orchestratorEventOccurredAt(event.Timestamp), payload)
	}
	if messageID := modulecore.MessageID(event.MessageID); messageID.Validate() == nil {
		envelope.MessageID = messageID
	}
	if sessionID := modulecore.SessionID(event.SessionID); sessionID.Validate() == nil {
		envelope.SessionID = sessionID
	}
	if traceID := modulecore.TraceID(event.TraceID); traceID.Validate() == nil {
		envelope.TraceID = traceID
	}

	if err := s.store.Append(context.Background(), envelope); err != nil {
		return fmt.Errorf("append canonical orchestrator event: %w", err)
	}
	return nil
}

// Query reads a bounded canonical component projection, then applies the
// existing Viewer filter semantics and requested result limit.
func (s *CanonicalEventLog) Query(ctx context.Context, filter LogFilter) ([]orchestrator.OrchestratorEvent, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("canonical event store is required")
	}

	envelopes, err := s.store.ListByComponent(ctx, canonicalEventComponent, canonicalEventLogReadLimit)
	if err != nil {
		return nil, fmt.Errorf("list canonical orchestrator events: %w", err)
	}
	sort.SliceStable(envelopes, func(i, j int) bool {
		if envelopes[i].OccurredAt.Equal(envelopes[j].OccurredAt) {
			return envelopes[i].EventID > envelopes[j].EventID
		}
		return envelopes[i].OccurredAt.After(envelopes[j].OccurredAt)
	})
	if len(envelopes) > canonicalEventLogReadLimit {
		envelopes = envelopes[:canonicalEventLogReadLimit]
	}

	items := make([]orchestrator.OrchestratorEvent, 0, len(envelopes))
	for _, envelope := range envelopes {
		if envelope.ComponentID != canonicalEventComponent {
			continue
		}
		event, ok := projectOrchestratorEvent(envelope)
		if !ok || !matchesLogFilter(event, filter) {
			continue
		}
		items = append(items, event)
	}
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func marshalOrchestratorEventPayload(event orchestrator.OrchestratorEvent) (map[string]any, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode orchestrator event payload: %w", err)
	}
	payload := make(map[string]any)
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("decode orchestrator event payload: %w", err)
	}
	return payload, nil
}

func orchestratorEventOccurredAt(raw string) time.Time {
	if occurredAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw)); err == nil {
		return occurredAt.UTC()
	}
	return time.Now().UTC()
}

func projectOrchestratorEvent(envelope modulecore.EventEnvelope) (orchestrator.OrchestratorEvent, bool) {
	if envelope.Payload == nil {
		return orchestrator.OrchestratorEvent{}, false
	}
	encoded, err := json.Marshal(envelope.Payload)
	if err != nil {
		return orchestrator.OrchestratorEvent{}, false
	}
	var event orchestrator.OrchestratorEvent
	if err := json.Unmarshal(encoded, &event); err != nil {
		return orchestrator.OrchestratorEvent{}, false
	}
	// EventType is assigned in the canonical envelope and is therefore the
	// authoritative type if a stored payload was edited independently.
	if strings.TrimSpace(envelope.EventType) != "" {
		event.Type = envelope.EventType
	}
	return event, true
}

func matchesLogFilter(event orchestrator.OrchestratorEvent, filter LogFilter) bool {
	agent := strings.ToLower(strings.TrimSpace(filter.Agent))
	switch {
	case filter.Type != "" && !strings.EqualFold(event.Type, filter.Type):
		return false
	case agent != "" && !strings.EqualFold(event.From, agent) && !strings.EqualFold(event.To, agent):
		return false
	case filter.Route != "" && !strings.EqualFold(event.Route, filter.Route):
		return false
	case filter.JobID != "" && !strings.EqualFold(event.JobID, filter.JobID):
		return false
	case filter.SessionID != "" && !strings.EqualFold(event.SessionID, filter.SessionID):
		return false
	case filter.ChatID != "" && !strings.EqualFold(event.ChatID, filter.ChatID):
		return false
	default:
		return true
	}
}
