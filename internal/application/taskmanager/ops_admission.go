package taskmanager

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var ErrAcceptedOPSClaimRejected = errors.New("accepted OPS claim rejected")

const acceptedOPSFixedTitle = "Accepted OPS request"
const acceptedOPSFixedContext = "Accepted OPS request"

var ErrNativeOPSResumeRejected = errors.New("native OPS resume rejected")

type NativeOPSResumeInput struct {
	TaskID            modulecore.TaskID
	ExpectedCoreRunID modulecore.RunID
	Source            domaintask.NativeOPSResumeSource
}

type NativeOPSResumeStatus string

const (
	NativeOPSResumeClaimed        NativeOPSResumeStatus = "claimed"
	NativeOPSResumeOutcomeUnknown NativeOPSResumeStatus = "outcome_unknown"
)

type NativeOPSResumeResult struct {
	Status     NativeOPSResumeStatus
	Task       domaintask.Task
	Run        domaintask.Run
	Claim      domaintask.NativeOPSResumeClaim
	MayExecute bool
}

// AdmitNativeOPSResume atomically claims one explicit same-Task Resume. The
// implementation is Task-owner authority: request scope, source linkage,
// current-last-Run CAS, new Run/Trace, and immutable idempotency claim commit
// together.
func (m *Manager) AdmitNativeOPSResume(ctx context.Context, input NativeOPSResumeInput) (NativeOPSResumeResult, error) {
	if m == nil || m.store == nil {
		return NativeOPSResumeResult{}, rejectNativeOPSResume("Task owner is unavailable")
	}
	if ctx == nil {
		return NativeOPSResumeResult{}, rejectNativeOPSResume("authenticated user scope is required")
	}
	if err := ctx.Err(); err != nil {
		return NativeOPSResumeResult{}, err
	}
	if err := input.TaskID.Validate(); err != nil {
		return NativeOPSResumeResult{}, rejectNativeOPSResume("Task ID is invalid")
	}
	if err := input.ExpectedCoreRunID.Validate(); err != nil {
		return NativeOPSResumeResult{}, rejectNativeOPSResume("expected CORE Run ID is invalid")
	}
	if err := input.Source.Validate(); err != nil {
		return NativeOPSResumeResult{}, rejectNativeOPSResume("saved Harness source is invalid")
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || scope.AuthenticatedUserID == "" ||
		scope.ActorID != scope.AuthenticatedUserID || !scope.Allows(domaintool.DataScopeUser) || scope.RequestID == "" {
		return NativeOPSResumeResult{}, rejectNativeOPSResume("authenticated user scope is invalid")
	}
	requestID := scope.RequestID
	payloadHash := domaintask.NativeOPSResumePayloadSHA256(input.TaskID, input.ExpectedCoreRunID)

	var result NativeOPSResumeResult
	err := m.taskTransaction(ctx, input.TaskID, func(txManager *Manager) error {
		task, err := txManager.store.GetTask(ctx, input.TaskID)
		if err != nil {
			if errors.Is(err, domaintask.ErrNotFound) {
				return rejectNativeOPSResume("canonical Task is unavailable")
			}
			return err
		}
		if err := task.Validate(); err != nil {
			return rejectNativeOPSResume("canonical Task is invalid")
		}
		if task.TaskID != input.TaskID || task.AcceptedOPSClaim == nil ||
			task.AcceptedOPSClaim.BackendSelection != domainconversation.BackendShiroNativeCodingV1 ||
			task.AcceptedOPSClaim.ReceiptRef.OwnerID != scope.AuthenticatedUserID ||
			task.OwnerID != domaintask.AcceptedOPSAgentAssignee || task.Assignee != domaintask.AcceptedOPSAgentAssignee ||
			task.Route != domaintask.RouteOperations {
			return rejectNativeOPSResume("authenticated user or Shiro OPS ownership does not match")
		}
		if !domaintask.ValidCriteriaRevision(task.ExpectedCriteriaRevision) {
			return rejectNativeOPSResume("Task has no frozen criteria revision")
		}

		// A repeat of a committed idempotency key returns its original new Run
		// without granting another caller the right to execute or resend.
		for _, previous := range task.NativeResumeClaims {
			if previous.RequestID != requestID {
				continue
			}
			if previous.OwnerUserID != scope.AuthenticatedUserID || previous.PayloadSHA256 != payloadHash {
				return rejectNativeOPSResume("request ID was already used with a different Resume payload")
			}
			run, err := txManager.store.GetRun(ctx, previous.NewCoreRunID)
			if err != nil || run.TaskID != task.TaskID || run.TraceID != previous.TraceID || run.StartReason != domaintask.RunStartReasonExplicitRerun {
				return rejectNativeOPSResume("stored Resume Run does not match its Task claim")
			}
			runs, err := txManager.store.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil {
				return err
			}
			if len(runs) == 0 || runs[len(runs)-1].RunID != previous.NewCoreRunID || runs[len(runs)-1].TaskID != task.TaskID {
				return rejectNativeOPSResume("stored Resume Run is no longer the current Task Run")
			}
			result = NativeOPSResumeResult{Status: NativeOPSResumeOutcomeUnknown, Task: task, Run: run, Claim: previous}
			return nil
		}

		if task.Status != domaintask.StatusFailed && task.Status != domaintask.StatusCancelled {
			return rejectNativeOPSResume("only a failed or cancelled OPS Task can be resumed")
		}
		runs, err := txManager.store.ListRuns(ctx, domaintask.RunFilter{TaskID: input.TaskID})
		if err != nil {
			return err
		}
		if len(runs) == 0 {
			return rejectNativeOPSResume("canonical Task has no prior Run")
		}
		last := runs[len(runs)-1]
		if last.RunID != input.ExpectedCoreRunID || last.TaskID != input.TaskID || last.Status == domaintask.RunStatusRunning ||
			last.Assignee != domaintask.AcceptedOPSAgentAssignee {
			return rejectNativeOPSResume("expected CORE Run is not the current terminal Shiro Run")
		}
		wantRunStatus := domaintask.RunStatusFailed
		if task.Status == domaintask.StatusCancelled {
			wantRunStatus = domaintask.RunStatusCancelled
		}
		if last.Status != wantRunStatus {
			return rejectNativeOPSResume("Task and latest CORE Run terminal states do not match")
		}
		generation, err := txManager.store.WriterGeneration()
		if err != nil {
			return err
		}
		if generation == 0 {
			return rejectNativeOPSResume("Task writer generation is unavailable")
		}

		traceID := modulecore.NewTraceID()
		started, run, err := txManager.startWithReasonAndCheckpointAndTrace(
			ctx, input.TaskID, domaintask.RunStartReasonExplicitRerun, "", traceID,
		)
		if err != nil {
			if errors.Is(err, ErrParallelLimit) {
				return rejectNativeOPSResume("Task execution capacity is unavailable")
			}
			return err
		}
		if run.WriterGeneration != generation || run.TraceID != traceID {
			return rejectNativeOPSResume("new CORE Run writer or Trace identity is invalid")
		}
		claim := domaintask.NativeOPSResumeClaim{
			RequestID: requestID, OwnerUserID: scope.AuthenticatedUserID, PayloadSHA256: payloadHash,
			ExpectedCoreRunID: input.ExpectedCoreRunID, NewCoreRunID: run.RunID, TraceID: traceID,
			Source: cloneNativeOPSResumeSource(input.Source), WriterGeneration: generation, CreatedAt: txManager.now().UTC(),
		}
		if err := claim.Validate(started); err != nil {
			return rejectNativeOPSResume("new Task Resume claim is invalid")
		}
		started.NativeResumeClaims = append(started.NativeResumeClaims, claim)
		if err := started.Validate(); err != nil {
			return rejectNativeOPSResume("updated Task Resume claim is invalid")
		}
		if err := txManager.store.SaveTask(ctx, started); err != nil {
			return err
		}
		result = NativeOPSResumeResult{Status: NativeOPSResumeClaimed, Task: started, Run: run, Claim: claim, MayExecute: true}
		return nil
	})
	if err != nil {
		return NativeOPSResumeResult{}, err
	}
	return result, nil
}

func rejectNativeOPSResume(reason string) error {
	return fmt.Errorf("%w: %s", ErrNativeOPSResumeRejected, reason)
}

func cloneNativeOPSResumeSource(source domaintask.NativeOPSResumeSource) domaintask.NativeOPSResumeSource {
	if source.CheckpointID != nil {
		checkpoint := *source.CheckpointID
		source.CheckpointID = &checkpoint
	}
	return source
}

// ExecuteNativeOPSResumeActionEffect holds the Task's durable per-Task fence
// while a caller persists a known child result into the matching Action. A
// Resume Run from an older writer generation is admitted only when the
// current writer owns the store and the immutable claim still selects the
// latest active Shiro Run. The callback must stay bounded and must not call
// the Task owner or perform Harness mutation.
func (m *Manager) ExecuteNativeOPSResumeActionEffect(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	claim domaintask.NativeOPSResumeClaim,
	effect func(context.Context) error,
) error {
	if err := validateNativeOPSResumeEffectArgs(m, ctx, taskID, runID, actorID, claim, effect); err != nil {
		return err
	}
	return m.store.WithTaskExecutionFence(ctx, taskID, func() error {
		if err := m.readTransaction(ctx, func(txManager *Manager) error {
			task, run, err := txManager.validateNativeOPSResumeAuthority(ctx, taskID, runID, actorID, claim)
			if err != nil {
				return err
			}
			if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || run.CompletedAt != nil {
				return fmt.Errorf("%w: native OPS Resume Action effect requires the exact active Run", ErrRunConflict)
			}
			return nil
		}); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := effect(ctx); err != nil {
			return err
		}
		return ctx.Err()
	})
}

func validateNativeOPSResumeEffectArgs(
	m *Manager,
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	claim domaintask.NativeOPSResumeClaim,
	effect func(context.Context) error,
) error {
	if err := validateNativeOPSResumeOwnerArgs(m, ctx, taskID, runID, actorID, claim); err != nil {
		return err
	}
	if effect == nil {
		return errors.New("native OPS Resume Action effect callback is required")
	}
	return nil
}

func validateNativeOPSResumeOwnerArgs(
	m *Manager,
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	claim domaintask.NativeOPSResumeClaim,
) error {
	if m == nil || m.store == nil {
		return errors.New("Task owner is unavailable")
	}
	if ctx == nil {
		return errors.New("native OPS Resume Action effect context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := taskID.Validate(); err != nil {
		return fmt.Errorf("task_id is invalid: %w", err)
	}
	if err := runID.Validate(); err != nil {
		return fmt.Errorf("run_id is invalid: %w", err)
	}
	if actorID != domaintask.AcceptedOPSAgentAssignee {
		return errors.New("native OPS Resume Action effect actor is invalid")
	}
	if err := validateNativeOPSResumeScope(ctx, claim); err != nil {
		return err
	}
	return nil
}

// CompleteNativeOPSResumeRun finalizes the exact latest Resume Run under a
// Task transaction. It accepts a prior generation only for the immutable
// current Resume claim after this process has acquired the current writer
// lease. Matching terminal state is replayed without writes.
func (m *Manager) CompleteNativeOPSResumeRun(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	claim domaintask.NativeOPSResumeClaim,
	status domaintask.Status,
	summary string,
	waitingReason string,
) (domaintask.Task, error) {
	if err := validateNativeOPSResumeOwnerArgs(m, ctx, taskID, runID, actorID, claim); err != nil {
		return domaintask.Task{}, err
	}
	expectedRunStatus, ok := runStatusForTaskStatus(status)
	if !ok {
		return domaintask.Task{}, fmt.Errorf("native OPS Resume completion status is not accepted: %s", status)
	}
	if status == domaintask.StatusWaiting && strings.TrimSpace(waitingReason) == "" {
		return domaintask.Task{}, errors.New("native OPS Resume waiting reason is required")
	}
	var completed domaintask.Task
	err := m.taskTransaction(ctx, taskID, func(txManager *Manager) error {
		task, run, err := txManager.validateNativeOPSResumeAuthority(ctx, taskID, runID, actorID, claim)
		if err != nil {
			return err
		}
		if task.Status == status && run.Status == expectedRunStatus &&
			task.Summary == strings.TrimSpace(summary) && run.Summary == strings.TrimSpace(summary) {
			completed = task
			return nil
		}
		if task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning || run.CompletedAt != nil {
			return fmt.Errorf("%w: native OPS Resume Task or Run is no longer active", ErrRunConflict)
		}
		completed, err = txManager.updateStatusInTransaction(
			ctx, taskID, status, strings.TrimSpace(summary), strings.TrimSpace(waitingReason), nil,
		)
		return err
	})
	if err != nil {
		return domaintask.Task{}, err
	}
	return completed, nil
}

// VerifyNativeOPSResumeReplay checks that a saved result belongs to the
// immutable latest Resume claim. It permits either an active Run awaiting
// adoption or the exact already-finalized outcome, and never changes state.
func (m *Manager) VerifyNativeOPSResumeReplay(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	claim domaintask.NativeOPSResumeClaim,
	status domaintask.Status,
) error {
	if err := validateNativeOPSResumeOwnerArgs(m, ctx, taskID, runID, actorID, claim); err != nil {
		return err
	}
	expectedRunStatus, ok := runStatusForTaskStatus(status)
	if !ok {
		return fmt.Errorf("native OPS Resume replay status is not accepted: %s", status)
	}
	return m.readTransaction(ctx, func(txManager *Manager) error {
		task, run, err := txManager.validateNativeOPSResumeAuthority(ctx, taskID, runID, actorID, claim)
		if err != nil {
			return err
		}
		if task.Status == domaintask.StatusRunning && run.Status == domaintask.RunStatusRunning && run.CompletedAt == nil {
			return nil
		}
		if task.Status != status || run.Status != expectedRunStatus {
			return fmt.Errorf("%w: native OPS Resume replay conflicts with the stored Task outcome", ErrRunConflict)
		}
		return nil
	})
}

func validateNativeOPSResumeScope(ctx context.Context, claim domaintask.NativeOPSResumeClaim) error {
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent ||
		scope.ActorID != domaintask.AcceptedOPSAgentAssignee || scope.AuthenticatedUserID != claim.OwnerUserID ||
		scope.RequestID != claim.RequestID || scope.AgentRole != "worker" || scope.Purpose != "ops" ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator ||
		!scope.Allows(domaintool.DataScopeUser) || !scope.Allows(domaintool.DataScopeInternal) {
		return errors.New("native OPS Resume Shiro user scope is invalid")
	}
	return nil
}

func (m *Manager) validateNativeOPSResumeAuthority(
	ctx context.Context,
	taskID modulecore.TaskID,
	runID modulecore.RunID,
	actorID string,
	claim domaintask.NativeOPSResumeClaim,
) (domaintask.Task, domaintask.Run, error) {
	var zeroTask domaintask.Task
	var zeroRun domaintask.Run
	if err := ctx.Err(); err != nil {
		return zeroTask, zeroRun, err
	}
	if actorID != domaintask.AcceptedOPSAgentAssignee {
		return zeroTask, zeroRun, fmt.Errorf("%w: native OPS Resume claim identity is invalid", ErrRunConflict)
	}
	generation, err := m.store.WriterGeneration()
	if err != nil {
		return zeroTask, zeroRun, fmt.Errorf("Task writer ownership unavailable: %w", err)
	}
	if generation == 0 || claim.WriterGeneration == 0 || claim.WriterGeneration > generation {
		return zeroTask, zeroRun, fmt.Errorf("%w: native OPS Resume writer generation is invalid", ErrRunConflict)
	}
	task, err := m.store.GetTask(ctx, taskID)
	if err != nil {
		return zeroTask, zeroRun, err
	}
	if err := task.Validate(); err != nil {
		return zeroTask, zeroRun, fmt.Errorf("%w: native OPS Resume Task is invalid: %v", ErrRunConflict, err)
	}
	if task.TaskID != taskID || task.OwnerID != actorID || task.Assignee != actorID || task.Route != domaintask.RouteOperations ||
		task.AcceptedOPSClaim == nil || task.AcceptedOPSClaim.BackendSelection != domainconversation.BackendShiroNativeCodingV1 ||
		task.AcceptedOPSClaim.ReceiptRef.OwnerID != claim.OwnerUserID || !domaintask.ValidCriteriaRevision(task.ExpectedCriteriaRevision) ||
		claim.PayloadSHA256 != domaintask.NativeOPSResumePayloadSHA256(taskID, claim.ExpectedCoreRunID) || claim.NewCoreRunID != runID {
		return zeroTask, zeroRun, fmt.Errorf("%w: native OPS Resume Task, user, or criteria ownership changed", ErrRunConflict)
	}
	if err := claim.Validate(task); err != nil || !containsExactNativeOPSResumeClaim(task.NativeResumeClaims, claim) {
		return zeroTask, zeroRun, fmt.Errorf("%w: immutable native OPS Resume claim is missing or changed", ErrRunConflict)
	}
	run, err := m.validateLatestRunInTransaction(ctx, taskID, runID)
	if err != nil {
		return zeroTask, zeroRun, err
	}
	if run.TaskID != taskID || run.RunID != claim.NewCoreRunID || run.TraceID != claim.TraceID ||
		run.Assignee != actorID || run.StartReason != domaintask.RunStartReasonExplicitRerun || run.WriterGeneration != claim.WriterGeneration {
		return zeroTask, zeroRun, fmt.Errorf("%w: native OPS Resume Run does not match its immutable claim", ErrRunConflict)
	}
	sourceRun, err := m.store.GetRun(ctx, claim.ExpectedCoreRunID)
	if err != nil {
		return zeroTask, zeroRun, err
	}
	if err := sourceRun.Validate(); err != nil || sourceRun.TaskID != taskID || sourceRun.Assignee != actorID ||
		(sourceRun.Status != domaintask.RunStatusFailed && sourceRun.Status != domaintask.RunStatusCancelled) {
		return zeroTask, zeroRun, fmt.Errorf("%w: native OPS Resume source Run is invalid", ErrRunConflict)
	}
	if err := ctx.Err(); err != nil {
		return zeroTask, zeroRun, err
	}
	return task, run, nil
}

func containsExactNativeOPSResumeClaim(claims []domaintask.NativeOPSResumeClaim, expected domaintask.NativeOPSResumeClaim) bool {
	for _, saved := range claims {
		if domaintask.NativeOPSResumeClaimsExtend([]domaintask.NativeOPSResumeClaim{saved}, []domaintask.NativeOPSResumeClaim{expected}) {
			return true
		}
	}
	return false
}

type AcceptedOPSClaimStatus string

const (
	AcceptedOPSClaimed        AcceptedOPSClaimStatus = "claimed"
	AcceptedOPSAlreadyRunning AcceptedOPSClaimStatus = "already_running"
	AcceptedOPSTerminal       AcceptedOPSClaimStatus = "terminal"
	AcceptedOPSOutcomeUnknown AcceptedOPSClaimStatus = "outcome_unknown"
	AcceptedOPSBlocked        AcceptedOPSClaimStatus = "blocked"
)

// AcceptedOPSClaimResult carries the canonical CORE Task and its first Run.
// MayExecute is true only for the call that commits both records.
type AcceptedOPSClaimResult struct {
	Status     AcceptedOPSClaimStatus
	Task       domaintask.Task
	Run        domaintask.Run
	MayExecute bool
	Reason     string
}

// AdmitAcceptedOPS claims the canonical Task and first Run for a validated
// Conversation receipt. The trusted authenticated user parent scope is read
// from ctx; backendSelection must be the already-selected Shiro profile.
// receipt.Validate checks shape only. Once wired, the HTTP boundary must
// resolve receipt from the Conversation owner for the authenticated
// user/request and must never accept a client-supplied receipt or backend
// selection. It must reject missing/not-found receipts and any mismatch in
// owner, payload hash, or canonical IDs, and select the backend server-side.
func (m *Manager) AdmitAcceptedOPS(
	ctx context.Context,
	receipt domainconversation.AcceptedOPSInputReceipt,
	backendSelection domainconversation.BackendSelection,
) (AcceptedOPSClaimResult, error) {
	reference, err := validateAcceptedOPSAdmission(ctx, receipt, backendSelection)
	if err != nil {
		return AcceptedOPSClaimResult{}, err
	}
	if m == nil || m.store == nil {
		return AcceptedOPSClaimResult{}, errors.New("task manager store is unavailable")
	}

	var result AcceptedOPSClaimResult
	err = m.taskTransaction(ctx, receipt.TaskID, func(txManager *Manager) error {
		task, getErr := txManager.store.GetTask(ctx, receipt.TaskID)
		if errors.Is(getErr, domaintask.ErrNotFound) {
			return txManager.createAcceptedOPSClaim(ctx, receipt, reference, backendSelection, &result)
		}
		if getErr != nil {
			return getErr
		}
		if !acceptedOPSClaimMatches(task, receipt, reference, backendSelection, txManager.expectedCriteriaRevision) {
			return rejectAcceptedOPSClaim("stored Task does not match the accepted receipt")
		}
		if err := task.Validate(); err != nil {
			return rejectAcceptedOPSClaim("stored Task is invalid")
		}

		runs, err := txManager.store.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
		if err != nil {
			return err
		}
		firstRun, found := acceptedOPSFirstRun(runs, receipt.TaskID)
		if !found {
			result = AcceptedOPSClaimResult{
				Status: AcceptedOPSOutcomeUnknown,
				Task:   task,
				Reason: "canonical first Run is unavailable",
			}
			return nil
		}
		result = AcceptedOPSClaimResult{Task: task, Run: firstRun}
		if len(runs) != 1 {
			result.Status = AcceptedOPSOutcomeUnknown
			result.Reason = "canonical Task does not have exactly one first Run"
			return nil
		}
		if task.Status == domaintask.StatusRunning && firstRun.Status == domaintask.RunStatusRunning {
			generation, generationErr := txManager.store.WriterGeneration()
			if generationErr != nil || generation == 0 || firstRun.WriterGeneration != generation {
				result.Status = AcceptedOPSOutcomeUnknown
				result.Reason = "first Run belongs to an unknown writer generation"
				return nil
			}
			result.Status = AcceptedOPSAlreadyRunning
			return nil
		}
		expectedRunStatus, hasCanonicalRunStatus := runStatusForTaskStatus(task.Status)
		if !domaintask.IsTerminal(task.Status) || !hasCanonicalRunStatus || firstRun.Status != expectedRunStatus {
			result.Status = AcceptedOPSOutcomeUnknown
			result.Reason = "stored Task and first Run do not form a canonical lifecycle pair"
			return nil
		}
		result.Status = AcceptedOPSTerminal
		return nil
	})
	if errors.Is(err, ErrParallelLimit) {
		return AcceptedOPSClaimResult{
			Status: AcceptedOPSBlocked,
			Reason: "Task execution capacity is unavailable",
		}, nil
	}
	if err != nil {
		return AcceptedOPSClaimResult{}, err
	}
	return result, nil
}

func validateAcceptedOPSAdmission(
	ctx context.Context,
	receipt domainconversation.AcceptedOPSInputReceipt,
	backendSelection domainconversation.BackendSelection,
) (domainconversation.AcceptedOPSInputReference, error) {
	if ctx == nil {
		return domainconversation.AcceptedOPSInputReference{}, rejectAcceptedOPSClaim("authenticated user scope is required")
	}
	if err := ctx.Err(); err != nil {
		return domainconversation.AcceptedOPSInputReference{}, err
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP ||
		scope.AuthenticatedUserID == "" || scope.ActorID != scope.AuthenticatedUserID ||
		!scope.Allows(domaintool.DataScopeUser) || scope.RequestID != receipt.RequestID || scope.AuthenticatedUserID != receipt.OwnerID {
		return domainconversation.AcceptedOPSInputReference{}, rejectAcceptedOPSClaim("authenticated user scope does not match the accepted receipt")
	}
	if backendSelection != domainconversation.BackendShiroNativeCodingV1 {
		return domainconversation.AcceptedOPSInputReference{}, rejectAcceptedOPSClaim("backend selection is not the fixed Shiro native profile")
	}
	reference, err := domainconversation.AcceptedOPSInputReferenceFromReceipt(receipt)
	if err != nil {
		return domainconversation.AcceptedOPSInputReference{}, rejectAcceptedOPSClaim("accepted receipt is invalid")
	}
	return reference, nil
}

func (m *Manager) createAcceptedOPSClaim(
	ctx context.Context,
	receipt domainconversation.AcceptedOPSInputReceipt,
	reference domainconversation.AcceptedOPSInputReference,
	backendSelection domainconversation.BackendSelection,
	result *AcceptedOPSClaimResult,
) error {
	draft := domaintask.Task{
		TaskID:           receipt.TaskID,
		Title:            acceptedOPSFixedTitle,
		Route:            domaintask.RouteOperations,
		OwnerID:          domaintask.AcceptedOPSAgentAssignee,
		Assignee:         domaintask.AcceptedOPSAgentAssignee,
		OriginSessionID:  receipt.SessionID,
		OriginThreadID:   receipt.ThreadID,
		OriginTurnID:     receipt.TurnID,
		OriginMessageID:  receipt.UserMessageID,
		AcceptedOPSClaim: &domaintask.AcceptedOPSClaim{ReceiptRef: reference, BackendSelection: backendSelection},
	}
	shared := domaintask.SharedRoleContext{
		TaskID:     receipt.TaskID,
		UserIntent: acceptedOPSFixedContext,
	}
	created, err := m.createInTransaction(ctx, draft, shared)
	if err != nil {
		return err
	}
	started, firstRun, err := m.startWithReason(ctx, created.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		return err
	}
	*result = AcceptedOPSClaimResult{
		Status:     AcceptedOPSClaimed,
		Task:       started,
		Run:        firstRun,
		MayExecute: true,
	}
	return nil
}

func acceptedOPSClaimMatches(
	task domaintask.Task,
	receipt domainconversation.AcceptedOPSInputReceipt,
	reference domainconversation.AcceptedOPSInputReference,
	backendSelection domainconversation.BackendSelection,
	expectedCriteriaRevision string,
) bool {
	return task.TaskID == receipt.TaskID && task.OwnerID == domaintask.AcceptedOPSAgentAssignee && task.Route == domaintask.RouteOperations &&
		task.Assignee == domaintask.AcceptedOPSAgentAssignee && task.OriginSessionID == receipt.SessionID &&
		task.OriginThreadID == receipt.ThreadID && task.OriginTurnID == receipt.TurnID &&
		task.OriginMessageID == receipt.UserMessageID && task.AcceptedOPSClaim != nil &&
		task.AcceptedOPSClaim.ReceiptRef == reference && task.AcceptedOPSClaim.BackendSelection == backendSelection &&
		task.ExpectedCriteriaRevision == expectedCriteriaRevision
}

func acceptedOPSFirstRun(runs []domaintask.Run, taskID modulecore.TaskID) (domaintask.Run, bool) {
	var first domaintask.Run
	found := false
	for _, run := range runs {
		if run.StartReason != domaintask.RunStartReasonFirst {
			continue
		}
		if found || run.Validate() != nil || run.TaskID != taskID || run.Assignee != domaintask.AcceptedOPSAgentAssignee || run.WriterGeneration == 0 {
			return domaintask.Run{}, false
		}
		first = run
		found = true
	}
	return first, found
}

func rejectAcceptedOPSClaim(reason string) error {
	return fmt.Errorf("%w: %s", ErrAcceptedOPSClaimRejected, reason)
}
