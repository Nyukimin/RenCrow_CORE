package delegation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// PrepareNativeOPSResumeSource resolves the terminal child run from the
// persisted Action/Attempt and confirms its known receipt and current Thread
// snapshot. It performs owner reads only; the caller invokes Task admission
// afterward, which rechecks the CORE current-run and user fence atomically.
func (r *Runtime) PrepareNativeOPSResumeSource(ctx context.Context, task domaintask.Task, expectedCoreRunID modulecore.RunID) (domaintask.NativeOPSResumeSource, error) {
	if ctx == nil || ctx.Err() != nil {
		return domaintask.NativeOPSResumeSource{}, errors.New("Resume source context is unavailable")
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindUser ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP || scope.AuthenticatedUserID == "" ||
		scope.ActorID != scope.AuthenticatedUserID || !scope.Allows(domaintool.DataScopeUser) || task.AcceptedOPSClaim == nil ||
		task.AcceptedOPSClaim.ReceiptRef.OwnerID != scope.AuthenticatedUserID || !validResumeTask(task) ||
		task.Status != domaintask.StatusFailed && task.Status != domaintask.StatusCancelled || expectedCoreRunID.Validate() != nil {
		return domaintask.NativeOPSResumeSource{}, errors.New("Resume source is not authorized for this Task")
	}
	reader, ok := r.actions.(ResumeActionReader)
	if !ok {
		return domaintask.NativeOPSResumeSource{}, errors.New("native Action history is unavailable")
	}
	actions, err := reader.ListActions(ctx, domainaction.Filter{TaskID: task.TaskID, RunID: expectedCoreRunID})
	if err != nil {
		return domaintask.NativeOPSResumeSource{}, fmt.Errorf("read source Action: %w", err)
	}
	var sourceAction *domainaction.Action
	for index := range actions {
		if !isNativeResumeAction(actions[index]) {
			continue
		}
		if sourceAction != nil {
			return domaintask.NativeOPSResumeSource{}, errors.New("source CORE Run has multiple native Actions")
		}
		item := actions[index]
		sourceAction = &item
	}
	if sourceAction == nil || sourceAction.TaskID != task.TaskID || sourceAction.RunID != expectedCoreRunID || sourceAction.Validate() != nil ||
		sourceAction.Status != domainaction.StatusFailed && sourceAction.Status != domainaction.StatusCancelled {
		return domaintask.NativeOPSResumeSource{}, errors.New("source CORE Action is absent or not terminally resumable")
	}
	attempts, err := reader.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: sourceAction.ActionID})
	if err != nil || len(attempts) != 1 {
		return domaintask.NativeOPSResumeSource{}, errors.New("source CORE Action has no unique typed Attempt")
	}
	attempt := attempts[0]
	if attempt.AttemptID != sourceAction.CurrentAttemptID || attempt.ActionID != sourceAction.ActionID || attempt.Validate() != nil || attempt.NativeDelegation == nil ||
		attempt.NativeDelegation.ExpectedCriteriaRevision != task.ExpectedCriteriaRevision || len(attempt.NativeDelegation.RunResult) == 0 ||
		attempt.Status != domainaction.AttemptStatusFailed && attempt.Status != domainaction.AttemptStatusCancelled {
		return domaintask.NativeOPSResumeSource{}, errors.New("source CORE Attempt is not a matching terminal native delegation")
	}
	if sourceAction.Status == domainaction.StatusFailed && attempt.Status != domainaction.AttemptStatusFailed ||
		sourceAction.Status == domainaction.StatusCancelled && attempt.Status != domainaction.AttemptStatusCancelled {
		return domaintask.NativeOPSResumeSource{}, errors.New("source Action and Attempt terminal states disagree")
	}
	child, err := acceptedChildResult(attempt)
	if err != nil {
		return domaintask.NativeOPSResumeSource{}, errors.New("source CORE Attempt has no accepted child Run")
	}
	result, err := decodeCanonical[protocol.RunResult](attempt.NativeDelegation.RunResult)
	if err != nil || !sameRunIdentity(child, result) || !result.Resumable {
		return domaintask.NativeOPSResumeSource{}, errors.New("source child RunResult is invalid or not resumable")
	}
	hash := sha256.Sum256(attempt.NativeDelegation.RunResult)
	source := domaintask.NativeOPSResumeSource{
		ActionID: sourceAction.ActionID, AttemptID: attempt.AttemptID, HarnessTaskID: child.TaskID, ThreadID: child.ThreadID,
		RunID: child.RunID, ReceiptID: child.ReceiptID, RunResultSHA256: hex.EncodeToString(hash[:]),
		CheckpointID: cloneString(result.LastCheckpointID),
	}
	if attempt.NativeDelegation.EffectiveMode() == domainaction.NativeDelegationModeResume {
		resumeMutation := attempt.NativeDelegation.Resume
		if resumeMutation == nil || resumeMutation.Status != domainaction.NativeMutationStatusAccepted {
			return domaintask.NativeOPSResumeSource{}, errors.New("source Resume receipt is unavailable")
		}
		previous, decodeErr := decodeCanonical[protocol.ResumeResult](resumeMutation.Result)
		if decodeErr != nil {
			return domaintask.NativeOPSResumeSource{}, errors.New("source Resume result is invalid")
		}
		source.PreviousRunID = previous.PreviousRunID
	}
	if err := source.Validate(); err != nil {
		return domaintask.NativeOPSResumeSource{}, errors.New("source child provenance is invalid")
	}
	c, err := r.acquire(ctx)
	if err != nil {
		return domaintask.NativeOPSResumeSource{}, err
	}
	info, session, childResult, err := readKnownChild(ctx, c, source, childResultReceiptOperation(attempt.NativeDelegation), attempt.NativeDelegation, r.settings.StepTimeout)
	if err != nil {
		return domaintask.NativeOPSResumeSource{}, err
	}
	if !info.Terminal || !childResult.Resumable || session.ActiveRunID != nil {
		return domaintask.NativeOPSResumeSource{}, errors.New("source child Run is active or not resumable")
	}
	return source, nil
}

