package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var (
	ErrDelegationReportUnsupported = errors.New("Task store does not support durable delegation reports")
	ErrDelegationReportReceipt     = errors.New("Task delegation report receipt is corrupt")
)

type delegationReportTaskStore interface {
	ExecuteIdempotentTaskOperationWithReport(
		context.Context,
		modulecore.TaskID,
		modulecore.ResponseID,
		string,
		string,
		func(Store) (domaintask.DelegationReport, json.RawMessage, error),
	) (json.RawMessage, error)
	ListPendingTaskDelegationReports(context.Context) ([]domaintask.DelegationReport, error)
	MarkTaskDelegationReportDelivered(context.Context, modulecore.ResponseID) error
}

type delegationReportOperationRequest struct {
	Operation string                      `json:"operation"`
	TaskID    modulecore.TaskID           `json:"task_id"`
	RunID     modulecore.RunID            `json:"run_id"`
	ActorID   string                      `json:"actor_id"`
	Status    domaintask.Status           `json:"task_status"`
	Envelope  conversation.ReportEnvelope `json:"report"`
}

type delegationReportOperationReceipt struct {
	ReportID       modulecore.ResponseID `json:"report_id"`
	ReportRevision uint64                `json:"report_revision"`
	TaskID         modulecore.TaskID     `json:"task_id"`
	RunID          modulecore.RunID      `json:"run_id"`
}

// CompleteWithReport closes the current Task Run and stores the full report
// with a small replay receipt in the Task owner's one JSONL batch. EventStore
// remains responsible for checking current delegation ownership and revision
// when it accepts this report.
func (m *Manager) CompleteWithReport(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	status domaintask.Status,
	report conversation.ReportEnvelope,
) (domaintask.Task, error) {
	if m == nil || m.store == nil {
		return domaintask.Task{}, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return domaintask.Task{}, errors.New("delegation report context is nil")
	}
	if err := ctx.Err(); err != nil {
		return domaintask.Task{}, err
	}
	if err := taskID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("run_id is invalid: %w", err)
	}
	if strings.TrimSpace(actorID) == "" {
		return domaintask.Task{}, errors.New("actor_id is required")
	}
	store, ok := m.store.(delegationReportTaskStore)
	if !ok {
		return domaintask.Task{}, ErrDelegationReportUnsupported
	}
	if report.WorkTaskID != taskID || report.WorkRunID != runID {
		return domaintask.Task{}, fmt.Errorf("%w: report Task/Run identity does not match completion request", ErrRunConflict)
	}
	report.Sender = actorID
	writerGeneration, err := m.store.WriterGeneration()
	if err != nil || writerGeneration == 0 {
		if err == nil {
			err = errors.New("Task writer generation is zero")
		}
		return domaintask.Task{}, fmt.Errorf("Task writer ownership unavailable: %w", err)
	}
	operationID := "delegation-report:" + string(report.ReportID)
	request := delegationReportOperationRequest{
		Operation: "complete_with_report", TaskID: taskID, RunID: runID,
		ActorID: actorID, Status: status, Envelope: report,
	}
	requestBytes, err := json.Marshal(request)
	if err != nil {
		return domaintask.Task{}, fmt.Errorf("encode delegation report operation: %w", err)
	}
	requestDigest := sha256.Sum256(requestBytes)
	requestHash := hex.EncodeToString(requestDigest[:])
	createdAt := time.Now().UTC()
	if m.now != nil {
		createdAt = m.now().UTC()
	}
	delegationReport := domaintask.DelegationReport{
		ReportID: report.ReportID, TaskID: taskID, RunID: runID, TaskStatus: status,
		TrustedSender: actorID, Envelope: report, OperationID: operationID,
		RequestHash: requestHash, WriterGeneration: writerGeneration,
		State: domaintask.DelegationReportPending, CreatedAt: createdAt.UnixNano(),
	}
	if err := delegationReport.Validate(); err != nil {
		return domaintask.Task{}, fmt.Errorf("delegation report is invalid: %w", err)
	}

	result, err := store.ExecuteIdempotentTaskOperationWithReport(ctx, taskID, report.ReportID, operationID, requestHash,
		func(tx Store) (domaintask.DelegationReport, json.RawMessage, error) {
			if tx == nil {
				return domaintask.DelegationReport{}, nil, errors.New("delegation report Task transaction store is unavailable")
			}
			txManager := &Manager{
				store: tx, limits: m.limits, expectedCriteriaRevision: m.expectedCriteriaRevision,
				idIndexed: m.idIndexed, now: m.now,
			}
			if err := txManager.validateRunExecutionInTransaction(ctx, taskID, runID, actorID); err != nil {
				return domaintask.DelegationReport{}, nil, err
			}
			selected, err := txManager.validateLatestRunInTransaction(ctx, taskID, runID)
			if err != nil {
				return domaintask.DelegationReport{}, nil, err
			}
			if selected.WriterGeneration != writerGeneration || selected.Assignee != actorID {
				return domaintask.DelegationReport{}, nil, fmt.Errorf("%w: latest Run ownership changed", ErrRunConflict)
			}
			if _, err := txManager.updateStatusInTransaction(ctx, taskID, status, "", "", nil); err != nil {
				return domaintask.DelegationReport{}, nil, err
			}
			receipt, err := json.Marshal(delegationReportOperationReceipt{
				ReportID: report.ReportID, ReportRevision: report.ReportRevision, TaskID: taskID, RunID: runID,
			})
			if err != nil {
				return domaintask.DelegationReport{}, nil, err
			}
			return delegationReport, receipt, nil
		})
	if err != nil {
		return domaintask.Task{}, err
	}
	var receipt delegationReportOperationReceipt
	if err := json.Unmarshal(result, &receipt); err != nil || receipt.ReportID != report.ReportID || receipt.ReportRevision != report.ReportRevision || receipt.TaskID != taskID || receipt.RunID != runID {
		return domaintask.Task{}, fmt.Errorf("%w: completion receipt does not match the report", ErrDelegationReportReceipt)
	}
	completed, err := m.Get(ctx, taskID)
	if err != nil {
		return domaintask.Task{}, err
	}
	return completed, nil
}

// ListPendingReports returns every Task-owned report that the EventStore
// bridge has not marked delivered. The Task store performs a full segment scan.
func (m *Manager) ListPendingReports(ctx context.Context) ([]domaintask.DelegationReport, error) {
	if m == nil || m.store == nil {
		return nil, errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return nil, errors.New("delegation report list context is nil")
	}
	store, ok := m.store.(delegationReportTaskStore)
	if !ok {
		return nil, ErrDelegationReportUnsupported
	}
	return store.ListPendingTaskDelegationReports(ctx)
}

// MarkReportDelivered follows EventStore acceptance. If the process crashes
// before this mark, the same report remains pending and can be retried exactly.
func (m *Manager) MarkReportDelivered(ctx context.Context, reportID modulecore.ResponseID) error {
	if m == nil || m.store == nil {
		return errors.New("task manager store is unavailable")
	}
	if ctx == nil {
		return errors.New("delegation report delivery context is nil")
	}
	if err := reportID.Validate(); err != nil {
		return fmt.Errorf("report_id is invalid: %w", err)
	}
	store, ok := m.store.(delegationReportTaskStore)
	if !ok {
		return ErrDelegationReportUnsupported
	}
	return store.MarkTaskDelegationReportDelivered(ctx, reportID)
}
