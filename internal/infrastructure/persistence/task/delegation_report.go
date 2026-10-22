package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskDelegationReportFilename       = "task_delegation_reports.jsonl"
	maxTaskDelegationReportRecordBytes = 4 << 20
)

var (
	ErrTaskDelegationReportConflict = errors.New("delegation report conflicts with its Task receipt")
	ErrTaskDelegationReportNotFound = errors.New("delegation report not found")
	ErrTaskDelegationReportCorrupt  = errors.New("delegation report log is corrupt")
)

type taskDelegationReportJournalEvent struct {
	Event            string                       `json:"event"`
	Report           *domaintask.DelegationReport `json:"report,omitempty"`
	ReportID         modulecore.ResponseID        `json:"report_id,omitempty"`
	OperationID      string                       `json:"operation_id,omitempty"`
	WriterGeneration uint64                       `json:"writer_generation,omitempty"`
	DeliveredAt      int64                        `json:"delivered_at_unix_nano,omitempty"`
}

type taskDelegationReportReceipt struct {
	ReportID       modulecore.ResponseID `json:"report_id"`
	ReportRevision uint64                `json:"report_revision"`
	TaskID         modulecore.TaskID     `json:"task_id"`
	RunID          modulecore.RunID      `json:"run_id"`
}

// ExecuteIdempotentTaskOperationWithReport commits the normal Task transaction,
// the complete typed report, and a small Task operation receipt in one batch.
// The report journal is intentionally outside the indexed Task record set.
func (s *JSONLStore) ExecuteIdempotentTaskOperationWithReport(
	ctx context.Context,
	taskID modulecore.TaskID,
	reportID modulecore.ResponseID,
	operationID string,
	requestHash string,
	fn func(domaintask.Store) (domaintask.DelegationReport, json.RawMessage, error),
) (json.RawMessage, error) {
	if s == nil {
		return nil, errors.New("task store is nil")
	}
	if ctx == nil {
		return nil, errors.New("delegation report context is nil")
	}
	if fn == nil {
		return nil, errors.New("delegation report callback is nil")
	}
	if err := taskID.Validate(); err != nil {
		return nil, fmt.Errorf("delegation report task_id is invalid: %w", err)
	}
	if err := reportID.Validate(); err != nil {
		return nil, fmt.Errorf("delegation report report_id is invalid: %w", err)
	}
	if err := validateTaskOperationID(operationID); err != nil {
		return nil, err
	}
	if err := validateTaskRequestHash(requestHash); err != nil {
		return nil, err
	}
	if s.readOnly {
		return nil, errors.New("task store is read-only")
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return nil, err
	}
	defer releaseAdmission()
	lease, err := s.executionFence.acquire(ctx, taskID)
	if err != nil {
		return nil, err
	}
	defer lease.release()

	s.rotateIfDue(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.batch == nil || s.writerLock == nil || s.writerGeneration == 0 {
		return nil, errors.New("task store writer is unavailable")
	}
	var result json.RawMessage
	err = s.writeBatch(withTxLabel(ctx, "ExecuteIdempotentTaskOperationWithReport", taskID), func() (map[string][]byte, error) {
		previous, ok, err := s.loadReceipt(ctx, operationID)
		if err != nil {
			return nil, err
		}
		if ok {
			if previous.TaskID != taskID || previous.RequestHash != requestHash {
				return nil, ErrTaskOperationConflict
			}
			if err := s.validateReportReceiptReference(ctx, reportID, operationID, requestHash, previous.Result); err != nil {
				return nil, err
			}
			result = append(json.RawMessage(nil), previous.Result...)
			return nil, nil
		}
		if err := s.activeTaskExecutionFenceError(taskID); err != nil {
			return nil, err
		}
		tx := newTaskTransaction(s, false, taskID, lease)
		defer tx.end()
		if err := tx.touchTask(ctx, taskID); err != nil {
			return nil, err
		}
		report, callbackResult, callbackErr := fn(tx)
		snapshot := tx.freezeAndSnapshot()
		if callbackErr != nil {
			return nil, callbackErr
		}
		if report.ReportID != reportID || report.TaskID != taskID || report.OperationID != operationID || report.RequestHash != requestHash || report.WriterGeneration != s.writerGeneration || report.State != domaintask.DelegationReportPending {
			return nil, errors.New("delegation report callback returned inconsistent Task owner metadata")
		}
		if err := report.Validate(); err != nil {
			return nil, fmt.Errorf("delegation report is invalid: %w", err)
		}
		var receiptRef taskDelegationReportReceipt
		if err := json.Unmarshal(callbackResult, &receiptRef); err != nil {
			return nil, fmt.Errorf("delegation report receipt reference is invalid: %w", err)
		}
		if receiptRef.ReportID != report.ReportID || receiptRef.ReportRevision != report.Envelope.ReportRevision || receiptRef.TaskID != taskID || receiptRef.RunID != report.RunID {
			return nil, errors.New("delegation report receipt does not reference its report")
		}
		if err := validateTaskOperationResult(callbackResult); err != nil {
			return nil, err
		}
		if err := validateTaskTransaction(ctx, s, snapshot); err != nil {
			return nil, err
		}
		encodedReport, err := json.Marshal(taskDelegationReportJournalEvent{Event: "saved", Report: &report})
		if err != nil {
			return nil, err
		}
		if len(encodedReport) > maxTaskDelegationReportRecordBytes {
			return nil, fmt.Errorf("delegation report journal record exceeds %d bytes", maxTaskDelegationReportRecordBytes)
		}
		snapshot.payloads[taskDelegationReportFilename] = append(encodedReport, '\n')
		receipt := TaskOperationReceipt{
			OperationID: operationID, RequestHash: requestHash, Scope: TaskOperationScopeTask,
			TaskID: taskID, Result: append(json.RawMessage(nil), callbackResult...),
			WriterGeneration: s.writerGeneration,
		}
		encodedReceipt, err := json.Marshal(receipt)
		if err != nil {
			return nil, err
		}
		if len(encodedReceipt) > maxTaskOperationResultBytes+1024 {
			return nil, errors.New("task operation receipt exceeds size limit")
		}
		snapshot.payloads[taskOperationReceiptFilename] = append(encodedReceipt, '\n')
		result = append(json.RawMessage(nil), callbackResult...)
		return snapshot.payloads, nil
	})
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), result...), nil
}