// ExecuteNativeOPSResume performs only the new Task-owned explicit Resume
// claim's frozen run/resume mutation. It never opens a Session or starts a new
// Task, and a stored unknown outcome is never resent by a later invocation.
func (r *Runtime) ExecuteNativeOPSResume(ctx context.Context, task domaintask.Task, run domaintask.Run, claim domaintask.NativeOPSResumeClaim) (agent.NativeCodingResult, error) {
	d, err := r.newResumeDelegation(ctx, task, run, claim, false)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	unlock := r.lockRun(d.parent)
	defer unlock()
	if err := d.ensureInitialAction(ctx); err != nil {
		return agent.NativeCodingResult{}, err
	}
	if result, done, err := d.completedResult(ctx); err != nil || done {
		return result, err
	}
	if err := d.refusePersistedUnknown(ctx); err != nil {
		return agent.NativeCodingResult{}, err
	}
	c, err := r.acquire(ctx)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	resumed, c, err := d.resumeChildRun(ctx, c)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	d.r.threads.put(resumed.ThreadID, d.corr)
	d.r.log.emit("info", eventDelegateAccepted, d.logFields(map[string]any{
		"harness_thread": harnessRef(resumed.ThreadID), "harness_task": harnessRef(resumed.TaskID),
		"harness_run": harnessRef(resumed.RunID), "harness_receipt": harnessRef(resumed.ReceiptID),
	}))
	start := startResultFromResume(resumed)
	runResult, completionCtx, releaseCompletionCtx, err := d.awaitRun(ctx, c, start)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	defer releaseCompletionCtx()
	return d.complete(completionCtx, c, start, runResult)
}

