package delegation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	delegationActionName = domainaction.NativeDelegationActionName
	maxSends             = 3
	shiroActorID         = "shiro"
)

var errStoredDeliveryUnknown = errors.New("native delegation mutation already has unknown delivery")

// runGate serializes invocations for one canonical Task/Run while allowing
// unrelated Runs to proceed independently. Durable Action state remains the
// recovery source; the gate only identifies the live invocation allowed to
// retry its own frozen request.
type runGate struct {
	mu   sync.Mutex
	refs int
}

func (r *Runtime) lockRun(identity domainexecution.Identity) func() {
	key := string(identity.TaskID) + "\x00" + string(identity.RunID)
	r.mu.Lock()
	gate := r.runGates[key]
	if gate == nil {
		gate = &runGate{}
		r.runGates[key] = gate
	}
	gate.refs++
	r.mu.Unlock()

	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		r.mu.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(r.runGates, key)
		}
		r.mu.Unlock()
	}
}

// DelegateNativeCoding implements agent.NativeCodingDelegate. It validates the
// trusted Shiro OPS scope and canonical Task/Run, then performs each Harness
// mutation under the Task owner's current-generation fence. Open and Start
// payloads are frozen in the Action owner before delivery; unknown delivery
// never becomes a fabricated failed RunResult.
func (r *Runtime) DelegateNativeCoding(ctx context.Context, request agent.NativeCodingRequest) (agent.NativeCodingResult, error) {
	if ctx == nil {
		return agent.NativeCodingResult{}, rejected("execution_context_required")
	}
	if err := ctx.Err(); err != nil {
		return agent.NativeCodingResult{}, err
	}
	identity, task, reference, err := r.canonicalExecution(ctx, request.Input)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	unlock := r.lockRun(identity)
	defer unlock()

	blocks, userText, err := nativeharnessclient.MaterializeContextRevision(request.Messages, nativeharnessclient.PromptSourceFromMessage)
	if err != nil {
		return agent.NativeCodingResult{}, rejected("context_invalid")
	}
	if err := ctx.Err(); err != nil {
		return agent.NativeCodingResult{}, err
	}

	d := &delegation{
		r: r, parent: identity, input: request.Input, blocks: blocks, userText: userText,
		task: task, reference: reference,
		corr: correlation{
			traceID: string(request.Input.TraceID()), taskID: string(identity.TaskID), turnID: string(request.Input.TurnID()),
		},
	}
	if err := d.ensureInitialAction(ctx); err != nil {
		return agent.NativeCodingResult{}, err
	}
	if result, ok, err := d.completedResult(ctx); err != nil || ok {
		return result, err
	}
	if err := d.refusePersistedUnknown(ctx); err != nil {
		return agent.NativeCodingResult{}, err
	}
	c, err := r.acquire(ctx)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	d.r.log.emit("info", eventDelegateStarted, d.logFields(nil))

	open, c, err := d.openSession(ctx, c)
	if err != nil {
		return agent.NativeCodingResult{}, d.reportMutationError(ctx, "open", err)
	}
	if err := ctx.Err(); err != nil {
		return agent.NativeCodingResult{}, d.unknown("start_not_attempted")
	}

	start, c, err := d.startRun(ctx, c, open.Session)
	if err != nil {
		return agent.NativeCodingResult{}, d.reportMutationError(ctx, "start", err)
	}
	d.r.threads.put(start.ThreadID, d.corr)
	d.r.log.emit("info", eventDelegateAccepted, d.logFields(map[string]any{
		"harness_thread": harnessRef(start.ThreadID), "harness_task": harnessRef(start.TaskID),
		"harness_run": harnessRef(start.RunID), "harness_receipt": harnessRef(start.ReceiptID),
	}))

	runResult, completionCtx, releaseCompletionCtx, err := d.awaitRun(ctx, c, start)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	defer releaseCompletionCtx()
	result, err := d.complete(completionCtx, c, start, runResult)
	if err != nil {
		return agent.NativeCodingResult{}, err
	}
	d.r.log.emit("info", eventDelegateFinished, d.logFields(map[string]any{
		"status": string(result.Status), "code": result.Code, "verification": string(result.Verification), "resumable": result.Resumable,
		"accepted": result.Accepted(), "harness_task": result.HarnessTask, "harness_run": result.HarnessRun, "harness_receipt": result.HarnessReceipt,
	}))
	return result, nil
}

type delegation struct {
	r         *Runtime
	parent    domainexecution.Identity
	task      domaintask.Task
	input     conversation.TurnInput
	blocks    []nativeharnessclient.ContextBlock
	userText  string
	reference *conversation.AcceptedOPSInputReference
	actionID  modulecore.ActionID
	attemptID modulecore.AttemptID
	corr      correlation

	openInitiated   bool
	startInitiated  bool
	resumeInitiated bool
	resumeSource    *domaintask.NativeOPSResumeSource
	resumeClaim     *domaintask.NativeOPSResumeClaim
}

func (d *delegation) key(suffix string) string {
	return "core." + string(d.attemptID) + "." + suffix
}

func (d *delegation) logFields(extra map[string]any) map[string]any {
	fields := d.corr.fields()
	fields["run_id"] = string(d.parent.RunID)
	fields["action_id"] = string(d.actionID)
	fields["attempt_id"] = string(d.attemptID)
	for key, value := range extra {
		fields[key] = value
	}
	return fields
}

