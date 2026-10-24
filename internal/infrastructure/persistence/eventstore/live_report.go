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

var (
	ErrReportConflict         = errors.New("delegation revision or state conflicts with report")
	ErrStaleReportRevision    = errors.New("accepted report revision changed before reply commit")
	ErrReportAssigneeMismatch = errors.New("authenticated report sender does not match delegation assignee")
	ErrReportTaskMismatch     = errors.New("report Task does not match the bound delegation Task")
)

type staleReportRevisionConflict struct {
	acceptedRevision uint64
	currentRevision  int64
}

func (e *staleReportRevisionConflict) Error() string {
	return fmt.Sprintf("%v: accepted report_revision %d, current report_revision %d", ErrReportConflict, e.acceptedRevision, e.currentRevision)
}

func (e *staleReportRevisionConflict) Unwrap() error { return ErrReportConflict }

func (e *staleReportRevisionConflict) Is(target error) bool { return target == ErrStaleReportRevision }

// ReportInput carries a Task-owner report and trusted identity metadata. Only
// the trusted Task bridge may call AcceptReport: TrustedSender must be copied
// from its authenticated identity, and the bridge must verify the Task-owner
// completion receipt, current/latest Run, and report/Task status consistency.
// EventStore independently checks the current delegation, bound Task, and
// assigned sender because those bindings are owned here.
//
// EventID, MessageID, TurnID, TraceID, and OccurredAt are runtime-issued
// envelope metadata. They may change on retry because the durable receipt
// returns the first committed accepted Turn.
type ReportInput struct {
	Report        conversation.ReportEnvelope
	TrustedSender string
	EventID       modulecore.EventID
	MessageID     modulecore.MessageID
	TurnID        modulecore.TurnID
	TraceID       modulecore.TraceID
	OccurredAt    time.Time
}

type storedReportBinding struct {
	delegation storedReadbackDelegation
	workTaskID sql.NullString
	reportRev  int64
}

