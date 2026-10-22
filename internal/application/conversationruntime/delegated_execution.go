package conversationruntime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	ExecuteDelegatedTaskWorkKind       = "execute_delegated_task"
	delegatedExecutionLeaseDuration    = 2 * time.Minute
	delegatedExecutionSummarySucceeded = "Delegated text-only work completed."
	delegatedExecutionSummaryFailed    = "Delegated text-only work could not be completed."
	delegatedExecutionFailureText      = "The assigned text-only worker could not complete this request."
	delegatedExecutionWaitingReason    = "Process restarted before the delegated text-only Run was durably reported."
	delegatedExecutionRetryReason      = "Automatic checkpoint retry after delegated text-only work failed before report commit."
)

var (
	ErrDelegatedExecutionUnavailable = errors.New("delegated Task execution is unavailable")
	ErrDelegatedExecutionInvalid     = errors.New("delegated Task execution source or Task is invalid")
)

// DelegatedExecutionLease is the EventStore fencing token for one execution
// attempt. The assignee is copied from current accepted delegation state.
type DelegatedExecutionLease struct {
	WorkID        string
	DedupeKey     string
	Kind          string
	SourceEventID modulecore.EventID
	SessionID     modulecore.SessionID
	Priority      int
	Attempt       int
	LeaseToken    string
	LeaseExpires  time.Time
	Version       int64
	Assignee      string
}

type DelegatedExecutionClaimRequest struct {
	Kind          string
	Limit         int
	LeaseDuration time.Duration
	Now           time.Time
}

// DelegatedExecutionSource is the bound Task plus the accepted, immutable
// source material that admission stored for this exact delegation revision.
type DelegatedExecutionSource struct {
	Lease    DelegatedExecutionLease
	OwnerID  string
	TaskID   modulecore.TaskID
	Handoff  conversation.HandoffEnvelope
	Readback conversation.ReadbackEnvelope
	Context  []DelegatedTaskContextMessage
}

type DelegatedExecutionStore interface {
	ClaimDelegatedExecutionWork(context.Context, DelegatedExecutionClaimRequest) ([]DelegatedExecutionLease, error)
	LoadDelegatedExecution(context.Context, DelegatedExecutionLease, string, time.Time) (DelegatedExecutionSource, error)
	RetryDelegatedExecutionWork(context.Context, DelegatedExecutionLease, time.Time) error
}

type DelegatedTaskExecutionOwner interface {
	ReadTaskRuns(context.Context, []modulecore.TaskID) (map[modulecore.TaskID]domaintask.Task, map[modulecore.TaskID][]domaintask.Run, error)
	Context(context.Context, modulecore.TaskID) (domaintask.SharedRoleContext, error)
	RecoverRunAfterProcessRestart(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status, string, string) (domaintask.Task, bool, error)
	StartRunFromCheckpoint(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.RunStartReason, string) (domaintask.Run, error)
	CompleteRun(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status, string, string) (domaintask.Task, error)
	CompleteWithReport(context.Context, modulecore.TaskID, modulecore.RunID, string, domaintask.Status, conversation.ReportEnvelope) (domaintask.Task, error)
	ListPendingReports(context.Context) ([]domaintask.DelegationReport, error)
}

// DelegatedPrivateWorker is implemented by Shiro and Midori private-work
// entrypoints. Kuro intentionally has no field or route in this executor.
type DelegatedPrivateWorker interface {
	ExecutePrivateWork(context.Context, conversation.TurnInput, []string) (string, error)
}

type DelegatedTaskExecutionConfig struct {
	Now func() time.Time
}

type DelegatedTaskExecutionDependencies struct {
	Store  DelegatedExecutionStore
	Tasks  DelegatedTaskExecutionOwner
	Shiro  DelegatedPrivateWorker
	Midori DelegatedPrivateWorker
	Config DelegatedTaskExecutionConfig
}

type DelegatedTaskExecutionResult struct {
	Claimed       bool
	Generated     bool
	ReportSaved   bool
	ReportPending bool
	WorkID        string
	DelegationID  modulecore.RequestID
	Revision      uint64
	TaskID        modulecore.TaskID
	RunID         modulecore.RunID
	ReportID      modulecore.ResponseID
}

// DelegatedTaskExecutor runs one EventStore lease and persists the complete
// Task report before returning. The process owner must pass a process-lifetime
// context, never an HTTP request context. A successful report is left for the
// report bridge; AcceptReport atomically accepts it and closes this work item.
type DelegatedTaskExecutor struct {
	store  DelegatedExecutionStore
	tasks  DelegatedTaskExecutionOwner
	shiro  DelegatedPrivateWorker
	midori DelegatedPrivateWorker
	now    func() time.Time
	mu     sync.Mutex
}