// ReconcileNativeOPSResume uses only saved IDs and read-only owner methods. It
// does not create an Action, issue RunResume, guess that a missing receipt is
// absent, or adopt an active child Run.
func (r *Runtime) ReconcileNativeOPSResume(ctx context.Context, task domaintask.Task, run domaintask.Run, claim domaintask.NativeOPSResumeClaim) (agent.NativeCodingResult, bool, error) {
	d, err := r.newResumeDelegation(ctx, task, run, claim, true)
	if err != nil {
		return agent.NativeCodingResult{}, false, err
	}
	reader, ok := r.actions.(ResumeActionReader)
	if !ok {
		return agent.NativeCodingResult{}, false, d.unknown("action_owner_unavailable")
	}
	unlock := r.lockRun(d.parent)
	defer unlock()
	actions, err := reader.ListActions(ctx, domainaction.Filter{TaskID: task.TaskID, RunID: run.RunID})
	if err != nil {
		return agent.NativeCodingResult{}, false, d.unknown("action_owner_unavailable")
	}
	var storedAction *domainaction.Action
	for index := range actions {
		if !isNativeResumeAction(actions[index]) {
			continue
		}
		if storedAction != nil {
			return agent.NativeCodingResult{}, false, d.unknown("delegation_state_ambiguous")
		}
		item := actions[index]
		storedAction = &item
	}
	if storedAction == nil {
		return agent.NativeCodingResult{}, false, d.unknown("resume_action_not_saved")
	}
	attempts, err := reader.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: storedAction.ActionID})
	if err != nil || len(attempts) != 1 {
		return agent.NativeCodingResult{}, false, d.unknown("resume_attempt_unavailable")
	}
	attempt := attempts[0]
	if err := d.validatePair(*storedAction, attempt); err != nil || storedAction.CurrentAttemptID != attempt.AttemptID {
		return agent.NativeCodingResult{}, false, d.unknown("delegation_state_invalid")
	}
	d.actionID, d.attemptID = storedAction.ActionID, attempt.AttemptID
	d.corr.actionID, d.corr.attemptID = string(storedAction.ActionID), string(attempt.AttemptID)
	mutation := attempt.NativeDelegation.Resume
	if len(attempt.NativeDelegation.RunResult) != 0 {
		projected, done, err := d.completedResultFromPair(*storedAction, attempt)
		if err != nil || !done {
			return agent.NativeCodingResult{}, false, d.unknown("stored_terminal_result_invalid")
		}
		status := nativeResumeTaskStatus(projected)
		if err := r.tasks.VerifyNativeOPSResumeReplay(ctx, task.TaskID, run.RunID, shiroActorID, claim, status); err != nil {
			return agent.NativeCodingResult{}, false, d.unknown("stored_terminal_task_binding_invalid")
		}
		return projected, true, nil
	}
	if mutation == nil {
		return agent.NativeCodingResult{}, false, d.unknown("resume_child_ids_unavailable")
	}
	if mutation.Status == domainaction.NativeMutationStatusRejected {
		code := persistedRejection(attempt)
		if code == "" {
			return agent.NativeCodingResult{}, false, d.unknown("stored_resume_rejection_invalid")
		}
		if err := r.tasks.VerifyNativeOPSResumeReplay(ctx, task.TaskID, run.RunID, shiroActorID, claim, domaintask.StatusFailed); err != nil {
			return agent.NativeCodingResult{}, false, d.unknown("stored_rejection_task_binding_invalid")
		}
		return agent.NativeCodingResult{}, true, rejected("harness_rejected_" + code)
	}
	if mutation.Status != domainaction.NativeMutationStatusAccepted {
		return agent.NativeCodingResult{}, false, d.unknown("resume_child_ids_unavailable")
	}
	resumed, err := d.validateAcceptedResumeMutation(attempt)
	if err != nil {
		return agent.NativeCodingResult{}, false, d.unknown("stored_resume_result_invalid")
	}
	c, err := r.acquire(ctx)
	if err != nil {
		return agent.NativeCodingResult{}, false, err
	}
	start := startResultFromResume(resumed)
	target := domaintask.NativeOPSResumeSource{
		HarnessTaskID: resumed.TaskID, ThreadID: resumed.ThreadID, RunID: resumed.RunID, ReceiptID: resumed.ReceiptID,
		CheckpointID: cloneString(resumed.CheckpointID),
	}
	info, session, _, err := readKnownChild(ctx, c, target, "run/resume", attempt.NativeDelegation, r.settings.StepTimeout)
	if err != nil {
		return agent.NativeCodingResult{}, false, d.unknown("known_child_read_unavailable")
	}
	if info.RunID != resumed.RunID || info.TaskID != resumed.TaskID || info.ThreadID != resumed.ThreadID {
		return agent.NativeCodingResult{}, false, d.unknown("known_child_identity_mismatch")
	}
	if !info.Terminal || info.Result == nil {
		return agent.NativeCodingResult{}, false, d.unknown("resume_child_running")
	}
	if session.ActiveRunID != nil && *session.ActiveRunID == resumed.RunID {
		return agent.NativeCodingResult{}, false, d.unknown("resume_child_running")
	}
	if session.ActiveRunID != nil {
		return agent.NativeCodingResult{}, false, d.unknown("resume_child_session_mismatch")
	}
	raw, err := protocol.Encode(*info.Result)
	if err != nil {
		return agent.NativeCodingResult{}, false, d.unknown("run_result_invalid")
	}
	if len(attempt.NativeDelegation.RunResult) != 0 && !bytes.Equal(attempt.NativeDelegation.RunResult, raw) {
		return agent.NativeCodingResult{}, false, d.unknown("stored_run_result_mismatch")
	}
	projected, err := d.complete(ctx, c, start, *info.Result)
	if err != nil {
		return agent.NativeCodingResult{}, false, err
	}
	return projected, true, nil
}

func nativeResumeTaskStatus(result agent.NativeCodingResult) domaintask.Status {
	if result.Accepted() {
		return domaintask.StatusSucceeded
	}
	if result.Status == agent.NativeRunCancelled {
		return domaintask.StatusCancelled
	}
	return domaintask.StatusFailed
}

