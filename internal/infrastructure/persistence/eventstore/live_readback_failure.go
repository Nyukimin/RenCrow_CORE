package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const delegationReadbackTimeout = 30 * time.Second

const readbackFailurePublicText = "委譲先から有効な復唱を確認できませんでした。Mioは再び応答できます。"

type ReadbackFailureReason string

const (
	ReadbackFailureInvalidOutput ReadbackFailureReason = "invalid_output"
	ReadbackFailureExpired       ReadbackFailureReason = "deadline_exceeded"
)

var ErrReadbackFailureConflict = errors.New("delegation readback failure conflicts with current work or state")

type ReadbackFailureInput struct {
	OperationID  string
	Work         WorkLeaseInput
	DelegationID modulecore.RequestID
	Revision     uint64
	Assignee     string
	Reason       ReadbackFailureReason
	EventID      modulecore.EventID
	MessageID    modulecore.MessageID
	OccurredAt   time.Time
}

type ReadbackFailureIntent struct {
	DelegationID modulecore.RequestID  `json:"delegation_id"`
	Revision     uint64                `json:"revision"`
	Assignee     string                `json:"assignee"`
	Reason       ReadbackFailureReason `json:"reason"`
}

type ReadbackFailure struct {
	OperationID  string                   `json:"operation_id"`
	DelegationID modulecore.RequestID     `json:"delegation_id"`
	Revision     uint64                   `json:"revision"`
	OriginTurnID modulecore.TurnID        `json:"origin_turn_id"`
	SessionID    modulecore.SessionID     `json:"session_id"`
	Reason       ReadbackFailureReason    `json:"reason"`
	Availability string                   `json:"availability"`
	Event        modulecore.EventEnvelope `json:"event"`
	Message      MessageIndex             `json:"message"`
}

// FailReadback records a bounded failure intent, then atomically commits the
// public handoff_error, the delegation/turn terminal state, and outbox closure.
// The intent lets a later lease resume terminalization without asking the LLM
// to repair or regenerate one turn after a transient storage failure.
func (s *SQLiteStore) FailReadback(ctx context.Context, input ReadbackFailureInput) (ReadbackFailure, error) {
	if err := s.ensureOpen(); err != nil {
		return ReadbackFailure{}, err
	}
	if ctx == nil {
		return ReadbackFailure{}, errors.New("readback failure context is required")
	}
	if err := validateReadbackFailureInput(input); err != nil {
		return ReadbackFailure{}, err
	}
	inputHash, err := readbackFailureInputHash(input)
	if err != nil {
		return ReadbackFailure{}, err
	}
	if s.shouldQueueWrite(ctx) {
		queuedInput := cloneQueuedValue(input)
		return enqueueOrderedWriteResult(s, ctx, 1, queuedInput, func(commandCtx context.Context) (ReadbackFailure, error) {
			return s.FailReadback(commandCtx, queuedInput)
		})
	}
	if err := s.persistReadbackFailureIntent(ctx, input, inputHash); err != nil {
		return ReadbackFailure{}, err
	}
	return s.commitReadbackFailure(ctx, input, inputHash)
}

