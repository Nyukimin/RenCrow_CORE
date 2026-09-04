package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	chatGPTMachineRawSourceType       = "chatgpt_export"
	chatGPTMachineProjectionType      = ChatGPTRawProjectionType
	chatGPTMachineProjectionRevision  = ChatGPTRawProjectionRevision
	chatGPTMachineProjectionOutput    = "conversation_l1"
	chatGPTMachineAuditEventType      = "memory.chatgpt_import_machine_finalized"
	chatGPTMachineRetryAuditEventType = "memory.chatgpt_import_machine_retry_requested"
)

type chatGPTMachineJobSummary struct {
	StateCounts        domainmemory.ChatGPTImportPromotionStateCounts
	JobCount           int
	FailedWithEvidence int
	MissingEvidence    int
	NonTerminal        int
}

// GetChatGPTImportProgress returns a consistent, export-scoped deterministic
// projection. It only reads persisted counts and state; it never invokes the
// profile extractor or changes a row.
func (s *L1SQLiteStore) GetChatGPTImportProgress(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportProgress, error) {
	input := domainmemory.ChatGPTImportRetryInput{RequestID: strings.TrimSpace(requestID), OwnerID: strings.TrimSpace(ownerID), ActorID: strings.TrimSpace(actorID), ExportID: strings.TrimSpace(exportID)}
	if err := input.Validate(); err != nil {
		return domainmemory.ChatGPTImportProgress{}, err
	}
	if s == nil || s.progressDB == nil {
		return domainmemory.ChatGPTImportProgress{}, chatGPTMachineUnavailableError()
	}
	if err := authorizeChatGPTImportScope(ctx, input.RequestID, input.OwnerID, input.ActorID); err != nil {
		return domainmemory.ChatGPTImportProgress{}, chatGPTMachineForbiddenError()
	}
	tx, err := s.progressDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return domainmemory.ChatGPTImportProgress{}, chatGPTMachineInternalError()
	}
	event, err := latestChatGPTMachineImportEvent(ctx, tx, input.OwnerID, input.ExportID)
	if err != nil {
		return domainmemory.ChatGPTImportProgress{}, rollbackL1Tx(tx, err)
	}
	progress, err := readChatGPTMachineProgress(ctx, tx, input.OwnerID, event)
	if err != nil {
		return domainmemory.ChatGPTImportProgress{}, rollbackL1Tx(tx, err)
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.ChatGPTImportProgress{}, chatGPTMachineInternalError()
	}
	progress.RequestID = input.RequestID
	return progress, nil
}

// ChatGPTImportProgress is also available under the descriptive method name
// used by owner adapters that treat the operation as a status read.
func (s *L1SQLiteStore) ChatGPTImportProgress(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportProgress, error) {
	return s.GetChatGPTImportProgress(ctx, requestID, ownerID, actorID, exportID)
}

// RetryFailedChatGPTImportJobsForExport requeues only failed jobs whose
// evidence event is still present and belongs to the requested export. A Raw
// source row is used only to report a missing evidence event; it is never a
// reason to requeue a job.
func (s *L1SQLiteStore) RetryFailedChatGPTImportJobsForExport(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportRetryResult, error) {
	input := domainmemory.ChatGPTImportRetryInput{RequestID: strings.TrimSpace(requestID), OwnerID: strings.TrimSpace(ownerID), ActorID: strings.TrimSpace(actorID), ExportID: strings.TrimSpace(exportID)}
	if err := input.Validate(); err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, err
	}
	if s == nil || s.db == nil {
		return domainmemory.ChatGPTImportRetryResult{}, chatGPTMachineUnavailableError()
	}
	if err := authorizeChatGPTImportScope(ctx, input.RequestID, input.OwnerID, input.ActorID); err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, chatGPTMachineForbiddenError()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, chatGPTMachineInternalError()
	}
	missingEvidence, err := chatGPTMachineMissingFailedEvidence(ctx, tx, input.OwnerID, input.ExportID)
	if err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
	}
	result, err := tx.ExecContext(ctx, `
UPDATE l1_profile_promotion_job
SET state = ?, attempt_count = 0, lease_token = '', lease_expires_at = NULL,
	next_attempt_at = NULL, last_error = '', updated_at = CURRENT_TIMESTAMP
WHERE state = ?
  AND EXISTS (
		SELECT 1
		FROM l1_chatgpt_profile_promotion_binding b
		WHERE b.owner_id = ? AND b.export_id = ?
		  AND b.evidence_event_id = l1_profile_promotion_job.evidence_event_id
	)
  AND EXISTS (
		SELECT 1 FROM l1_memory_event e
		WHERE e.id = l1_profile_promotion_job.evidence_event_id
		  AND e.speaker = 'user' AND e.source = ? AND e.layer = 'L3'
	)
`, domainmemory.ProfilePromotionPending, domainmemory.ProfilePromotionFailed, input.OwnerID, input.ExportID, chatGPTMachineRawSourceType)
	if err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
	}
	requeued64, err := result.RowsAffected()
	if err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
	}
	requeued := int(requeued64)
	auditReference := chatGPTMachineAuditReference("retry", input.OwnerID, input.ActorID, input.ExportID)
	if requeued > 0 || missingEvidence > 0 {
		if _, err := appendL1EventLog(ctx, tx, chatGPTMachineRetryAuditEventType, "user:"+input.OwnerID, "", "", 0, "", map[string]interface{}{
			"export_id": input.ExportID, "requeued_count": requeued, "missing_evidence_count": missingEvidence,
		}, "chatgpt_import_machine"); err != nil {
			return domainmemory.ChatGPTImportRetryResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
		}
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.ChatGPTImportRetryResult{}, chatGPTMachineInternalError()
	}
	return domainmemory.ChatGPTImportRetryResult{RequestID: input.RequestID, ExportID: input.ExportID, RequeuedCount: requeued, MissingEvidenceCount: missingEvidence, AuditReference: auditReference}, nil
}