func (r *Runtime) canonicalExecution(ctx context.Context, input conversation.TurnInput) (domainexecution.Identity, domaintask.Task, *conversation.AcceptedOPSInputReference, error) {
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil || identity.TraceID != input.TraceID() {
		return domainexecution.Identity{}, domaintask.Task{}, nil, rejected("execution_identity_required")
	}
	scope, found := domaintool.ToolExecutionScopeFromContext(ctx)
	if !found || scope.Validate() != nil || scope.ActorKind != domaintool.ActorKindAgent ||
		scope.ActorID != shiroActorID || scope.AgentRole != "worker" || scope.Purpose != "ops" ||
		scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || !scope.Allows(domaintool.DataScopeInternal) {
		return domainexecution.Identity{}, domaintask.Task{}, nil, rejected(codeScopeInvalid)
	}
	task, err := r.tasks.Get(ctx, identity.TaskID)
	if err != nil || task.TaskID != identity.TaskID || task.Status != domaintask.StatusRunning ||
		task.Assignee != shiroActorID || task.Route != domaintask.RouteOperations {
		return domainexecution.Identity{}, domaintask.Task{}, nil, blocked(codeTaskUnavailable)
	}
	if !domaintask.ValidCriteriaRevision(task.ExpectedCriteriaRevision) || task.ExpectedCriteriaRevision != r.settings.ExpectedCriteriaRevision {
		return domainexecution.Identity{}, domaintask.Task{}, nil, blocked("criteria_revision_unavailable")
	}
	if task.OriginTurnID != "" && task.OriginTurnID != input.TurnID() {
		return domainexecution.Identity{}, domaintask.Task{}, nil, rejected("task_turn_mismatch")
	}
	var reference *conversation.AcceptedOPSInputReference
	if task.AcceptedOPSClaim != nil {
		claim := task.AcceptedOPSClaim
		if err := claim.Validate(task); err != nil {
			return domainexecution.Identity{}, domaintask.Task{}, nil, blocked(codeTaskUnavailable)
		}
		ref := claim.ReceiptRef
		if err := ref.Validate(); err != nil || scope.AuthenticatedUserID != ref.OwnerID || scope.RequestID != ref.RequestID || !scope.Allows(domaintool.DataScopeUser) {
			return domainexecution.Identity{}, domaintask.Task{}, nil, rejected(codeScopeInvalid)
		}
		reference = &ref
	}
	return identity, task, reference, nil
}

func (d *delegation) ensureInitialAction(ctx context.Context) error {
	stepCtx, cancel := context.WithTimeout(ctx, d.r.settings.StepTimeout)
	defer cancel()
	var ownerErr error
	fenceErr := d.r.tasks.ExecuteRunEffect(stepCtx, d.parent.TaskID, d.parent.RunID, shiroActorID, func(effectCtx context.Context) error {
		ownerErr = d.ensureInitialActionOwner(effectCtx)
		return ownerErr
	})
	if ownerErr != nil {
		return ownerErr
	}
	if fenceErr != nil {
		return blocked(codeTaskUnavailable)
	}
	return nil
}

func (d *delegation) ensureInitialActionOwner(ctx context.Context) error {
	action, attempt, err := d.r.actions.EnsureNativeDelegation(ctx, d.ensureInput())
	if err != nil {
		return blocked("action_owner_unavailable")
	}
	if err := d.validatePair(action, attempt); err != nil {
		return blocked("delegation_state_invalid")
	}
	d.actionID, d.attemptID = action.ActionID, attempt.AttemptID
	d.corr.actionID, d.corr.attemptID = string(action.ActionID), string(attempt.AttemptID)
	return nil
}

func (d *delegation) ensurePair(ctx context.Context) (domainaction.Action, domainaction.Attempt, error) {
	action, attempt, err := d.r.actions.EnsureNativeDelegation(ctx, d.ensureInput())
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if err := d.validatePair(action, attempt); err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	if d.actionID != "" && (action.ActionID != d.actionID || attempt.AttemptID != d.attemptID) {
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("canonical native delegation identity changed")
	}
	if d.actionID == "" {
		d.actionID, d.attemptID = action.ActionID, attempt.AttemptID
		d.corr.actionID, d.corr.attemptID = string(action.ActionID), string(attempt.AttemptID)
	}
	return action, attempt, nil
}

func (d *delegation) validatePair(action domainaction.Action, attempt domainaction.Attempt) error {
	wantMode := domainaction.NativeDelegationModeOpenStart
	wantReason := domainaction.AttemptStartReasonFirst
	if d.resumeSource != nil {
		wantMode = domainaction.NativeDelegationModeResume
		wantReason = domainaction.AttemptStartReasonExplicitResume
	}
	if action.TaskID != d.parent.TaskID || action.RunID != d.parent.RunID || action.Kind != domainaction.KindDelegation ||
		action.Name != delegationActionName || action.CurrentAttemptID != attempt.AttemptID || attempt.ActionID != action.ActionID ||
		attempt.StartReason != wantReason || attempt.NativeDelegation == nil || attempt.NativeDelegation.EffectiveMode() != wantMode ||
		attempt.NativeDelegation.ExpectedCriteriaRevision != d.task.ExpectedCriteriaRevision ||
		!nativeResumeSourceMatches(attempt.NativeDelegation.ResumeSource, actionResumeSource(d.resumeSource)) {
		return errors.New("native delegation pair is not the canonical first attempt")
	}
	return nil
}

func (d *delegation) ensureInput() actionmanager.EnsureNativeDelegationInput {
	input := actionmanager.EnsureNativeDelegationInput{
		TaskID: d.parent.TaskID, RunID: d.parent.RunID, Mode: domainaction.NativeDelegationModeOpenStart,
		ExpectedCriteriaRevision: d.task.ExpectedCriteriaRevision, InputReference: d.reference,
	}
	if d.resumeSource != nil {
		input.Mode = domainaction.NativeDelegationModeResume
		input.InputReference = nil
		input.ResumeSource = actionResumeSource(d.resumeSource)
	}
	return input
}

