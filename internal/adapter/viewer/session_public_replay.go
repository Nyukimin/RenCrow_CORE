package viewer

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// SessionPublicReplayScope is constructed only by the trusted runtime route
// after resolving the canonical Viewer session and its server-owned owner.
type SessionPublicReplayScope struct {
	SessionID  modulecore.SessionID
	OwnerID    string
	ActorID    string
	CutoverSeq modulecore.EventSeq
}

// SessionPublicEventReplayReader reads the durable public-message projection
// for one already-authorized v2 session.
type SessionPublicEventReplayReader interface {
	ReplaySessionPublicPage(context.Context, SessionPublicReplayScope, modulecore.EventSeq, modulecore.EventSeq, int) ([]orchestrator.OrchestratorEvent, modulecore.EventSeq, error)
	ReadSessionPublicMessage(context.Context, SessionPublicReplayScope, modulecore.EventID, modulecore.EventSeq) (orchestrator.OrchestratorEvent, bool, error)
}

type sessionPublicEnvelopeReader interface {
	ReadSessionPublicMessagesPage(context.Context, modulecore.SessionID, string, string, modulecore.EventSeq, modulecore.EventSeq, int) ([]modulecore.EventEnvelope, modulecore.EventSeq, error)
	ReadSessionPublicMessageByEventID(context.Context, modulecore.SessionID, string, string, modulecore.EventID, modulecore.EventSeq) (modulecore.EventEnvelope, bool, error)
}

var _ SessionPublicEventReplayReader = (*CanonicalEventLog)(nil)

// ReplaySessionPublicPage validates the trusted scope, asks the EventStore to
// enforce owner, session, recipient, visibility, and cutover in its query, then
// strictly projects the canonical envelopes for SSE.
func (s *CanonicalEventLog) ReplaySessionPublicPage(ctx context.Context, scope SessionPublicReplayScope, after, through modulecore.EventSeq, limit int) ([]orchestrator.OrchestratorEvent, modulecore.EventSeq, error) {
	if s == nil || s.store == nil {
		return nil, 0, fmt.Errorf("canonical event store is required")
	}
	if ctx == nil || scope.SessionID.Validate() != nil || scope.OwnerID == "" || scope.ActorID == "" || scope.CutoverSeq < 0 || after < scope.CutoverSeq || limit <= 0 || limit > canonicalEventLogReadLimit {
		return nil, 0, fmt.Errorf("invalid owner-scoped public replay query")
	}
	reader, ok := s.store.(sessionPublicEnvelopeReader)
	if !ok {
		return nil, 0, fmt.Errorf("canonical event store does not support session-scoped public replay")
	}
	envelopes, watermark, err := reader.ReadSessionPublicMessagesPage(ctx, scope.SessionID, scope.OwnerID, scope.ActorID, after, through, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("read owner-scoped public conversation replay: %w", err)
	}
	if watermark < after || (through != 0 && watermark != through) || len(envelopes) > limit {
		return nil, 0, fmt.Errorf("session public replay returned an invalid watermark or page")
	}
	items := make([]orchestrator.OrchestratorEvent, 0, len(envelopes))
	previous := after
	for index, envelope := range envelopes {
		if envelope.EventSeq <= previous || envelope.EventSeq > watermark || envelope.SessionID != scope.SessionID || envelope.ComponentID != canonicalEventComponent {
			return nil, 0, fmt.Errorf("session public replay envelope %d is outside the trusted scope", index)
		}
		event, err := projectOrchestratorEventStrict(envelope)
		if err != nil {
			return nil, 0, fmt.Errorf("project session public replay envelope %d: %w", index, err)
		}
		if event.SessionID != scope.SessionID || event.Visibility != "public" || event.MessageID.Validate() != nil || event.TurnID.Validate() != nil {
			return nil, 0, fmt.Errorf("session public replay envelope %d is not an indexed public message", index)
		}
		// The durable index is authoritative and the Viewer projection must not
		// expose any component-internal payload fields.
		event.RawContent = ""
		items = append(items, event)
		previous = envelope.EventSeq
	}
	return items, watermark, nil
}

func (s *CanonicalEventLog) ReadSessionPublicMessage(ctx context.Context, scope SessionPublicReplayScope, eventID modulecore.EventID, eventSeq modulecore.EventSeq) (orchestrator.OrchestratorEvent, bool, error) {
	if s == nil || s.store == nil {
		return orchestrator.OrchestratorEvent{}, false, fmt.Errorf("canonical event store is required")
	}
	if ctx == nil || scope.SessionID.Validate() != nil || scope.OwnerID == "" || scope.ActorID == "" || scope.CutoverSeq < 0 || eventID.Validate() != nil || eventSeq <= scope.CutoverSeq {
		return orchestrator.OrchestratorEvent{}, false, fmt.Errorf("invalid owner-scoped public event query")
	}
	reader, ok := s.store.(sessionPublicEnvelopeReader)
	if !ok {
		return orchestrator.OrchestratorEvent{}, false, fmt.Errorf("canonical event store does not support session-scoped public event lookup")
	}
	envelope, found, err := reader.ReadSessionPublicMessageByEventID(ctx, scope.SessionID, scope.OwnerID, scope.ActorID, eventID, eventSeq)
	if err != nil || !found {
		return orchestrator.OrchestratorEvent{}, found, err
	}
	if envelope.EventID != eventID || envelope.EventSeq != eventSeq || envelope.SessionID != scope.SessionID || envelope.ComponentID != canonicalEventComponent {
		return orchestrator.OrchestratorEvent{}, false, fmt.Errorf("session public event lookup returned an envelope outside the trusted scope")
	}
	event, err := projectOrchestratorEventStrict(envelope)
	if err != nil {
		return orchestrator.OrchestratorEvent{}, false, err
	}
	if event.EventID != eventID || event.EventSeq != eventSeq || event.SessionID != scope.SessionID || event.Visibility != "public" || event.MessageID.Validate() != nil || event.TurnID.Validate() != nil {
		return orchestrator.OrchestratorEvent{}, false, fmt.Errorf("session public event is not an indexed public message")
	}
	event.RawContent = ""
	return event, true, nil
}
