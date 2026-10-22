package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// DelegationTaskSourceInput limits access to the current accepted delegation
// source to a live task-start lease and the trusted worker identity.
type DelegationTaskSourceInput struct {
	WorkLeaseInput
	SessionID modulecore.SessionID
	Assignee  string
}

// DelegationTaskSource is the exact handoff, accepted readback, and authorized
// public context from which the Task owner may create the delegated Task.
type DelegationTaskSource struct {
	Work     LeasedWorkItem                `json:"work"`
	OwnerID  string                        `json:"owner_id"`
	Handoff  conversation.HandoffEnvelope  `json:"handoff"`
	Readback conversation.ReadbackEnvelope `json:"readback"`
	Context  []DelegationContextMessage    `json:"context"`
}

// LoadDelegationTask revalidates the active task-start lease and loads only a
// current accepted readback with its canonical handoff and hash-checked public
// context. The method is read-only so TaskManager can call it again at its
// pre-commit admission boundary.
func (s *SQLiteStore) LoadDelegationTask(ctx context.Context, input DelegationTaskSourceInput) (DelegationTaskSource, error) {
	if err := s.ensureOpen(); err != nil {
		return DelegationTaskSource{}, err
	}
	if ctx == nil || input.SessionID == "" || strings.TrimSpace(input.Assignee) == "" || input.Assignee != strings.TrimSpace(input.Assignee) {
		return DelegationTaskSource{}, errors.New("delegation task source requires context, session, and trimmed assignee")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DelegationTaskSource{}, err
	}
	defer tx.Rollback()

	work, err := loadCurrentWorkLease(ctx, tx, input.WorkLeaseInput)
	if err != nil {
		return DelegationTaskSource{}, err
	}
	if work.Kind != "start_delegated_task" || work.SessionID != input.SessionID {
		return DelegationTaskSource{}, fmt.Errorf("%w: task-start lease kind, session, or assignee mismatch", ErrDelegationWorkConflict)
	}
	var delegationID string
	var revision int64
	err = tx.QueryRowContext(ctx, `
		SELECT d.delegation_id, d.revision
		FROM live_delegation d
		WHERE ? = 'delegation:' || d.delegation_id || ':' || d.revision || ':start_task'`, work.DedupeKey,
	).Scan(&delegationID, &revision)
	if err != nil || revision < 1 {
		return DelegationTaskSource{}, fmt.Errorf("%w: task-start dedupe key is invalid", ErrDelegationWorkConflict)
	}
	requestID := modulecore.RequestID(delegationID)
	var latestRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?`, delegationID).Scan(&latestRevision); err != nil {
		return DelegationTaskSource{}, err
	}
	if latestRevision != revision {
		return DelegationTaskSource{}, fmt.Errorf("%w: delegation revision is no longer current", ErrDelegationWorkConflict)
	}
	delegation, err := loadReadbackDelegation(ctx, tx, requestID, uint64(revision))
	if err != nil || delegation.sessionID != string(input.SessionID) || delegation.assignee != input.Assignee ||
		delegation.delegationState != string(conversation.ReadbackStatusAccepted) || delegation.turnState != "completed" ||
		!delegation.turnDelegationID.Valid || delegation.turnDelegationID.String != delegationID {
		return DelegationTaskSource{}, fmt.Errorf("%w: delegation is not currently accepted for this actor", ErrDelegationWorkConflict)
	}
	var boundTaskID string
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(work_task_id,'') FROM live_delegation WHERE delegation_id=? AND revision=?`, delegationID, revision).Scan(&boundTaskID); err != nil {
		return DelegationTaskSource{}, err
	}
	if boundTaskID != "" || work.SourceEventID == "" {
		return DelegationTaskSource{}, fmt.Errorf("%w: task is already bound or has no readback event", ErrDelegationWorkConflict)
	}

	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return DelegationTaskSource{}, fmt.Errorf("load delegated task handoff context: %w", err)
	}
	handoff, instruction, err := validateStoredHandoff(ctx, tx, delegation, contextEvent, ReadbackInput{
		DelegationID: requestID, Revision: uint64(revision), Assignee: input.Assignee,
	})
	if err != nil || handoff.FromActor != "mio" || instruction.EventID == "" {
		return DelegationTaskSource{}, fmt.Errorf("%w: stored handoff is not an authenticated Mio delegation", ErrDelegationWorkConflict)
	}

	readbackEvent, err := loadEventEnvelopeTx(ctx, tx, work.SourceEventID)
	if err != nil {
		return DelegationTaskSource{}, fmt.Errorf("load accepted delegated task readback: %w", err)
	}
	if readbackEvent.CausationEventID != instruction.EventID {
		return DelegationTaskSource{}, fmt.Errorf("%w: readback does not descend from the current instruction", ErrDelegationWorkConflict)
	}
	var index MessageIndex
	err = tx.QueryRowContext(ctx, `
		SELECT message_id,event_id,session_id,turn_id,from_actor,to_kind,to_actor,kind,reply_to_message_id,visibility
		FROM live_message_index WHERE event_id=?`, string(readbackEvent.EventID)).Scan(
		&index.MessageID, &index.EventID, &index.SessionID, &index.TurnID, &index.FromActor,
		&index.ToKind, &index.ToActor, &index.Kind, &index.ReplyToMessageID, &index.Visibility,
	)
	if err != nil || index.MessageID != readbackEvent.MessageID || index.SessionID != input.SessionID ||
		index.TurnID != modulecore.TurnID(delegation.originTurnID) || index.FromActor != input.Assignee ||
		index.ToKind != "user" || index.ToActor != delegation.sessionOwnerID || index.Kind != "readback" ||
		index.Visibility != "public" || index.ReplyToMessageID != handoff.ReplyTo {
		return DelegationTaskSource{}, fmt.Errorf("%w: readback public message index is inconsistent", ErrDelegationWorkConflict)
	}
	if err := validateMessageIndex(index, readbackEvent, input.SessionID, modulecore.TurnID(delegation.originTurnID)); err != nil {
		return DelegationTaskSource{}, fmt.Errorf("%w: readback event is not accepted", ErrDelegationWorkConflict)
	}
	readback, err := decodeAcceptedTaskReadback(readbackEvent, handoff, delegation, input.Assignee)
	if err != nil {
		return DelegationTaskSource{}, err
	}
	contextMessages, err := loadAndValidateDelegationContext(ctx, tx, contextEvent, handoff, delegation)
	if err != nil {
		return DelegationTaskSource{}, err
	}
	if err := tx.Commit(); err != nil {
		return DelegationTaskSource{}, err
	}
	work.Assignee = input.Assignee
	return DelegationTaskSource{
		Work: work, OwnerID: delegation.sessionOwnerID, Handoff: handoff, Readback: readback, Context: contextMessages,
	}, nil
}