// AcceptReport atomically persists a private agent report event, its accepted
// Mio reply Turn, the delegation report revision/state, and the idempotency
// receipt. Task-owner completion and this EventStore commit remain separate
// owner transactions; the Task bridge can safely replay pending reports.
func (s *SQLiteStore) AcceptReport(ctx context.Context, input ReportInput) (AcceptedTurn, error) {
	if err := s.ensureOpen(); err != nil {
		return AcceptedTurn{}, err
	}
	if ctx == nil {
		return AcceptedTurn{}, errors.New("report context is required")
	}
	if err := validateReportInput(input); err != nil {
		return AcceptedTurn{}, err
	}
	input.Report.Sender = input.TrustedSender
	inputHash, err := reportInputHash(input)
	if err != nil {
		return AcceptedTurn{}, err
	}
	operationID := reportAcceptanceOperationID(input.Report.ReportID)
	submissionID := reportSubmissionID(input.Report.ReportID)
	if s.shouldQueueWrite(ctx) {
		queuedInput := cloneQueuedValue(input)
		return enqueueOrderedWriteResult(s, ctx, 1, queuedInput, func(commandCtx context.Context) (AcceptedTurn, error) {
			return s.AcceptReport(commandCtx, queuedInput)
		})
	}

	tx, unlockSequence, err := s.beginSequenceTx(ctx)
	if err != nil {
		return AcceptedTurn{}, err
	}
	defer unlockSequence()
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	// Check the receipt before current state. An exact replay remains valid
	// after the first acceptance has closed this delegation revision.
	if receipt, found, err := readOperationReceipt(ctx, tx, operationID); err != nil {
		return AcceptedTurn{}, err
	} else if found {
		if receipt.operationKind != "accept_report" || receipt.inputSHA256 != inputHash {
			return AcceptedTurn{}, ErrOperationConflict
		}
		var accepted AcceptedTurn
		if err := json.Unmarshal([]byte(receipt.resultJSON), &accepted); err != nil {
			return AcceptedTurn{}, fmt.Errorf("decode accepted report receipt: %w", err)
		}
		if err := validateStoredReportAcceptance(accepted, input, operationID, submissionID); err != nil {
			return AcceptedTurn{}, err
		}
		if err := tx.Commit(); err != nil {
			return AcceptedTurn{}, err
		}
		committed = true
		return accepted, nil
	}

	binding, err := loadReportBinding(ctx, tx, input.Report.DelegationID, input.Report.DelegationRevision)
	if err != nil {
		return AcceptedTurn{}, err
	}
	if input.TrustedSender != binding.delegation.assignee {
		return AcceptedTurn{}, ErrReportAssigneeMismatch
	}
	if !binding.workTaskID.Valid || input.Report.WorkTaskID.String() != binding.workTaskID.String {
		return AcceptedTurn{}, fmt.Errorf("%w: report task %q, bound task %q", ErrReportTaskMismatch, input.Report.WorkTaskID, binding.workTaskID.String)
	}
	var currentRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id = ?`, string(input.Report.DelegationID)).Scan(&currentRevision); err != nil {
		return AcceptedTurn{}, err
	}
	if currentRevision != binding.delegation.revision || binding.delegation.delegationState != "accepted" ||
		binding.delegation.turnState != "completed" || !binding.delegation.turnDelegationID.Valid ||
		binding.delegation.turnDelegationID.String != string(input.Report.DelegationID) ||
		binding.reportRev < 0 || uint64(binding.reportRev) == math.MaxUint64 ||
		input.Report.ReportRevision != uint64(binding.reportRev)+1 {
		return AcceptedTurn{}, ErrReportConflict
	}

	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(binding.delegation.contextEventID))
	if err != nil {
		return AcceptedTurn{}, fmt.Errorf("load private handoff context for report: %w", err)
	}
	if input.TraceID == contextEvent.TraceID {
		return AcceptedTurn{}, errors.New("report reply must use a trace separate from its origin handoff")
	}
	handoff, _, err := validateStoredHandoff(ctx, tx, binding.delegation, contextEvent, ReadbackInput{
		DelegationID: input.Report.DelegationID,
		Revision:     input.Report.DelegationRevision,
		Assignee:     input.TrustedSender,
	})
	if err != nil {
		return AcceptedTurn{}, err
	}
	if err := conversation.ValidateReportEnvelope(input.Report, handoff); err != nil {
		return AcceptedTurn{}, fmt.Errorf("validate report envelope: %w", err)
	}

	event := makeReportEvent(input, binding, contextEvent.TraceID)
	message := MessageIndex{
		MessageID: event.MessageID, EventID: event.EventID, SessionID: event.SessionID,
		TurnID: event.TurnID, FromActor: input.TrustedSender, ToKind: "agent", ToActor: "mio",
		Kind: "agent_report", Visibility: "private",
	}
	if err := validateMessageIndex(message, event, event.SessionID, event.TurnID); err != nil {
		return AcceptedTurn{}, fmt.Errorf("validate report message index: %w", err)
	}

	var ownerID string
	var epoch int64
	if err := tx.QueryRowContext(ctx, `SELECT owner_id, epoch FROM live_conversation_session WHERE session_id = ?`, string(event.SessionID)).Scan(&ownerID, &epoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AcceptedTurn{}, ErrConversationSessionConflict
		}
		return AcceptedTurn{}, err
	}
	if ownerID != binding.delegation.sessionOwnerID {
		return AcceptedTurn{}, ErrConversationSessionConflict
	}

	event.EventSeq, err = s.nextSequence(ctx)
	if err != nil {
		return AcceptedTurn{}, err
	}
	if err := ensureEventSeqAvailable(ctx, tx, event.EventSeq); err != nil {
		return AcceptedTurn{}, err
	}
	if err := ensureEventIDAbsent(ctx, tx, event.EventID); err != nil {
		return AcceptedTurn{}, err
	}
	if err := s.validateReferences(ctx, tx, event); err != nil {
		return AcceptedTurn{}, err
	}
	event, err = insertLiveEvent(ctx, tx, event)
	if err != nil {
		return AcceptedTurn{}, err
	}
	if err := insertMessageIndex(ctx, tx, message); err != nil {
		return AcceptedTurn{}, err
	}

	accepted := AcceptedTurn{
		OperationID: operationID, ClientSubmissionID: submissionID,
		SessionID: event.SessionID, TurnID: event.TurnID, ActorID: input.TrustedSender,
		TriggerKind: "report_reply", InputMessageID: event.MessageID,
		AdmissionSeq: event.EventSeq, GenerationEpoch: epoch, State: "accepted", InputEvent: event,
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO live_conversation_turn
			(turn_id, session_id, owner_id, actor_id, trigger_kind, client_submission_id,
			 submission_sha256, accept_receipt_id, input_message_id, admission_seq, state, generation_epoch,
			 source_turn_id, delegation_id)
		VALUES (?, ?, ?, ?, 'report_reply', ?, ?, ?, ?, ?, 'accepted', ?, ?, ?)`,
		string(accepted.TurnID), string(accepted.SessionID), ownerID, input.TrustedSender,
		submissionID, inputHash, operationID, string(event.MessageID), int64(event.EventSeq), epoch,
		binding.delegation.originTurnID, string(input.Report.DelegationID),
	); err != nil {
		return AcceptedTurn{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE live_conversation_session SET admission_seq = ? WHERE session_id = ?`, int64(event.EventSeq), string(event.SessionID)); err != nil {
		return AcceptedTurn{}, err
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_delegation SET state = ?, report_revision = ?
		WHERE delegation_id = ? AND revision = ? AND state = 'accepted' AND report_revision = ?`,
		string(input.Report.Status), int64(input.Report.ReportRevision), string(input.Report.DelegationID),
		int64(input.Report.DelegationRevision), binding.reportRev,
	)
	if err != nil {
		return AcceptedTurn{}, err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return AcceptedTurn{}, fmt.Errorf("check report delegation update: %w", err)
	}
	if updated != 1 {
		return AcceptedTurn{}, ErrReportConflict
	}
	if err := insertOperationReceipt(ctx, tx, operationID, "accept_report", inputHash, accepted); err != nil {
		return AcceptedTurn{}, err
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE live_work_outbox SET state='completed'
		WHERE dedupe_key=? AND kind='execute_delegated_task' AND session_id=?
		  AND state IN ('pending','leased','needs_attention','completed')
		  AND EXISTS (
			SELECT 1 FROM live_delegation d
			WHERE d.delegation_id=? AND d.revision=? AND d.state=?
			  AND d.report_revision=? AND d.work_task_id=?
		  AND d.revision=(SELECT MAX(d2.revision) FROM live_delegation d2 WHERE d2.delegation_id=d.delegation_id)
		  )`,
		delegationExecuteDedupeKey(input.Report.DelegationID, input.Report.DelegationRevision),
		string(event.SessionID), string(input.Report.DelegationID), int64(input.Report.DelegationRevision),
		string(input.Report.Status), int64(input.Report.ReportRevision), string(input.Report.WorkTaskID),
	)
	if err != nil {
		return AcceptedTurn{}, err
	}
	updated, err = result.RowsAffected()
	if err != nil {
		return AcceptedTurn{}, fmt.Errorf("check delegated task work completion: %w", err)
	}
	if updated != 1 {
		return AcceptedTurn{}, ErrDelegationWorkConflict
	}
	if err := tx.Commit(); err != nil {
		return AcceptedTurn{}, err
	}
	committed = true
	s.observeCommittedOrchestratorEvent(accepted.InputEvent)
	return accepted, nil
}

func validateReportInput(input ReportInput) error {
	if err := input.Report.ReportID.Validate(); err != nil {
		return fmt.Errorf("report_id: %w", err)
	}
	if err := input.Report.DelegationID.Validate(); err != nil {
		return fmt.Errorf("delegation_id: %w", err)
	}
	if input.Report.ReportRevision == 0 || input.Report.ReportRevision > math.MaxInt64 ||
		input.Report.DelegationRevision == 0 || input.Report.DelegationRevision > math.MaxInt64 {
		return errors.New("report and delegation revisions must be positive and fit stored revision columns")
	}
	if strings.TrimSpace(input.TrustedSender) == "" || input.TrustedSender != strings.TrimSpace(input.TrustedSender) {
		return errors.New("authenticated report sender is required and must be trimmed")
	}
	if err := input.EventID.Validate(); err != nil {
		return fmt.Errorf("event_id: %w", err)
	}
	if err := input.MessageID.Validate(); err != nil {
		return fmt.Errorf("message_id: %w", err)
	}
	if err := input.TurnID.Validate(); err != nil {
		return fmt.Errorf("turn_id: %w", err)
	}
	if err := input.TraceID.Validate(); err != nil {
		return fmt.Errorf("trace_id: %w", err)
	}
	if input.OccurredAt.IsZero() {
		return errors.New("report occurred_at is required")
	}
	return nil
}

func reportInputHash(input ReportInput) (string, error) {
	material := struct {
		Report        conversation.ReportEnvelope `json:"report"`
		TrustedSender string                      `json:"trusted_sender"`
	}{Report: input.Report, TrustedSender: input.TrustedSender}
	return hashJSON(material)
}

func loadReportBinding(ctx context.Context, tx *sql.Tx, delegationID modulecore.RequestID, revision uint64) (storedReportBinding, error) {
	delegation, err := loadReadbackDelegation(ctx, tx, delegationID, revision)
	if err != nil {
		if errors.Is(err, ErrReadbackConflict) {
			return storedReportBinding{}, ErrReportConflict
		}
		return storedReportBinding{}, err
	}
	binding := storedReportBinding{delegation: delegation}
	err = tx.QueryRowContext(ctx, `
		SELECT work_task_id, report_revision FROM live_delegation
		WHERE delegation_id = ? AND revision = ?`, string(delegationID), int64(revision),
	).Scan(&binding.workTaskID, &binding.reportRev)
	if errors.Is(err, sql.ErrNoRows) {
		return storedReportBinding{}, ErrReportConflict
	}
	if err != nil {
		return storedReportBinding{}, err
	}
	return binding, nil
}

func makeReportEvent(input ReportInput, binding storedReportBinding, originTraceID modulecore.TraceID) modulecore.EventEnvelope {
	report := input.Report
	// Sender is runtime-bound metadata and intentionally omitted from the
	// stored report body. The authoritative actor identity is envelope data.
	report.Sender = ""
	payload := map[string]any{
		"kind": "agent_report", "visibility": "private", "from": input.TrustedSender,
		"source_actor_id": input.TrustedSender, "to": "mio", "text": report.Summary,
		"trusted_sender":    input.TrustedSender,
		"origin_trace_id":   string(originTraceID),
		"origin_turn_id":    binding.delegation.originTurnID,
		"origin_message_id": binding.delegation.inputMessageID,
		"delegation_id":     string(report.DelegationID), "delegation_revision": report.DelegationRevision,
		"report_id": string(report.ReportID), "report_revision": report.ReportRevision,
		"work_task_id": string(report.WorkTaskID), "work_run_id": string(report.WorkRunID),
		"report": report,
	}
	return modulecore.EventEnvelope{
		SchemaVersion: modulecore.EventEnvelopeSchemaVersion,
		EventID:       input.EventID, TraceID: input.TraceID,
		EventType: "conversation.agent_report", ComponentID: "conversation_runtime",
		ActorKind: "agent", ActorID: input.TrustedSender, OccurredAt: input.OccurredAt.UTC(),
		SessionID: modulecore.SessionID(binding.delegation.sessionID), TurnID: input.TurnID,
		RequestID: report.DelegationID, ResponseID: report.ReportID,
		TaskID: report.WorkTaskID, RunID: report.WorkRunID, MessageID: input.MessageID,
		Payload: payload,
	}
}

func validateStoredReportAcceptance(accepted AcceptedTurn, input ReportInput, operationID, submissionID string) error {
	if accepted.OperationID != operationID || accepted.ClientSubmissionID != submissionID ||
		accepted.TriggerKind != "report_reply" || accepted.State != "accepted" ||
		accepted.ActorID != input.TrustedSender || accepted.TurnID == "" || accepted.SessionID == "" ||
		accepted.InputEvent.EventType != "conversation.agent_report" || accepted.InputEvent.ComponentID != "conversation_runtime" ||
		accepted.InputEvent.ActorKind != "agent" || accepted.InputEvent.ActorID != input.TrustedSender ||
		accepted.InputEvent.ResponseID != input.Report.ReportID || accepted.InputEvent.RequestID != input.Report.DelegationID ||
		accepted.InputEvent.MessageID != accepted.InputMessageID || accepted.InputEvent.TurnID != accepted.TurnID ||
		accepted.InputEvent.SessionID != accepted.SessionID || accepted.InputEvent.CausationEventID != "" ||
		len(accepted.InputEvent.DependencyEventIDs) != 0 {
		return errors.New("stored report acceptance receipt is inconsistent")
	}
	message := MessageIndex{
		MessageID: accepted.InputMessageID, EventID: accepted.InputEvent.EventID,
		SessionID: accepted.SessionID, TurnID: accepted.TurnID,
		FromActor: input.TrustedSender, ToKind: "agent", ToActor: "mio",
		Kind: "agent_report", Visibility: "private",
	}
	if err := validateRecoverableTurnActor(accepted, operationID, submissionID, accepted.InputEvent, message); err != nil {
		return fmt.Errorf("stored report acceptance payload is inconsistent: %w", err)
	}
	return nil
}

// validateReportReplyCommitFence prevents Mio from committing a user-facing
// answer after the accepted report has been canceled or superseded. It runs
// in the same transaction as CommitTurnBundle. A committed-bundle receipt is
// checked before this fence, so an exact retry of an already committed reply
// still returns its durable result.
func validateReportReplyCommitFence(ctx context.Context, tx *sql.Tx, turnID modulecore.TurnID, sessionID modulecore.SessionID) error {
	var actorID, triggerKind, submissionID, operationID, inputMessageID string
	var message MessageIndex
	var replyTo sql.NullString
	var eventID, traceID, schemaVersion, eventType, componentID, occurredAt, envelope string
	var eventSeq int64
	err := tx.QueryRowContext(ctx, `
		SELECT t.actor_id, t.trigger_kind, t.client_submission_id, t.accept_receipt_id, t.input_message_id,
		       e.event_id, e.event_seq, e.trace_id, e.schema_version, e.event_type, e.component_id, e.occurred_at, e.envelope_json,
		       m.message_id, m.event_id, m.session_id, m.turn_id, m.from_actor, m.to_kind, m.to_actor,
		       m.kind, m.reply_to_message_id, m.visibility
		FROM live_conversation_turn t
		JOIN live_message_index m ON m.message_id = t.input_message_id
		JOIN event_envelope e ON e.event_id = m.event_id
		WHERE t.turn_id = ? AND t.session_id = ?`, string(turnID), string(sessionID),
	).Scan(
		&actorID, &triggerKind, &submissionID, &operationID, &inputMessageID,
		&eventID, &eventSeq, &traceID, &schemaVersion, &eventType, &componentID, &occurredAt, &envelope,
		&message.MessageID, &message.EventID, &message.SessionID, &message.TurnID, &message.FromActor,
		&message.ToKind, &message.ToActor, &message.Kind, &replyTo, &message.Visibility,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReportConflict
		}
		return err
	}
	event, err := decodeStoredEnvelope(eventID, eventSeq, traceID, schemaVersion, eventType, componentID, occurredAt, envelope)
	if err != nil {
		return fmt.Errorf("decode accepted report input for commit fence: %w", err)
	}
	message.EventID = modulecore.EventID(message.EventID)
	message.ReplyToMessageID = modulecore.MessageID(replyTo.String)
	accepted := AcceptedTurn{TurnID: turnID, TriggerKind: triggerKind, ActorID: actorID, InputMessageID: modulecore.MessageID(inputMessageID)}
	if err := validateRecoverableTurnActor(accepted, operationID, submissionID, event, message); err != nil {
		return fmt.Errorf("validate accepted report input for commit fence: %w", err)
	}
	if triggerKind != "report_reply" {
		return nil
	}

	delegationID := payloadString(event.Payload, "delegation_id")
	delegationRevision, okRevision := payloadUint64(event.Payload["delegation_revision"])
	reportRevision, okReportRevision := payloadUint64(event.Payload["report_revision"])
	report, reportOK := event.Payload["report"].(map[string]any)
	reportStatus := payloadString(report, "status")
	if !reportOK || delegationID == "" || delegationID != string(event.RequestID) || !okRevision || delegationRevision == 0 ||
		!okReportRevision || reportRevision == 0 || reportStatus == "" ||
		payloadString(report, "delegation_id") != delegationID ||
		payloadUint64Field(report, "delegation_revision") != delegationRevision ||
		payloadString(report, "report_id") != string(event.ResponseID) ||
		payloadUint64Field(report, "report_revision") != reportRevision ||
		payloadString(report, "work_task_id") != string(event.TaskID) ||
		payloadString(report, "work_run_id") != string(event.RunID) ||
		payloadString(event.Payload, "work_task_id") != string(event.TaskID) ||
		payloadString(event.Payload, "work_run_id") != string(event.RunID) {
		return ErrReportConflict
	}

	var storedState, assignee, workTaskID string
	var storedRevision, storedReportRevision, currentRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT revision, state, COALESCE(work_task_id, ''), report_revision, assignee,
		       (SELECT MAX(revision) FROM live_delegation WHERE delegation_id = d.delegation_id)
		FROM live_delegation d WHERE delegation_id = ? AND revision = ?`, delegationID, int64(delegationRevision),
	).Scan(&storedRevision, &storedState, &workTaskID, &storedReportRevision, &assignee, &currentRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrReportConflict
		}
		return err
	}
	if storedReportRevision < 0 || uint64(storedReportRevision) != reportRevision {
		return &staleReportRevisionConflict{acceptedRevision: reportRevision, currentRevision: storedReportRevision}
	}
	if storedRevision != int64(delegationRevision) || currentRevision != storedRevision ||
		storedState != reportStatus ||
		workTaskID != string(event.TaskID) || assignee != actorID {
		return ErrReportConflict
	}
	return nil
}