func NewDelegatedTaskExecutor(dependencies DelegatedTaskExecutionDependencies) (*DelegatedTaskExecutor, error) {
	if dependencies.Store == nil || dependencies.Tasks == nil {
		return nil, errors.New("delegated execution EventStore and Task owner are required")
	}
	now := dependencies.Config.Now
	if now == nil {
		now = time.Now
	}
	return &DelegatedTaskExecutor{
		store: dependencies.Store, tasks: dependencies.Tasks,
		shiro: dependencies.Shiro, midori: dependencies.Midori, now: now,
	}, nil
}

// StartOne claims and executes at most one accepted, bound delegated Task.
// Shiro and Midori are selected from the trusted EventStore lease assignee.
func (e *DelegatedTaskExecutor) StartOne(ctx context.Context) (DelegatedTaskExecutionResult, error) {
	if ctx == nil {
		return DelegatedTaskExecutionResult{}, errors.New("delegated execution process context is required")
	}
	if err := ctx.Err(); err != nil {
		return DelegatedTaskExecutionResult{}, err
	}
	if e == nil || e.store == nil || e.tasks == nil || e.now == nil {
		return DelegatedTaskExecutionResult{}, ErrDelegatedExecutionUnavailable
	}
	e.mu.Lock()
	defer e.mu.Unlock()

	now := e.now().UTC()
	leases, err := e.store.ClaimDelegatedExecutionWork(ctx, DelegatedExecutionClaimRequest{
		Kind: ExecuteDelegatedTaskWorkKind, Limit: 1, LeaseDuration: delegatedExecutionLeaseDuration, Now: now,
	})
	if err != nil {
		return DelegatedTaskExecutionResult{}, fmt.Errorf("claim delegated execution work: %w", err)
	}
	if len(leases) == 0 {
		return DelegatedTaskExecutionResult{}, nil
	}
	if len(leases) != 1 {
		causes := []error{fmt.Errorf("delegated execution claim returned %d leases, want at most one", len(leases))}
		for _, lease := range leases {
			if retryErr := e.retry(ctx, lease); retryErr != nil {
				causes = append(causes, retryErr)
			}
		}
		return DelegatedTaskExecutionResult{Claimed: true}, errors.Join(causes...)
	}
	return e.executeLease(ctx, leases[0])
}

