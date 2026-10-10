package delegation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	harnessclient "github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestResumeUnknownDeliveryRetriesFrozenPayloadWithinBound(t *testing.T) {
	d, delegation, ctx, client := newPreparedResumeTest(t)
	client.onResume = func(_ context.Context, n int, input protocol.ResumeInput) (protocol.ResumeResult, error) {
		if n == 1 {
			return protocol.ResumeResult{}, errors.Join(harnessclient.ErrOutcomeUnknown, context.DeadlineExceeded)
		}
		return resumeTestResult(client, input), nil
	}

	result, _, err := delegation.resumeChildRun(ctx, client)
	if err != nil || !resumeResultMatches(result, delegation.resumeSource) {
		t.Fatalf("same-key retry should recover the accepted Resume result: result=%+v err=%v", result, err)
	}
	if len(client.resumes) != 2 || len(client.resumes) > maxSends {
		t.Fatalf("uncertain Resume should retry within the fixed send bound: calls=%d max=%d", len(client.resumes), maxSends)
	}
	if !reflect.DeepEqual(client.resumes[0], client.resumes[1]) {
		t.Fatalf("uncertain Resume must reuse the exact input: first=%+v retry=%+v", client.resumes[0], client.resumes[1])
	}
	wire, err := protocol.Encode(client.resumes[0])
	if err != nil || !bytes.Equal(wire, d.rec.attempt.NativeDelegation.Resume.Payload) {
		t.Fatalf("retries must match the frozen SDK bytes: wire=%q stored=%q err=%v", wire, d.rec.attempt.NativeDelegation.Resume.Payload, err)
	}
	if len(client.opens) != 0 || len(client.starts) != 0 {
		t.Fatalf("Resume retries must not Open or Start: opens=%d starts=%d", len(client.opens), len(client.starts))
	}
	if stored := d.rec.attempt.NativeDelegation.Resume; stored == nil || stored.Status != domainaction.NativeMutationStatusAccepted {
		t.Fatalf("the retry result must be persisted as accepted: %+v", stored)
	}
}

func TestResumeCancellationKeepsUnknownAndLaterInvocationDoesNotResend(t *testing.T) {
	d, delegation, ctx, client := newPreparedResumeTest(t)
	ctx, cancel := context.WithCancel(ctx)
	client.onResume = func(_ context.Context, n int, _ protocol.ResumeInput) (protocol.ResumeResult, error) {
		if n != 1 {
			t.Fatalf("cancelled unknown delivery must not send again, call=%d", n)
		}
		cancel()
		return protocol.ResumeResult{}, errors.Join(harnessclient.ErrOutcomeUnknown, context.DeadlineExceeded)
	}

	_, _, err := delegation.resumeChildRun(ctx, client)
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("cancelled possibly delivered Resume must remain unknown: %v", err)
	}
	if len(client.resumes) != 1 {
		t.Fatalf("cancellation after uncertainty must stop further sends: calls=%d", len(client.resumes))
	}
	stored := d.rec.attempt.NativeDelegation.Resume
	if stored == nil || stored.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("the possible delivery must stay persisted as unknown: %+v", stored)
	}

	// A process restart has no local knowledge that permits another delivery.
	delegation.resumeInitiated = false
	_, _, err = delegation.resumeChildRun(context.Background(), client)
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) || len(client.resumes) != 1 {
		t.Fatalf("later invocation must preserve unknown without resending: calls=%d err=%v", len(client.resumes), err)
	}
}

