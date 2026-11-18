package eventstore

import (
	"context"
	"database/sql"
	"encoding/json"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// FindTaskMeetingGroupSessionForRoot resolves the native SessionID from the
// already persisted occurrence receipt. Callers supply only the frozen
// occurrence and actual root Task/Run; the EventStore validates the receipt
// against that Task marker before returning the mapping.
func (s *SQLiteStore) FindTaskMeetingGroupSessionForRoot(
	ctx context.Context,
	occurrenceKey string,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	reader domaintask.TaskMeetingInvocationReader,
	authorize domaintask.TaskMeetingReadAuthorizationCheck,
) (TaskMeetingGroupSessionReceipt, bool, error) {
	if err := s.ensureOpen(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	if ctx == nil || reader == nil || authorize == nil || occurrenceKey == "" || taskID.Validate() != nil || runID.Validate() != nil {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingUnavailable
	}
	task, run, _, err := reader.GetTaskMeetingInvocationState(ctx, taskID, runID, authorize)
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	invocation := task.TaskMeetingInvocation
	if invocation == nil || invocation.Purpose != domaintask.TaskMeetingPurposeRoot || invocation.OccurrenceKey != occurrenceKey ||
		invocation.ActorID != invocation.CoordinatorActorID || task.TaskID != taskID || run.TaskID != taskID || run.RunID != runID {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingForbidden
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	defer tx.Rollback()
	prior, found, err := readOperationReceipt(ctx, tx, taskMeetingSessionOperationID(occurrenceKey))
	if err != nil || !found {
		return TaskMeetingGroupSessionReceipt{}, found, err
	}
	if prior.operationKind != taskMeetingOpenOperation {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	var receipt TaskMeetingGroupSessionReceipt
	if err := json.Unmarshal([]byte(prior.resultJSON), &receipt); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	request := TaskMeetingGroupSessionRequest{
		OccurrenceKey: occurrenceKey, TaskID: taskID, RunID: runID,
		OwnerUserID: invocation.ConfiguredOwnerUserID, CoordinatorActorID: invocation.CoordinatorActorID,
		RegistrationEvidenceID: invocation.RegistrationEvidenceID, ConfigRevision: invocation.ConfigRevision,
		ConfigSHA256: invocation.ConfigSHA256, InputSHA256: invocation.InputSHA256,
		RequestID: receipt.RequestID, Audience: append([]string(nil), invocation.Audience...),
	}
	inputHash, err := hashJSON(request)
	if err != nil || prior.inputSHA256 != inputHash {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := validateTaskMeetingGroupSessionReceipt(receipt, request, task, run); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := validateTaskMeetingOpenEvent(ctx, tx, receipt); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	var ownerID, runtimeVersion string
	if err := tx.QueryRowContext(ctx, `SELECT owner_id,runtime_version FROM live_conversation_session WHERE session_id=?`, string(receipt.SessionID)).Scan(&ownerID, &runtimeVersion); err != nil || ownerID != request.OwnerUserID || runtimeVersion != taskMeetingRuntimeVersion {
		return TaskMeetingGroupSessionReceipt{}, false, ErrTaskMeetingReceiptBad
	}
	if err := tx.Commit(); err != nil {
		return TaskMeetingGroupSessionReceipt{}, false, err
	}
	return receipt, true, nil
}