func (e *DelegatedTaskExecutor) executeLease(ctx context.Context, lease DelegatedExecutionLease) (DelegatedTaskExecutionResult, error) {
	result := DelegatedTaskExecutionResult{Claimed: true, WorkID: lease.WorkID}
	var cleanupTaskID modulecore.TaskID
	var cleanupRunID modulecore.RunID
	var cleanupActorID string
	leaseRetryAllowed := true
	fail := func(cause error) (DelegatedTaskExecutionResult, error) {
		if cleanupRunID != "" {
			if closeErr := e.closeRunForCheckpointRetry(ctx, cleanupTaskID, cleanupRunID, cleanupActorID); closeErr != nil {
				return result, errors.Join(cause, closeErr)
			}
			cleanupRunID = ""
		}
		if !leaseRetryAllowed {
			return result, cause
		}
		if retryErr := e.retry(ctx, lease); retryErr != nil {
			return result, errors.Join(cause, fmt.Errorf("retry delegated execution lease: %w", retryErr))
		}
		return result, cause
	}
	if err := validateDelegatedExecutionLease(lease, e.now()); err != nil {
		return fail(err)
	}
	actorID := lease.Assignee
	worker, err := e.worker(actorID)
	if err != nil {
		return fail(err)
	}
	source, err := e.store.LoadDelegatedExecution(ctx, lease, actorID, e.now().UTC())
	if err != nil {
		return fail(fmt.Errorf("load current delegated execution source: %w", err))
	}
	if err := validateDelegatedExecutionSource(source, lease, actorID); err != nil {
		return fail(err)
	}
	result.DelegationID, result.Revision, result.TaskID = source.Handoff.DelegationID, source.Handoff.Revision, source.TaskID
	task, runs, err := e.readTaskRuns(ctx, source.TaskID)
	if err != nil {
		return fail(err)
	}
	if err := validateDelegatedTask(task, source, domaintask.SharedRoleContext{TaskID: source.TaskID}, actorID); err != nil {
		return fail(err)
	}
	run, err := latestDelegatedRun(runs, source.TaskID, actorID)
	if err != nil {
		return fail(err)
	}
	if task.Status == domaintask.StatusRunning && run.Status == domaintask.RunStatusRunning {
		// Bind cleanup to the exact current Run before any later content or
		// report read can fail. Task and Run identity were validated above, and
		// CompleteRun rechecks this Run ID before changing state.
		cleanupTaskID, cleanupRunID, cleanupActorID = source.TaskID, run.RunID, actorID
		result.RunID = run.RunID
		_, recovered, recoverErr := e.tasks.RecoverRunAfterProcessRestart(ctx, source.TaskID, run.RunID, actorID,
			domaintask.StatusWaiting, "", delegatedExecutionWaitingReason)
		if recoverErr != nil {
			return fail(fmt.Errorf("recover stale delegated Run: %w", recoverErr))
		}
		if recovered {
			cleanupRunID = ""
			task, runs, err = e.readTaskRuns(ctx, source.TaskID)
			if err != nil {
				return fail(err)
			}
			if err := validateDelegatedTask(task, source, domaintask.SharedRoleContext{TaskID: source.TaskID}, actorID); err != nil {
				return fail(err)
			}
			run, err = latestDelegatedRun(runs, source.TaskID, actorID)
			if err != nil {
				return fail(err)
			}
		}
	}
	if task.Status == domaintask.StatusRunning && run.Status == domaintask.RunStatusRunning {
		// A Run admitted in this process is already current-generation. Its ID is
		// retained so any subsequent failure closes only this exact Run.
		cleanupTaskID, cleanupRunID, cleanupActorID = source.TaskID, run.RunID, actorID
		result.RunID = run.RunID
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if report, found, err := e.pendingReport(ctx, source, actorID); err != nil {
		return fail(err)
	} else if found {
		if cleanupRunID != "" {
			return fail(fmt.Errorf("%w: pending report exists while the bound delegated Run is active", ErrDelegatedExecutionInvalid))
		}
		result.ReportPending, result.ReportSaved, result.ReportID, result.RunID = true, true, report.ReportID, report.RunID
		return result, nil
	}
	shared, err := e.delegatedTaskSharedContext(ctx, source, actorID)
	if err != nil {
		return fail(err)
	}
	checkpointHash, err := delegatedExecutionCheckpointHash(source, shared)
	if err != nil {
		return fail(err)
	}
	if err := validateDelegatedTask(task, source, shared, actorID); err != nil {
		return fail(err)
	}
	if task.Status == domaintask.StatusWaiting && run.Status == domaintask.RunStatusWaiting {
		if err := validateResumableDelegatedRun(run, checkpointHash); err != nil {
			return fail(err)
		}
		run, err = e.tasks.StartRunFromCheckpoint(ctx, source.TaskID, run.RunID, actorID,
			domaintask.RunStartReasonCheckpointResume, checkpointHash)
		if err != nil {
			// A concurrent recovery may already have issued the exact successor.
			// Re-read once and reuse it only when its immutable source hash matches.
			task, runs, readErr := e.readTaskRuns(ctx, source.TaskID)
			if readErr != nil {
				// StartRunFromCheckpoint may have committed before returning an
				// ambiguous error. Without an owner read we cannot safely close an
				// exact Run or release this lease for another attempt.
				leaseRetryAllowed = false
				return fail(errors.Join(fmt.Errorf("resume delegated Run from checkpoint: %w", err), readErr))
			}
			if task.Status != domaintask.StatusRunning {
				leaseRetryAllowed = false
				return fail(fmt.Errorf("resume delegated Run from checkpoint: %w", err))
			}
			run, readErr = latestDelegatedRun(runs, source.TaskID, actorID)
			if readErr != nil || run.Status != domaintask.RunStatusRunning || run.StartReason != domaintask.RunStartReasonCheckpointResume || run.StartCheckpointSHA256 != checkpointHash {
				leaseRetryAllowed = false
				return fail(errors.Join(fmt.Errorf("resume delegated Run from checkpoint: %w", err), readErr))
			}
		}
		// Record the exact owner-issued successor immediately. Every later error
		// path is fenced to this Run ID, so an expired lease cannot close a newer
		// attempt's Run on the same Task.
		cleanupTaskID, cleanupRunID, cleanupActorID = source.TaskID, run.RunID, actorID
		task, runs, err = e.readTaskRuns(ctx, source.TaskID)
		if err != nil {
			return fail(err)
		}
		if task.Status != domaintask.StatusRunning {
			return fail(fmt.Errorf("%w: resumed Task is not running", ErrDelegatedExecutionInvalid))
		}
		currentRun, runErr := latestDelegatedRun(runs, source.TaskID, actorID)
		if runErr != nil || currentRun.RunID != run.RunID || currentRun.Status != domaintask.RunStatusRunning || currentRun.StartReason != domaintask.RunStartReasonCheckpointResume || currentRun.StartCheckpointSHA256 != checkpointHash {
			return fail(errors.Join(fmt.Errorf("%w: resumed Run is not the exact latest source-bound successor", ErrDelegatedExecutionInvalid), runErr))
		}
		run = currentRun
	}
	if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		if cleanupRunID != "" {
			leaseRetryAllowed = false
		}
		return fail(fmt.Errorf("%w: delegated Task has no active Run", ErrDelegatedExecutionInvalid))
	}
	if run.StartReason == domaintask.RunStartReasonCheckpointResume {
		if run.StartCheckpointSHA256 != checkpointHash {
			return fail(fmt.Errorf("%w: active checkpoint Run source hash differs", ErrDelegatedExecutionInvalid))
		}
	} else if run.StartReason != domaintask.RunStartReasonFirst || run.StartCheckpointSHA256 != "" {
		return fail(fmt.Errorf("%w: active delegated Run start reason is unsupported", ErrDelegatedExecutionInvalid))
	}
	result.RunID = run.RunID
	input, err := delegatedTurnInput(source, shared)
	if err != nil {
		return fail(err)
	}
	if err := validateDelegatedTask(task, source, shared, actorID); err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	currentSource, err := e.store.LoadDelegatedExecution(ctx, lease, actorID, e.now().UTC())
	if err != nil {
		return fail(fmt.Errorf("revalidate delegated source before text generation: %w", err))
	}
	if !reflect.DeepEqual(source, currentSource) {
		return fail(fmt.Errorf("%w: accepted delegation source changed before execution", ErrDelegatedExecutionInvalid))
	}
	if err := e.verifyCurrentTaskRun(ctx, source, shared, actorID, run.RunID, checkpointHash); err != nil {
		return fail(fmt.Errorf("verify delegated Task and Run before text generation: %w", err))
	}
	identityCtx, err := execution.WithIdentity(ctx, source.TaskID, run.RunID, input.TraceID())
	if err != nil {
		return fail(fmt.Errorf("bind delegated execution identity: %w", err))
	}
	scope, err := tool.NewToolExecutionScope(string(source.Handoff.DelegationID), tool.ActorKindAgent, actorID, "",
		[]string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		return fail(fmt.Errorf("create trusted delegated Agent scope: %w", err))
	}
	scope.AgentRole, scope.Purpose = "worker", "delegated_task_execution"
	if err := scope.Validate(); err != nil {
		return fail(fmt.Errorf("validate trusted delegated Agent scope: %w", err))
	}
	identityCtx = tool.WithToolExecutionScope(identityCtx, scope)
	output, workerErr := worker.ExecutePrivateWork(identityCtx, input, []string{})
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if !utf8ReportText(output) {
		workerErr = errors.New("worker result is empty, invalid UTF-8, or exceeds the report limit")
		output = ""
	}
	result.Generated = workerErr == nil
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	currentSource, err = e.store.LoadDelegatedExecution(ctx, lease, actorID, e.now().UTC())
	if err != nil {
		return fail(fmt.Errorf("revalidate delegated source before durable report: %w", err))
	}
	if !reflect.DeepEqual(source, currentSource) {
		return fail(fmt.Errorf("%w: accepted delegation source changed before report commit", ErrDelegatedExecutionInvalid))
	}
	if err := e.verifyCurrentTaskRun(ctx, source, shared, actorID, run.RunID, checkpointHash); err != nil {
		return fail(fmt.Errorf("verify delegated Task and Run before report commit: %w", err))
	}
	status, reportStatus := domaintask.StatusSucceeded, conversation.ReportStatusSucceeded
	summary, reportText := delegatedExecutionSummarySucceeded, output
	limitations := []string{}
	if workerErr != nil {
		status, reportStatus = domaintask.StatusFailed, conversation.ReportStatusFailed
		summary, reportText = delegatedExecutionSummaryFailed, delegatedExecutionFailureText
		limitations = []string{"No external capabilities were granted; the requested text result was not produced."}
	}
	report := conversation.ReportEnvelope{
		ReportID: modulecore.NewResponseID(), ReportRevision: 1,
		DelegationID: source.Handoff.DelegationID, DelegationRevision: source.Handoff.Revision,
		WorkTaskID: source.TaskID, WorkRunID: run.RunID, Status: reportStatus,
		Summary: summary, Result: reportText, EvidenceRefs: []modulecore.EvidenceID{},
		ArtifactRefs: []modulecore.ArtifactID{}, Limitations: limitations, Sender: actorID,
	}
	if err := conversation.ValidateReportEnvelope(report, source.Handoff); err != nil {
		return fail(fmt.Errorf("build delegated Task report: %w", err))
	}
	if _, err := e.tasks.CompleteWithReport(ctx, source.TaskID, run.RunID, actorID, status, report); err != nil {
		completionErr := fmt.Errorf("persist delegated Task completion and report: %w", err)
		reconcileCtx, cancel := e.detachedStateContext(ctx)
		pending, found, reportErr := e.pendingReport(reconcileCtx, source, actorID)
		taskAfter, runsAfter, taskErr := e.readTaskRuns(reconcileCtx, source.TaskID)
		cancel()
		if taskErr != nil {
			return result, errors.Join(completionErr,
				wrapDelegatedReadError("reconcile pending report after completion error", reportErr),
				fmt.Errorf("reconcile Task state after completion error: %w", taskErr))
		}
		latestAfter, latestErr := latestDelegatedRun(runsAfter, source.TaskID, actorID)
		if latestErr != nil {
			return result, errors.Join(completionErr, fmt.Errorf("reconcile current delegated Run after completion error: %w", latestErr))
		}
		if found {
			wantRunStatus, ok := delegatedReportRunStatus(pending.TaskStatus)
			if ok && taskAfter.Status == pending.TaskStatus && latestAfter.RunID == pending.RunID && latestAfter.Status == wantRunStatus {
				result.ReportSaved, result.ReportPending, result.ReportID, result.RunID = true, true, pending.ReportID, pending.RunID
				return result, nil
			}
			return result, errors.Join(completionErr, fmt.Errorf("pending delegated report and Task state disagree after completion error"))
		}
		if reportErr != nil {
			readErr := wrapDelegatedReadError("reconcile pending report after completion error", reportErr)
			if taskAfter.Status == domaintask.StatusRunning && latestAfter.RunID == run.RunID && latestAfter.Status == domaintask.RunStatusRunning {
				return fail(errors.Join(completionErr, readErr))
			}
			if taskAfter.Status == domaintask.StatusWaiting && latestAfter.RunID == run.RunID && latestAfter.Status == domaintask.RunStatusWaiting {
				cleanupRunID = ""
				return fail(errors.Join(completionErr, readErr))
			}
			return result, errors.Join(completionErr, readErr, fmt.Errorf("delegated Task/Run changed before report completion could be reconciled"))
		}
		if taskAfter.Status == domaintask.StatusRunning && latestAfter.RunID == run.RunID && latestAfter.Status == domaintask.RunStatusRunning {
			return fail(completionErr)
		}
		if taskAfter.Status == domaintask.StatusWaiting && latestAfter.RunID == run.RunID && latestAfter.Status == domaintask.RunStatusWaiting {
			// A concurrent recovery already released this exact Run. It is safe to
			// retry the lease, but do not issue another Task or close a successor.
			cleanupRunID = ""
			return fail(completionErr)
		}
		return result, errors.Join(completionErr, fmt.Errorf("delegated Task/Run changed before report completion could be reconciled"))
	}
	result.ReportSaved, result.ReportPending, result.ReportID = true, true, report.ReportID
	return result, nil
}

