package eventstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type ClarificationReplyContextInput struct {
	SessionID   modulecore.SessionID
	ReplyTurnID modulecore.TurnID
	OwnerID     string
}

// ClarificationReplyContext joins the public prompt and answer to their
// canonical private question, handoff, and authorized origin context.
type ClarificationReplyContext struct {
	Reply           AcceptedTurn                 `json:"reply"`
	Receipt         ClarificationReplyReceipt    `json:"receipt"`
	AnswerEvent     modulecore.EventEnvelope     `json:"answer_event"`
	AnswerMessage   MessageIndex                 `json:"answer_message"`
	PromptEvent     modulecore.EventEnvelope     `json:"prompt_event"`
	PromptMessage   MessageIndex                 `json:"prompt_message"`
	ControlTurn     AcceptedTurn                 `json:"control_turn"`
	QuestionEvent   modulecore.EventEnvelope     `json:"question_event"`
	QuestionMessage MessageIndex                 `json:"question_message"`
	Handoff         conversation.HandoffEnvelope `json:"handoff"`
	OriginTurnID    modulecore.TurnID            `json:"origin_turn_id"`
	Context         []DelegationContextMessage   `json:"context"`
	AnswerText      string                       `json:"answer_text"`
}

type ClarificationReplyContinuationInput struct {
	OperationID       string
	OwnerID           string
	SessionID         modulecore.SessionID
	ReplyTurnID       modulecore.TurnID
	PromptMessageID   modulecore.MessageID
	DelegationID      modulecore.RequestID
	Revision          uint64
	GenerationEpoch   int64
	Instruction       string
	InvocationReceipt agent.InvocationReceipt
	EventID           modulecore.EventID
	MessageID         modulecore.MessageID
	ContextEventID    modulecore.EventID
	ReceiptEventID    modulecore.EventID
	OccurredAt        time.Time
}

type ClarificationReplyContinuationCommit struct {
	OperationID             string                        `json:"operation_id"`
	OwnerID                 string                        `json:"owner_id"`
	SessionID               modulecore.SessionID          `json:"session_id"`
	ReplyTurnID             modulecore.TurnID             `json:"reply_turn_id"`
	ReplyClientSubmissionID string                        `json:"reply_client_submission_id"`
	PromptMessageID         modulecore.MessageID          `json:"prompt_message_id"`
	DelegationID            modulecore.RequestID          `json:"delegation_id"`
	Revision                uint64                        `json:"revision"`
	NextRevision            uint64                        `json:"next_revision"`
	ResultState             string                        `json:"result_state"`
	InvocationReceipt       agent.InvocationReceipt       `json:"invocation_receipt"`
	ContextEvent            modulecore.EventEnvelope      `json:"context_event"`
	Event                   modulecore.EventEnvelope      `json:"event"`
	ReceiptEvent            modulecore.EventEnvelope      `json:"receipt_event"`
	Message                 MessageIndex                  `json:"message"`
	Events                  []modulecore.EventEnvelope    `json:"events"`
	MessageIndexes          []MessageIndex                `json:"message_indexes"`
	NextHandoff             *conversation.HandoffEnvelope `json:"next_handoff,omitempty"`
	DispatchQueued          bool                          `json:"dispatch_queued"`
}

func (s *SQLiteStore) LoadClarificationReplyContext(ctx context.Context, input ClarificationReplyContextInput) (ClarificationReplyContext, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationReplyContext{}, err
	}
	if ctx == nil || input.SessionID.Validate() != nil || input.ReplyTurnID.Validate() != nil ||
		strings.TrimSpace(input.OwnerID) == "" || input.OwnerID != strings.TrimSpace(input.OwnerID) {
		return ClarificationReplyContext{}, errors.New("clarification reply context requires context, session, reply Turn, and owner")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClarificationReplyContext{}, err
	}
	defer tx.Rollback()
	loaded, err := loadClarificationReplyContextTx(ctx, tx, input)
	if err != nil {
		return ClarificationReplyContext{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClarificationReplyContext{}, err
	}
	return loaded, nil
}

func loadClarificationReplyContextTx(ctx context.Context, tx *sql.Tx, input ClarificationReplyContextInput) (ClarificationReplyContext, error) {
	return loadClarificationReplyContextWithStateTx(ctx, tx, input, true)
}

func loadCommittedClarificationReplyContextTx(ctx context.Context, tx *sql.Tx, input ClarificationReplyContextInput) (ClarificationReplyContext, error) {
	return loadClarificationReplyContextWithStateTx(ctx, tx, input, false)
}