func (s *SQLiteStore) persistReadbackFailureIntent(ctx context.Context, input ReadbackFailureInput, inputHash string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	intentID := readbackFailureIntentOperationID(input.DelegationID, input.Revision)
	if receipt, found, err := readOperationReceipt(ctx, tx, intentID); err != nil {
		return err
	} else if found {
		if _, err := validateReadbackFailureIntentReceipt(receipt, input, inputHash); err != nil {
			return err
		}
		return tx.Commit()
	}

	delegation, handoff, _, err := validateReadbackFailureFence(ctx, tx, input)
	if err != nil {
		return err
	}
	intent := ReadbackFailureIntent{
		DelegationID: input.DelegationID, Revision: input.Revision,
		Assignee: delegation.assignee, Reason: input.Reason,
	}
	if handoff.DelegationID != intent.DelegationID || handoff.Revision != intent.Revision || handoff.ToActor != intent.Assignee {
		return ErrReadbackFailureConflict
	}
	if err := insertOperationReceipt(ctx, tx, intentID, "readback_failure_intent", inputHash, intent); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) commitReadbackFailure(ctx context.Context, input ReadbackFailureInput, inputHash string) (ReadbackFailure, error) {
	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return ReadbackFailure{}, err
	}
	defer unlockSequence()
	defer tx.Rollback()

	if receipt, found, err := readOperationReceipt(ctx, tx, input.OperationID); err != nil {
		return ReadbackFailure{}, err
	} else if found {
		if receipt.operationKind != "fail_readback" || receipt.inputSHA256 != inputHash {
			return ReadbackFailure{}, ErrOperationConflict
		}
		var failure ReadbackFailure
		if err := json.Unmarshal([]byte(receipt.resultJSON), &failure); err != nil {
			return ReadbackFailure{}, fmt.Errorf("decode terminal readback failure receipt: %w", err)
		}
		if err := validateStoredReadbackFailure(failure, input); err != nil {
			return ReadbackFailure{}, err
		}
		if err := tx.Commit(); err != nil {
			return ReadbackFailure{}, err
		}
		return failure, nil
	}

	delegation, handoff, instruction, err := validateReadbackFailureFence(ctx, tx, input)
	if err != nil {
		return ReadbackFailure{}, err
	}
	intentReceipt, found, err := readOperationReceipt(ctx, tx, readbackFailureIntentOperationID(input.DelegationID, input.Revision))
	if err != nil {
		return ReadbackFailure{}, err
	}
	if !found {
		return ReadbackFailure{}, ErrReadbackFailureConflict
	}
	intent, err := validateReadbackFailureIntentReceipt(intentReceipt, input, inputHash)
	if err != nil {
		return ReadbackFailure{}, err
	}
	if intent.DelegationID != input.DelegationID || intent.Revision != input.Revision || intent.Assignee != input.Assignee || intent.Reason != input.Reason {
		return ReadbackFailure{}, ErrReadbackFailureConflict
	}

	event := makeReadbackFailureEvent(input, delegation, handoff, instruction)
	message := MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID, SessionID: modulecore.SessionID(delegation.sessionID),
		TurnID: modulecore.TurnID(delegation.originTurnID), FromActor: "mio",
		ToKind: "user", ToActor: delegation.sessionOwnerID, Kind: "handoff_error",
		ReplyToMessageID: handoff.ReplyTo, Visibility: "public",
	}
	if err := validateMessageIndex(message, event, message.SessionID, message.TurnID); err != nil {
		return ReadbackFailure{}, fmt.Errorf("validate terminal handoff failure message index: %w", err)
	}
	if err := validateReplyTarget(ctx, tx, message.ReplyToMessageID, message.SessionID); err != nil {
		return ReadbackFailure{}, err
	}
	event.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return ReadbackFailure{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return ReadbackFailure{}, err
	}
	if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
		return ReadbackFailure{}, err
	}
	if err := s.validateReferences(ctx, tx, event); err != nil {
		return ReadbackFailure{}, err
	}
	event, err = insertLiveEvent(ctx, tx, event)
	if err != nil {
		return ReadbackFailure{}, err
	}
	if err := insertMessageIndex(ctx, tx, message); err != nil {
		return ReadbackFailure{}, err
	}
	if err := insertConversationProjectionWork(ctx, tx, event, message); err != nil {
		return ReadbackFailure{}, err
	}
	if err := updateReadbackFailureState(ctx, tx, input, delegation.activeTurnID); err != nil {
		return ReadbackFailure{}, err
	}

	failure := ReadbackFailure{
		OperationID: input.OperationID, DelegationID: input.DelegationID, Revision: input.Revision,
		OriginTurnID: modulecore.TurnID(delegation.originTurnID), SessionID: modulecore.SessionID(delegation.sessionID),
		Reason: input.Reason, Availability: "free", Event: event, Message: message,
	}
	if err := validateStoredReadbackFailure(failure, input); err != nil {
		return ReadbackFailure{}, err
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "fail_readback", inputHash, failure); err != nil {
		return ReadbackFailure{}, err
	}
	if err := tx.Commit(); err != nil {
		return ReadbackFailure{}, err
	}
	s.observeCommittedOrchestratorEvent(failure.Event)
	return failure, nil
}