func (e *DelegatedTaskExecutor) worker(actorID string) (DelegatedPrivateWorker, error) {
	switch actorID {
	case "shiro":
		if e.shiro == nil {
			return nil, fmt.Errorf("%w: Shiro private worker is unavailable", ErrDelegatedExecutionUnavailable)
		}
		return e.shiro, nil
	case "midori":
		if e.midori == nil {
			return nil, fmt.Errorf("%w: Midori private worker is unavailable", ErrDelegatedExecutionUnavailable)
		}
		return e.midori, nil
	default:
		return nil, fmt.Errorf("%w: actor %q is not an allowed text-only worker", ErrDelegatedExecutionInvalid, actorID)
	}
}

func (e *DelegatedTaskExecutor) retry(ctx context.Context, lease DelegatedExecutionLease) error {
	retryCtx, cancel := e.detachedStateContext(ctx)
	defer cancel()
	return e.store.RetryDelegatedExecutionWork(retryCtx, lease, e.now().UTC())
}

func (e *DelegatedTaskExecutor) closeRunForCheckpointRetry(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string) error {
	closeCtx, cancel := e.detachedStateContext(ctx)
	defer cancel()
	completed, err := e.tasks.CompleteRun(closeCtx, taskID, runID, actorID, domaintask.StatusWaiting, "", delegatedExecutionRetryReason)
	if err == nil && completed.TaskID == taskID && completed.Status == domaintask.StatusWaiting && completed.WaitingReason == delegatedExecutionRetryReason {
		return nil
	}
	if err == nil {
		err = errors.New("Task owner returned a nonmatching waiting state")
	}
	{
		completedErr := fmt.Errorf("complete delegated Run %s as waiting before retry: %w", runID, err)
		task, runs, readErr := e.readTaskRuns(closeCtx, taskID)
		if readErr == nil {
			latest, latestErr := latestDelegatedRun(runs, taskID, actorID)
			if latestErr == nil && task.Status == domaintask.StatusWaiting && task.WaitingReason != "" &&
				latest.RunID == runID && latest.Status == domaintask.RunStatusWaiting {
				// The owner committed the waiting transition but its response was
				// lost. The exact current Run is closed, so the slot is released.
				return nil
			}
			readErr = errors.Join(fmt.Errorf("Task/Run is not the exact waiting predecessor: Task=%s Run=%s task_status=%s run_status=%s",
				task.TaskID, latest.RunID, task.Status, latest.Status), latestErr)
		}
		if readErr != nil {
			completedErr = errors.Join(completedErr, fmt.Errorf("verify delegated Run closure: %w", readErr))
		}
		return completedErr
	}
}