func TestResumeRemoteRejectionIsDefiniteAfterPersistenceAndReplayDoesNotResend(t *testing.T) {
	d, delegation, ctx, client := newPreparedResumeTest(t)
	client.onResume = func(context.Context, int, protocol.ResumeInput) (protocol.ResumeResult, error) {
		return protocol.ResumeResult{}, &harnessclient.RemoteError{
			RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "policy refused"},
		}
	}

	_, _, err := delegation.resumeChildRun(ctx, client)
	if !errors.Is(err, agent.ErrNativeCodingRejected) {
		t.Fatalf("persisted first-send refusal should be definite: %v", err)
	}
	if len(client.resumes) != 1 {
		t.Fatalf("definite refusal must not retry RunResume: calls=%d", len(client.resumes))
	}
	stored := d.rec.attempt.NativeDelegation.Resume
	if stored == nil || stored.Status != domainaction.NativeMutationStatusRejected || stored.ErrorCode != string(protocol.CodeForbidden) ||
		d.rec.action.Status != domainaction.StatusFailed || d.rec.attempt.Status != domainaction.AttemptStatusFailed {
		t.Fatalf("definite refusal was not persisted as terminal Action state: action=%+v attempt=%+v mutation=%+v", d.rec.action, d.rec.attempt, stored)
	}

	delegation.resumeInitiated = false
	_, done, replayErr := delegation.completedResult(ctx)
	if !errors.Is(replayErr, agent.ErrNativeCodingRejected) || done {
		t.Fatalf("crash replay should expose the saved definite refusal without a new Harness mutation: done=%v err=%v", done, replayErr)
	}
	if len(client.resumes) != 1 {
		t.Fatalf("saved rejection replay resent RunResume: calls=%d", len(client.resumes))
	}
}

func TestResumeRemoteRejectionPersistFailureRemainsUnknown(t *testing.T) {
	d, delegation, ctx, client := newPreparedResumeTest(t)
	d.rec.outcomeErr = errors.New("Action persistence unavailable")
	client.onResume = func(context.Context, int, protocol.ResumeInput) (protocol.ResumeResult, error) {
		return protocol.ResumeResult{}, &harnessclient.RemoteError{
			RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "policy refused"},
		}
	}

	_, _, err := delegation.resumeChildRun(ctx, client)
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("refusal whose persistence failed must remain unknown: %v", err)
	}
	if len(client.resumes) != 1 || d.rec.attempt.NativeDelegation.Resume.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("failed refusal persistence must preserve one uncertain delivery: calls=%d mutation=%+v", len(client.resumes), d.rec.attempt.NativeDelegation.Resume)
	}
}

func TestResumeRemoteRejectionAfterPriorUnknownRemainsUnknown(t *testing.T) {
	d, delegation, ctx, client := newPreparedResumeTest(t)
	client.onResume = func(_ context.Context, n int, _ protocol.ResumeInput) (protocol.ResumeResult, error) {
		if n == 1 {
			return protocol.ResumeResult{}, errors.Join(harnessclient.ErrOutcomeUnknown, context.DeadlineExceeded)
		}
		return protocol.ResumeResult{}, &harnessclient.RemoteError{
			RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "policy refused"},
		}
	}

	_, _, err := delegation.resumeChildRun(ctx, client)
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("remote refusal after a prior uncertain delivery must stay unknown: %v", err)
	}
	if len(client.resumes) < 2 || len(client.resumes) > maxSends || d.rec.attempt.NativeDelegation.Resume.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("prior unknown must not be promoted to rejection: calls=%d mutation=%+v", len(client.resumes), d.rec.attempt.NativeDelegation.Resume)
	}
}

func TestResumeReconciliationEffectFenceRefusesWithoutActionMutation(t *testing.T) {
	d, delegation, ctx, client := newPreparedResumeTest(t)
	input, err := decodeCanonical[protocol.ResumeInput](d.rec.attempt.NativeDelegation.Resume.Payload)
	if err != nil {
		t.Fatal(err)
	}
	resumed := resumeTestResult(client, input)
	client.setResumeResult(resumed)
	if _, _, err := d.rec.MarkNativeMutationDeliveryUnknown(ctx, delegation.actionID, delegation.attemptID, domainaction.NativeMutationSlotResume); err != nil {
		t.Fatal(err)
	}
	resumeBytes, err := protocol.Encode(resumed)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.rec.RecordNativeMutationOutcome(ctx, actionmanager.RecordNativeMutationOutcomeInput{
		ActionID: delegation.actionID, AttemptID: delegation.attemptID, Slot: domainaction.NativeMutationSlotResume,
		Status: domainaction.NativeMutationStatusAccepted, Result: resumeBytes,
	}); err != nil {
		t.Fatal(err)
	}
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	claim := domaintask.NativeOPSResumeClaim{
		RequestID: scope.RequestID, OwnerUserID: scope.AuthenticatedUserID,
		NewCoreRunID: delegation.parent.RunID, TraceID: delegation.parent.TraceID,
	}
	delegation.resumeClaim = &claim
	d.tasks.fenceError = errors.New("Task authority changed before Action persistence")
	beforeAction, beforeAttempt := d.rec.action, d.rec.attempt.Clone()
	runResult := client.runResult("failed", "not_run")
	_, err = delegation.complete(ctx, nil, startResultFromResume(resumed), runResult)
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("stale Task authority must leave terminal Action persistence unresolved: %v", err)
	}
	if !reflect.DeepEqual(d.rec.action, beforeAction) || !reflect.DeepEqual(d.rec.attempt, beforeAttempt) || len(d.rec.completed) != 0 {
		t.Fatalf("refused Task fence changed Action/proof/RunResult state: action before=%+v after=%+v attempt before=%+v after=%+v completions=%d",
			beforeAction, d.rec.action, beforeAttempt, d.rec.attempt, len(d.rec.completed))
	}
}