// ListPendingTaskDelegationReports scans every segment so a restart bridge does
// not omit reports merely because they precede the current Task segment.
func (s *JSONLStore) ListPendingTaskDelegationReports(ctx context.Context) ([]domaintask.DelegationReport, error) {
	if s == nil {
		return nil, errors.New("task store is nil")
	}
	if ctx == nil {
		return nil, errors.New("delegation report list context is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.batch == nil {
		return nil, errors.New("task store batch is unavailable")
	}
	var reports []domaintask.DelegationReport
	err := s.batch.Read(withTxLabel(ctx, "ListPendingTaskDelegationReports", ""), func() error {
		var err error
		reports, err = s.foldTaskDelegationReports(ctx)
		return err
	})
	if err != nil {
		return nil, err
	}
	pending := make([]domaintask.DelegationReport, 0, len(reports))
	for _, report := range reports {
		if report.State == domaintask.DelegationReportPending {
			pending = append(pending, report)
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ReportID < pending[j].ReportID })
	return pending, nil
}

// MarkTaskDelegationReportDelivered is idempotent. A crash after EventStore
// accepted a report but before this mark causes a safe retry of the same ID.
func (s *JSONLStore) MarkTaskDelegationReportDelivered(ctx context.Context, reportID modulecore.ResponseID) error {
	if s == nil {
		return errors.New("task store is nil")
	}
	if ctx == nil {
		return errors.New("delegation report delivery context is nil")
	}
	if err := reportID.Validate(); err != nil {
		return fmt.Errorf("delegation report report_id is invalid: %w", err)
	}
	reports, err := s.readTaskDelegationReportsUnderBatch(ctx)
	if err != nil {
		return err
	}
	var target *domaintask.DelegationReport
	for i := range reports {
		if reports[i].ReportID == reportID {
			candidate := reports[i]
			target = &candidate
			break
		}
	}
	if target == nil {
		return ErrTaskDelegationReportNotFound
	}
	if target.State == domaintask.DelegationReportDelivered {
		return nil
	}
	releaseAdmission, err := s.lifecycle.admit(ctx)
	if err != nil {
		return err
	}
	defer releaseAdmission()
	lease, err := s.executionFence.acquire(ctx, target.TaskID)
	if err != nil {
		return err
	}
	defer lease.release()
	s.rotateIfDue(ctx)
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return os.ErrClosed
	}
	if s.readOnly || s.batch == nil || s.writerLock == nil || s.writerGeneration == 0 {
		return errors.New("task store writer is unavailable")
	}
	return s.writeBatch(withTxLabel(ctx, "MarkTaskDelegationReportDelivered", target.TaskID), func() (map[string][]byte, error) {
		current, err := s.foldTaskDelegationReports(ctx)
		if err != nil {
			return nil, err
		}
		for _, report := range current {
			if report.ReportID != reportID {
				continue
			}
			if report.State == domaintask.DelegationReportDelivered {
				return nil, nil
			}
			if report.TaskID != target.TaskID || report.OperationID != target.OperationID {
				return nil, ErrTaskDelegationReportConflict
			}
			event := taskDelegationReportJournalEvent{
				Event: "delivered", ReportID: report.ReportID, OperationID: report.OperationID,
				WriterGeneration: s.writerGeneration, DeliveredAt: time.Now().UTC().UnixNano(),
			}
			encoded, err := json.Marshal(event)
			if err != nil {
				return nil, err
			}
			return map[string][]byte{taskDelegationReportFilename: append(encoded, '\n')}, nil
		}
		return nil, ErrTaskDelegationReportNotFound
	})
}