func (e *DelegatedTaskExecutor) detachedStateContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(ctx), taskmanager.StartedRunTerminalWriteTimeout)
}

func wrapDelegatedReadError(message string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", message, err)
}

func delegatedReportRunStatus(status domaintask.Status) (domaintask.RunStatus, bool) {
	switch status {
	case domaintask.StatusSucceeded:
		return domaintask.RunStatusSucceeded, true
	case domaintask.StatusFailed:
		return domaintask.RunStatusFailed, true
	default:
		return "", false
	}
}

func validateDelegatedExecutionLease(lease DelegatedExecutionLease, now time.Time) error {
	delegationID, revision, parseErr := parseDelegatedExecutionDedupe(lease.DedupeKey)
	if lease.WorkID == "" || lease.Kind != ExecuteDelegatedTaskWorkKind || lease.DedupeKey == "" ||
		lease.SessionID == "" || lease.SourceEventID == "" || lease.LeaseToken == "" || lease.Version < 1 ||
		lease.LeaseExpires.IsZero() || !lease.LeaseExpires.After(now) || lease.Assignee == "" ||
		strings.TrimSpace(lease.Assignee) != lease.Assignee || superagent.ValidateActorID(lease.Assignee) != nil ||
		parseErr != nil || revision == 0 || delegationID.Validate() != nil ||
		lease.SessionID.Validate() != nil || lease.SourceEventID.Validate() != nil {
		return fmt.Errorf("%w: execution lease is invalid", ErrDelegatedExecutionInvalid)
	}
	return nil
}