func (d *delegation) completedResult(ctx context.Context) (agent.NativeCodingResult, bool, error) {
	action, attempt, err := d.ensurePair(ctx)
	if err != nil {
		return agent.NativeCodingResult{}, false, d.unknown("action_owner_unavailable")
	}
	return d.completedResultFromPair(action, attempt)
}

func (d *delegation) completedResultFromPair(action domainaction.Action, attempt domainaction.Attempt) (agent.NativeCodingResult, bool, error) {
	if len(attempt.NativeDelegation.RunResult) != 0 {
		start, err := acceptedChildResult(attempt)
		if err != nil {
			return agent.NativeCodingResult{}, false, d.unknown("stored_start_invalid")
		}
		runResult, err := decodeCanonical[protocol.RunResult](attempt.NativeDelegation.RunResult)
		if err != nil || !sameRunIdentity(start, runResult) {
			return agent.NativeCodingResult{}, false, d.unknown("stored_run_result_invalid")
		}
		if d.project(runResult, start).Accepted() && !storedResultProofValid(attempt, d.task.ExpectedCriteriaRevision, runResult, attempt.NativeDelegation.RunResult) {
			return agent.NativeCodingResult{}, false, d.unknown("stored_criteria_proof_invalid")
		}
		if action.IsOpen() || attempt.IsActive() {
			return agent.NativeCodingResult{}, false, d.unknown("stored_terminal_state_conflict")
		}
		expectedAttempt, expectedAction := terminalActionStatuses(runResult)
		if attempt.Status != expectedAttempt || action.Status != expectedAction {
			return agent.NativeCodingResult{}, false, d.unknown("stored_terminal_status_conflict")
		}
		return d.project(runResult, start), true, nil
	}
	if !action.IsOpen() || !attempt.IsActive() {
		if code := persistedRejection(attempt); code != "" {
			return agent.NativeCodingResult{}, false, rejected("harness_rejected_" + code)
		}
		return agent.NativeCodingResult{}, false, d.unknown("delegation_already_terminal")
	}
	return agent.NativeCodingResult{}, false, nil
}

func (d *delegation) refusePersistedUnknown(ctx context.Context) error {
	_, attempt, err := d.ensurePair(ctx)
	if err != nil {
		return d.unknown("action_owner_unavailable")
	}
	for _, slot := range []*domainaction.NativeMutation{attempt.NativeDelegation.Open, attempt.NativeDelegation.Start, attempt.NativeDelegation.Resume} {
		if slot != nil && slot.Status == domainaction.NativeMutationStatusDeliveryUnknown {
			return d.unknown("persisted_delivery_unknown")
		}
	}
	return nil
}

func (d *delegation) openInput() protocol.SessionOpenInput {
	return protocol.SessionOpenInput{
		WorkspacePath: d.r.settings.Workspace.Path,
		Binding: protocol.Binding{
			Kind: "alias", Selector: d.r.settings.Binding.Selector, ProfileRevision: d.r.settings.Binding.ProfileRevision,
			AgentID: protocol.Str(shiroAgentID), ExecutionRole: protocol.Str(d.r.settings.Binding.ExecutionRole),
		},
		PolicyRef: d.r.settings.Workspace.PolicyRef, ExecutionMode: d.r.settings.Workspace.ExecutionMode,
		IdempotencyKey: d.key("open"),
	}
}

func (d *delegation) startInput(ctx context.Context, session protocol.SessionInfo) (protocol.StartInput, error) {
	proof, err := d.acceptedOriginProof(ctx, session.ThreadID)
	if err != nil {
		return protocol.StartInput{}, err
	}
	blocks := make([]protocol.ContextBlock, 0, len(d.blocks))
	for _, block := range d.blocks {
		converted := protocol.ContextBlock{Kind: string(block.Kind), Text: block.Text, Revision: block.Revision}
		if block.Source != nil {
			converted.Source = &protocol.SourceRef{
				Owner: block.Source.Owner, SourceID: block.Source.SourceID, RawHash: block.Source.RawHash,
				ProjectionVersion: block.Source.ProjectionVersion,
				Range:             protocol.ByteRange{Start: block.Source.Range.Start, End: block.Source.Range.End},
				Origin:            block.Source.Origin, Sequence: block.Source.Sequence,
			}
		}
		blocks = append(blocks, converted)
	}
	turnID, actionID, attemptID := string(d.input.TurnID()), string(d.actionID), string(d.attemptID)
	return protocol.StartInput{
		ThreadID:      session.ThreadID,
		Input:         protocol.InputMessage{Text: d.userText, OriginProof: proof},
		ContextBlocks: blocks,
		Upstream: &protocol.Upstream{
			Owner: upstreamOwner, TaskID: string(d.parent.TaskID), TraceID: string(d.parent.TraceID),
			TurnID: &turnID, ActionID: &actionID, AttemptID: &attemptID,
		},
		ExpectedContextRevision: session.ContextRevision, ExpectedControlRevision: session.ControlRevision,
		IdempotencyKey: d.key("start"), Limits: d.r.settings.Limits,
	}, nil
}

func (d *delegation) openSession(ctx context.Context, initial Client) (protocol.SessionOpenResult, Client, error) {
	return sendMutation(d, ctx, initial, domainaction.NativeMutationSlotOpen, "open", func(context.Context) (protocol.SessionOpenInput, error) {
		return d.openInput(), nil
	},
		func(ctx context.Context, c Client, input protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
			return c.SessionOpen(ctx, input)
		}, func(_ protocol.SessionOpenInput, result protocol.SessionOpenResult) error {
			if result.Session.ThreadID == "" || result.ReceiptID == "" {
				return errors.New("session/open result identity is incomplete")
			}
			return nil
		}, &d.openInitiated)
}