// RetryFailedChatGPTImportJobs is a concise compatibility alias for callers
// that already use the operation name without the "ForExport" suffix.
func (s *L1SQLiteStore) RetryFailedChatGPTImportJobs(ctx context.Context, requestID, ownerID, actorID, exportID string) (domainmemory.ChatGPTImportRetryResult, error) {
	return s.RetryFailedChatGPTImportJobsForExport(ctx, requestID, ownerID, actorID, exportID)
}

// FinalizeChatGPTImport verifies an applied import and writes one immutable
// receipt only when input.Apply is true. It is deliberately a machine
// verification step: UserMemory candidates remain candidates and no LLM or
// extractor is reachable here.
func (s *L1SQLiteStore) FinalizeChatGPTImport(ctx context.Context, input domainmemory.ChatGPTImportFinalizeInput) (domainmemory.ChatGPTImportFinalizeResult, error) {
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.ExportID = strings.TrimSpace(input.ExportID)
	if err := input.Validate(); err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, err
	}
	if s == nil || s.db == nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, chatGPTMachineUnavailableError()
	}
	if err := authorizeChatGPTImportScope(ctx, input.RequestID, input.OwnerID, input.ActorID); err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, chatGPTMachineForbiddenError()
	}
	receiptID := ""
	if input.Apply {
		receiptID = chatGPTMachineReceiptID(input.OwnerID, input.ExportID, true)
	}
	payloadHash := chatGPTMachinePayloadHash(input.OwnerID, input.ActorID, input.ExportID, input.Apply)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, chatGPTMachineInternalError()
	}
	if input.Apply {
		if stored, found, readErr := readChatGPTMachineReceipt(ctx, tx, input, receiptID, payloadHash); readErr != nil {
			return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, readErr)
		} else if found {
			if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
				return domainmemory.ChatGPTImportFinalizeResult{}, chatGPTMachineInternalError()
			}
			stored.IdempotentReplay = true
			return stored, nil
		}
	}
	event, err := latestChatGPTMachineImportEvent(ctx, tx, input.OwnerID, input.ExportID)
	if err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, err)
	}
	if event.State != domainmemory.ChatGPTImportStateCompleted || !event.Apply {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, chatGPTMachineBlockedError())
	}
	progress, err := readChatGPTMachineProgress(ctx, tx, input.OwnerID, event)
	if err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, err)
	}
	if !progress.TerminalSuccess {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, chatGPTMachineBlockedError())
	}
	result := chatGPTMachineFinalizeResult(input, receiptID, progress)
	if !input.Apply {
		if err := tx.Commit(); err != nil {
			return domainmemory.ChatGPTImportFinalizeResult{}, chatGPTMachineInternalError()
		}
		return result, nil
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
	}
	if _, err := appendL1EventLog(ctx, tx, chatGPTMachineAuditEventType, "user:"+input.OwnerID, "", "", 0, "", map[string]interface{}{
		"export_id": input.ExportID, "apply": input.Apply, "receipt_id": receiptID,
		"raw_count": progress.RawCount, "projection_count": progress.ProjectionCount, "job_count": progress.JobCount,
	}, "chatgpt_import_machine"); err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_chatgpt_import_finalize_receipt (
	receipt_id, owner_id, actor_id, export_id, apply, payload_hash, result_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
`, receiptID, input.OwnerID, input.ActorID, input.ExportID, boolInt(input.Apply), payloadHash, string(resultJSON))
	if err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, rollbackL1Tx(tx, chatGPTMachineInternalError())
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, chatGPTMachineInternalError()
	}
	return result, nil
}

func latestChatGPTMachineImportEvent(ctx context.Context, tx *sql.Tx, ownerID, exportID string) (domainmemory.ChatGPTImportEvent, error) {
	event, err := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE owner_id = ? AND export_id = ? ORDER BY rowid DESC LIMIT 1`, ownerID, exportID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmemory.ChatGPTImportEvent{}, chatGPTMachineNotFoundError()
	}
	if err != nil {
		return domainmemory.ChatGPTImportEvent{}, chatGPTMachineInternalError()
	}
	return event, nil
}