func validateReadbackFailureFence(ctx context.Context, tx *sql.Tx, input ReadbackFailureInput) (storedReadbackDelegation, conversation.HandoffEnvelope, modulecore.EventEnvelope, error) {
	work, state, err := readWorkForUpdate(ctx, tx, input.Work.WorkID)
	if err != nil {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, err
	}
	if _, err := validateWorkLease(work, state, input.Work, normalizedNow(input.Work.Now)); err != nil {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, err
	}
	if work.Kind != "dispatch_delegation" || work.DedupeKey != readbackDispatchDedupeKey(input.DelegationID, input.Revision) {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, ErrDelegationWorkConflict
	}
	delegation, err := loadReadbackDelegation(ctx, tx, input.DelegationID, input.Revision)
	if err != nil {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, err
	}
	var latestRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?`, string(input.DelegationID)).Scan(&latestRevision); err != nil {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, err
	}
	if latestRevision != int64(input.Revision) || delegation.delegationState != "dispatch_accepted" ||
		delegation.turnState != "awaiting_readback" || !delegation.turnDelegationID.Valid ||
		delegation.turnDelegationID.String != string(input.DelegationID) || delegation.assignee != input.Assignee ||
		work.SessionID != modulecore.SessionID(delegation.sessionID) || work.SourceEventID == "" {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, ErrReadbackFailureConflict
	}
	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, fmt.Errorf("load private handoff context for failure: %w", err)
	}
	handoff, instruction, err := validateStoredHandoff(ctx, tx, delegation, contextEvent, ReadbackInput{
		DelegationID: input.DelegationID, Revision: input.Revision, Assignee: input.Assignee,
	})
	if err != nil {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, err
	}
	if instruction.EventID != work.SourceEventID || handoff.ToActor != input.Assignee {
		return storedReadbackDelegation{}, conversation.HandoffEnvelope{}, modulecore.EventEnvelope{}, ErrDelegationWorkConflict
	}
	return delegation, handoff, instruction, nil
}

func updateReadbackFailureState(ctx context.Context, tx *sql.Tx, input ReadbackFailureInput, activeTurnID string) error {
	result, err := tx.ExecContext(ctx, `
		UPDATE live_delegation SET state='needs_attention'
		WHERE delegation_id=? AND revision=? AND state='dispatch_accepted'`, string(input.DelegationID), int64(input.Revision))
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrReadbackFailureConflict
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE live_conversation_turn SET state='completed'
		WHERE turn_id=?
		  AND delegation_id=? AND state='awaiting_readback'`,
		activeTurnID, string(input.DelegationID))
	if err != nil {
		return err
	}
	updated, err = result.RowsAffected()
	if err != nil || updated != 1 {
		return ErrReadbackFailureConflict
	}
	if err := completeLeasedWork(ctx, tx, input.Work); err != nil {
		return err
	}
	return nil
}

func makeReadbackFailureEvent(input ReadbackFailureInput, delegation storedReadbackDelegation, handoff conversation.HandoffEnvelope, instruction modulecore.EventEnvelope) modulecore.EventEnvelope {
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID,
		TraceID: handoff.SourceTraceID, CausationEventID: instruction.EventID,
		EventType: "agent.response", ComponentID: "orchestrator", OccurredAt: input.OccurredAt.UTC(),
		SessionID: modulecore.SessionID(delegation.sessionID), TurnID: modulecore.TurnID(delegation.originTurnID),
		RequestID: input.DelegationID, MessageID: input.MessageID,
		Payload: map[string]any{
			"kind": "handoff_error", "status": "failed", "reason": string(input.Reason),
			"delegation_id": string(input.DelegationID), "delegation_revision": input.Revision,
			"availability": "free", "visibility": "public", "from": "mio", "source_actor_id": "mio", "to": "user",
			"content": readbackFailurePublicText, "text": readbackFailurePublicText,
			"reply_to_message_id": string(handoff.ReplyTo),
		},
	}
}