func (d *delegation) startRun(ctx context.Context, initial Client, session protocol.SessionInfo) (protocol.StartResult, Client, error) {
	return sendMutation(d, ctx, initial, domainaction.NativeMutationSlotStart, "start", func(ctx context.Context) (protocol.StartInput, error) {
		return d.startInput(ctx, session)
	}, func(ctx context.Context, c Client, input protocol.StartInput) (protocol.StartResult, error) {
		return c.TurnStart(ctx, input)
	}, func(input protocol.StartInput, result protocol.StartResult) error {
		if !result.Accepted || result.SessionID == "" || result.ThreadID != input.ThreadID || result.TaskID == "" || result.RunID == "" || result.ReceiptID == "" {
			return errors.New("turn/start result identity is incomplete")
		}
		return nil
	}, &d.startInitiated)
}

func sendMutation[In protocol.Message, Out protocol.Message](
	d *delegation,
	ctx context.Context,
	initial Client,
	slotName domainaction.NativeMutationSlot,
	suffix string,
	build func(context.Context) (In, error),
	call func(context.Context, Client, In) (Out, error),
	validate func(In, Out) error,
	initiated *bool,
) (Out, Client, error) {
	var zero Out
	c := initial
	for send := 1; send <= maxSends; send++ {
		if ctx.Err() != nil {
			return zero, c, d.unknown(string(slotName) + "_cancelled")
		}
		if send > 1 {
			if err := waitBackoff(ctx, d.r.backoff(send-1)); err != nil {
				return zero, c, d.unknown(string(slotName) + "_retry_cancelled")
			}
		}
		var err error
		c, err = d.r.acquire(ctx)
		if err != nil {
			return zero, c, d.unknown(string(slotName) + "_client_unavailable")
		}
		action, attempt, err := d.ensurePair(ctx)
		if err != nil {
			return zero, c, d.unknown(string(slotName) + "_action_owner_unavailable")
		}
		stored := nativeMutation(attempt, slotName)
		if stored != nil {
			if stored.Key != d.key(suffix) {
				return zero, c, d.unknown(string(slotName) + "_stored_key_conflict")
			}
			switch stored.Status {
			case domainaction.NativeMutationStatusAccepted:
				result, decodeErr := decodeCanonical[Out](stored.Result)
				if decodeErr != nil {
					return zero, c, d.unknown(string(slotName) + "_stored_result_invalid")
				}
				return result, c, nil
			case domainaction.NativeMutationStatusRejected:
				return zero, c, rejected("harness_rejected_" + stored.ErrorCode)
			case domainaction.NativeMutationStatusDeliveryUnknown:
				if !*initiated {
					return zero, c, d.unknown("persisted_delivery_unknown")
				}
			case domainaction.NativeMutationStatusPrepared:
			default:
				return zero, c, d.unknown(string(slotName) + "_stored_state_invalid")
			}
		} else if slotName == domainaction.NativeMutationSlotStart {
			open := attempt.NativeDelegation.Open
			if open == nil || open.Status != domainaction.NativeMutationStatusAccepted {
				return zero, c, d.unknown("start_without_accepted_open")
			}
		}
		_ = action

		stepCtx, cancel := context.WithTimeout(ctx, d.r.settings.StepTimeout)
		var callbackStarted, sendAttempted bool
		var accepted *Out
		var rejectedCode string
		var remoteRejectionAfterUnknown bool
		var rpcErr error
		var innerErr error
		fenceErr := d.r.tasks.ExecuteRunEffect(stepCtx, d.parent.TaskID, d.parent.RunID, shiroActorID, func(effectCtx context.Context) error {
			callbackStarted = true
			freshAction, freshAttempt, err := d.ensurePair(effectCtx)
			if err != nil {
				innerErr = err
				return err
			}
			if freshAction.IsOpen() == false || freshAttempt.IsActive() == false {
				innerErr = errors.New("native delegation is no longer active")
				return innerErr
			}
			fresh := nativeMutation(freshAttempt, slotName)
			var input In
			if fresh == nil {
				input, err = build(effectCtx)
				if err != nil {
					innerErr = err
					return err
				}
				payload, encodeErr := protocol.Encode(input)
				if encodeErr != nil {
					innerErr = encodeErr
					return encodeErr
				}
				freshAction, freshAttempt, err = d.r.actions.PrepareNativeMutation(effectCtx, actionmanager.PrepareNativeMutationInput{
					ActionID: d.actionID, AttemptID: d.attemptID, Slot: slotName, Key: d.key(suffix), Payload: payload,
				})
				if err != nil || d.validatePair(freshAction, freshAttempt) != nil {
					innerErr = errors.New("native mutation could not be frozen")
					return innerErr
				}
				fresh = nativeMutation(freshAttempt, slotName)
			}
			if fresh == nil || fresh.Key != d.key(suffix) {
				innerErr = errors.New("native mutation slot is missing or has a different key")
				return innerErr
			}
			priorDeliveryUnknown := fresh.Status == domainaction.NativeMutationStatusDeliveryUnknown
			switch fresh.Status {
			case domainaction.NativeMutationStatusAccepted:
				result, decodeErr := decodeCanonical[Out](fresh.Result)
				if decodeErr != nil {
					innerErr = decodeErr
					return decodeErr
				}
				accepted = &result
				return nil
			case domainaction.NativeMutationStatusRejected:
				rejectedCode = fresh.ErrorCode
				return nil
			case domainaction.NativeMutationStatusDeliveryUnknown:
				if !*initiated {
					innerErr = errStoredDeliveryUnknown
					return innerErr
				}
			case domainaction.NativeMutationStatusPrepared:
			default:
				innerErr = errors.New("native mutation slot has invalid status")
				return innerErr
			}
			input, err = decodeCanonical[In](fresh.Payload)
			if err != nil {
				innerErr = err
				return err
			}
			wireBytes, err := protocol.Encode(input)
			if err != nil || !bytes.Equal(wireBytes, fresh.Payload) {
				innerErr = errors.New("native mutation bytes do not match the pinned SDK encoder")
				return innerErr
			}
			_, _, err = d.r.actions.MarkNativeMutationDeliveryUnknown(effectCtx, d.actionID, d.attemptID, slotName)
			if err != nil {
				innerErr = err
				return err
			}
			*initiated = true
			sendAttempted = true
			out, callErr := call(effectCtx, c, input)
			rpcErr = callErr
			if callErr != nil {
				if errors.Is(callErr, client.ErrOutcomeUnknown) || protocol.CodeOf(callErr) == protocol.CodePersistenceUncertain {
					if persistErr := d.recordObservationWith(effectCtx, string(slotName)+"_delivery_unknown", failureCode(callErr)); persistErr != nil {
						innerErr = persistErr
						return persistErr
					}
					return nil
				}
				var remote *client.RemoteError
				if errors.As(callErr, &remote) {
					code := safeObservationCode(protocol.CodeOf(callErr))
					if priorDeliveryUnknown {
						remoteRejectionAfterUnknown = true
						if persistErr := d.recordObservationWith(effectCtx, string(slotName)+"_remote_rejection_after_unknown", code); persistErr != nil {
							innerErr = persistErr
							return persistErr
						}
						return nil
					}
					_, _, persistErr := d.r.actions.RecordNativeMutationOutcome(effectCtx, actionmanager.RecordNativeMutationOutcomeInput{
						ActionID: d.actionID, AttemptID: d.attemptID, Slot: slotName,
						Status: domainaction.NativeMutationStatusRejected, ErrorCode: code,
					})
					if persistErr != nil {
						innerErr = persistErr
						return persistErr
					}
					rejectedCode = code
					return nil
				}
				if persistErr := d.recordObservationWith(effectCtx, string(slotName)+"_delivery_unknown", failureCode(callErr)); persistErr != nil {
					innerErr = persistErr
					return persistErr
				}
				return nil
			}
			resultBytes, err := protocol.Encode(out)
			if err == nil {
				err = validate(input, out)
			}
			if err != nil {
				rpcErr = err
				if persistErr := d.recordObservationWith(effectCtx, string(slotName)+"_invalid_owner_response", "invalid_owner_response"); persistErr != nil {
					innerErr = persistErr
					return persistErr
				}
				return nil
			}
			_, _, err = d.r.actions.RecordNativeMutationOutcome(effectCtx, actionmanager.RecordNativeMutationOutcomeInput{
				ActionID: d.actionID, AttemptID: d.attemptID, Slot: slotName,
				Status: domainaction.NativeMutationStatusAccepted, Result: resultBytes,
			})
			if err != nil {
				innerErr = err
				return err
			}
			accepted = &out
			return nil
		})
		cancel()
		if accepted != nil {
			if fenceErr != nil {
				return zero, c, d.unknown(string(slotName) + "_fence_release_uncertain")
			}
			return *accepted, c, nil
		}
		if rejectedCode != "" {
			return zero, c, rejected("harness_rejected_" + rejectedCode)
		}
		if errors.Is(innerErr, errStoredDeliveryUnknown) {
			return zero, c, d.unknown("persisted_delivery_unknown")
		}
		if errors.Is(innerErr, agent.ErrNativeCodingBlocked) || errors.Is(innerErr, agent.ErrNativeCodingRejected) {
			return zero, c, innerErr
		}
		if innerErr != nil {
			if sendAttempted || callbackStarted {
				return zero, c, d.unknown(string(slotName) + "_owner_state_uncertain")
			}
			return zero, c, blocked(codeTaskUnavailable)
		}
		if remoteRejectionAfterUnknown {
			return zero, c, d.unknown(string(slotName) + "_remote_rejection_after_unknown")
		}
		if fenceErr != nil {
			if sendAttempted || *initiated {
				return zero, c, d.unknown(string(slotName) + "_fence_uncertain")
			}
			return zero, c, blocked(codeTaskUnavailable)
		}
		if rpcErr == nil {
			return zero, c, d.unknown(string(slotName) + "_response_not_persisted")
		}
		if send == maxSends || ctx.Err() != nil {
			return zero, c, d.unknown(string(slotName) + "_delivery_unknown")
		}
	}
	return zero, c, d.unknown(string(slotName) + "_delivery_unknown")
}