func readChatGPTMachineProgress(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ownerID string, event domainmemory.ChatGPTImportEvent) (domainmemory.ChatGPTImportProgress, error) {
	// Raw, projection, and completed-receipt counts are immutable values from
	// the latest import event. Re-scanning the large source/projection tables
	// here would both repeat work and make progress depend on mutable metadata.
	rawCount := event.Counts.RawCount
	projectionCount := event.Counts.ProjectionCount
	receiptCount := event.Counts.ProjectionCount
	jobs, err := readChatGPTMachineJobs(ctx, queryer, ownerID, event.Binding.ExportID, event.Counts.JobCount)
	if err != nil {
		return domainmemory.ChatGPTImportProgress{}, err
	}
	progress := domainmemory.ChatGPTImportProgress{
		ImportID: event.ImportID, ExportID: event.Binding.ExportID, Apply: event.Apply, State: event.State,
		ExpectedRawCount: event.Counts.RawCount, ExpectedProjectionCount: event.Counts.ProjectionCount,
		ExpectedJobCount: event.Counts.JobCount, RawCount: rawCount, ProjectionCount: projectionCount,
		CompletedProjectionReceiptCount: receiptCount, JobCount: jobs.JobCount,
		PromotionStateCounts: jobs.StateCounts, FailedWithEvidenceCount: jobs.FailedWithEvidence,
		MissingEvidenceCount: jobs.MissingEvidence, NonTerminalCount: jobs.NonTerminal,
	}
	progress.TerminalSuccess = event.State == domainmemory.ChatGPTImportStateCompleted && event.Apply &&
		rawCount == event.Counts.RawCount && projectionCount == event.Counts.ProjectionCount && receiptCount == event.Counts.ProjectionCount &&
		jobs.JobCount == event.Counts.JobCount && jobs.FailedWithEvidence == 0 && jobs.MissingEvidence == 0 && jobs.NonTerminal == 0
	return progress, nil
}

func readChatGPTMachineJobs(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, ownerID, exportID string, expectedJobCount int) (chatGPTMachineJobSummary, error) {
	var summary chatGPTMachineJobSummary
	var bindingCount, pending, running, retryWait, completed, failed int
	var missingEvidence, failedMissingEvidence, missingJob int
	err := queryer.QueryRowContext(ctx, `
	SELECT binding_count, pending_count, running_count, retry_wait_count,
		completed_count, failed_count, missing_evidence_count,
		failed_missing_evidence_count, missing_job_count
	FROM l1_chatgpt_profile_promotion_summary
	WHERE owner_id = ? AND export_id = ?
	`, ownerID, exportID).Scan(
		&bindingCount, &pending, &running, &retryWait, &completed, &failed,
		&missingEvidence, &failedMissingEvidence, &missingJob)
	if errors.Is(err, sql.ErrNoRows) {
		if expectedJobCount != 0 {
			return chatGPTMachineJobSummary{}, chatGPTMachineBlockedError()
		}
		return chatGPTMachineJobSummary{}, nil
	}
	if err != nil {
		return chatGPTMachineJobSummary{}, chatGPTMachineInternalError()
	}
	stateCount := pending + running + retryWait + completed + failed
	if bindingCount != expectedJobCount || stateCount != bindingCount || missingJob != 0 ||
		missingEvidence > bindingCount || failedMissingEvidence > missingEvidence || failedMissingEvidence > failed {
		return chatGPTMachineJobSummary{}, chatGPTMachineBlockedError()
	}
	summary.StateCounts = domainmemory.ChatGPTImportPromotionStateCounts{
		Pending: pending, Running: running, RetryWait: retryWait, Completed: completed, Failed: failed,
	}
	summary.JobCount = bindingCount
	summary.FailedWithEvidence = failed - failedMissingEvidence
	summary.MissingEvidence = missingEvidence
	summary.NonTerminal = bindingCount - completed
	return summary, nil
}

func chatGPTMachineMissingFailedEvidence(ctx context.Context, tx *sql.Tx, ownerID, exportID string) (int, error) {
	var count int
	err := tx.QueryRowContext(ctx, `
SELECT failed_missing_evidence_count
FROM l1_chatgpt_profile_promotion_summary
WHERE owner_id = ? AND export_id = ?
`, ownerID, exportID).Scan(&count)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, chatGPTMachineBlockedError()
	}
	return count, err
}