func validateStoredReadbackFailure(failure ReadbackFailure, input ReadbackFailureInput) error {
	delegationRevision, revisionOK := eventPayloadUint64(failure.Event.Payload["delegation_revision"])
	if failure.OperationID != input.OperationID || failure.DelegationID != input.DelegationID || failure.Revision != input.Revision ||
		failure.Reason != input.Reason || failure.Availability != "free" || failure.Event.EventSeq <= 0 ||
		failure.Event.RequestID != input.DelegationID || failure.Event.EventType != "agent.response" ||
		failure.Event.ComponentID != "orchestrator" || failure.Event.SessionID != failure.SessionID ||
		failure.Event.TurnID != failure.OriginTurnID || failure.Message.EventID != failure.Event.EventID ||
		failure.Message.MessageID != failure.Event.MessageID || failure.Message.Kind != "handoff_error" ||
		failure.Message.SessionID != failure.SessionID || failure.Message.TurnID != failure.OriginTurnID ||
		failure.Message.FromActor != "mio" || failure.Message.Visibility != "public" ||
		failure.Message.ToKind != "user" || failure.Message.ToActor == "" {
		return errors.New("stored terminal readback failure is inconsistent")
	}
	if err := modulecore.ValidateEventEnvelope(failure.Event); err != nil {
		return fmt.Errorf("stored terminal readback event is invalid: %w", err)
	}
	if payloadString(failure.Event.Payload, "kind") != "handoff_error" || payloadString(failure.Event.Payload, "status") != "failed" ||
		payloadString(failure.Event.Payload, "reason") != string(input.Reason) ||
		payloadString(failure.Event.Payload, "delegation_id") != string(input.DelegationID) ||
		!revisionOK || delegationRevision != input.Revision ||
		payloadString(failure.Event.Payload, "availability") != "free" || payloadString(failure.Event.Payload, "visibility") != "public" ||
		payloadString(failure.Event.Payload, "from") != "mio" || payloadString(failure.Event.Payload, "source_actor_id") != "mio" ||
		payloadString(failure.Event.Payload, "to") != "user" || payloadString(failure.Event.Payload, "content") != readbackFailurePublicText ||
		payloadString(failure.Event.Payload, "text") != readbackFailurePublicText ||
		payloadString(failure.Event.Payload, "reply_to_message_id") == "" ||
		string(failure.Message.ReplyToMessageID) != payloadString(failure.Event.Payload, "reply_to_message_id") {
		return errors.New("stored terminal readback event payload is invalid")
	}
	return nil
}

func validateReadbackFailureInput(input ReadbackFailureInput) error {
	if input.OperationID != readbackFailureOperationID(input.DelegationID, input.Revision) {
		return errors.New("readback failure operation_id does not match its delegation revision")
	}
	if err := input.DelegationID.Validate(); err != nil {
		return fmt.Errorf("delegation_id: %w", err)
	}
	if input.Revision == 0 || input.Revision > math.MaxInt64 {
		return errors.New("readback failure revision must be positive and fit the stored revision column")
	}
	if strings.TrimSpace(input.Assignee) == "" || input.Assignee != strings.TrimSpace(input.Assignee) {
		return errors.New("readback failure assignee is required and must be trimmed")
	}
	if !validPersistedReadbackFailureReason(input.Reason) {
		return errors.New("readback failure reason is invalid")
	}
	if input.Work.WorkID == "" || input.Work.LeaseToken == "" || input.Work.Version < 1 || input.Work.Now.IsZero() {
		return errors.New("readback failure requires the current work lease and time")
	}
	if err := input.EventID.Validate(); err != nil {
		return fmt.Errorf("event_id: %w", err)
	}
	if err := input.MessageID.Validate(); err != nil {
		return fmt.Errorf("message_id: %w", err)
	}
	if input.OccurredAt.IsZero() {
		return errors.New("readback failure occurred_at is required")
	}
	return nil
}