type staleReportQuarantineReceipt struct {
	TurnID                 modulecore.TurnID    `json:"turn_id"`
	SessionID              modulecore.SessionID `json:"session_id"`
	State                  string               `json:"state"`
	Reason                 string               `json:"reason"`
	AcceptedReportRevision uint64               `json:"accepted_report_revision"`
	CurrentReportRevision  int64                `json:"current_report_revision"`
}

func quarantineStaleReportTurn(ctx context.Context, tx *sql.Tx, turnID modulecore.TurnID, sessionID modulecore.SessionID, stale *staleReportRevisionConflict) error {
	operationID := staleReportQuarantineOperationID(turnID)
	receipt := staleReportQuarantineReceipt{
		TurnID: turnID, SessionID: sessionID, State: "quarantined", Reason: "report_revision_changed",
		AcceptedReportRevision: stale.acceptedRevision, CurrentReportRevision: stale.currentRevision,
	}
	inputHash, err := hashJSON(receipt)
	if err != nil {
		return fmt.Errorf("hash stale report quarantine receipt: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE live_conversation_turn SET state = 'quarantined'
		WHERE turn_id = ? AND session_id = ? AND trigger_kind = 'report_reply' AND state = 'accepted'`,
		string(turnID), string(sessionID),
	)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("check stale report turn quarantine: %w", err)
	}
	if updated != 1 {
		return ErrConversationTurnConflict
	}
	if err := insertOperationReceipt(ctx, tx, operationID, "quarantine_stale_report", inputHash, receipt); err != nil {
		return err
	}
	return nil
}

func staleReportQuarantineOperationID(turnID modulecore.TurnID) string {
	return "quarantine-stale-report:" + string(turnID)
}

func payloadUint64Field(payload map[string]any, key string) uint64 {
	value, ok := payloadUint64(payload[key])
	if !ok {
		return 0
	}
	return value
}

func payloadUint64(value any) (uint64, bool) {
	switch typed := value.(type) {
	case uint64:
		return typed, true
	case uint:
		return uint64(typed), true
	case int:
		if typed >= 0 {
			return uint64(typed), true
		}
	case int64:
		if typed >= 0 {
			return uint64(typed), true
		}
	case float64:
		if typed >= 0 && typed <= math.MaxInt64 && math.Trunc(typed) == typed {
			return uint64(typed), true
		}
	case json.Number:
		parsed, err := typed.Int64()
		if err == nil && parsed >= 0 {
			return uint64(parsed), true
		}
	}
	return 0, false
}

func reportAcceptanceOperationID(reportID modulecore.ResponseID) string {
	return "accept_report:" + string(reportID)
}

func reportSubmissionID(reportID modulecore.ResponseID) string {
	return "report:" + string(reportID)
}