func (s *JSONLStore) validateReportReceiptReference(ctx context.Context, reportID modulecore.ResponseID, operationID, requestHash string, raw json.RawMessage) error {
	var ref taskDelegationReportReceipt
	if err := json.Unmarshal(raw, &ref); err != nil {
		return fmt.Errorf("delegation report receipt reference is corrupt: %w", err)
	}
	if ref.ReportID != reportID {
		return ErrTaskOperationConflict
	}
	reports, err := s.foldTaskDelegationReports(ctx)
	if err != nil {
		return err
	}
	for _, report := range reports {
		if report.ReportID == reportID {
			if report.OperationID != operationID || report.RequestHash != requestHash || report.Envelope.ReportRevision != ref.ReportRevision || report.TaskID != ref.TaskID || report.RunID != ref.RunID {
				return ErrTaskDelegationReportConflict
			}
			return nil
		}
	}
	return fmt.Errorf("%w: receipt references absent report %s", ErrTaskDelegationReportCorrupt, reportID)
}

func (s *JSONLStore) readTaskDelegationReportsUnderBatch(ctx context.Context) ([]domaintask.DelegationReport, error) {
	if s == nil || ctx == nil {
		return nil, errors.New("delegation report store or context is nil")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.closed {
		return nil, os.ErrClosed
	}
	if s.batch == nil {
		return nil, errors.New("task store batch is unavailable")
	}
	var reports []domaintask.DelegationReport
	err := s.batch.Read(withTxLabel(ctx, "ReadTaskDelegationReports", ""), func() error {
		var err error
		reports, err = s.foldTaskDelegationReports(ctx)
		return err
	})
	return reports, err
}

func (s *JSONLStore) foldTaskDelegationReports(ctx context.Context) ([]domaintask.DelegationReport, error) {
	paths := []string{filepath.Join(s.root, taskDelegationReportFilename)}
	if s.idx != nil {
		s.idx.mu.RLock()
		paths = make([]string, 0, len(s.idx.segs))
		for _, seg := range s.idx.segs {
			paths = append(paths, filepath.Join(seg.dir, taskDelegationReportFilename))
		}
		s.idx.mu.RUnlock()
	}
	byID := make(map[modulecore.ResponseID]domaintask.DelegationReport)
	for _, path := range paths {
		events, err := readJSONLLines[taskDelegationReportJournalEvent](ctx, path)
		if err != nil {
			return nil, fmt.Errorf("%w: %s: %v", ErrTaskDelegationReportCorrupt, filepath.Base(path), err)
		}
		for index, event := range events {
			switch event.Event {
			case "saved":
				if event.Report == nil {
					return nil, fmt.Errorf("%w: %s record %d has no report", ErrTaskDelegationReportCorrupt, filepath.Base(path), index)
				}
				report := *event.Report
				report.Envelope.Sender = report.TrustedSender
				if report.State != domaintask.DelegationReportPending || report.DeliveredAt != 0 {
					return nil, fmt.Errorf("%w: saved report state is not pending", ErrTaskDelegationReportCorrupt)
				}
				if err := report.Validate(); err != nil {
					return nil, fmt.Errorf("%w: report %s: %v", ErrTaskDelegationReportCorrupt, report.ReportID, err)
				}
				if previous, exists := byID[report.ReportID]; exists {
					if !sameTaskDelegationReport(previous, report) {
						return nil, fmt.Errorf("%w: report_id %s has conflicting saved records", ErrTaskDelegationReportCorrupt, report.ReportID)
					}
					continue
				}
				byID[report.ReportID] = report
			case "delivered":
				if event.ReportID == "" || event.OperationID == "" || event.WriterGeneration == 0 || event.DeliveredAt <= 0 {
					return nil, fmt.Errorf("%w: delivered record %d has incomplete metadata", ErrTaskDelegationReportCorrupt, index)
				}
				report, exists := byID[event.ReportID]
				if !exists || report.OperationID != event.OperationID {
					return nil, fmt.Errorf("%w: delivery event references an unknown report", ErrTaskDelegationReportCorrupt)
				}
				if report.State == domaintask.DelegationReportDelivered {
					continue
				}
				report.State = domaintask.DelegationReportDelivered
				report.DeliveredAt = event.DeliveredAt
				byID[event.ReportID] = report
			default:
				return nil, fmt.Errorf("%w: event %q is invalid", ErrTaskDelegationReportCorrupt, event.Event)
			}
		}
	}
	reports := make([]domaintask.DelegationReport, 0, len(byID))
	for _, report := range byID {
		reports = append(reports, report)
	}
	sort.Slice(reports, func(i, j int) bool { return reports[i].ReportID < reports[j].ReportID })
	return reports, nil
}

func sameTaskDelegationReport(left, right domaintask.DelegationReport) bool {
	leftBytes, leftErr := json.Marshal(left)
	rightBytes, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftBytes) == string(rightBytes)
}