func (d *delegation) awaitRun(ctx context.Context, c Client, start protocol.StartResult) (protocol.RunResult, context.Context, context.CancelFunc, error) {
	result, err := c.AwaitRun(ctx, start.RunID, client.AwaitOptions{InterruptKeyPrefix: ""})
	if err == nil {
		if !sameRunIdentity(start, result) {
			_ = d.recordObservationWith(ctx, "run_result_identity_invalid", "invalid_owner_response")
			return protocol.RunResult{}, nil, nil, d.unknown("run_result_identity_invalid")
		}
		if ctx.Err() != nil {
			grace := d.r.settings.CancelGrace
			if grace <= 0 {
				grace = client.DefaultCancelGrace
			}
			graceCtx, cancelGrace := context.WithTimeout(context.WithoutCancel(ctx), grace)
			return result, graceCtx, cancelGrace, nil
		}
		return result, ctx, func() {}, nil
	}
	if ctx.Err() == nil {
		_ = d.recordObservationWith(ctx, "await_unknown", failureCode(err))
		d.r.log.emit("error", eventDelegateUnknown, d.logFields(map[string]any{"phase": "await"}))
		return protocol.RunResult{}, nil, nil, d.unknown("run_end_unavailable")
	}

	grace := d.r.settings.CancelGrace
	if grace <= 0 {
		grace = client.DefaultCancelGrace
	}
	graceCtx, cancelGrace := context.WithTimeout(context.WithoutCancel(ctx), grace)
	stepCtx, cancelStep := context.WithTimeout(graceCtx, d.r.settings.StepTimeout)
	var interruptReceipt protocol.InterruptReceipt
	var interruptErr error
	fenceErr := d.r.tasks.ExecuteRunEffect(stepCtx, d.parent.TaskID, d.parent.RunID, shiroActorID, func(effectCtx context.Context) error {
		interruptReceipt, interruptErr = c.InterruptRun(effectCtx, start.RunID, d.key("stop"))
		return interruptErr
	})
	cancelStep()
	if interruptErr != nil || fenceErr != nil {
		code := failureCode(interruptErr)
		if interruptErr == nil {
			code = failureCode(fenceErr)
		}
		_ = d.recordObservationWith(graceCtx, "interrupt_unknown", code)
	} else if interruptReceipt.RunID != start.RunID || !interruptReceipt.SignalRecorded {
		code := safeObservationCode(interruptReceipt.Code)
		if code == "" {
			code = "signal_not_confirmed"
		}
		_ = d.recordObservationWith(graceCtx, "interrupt_unknown", code)
	} else {
		_ = d.recordObservationWith(graceCtx, "interrupt_signal_recorded", interruptReceipt.Code)
	}

	result, err = c.AwaitRun(graceCtx, start.RunID, client.AwaitOptions{InterruptKeyPrefix: ""})
	if err != nil {
		_ = d.recordObservationWith(graceCtx, "cancel_wait_unknown", failureCode(err))
		d.r.log.emit("error", eventDelegateUnknown, d.logFields(map[string]any{"phase": "cancel_wait"}))
		cancelGrace()
		return protocol.RunResult{}, nil, nil, d.unknown("run_end_after_cancel_unavailable")
	}
	if !sameRunIdentity(start, result) {
		_ = d.recordObservationWith(graceCtx, "cancel_result_identity_invalid", "invalid_owner_response")
		cancelGrace()
		return protocol.RunResult{}, nil, nil, d.unknown("run_result_identity_invalid")
	}
	return result, graceCtx, cancelGrace, nil
}