func readChatGPTMachineReceipt(ctx context.Context, tx *sql.Tx, input domainmemory.ChatGPTImportFinalizeInput, receiptID, payloadHash string) (domainmemory.ChatGPTImportFinalizeResult, bool, error) {
	var ownerID, actorID, exportID, storedHash, resultJSON string
	var apply int
	err := tx.QueryRowContext(ctx, `SELECT owner_id, actor_id, export_id, apply, payload_hash, result_json FROM l1_chatgpt_import_finalize_receipt WHERE receipt_id = ?`, receiptID).Scan(&ownerID, &actorID, &exportID, &apply, &storedHash, &resultJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmemory.ChatGPTImportFinalizeResult{}, false, nil
	}
	if err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, false, chatGPTMachineInternalError()
	}
	if ownerID != input.OwnerID || actorID != input.ActorID || exportID != input.ExportID || apply != boolInt(input.Apply) || storedHash != payloadHash {
		return domainmemory.ChatGPTImportFinalizeResult{}, false, chatGPTMachineConflictError()
	}
	var result domainmemory.ChatGPTImportFinalizeResult
	decoder := json.NewDecoder(strings.NewReader(resultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return domainmemory.ChatGPTImportFinalizeResult{}, false, chatGPTMachineInternalError()
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domainmemory.ChatGPTImportFinalizeResult{}, false, chatGPTMachineInternalError()
	}
	if result.ReceiptID != receiptID || result.ExportID != input.ExportID || result.Apply != input.Apply || result.Status != domainmemory.ChatGPTImportFinalizeStatusCompleted || result.IdempotentReplay {
		return domainmemory.ChatGPTImportFinalizeResult{}, false, chatGPTMachineInternalError()
	}
	return result, true, nil
}

func chatGPTMachineFinalizeResult(input domainmemory.ChatGPTImportFinalizeInput, receiptID string, progress domainmemory.ChatGPTImportProgress) domainmemory.ChatGPTImportFinalizeResult {
	return domainmemory.ChatGPTImportFinalizeResult{
		RequestID: input.RequestID, ExportID: input.ExportID, Apply: input.Apply,
		Status: domainmemory.ChatGPTImportFinalizeStatusCompleted, ReceiptID: receiptID,
		ExpectedRawCount: progress.ExpectedRawCount, ExpectedProjectionCount: progress.ExpectedProjectionCount,
		ExpectedJobCount: progress.ExpectedJobCount, RawCount: progress.RawCount, ProjectionCount: progress.ProjectionCount,
		CompletedProjectionReceiptCount: progress.CompletedProjectionReceiptCount, JobCount: progress.JobCount,
		PromotionStateCounts: progress.PromotionStateCounts, FailedWithEvidenceCount: progress.FailedWithEvidenceCount,
		MissingEvidenceCount: progress.MissingEvidenceCount, NonTerminalCount: progress.NonTerminalCount,
		AuditReference: chatGPTMachineAuditReference("finalize", input.OwnerID, input.ActorID, input.ExportID),
	}
}

func chatGPTMachineReceiptID(ownerID, exportID string, apply bool) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-finalize-receipt.v1\x00" + ownerID + "\x00" + exportID + "\x00" + fmt.Sprint(apply)))
	return "chatgpt-finalize-receipt:" + hex.EncodeToString(digest[:])
}

func chatGPTMachinePayloadHash(ownerID, actorID, exportID string, apply bool) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-finalize-payload.v1\x00" + ownerID + "\x00" + actorID + "\x00" + exportID + "\x00" + fmt.Sprint(apply)))
	return hex.EncodeToString(digest[:])
}

func chatGPTMachineAuditReference(operation, ownerID, actorID, exportID string) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-machine-audit.v1\x00" + operation + "\x00" + ownerID + "\x00" + actorID + "\x00" + exportID))
	return "chatgpt-machine-audit:" + hex.EncodeToString(digest[:])
}

func chatGPTMachineUnavailableError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorUnavailable, "ChatGPT import machine operation is unavailable")
}

func chatGPTMachineForbiddenError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorForbidden, "ChatGPT import machine operation is forbidden")
}

func chatGPTMachineNotFoundError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorNotFound, "ChatGPT import was not found")
}

func chatGPTMachineConflictError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorConflict, "ChatGPT import machine operation conflicts with current state")
}

func chatGPTMachineBlockedError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorBlocked, "ChatGPT import finalization is blocked")
}

func chatGPTMachineInternalError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorInternal, "ChatGPT import machine operation failed")
}
