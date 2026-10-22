package eventstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// DelegatedExecutionSourceInput bounds accepted execution-source reads to one
// current execute-work lease and its trusted assignee.
type DelegatedExecutionSourceInput struct {
	WorkLeaseInput
	SessionID modulecore.SessionID
	Assignee  string
}

// DelegatedExecutionSource contains only the current accepted handoff,
// accepted readback, public authorized context, and bound Task identity.
// TaskManager remains the owner of the Run itself.
type DelegatedExecutionSource struct {
	Work     LeasedWorkItem                `json:"work"`
	OwnerID  string                        `json:"owner_id"`
	TaskID   modulecore.TaskID             `json:"task_id"`
	Handoff  conversation.HandoffEnvelope  `json:"handoff"`
	Readback conversation.ReadbackEnvelope `json:"readback"`
	Context  []DelegationContextMessage    `json:"context"`
}

// LoadDelegatedExecution revalidates the execute-work lease and loads the
// exact current accepted source and bound Task inside one EventStore snapshot.
// It is read-only; TaskManager separately owns and verifies the latest Task Run.
func (s *SQLiteStore) LoadDelegatedExecution(ctx context.Context, input DelegatedExecutionSourceInput) (DelegatedExecutionSource, error) {
	if err := s.ensureOpen(); err != nil {
		return DelegatedExecutionSource{}, err
	}
	if ctx == nil || input.SessionID == "" || strings.TrimSpace(input.Assignee) == "" || input.Assignee != strings.TrimSpace(input.Assignee) {
		return DelegatedExecutionSource{}, errors.New("delegated execution source requires context, session, and trimmed assignee")
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return DelegatedExecutionSource{}, err
	}
	defer tx.Rollback()

	work, err := loadCurrentWorkLease(ctx, tx, input.WorkLeaseInput)
	if err != nil {
		return DelegatedExecutionSource{}, err
	}
	if work.Kind != "execute_delegated_task" || work.SessionID != input.SessionID {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: execution lease kind or session mismatch", ErrDelegationWorkConflict)
	}
	var delegationID string
	var revision int64
	err = tx.QueryRowContext(ctx, `
		SELECT d.delegation_id, d.revision
		FROM live_delegation d
		WHERE ? = 'delegation:' || d.delegation_id || ':' || d.revision || ':execute'`, work.DedupeKey,
	).Scan(&delegationID, &revision)
	if err != nil || revision < 1 {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: execution dedupe key is invalid", ErrDelegationWorkConflict)
	}
	requestID := modulecore.RequestID(delegationID)
	if work.WorkID != delegationExecuteWorkID(requestID, uint64(revision)) {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: execution work ID is not canonical", ErrDelegationWorkConflict)
	}
	var startKind, startSourceEventID, startSessionID, startState string
	if err := tx.QueryRowContext(ctx, `
		SELECT kind,source_event_id,session_id,state FROM live_work_outbox WHERE dedupe_key=?`,
		"delegation:"+delegationID+":"+fmt.Sprint(revision)+":start_task",
	).Scan(&startKind, &startSourceEventID, &startSessionID, &startState); err != nil ||
		startKind != "start_delegated_task" || startSourceEventID != string(work.SourceEventID) ||
		startSessionID != string(input.SessionID) || startState != "completed" {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: execution work is not linked to a completed Task admission", ErrDelegationWorkConflict)
	}
	var latestRevision int64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(revision) FROM live_delegation WHERE delegation_id=?`, delegationID).Scan(&latestRevision); err != nil {
		return DelegatedExecutionSource{}, err
	}
	if latestRevision != revision {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: delegation revision is no longer current", ErrDelegationWorkConflict)
	}
	delegation, err := loadReadbackDelegation(ctx, tx, requestID, uint64(revision))
	if err != nil || delegation.sessionID != string(input.SessionID) || delegation.assignee != input.Assignee ||
		delegation.delegationState != string(conversation.ReadbackStatusAccepted) || delegation.turnState != "completed" ||
		!delegation.turnDelegationID.Valid || delegation.turnDelegationID.String != delegationID {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: delegation is not currently accepted for this actor", ErrDelegationWorkConflict)
	}
	var boundTaskID string
	var reportRevision int64
	if err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(work_task_id,''), report_revision
		FROM live_delegation WHERE delegation_id=? AND revision=?`, delegationID, revision,
	).Scan(&boundTaskID, &reportRevision); err != nil {
		return DelegatedExecutionSource{}, err
	}
	if boundTaskID == "" || reportRevision != 0 || work.SourceEventID == "" {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: execution Task is unbound or already has a report", ErrDelegationWorkConflict)
	}

	contextEvent, err := loadEventEnvelopeTx(ctx, tx, modulecore.EventID(delegation.contextEventID))
	if err != nil {
		return DelegatedExecutionSource{}, fmt.Errorf("load delegated execution handoff context: %w", err)
	}
	handoff, instruction, err := validateStoredHandoff(ctx, tx, delegation, contextEvent, ReadbackInput{
		DelegationID: requestID, Revision: uint64(revision), Assignee: input.Assignee,
	})
	if err != nil || handoff.FromActor != "mio" || handoff.ToActor != input.Assignee || instruction.EventID == "" {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: stored handoff is not an authenticated Mio delegation", ErrDelegationWorkConflict)
	}
	readbackEvent, err := loadEventEnvelopeTx(ctx, tx, work.SourceEventID)
	if err != nil {
		return DelegatedExecutionSource{}, fmt.Errorf("load accepted delegated execution readback: %w", err)
	}
	if readbackEvent.CausationEventID != instruction.EventID {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: readback does not descend from current instruction", ErrDelegationWorkConflict)
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
		return DelegatedExecutionSource{}, fmt.Errorf("%w: readback public message index is inconsistent", ErrDelegationWorkConflict)
	}
	if err := validateMessageIndex(index, readbackEvent, input.SessionID, modulecore.TurnID(delegation.originTurnID)); err != nil {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: readback event is not accepted", ErrDelegationWorkConflict)
	}
	readback, err := decodeAcceptedTaskReadback(readbackEvent, handoff, delegation, input.Assignee)
	if err != nil {
		return DelegatedExecutionSource{}, err
	}
	contextMessages, err := loadAndValidateDelegationContext(ctx, tx, contextEvent, handoff, delegation)
	if err != nil {
		return DelegatedExecutionSource{}, err
	}
	if err := tx.Commit(); err != nil {
		return DelegatedExecutionSource{}, err
	}
	work.Assignee = input.Assignee
	taskID := modulecore.TaskID(boundTaskID)
	if err := taskID.Validate(); err != nil {
		return DelegatedExecutionSource{}, fmt.Errorf("%w: bound Task identity is invalid", ErrDelegationWorkConflict)
	}
	return DelegatedExecutionSource{
		Work: work, OwnerID: delegation.sessionOwnerID, TaskID: taskID,
		Handoff: handoff, Readback: readback, Context: contextMessages,
	}, nil
}