func (d *delegation) complete(ctx context.Context, c Client, start protocol.StartResult, runResult protocol.RunResult) (agent.NativeCodingResult, error) {
	if !sameRunIdentity(start, runResult) {
		if d.resumeClaim == nil {
			_ = d.recordObservationWith(ctx, "run_result_identity_invalid", "invalid_owner_response")
		}
		return agent.NativeCodingResult{}, d.unknown("run_result_identity_invalid")
	}
	runBytes, err := protocol.Encode(runResult)
	if err != nil {
		if d.resumeClaim == nil {
			_ = d.recordObservationWith(ctx, "run_result_invalid", "invalid_owner_response")
		}
		return agent.NativeCodingResult{}, d.unknown("run_result_invalid")
	}
	attemptStatus, actionStatus := terminalActionStatuses(runResult)
	var proof *domainaction.NativeDelegationProof
	var proofErr error
	if nativeRunResultAccepted(runResult) {
		proofCtx, cancelProof := context.WithTimeout(ctx, d.r.settings.StepTimeout)
		proof, proofErr = d.proveAcceptedRun(proofCtx, c, start, runResult, runBytes)
		cancelProof()
		if proofErr != nil {
			attemptStatus, actionStatus = domainaction.AttemptStatusFailed, domainaction.StatusFailed
		}
	}
	summary := summarizeRunResult(runResult, start)
	// Once AwaitRun has delivered a terminal result, preserve that exact result
	// through one bounded Task-owner finalization attempt even if the caller
	// cancels while proof reads are in flight. WithoutCancel retains the trusted
	// execution-scope values; ExecuteRunEffect still enforces the durable Task,
	// Run, and current-writer fence before the existing Action transaction can
	// save anything.
	stepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.r.settings.StepTimeout)
	var alreadyCompleted *agent.NativeCodingResult
	persist := func(effectCtx context.Context) error {
		var action domainaction.Action
		var attempt domainaction.Attempt
		var err error
		if d.resumeClaim != nil {
			action, attempt, err = d.readResumePair(effectCtx)
		} else {
			action, attempt, err = d.ensurePair(effectCtx)
		}
		if err != nil {
			return err
		}
		if len(attempt.NativeDelegation.RunResult) != 0 {
			stored, done, err := d.completedResultFromPair(action, attempt)
			if err != nil || !done || !bytes.Equal(attempt.NativeDelegation.RunResult, runBytes) {
				return errors.New("stored terminal Action does not match the known RunResult")
			}
			alreadyCompleted = &stored
			return nil
		}
		acceptedStart, err := acceptedChildResult(attempt)
		if err != nil || acceptedStart.RunID != start.RunID || acceptedStart.TaskID != start.TaskID || acceptedStart.ThreadID != start.ThreadID {
			return errors.New("terminal RunResult does not match accepted child Run")
		}
		if d.resumeSource != nil {
			resumed, resumeErr := d.validateAcceptedResumeMutation(attempt)
			if resumeErr != nil || resumed.RunID != start.RunID || resumed.TaskID != start.TaskID || resumed.ThreadID != start.ThreadID {
				return errors.New("terminal RunResult does not match the accepted Resume mutation")
			}
		}
		_, _, err = d.r.actions.CompleteNativeDelegation(effectCtx, actionmanager.CompleteNativeDelegationInput{
			ActionID: d.actionID, AttemptID: d.attemptID, RunResult: runBytes, Proof: proof,
			AttemptStatus: attemptStatus, ActionStatus: actionStatus, Summary: summary,
		})
		return err
	}
	if d.resumeClaim != nil {
		err = d.r.tasks.ExecuteNativeOPSResumeActionEffect(
			stepCtx, d.parent.TaskID, d.parent.RunID, shiroActorID, *d.resumeClaim, persist,
		)
	} else {
		err = d.r.tasks.ExecuteRunEffect(stepCtx, d.parent.TaskID, d.parent.RunID, shiroActorID, persist)
	}
	cancel()
	if err != nil {
		if d.resumeClaim == nil {
			_ = d.recordObservationWith(ctx, "final_persistence_unknown", failureCode(err))
		}
		d.r.log.emit("error", eventDelegateUnknown, d.logFields(map[string]any{"phase": "complete"}))
		return agent.NativeCodingResult{}, d.unknown("final_persistence_unknown")
	}
	if alreadyCompleted != nil {
		return *alreadyCompleted, nil
	}
	if proofErr != nil {
		if d.resumeClaim == nil {
			_ = d.recordObservationWith(ctx, "criteria_proof_unavailable", "owner_evidence_unconfirmed")
		}
		return agent.NativeCodingResult{}, d.unknown("criteria_proof_unavailable")
	}
	projected := d.project(runResult, start)
	if projected.Accepted() && proof == nil {
		return agent.NativeCodingResult{}, d.unknown("criteria_proof_unavailable")
	}
	return projected, nil
}