func newPreparedResumeTest(t *testing.T) (*testDeployment, *delegation, context.Context, *fakeClient) {
	t.Helper()
	d := newDeployment(t)
	ctx, _, _ := newTurn(t, "resume test")
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	clientValue, err := d.runtime.acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	client, ok := clientValue.(*fakeClient)
	if !ok {
		t.Fatalf("test runtime returned %T, want fakeClient", clientValue)
	}
	source := domaintask.NativeOPSResumeSource{
		ActionID: modulecore.NewActionID(), AttemptID: modulecore.NewAttemptID(),
		HarnessTaskID: client.task, ThreadID: client.thread, RunID: harnessID("run"), ReceiptID: harnessID("rcp"),
		RunResultSHA256: strings.Repeat("a", 64),
	}
	task := domaintask.Task{TaskID: identity.TaskID, ExpectedCriteriaRevision: d.settings.ExpectedCriteriaRevision}
	delegation := &delegation{
		r: d.runtime, parent: identity, task: task, resumeSource: &source,
		corr: correlation{traceID: string(identity.TraceID), taskID: string(identity.TaskID)},
	}
	action, attempt, err := d.rec.EnsureNativeDelegation(ctx, actionmanager.EnsureNativeDelegationInput{
		TaskID: identity.TaskID, RunID: identity.RunID, Mode: domainaction.NativeDelegationModeResume,
		ExpectedCriteriaRevision: task.ExpectedCriteriaRevision, ResumeSource: actionResumeSource(&source),
	})
	if err != nil {
		t.Fatalf("prepare Resume Action: %v", err)
	}
	delegation.actionID, delegation.attemptID = action.ActionID, attempt.AttemptID
	resumeInput := protocol.ResumeInput{
		TaskID: source.HarnessTaskID, ExpectedLastRunID: source.RunID, ExpectedControlRevision: 4,
		IdempotencyKey: delegation.key("resume"), Limits: d.settings.Limits,
	}
	payload, err := protocol.Encode(resumeInput)
	if err != nil {
		raw, _ := json.Marshal(resumeInput)
		t.Fatalf("encode frozen Resume input %s: %v", raw, err)
	}
	if _, _, err := d.rec.PrepareNativeMutation(ctx, actionmanager.PrepareNativeMutationInput{
		ActionID: action.ActionID, AttemptID: attempt.AttemptID, Slot: domainaction.NativeMutationSlotResume,
		Key: delegation.key("resume"), Payload: payload,
	}); err != nil {
		t.Fatalf("persist frozen Resume input: %v", err)
	}
	return d, delegation, ctx, client
}

func resumeTestResult(c *fakeClient, input protocol.ResumeInput) protocol.ResumeResult {
	return protocol.ResumeResult{
		ReceiptID: harnessID("rcp"), TaskID: input.TaskID, ThreadID: c.thread,
		PreviousRunID: input.ExpectedLastRunID, RunID: harnessID("run"), TraceID: harnessID("trc"),
		CheckpointID: input.CheckpointID, EffectiveLimits: input.Limits,
		DeadlineAt: protocol.FormatTimestamp(time.Now().UTC().Add(time.Hour)), RecoveryPolicyRevision: strings.Repeat("b", 64),
	}
}