func parseDelegatedExecutionDedupe(value string) (modulecore.RequestID, uint64, error) {
	parts := strings.Split(value, ":")
	if len(parts) != 4 || parts[0] != "delegation" || parts[3] != "execute" {
		return "", 0, errors.New("delegated execution dedupe key shape is invalid")
	}
	revision, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || revision == 0 || strconv.FormatUint(revision, 10) != parts[2] {
		return "", 0, errors.New("delegated execution dedupe revision is invalid")
	}
	return modulecore.RequestID(parts[1]), revision, nil
}

func validateDelegatedExecutionSource(source DelegatedExecutionSource, lease DelegatedExecutionLease, actorID string) error {
	if source.Lease != lease || source.TaskID == "" || strings.TrimSpace(source.OwnerID) == "" ||
		source.Handoff.FromActor != "mio" || source.Handoff.ToActor != actorID ||
		source.Handoff.SessionID != lease.SessionID || source.Handoff.Revision == 0 ||
		source.Readback.Status != conversation.ReadbackStatusAccepted || source.Readback.Sender != actorID ||
		source.Readback.DelegationID != source.Handoff.DelegationID || source.Readback.Revision != source.Handoff.Revision ||
		lease.DedupeKey != fmt.Sprintf("delegation:%s:%d:execute", source.Handoff.DelegationID, source.Handoff.Revision) ||
		len(source.Handoff.AllowedCapabilities) != 0 {
		return fmt.Errorf("%w: source is stale, unbound, or grants capabilities", ErrDelegatedExecutionInvalid)
	}
	if err := source.TaskID.Validate(); err != nil {
		return fmt.Errorf("%w: bound Task ID is invalid", ErrDelegatedExecutionInvalid)
	}
	if err := conversation.ValidateHandoffEnvelope(source.Handoff); err != nil {
		return fmt.Errorf("%w: invalid HandoffEnvelope: %v", ErrDelegatedExecutionInvalid, err)
	}
	if err := conversation.ValidateReadbackEnvelope(source.Readback, source.Handoff, conversation.TrustedReadbackMetadata{
		DelegationID: source.Handoff.DelegationID, Revision: source.Handoff.Revision, Sender: actorID,
	}); err != nil {
		return fmt.Errorf("%w: invalid accepted readback: %v", ErrDelegatedExecutionInvalid, err)
	}
	return validateDelegatedTaskSource(DelegatedTaskSource{
		OwnerID: source.OwnerID, Handoff: source.Handoff, Readback: source.Readback, Context: source.Context,
	}, DelegatedTaskLease{
		DedupeKey: fmt.Sprintf("delegation:%s:%d:start_task", source.Handoff.DelegationID, source.Handoff.Revision),
		SessionID: lease.SessionID, Assignee: actorID,
	}, actorID)
}