func decodeAcceptedTaskReadback(event modulecore.EventEnvelope, handoff conversation.HandoffEnvelope, delegation storedReadbackDelegation, assignee string) (conversation.ReadbackEnvelope, error) {
	if event.ComponentID != "orchestrator" || event.EventType != "agent.response" ||
		event.SessionID != modulecore.SessionID(delegation.sessionID) || event.TurnID != modulecore.TurnID(delegation.originTurnID) ||
		event.RequestID != handoff.DelegationID || event.CausationEventID == "" ||
		payloadString(event.Payload, "kind") != "readback" || payloadString(event.Payload, "visibility") != "public" ||
		payloadString(event.Payload, "availability") != "free" || payloadString(event.Payload, "from") != assignee ||
		payloadString(event.Payload, "source_actor_id") != assignee || payloadString(event.Payload, "to") != "user" ||
		payloadString(event.Payload, "delegation_id") != string(handoff.DelegationID) {
		return conversation.ReadbackEnvelope{}, ErrDelegationWorkConflict
	}
	revision, ok := eventPayloadUint64(event.Payload["delegation_revision"])
	if !ok || revision != handoff.Revision {
		return conversation.ReadbackEnvelope{}, ErrDelegationWorkConflict
	}
	payload := event.Payload
	readback := conversation.ReadbackEnvelope{
		DelegationID:   handoff.DelegationID,
		Revision:       handoff.Revision,
		Sender:         assignee,
		SchemaVersion:  payloadString(payload, "schema_version"),
		Status:         conversation.ReadbackStatus(payloadString(payload, "status")),
		UnderstoodGoal: payloadString(payload, "understood_goal"),
		ExpectedOutput: payloadString(payload, "expected_output"),
		Text:           payloadString(payload, "text"),
	}
	keys, ok := payloadStringSlice(payload["acknowledged_constraint_keys"])
	if !ok {
		return conversation.ReadbackEnvelope{}, ErrDelegationWorkConflict
	}
	readback.AcknowledgedConstraintKeys = keys
	if err := conversation.ValidateReadbackEnvelope(readback, handoff, conversation.TrustedReadbackMetadata{
		DelegationID: handoff.DelegationID, Revision: handoff.Revision, Sender: assignee,
	}); err != nil || readback.Status != conversation.ReadbackStatusAccepted || event.MessageID == "" ||
		payloadString(payload, "reply_to_message_id") != string(handoff.ReplyTo) {
		return conversation.ReadbackEnvelope{}, ErrDelegationWorkConflict
	}
	return readback, nil
}

func payloadStringSlice(value any) ([]string, bool) {
	values, ok := value.([]any)
	if !ok {
		if direct, directOK := value.([]string); directOK {
			return append([]string(nil), direct...), true
		}
		return nil, false
	}
	result := make([]string, len(values))
	for i, value := range values {
		item, ok := value.(string)
		if !ok {
			return nil, false
		}
		result[i] = item
	}
	return result, true
}

func eventPayloadUint64(value any) (uint64, bool) {
	switch value := value.(type) {
	case float64:
		if value < 1 || value > math.MaxInt64 || math.Trunc(value) != value {
			return 0, false
		}
		return uint64(value), true
	case int64:
		if value < 1 {
			return 0, false
		}
		return uint64(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil || parsed < 1 {
			return 0, false
		}
		return uint64(parsed), true
	default:
		return 0, false
	}
}