func loadClarificationReplyContextWithStateTx(ctx context.Context, tx *sql.Tx, input ClarificationReplyContextInput, requireLatest bool) (ClarificationReplyContext, error) {
	var receipt ClarificationReplyReceipt
	var revision int64
	var accepted AcceptedTurn
	var ownerID, actorID, triggerKind, state, operationID, submissionSHA256 string
	var turnSourceID, turnDelegationID string
	var admissionSeq, generationEpoch int64
	err := tx.QueryRowContext(ctx, `
		SELECT r.prompt_message_id,r.source_turn_id,r.delegation_id,r.revision,r.reply_turn_id,r.reply_message_id,
		       r.owner_id,r.session_id,r.client_submission_id,r.submission_sha256,
	       t.turn_id,t.session_id,t.owner_id,t.actor_id,t.trigger_kind,t.client_submission_id,t.accept_receipt_id,
	       t.input_message_id,t.admission_seq,t.generation_epoch,t.state,t.submission_sha256,
	       COALESCE(t.source_turn_id,''),COALESCE(t.delegation_id,'')
		FROM live_clarification_reply_receipt r
		JOIN live_conversation_turn t ON t.turn_id=r.reply_turn_id
		WHERE r.reply_turn_id=?`, string(input.ReplyTurnID)).Scan(
		&receipt.PromptMessageID, &receipt.SourceTurnID, &receipt.DelegationID, &revision,
		&receipt.ReplyTurnID, &receipt.ReplyMessageID, &receipt.OwnerID, &receipt.SessionID,
		&receipt.ClientSubmissionID, &receipt.SubmissionSHA256,
		&accepted.TurnID, &accepted.SessionID, &ownerID, &actorID, &triggerKind, &accepted.ClientSubmissionID,
		&operationID, &accepted.InputMessageID, &admissionSeq, &generationEpoch, &state, &submissionSHA256,
		&turnSourceID, &turnDelegationID,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	if err != nil {
		return ClarificationReplyContext{}, err
	}
	receipt.Revision = uint64(revision)
	if receipt.SessionID != input.SessionID || ownerID != input.OwnerID || receipt.OwnerID != input.OwnerID || accepted.SessionID != input.SessionID ||
		accepted.TurnID != input.ReplyTurnID || accepted.ClientSubmissionID != receipt.ClientSubmissionID ||
		submissionSHA256 != receipt.SubmissionSHA256 || turnSourceID != string(receipt.SourceTurnID) ||
		turnDelegationID != string(receipt.DelegationID) || triggerKind != "clarification_reply" ||
		(state != "accepted" && state != "awaiting_readback" && state != "completed") {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	accepted.OperationID = operationID
	accepted.ActorID = actorID
	accepted.TriggerKind = triggerKind
	accepted.AdmissionSeq = modulecore.EventSeq(admissionSeq)
	accepted.GenerationEpoch = generationEpoch
	accepted.State = state
	accepted.ClarificationReply = &receipt

	answerMessage, err := loadMessageIndexTx(ctx, tx, receipt.ReplyMessageID)
	if err != nil || answerMessage.MessageID != accepted.InputMessageID || answerMessage.EventID == "" {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	answerEvent, err := eventForIndexedMessage(ctx, tx, answerMessage.MessageID)
	if err != nil || answerEvent.EventID != answerMessage.EventID || answerEvent.SessionID != input.SessionID ||
		answerEvent.TurnID != input.ReplyTurnID || answerEvent.EventType != "conversation.user_message" ||
		answerEvent.ComponentID != "orchestrator" || answerEvent.ActorKind != "" || answerEvent.ActorID != "" ||
		answerMessage.FromActor != actorID || answerMessage.ToKind != "agent" || answerMessage.ToActor != "mio" ||
		answerMessage.Kind != "user_message" || answerMessage.Visibility != "public" ||
		answerMessage.ReplyToMessageID != receipt.PromptMessageID ||
		payloadString(answerEvent.Payload, "from") != "user" || payloadString(answerEvent.Payload, "source_actor_id") != actorID ||
		payloadString(answerEvent.Payload, "reply_to_message_id") != string(receipt.PromptMessageID) {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	answerText := payloadString(answerEvent.Payload, "content")
	if answerText == "" || payloadString(answerEvent.Payload, "text") != answerText {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	accepted.InputEvent = answerEvent

	promptMessage, err := loadMessageIndexTx(ctx, tx, receipt.PromptMessageID)
	if err != nil {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	promptEvent, err := eventForIndexedMessage(ctx, tx, receipt.PromptMessageID)
	if err != nil || promptEvent.EventID != promptMessage.EventID || promptEvent.SessionID != input.SessionID ||
		promptEvent.TurnID != promptMessage.TurnID || promptEvent.EventType != "agent.response" || promptEvent.ComponentID != "orchestrator" ||
		promptMessage.FromActor != "mio" || promptMessage.ToKind != "user" || promptMessage.ToActor != input.OwnerID ||
		promptMessage.Kind != "clarification_prompt" || promptMessage.Visibility != "public" ||
		payloadString(promptEvent.Payload, "kind") != "clarification_prompt" || payloadString(promptEvent.Payload, "status") != "awaiting_user_clarification" ||
		payloadString(promptEvent.Payload, "visibility") != "public" || payloadString(promptEvent.Payload, "from") != "mio" ||
		payloadString(promptEvent.Payload, "to") != "user" || payloadString(promptEvent.Payload, "delegation_id") != string(receipt.DelegationID) ||
		payloadUint64Field(promptEvent.Payload, "delegation_revision") != receipt.Revision {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	var assignee string
	query := `SELECT assignee FROM live_delegation WHERE delegation_id=? AND revision=? AND state='needs_clarification'`
	args := []any{string(receipt.DelegationID), int64(receipt.Revision)}
	if requireLatest {
		query += ` AND revision=(SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?)`
		args = append(args, string(receipt.DelegationID))
	}
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&assignee); err != nil {
		return ClarificationReplyContext{}, fmt.Errorf("load clarification reply delegation revision %q/%d: %w", receipt.DelegationID, receipt.Revision, err)
	}
	controlInput := ClarificationControlContextInput{
		SessionID: input.SessionID, ControlTurnID: promptMessage.TurnID, OwnerID: input.OwnerID, Assignee: assignee,
	}
	var control ClarificationControlContext
	if requireLatest {
		control, err = loadClarificationReplyControlContextTx(ctx, tx, controlInput)
	} else {
		control, err = loadCommittedClarificationReplyControlContextTx(ctx, tx, controlInput)
	}
	if err != nil {
		return ClarificationReplyContext{}, fmt.Errorf("load original clarification control context: %w", err)
	}
	if control.Handoff.OriginTurnID != receipt.SourceTurnID ||
		control.Handoff.DelegationID != receipt.DelegationID || control.Handoff.Revision != receipt.Revision ||
		promptEvent.CausationEventID != control.QuestionEvent.EventID || promptMessage.ReplyToMessageID != control.Handoff.ReplyTo {
		return ClarificationReplyContext{}, ErrClarificationReplyConflict
	}
	return ClarificationReplyContext{
		Reply: accepted, Receipt: receipt,
		AnswerEvent: answerEvent, AnswerMessage: answerMessage,
		PromptEvent: promptEvent, PromptMessage: promptMessage,
		ControlTurn: control.ControlTurn, QuestionEvent: control.QuestionEvent,
		QuestionMessage: control.QuestionMessage, Handoff: control.Handoff,
		OriginTurnID: receipt.SourceTurnID, Context: control.Context, AnswerText: answerText,
	}, nil
}

func loadMessageIndexTx(ctx context.Context, tx *sql.Tx, messageID modulecore.MessageID) (MessageIndex, error) {
	var message MessageIndex
	err := tx.QueryRowContext(ctx, `
		SELECT message_id,event_id,session_id,turn_id,from_actor,to_kind,to_actor,kind,reply_to_message_id,visibility
		FROM live_message_index WHERE message_id=?`, string(messageID)).Scan(
		&message.MessageID, &message.EventID, &message.SessionID, &message.TurnID, &message.FromActor,
		&message.ToKind, &message.ToActor, &message.Kind, &message.ReplyToMessageID, &message.Visibility,
	)
	return message, err
}

func validateClarificationReplyTarget(ctx context.Context, tx *sql.Tx, input AcceptInput) (ClarificationReplyReceipt, error) {
	if input.InputMessage.ReplyToMessageID == "" || payloadString(input.InputEvent.Payload, "reply_to_message_id") != string(input.InputMessage.ReplyToMessageID) {
		return ClarificationReplyReceipt{}, ErrClarificationReplyConflict
	}
	const query = `
		WITH candidates AS (
			SELECT m.message_id,m.session_id,s.owner_id,t.source_turn_id,d.delegation_id,d.revision,d.assignee,e.event_seq
			FROM live_message_index m
			JOIN event_envelope e ON e.event_id=m.event_id
			JOIN live_conversation_session s ON s.session_id=m.session_id
			JOIN live_conversation_turn t ON t.turn_id=m.turn_id AND t.session_id=m.session_id AND t.owner_id=s.owner_id
			JOIN live_delegation d ON d.delegation_id=json_extract(e.envelope_json,'$.payload.delegation_id')
			 AND d.revision=CAST(json_extract(e.envelope_json,'$.payload.delegation_revision') AS INTEGER)
			WHERE m.session_id=? AND s.owner_id=?
			  AND m.from_actor='mio' AND m.to_kind='user' AND m.to_actor=s.owner_id
			  AND m.kind='clarification_prompt' AND m.visibility='public'
			  AND e.event_type='agent.response' AND e.component_id='orchestrator'
			  AND json_extract(e.envelope_json,'$.payload.kind')='clarification_prompt'
			  AND json_extract(e.envelope_json,'$.payload.status')='awaiting_user_clarification'
			  AND json_extract(e.envelope_json,'$.payload.visibility')='public'
			  AND json_extract(e.envelope_json,'$.payload.from')='mio'
			  AND json_extract(e.envelope_json,'$.payload.to')='user'
			  AND json_extract(e.envelope_json,'$.payload.delegation_id')=d.delegation_id
			  AND CAST(json_extract(e.envelope_json,'$.payload.delegation_revision') AS INTEGER)=d.revision
			  AND d.state='needs_clarification'
			  AND d.revision=(SELECT MAX(current.revision) FROM live_delegation current WHERE current.delegation_id=d.delegation_id)
			  AND t.trigger_kind='clarification_control' AND t.state='completed'
			  AND t.source_turn_id=d.origin_turn_id AND t.delegation_id=d.delegation_id
			  AND t.generation_epoch=s.epoch
			  AND NOT EXISTS (SELECT 1 FROM live_clarification_reply_receipt used WHERE used.prompt_message_id=m.message_id)
		)
		SELECT message_id,session_id,owner_id,source_turn_id,delegation_id,revision,assignee
		FROM candidates
		WHERE message_id=? AND event_seq=(SELECT MAX(event_seq) FROM candidates)`
	var receipt ClarificationReplyReceipt
	var revision int64
	var sessionID, ownerID, sourceTurnID, delegationID, assignee string
	err := tx.QueryRowContext(ctx, query, string(input.InputEvent.SessionID), input.OwnerID, string(input.InputMessage.ReplyToMessageID)).Scan(
		&receipt.PromptMessageID, &sessionID, &ownerID, &sourceTurnID, &delegationID, &revision, &assignee,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return ClarificationReplyReceipt{}, ErrClarificationReplyConflict
	}
	if err != nil {
		return ClarificationReplyReceipt{}, err
	}
	receipt.SessionID = modulecore.SessionID(sessionID)
	receipt.OwnerID = ownerID
	receipt.SourceTurnID = modulecore.TurnID(sourceTurnID)
	receipt.DelegationID = modulecore.RequestID(delegationID)
	receipt.Revision = uint64(revision)
	receipt.ReplyTurnID = input.InputEvent.TurnID
	receipt.ReplyMessageID = input.InputEvent.MessageID
	receipt.ClientSubmissionID = input.ClientSubmissionID
	if receipt.SessionID != input.InputEvent.SessionID || receipt.OwnerID != input.OwnerID || receipt.SourceTurnID == "" ||
		receipt.Revision == 0 || receipt.DelegationID == "" || input.ActorID == "" {
		return ClarificationReplyReceipt{}, ErrClarificationReplyConflict
	}
	promptMessage, err := loadMessageIndexTx(ctx, tx, receipt.PromptMessageID)
	if err != nil {
		return ClarificationReplyReceipt{}, ErrClarificationReplyConflict
	}
	control, err := loadClarificationReplyControlContextTx(ctx, tx, ClarificationControlContextInput{
		SessionID: receipt.SessionID, ControlTurnID: promptMessage.TurnID, OwnerID: receipt.OwnerID, Assignee: assignee,
	})
	if err != nil || control.Handoff.DelegationID != receipt.DelegationID || control.Handoff.Revision != receipt.Revision ||
		control.Handoff.OriginTurnID != receipt.SourceTurnID {
		return ClarificationReplyReceipt{}, ErrClarificationReplyConflict
	}
	promptEvent, err := eventForIndexedMessage(ctx, tx, promptMessage.MessageID)
	if err != nil || promptEvent.EventID != promptMessage.EventID || promptEvent.SessionID != receipt.SessionID ||
		promptEvent.TurnID != promptMessage.TurnID || promptEvent.EventType != "agent.response" || promptEvent.ComponentID != "orchestrator" ||
		promptEvent.CausationEventID != control.QuestionEvent.EventID || promptMessage.FromActor != "mio" ||
		promptMessage.ToKind != "user" || promptMessage.ToActor != receipt.OwnerID || promptMessage.Kind != "clarification_prompt" ||
		promptMessage.Visibility != "public" || promptMessage.ReplyToMessageID != control.Handoff.ReplyTo ||
		payloadString(promptEvent.Payload, "kind") != "clarification_prompt" ||
		payloadString(promptEvent.Payload, "status") != "awaiting_user_clarification" ||
		payloadString(promptEvent.Payload, "visibility") != "public" || payloadString(promptEvent.Payload, "from") != "mio" ||
		payloadString(promptEvent.Payload, "to") != "user" ||
		payloadString(promptEvent.Payload, "delegation_id") != string(receipt.DelegationID) ||
		payloadUint64Field(promptEvent.Payload, "delegation_revision") != receipt.Revision {
		return ClarificationReplyReceipt{}, ErrClarificationReplyConflict
	}
	return receipt, nil
}

func nullableClarificationReplySourceTurn(receipt *ClarificationReplyReceipt) any {
	if receipt == nil {
		return nil
	}
	return string(receipt.SourceTurnID)
}

func nullableClarificationReplyDelegation(receipt *ClarificationReplyReceipt) any {
	if receipt == nil {
		return nil
	}
	return string(receipt.DelegationID)
}

func insertClarificationReplyReceipt(ctx context.Context, tx *sql.Tx, receipt ClarificationReplyReceipt) error {
	result, err := tx.ExecContext(ctx, `
		INSERT INTO live_clarification_reply_receipt
			(prompt_message_id,owner_id,session_id,source_turn_id,delegation_id,revision,reply_turn_id,reply_message_id,
			 client_submission_id,submission_sha256,created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(prompt_message_id) DO NOTHING`,
		string(receipt.PromptMessageID), receipt.OwnerID, string(receipt.SessionID), string(receipt.SourceTurnID),
		string(receipt.DelegationID), int64(receipt.Revision), string(receipt.ReplyTurnID), string(receipt.ReplyMessageID),
		receipt.ClientSubmissionID, receipt.SubmissionSHA256, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if inserted != 1 {
		return ErrClarificationReplyConflict
	}
	return nil
}

// CommitClarificationReplyContinuation advances a consumed user clarification
// answer into one new private delegation revision. The stored public answer
// becomes an authorized context message; private Agent questions remain their
// original private events and are not fabricated into the context snapshot.
func (s *SQLiteStore) CommitClarificationReplyContinuation(ctx context.Context, input ClarificationReplyContinuationInput) (ClarificationReplyContinuationCommit, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	if ctx == nil {
		return ClarificationReplyContinuationCommit{}, errors.New("clarification reply continuation context is required")
	}
	if err := validateClarificationReplyContinuationInput(input); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	inputHash, err := clarificationReplyContinuationInputHash(input)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if receipt, found, err := readOperationReceipt(ctx, tx, input.OperationID); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	} else if found {
		if receipt.operationKind != "commit_clarification_reply_continuation" || receipt.inputSHA256 != inputHash {
			return ClarificationReplyContinuationCommit{}, ErrOperationConflict
		}
		var result ClarificationReplyContinuationCommit
		if err := json.Unmarshal([]byte(receipt.resultJSON), &result); err != nil {
			return ClarificationReplyContinuationCommit{}, fmt.Errorf("decode clarification reply continuation receipt: %w", err)
		}
		if err := validateStoredClarificationReplyContinuationCommit(result, input); err != nil {
			return ClarificationReplyContinuationCommit{}, err
		}
		reply, err := loadCommittedClarificationReplyContextTx(ctx, tx, ClarificationReplyContextInput{
			SessionID: input.SessionID, ReplyTurnID: input.ReplyTurnID, OwnerID: input.OwnerID,
		})
		if err != nil || reply.Receipt.PromptMessageID != input.PromptMessageID ||
			reply.Receipt.DelegationID != input.DelegationID || reply.Receipt.Revision != input.Revision ||
			validateClarificationReplyContinuationLinks(result, reply) != nil {
			return ClarificationReplyContinuationCommit{}, ErrClarificationReplyConflict
		}
		if err := tx.Commit(); err != nil {
			return ClarificationReplyContinuationCommit{}, err
		}
		committed = true
		return result, nil
	}

	reply, err := loadClarificationReplyContextTx(ctx, tx, ClarificationReplyContextInput{
		SessionID: input.SessionID, ReplyTurnID: input.ReplyTurnID, OwnerID: input.OwnerID,
	})
	if err != nil || reply.Receipt.PromptMessageID != input.PromptMessageID ||
		reply.Receipt.DelegationID != input.DelegationID || reply.Receipt.Revision != input.Revision ||
		reply.Reply.GenerationEpoch != input.GenerationEpoch || reply.Reply.State != "accepted" {
		return ClarificationReplyContinuationCommit{}, ErrClarificationReplyConflict
	}
	result, err := s.commitClarificationReplyContinuationTx(ctx, tx, input, reply)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	if err := insertOperationReceipt(ctx, tx, input.OperationID, "commit_clarification_reply_continuation", inputHash, result); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	if err := tx.Commit(); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	committed = true
	return result, nil
}

func (s *SQLiteStore) commitClarificationReplyContinuationTx(ctx context.Context, tx *sql.Tx, input ClarificationReplyContinuationInput, reply ClarificationReplyContext) (ClarificationReplyContinuationCommit, error) {
	if input.Revision >= uint64(math.MaxInt64) {
		return ClarificationReplyContinuationCommit{}, ErrClarificationReplyConflict
	}
	handoff := reply.Handoff
	delegation, err := loadReadbackDelegation(ctx, tx, input.DelegationID, input.Revision)
	if err != nil || delegation.delegationState != string(conversation.ReadbackStatusNeedsClarification) ||
		delegation.sessionID != string(input.SessionID) || delegation.sessionOwnerID != reply.Receipt.OwnerID {
		return ClarificationReplyContinuationCommit{}, ErrClarificationReplyConflict
	}
	oldContext, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	contextMessages, err := loadAndValidateDelegationContext(ctx, tx, oldContext, handoff, delegation)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	updatedMessages, contextHash, err := extendDelegationContextWithUserAnswer(oldContext, contextMessages, reply.AnswerMessage.MessageID, reply.AnswerText)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}

	newHandoff := handoff
	newHandoff.Revision = input.Revision + 1
	newHandoff.Instruction = input.Instruction
	newHandoff.InstructionMessageID = input.MessageID
	newHandoff.ContextSnapshotHash = contextHash
	if err := conversation.ValidateHandoffEnvelope(newHandoff); err != nil {
		return ClarificationReplyContinuationCommit{}, fmt.Errorf("validate user-reply continuation HandoffEnvelope: %w", err)
	}
	contextPayload, err := cloneEventPayload(oldContext.Payload)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	contextPayload["messages"] = updatedMessages
	contextPayload["context_snapshot_hash"] = contextHash
	contextPayload["handoff"] = newHandoff
	contextEvent := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.ContextEventID, TraceID: handoff.SourceTraceID,
		CausationEventID: oldContext.CausationEventID, EventType: "conversation.delegation_context", ComponentID: "conversation_runtime",
		OccurredAt: input.OccurredAt.UTC(), SessionID: input.SessionID, TurnID: handoff.OriginTurnID,
		RequestID: input.DelegationID, Payload: contextPayload,
	}
	primary := modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion, EventID: input.EventID, TraceID: handoff.SourceTraceID,
		CausationEventID: contextEvent.EventID, EventType: "conversation.delegation_instruction", ComponentID: "delegation_mailbox",
		OccurredAt: input.OccurredAt.UTC(), SessionID: input.SessionID, TurnID: handoff.OriginTurnID,
		RequestID: input.DelegationID, MessageID: input.MessageID, ActorKind: "agent", ActorID: "mio",
		Payload: map[string]any{
			"kind": "delegation_instruction", "delegation_id": string(input.DelegationID), "delegation_revision": input.Revision + 1,
			"visibility": "private", "from": "mio", "source_actor_id": "mio", "to": handoff.ToActor,
			"content": input.Instruction, "text": input.Instruction,
		},
	}
	message := MessageIndex{
		MessageID: input.MessageID, EventID: input.EventID, SessionID: input.SessionID, TurnID: handoff.OriginTurnID,
		FromActor: "mio", ToKind: "agent", ToActor: handoff.ToActor, Kind: "delegation_instruction",
		ReplyToMessageID: handoff.ReplyTo, Visibility: "private",
	}
	receiptEvent := clarificationReplyReceiptEvent(input, reply.AnswerEvent)
	if err := s.insertClarificationEvents(ctx, tx, []modulecore.EventEnvelope{contextEvent, primary, receiptEvent}, []MessageIndex{message}, input.SessionID, handoff.OriginTurnID); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	contextEvent, err = loadEventEnvelopeTx(ctx, tx, input.ContextEventID)
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	primary = primaryFromPersistedEvent(ctx, tx, input.EventID)
	receiptEvent = primaryFromPersistedEvent(ctx, tx, input.ReceiptEventID)
	if primary.EventID == "" || receiptEvent.EventID == "" {
		return ClarificationReplyContinuationCommit{}, errors.New("persisted clarification reply continuation could not be reloaded")
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO live_delegation
		(delegation_id,revision,origin_turn_id,assignee,instruction_message_id,context_event_id,state,work_task_id,report_revision)
		VALUES(?,?,?,?,?,?,'dispatch_accepted',NULL,0)`,
		string(input.DelegationID), int64(input.Revision+1), string(handoff.OriginTurnID), handoff.ToActor,
		string(input.MessageID), string(input.ContextEventID)); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	work := WorkItem{
		WorkID:    fmt.Sprintf("dispatch:%s:%d", input.DelegationID, input.Revision+1),
		DedupeKey: readbackDispatchDedupeKey(input.DelegationID, input.Revision+1), Kind: "dispatch_delegation",
		SourceEventID: input.EventID, SessionID: input.SessionID, Priority: 5,
	}
	if err := insertClarificationDispatch(ctx, tx, work); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	updated, err := tx.ExecContext(ctx, `UPDATE live_conversation_turn SET state='awaiting_readback',result_receipt_id=?
		WHERE turn_id=? AND session_id=? AND generation_epoch=? AND trigger_kind='clarification_reply'
		AND delegation_id=? AND state='accepted' AND result_receipt_id IS NULL`,
		input.OperationID, string(input.ReplyTurnID), string(input.SessionID), input.GenerationEpoch, string(input.DelegationID))
	if err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	rows, err := updated.RowsAffected()
	if err != nil || rows != 1 {
		return ClarificationReplyContinuationCommit{}, ErrClarificationReplyConflict
	}
	nextHandoff := newHandoff
	result := ClarificationReplyContinuationCommit{
		OperationID: input.OperationID, OwnerID: input.OwnerID, SessionID: input.SessionID, ReplyTurnID: input.ReplyTurnID,
		ReplyClientSubmissionID: reply.Reply.ClientSubmissionID,
		PromptMessageID:         input.PromptMessageID, DelegationID: input.DelegationID,
		Revision: input.Revision, NextRevision: input.Revision + 1, ResultState: "awaiting_readback",
		InvocationReceipt: minimalClarificationInvocationReceipt(input.InvocationReceipt), ContextEvent: contextEvent, Event: primary,
		ReceiptEvent: receiptEvent, Message: message, Events: []modulecore.EventEnvelope{contextEvent, primary, receiptEvent},
		MessageIndexes: []MessageIndex{message}, NextHandoff: &nextHandoff, DispatchQueued: true,
	}
	if err := validateClarificationReplyContinuationLinks(result, reply); err != nil {
		return ClarificationReplyContinuationCommit{}, err
	}
	return result, nil
}

func minimalClarificationInvocationReceipt(receipt agent.InvocationReceipt) agent.InvocationReceipt {
	return agent.InvocationReceipt{RequestID: receipt.RequestID, ResponseID: receipt.ResponseID, Status: receipt.Status}
}

func extendDelegationContextWithUserAnswer(oldContext modulecore.EventEnvelope, messages []DelegationContextMessage, answerID modulecore.MessageID, answerText string) ([]DelegationContextMessage, string, error) {
	if len(messages) == 0 || len(messages) >= 256 || answerID.Validate() != nil || strings.TrimSpace(answerText) == "" {
		return nil, "", errors.New("user answer cannot be appended to the stored delegation context")
	}
	for _, message := range messages {
		if message.MessageID == answerID {
			return nil, "", errors.New("user answer is already present in the stored delegation context")
		}
	}
	updated := append(append([]DelegationContextMessage(nil), messages...), DelegationContextMessage{MessageID: answerID, Role: "user", Text: answerText})
	sourceRevision, ok1 := oldContext.Payload["source_revision"].(string)
	cutoverRevision, ok2 := oldContext.Payload["cutover_revision"].(string)
	if !ok1 || !ok2 {
		return nil, "", errors.New("stored delegation context revision metadata is invalid")
	}
	encoded, err := json.Marshal(struct {
		SourceRevision  string                     `json:"source_revision"`
		CutoverRevision string                     `json:"cutover_revision"`
		Messages        []DelegationContextMessage `json:"messages"`
	}{SourceRevision: sourceRevision, CutoverRevision: cutoverRevision, Messages: updated})
	if err != nil || len(encoded) > maxDelegationContextLen {
		return nil, "", errors.New("user-reply delegation context exceeds its stored size bound")
	}
	digest := sha256.Sum256(encoded)
	return updated, hex.EncodeToString(digest[:]), nil
}

func clarificationReplyReceiptEvent(input ClarificationReplyContinuationInput, answer modulecore.EventEnvelope) modulecore.EventEnvelope {
	payload := map[string]any{"status": input.InvocationReceipt.Status}
	if input.InvocationReceipt.RequestID != "" {
		payload["request_id"] = input.InvocationReceipt.RequestID
	}
	if input.InvocationReceipt.ResponseID != "" {
		payload["response_id"] = input.InvocationReceipt.ResponseID
	}
	event := modulecore.NewEventEnvelope(answer.TraceID, answer.EventID, nil,
		"conversation_runtime", "conversation.mio_emission_receipt", input.OccurredAt.UTC(), payload)
	event.EventID = input.ReceiptEventID
	event.SessionID, event.TurnID = input.SessionID, input.ReplyTurnID
	return event
}

func validateClarificationReplyContinuationInput(input ClarificationReplyContinuationInput) error {
	if strings.TrimSpace(input.OperationID) == "" || input.OperationID != strings.TrimSpace(input.OperationID) {
		return errors.New("clarification reply continuation operation_id is required and must be trimmed")
	}
	if strings.TrimSpace(input.OwnerID) == "" || input.OwnerID != strings.TrimSpace(input.OwnerID) ||
		input.SessionID.Validate() != nil || input.ReplyTurnID.Validate() != nil || input.PromptMessageID.Validate() != nil || input.DelegationID.Validate() != nil {
		return errors.New("clarification reply continuation identity is invalid")
	}
	if input.Revision == 0 || input.Revision >= uint64(math.MaxInt64) || input.GenerationEpoch <= 0 {
		return errors.New("clarification reply continuation revision and generation epoch are invalid")
	}
	if strings.TrimSpace(input.Instruction) == "" || len([]byte(input.Instruction)) > conversation.MaxInstructionBytes {
		return errors.New("clarification reply continuation instruction is empty or oversized")
	}
	if input.EventID.Validate() != nil || input.MessageID.Validate() != nil || input.ContextEventID.Validate() != nil ||
		input.ReceiptEventID.Validate() != nil || input.EventID == input.ContextEventID || input.EventID == input.ReceiptEventID ||
		input.ContextEventID == input.ReceiptEventID || input.OccurredAt.IsZero() {
		return errors.New("clarification reply continuation event identity is invalid")
	}
	if err := validateClarificationInvocationReceipt(input.InvocationReceipt); err != nil || input.InvocationReceipt.Status != "completed" {
		return errors.New("clarification reply continuation invocation receipt is invalid")
	}
	return nil
}

func clarificationReplyContinuationInputHash(input ClarificationReplyContinuationInput) (string, error) {
	return hashJSON(struct {
		OwnerID           string               `json:"owner_id"`
		SessionID         modulecore.SessionID `json:"session_id"`
		ReplyTurnID       modulecore.TurnID    `json:"reply_turn_id"`
		PromptMessageID   modulecore.MessageID `json:"prompt_message_id"`
		DelegationID      modulecore.RequestID `json:"delegation_id"`
		Revision          uint64               `json:"revision"`
		GenerationEpoch   int64                `json:"generation_epoch"`
		Instruction       string               `json:"instruction"`
		InvocationReceipt struct {
			RequestID  string `json:"request_id"`
			ResponseID string `json:"response_id"`
			Status     string `json:"status"`
		} `json:"invocation_receipt"`
	}{input.OwnerID, input.SessionID, input.ReplyTurnID, input.PromptMessageID, input.DelegationID, input.Revision,
		input.GenerationEpoch, input.Instruction, struct {
			RequestID  string `json:"request_id"`
			ResponseID string `json:"response_id"`
			Status     string `json:"status"`
		}{input.InvocationReceipt.RequestID, input.InvocationReceipt.ResponseID, input.InvocationReceipt.Status}})
}

func validateStoredClarificationReplyContinuationCommit(result ClarificationReplyContinuationCommit, input ClarificationReplyContinuationInput) error {
	if result.OperationID != input.OperationID || result.OwnerID != input.OwnerID || result.SessionID != input.SessionID || result.ReplyTurnID != input.ReplyTurnID ||
		strings.TrimSpace(result.ReplyClientSubmissionID) == "" || result.ReplyClientSubmissionID != strings.TrimSpace(result.ReplyClientSubmissionID) ||
		result.PromptMessageID != input.PromptMessageID || result.DelegationID != input.DelegationID || result.Revision != input.Revision ||
		result.NextRevision != input.Revision+1 || result.ResultState != "awaiting_readback" || !result.DispatchQueued ||
		result.InvocationReceipt != minimalClarificationInvocationReceipt(input.InvocationReceipt) || result.NextHandoff == nil ||
		result.NextHandoff.DelegationID != input.DelegationID || result.NextHandoff.Revision != input.Revision+1 ||
		result.Event.EventType != "conversation.delegation_instruction" || result.Event.ComponentID != "delegation_mailbox" ||
		result.Event.SessionID != input.SessionID || result.Event.RequestID != input.DelegationID ||
		result.Message.MessageID != result.Event.MessageID || result.Message.EventID != result.Event.EventID ||
		result.Message.Visibility != "private" || result.Message.ToKind != "agent" || result.Message.ToActor != result.NextHandoff.ToActor ||
		len(result.Events) != 3 || len(result.MessageIndexes) != 1 || result.MessageIndexes[0] != result.Message ||
		!reflect.DeepEqual(result.Events[0], result.ContextEvent) || !reflect.DeepEqual(result.Events[1], result.Event) ||
		!reflect.DeepEqual(result.Events[2], result.ReceiptEvent) ||
		result.ReceiptEvent.EventType != "conversation.mio_emission_receipt" || result.ReceiptEvent.ComponentID != "conversation_runtime" ||
		result.ReceiptEvent.SessionID != input.SessionID || result.ReceiptEvent.TurnID != input.ReplyTurnID ||
		result.ReceiptEvent.CausationEventID == "" ||
		result.ReceiptEvent.MessageID != "" || result.ReceiptEvent.RequestID != "" || result.ReceiptEvent.ActorKind != "" || result.ReceiptEvent.ActorID != "" ||
		payloadString(result.ReceiptEvent.Payload, "status") != result.InvocationReceipt.Status ||
		payloadString(result.ReceiptEvent.Payload, "request_id") != result.InvocationReceipt.RequestID ||
		payloadString(result.ReceiptEvent.Payload, "response_id") != result.InvocationReceipt.ResponseID {
		return errors.New("stored clarification reply continuation receipt is inconsistent")
	}
	if result.Event.EventSeq <= 0 || result.ContextEvent.EventSeq <= 0 || result.ReceiptEvent.EventSeq <= 0 ||
		result.ContextEvent.EventType != "conversation.delegation_context" || result.ContextEvent.ComponentID != "conversation_runtime" ||
		result.ContextEvent.SessionID != input.SessionID || result.ContextEvent.RequestID != input.DelegationID ||
		result.ContextEvent.TraceID != result.NextHandoff.SourceTraceID || result.Event.TraceID != result.NextHandoff.SourceTraceID ||
		result.Event.CausationEventID != result.ContextEvent.EventID ||
		payloadString(result.Event.Payload, "delegation_id") != string(input.DelegationID) ||
		payloadUint64Field(result.Event.Payload, "delegation_revision") != input.Revision+1 ||
		payloadString(result.Event.Payload, "visibility") != "private" || payloadString(result.Event.Payload, "to") != result.NextHandoff.ToActor {
		return errors.New("stored clarification reply continuation event identity is inconsistent")
	}
	return nil
}

func validateClarificationReplyContinuationLinks(result ClarificationReplyContinuationCommit, reply ClarificationReplyContext) error {
	if result.Event.TraceID != reply.Handoff.SourceTraceID || result.ContextEvent.TraceID != reply.Handoff.SourceTraceID ||
		result.Event.CausationEventID != result.ContextEvent.EventID ||
		result.ReceiptEvent.TraceID != reply.AnswerEvent.TraceID || result.ReceiptEvent.CausationEventID != reply.AnswerEvent.EventID {
		return errors.New("clarification reply continuation does not preserve prompt and answer trace lineage")
	}
	return nil
}

func (s *SQLiteStore) LoadClarificationReplyContinuationOutcome(ctx context.Context, replyTurnID modulecore.TurnID) (ClarificationReplyContinuationCommit, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return ClarificationReplyContinuationCommit{}, false, err
	}
	if ctx == nil || replyTurnID.Validate() != nil {
		return ClarificationReplyContinuationCommit{}, false, errors.New("clarification reply outcome requires context and reply Turn ID")
	}
	var receiptID sql.NullString
	err := s.db.QueryRowContext(ctx, `SELECT result_receipt_id FROM live_conversation_turn WHERE turn_id=? AND trigger_kind='clarification_reply'`, string(replyTurnID)).Scan(&receiptID)
	if errors.Is(err, sql.ErrNoRows) || (err == nil && !receiptID.Valid) {
		return ClarificationReplyContinuationCommit{}, false, nil
	}
	if err != nil {
		return ClarificationReplyContinuationCommit{}, false, err
	}
	var operationKind, resultJSON string
	if err := s.db.QueryRowContext(ctx, `SELECT operation_kind,result_json FROM live_operation_receipt WHERE operation_id=?`, receiptID.String).Scan(&operationKind, &resultJSON); err != nil {
		return ClarificationReplyContinuationCommit{}, false, err
	}
	if operationKind != "commit_clarification_reply_continuation" {
		return ClarificationReplyContinuationCommit{}, false, errors.New("clarification reply Turn references a different receipt kind")
	}
	var result ClarificationReplyContinuationCommit
	if err := json.Unmarshal([]byte(resultJSON), &result); err != nil {
		return ClarificationReplyContinuationCommit{}, false, fmt.Errorf("decode clarification reply continuation outcome: %w", err)
	}
	if result.OperationID != receiptID.String || result.ReplyTurnID != replyTurnID ||
		validateStoredClarificationReplyContinuationCommit(result, ClarificationReplyContinuationInput{
			OperationID: result.OperationID, OwnerID: result.OwnerID, SessionID: result.SessionID, ReplyTurnID: result.ReplyTurnID,
			PromptMessageID: result.PromptMessageID, DelegationID: result.DelegationID, Revision: result.Revision,
			InvocationReceipt: result.InvocationReceipt,
		}) != nil {
		return ClarificationReplyContinuationCommit{}, false, errors.New("stored clarification reply continuation identity is inconsistent")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ClarificationReplyContinuationCommit{}, false, err
	}
	defer tx.Rollback()
	reply, err := loadCommittedClarificationReplyContextTx(ctx, tx, ClarificationReplyContextInput{
		SessionID: result.SessionID, ReplyTurnID: replyTurnID, OwnerID: result.OwnerID,
	})
	if err != nil {
		return ClarificationReplyContinuationCommit{}, false, fmt.Errorf("load committed clarification reply context: %w", err)
	}
	if reply.Receipt.PromptMessageID != result.PromptMessageID || reply.Receipt.DelegationID != result.DelegationID ||
		reply.Receipt.Revision != result.Revision || reply.Reply.TurnID != result.ReplyTurnID ||
		reply.Reply.ClientSubmissionID != result.ReplyClientSubmissionID ||
		reply.Reply.ClientSubmissionID == "" || reply.Reply.ClientSubmissionID != strings.TrimSpace(reply.Reply.ClientSubmissionID) ||
		validateClarificationReplyContinuationLinks(result, reply) != nil {
		return ClarificationReplyContinuationCommit{}, false, errors.New("stored clarification reply continuation does not match its prompt and answer")
	}
	if err := tx.Commit(); err != nil {
		return ClarificationReplyContinuationCommit{}, false, err
	}
	return result, true, nil
}