func (e *DelegatedTaskExecutor) pendingReport(ctx context.Context, source DelegatedExecutionSource, actorID string) (domaintask.DelegationReport, bool, error) {
	reports, err := e.tasks.ListPendingReports(ctx)
	if err != nil {
		return domaintask.DelegationReport{}, false, fmt.Errorf("read durable pending delegated reports: %w", err)
	}
	for _, report := range reports {
		if report.TaskID != source.TaskID {
			continue
		}
		if err := report.Validate(); err != nil {
			return domaintask.DelegationReport{}, false, fmt.Errorf("validate pending delegated report: %w", err)
		}
		if report.Envelope.DelegationID != source.Handoff.DelegationID || report.Envelope.DelegationRevision != source.Handoff.Revision ||
			report.TrustedSender != actorID || report.Envelope.WorkTaskID != source.TaskID {
			return domaintask.DelegationReport{}, false, fmt.Errorf("%w: Task has a pending report for another delegation or Actor", ErrDelegatedExecutionInvalid)
		}
		return report, true, nil
	}
	return domaintask.DelegationReport{}, false, nil
}

func (e *DelegatedTaskExecutor) readTaskRuns(ctx context.Context, taskID modulecore.TaskID) (domaintask.Task, []domaintask.Run, error) {
	tasks, runs, err := e.tasks.ReadTaskRuns(ctx, []modulecore.TaskID{taskID})
	if err != nil {
		return domaintask.Task{}, nil, fmt.Errorf("read bound delegated Task and Runs: %w", err)
	}
	task, found := tasks[taskID]
	if !found {
		return domaintask.Task{}, nil, fmt.Errorf("%w: bound Task is absent from Task owner", ErrDelegatedExecutionInvalid)
	}
	return task, runs[taskID], nil
}

func (e *DelegatedTaskExecutor) verifyCurrentTaskRun(
	ctx context.Context,
	source DelegatedExecutionSource,
	shared domaintask.SharedRoleContext,
	actorID string,
	expectedRunID modulecore.RunID,
	checkpointHash string,
) error {
	task, runs, err := e.readTaskRuns(ctx, source.TaskID)
	if err != nil {
		return err
	}
	if err := validateDelegatedTask(task, source, shared, actorID); err != nil {
		return err
	}
	if task.Status != domaintask.StatusRunning {
		return fmt.Errorf("%w: delegated Task is no longer running", ErrDelegatedExecutionInvalid)
	}
	run, err := latestDelegatedRun(runs, source.TaskID, actorID)
	if err != nil {
		return err
	}
	if run.RunID != expectedRunID || run.Status != domaintask.RunStatusRunning {
		return fmt.Errorf("%w: delegated Run is no longer the latest active Run", ErrDelegatedExecutionInvalid)
	}
	if run.StartReason == domaintask.RunStartReasonCheckpointResume {
		if run.StartCheckpointSHA256 != checkpointHash {
			return fmt.Errorf("%w: active checkpoint Run source hash differs", ErrDelegatedExecutionInvalid)
		}
	} else if run.StartReason != domaintask.RunStartReasonFirst || run.StartCheckpointSHA256 != "" {
		return fmt.Errorf("%w: active delegated Run start reason is unsupported", ErrDelegatedExecutionInvalid)
	}
	return nil
}

func (e *DelegatedTaskExecutor) delegatedTaskSharedContext(ctx context.Context, source DelegatedExecutionSource, actorID string) (domaintask.SharedRoleContext, error) {
	_, expected, err := delegatedTaskDraft(DelegatedTaskSource{
		OwnerID: source.OwnerID, Handoff: source.Handoff, Readback: source.Readback, Context: source.Context,
	}, actorID)
	if err != nil {
		return domaintask.SharedRoleContext{}, err
	}
	stored, err := e.tasks.Context(ctx, source.TaskID)
	if err != nil {
		return domaintask.SharedRoleContext{}, fmt.Errorf("read delegated Task SharedRoleContext: %w", err)
	}
	expected.TaskID = source.TaskID
	stored.UpdatedAt = time.Time{}
	expected.UpdatedAt = time.Time{}
	// TaskStore JSON omits zero-length Constraints, so reloads canonicalize an
	// empty slice to nil. Treat only those two representations as equivalent;
	// any populated mismatch remains a source validation failure.
	if len(stored.Constraints) == 0 && len(expected.Constraints) == 0 {
		stored.Constraints = nil
		expected.Constraints = nil
	}
	if !reflect.DeepEqual(stored, expected) {
		return domaintask.SharedRoleContext{}, fmt.Errorf("%w: stored SharedRoleContext differs from accepted source", ErrDelegatedExecutionInvalid)
	}
	return expected, nil
}

func validateDelegatedTask(task domaintask.Task, source DelegatedExecutionSource, shared domaintask.SharedRoleContext, actorID string) error {
	if err := task.Validate(); err != nil {
		return fmt.Errorf("%w: Task record is invalid: %v", ErrDelegatedExecutionInvalid, err)
	}
	if task.TaskID != source.TaskID || task.Title != source.Handoff.Goal || task.Route != domaintask.RouteGeneral ||
		task.OwnerID != source.OwnerID || task.Assignee != actorID || !task.ReadOnly ||
		task.OriginSessionID != source.Handoff.SessionID || task.OriginTurnID != source.Handoff.OriginTurnID ||
		task.OriginMessageID != source.Handoff.OriginUserMessageID || task.ModuleID != "" || task.ModuleRoot != "" ||
		task.RoutingEventID != "" || task.AssignmentEventID != "" || task.AcceptedOPSClaim != nil ||
		task.ExpectedCriteriaRevision != "" || len(task.NativeResumeClaims) != 0 || len(task.CoderRoles) != 0 ||
		task.WorkstreamID != "" || task.GoalID != "" || task.ParentTaskID != "" || len(task.DependencyTaskIDs) != 0 ||
		shared.TaskID != source.TaskID {
		return fmt.Errorf("%w: Task is not the current read-only delegated Task", ErrDelegatedExecutionInvalid)
	}
	return nil
}