func (d *delegation) readResumePair(ctx context.Context) (domainaction.Action, domainaction.Attempt, error) {
	reader, ok := d.r.actions.(ResumeActionReader)
	if !ok {
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("Resume Action owner is read-only unavailable")
	}
	actions, err := reader.ListActions(ctx, domainaction.Filter{TaskID: d.parent.TaskID, RunID: d.parent.RunID})
	if err != nil {
		return domainaction.Action{}, domainaction.Attempt{}, err
	}
	var selected *domainaction.Action
	for index := range actions {
		if !isNativeResumeAction(actions[index]) {
			continue
		}
		if selected != nil {
			return domainaction.Action{}, domainaction.Attempt{}, errors.New("Resume Action identity is ambiguous")
		}
		item := actions[index]
		selected = &item
	}
	if selected == nil || d.actionID != "" && selected.ActionID != d.actionID {
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("Resume Action identity changed")
	}
	attempts, err := reader.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: selected.ActionID})
	if err != nil || len(attempts) != 1 {
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("Resume Attempt is unavailable or ambiguous")
	}
	attempt := attempts[0]
	if d.attemptID != "" && attempt.AttemptID != d.attemptID || selected.CurrentAttemptID != attempt.AttemptID || d.validatePair(*selected, attempt) != nil {
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("Resume Action and Attempt no longer match the saved claim")
	}
	if d.actionID == "" {
		d.actionID, d.attemptID = selected.ActionID, attempt.AttemptID
		d.corr.actionID, d.corr.attemptID = string(selected.ActionID), string(attempt.AttemptID)
	}
	return *selected, attempt, nil
}

func (d *delegation) project(runResult protocol.RunResult, start protocol.StartResult) agent.NativeCodingResult {
	return agent.NativeCodingResult{
		Status: agent.NativeCodingRunStatus(runResult.Status), Code: runResult.Code, FinalText: runResult.FinalText,
		Verification: agent.NativeCodingVerification(runResult.Verification.Status), Resumable: runResult.Resumable,
		HarnessTask: harnessRef(runResult.TaskID), HarnessRun: harnessRef(runResult.RunID), HarnessReceipt: harnessRef(start.ReceiptID),
		ActionID: d.actionID, AttemptID: d.attemptID,
	}
}

func (d *delegation) reportMutationError(ctx context.Context, phase string, err error) error {
	if errors.Is(err, agent.ErrNativeCodingRejected) || errors.Is(err, agent.ErrNativeCodingBlocked) || errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		if errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
			d.r.log.emit("error", eventDelegateUnknown, d.logFields(map[string]any{"phase": phase}))
		}
		return err
	}
	_ = d.recordObservation(context.WithoutCancel(ctx), phase+"_owner_state_unknown", failureCode(err))
	return d.unknown(phase + "_owner_state_unknown")
}

func (d *delegation) unknown(code string) error {
	return fmt.Errorf("%w: %s", agent.ErrNativeCodingOutcomeUnknown, safeObservationCode(code))
}

func (d *delegation) recordObservation(ctx context.Context, kind, code string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	obsCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.r.settings.StepTimeout)
	defer cancel()
	return d.recordObservationWith(obsCtx, kind, code)
}

func (d *delegation) recordObservationWith(ctx context.Context, kind, code string) error {
	_, _, err := d.r.actions.RecordNativeObservation(ctx, actionmanager.RecordNativeObservationInput{
		ActionID: d.actionID, AttemptID: d.attemptID,
		Observation: domainaction.NativeObservation{Kind: safeObservationCode(kind), Code: safeObservationCode(code), ObservedAt: d.r.now().UTC()},
	})
	return err
}

func nativeMutation(attempt domainaction.Attempt, slot domainaction.NativeMutationSlot) *domainaction.NativeMutation {
	if attempt.NativeDelegation == nil {
		return nil
	}
	switch slot {
	case domainaction.NativeMutationSlotOpen:
		return attempt.NativeDelegation.Open
	case domainaction.NativeMutationSlotStart:
		return attempt.NativeDelegation.Start
	case domainaction.NativeMutationSlotResume:
		return attempt.NativeDelegation.Resume
	default:
		return nil
	}
}

func decodeCanonical[T protocol.Message](raw []byte) (T, error) {
	var zero T
	decoded, err := protocol.Decode[T](raw)
	if err != nil {
		return zero, err
	}
	encoded, err := protocol.Encode(decoded)
	if err != nil || !bytes.Equal(encoded, raw) {
		return zero, errors.New("stored protocol bytes are not canonical under the pinned SDK")
	}
	return decoded, nil
}