func readbackFailureInputHash(input ReadbackFailureInput) (string, error) {
	return hashJSON(struct {
		DelegationID modulecore.RequestID  `json:"delegation_id"`
		Revision     uint64                `json:"revision"`
		Assignee     string                `json:"assignee"`
		Reason       ReadbackFailureReason `json:"reason"`
	}{input.DelegationID, input.Revision, input.Assignee, input.Reason})
}

func validateReadbackFailureIntentReceipt(receipt operationReceipt, input ReadbackFailureInput, inputHash string) (ReadbackFailureIntent, error) {
	if receipt.operationKind != "readback_failure_intent" || receipt.inputSHA256 != inputHash {
		return ReadbackFailureIntent{}, ErrOperationConflict
	}
	var intent ReadbackFailureIntent
	if err := json.Unmarshal([]byte(receipt.resultJSON), &intent); err != nil {
		return ReadbackFailureIntent{}, fmt.Errorf("decode readback failure intent: %w", err)
	}
	if intent.DelegationID != input.DelegationID || intent.Revision != input.Revision ||
		intent.Assignee != input.Assignee || intent.Reason != input.Reason {
		return ReadbackFailureIntent{}, ErrReadbackFailureConflict
	}
	return intent, nil
}

func validPersistedReadbackFailureReason(reason ReadbackFailureReason) bool {
	return reason == ReadbackFailureInvalidOutput || reason == ReadbackFailureExpired
}

func readbackFailureOperationID(id modulecore.RequestID, revision uint64) string {
	return fmt.Sprintf("delegation-readback-failure:%s:%d", id, revision)
}

func readbackFailureIntentOperationID(id modulecore.RequestID, revision uint64) string {
	return fmt.Sprintf("delegation-readback-failure-intent:%s:%d", id, revision)
}

func readbackDispatchDedupeKey(id modulecore.RequestID, revision uint64) string {
	return fmt.Sprintf("delegation:%s:%d:start", id, revision)
}

func readbackDeadlineAt(acceptedAt time.Time) time.Time {
	return acceptedAt.UTC().Add(delegationReadbackTimeout)
}

func delegationReadbackAcceptedAt(ctx context.Context, tx *sql.Tx, delegationID modulecore.RequestID, revision uint64) (time.Time, error) {
	var originTurnID, assignee, sessionID string
	var instructionMessageID, instructionEventID string
	var messageSessionID, messageTurnID, fromActor, toKind, toActor, messageKind, visibility string
	err := tx.QueryRowContext(ctx, `
		SELECT d.origin_turn_id, d.assignee, t.session_id,
		       d.instruction_message_id, m.event_id, m.session_id, m.turn_id,
		       m.from_actor, m.to_kind, m.to_actor, m.kind, m.visibility
		FROM live_delegation d
		JOIN live_conversation_turn t ON t.turn_id=d.origin_turn_id
		JOIN live_message_index m ON m.message_id=d.instruction_message_id
		WHERE d.delegation_id=? AND d.revision=?`, string(delegationID), int64(revision)).Scan(
		&originTurnID, &assignee, &sessionID, &instructionMessageID, &instructionEventID,
		&messageSessionID, &messageTurnID, &fromActor, &toKind, &toActor, &messageKind, &visibility,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrReadbackFailureConflict
	}
	if err != nil {
		return time.Time{}, err
	}
	if instructionMessageID == "" || instructionEventID == "" || assignee == "" || fromActor == "" ||
		messageSessionID != sessionID || messageTurnID != originTurnID || toKind != "agent" ||
		toActor != assignee || messageKind != "delegation_instruction" || visibility != "private" {
		return time.Time{}, ErrReadbackFailureConflict
	}
	instruction, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(instructionEventID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return time.Time{}, ErrReadbackFailureConflict
		}
		return time.Time{}, fmt.Errorf("load persisted delegation instruction event: %w", err)
	}
	if instruction.MessageID != modulecore.MessageID(instructionMessageID) ||
		instruction.EventType != "conversation.delegation_instruction" || instruction.ComponentID == "orchestrator" ||
		instruction.SessionID != modulecore.SessionID(sessionID) || instruction.TurnID != modulecore.TurnID(originTurnID) ||
		instruction.ActorKind != "agent" || instruction.ActorID != fromActor ||
		payloadString(instruction.Payload, "from") != fromActor || payloadString(instruction.Payload, "to") != assignee {
		return time.Time{}, ErrReadbackFailureConflict
	}
	return instruction.OccurredAt.UTC(), nil
}