func latestDelegatedRun(runs []domaintask.Run, taskID modulecore.TaskID, actorID string) (domaintask.Run, error) {
	if len(runs) == 0 {
		return domaintask.Run{}, fmt.Errorf("%w: bound Task has no Run", ErrDelegatedExecutionInvalid)
	}
	seen := make(map[modulecore.RunID]struct{}, len(runs))
	active := 0
	latestCount := 0
	var latest domaintask.Run
	for _, run := range runs {
		if err := run.Validate(); err != nil || run.TaskID != taskID || run.Assignee != actorID {
			return domaintask.Run{}, fmt.Errorf("%w: Run is invalid or belongs to another Task/Actor", ErrDelegatedExecutionInvalid)
		}
		if _, exists := seen[run.RunID]; exists {
			return domaintask.Run{}, fmt.Errorf("%w: duplicate Run identity", ErrDelegatedExecutionInvalid)
		}
		seen[run.RunID] = struct{}{}
		if run.Status == domaintask.RunStatusRunning {
			active++
		}
		if latest.RunID == "" || run.StartedAt.After(latest.StartedAt) {
			latest, latestCount = run, 1
		} else if run.StartedAt.Equal(latest.StartedAt) {
			latestCount++
		}
	}
	if active > 1 || latestCount != 1 {
		return domaintask.Run{}, fmt.Errorf("%w: Run history has multiple active or ambiguous latest Runs", ErrDelegatedExecutionInvalid)
	}
	return latest, nil
}

func validateResumableDelegatedRun(run domaintask.Run, checkpointHash string) error {
	if run.Status != domaintask.RunStatusWaiting ||
		(run.StartReason != domaintask.RunStartReasonFirst && run.StartReason != domaintask.RunStartReasonCheckpointResume) ||
		(run.StartReason == domaintask.RunStartReasonFirst && run.StartCheckpointSHA256 != "") ||
		(run.StartReason == domaintask.RunStartReasonCheckpointResume && run.StartCheckpointSHA256 != checkpointHash) {
		return fmt.Errorf("%w: waiting Run does not prove an immutable text-only source checkpoint", ErrDelegatedExecutionInvalid)
	}
	return nil
}

func delegatedExecutionCheckpointHash(source DelegatedExecutionSource, shared domaintask.SharedRoleContext) (string, error) {
	shared.UpdatedAt = time.Time{}
	immutable := struct {
		OwnerID  string
		Handoff  conversation.HandoffEnvelope
		Readback conversation.ReadbackEnvelope
		Context  []DelegatedTaskContextMessage
		Shared   domaintask.SharedRoleContext
	}{source.OwnerID, source.Handoff, source.Readback, source.Context, shared}
	encoded, err := json.Marshal(immutable)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

func delegatedTurnInput(source DelegatedExecutionSource, shared domaintask.SharedRoleContext) (conversation.TurnInput, error) {
	address, err := conversation.NewChannelAddress("delegated", string(source.Handoff.SessionID))
	if err != nil {
		return conversation.TurnInput{}, err
	}
	text := "Delegated text-only assignment\n\nGoal: " + source.Handoff.Goal +
		"\n\nInstruction: " + source.Handoff.Instruction + "\n\nExpected output: " + source.Handoff.ExpectedOutput +
		"\n\nConstraints:\n"
	for _, constraint := range source.Handoff.Constraints {
		text += "- " + constraint.Key + ": " + constraint.Text + "\n"
	}
	text += "\n" + shared.HandoffNotes + "\nProduce the requested text only. Do not use tools or claim external actions."
	input, err := conversation.ReconstructTurnInput(source.TaskID, source.Handoff.OriginTurnID,
		source.Handoff.SourceTraceID, source.Handoff.OriginUserMessageID, source.Handoff.InstructionMessageID, text, address)
	if err != nil {
		return conversation.TurnInput{}, err
	}
	return input.WithSessionID(string(source.Handoff.SessionID)).WithRoute(routing.RouteCHAT), nil
}

func utf8ReportText(value string) bool {
	return value != "" && strings.TrimSpace(value) != "" && len(value) <= conversation.MaxReportResultBytes && utf8.ValidString(value)
}