func (d *delegation) validateAcceptedResumeMutation(attempt domainaction.Attempt) (protocol.ResumeResult, error) {
	if attempt.NativeDelegation == nil || d.resumeSource == nil || attempt.NativeDelegation.Resume == nil {
		return protocol.ResumeResult{}, errors.New("accepted Resume mutation is absent")
	}
	mutation := attempt.NativeDelegation.Resume
	if mutation.Status != domainaction.NativeMutationStatusAccepted || mutation.Key != d.key("resume") || len(mutation.Payload) == 0 || len(mutation.Result) == 0 {
		return protocol.ResumeResult{}, errors.New("accepted Resume mutation identity is invalid")
	}
	input, err := decodeCanonical[protocol.ResumeInput](mutation.Payload)
	if err != nil || input.TaskID != d.resumeSource.HarnessTaskID || input.ExpectedLastRunID != d.resumeSource.RunID ||
		input.IdempotencyKey != mutation.Key || input.ExpectedControlRevision == 0 || input.Binding != nil ||
		!sameOptionalString(input.CheckpointID, stringValue(d.resumeSource.CheckpointID)) {
		return protocol.ResumeResult{}, errors.New("frozen Resume payload does not bind the saved source")
	}
	wire, err := protocol.Encode(input)
	if err != nil || !bytes.Equal(wire, mutation.Payload) {
		return protocol.ResumeResult{}, errors.New("frozen Resume payload is not canonical")
	}
	resumed, err := decodeCanonical[protocol.ResumeResult](mutation.Result)
	if err != nil || !resumeResultMatches(resumed, d.resumeSource) {
		return protocol.ResumeResult{}, errors.New("accepted Resume result does not bind the saved source")
	}
	return resumed, nil
}

func (r *Runtime) newResumeDelegation(ctx context.Context, task domaintask.Task, run domaintask.Run, claim domaintask.NativeOPSResumeClaim, allowTerminal bool) (*delegation, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil || identity.TaskID != task.TaskID || identity.RunID != run.RunID || identity.TraceID != claim.TraceID || run.TraceID != claim.TraceID {
		return nil, rejected("execution_identity_required")
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != shiroActorID ||
		scope.AgentRole != "worker" || scope.Purpose != "ops" || scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator ||
		!scope.Allows(domaintool.DataScopeInternal) || !scope.Allows(domaintool.DataScopeUser) ||
		scope.AuthenticatedUserID != claim.OwnerUserID || scope.RequestID != claim.RequestID {
		return nil, rejected(codeScopeInvalid)
	}
	validLifecycle := task.Status == domaintask.StatusRunning && run.Status == domaintask.RunStatusRunning
	if allowTerminal && domaintask.IsTerminal(task.Status) && domaintask.IsRunTerminal(run.Status) {
		validLifecycle = (task.Status == domaintask.StatusSucceeded && run.Status == domaintask.RunStatusSucceeded) ||
			(task.Status == domaintask.StatusFailed && run.Status == domaintask.RunStatusFailed) ||
			(task.Status == domaintask.StatusCancelled && (run.Status == domaintask.RunStatusCancelled || run.Status == domaintask.RunStatusInterrupted)) ||
			(task.Status == domaintask.StatusWaiting && run.Status == domaintask.RunStatusWaiting)
	}
	if !validResumeTask(task) || !validLifecycle || task.Assignee != shiroActorID || run.StartReason != domaintask.RunStartReasonExplicitRerun ||
		task.AcceptedOPSClaim.ReceiptRef.OwnerID != claim.OwnerUserID || run.TaskID != task.TaskID || run.Assignee != shiroActorID ||
		run.WriterGeneration != claim.WriterGeneration || claim.NewCoreRunID != run.RunID || claim.Validate(task) != nil {
		return nil, blocked(codeTaskUnavailable)
	}
	foundClaim := false
	for _, saved := range task.NativeResumeClaims {
		if saved.RequestID == claim.RequestID && saved.NewCoreRunID == claim.NewCoreRunID && saved.PayloadSHA256 == claim.PayloadSHA256 &&
			saved.OwnerUserID == claim.OwnerUserID && saved.TraceID == claim.TraceID && domaintask.NativeOPSResumeClaimsExtend([]domaintask.NativeOPSResumeClaim{saved}, []domaintask.NativeOPSResumeClaim{claim}) {
			foundClaim = true
			break
		}
	}
	if !foundClaim {
		return nil, blocked(codeTaskUnavailable)
	}
	claimCopy := claim
	return &delegation{r: r, parent: identity, task: task, resumeSource: &claim.Source, corr: correlation{
		traceID: string(claim.TraceID), taskID: string(task.TaskID), turnID: string(task.OriginTurnID),
	}, resumeClaim: &claimCopy}, nil
}

func validResumeTask(task domaintask.Task) bool {
	return task.Validate() == nil && task.Route == domaintask.RouteOperations && task.OwnerID == shiroActorID && task.Assignee == shiroActorID &&
		task.AcceptedOPSClaim != nil && task.AcceptedOPSClaim.BackendSelection == "shiro_native_coding_v1" &&
		domaintask.ValidCriteriaRevision(task.ExpectedCriteriaRevision)
}

func isNativeResumeAction(action domainaction.Action) bool {
	return action.Kind == domainaction.KindDelegation && action.Name == domainaction.NativeDelegationActionName
}

func actionResumeSource(source *domaintask.NativeOPSResumeSource) *domainaction.NativeDelegationResumeSource {
	if source == nil {
		return nil
	}
	return &domainaction.NativeDelegationResumeSource{
		ActionID: string(source.ActionID), AttemptID: string(source.AttemptID), HarnessTaskID: source.HarnessTaskID,
		ThreadID: source.ThreadID, RunID: source.RunID, ReceiptID: source.ReceiptID, PreviousRunID: source.PreviousRunID,
		CheckpointID: cloneString(source.CheckpointID), RunResultSHA256: source.RunResultSHA256,
	}
}

func nativeResumeSourceMatches(left, right *domainaction.NativeDelegationResumeSource) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.ActionID == right.ActionID && left.AttemptID == right.AttemptID && left.HarnessTaskID == right.HarnessTaskID &&
		left.ThreadID == right.ThreadID && left.RunID == right.RunID && left.ReceiptID == right.ReceiptID &&
		left.PreviousRunID == right.PreviousRunID && left.RunResultSHA256 == right.RunResultSHA256 &&
		(left.CheckpointID == nil && right.CheckpointID == nil || left.CheckpointID != nil && right.CheckpointID != nil && *left.CheckpointID == *right.CheckpointID)
}