// HasAcceptedStoredRunResult reports whether a persisted native Attempt has a
// canonical, accepted Start and a matching completed+passed RunResult. Runtime
// restart recovery uses this before treating the owning CORE Run as succeeded.
func HasAcceptedStoredRunResult(attempt domainaction.Attempt, expectedCriteriaRevision string) bool {
	if attempt.Status != domainaction.AttemptStatusSucceeded || attempt.NativeDelegation == nil || len(attempt.NativeDelegation.RunResult) == 0 {
		return false
	}
	start, err := acceptedChildResult(attempt)
	if err != nil || !start.Accepted {
		return false
	}
	result, err := decodeCanonical[protocol.RunResult](attempt.NativeDelegation.RunResult)
	if err != nil || !sameRunIdentity(start, result) {
		return false
	}
	return storedResultProofValid(attempt, expectedCriteriaRevision, result, attempt.NativeDelegation.RunResult)
}

func storedResultProofValid(attempt domainaction.Attempt, expectedCriteriaRevision string, result protocol.RunResult, resultBytes []byte) bool {
	if attempt.NativeDelegation == nil || !nativeRunResultAccepted(result) || !domaintask.ValidCriteriaRevision(expectedCriteriaRevision) ||
		attempt.NativeDelegation.ExpectedCriteriaRevision != expectedCriteriaRevision || result.Verification.CriteriaRevision == nil ||
		*result.Verification.CriteriaRevision != expectedCriteriaRevision || attempt.NativeDelegation.Proof == nil {
		return false
	}
	proof := attempt.NativeDelegation.Proof
	if proof.Validate() != nil || proof.ExpectedCriteriaRevision != expectedCriteriaRevision {
		return false
	}
	hash := sha256.Sum256(resultBytes)
	if hex.EncodeToString(hash[:]) != proof.RunResultSHA256 || len(proof.Evidence) != len(result.Verification.EvidenceIDs) {
		return false
	}
	for index, evidence := range proof.Evidence {
		if evidence.EvidenceID != result.Verification.EvidenceIDs[index] {
			return false
		}
	}
	return uniqueNonempty(result.Verification.EvidenceIDs) && uniqueNonempty(result.EvidenceIDs) &&
		containsAll(result.EvidenceIDs, result.Verification.EvidenceIDs) && len(result.UnresolvedActionIDs) == 0
}

func sameRunIdentity(start protocol.StartResult, result protocol.RunResult) bool {
	return start.RunID != "" && start.TaskID != "" && result.RunID == start.RunID && result.TaskID == start.TaskID
}

func terminalActionStatuses(result protocol.RunResult) (domainaction.AttemptStatus, domainaction.Status) {
	if nativeRunResultAccepted(result) {
		return domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded
	}
	if result.Status == string(agent.NativeRunCancelled) {
		return domainaction.AttemptStatusCancelled, domainaction.StatusCancelled
	}
	return domainaction.AttemptStatusFailed, domainaction.StatusFailed
}

func persistedRejection(attempt domainaction.Attempt) string {
	if attempt.NativeDelegation == nil {
		return ""
	}
	for _, slot := range []*domainaction.NativeMutation{attempt.NativeDelegation.Open, attempt.NativeDelegation.Start, attempt.NativeDelegation.Resume} {
		if slot != nil && slot.Status == domainaction.NativeMutationStatusRejected {
			return slot.ErrorCode
		}
	}
	return ""
}

func safeObservationCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) > 128 || strings.ContainsAny(value, " \t\r\n") {
		return "unknown"
	}
	return value
}

func failureCode(err error) string {
	if err == nil {
		return ""
	}
	if code := protocol.CodeOf(err); code != "" {
		return safeObservationCode(code)
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, client.ErrStopping):
		return "client_stopping"
	case errors.Is(err, client.ErrClosed):
		return "client_closed"
	case errors.Is(err, client.ErrOutcomeUnknown):
		return "delivery_unknown"
	default:
		return "transport_error"
	}
}

func waitBackoff(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func classifyRefusal(err error) error {
	if errors.Is(err, agent.ErrNativeCodingBlocked) || errors.Is(err, agent.ErrNativeCodingRejected) || errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		return err
	}
	var remote *client.RemoteError
	if errors.As(err, &remote) {
		return rejected("harness_rejected_" + safeObservationCode(protocol.CodeOf(err)))
	}
	if protocol.CodeOf(err) == protocol.CodeInvalidParams || protocol.CodeOf(err) == protocol.CodeInvalidRequest {
		return rejected("request_invalid")
	}
	return blocked("harness_call_failed")
}

func refusalDetail(err error) string {
	for _, sentinel := range []error{agent.ErrNativeCodingRejected, agent.ErrNativeCodingBlocked, agent.ErrNativeCodingOutcomeUnknown} {
		if errors.Is(err, sentinel) {
			return strings.TrimPrefix(err.Error(), sentinel.Error()+": ")
		}
	}
	return "refused"
}

func summarize(result agent.NativeCodingResult) string {
	return fmt.Sprintf("native_harness status=%s code=%s verification=%s resumable=%t harness_task=%s/%s harness_run=%s/%s harness_receipt=%s/%s",
		result.Status, orDash(result.Code), result.Verification, result.Resumable,
		result.HarnessTask.Owner, result.HarnessTask.ID, result.HarnessRun.Owner, result.HarnessRun.ID,
		result.HarnessReceipt.Owner, result.HarnessReceipt.ID)
}

func summarizeRunResult(result protocol.RunResult, start protocol.StartResult) string {
	return fmt.Sprintf("native_harness status=%s code=%s verification=%s resumable=%t harness_task=%s/%s harness_run=%s/%s harness_receipt=%s/%s",
		result.Status, orDash(result.Code), result.Verification.Status, result.Resumable,
		agent.HarnessOwner, result.TaskID, agent.HarnessOwner, result.RunID, agent.HarnessOwner, start.ReceiptID)
}

func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