func loadReadbackFailureIntent(ctx context.Context, tx *sql.Tx, handoff conversation.HandoffEnvelope) (*ReadbackFailureIntent, error) {
	receipt, found, err := readOperationReceipt(ctx, tx, readbackFailureIntentOperationID(handoff.DelegationID, handoff.Revision))
	if err != nil || !found {
		return nil, err
	}
	if receipt.operationKind != "readback_failure_intent" {
		return nil, ErrOperationConflict
	}
	var intent ReadbackFailureIntent
	if err := json.Unmarshal([]byte(receipt.resultJSON), &intent); err != nil {
		return nil, fmt.Errorf("decode readback failure intent: %w", err)
	}
	if intent.DelegationID != handoff.DelegationID || intent.Revision != handoff.Revision ||
		intent.Assignee != handoff.ToActor || !validPersistedReadbackFailureReason(intent.Reason) {
		return nil, ErrReadbackFailureConflict
	}
	inputHash, err := readbackFailureInputHash(ReadbackFailureInput{
		DelegationID: intent.DelegationID, Revision: intent.Revision, Assignee: intent.Assignee, Reason: intent.Reason,
	})
	if err != nil {
		return nil, err
	}
	if receipt.inputSHA256 != inputHash {
		return nil, ErrOperationConflict
	}
	return &intent, nil
}

func delegationReadbackDeadlineForWork(ctx context.Context, tx *sql.Tx, work LeasedWorkItem) (time.Time, error) {
	var delegationID string
	var revision int64
	err := tx.QueryRowContext(ctx, `
		SELECT d.delegation_id, d.revision FROM live_delegation d
		JOIN live_message_index m ON m.message_id=d.instruction_message_id
		WHERE m.event_id=? AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)`,
		string(work.SourceEventID)).Scan(&delegationID, &revision)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, ErrDelegationWorkConflict
	}
	if err != nil {
		return time.Time{}, err
	}
	acceptedAt, err := delegationReadbackAcceptedAt(ctx, tx, modulecore.RequestID(delegationID), uint64(revision))
	if err != nil {
		return time.Time{}, err
	}
	return readbackDeadlineAt(acceptedAt), nil
}

func readStoredReadbackFailure(ctx context.Context, tx *sql.Tx, id modulecore.RequestID, revision uint64) (*ReadbackFailure, error) {
	receipt, found, err := readOperationReceipt(ctx, tx, readbackFailureOperationID(id, revision))
	if err != nil || !found {
		return nil, err
	}
	if receipt.operationKind != "fail_readback" {
		return nil, ErrOperationConflict
	}
	var assignee string
	if err := tx.QueryRowContext(ctx, `SELECT assignee FROM live_delegation WHERE delegation_id=? AND revision=?`, string(id), int64(revision)).Scan(&assignee); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrReadbackFailureConflict
		}
		return nil, err
	}
	var failure ReadbackFailure
	if err := json.Unmarshal([]byte(receipt.resultJSON), &failure); err != nil {
		return nil, fmt.Errorf("decode terminal readback failure receipt: %w", err)
	}
	if err := validateStoredReadbackFailure(failure, ReadbackFailureInput{
		OperationID: readbackFailureOperationID(id, revision), DelegationID: id, Revision: revision, Assignee: assignee, Reason: failure.Reason,
	}); err != nil {
		return nil, err
	}
	inputHash, err := readbackFailureInputHash(ReadbackFailureInput{
		DelegationID: id, Revision: revision, Assignee: assignee, Reason: failure.Reason,
	})
	if err != nil {
		return nil, err
	}
	if receipt.inputSHA256 != inputHash {
		return nil, ErrOperationConflict
	}
	return &failure, nil
}