func acceptedChildResult(attempt domainaction.Attempt) (protocol.StartResult, error) {
	if attempt.NativeDelegation == nil {
		return protocol.StartResult{}, errors.New("native state is absent")
	}
	switch attempt.NativeDelegation.EffectiveMode() {
	case domainaction.NativeDelegationModeOpenStart:
		mutation := attempt.NativeDelegation.Start
		if mutation == nil || mutation.Status != domainaction.NativeMutationStatusAccepted {
			return protocol.StartResult{}, errors.New("Start is not accepted")
		}
		start, err := decodeCanonical[protocol.StartResult](mutation.Result)
		if err != nil || !start.Accepted {
			return protocol.StartResult{}, errors.New("Start result is invalid")
		}
		return start, nil
	case domainaction.NativeDelegationModeResume:
		mutation := attempt.NativeDelegation.Resume
		if mutation == nil || mutation.Status != domainaction.NativeMutationStatusAccepted {
			return protocol.StartResult{}, errors.New("Resume is not accepted")
		}
		resumed, err := decodeCanonical[protocol.ResumeResult](mutation.Result)
		if err != nil {
			return protocol.StartResult{}, errors.New("Resume result is invalid")
		}
		return startResultFromResume(resumed), nil
	default:
		return protocol.StartResult{}, errors.New("native delegation mode is invalid")
	}
}

func startResultFromResume(resumed protocol.ResumeResult) protocol.StartResult {
	return protocol.StartResult{
		Accepted: true, ReceiptID: resumed.ReceiptID, TaskID: resumed.TaskID, ThreadID: resumed.ThreadID,
		RunID: resumed.RunID, TraceID: resumed.TraceID, EffectiveLimits: resumed.EffectiveLimits,
		DeadlineAt: resumed.DeadlineAt, RecoveryPolicyRevision: resumed.RecoveryPolicyRevision,
	}
}

func resumeResultMatches(resumed protocol.ResumeResult, source *domaintask.NativeOPSResumeSource) bool {
	return source != nil && resumed.ReceiptID != "" && resumed.TaskID == source.HarnessTaskID && resumed.ThreadID == source.ThreadID &&
		resumed.PreviousRunID == source.RunID && resumed.RunID != "" && resumed.RunID != resumed.PreviousRunID && resumed.TraceID != "" &&
		sameOptionalString(resumed.CheckpointID, stringValue(source.CheckpointID))
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func cloneString(value *string) *string {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func childResultReceiptOperation(state *domainaction.NativeDelegation) string {
	if state != nil && state.EffectiveMode() == domainaction.NativeDelegationModeResume {
		return "run/resume"
	}
	return "turn/start"
}

func readKnownChild(ctx context.Context, c Client, source domaintask.NativeOPSResumeSource, operation string, state *domainaction.NativeDelegation, timeout time.Duration) (protocol.RunInfo, protocol.SessionInfo, protocol.RunResult, error) {
	if state == nil || ctx == nil || c == nil {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child read inputs are unavailable")
	}
	var receiptID string
	var receiptResult []byte
	var wantReceiptType string
	if state.EffectiveMode() == domainaction.NativeDelegationModeOpenStart {
		if state.Start == nil || state.Start.Status != domainaction.NativeMutationStatusAccepted {
			return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known Start receipt is unavailable")
		}
		start, err := decodeCanonical[protocol.StartResult](state.Start.Result)
		if err != nil || !start.Accepted || start.RunID != source.RunID || start.TaskID != source.HarnessTaskID || start.ThreadID != source.ThreadID {
			return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known Start receipt identity is invalid")
		}
		receiptID, receiptResult, wantReceiptType = start.ReceiptID, state.Start.Result, protocol.ReceiptStartResult
	} else if state.EffectiveMode() == domainaction.NativeDelegationModeResume {
		if state.Resume == nil || state.Resume.Status != domainaction.NativeMutationStatusAccepted {
			return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known Resume receipt is unavailable")
		}
		resumed, err := decodeCanonical[protocol.ResumeResult](state.Resume.Result)
		if err != nil || resumed.RunID != source.RunID || resumed.TaskID != source.HarnessTaskID || resumed.ThreadID != source.ThreadID {
			return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known Resume receipt identity is invalid")
		}
		receiptID, receiptResult, wantReceiptType = resumed.ReceiptID, state.Resume.Result, protocol.ReceiptResumeResult
	} else {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child mutation mode is invalid")
	}
	if receiptID != source.ReceiptID || receiptID == "" {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child receipt ID differs from the saved source")
	}
	if timeout <= 0 {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child read timeout is unavailable")
	}
	var receipt protocol.ReceiptRecord
	stepCtx, stop := context.WithTimeout(ctx, timeout)
	receipt, err := c.ReceiptGet(stepCtx, protocol.ReceiptGetInput{ReceiptID: receiptID})
	stop()
	if err != nil || receipt.ReceiptID != receiptID || receipt.Operation != operation || receipt.Stage != "accepted" || receipt.Error != nil || receipt.Result == nil ||
		receipt.Result.Type != wantReceiptType || !bytes.Equal(receipt.Result.Value, receiptResult) {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child receipt is unavailable or mismatched")
	}
	var session protocol.SessionInfo
	stepCtx, stop = context.WithTimeout(ctx, timeout)
	session, err = c.SessionGet(stepCtx, protocol.SessionGetInput{ThreadID: source.ThreadID})
	stop()
	if err != nil || session.ThreadID != source.ThreadID || session.SessionID == "" || session.ControlRevision < 1 {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child Thread is unavailable or mismatched")
	}
	var info protocol.RunInfo
	stepCtx, stop = context.WithTimeout(ctx, timeout)
	info, err = c.RunGet(stepCtx, protocol.RunGetInput{RunID: source.RunID})
	stop()
	if err != nil || info.RunID != source.RunID || info.TaskID != source.HarnessTaskID || info.ThreadID != source.ThreadID || info.LastEventSeq < 1 {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child Run is unavailable or mismatched")
	}
	if !info.Terminal {
		if info.Result != nil {
			return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("nonterminal child Run unexpectedly contains a result")
		}
		return info, session, protocol.RunResult{}, nil
	}
	if info.Result == nil {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("terminal child Run has no result")
	}
	rawResult, err := protocol.Encode(*info.Result)
	if err != nil || len(state.RunResult) != 0 && !bytes.Equal(rawResult, state.RunResult) {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child RunResult differs from saved Action bytes")
	}
	hash := sha256.Sum256(rawResult)
	if source.RunResultSHA256 != "" && hex.EncodeToString(hash[:]) != source.RunResultSHA256 || info.Result.RunID != source.RunID || info.Result.TaskID != source.HarnessTaskID {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child RunResult digest or IDs differ from the saved source")
	}
	if source.RunResultSHA256 != "" && (source.CheckpointID == nil && info.Result.LastCheckpointID != nil || source.CheckpointID != nil && (info.Result.LastCheckpointID == nil || *source.CheckpointID != *info.Result.LastCheckpointID)) {
		return protocol.RunInfo{}, protocol.SessionInfo{}, protocol.RunResult{}, errors.New("known child checkpoint differs from the saved source")
	}
	return info, session, *info.Result, nil
}

func (d *delegation) resumeChildRun(ctx context.Context, initial Client) (protocol.ResumeResult, Client, error) {
	var zero protocol.ResumeResult
	c := initial
	for send := 1; send <= maxSends; send++ {
		if ctx.Err() != nil {
			return zero, c, d.unknown("resume_cancelled")
		}
		if send > 1 {
			if err := waitBackoff(ctx, d.r.backoff(send-1)); err != nil {
				return zero, c, d.unknown("resume_retry_cancelled")
			}
		}
		var err error
		c, err = d.r.acquire(ctx)
		if err != nil {
			return zero, c, d.unknown("resume_client_unavailable")
		}
		_, attempt, err := d.ensurePair(ctx)
		if err != nil {
			return zero, c, d.unknown("resume_action_unavailable")
		}
		mutation := attempt.NativeDelegation.Resume
		if mutation != nil && mutation.Status == domainaction.NativeMutationStatusDeliveryUnknown && !d.resumeInitiated {
			return zero, c, d.unknown("persisted_delivery_unknown")
		}
		stepCtx, cancel := context.WithTimeout(ctx, d.r.settings.StepTimeout)
		var started, sendAttempted bool
		var accepted *protocol.ResumeResult
		var rejectedCode string
		var retryUncertain bool
		var rpcErr, innerErr error
		fenceErr := d.r.tasks.ExecuteRunEffect(stepCtx, d.parent.TaskID, d.parent.RunID, shiroActorID, func(effectCtx context.Context) error {
			started = true
			freshAction, freshAttempt, err := d.ensurePair(effectCtx)
			if err != nil {
				innerErr = err
				return err
			}
			if !freshAction.IsOpen() || !freshAttempt.IsActive() {
				innerErr = errors.New("native Resume Action is no longer active")
				return innerErr
			}
			fresh := freshAttempt.NativeDelegation.Resume
			var input protocol.ResumeInput
			if fresh == nil {
				input, err = d.buildResumeInput(effectCtx, c)
				if err != nil {
					innerErr = err
					return err
				}
				payload, encodeErr := protocol.Encode(input)
				if encodeErr != nil {
					innerErr = encodeErr
					return encodeErr
				}
				_, freshAttempt, err = d.r.actions.PrepareNativeMutation(effectCtx, actionmanager.PrepareNativeMutationInput{
					ActionID: d.actionID, AttemptID: d.attemptID, Slot: domainaction.NativeMutationSlotResume, Key: d.key("resume"), Payload: payload,
				})
				if err != nil {
					innerErr = errors.New("native Resume payload could not be frozen")
					return innerErr
				}
				fresh = freshAttempt.NativeDelegation.Resume
			}
			if fresh == nil || fresh.Key != d.key("resume") {
				innerErr = errors.New("native Resume mutation is missing or has a different key")
				return innerErr
			}
			priorUnknown := fresh.Status == domainaction.NativeMutationStatusDeliveryUnknown
			switch fresh.Status {
			case domainaction.NativeMutationStatusAccepted:
				result, decodeErr := decodeCanonical[protocol.ResumeResult](fresh.Result)
				if decodeErr != nil || !resumeResultMatches(result, d.resumeSource) {
					innerErr = errors.New("stored Resume result is invalid")
					return innerErr
				}
				accepted = &result
				return nil
			case domainaction.NativeMutationStatusRejected:
				rejectedCode = fresh.ErrorCode
				return nil
			case domainaction.NativeMutationStatusDeliveryUnknown:
				if !d.resumeInitiated {
					innerErr = errStoredDeliveryUnknown
					return innerErr
				}
			case domainaction.NativeMutationStatusPrepared:
			default:
				innerErr = errors.New("native Resume mutation state is invalid")
				return innerErr
			}
			input, err = decodeCanonical[protocol.ResumeInput](fresh.Payload)
			if err != nil {
				innerErr = err
				return err
			}
			wire, err := protocol.Encode(input)
			if err != nil || !bytes.Equal(wire, fresh.Payload) {
				innerErr = errors.New("frozen Resume bytes do not match the pinned SDK encoder")
				return innerErr
			}
			if _, _, err := d.r.actions.MarkNativeMutationDeliveryUnknown(effectCtx, d.actionID, d.attemptID, domainaction.NativeMutationSlotResume); err != nil {
				innerErr = err
				return err
			}
			d.resumeInitiated = true
			sendAttempted = true
			result, callErr := c.RunResume(effectCtx, input)
			rpcErr = callErr
			if callErr != nil {
				if errors.Is(callErr, client.ErrOutcomeUnknown) || protocol.CodeOf(callErr) == protocol.CodePersistenceUncertain {
					retryUncertain = true
					_ = d.recordObservationWith(effectCtx, "resume_delivery_unknown", failureCode(callErr))
					return nil
				}
				var remote *client.RemoteError
				if errors.As(callErr, &remote) {
					if priorUnknown {
						retryUncertain = true
						_ = d.recordObservationWith(effectCtx, "resume_remote_rejection_after_unknown", safeObservationCode(protocol.CodeOf(callErr)))
						return nil
					}
					code := safeObservationCode(protocol.CodeOf(callErr))
					if code == "" {
						code = "rejected"
					}
					_, _, innerErr = d.r.actions.RecordNativeMutationOutcome(effectCtx, actionmanager.RecordNativeMutationOutcomeInput{
						ActionID: d.actionID, AttemptID: d.attemptID, Slot: domainaction.NativeMutationSlotResume,
						Status: domainaction.NativeMutationStatusRejected, ErrorCode: code,
					})
					if innerErr == nil {
						rejectedCode = code
					}
					return innerErr
				}
				_ = d.recordObservationWith(effectCtx, "resume_delivery_unknown", failureCode(callErr))
				return nil
			}
			if !resumeResultMatches(result, d.resumeSource) {
				innerErr = errors.New("RunResume returned mismatched child IDs")
				return innerErr
			}
			payload, err := protocol.Encode(result)
			if err != nil {
				innerErr = err
				return err
			}
			_, _, err = d.r.actions.RecordNativeMutationOutcome(effectCtx, actionmanager.RecordNativeMutationOutcomeInput{
				ActionID: d.actionID, AttemptID: d.attemptID, Slot: domainaction.NativeMutationSlotResume,
				Status: domainaction.NativeMutationStatusAccepted, Result: payload,
			})
			if err != nil {
				innerErr = err
				return err
			}
			accepted = &result
			return nil
		})
		cancel()
		if accepted != nil {
			if fenceErr != nil {
				return zero, c, d.unknown("resume_fence_release_uncertain")
			}
			return *accepted, c, nil
		}
		if rejectedCode != "" {
			if innerErr != nil {
				return zero, c, d.unknown("resume_rejection_persist_unknown")
			}
			return zero, c, rejected("harness_rejected_" + rejectedCode)
		}
		if errors.Is(innerErr, errStoredDeliveryUnknown) || errors.Is(innerErr, agent.ErrNativeCodingBlocked) || errors.Is(innerErr, agent.ErrNativeCodingRejected) {
			return zero, c, d.unknown("persisted_delivery_unknown")
		}
		if innerErr != nil || fenceErr != nil {
			if sendAttempted || started || d.resumeInitiated || rpcErr != nil {
				return zero, c, d.unknown("resume_owner_state_uncertain")
			}
			return zero, c, blocked(codeTaskUnavailable)
		}
		if !retryUncertain || send == maxSends || ctx.Err() != nil {
			return zero, c, d.unknown("resume_delivery_unknown")
		}
	}
	return zero, c, d.unknown("resume_delivery_unknown")
}

func (d *delegation) buildResumeInput(ctx context.Context, c Client) (protocol.ResumeInput, error) {
	info, session, result, err := readKnownChild(ctx, c, *d.resumeSource, childResultReceiptOperationFromSource(*d.resumeSource), d.sourceActionState(ctx), d.r.settings.StepTimeout)
	if err != nil {
		return protocol.ResumeInput{}, err
	}
	if !info.Terminal || !result.Resumable || session.ActiveRunID != nil {
		return protocol.ResumeInput{}, errors.New("source child is not currently resumable")
	}
	return protocol.ResumeInput{
		TaskID: d.resumeSource.HarnessTaskID, ExpectedLastRunID: d.resumeSource.RunID,
		CheckpointID: cloneString(d.resumeSource.CheckpointID), ExpectedControlRevision: session.ControlRevision,
		Binding: nil, IdempotencyKey: d.key("resume"), Limits: d.r.settings.Limits,
	}, nil
}

func (d *delegation) sourceActionState(ctx context.Context) *domainaction.NativeDelegation {
	reader, ok := d.r.actions.(ResumeActionReader)
	if !ok || d.resumeSource == nil {
		return nil
	}
	actionID := modulecore.ActionID(d.resumeSource.ActionID)
	attempts, err := reader.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: actionID})
	if err != nil || len(attempts) != 1 || attempts[0].AttemptID != d.resumeSource.AttemptID || attempts[0].NativeDelegation == nil {
		return nil
	}
	state := attempts[0].NativeDelegation.Clone()
	return &state
}

func childResultReceiptOperationFromSource(source domaintask.NativeOPSResumeSource) string {
	if source.PreviousRunID != "" {
		return "run/resume"
	}
	return "turn/start"
}
