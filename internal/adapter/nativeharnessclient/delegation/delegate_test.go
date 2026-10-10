package delegation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_Harness/pkg/client"
	"github.com/Nyukimin/RenCrow_Harness/pkg/protocol"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func delegate(t *testing.T, d *testDeployment, text string) (agent.NativeCodingResult, error, domainexecution.Identity) {
	t.Helper()
	ctx, input, messages := newTurn(t, text)
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	return result, err, identity
}

func newDeploymentWithTaskManager(t *testing.T) (*testDeployment, *taskmanager.Manager) {
	t.Helper()
	d := newDeployment(t)
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	realTasks, err := taskmanager.NewWithExpectedCriteriaRevision(taskStore, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatalf("close fake-owner Runtime: %v", err)
	}
	runtime, err := NewRuntime(d.settings, d.rec, realTasks, Options{
		Start:   d.start,
		Now:     func() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.now },
		Log:     d.log.write,
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("NewRuntime with canonical TaskManager: %v", err)
	}
	d.runtime = runtime
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
	return d, realTasks
}

func TestDelegationRequiresTrustedShiroWorkerOPSScopeBeforeRPC(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "x")
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err = domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if !errors.Is(err, agent.ErrNativeCodingBlocked) && !errors.Is(err, agent.ErrNativeCodingRejected) {
		t.Fatalf("missing trusted Shiro worker/ops scope must be refused, got %v", err)
	}
	if d.startCalls() != 0 {
		t.Fatalf("a request without the trusted scope must not start the Harness, starts=%d", d.startCalls())
	}
	if len(d.rec.created) != 0 {
		t.Fatalf("a request without the trusted scope must not create an Action, created=%d", len(d.rec.created))
	}
	if len(d.log.events(t, eventDelegateAccepted)) != 0 {
		t.Fatal("a request without the trusted scope must not be accepted")
	}
}

func TestDelegationRejectsScopeAndIdentityMismatchesBeforeActionOrRPC(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*domaintool.ToolExecutionScope)
	}{
		{name: "wrong actor", mutate: func(scope *domaintool.ToolExecutionScope) { scope.ActorID = "mio" }},
		{name: "wrong role", mutate: func(scope *domaintool.ToolExecutionScope) { scope.AgentRole = "reviewer" }},
		{name: "wrong purpose", mutate: func(scope *domaintool.ToolExecutionScope) { scope.Purpose = "chat" }},
		{name: "wrong authentication source", mutate: func(scope *domaintool.ToolExecutionScope) {
			scope.AuthenticationSource = domaintool.AuthenticationSourceHTTP
		}},
		{name: "no internal grant", mutate: func(scope *domaintool.ToolExecutionScope) {
			scope.AllowedDataScopes = []string{domaintool.DataScopePublic}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			ctx, input, messages := newTurn(t, "x")
			ctx = mutateScope(t, ctx, tc.mutate)
			_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingRejected) {
				t.Fatalf("scope mismatch must be rejected, got %v", err)
			}
			if d.startCalls() != 0 || len(d.rec.created) != 0 {
				t.Fatalf("scope mismatch reached client or Action: starts=%d actions=%d", d.startCalls(), len(d.rec.created))
			}
		})
	}
	t.Run("trace does not match the turn", func(t *testing.T) {
		d := newDeployment(t)
		ctx, input, messages := newTurn(t, "x")
		identity, _ := domainexecution.IdentityFromContext(ctx)
		scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
		if !ok {
			t.Fatal("fixture has no execution scope")
		}
		ctx, err := domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, modulecore.NewTraceID())
		if err != nil {
			t.Fatal(err)
		}
		ctx = domaintool.WithToolExecutionScope(ctx, scope)
		_, err = d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		if !errors.Is(err, agent.ErrNativeCodingRejected) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("trace mismatch err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
}

func TestDelegationAllowsCanonicalChildTaskIdentityAndUsesItForUpstream(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "x")
	identity, _ := domainexecution.IdentityFromContext(ctx)
	scope, _ := domaintool.ToolExecutionScopeFromContext(ctx)
	childID := modulecore.NewTaskID()
	ctx, err := domainexecution.WithIdentity(context.Background(), childID, identity.RunID, identity.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	if input.RootTaskID() == childID {
		t.Fatal("fixture must exercise a child Task below the conversation root")
	}
	if _, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages}); err != nil {
		t.Fatalf("child Task Run must be allowed: %v", err)
	}
	if len(d.rec.created) != 1 || d.rec.created[0].TaskID != childID || d.client(0).starts[0].Upstream.TaskID != string(childID) {
		t.Fatalf("the canonical child Task owns the Action and upstream: action=%+v upstream=%+v", d.rec.created, d.client(0).starts[0].Upstream)
	}
}

func TestDelegationAllowsMioOwnedTaskManagerChildAssignedToShiro(t *testing.T) {
	d := newDeployment(t)
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(t.TempDir(), "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := taskStore.Close(); err != nil {
			t.Errorf("close Task store: %v", err)
		}
	})
	realTasks, err := taskmanager.NewWithExpectedCriteriaRevision(taskStore, taskmanager.DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	root, err := realTasks.Create(context.Background(), domaintask.Task{
		Title: "Viewer root OPS task", Route: domaintask.RouteOperations, OwnerID: "mio", Assignee: "mio",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create Mio-owned root Task: %v", err)
	}
	if _, err := realTasks.Start(context.Background(), root.TaskID); err != nil {
		t.Fatalf("start Mio-owned root Task: %v", err)
	}
	child, err := realTasks.Create(context.Background(), domaintask.Task{
		Title: "Shiro execution child", Route: domaintask.RouteOperations, ParentTaskID: root.TaskID,
		OwnerID: "mio", Assignee: shiroActorID,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create Shiro-assigned child Task: %v", err)
	}
	run, err := realTasks.StartRunWithReason(context.Background(), child.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start Shiro child Run: %v", err)
	}

	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatalf("close fake-owner Runtime: %v", err)
	}
	runtime, err := NewRuntime(d.settings, d.rec, realTasks, Options{
		Start:   d.start,
		Now:     func() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.now },
		Log:     d.log.write,
		Backoff: func(int) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatalf("NewRuntime with canonical TaskManager: %v", err)
	}
	d.runtime = runtime
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })

	ctx, input, messages := newTurn(t, "execute child")
	if input.RootTaskID() == child.TaskID {
		t.Fatal("fixture must keep conversation root and execution child identities distinct")
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		t.Fatal("fixture has no trusted Shiro execution scope")
	}
	ctx, err = domainexecution.WithIdentity(context.Background(), child.TaskID, run.RunID, input.TraceID())
	if err != nil {
		t.Fatal(err)
	}
	ctx = domaintool.WithToolExecutionScope(ctx, scope)
	result, err := runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("Mio-owned Task assigned to Shiro must pass canonical Task fencing: %v", err)
	}
	if !result.Accepted() {
		t.Fatalf("the completed and verified child Run must be accepted: %+v", result)
	}
	if d.rec.action.TaskID != child.TaskID || d.rec.action.RunID != run.RunID {
		t.Fatalf("delegation Action must belong to the canonical Shiro child Run: action=%+v", d.rec.action)
	}
	if got := d.client(0).starts; len(got) != 1 || got[0].Upstream.TaskID != string(child.TaskID) {
		t.Fatalf("Harness upstream must use the child Task identity: %+v", got)
	}
}

func TestInitialTaskFenceRejectsStaleOrMismatchedRunBeforeActionOrHarness(t *testing.T) {
	for _, scenario := range []string{"stale run", "Task and Run mismatch"} {
		t.Run(scenario, func(t *testing.T) {
			d, tasks := newDeploymentWithTaskManager(t)
			first, err := tasks.Create(context.Background(), domaintask.Task{
				Title: "Mio-owned Shiro OPS Task", Route: domaintask.RouteOperations, OwnerID: "mio", Assignee: shiroActorID,
			}, domaintask.SharedRoleContext{})
			if err != nil {
				t.Fatalf("create first Task: %v", err)
			}
			firstRun, err := tasks.StartRunWithReason(context.Background(), first.TaskID, domaintask.RunStartReasonFirst)
			if err != nil {
				t.Fatalf("start first Run: %v", err)
			}
			runID := firstRun.RunID
			if scenario == "stale run" {
				if _, err := tasks.Wait(context.Background(), first.TaskID, "replace run"); err != nil {
					t.Fatalf("wait first Run: %v", err)
				}
				if _, err := tasks.Resume(context.Background(), first.TaskID); err != nil {
					t.Fatalf("resume Task: %v", err)
				}
				currentRun, err := tasks.StartRunWithReason(context.Background(), first.TaskID, domaintask.RunStartReasonCheckpointResume)
				if err != nil {
					t.Fatalf("start current Run: %v", err)
				}
				if currentRun.RunID == firstRun.RunID {
					t.Fatal("TaskManager must issue a distinct current Run")
				}
			} else {
				if _, err := tasks.Wait(context.Background(), first.TaskID, "free OPS capacity"); err != nil {
					t.Fatalf("wait first Run: %v", err)
				}
				if _, err := tasks.Resume(context.Background(), first.TaskID); err != nil {
					t.Fatalf("resume first Task: %v", err)
				}
				second, err := tasks.Create(context.Background(), domaintask.Task{
					Title: "Second Shiro OPS Task", Route: domaintask.RouteOperations, OwnerID: "mio", Assignee: shiroActorID,
				}, domaintask.SharedRoleContext{})
				if err != nil {
					t.Fatalf("create second Task: %v", err)
				}
				secondRun, err := tasks.StartRunWithReason(context.Background(), second.TaskID, domaintask.RunStartReasonFirst)
				if err != nil {
					t.Fatalf("start second Run: %v", err)
				}
				if _, err := tasks.Wait(context.Background(), second.TaskID, "free second OPS slot"); err != nil {
					t.Fatalf("wait second Run: %v", err)
				}
				if _, err := tasks.StartRunWithReason(context.Background(), first.TaskID, domaintask.RunStartReasonCheckpointResume); err != nil {
					t.Fatalf("restart first Task: %v", err)
				}
				runID = secondRun.RunID
			}

			seed, input, messages := newTurn(t, "must not start")
			scope, ok := domaintool.ToolExecutionScopeFromContext(seed)
			if !ok {
				t.Fatal("fixture has no trusted Shiro scope")
			}
			ctx, err := domainexecution.WithIdentity(context.Background(), first.TaskID, runID, input.TraceID())
			if err != nil {
				t.Fatal(err)
			}
			ctx = domaintool.WithToolExecutionScope(ctx, scope)
			_, err = d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingBlocked) {
				t.Fatalf("stale or mismatched Run must be blocked at the initial Task fence: %v", err)
			}
			if len(d.rec.created) != 0 || d.startCalls() != 0 {
				t.Fatalf("invalid Run must be refused before Action or Harness creation: actions=%d clients=%d", len(d.rec.created), d.startCalls())
			}
		})
	}
}

func TestDelegationUsesOnlyTheCanonicalAcceptedOPSReceiptReference(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "x")
	identity, _ := domainexecution.IdentityFromContext(ctx)
	task := domaintask.Task{
		TaskID: identity.TaskID, OwnerID: shiroActorID, Assignee: shiroActorID, Route: domaintask.RouteOperations,
		Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("a", 64), OriginSessionID: modulecore.NewSessionID(), OriginThreadID: modulecore.NewThreadID(),
		OriginTurnID: input.TurnID(), OriginMessageID: input.UserMessageID(),
	}
	reference, accepted := acceptedOPSFixture(input, task, conversation.AcceptedOPSInputOriginAutomation)
	task.AcceptedOPSClaim = &domaintask.AcceptedOPSClaim{ReceiptRef: reference, BackendSelection: conversation.BackendShiroNativeCodingV1}
	d.tasks.task = &task
	ctx = mutateScope(t, ctx, func(scope *domaintool.ToolExecutionScope) {
		scope.RequestID = reference.RequestID
		scope.AuthenticatedUserID = reference.OwnerID
		scope.AllowedDataScopes = []string{domaintool.DataScopePublic, domaintool.DataScopeUser, domaintool.DataScopeInternal}
	})
	ctx = nativeharnessclient.WithAcceptedInputReader(ctx, acceptedOPSInputReaderFunc(func(context.Context) (conversation.AcceptedOPSInput, error) {
		return accepted, nil
	}))
	if _, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages}); err != nil {
		t.Fatalf("accepted OPS claim must retain its canonical reference: %v", err)
	}
	if stored := d.rec.attempt.NativeDelegation.InputReference; stored == nil || *stored != reference {
		t.Fatalf("Action reference=%+v, want canonical Task claim %+v", stored, reference)
	}
	if starts := d.client(0).starts; len(starts) != 1 || starts[0].Input.OriginProof != nil {
		t.Fatalf("Automation source must send one proof-free Start, got %+v", starts)
	}
}

func TestHumanAcceptedInputIsSignedOnceAndFrozenAcrossUnknownRetryAndTTLExpiry(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "  原文 café\n")
	identity, _ := domainexecution.IdentityFromContext(ctx)
	task := domaintask.Task{
		TaskID: identity.TaskID, OwnerID: shiroActorID, Assignee: shiroActorID, Route: domaintask.RouteOperations,
		Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("a", 64), OriginSessionID: modulecore.NewSessionID(), OriginThreadID: modulecore.NewThreadID(),
		OriginTurnID: input.TurnID(), OriginMessageID: input.UserMessageID(),
	}
	reference, accepted := acceptedOPSFixture(input, task, conversation.AcceptedOPSInputOriginHuman)
	task.AcceptedOPSClaim = &domaintask.AcceptedOPSClaim{ReceiptRef: reference, BackendSelection: conversation.BackendShiroNativeCodingV1}
	d.tasks.task = &task
	ctx = mutateScope(t, ctx, func(scope *domaintool.ToolExecutionScope) {
		scope.RequestID = reference.RequestID
		scope.AuthenticatedUserID = reference.OwnerID
		scope.AllowedDataScopes = []string{domaintool.DataScopePublic, domaintool.DataScopeUser, domaintool.DataScopeInternal}
	})
	reader := &countingAcceptedInputReader{input: accepted}
	ctx = nativeharnessclient.WithAcceptedInputReader(ctx, reader)
	clock := &delegationOriginClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
	countedSigner := newCountingTestOriginProofSigner(t, clock)
	d.runtime.originProofSigner = countedSigner
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, in protocol.StartInput) (protocol.StartResult, error) {
			if n == 1 {
				clock.now = clock.now.Add(10 * time.Minute)
				return protocol.StartResult{}, client.ErrOutcomeUnknown
			}
			return c.startResult(in), nil
		}
		return c, nil
	}
	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil || !result.Accepted() {
		t.Fatalf("Human relay should complete after resending its frozen Start: %+v %v", result, err)
	}
	starts := d.client(0).starts
	if len(starts) != 2 || !reflect.DeepEqual(starts[0], starts[1]) {
		t.Fatalf("Start retry did not reuse exact frozen payload: %+v", starts)
	}
	if starts[0].Input.OriginProof == nil {
		t.Fatal("Human source did not produce an origin proof")
	}
	proof := starts[0].Input.OriginProof
	if proof.Origin != string(protocol.OriginHuman) || proof.SourceMessageID != string(accepted.Receipt.UserMessageID) ||
		proof.SourceThreadID != string(accepted.Receipt.ThreadID) || proof.DestinationThreadID != starts[0].ThreadID ||
		proof.MutationKey != starts[0].IdempotencyKey || proof.RawHash != accepted.Receipt.RawSHA256 ||
		proof.Sequence != uint64(accepted.Receipt.AcceptanceSequence) || proof.ExpiresAt != "2026-10-09T12:05:00Z" {
		t.Fatalf("Human proof does not bind the owner receipt and frozen Start: %+v", proof)
	}
	if reader.calls != 1 || countedSigner.calls != 1 {
		t.Fatalf("retry reread or resigned source: reads=%d signatures=%d", reader.calls, countedSigner.calls)
	}
	replay, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil || !replay.Accepted() || reader.calls != 1 || countedSigner.calls != 1 || d.startCalls() != 1 || len(d.client(0).starts) != 2 {
		t.Fatalf("stored terminal replay must not read, sign or send again: result=%+v err=%v reads=%d signatures=%d clients=%d starts=%d", replay, err, reader.calls, countedSigner.calls, d.startCalls(), len(d.client(0).starts))
	}
}

func TestHumanAcceptedInputCannotDowngradeWhenReaderOrSignerIsUnavailable(t *testing.T) {
	for _, missing := range []string{"reader", "reader error", "signer"} {
		t.Run(missing, func(t *testing.T) {
			d := newDeployment(t)
			ctx, input, messages := newTurn(t, "human source")
			identity, _ := domainexecution.IdentityFromContext(ctx)
			task := domaintask.Task{
				TaskID: identity.TaskID, OwnerID: shiroActorID, Assignee: shiroActorID, Route: domaintask.RouteOperations,
				Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("a", 64), OriginSessionID: modulecore.NewSessionID(), OriginThreadID: modulecore.NewThreadID(),
				OriginTurnID: input.TurnID(), OriginMessageID: input.UserMessageID(),
			}
			reference, accepted := acceptedOPSFixture(input, task, conversation.AcceptedOPSInputOriginHuman)
			task.AcceptedOPSClaim = &domaintask.AcceptedOPSClaim{ReceiptRef: reference, BackendSelection: conversation.BackendShiroNativeCodingV1}
			d.tasks.task = &task
			ctx = mutateScope(t, ctx, func(scope *domaintool.ToolExecutionScope) {
				scope.RequestID, scope.AuthenticatedUserID = reference.RequestID, reference.OwnerID
				scope.AllowedDataScopes = []string{domaintool.DataScopePublic, domaintool.DataScopeUser, domaintool.DataScopeInternal}
			})
			if missing == "reader error" {
				ctx = nativeharnessclient.WithAcceptedInputReader(ctx, acceptedOPSInputReaderFunc(func(context.Context) (conversation.AcceptedOPSInput, error) {
					return conversation.AcceptedOPSInput{}, conversation.ErrAcceptedOPSInputUnavailable
				}))
			} else if missing != "reader" {
				ctx = nativeharnessclient.WithAcceptedInputReader(ctx, &countingAcceptedInputReader{input: accepted})
			}
			_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingBlocked) || d.startCalls() != 1 || len(d.client(0).starts) != 0 || len(d.rec.created) == 0 {
				t.Fatalf("Human source must block before Start without fallback: err=%v clients=%d starts=%d actions=%d", err, d.startCalls(), len(d.client(0).starts), len(d.rec.created))
			}
		})
	}
}

func TestHumanAcceptedInputTamperingCannotSignOrStart(t *testing.T) {
	mutations := map[string]func(*conversation.AcceptedOPSInput){
		"raw bytes":        func(input *conversation.AcceptedOPSInput) { input.RawMessage += " changed" },
		"request identity": func(input *conversation.AcceptedOPSInput) { input.Receipt.RequestID = "different-request" },
		"owner identity": func(input *conversation.AcceptedOPSInput) {
			input.Receipt.OwnerID, input.Receipt.ActorID = "other-user", "other-user"
		},
		"task identity": func(input *conversation.AcceptedOPSInput) { input.Receipt.TaskID = modulecore.NewTaskID() },
		"turn identity": func(input *conversation.AcceptedOPSInput) { input.Receipt.TurnID = modulecore.NewTurnID() },
		"payload hash":  func(input *conversation.AcceptedOPSInput) { input.Receipt.PayloadSHA256 = strings.Repeat("d", 64) },
		"raw hash":      func(input *conversation.AcceptedOPSInput) { input.Receipt.RawSHA256 = strings.Repeat("e", 64) },
		"origin": func(input *conversation.AcceptedOPSInput) {
			input.Receipt.DeclaredOrigin = conversation.AcceptedOPSInputOriginAutomation
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			d := newDeployment(t)
			ctx, input, messages := newTurn(t, "human source")
			identity, _ := domainexecution.IdentityFromContext(ctx)
			task := domaintask.Task{
				TaskID: identity.TaskID, OwnerID: shiroActorID, Assignee: shiroActorID, Route: domaintask.RouteOperations,
				Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("a", 64), OriginSessionID: modulecore.NewSessionID(), OriginThreadID: modulecore.NewThreadID(),
				OriginTurnID: input.TurnID(), OriginMessageID: input.UserMessageID(),
			}
			reference, accepted := acceptedOPSFixture(input, task, conversation.AcceptedOPSInputOriginHuman)
			task.AcceptedOPSClaim = &domaintask.AcceptedOPSClaim{ReceiptRef: reference, BackendSelection: conversation.BackendShiroNativeCodingV1}
			d.tasks.task = &task
			ctx = mutateScope(t, ctx, func(scope *domaintool.ToolExecutionScope) {
				scope.RequestID, scope.AuthenticatedUserID = reference.RequestID, reference.OwnerID
				scope.AllowedDataScopes = []string{domaintool.DataScopePublic, domaintool.DataScopeUser, domaintool.DataScopeInternal}
			})
			mutate(&accepted)
			reader := &countingAcceptedInputReader{input: accepted}
			ctx = nativeharnessclient.WithAcceptedInputReader(ctx, reader)
			clock := &delegationOriginClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
			countedSigner := newCountingTestOriginProofSigner(t, clock)
			d.runtime.originProofSigner = countedSigner
			_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingRejected) || reader.calls != 1 || countedSigner.calls != 0 || d.startCalls() != 1 || len(d.client(0).starts) != 0 {
				t.Fatalf("tampered Human source must reject before signing/Start: err=%v reads=%d signatures=%d clients=%d starts=%d", err, reader.calls, countedSigner.calls, d.startCalls(), len(d.client(0).starts))
			}
		})
	}
}

type acceptedOPSInputReaderFunc func(context.Context) (conversation.AcceptedOPSInput, error)

func (f acceptedOPSInputReaderFunc) ReadAcceptedInput(ctx context.Context) (conversation.AcceptedOPSInput, error) {
	return f(ctx)
}

type countingAcceptedInputReader struct {
	input conversation.AcceptedOPSInput
	calls int
}

func (r *countingAcceptedInputReader) ReadAcceptedInput(context.Context) (conversation.AcceptedOPSInput, error) {
	r.calls++
	return r.input, nil
}

func acceptedOPSFixture(input conversation.TurnInput, task domaintask.Task, origin conversation.AcceptedOPSInputOrigin) (conversation.AcceptedOPSInputReference, conversation.AcceptedOPSInput) {
	request := conversation.AcceptedOPSInputRequest{
		RequestID: "request-human-test", OwnerID: "ren", ActorID: "ren", SessionID: task.OriginSessionID,
		FirstThreadID: task.OriginThreadID, TaskID: task.TaskID, TurnID: input.TurnID(), TraceID: input.TraceID(),
		UserMessageID: input.UserMessageID(), AgentMessageID: input.AgentMessageID(), RawMessage: input.MessageText(), DeclaredOrigin: origin,
	}
	payloadHash, _ := conversation.AcceptedOPSInputPayloadSHA256(request)
	rawHash := sha256.Sum256([]byte(request.RawMessage))
	accepted := conversation.AcceptedOPSInput{
		Receipt: conversation.AcceptedOPSInputReceipt{
			AcceptanceSequence: 1, RequestID: request.RequestID, OwnerID: request.OwnerID, ActorID: request.ActorID,
			SessionID: request.SessionID, ThreadID: request.FirstThreadID, ThreadSeq: modulecore.ThreadSeq(1),
			ThreadKind: modulecore.ThreadKindUserConversation, TaskID: request.TaskID, TurnID: request.TurnID,
			TraceID: request.TraceID, UserMessageID: request.UserMessageID, AgentMessageID: request.AgentMessageID,
			DeclaredOrigin: origin, PayloadSHA256: payloadHash, RawRecordID: "raw-record-test", ManifestID: "manifest-test",
			RawSHA256: hex.EncodeToString(rawHash[:]), ManifestSHA256: strings.Repeat("c", 64), AcceptedAt: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC),
		},
		RawMessage: request.RawMessage,
	}
	return conversation.AcceptedOPSInputReference{OwnerID: request.OwnerID, RequestID: request.RequestID, PayloadSHA256: payloadHash}, accepted
}

type delegationOriginClock struct{ now time.Time }

func (c *delegationOriginClock) Now() time.Time { return c.now }

type delegationOriginNonce struct{ value string }

func (n *delegationOriginNonce) NewNonce() (string, error) { return n.value, nil }

func newCountingTestOriginProofSigner(t *testing.T, clock *delegationOriginClock) *countingOriginProofSigner {
	t.Helper()
	key, err := nativeharnessclient.ParseKey([]byte(strings.Repeat("ab", 32)))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := nativeharnessclient.NewOriginSigner(nativeharnessclient.IssuerSettings{
		Issuer: "core:test", KeyID: "fixture-key", Audience: "core:local", TTLSeconds: 300,
	}, key, clock, &delegationOriginNonce{value: "fixture-nonce-0001"})
	if err != nil {
		t.Fatal(err)
	}
	return &countingOriginProofSigner{signer: signer}
}

type countingOriginProofSigner struct {
	signer *nativeharnessclient.OriginSigner
	calls  int
}

func (s *countingOriginProofSigner) Sign(request nativeharnessclient.OriginProofRequest) (nativeharnessclient.OriginProof, error) {
	s.calls++
	return s.signer.Sign(request)
}

func TestDelegationRejectsAcceptedOPSClaimWhenUserScopeDoesNotMatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*domaintool.ToolExecutionScope)
	}{
		{name: "wrong user", mutate: func(scope *domaintool.ToolExecutionScope) { scope.AuthenticatedUserID = "other" }},
		{name: "wrong request", mutate: func(scope *domaintool.ToolExecutionScope) { scope.RequestID = "other-request" }},
		{name: "no user grant", mutate: func(scope *domaintool.ToolExecutionScope) {
			scope.AllowedDataScopes = []string{domaintool.DataScopePublic, domaintool.DataScopeInternal}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			ctx, input, messages := newTurn(t, "x")
			identity, _ := domainexecution.IdentityFromContext(ctx)
			reference := conversation.AcceptedOPSInputReference{OwnerID: "ren", RequestID: "request", PayloadSHA256: strings.Repeat("b", 64)}
			task := domaintask.Task{
				TaskID: identity.TaskID, OwnerID: shiroActorID, Assignee: shiroActorID, Route: domaintask.RouteOperations,
				Status: domaintask.StatusRunning, ExpectedCriteriaRevision: strings.Repeat("a", 64), OriginSessionID: modulecore.NewSessionID(), OriginThreadID: modulecore.NewThreadID(),
				OriginTurnID: input.TurnID(), OriginMessageID: modulecore.NewMessageID(),
				AcceptedOPSClaim: &domaintask.AcceptedOPSClaim{ReceiptRef: reference, BackendSelection: conversation.BackendShiroNativeCodingV1},
			}
			d.tasks.task = &task
			ctx = mutateScope(t, ctx, func(scope *domaintool.ToolExecutionScope) {
				scope.RequestID, scope.AuthenticatedUserID = reference.RequestID, reference.OwnerID
				scope.AllowedDataScopes = []string{domaintool.DataScopePublic, domaintool.DataScopeUser, domaintool.DataScopeInternal}
				tc.mutate(scope)
			})
			_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingRejected) || d.startCalls() != 0 || len(d.rec.created) != 0 {
				t.Fatalf("accepted claim mismatch err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
			}
		})
	}
}

func mutateScope(t *testing.T, ctx context.Context, mutate func(*domaintool.ToolExecutionScope)) context.Context {
	t.Helper()
	identity, err := domainexecution.IdentityFromContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok {
		t.Fatal("fixture has no execution scope")
	}
	mutate(&scope)
	ctx, err = domainexecution.WithIdentity(context.Background(), identity.TaskID, identity.RunID, identity.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(ctx, scope)
}

func TestDelegationHappyPathRecordsOneDelegationActionAndCorrelatesBothSides(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, secretText)
	identity, _ := domainexecution.IdentityFromContext(ctx)

	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil {
		t.Fatalf("DelegateNativeCoding: %v", err)
	}
	c := d.client(0)

	// CORE side: one delegation Action in the parent Task Run, closed succeeded.
	if len(d.rec.created) != 1 {
		t.Fatalf("exactly one delegation Action must be recorded, got %d", len(d.rec.created))
	}
	created := d.rec.created[0]
	if created.Kind != domainaction.KindDelegation || created.TaskID != identity.TaskID || created.RunID != identity.RunID {
		t.Fatalf("the delegation Action must belong to the parent Task Run: %+v", created)
	}
	if len(d.rec.completed) != 1 || d.rec.completed[0].attempt != domainaction.AttemptStatusSucceeded || d.rec.completed[0].action != domainaction.StatusSucceeded {
		t.Fatalf("the Action must be closed once, succeeded: %+v", d.rec.completed)
	}
	if result.ActionID != d.rec.actionID || result.AttemptID != d.rec.attemptID {
		t.Fatalf("the result must name CORE's Action and Attempt: %+v", result)
	}

	// Harness side: the keys and the upstream reference carry CORE's IDs.
	attempt := string(d.rec.attemptID)
	if len(c.opens) != 1 || c.opens[0].IdempotencyKey != "core."+attempt+".open" {
		t.Fatalf("session/open key: %+v", c.opens)
	}
	open := c.opens[0]
	if open.WorkspacePath != d.settings.Workspace.Path || open.PolicyRef != "workspace-write" || open.ExecutionMode != protocol.ModeStructuredOnly {
		t.Fatalf("the session must use the configured workspace: %+v", open)
	}
	if open.Binding.Kind != "alias" || open.Binding.Selector != "shiro-worker-exec" || open.Binding.ProfileRevision != "rev-1" ||
		open.Binding.AgentID == nil || *open.Binding.AgentID != "shiro" || open.Binding.ExecutionRole == nil || *open.Binding.ExecutionRole != "worker" {
		t.Fatalf("the binding must be the existing Shiro execution alias with agent_id shiro: %+v", open.Binding)
	}
	if len(c.starts) != 1 {
		t.Fatalf("turn/start must be sent once, got %d", len(c.starts))
	}
	start := c.starts[0]
	if start.IdempotencyKey != "core."+attempt+".start" {
		t.Fatalf("turn/start key = %q", start.IdempotencyKey)
	}
	if start.ThreadID != c.thread || start.ExpectedContextRevision != 3 || start.ExpectedControlRevision != 4 {
		t.Fatalf("the start must target the opened thread with its revisions: %+v", start)
	}
	if start.Input.Text != secretText || start.Input.OriginProof != nil {
		t.Fatalf("the user text is sent as given, without any OriginProof (Automation): %+v", start.Input)
	}
	if start.Limits != d.settings.Limits {
		t.Fatalf("the complete configured limits must be sent: %+v", start.Limits)
	}
	up := start.Upstream
	if up == nil || up.Owner != "RenCrow_CORE" || up.TaskID != string(identity.TaskID) || up.TraceID != string(input.TraceID()) {
		t.Fatalf("upstream must reference CORE's parent Task and Trace: %+v", up)
	}
	if up.TurnID == nil || *up.TurnID != string(input.TurnID()) || up.ActionID == nil || *up.ActionID != string(d.rec.actionID) ||
		up.AttemptID == nil || *up.AttemptID != attempt {
		t.Fatalf("upstream must reference CORE's turn, Action and Attempt: %+v", up)
	}
	if up.SessionID != nil || up.ThreadID != nil {
		t.Fatalf("CORE has no canonical session or thread for the turn yet (null): %+v", up)
	}

	// The ContextBlocks are the typed F32 projection of the messages.
	want, userText, err := nativeharnessclient.MaterializeContextRevision(messages, nil)
	if err != nil || userText != secretText {
		t.Fatalf("Materialize: %v %q", err, userText)
	}
	if len(start.ContextBlocks) != len(want) || len(want) != 4 {
		t.Fatalf("four typed blocks expected, got %d", len(start.ContextBlocks))
	}
	for i, block := range start.ContextBlocks {
		if block.Kind != string(want[i].Kind) || block.Text != want[i].Text || block.Revision != want[i].Revision || block.Source != nil {
			t.Fatalf("block %d differs from the F32 projection (and Recall has no source yet): %+v vs %+v", i, block, want[i])
		}
	}
	if len(c.awaits) != 1 || c.awaits[0].runID != c.run || c.awaits[0].opts.InterruptKeyPrefix != "" {
		t.Fatalf("read-only Run polling must not invoke the SDK's hidden interrupt path: %+v", c.awaits)
	}

	// The projection keeps the Harness IDs with their owner.
	if result.HarnessTask != (agent.ExternalRef{Owner: agent.HarnessOwner, ID: c.task}) ||
		result.HarnessRun != (agent.ExternalRef{Owner: agent.HarnessOwner, ID: c.run}) ||
		result.HarnessReceipt != (agent.ExternalRef{Owner: agent.HarnessOwner, ID: c.receipt}) {
		t.Fatalf("Harness IDs must carry their owner: %+v", result)
	}
	if !result.Accepted() || result.FinalText != secretFinal {
		t.Fatalf("completed + passed: %+v", result)
	}

	// The CORE record names the Harness IDs by owner and holds no content.
	summary := d.rec.completed[0].summary
	mustContain(t, "summary", summary, "status=completed", "verification=passed",
		"harness_task=RenCrow_Harness/"+c.task, "harness_run=RenCrow_Harness/"+c.run, "harness_receipt=RenCrow_Harness/"+c.receipt)
	mustNotContain(t, "summary", summary, secretText, secretFinal)
}

func TestDelegationKeepsTheRunResultStatusesAndMapsTheActionClosure(t *testing.T) {
	cases := []struct {
		status, code, verification string
		resumable                  bool
		wantAttempt                domainaction.AttemptStatus
		wantAction                 domainaction.Status
	}{
		{"completed", "FINAL_RESPONSE_ACCEPTED", "passed", false, domainaction.AttemptStatusSucceeded, domainaction.StatusSucceeded},
		{"completed", "FINAL_RESPONSE_ACCEPTED", "not_run", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"completed", "FINAL_RESPONSE_ACCEPTED", "failed", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"incomplete", "DEADLINE_EXCEEDED", "not_run", true, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"rejected", "POLICY_REJECTED", "not_run", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"blocked", "MODEL_GENERATION_OUTCOME_UNKNOWN", "unknown", true, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"cancelled", "CANCELLED", "not_run", true, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled},
		{"failed", "TOOL_FAILED", "failed", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
		{"restart_required", "PERSISTENCE_UNCERTAIN", "unknown", false, domainaction.AttemptStatusFailed, domainaction.StatusFailed},
	}
	for _, tc := range cases {
		t.Run(tc.status+"/"+tc.verification, func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
					result := c.runResult(tc.status, tc.verification)
					result.Code, result.Resumable = tc.code, tc.resumable
					return result, nil
				}
				return c, nil
			}
			result, err, _ := delegate(t, d, "x")
			if err != nil {
				t.Fatalf("a Run the Harness determined is a result, not an error: %v", err)
			}
			if string(result.Status) != tc.status || result.Code != tc.code || string(result.Verification) != tc.verification || result.Resumable != tc.resumable {
				t.Fatalf("the RunResult must be kept as it is: %+v", result)
			}
			if got := result.Accepted(); got != (tc.status == "completed" && tc.verification == "passed") {
				t.Fatalf("Accepted() = %v for %s/%s", got, tc.status, tc.verification)
			}
			done := d.rec.completed[0]
			if done.attempt != tc.wantAttempt || done.action != tc.wantAction {
				t.Fatalf("closure = %s/%s, want %s/%s", done.attempt, done.action, tc.wantAttempt, tc.wantAction)
			}
			mustContain(t, "summary", done.summary, "status="+tc.status, "verification="+tc.verification, "resumable=")
		})
	}
}

func TestHasAcceptedStoredRunResultRequiresCanonicalMatchingCompletedPassedPair(t *testing.T) {
	f := newFakeClient()
	start := f.startResult(protocol.StartInput{ThreadID: f.thread, Limits: protocol.Limits{
		MaxModelSteps: 1, MaxToolCallsPerStep: 1, DeadlineSeconds: 1, MaxCaptureBytes: 2048, MaxGenerationAttempts: 1,
	}})
	startBytes, err := protocol.Encode(start)
	if err != nil {
		t.Fatal(err)
	}
	revision := strings.Repeat("a", 64)
	makeAcceptedMutation := func(key string, result []byte) *domainaction.NativeMutation {
		payload := []byte(`{"idempotency_key":"` + key + `"}`)
		hash := sha256.Sum256(payload)
		return &domainaction.NativeMutation{
			Key: key, Payload: payload, PayloadSHA256: hex.EncodeToString(hash[:]),
			Status: domainaction.NativeMutationStatusAccepted, Result: append([]byte(nil), result...),
		}
	}
	for _, tc := range []struct {
		name         string
		verification string
		mutate       func(*protocol.RunResult)
		want         bool
	}{
		{name: "completed passed", verification: "passed", want: true},
		{name: "completed not run", verification: "not_run"},
		{name: "completed failed", verification: "failed"},
		{name: "run identity mismatch", verification: "passed", mutate: func(result *protocol.RunResult) { result.RunID = harnessID("run") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runResult := f.runResult("completed", tc.verification)
			if tc.mutate != nil {
				tc.mutate(&runResult)
			}
			runBytes, err := protocol.Encode(runResult)
			if err != nil {
				t.Fatal(err)
			}
			var proof *domainaction.NativeDelegationProof
			if tc.want {
				hash := sha256.Sum256(runBytes)
				proof = &domainaction.NativeDelegationProof{
					Version: domainaction.NativeDelegationProofVersion, ExpectedCriteriaRevision: revision,
					RunResultSHA256: hex.EncodeToString(hash[:]), TerminalEventID: harnessID("evt"), TerminalEventSeq: 7,
					Evidence: []domainaction.NativeDelegationEvidenceProof{{EvidenceID: runResult.Verification.EvidenceIDs[0],
						VerifierActionID: harnessID("act"), VerifierAttemptID: harnessID("att"), CompletionEventID: harnessID("evt"), RawHash: strings.Repeat("b", 64), TotalBytes: 1}},
				}
			}
			attempt := domainaction.Attempt{
				Status: domainaction.AttemptStatusSucceeded,
				NativeDelegation: &domainaction.NativeDelegation{
					ExpectedCriteriaRevision: revision,
					Open:                     makeAcceptedMutation("legacy-open", []byte(`{"thread_id":"`+f.thread+`"}`)),
					Start:                    makeAcceptedMutation("legacy-start", startBytes),
					RunResult:                runBytes,
					Proof:                    proof,
				},
			}
			if tc.want {
				if err := attempt.NativeDelegation.Validate(); err != nil {
					t.Fatalf("historical open/start delegation with valid proof is invalid: %v", err)
				}
			}
			if got := HasAcceptedStoredRunResult(attempt, revision); got != tc.want {
				t.Fatalf("HasAcceptedStoredRunResult=%v want=%v", got, tc.want)
			}
		})
	}
	runResult := f.runResult("completed", "passed")
	runBytes, err := protocol.Encode(runResult)
	if err != nil {
		t.Fatal(err)
	}
	runHash := sha256.Sum256(runBytes)
	legacyProof := &domainaction.NativeDelegationProof{
		Version: domainaction.NativeDelegationProofVersion, ExpectedCriteriaRevision: revision,
		RunResultSHA256: hex.EncodeToString(runHash[:]), TerminalEventID: harnessID("evt"), TerminalEventSeq: 7,
		Evidence: []domainaction.NativeDelegationEvidenceProof{{EvidenceID: runResult.Verification.EvidenceIDs[0],
			VerifierActionID: harnessID("act"), VerifierAttemptID: harnessID("att"), CompletionEventID: harnessID("evt"), RawHash: strings.Repeat("b", 64), TotalBytes: 1}},
	}
	legacyAttempt := func(proof *domainaction.NativeDelegationProof) domainaction.Attempt {
		return domainaction.Attempt{Status: domainaction.AttemptStatusSucceeded, NativeDelegation: &domainaction.NativeDelegation{
			ExpectedCriteriaRevision: revision,
			Open:                     makeAcceptedMutation("legacy-open", []byte(`{"thread_id":"`+f.thread+`"}`)),
			Start:                    makeAcceptedMutation("legacy-start", startBytes),
			RunResult:                append([]byte(nil), runBytes...),
			Proof:                    proof,
		}}
	}
	t.Run("legacy mode with Resume state", func(t *testing.T) {
		attempt := legacyAttempt(legacyProof)
		attempt.NativeDelegation.Resume = makeAcceptedMutation("legacy-resume", []byte(`{"run_id":"`+harnessID("run")+`"}`))
		if HasAcceptedStoredRunResult(attempt, revision) {
			t.Fatal("a historical no-mode delegation with Resume state cannot authorize success")
		}
	})
	t.Run("missing proof", func(t *testing.T) {
		if HasAcceptedStoredRunResult(legacyAttempt(nil), revision) {
			t.Fatal("a historical open/start result without a proof cannot authorize success")
		}
	})
	t.Run("noncanonical bytes", func(t *testing.T) {
		attempt := domainaction.Attempt{
			Status: domainaction.AttemptStatusSucceeded,
			NativeDelegation: &domainaction.NativeDelegation{
				ExpectedCriteriaRevision: revision,
				Open:                     makeAcceptedMutation("legacy-open", []byte(`{"thread_id":"`+f.thread+`"}`)),
				Start:                    makeAcceptedMutation("legacy-start", startBytes),
				RunResult:                []byte(`{"status":"completed","verification":{"status":"passed"}}`),
			},
		}
		if HasAcceptedStoredRunResult(attempt, strings.Repeat("a", 64)) {
			t.Fatal("an invalid stored RunResult cannot authorize Task success")
		}
	})
}

func TestDelegationResendsTheSameStartPayloadWithTheSameKeyAfterAnUnknownOutcome(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, in protocol.StartInput) (protocol.StartResult, error) {
			if n < 3 {
				if n == 1 {
					d.runtime.settings.Limits.MaxModelSteps++ // the frozen retry must ignore later settings changes
				}
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
			}
			return c.startResult(in), nil
		}
		return c, nil
	}
	result, err, _ := delegate(t, d, "x")
	if err != nil || !result.Accepted() {
		t.Fatalf("the third send answers: %+v %v", result, err)
	}
	c := d.client(0)
	if len(c.starts) != 3 {
		t.Fatalf("sends = %d, want 3", len(c.starts))
	}
	for i := 1; i < len(c.starts); i++ {
		if !reflect.DeepEqual(c.starts[0], c.starts[i]) {
			t.Fatalf("a resend must repeat the first payload unchanged:\n first %+v\n resend %+v", c.starts[0], c.starts[i])
		}
	}
	if len(c.opens) != 1 {
		t.Fatalf("the session is opened once, got %d", len(c.opens))
	}
	if len(d.rec.created) != 1 {
		t.Fatal("a resend must not create a second delegation Action")
	}
}

func TestDelegationRetriesFrozenOpenBytesAfterConfigurationChanges(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onOpen = func(n int, in protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
			if n == 1 {
				d.runtime.settings.Workspace.Path = filepath.Join(d.settings.Workspace.Path, "changed")
				d.runtime.settings.Binding.Selector = "changed-selector"
				return protocol.SessionOpenResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
			}
			return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: c.session()}, nil
		}
		return c, nil
	}
	if _, err, _ := delegate(t, d, "x"); err != nil {
		t.Fatalf("the retry answers from the original receipt: %v", err)
	}
	c := d.client(0)
	if len(c.opens) != 2 || !reflect.DeepEqual(c.opens[0], c.opens[1]) {
		t.Fatalf("the retry must preserve the exact frozen Open payload: %+v", c.opens)
	}
	if c.opens[1].WorkspacePath != d.settings.Workspace.Path || c.opens[1].Binding.Selector != d.settings.Binding.Selector {
		t.Fatalf("the resend must use original configuration: %+v", c.opens[1])
	}
}

func TestDelegationNeverMakesANewKeyWhenTheOutcomeStaysUnknown(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
			return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("an outcome that stays unknown must be reported as such: %v", err)
	}
	c := d.client(0)
	keys := map[string]bool{}
	for _, start := range c.starts {
		keys[start.IdempotencyKey] = true
	}
	if len(c.starts) != maxSends || len(keys) != 1 {
		t.Fatalf("the same request is sent %d times with one key, got %d sends and keys %v", maxSends, len(c.starts), keys)
	}
	if len(c.awaits) != 0 {
		t.Fatal("no Run is awaited when no acceptance is known")
	}
	if len(d.rec.completed) != 0 || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("an unknown delivery keeps the Action open and Attempt running: action=%s attempt=%s completions=%+v", d.rec.action.Status, d.rec.attempt.Status, d.rec.completed)
	}
	if d.rec.attempt.NativeDelegation.Start == nil || d.rec.attempt.NativeDelegation.Start.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("the uncertain Start must remain delivery_unknown: %+v", d.rec.attempt.NativeDelegation.Start)
	}
	if got := len(d.log.events(t, eventDelegateUnknown)); got != 1 {
		t.Fatalf("the unknown outcome is logged once, got %d", got)
	}
}

func TestDelegationSendsTheSameInputToAReplacementClientWhenTheConnectionEndedMidStart(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(n int, _ client.Config) (*fakeClient, error) {
		c := newFakeClient()
		if n == 1 {
			c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
				c.end() // the child died after the request was written
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, client.ErrClosed)
			}
		}
		return c, nil
	}
	result, err, _ := delegate(t, d, "x")
	if err != nil || !result.Accepted() {
		t.Fatalf("the replacement answers: %+v %v", result, err)
	}
	if d.startCalls() != 2 {
		t.Fatalf("a replacement Harness must be started, starts = %d", d.startCalls())
	}
	first, second := d.client(0).starts, d.client(1).starts
	if len(first) != 1 || len(second) != 1 || !reflect.DeepEqual(first[0], second[0]) {
		t.Fatalf("the replacement must receive the first payload unchanged: %+v vs %+v", first, second)
	}
	if len(d.client(1).opens) != 0 {
		t.Fatal("the session is not opened again for the same delegation")
	}
}

func TestDelegationRefusalsAreTypedAndNothingIsAwaited(t *testing.T) {
	t.Run("session refused", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) {
			c := newFakeClient()
			c.onOpen = func(int, protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
				return protocol.SessionOpenResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "no"}}
			}
			return c, nil
		}
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingRejected) {
			t.Fatalf("a definite remote session refusal must be rejected: %v", err)
		}
		mustContain(t, "refusal", err.Error(), "harness_rejected_FORBIDDEN")
		if len(d.client(0).starts) != 0 || len(d.client(0).awaits) != 0 {
			t.Fatal("nothing may be started after a refused session")
		}
		if d.rec.action.Status != domainaction.StatusFailed || d.rec.attempt.Status != domainaction.AttemptStatusFailed ||
			d.rec.attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusRejected {
			t.Fatalf("the definite remote refusal must persist rejected mutation and failed pair: action=%s attempt=%s mutation=%+v", d.rec.action.Status, d.rec.attempt.Status, d.rec.attempt.NativeDelegation.Open)
		}
	})
	t.Run("start refused as invalid", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) {
			c := newFakeClient()
			c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
				return protocol.StartResult{}, protocol.NewError(protocol.CodeInvalidParams, "bad")
			}
			return c, nil
		}
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
			t.Fatalf("a non-owner protocol error after delivery begins remains unknown: %v", err)
		}
		if d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
			t.Fatalf("a non-definite failure must leave the pair active: action=%s attempt=%s", d.rec.action.Status, d.rec.attempt.Status)
		}
	})
	t.Run("start refused by the Harness", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) {
			c := newFakeClient()
			c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
				return protocol.StartResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeBusy, Message: "busy"}}
			}
			return c, nil
		}
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingRejected) {
			t.Fatalf("a definite Harness refusal rejects the turn: %v", err)
		}
		if len(d.client(0).starts) != 1 {
			t.Fatal("a refusal is an answer: the request is not sent again")
		}
	})
}

func TestRemoteRejectionAfterUnknownDeliveryRemainsUnknown(t *testing.T) {
	for _, slot := range []domainaction.NativeMutationSlot{domainaction.NativeMutationSlotOpen, domainaction.NativeMutationSlotStart} {
		t.Run(string(slot), func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				if slot == domainaction.NativeMutationSlotOpen {
					c.onOpen = func(n int, _ protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
						if n == 1 {
							return protocol.SessionOpenResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
						}
						return protocol.SessionOpenResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "profile changed"}}
					}
				} else {
					c.onStart = func(n int, _ protocol.StartInput) (protocol.StartResult, error) {
						if n == 1 {
							return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
						}
						return protocol.StartResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{Code: protocol.CodeForbidden, Message: "profile changed"}}
					}
				}
				return c, nil
			}
			_, err, _ := delegate(t, d, "x")
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("a remote rejection cannot resolve a previously unknown delivery: %v", err)
			}
			if d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning || len(d.rec.attempt.NativeDelegation.RunResult) != 0 {
				t.Fatalf("uncertain historical delivery must keep the pair active: action=%s attempt=%s result=%q", d.rec.action.Status, d.rec.attempt.Status, d.rec.attempt.NativeDelegation.RunResult)
			}
			mutation := d.rec.attempt.NativeDelegation.Open
			if slot == domainaction.NativeMutationSlotStart {
				if d.rec.attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusAccepted {
					t.Fatalf("Open should have been accepted before Start retry: %+v", d.rec.attempt.NativeDelegation.Open)
				}
				mutation = d.rec.attempt.NativeDelegation.Start
			}
			if mutation == nil || mutation.Status != domainaction.NativeMutationStatusDeliveryUnknown {
				t.Fatalf("prior unknown mutation must remain delivery_unknown: %+v", mutation)
			}
			wantOpenCalls, wantStartCalls := 2, 0
			if slot == domainaction.NativeMutationSlotStart {
				wantOpenCalls, wantStartCalls = 1, 2
			}
			if len(d.client(0).opens) != wantOpenCalls || len(d.client(0).starts) != wantStartCalls {
				t.Fatalf("one frozen retry should follow the unknown delivery: Open=%d Start=%d", len(d.client(0).opens), len(d.client(0).starts))
			}
		})
	}
}

func TestDelegationPrerequisitesFailBeforeAnythingIsStarted(t *testing.T) {
	t.Run("no execution identity", func(t *testing.T) {
		d := newDeployment(t)
		_, input, messages := newTurn(t, "x")
		_, err := d.runtime.DelegateNativeCoding(context.Background(), agent.NativeCodingRequest{Input: input, Messages: messages})
		if !errors.Is(err, agent.ErrNativeCodingRejected) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
	t.Run("unusable messages", func(t *testing.T) {
		d := newDeployment(t)
		ctx, input, _ := newTurn(t, "x")
		_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: []llm.Message{{Role: "system", Content: "flat", Type: llm.PromptContextCharacter}}})
		if !errors.Is(err, agent.ErrNativeCodingRejected) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
	t.Run("already cancelled", func(t *testing.T) {
		d := newDeployment(t)
		ctx, input, messages := newTurn(t, "x")
		ctx, cancel := context.WithCancel(ctx)
		cancel()
		_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		if !errors.Is(err, context.Canceled) || d.startCalls() != 0 || len(d.rec.created) != 0 {
			t.Fatalf("err=%v starts=%d actions=%d", err, d.startCalls(), len(d.rec.created))
		}
	})
	t.Run("Action cannot be recorded", func(t *testing.T) {
		d := newDeployment(t)
		d.rec.createErr = errors.New("store down")
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingBlocked) {
			t.Fatalf("a delegation that CORE cannot record must not run: %v", err)
		}
		if d.startCalls() != 0 {
			t.Fatal("no Harness client may start for a delegation that has no Action")
		}
	})
	t.Run("Harness unavailable", func(t *testing.T) {
		d := newDeployment(t)
		d.startFn = func(int, client.Config) (*fakeClient, error) { return nil, errors.New("down") }
		_, err, _ := delegate(t, d, "x")
		if !errors.Is(err, agent.ErrNativeCodingBlocked) || len(d.rec.created) != 1 || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
			t.Fatalf("err=%v actions=%d action=%s attempt=%s", err, len(d.rec.created), d.rec.action.Status, d.rec.attempt.Status)
		}
	})
}

func TestDelegationCancellationBecomesAStopSignalAndTheRunsEndIsKept(t *testing.T) {
	d := newDeployment(t)
	entered := make(chan struct{})
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		awaits := 0
		c.onAwait = func(ctx context.Context, _ string, opts client.AwaitOptions) (protocol.RunResult, error) {
			if opts.InterruptKeyPrefix != "" {
				return protocol.RunResult{}, errors.New("AwaitRun must not use its hidden interrupt path")
			}
			awaits++
			if awaits == 1 {
				close(entered)
				<-ctx.Done()
				return protocol.RunResult{}, ctx.Err()
			}
			result := c.runResult("cancelled", "not_run")
			result.Code, result.Resumable = "CANCELLED", true
			return result, nil
		}
		c.onInterrupt = func(ctx context.Context, runID, keyPrefix string) (protocol.InterruptReceipt, error) {
			if err := ctx.Err(); err != nil {
				return protocol.InterruptReceipt{}, err
			}
			if _, ok := ctx.Deadline(); !ok {
				return protocol.InterruptReceipt{}, errors.New("interrupt call has no bounded deadline")
			}
			return protocol.InterruptReceipt{ReceiptID: harnessID("ircp"), RunID: runID, SignalRecorded: true}, nil
		}
		return c, nil
	}
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type outcome struct {
		result agent.NativeCodingResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
		done <- outcome{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the delegation never reached the wait")
	}
	cancel()
	select {
	case got := <-done:
		if got.err != nil || got.result.Status != agent.NativeRunCancelled || !got.result.Resumable {
			t.Fatalf("a cancelled Run is a typed result: %+v %v", got.result, got.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the delegation did not return after the cancellation")
	}
	if len(d.rec.completed) != 1 || d.rec.completed[0].attempt != domainaction.AttemptStatusCancelled || d.rec.completed[0].action != domainaction.StatusCancelled {
		t.Fatalf("only the actual terminal RunResult may close the delegation: %+v", d.rec.completed)
	}
	c := d.client(0)
	if len(c.awaits) != 2 || c.awaits[0].opts.InterruptKeyPrefix != "" || c.awaits[1].opts.InterruptKeyPrefix != "" {
		t.Fatalf("both Run polls are read-only: %+v", c.awaits)
	}
	if len(c.interrupts) != 1 || c.interrupts[0].runID != c.run || c.interrupts[0].keyPrefix != "core."+string(d.rec.attemptID)+".stop" {
		t.Fatalf("cancellation sends an explicit fenced interrupt: %+v", c.interrupts)
	}
	if d.tasks.lastActor != shiroActorID || d.tasks.fenceCalls != 5 {
		t.Fatalf("initial Action, open, start, interrupt and final persistence use the Shiro Task fence: actor=%s calls=%d", d.tasks.lastActor, d.tasks.fenceCalls)
	}
	if len(c.starts) != 1 {
		t.Fatal("a cancellation must not start anything again")
	}
}

func TestTerminalRunRacingCallerCancellationPersistsWithinGrace(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
			cancel()
			return c.runResult("completed", "passed"), nil
		}
		return c, nil
	}
	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if err != nil || result.Status != agent.NativeRunCompleted || result.Verification != agent.NativeVerificationPassed {
		t.Fatalf("an actual terminal result racing cancellation must persist within grace: result=%+v err=%v", result, err)
	}
	if len(d.rec.completed) != 1 || d.rec.completed[0].action != domainaction.StatusSucceeded || len(d.client(0).interrupts) != 0 {
		t.Fatalf("terminal result needs one completion and no interrupt: completions=%+v interrupts=%+v", d.rec.completed, d.client(0).interrupts)
	}
}

func TestDelegationAStopThatDoesNotEndTheRunIsAnUnknownOutcome(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		awaits := 0
		c.onAwait = func(ctx context.Context, _ string, _ client.AwaitOptions) (protocol.RunResult, error) {
			awaits++
			if awaits == 1 {
				<-ctx.Done()
				return protocol.RunResult{}, ctx.Err()
			}
			return protocol.RunResult{}, client.ErrRunStillActive
		}
		return c, nil
	}
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("a stop signal is a record, not proof: %v", err)
	}
	if len(d.rec.completed) != 0 || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("a nonterminal cancellation outcome keeps the pair active: action=%s attempt=%s", d.rec.action.Status, d.rec.attempt.Status)
	}
}

func TestDelegationALostConnectionWhileAwaitingIsAnUnknownOutcomeAndNotReStarted(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onAwait = func(context.Context, string, client.AwaitOptions) (protocol.RunResult, error) {
			c.end()
			return protocol.RunResult{}, client.ErrClosed
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("the end of the Run is unknown: %v", err)
	}
	if d.startCalls() != 1 {
		t.Fatalf("an accepted delegation must not be started again, starts = %d", d.startCalls())
	}
	if len(d.rec.completed) != 0 || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("unknown await keeps the Action open and Attempt running: action=%s attempt=%s", d.rec.action.Status, d.rec.attempt.Status)
	}
}

func TestDelegationLogsContainNoContentPathOrKeyAndNameHarnessIDsByOwner(t *testing.T) {
	d := newDeployment(t)
	delegate(t, d, secretText)
	var all strings.Builder
	for _, line := range d.log.all() {
		all.WriteString(line)
		all.WriteByte('\n')
	}
	mustNotContain(t, "log", all.String(), secretText, secretFinal, d.settings.HarnessBinary, d.settings.HarnessConfig, "mac", "origin_proof")
	accepted := d.log.events(t, eventDelegateAccepted)
	if len(accepted) != 1 {
		t.Fatalf("one acceptance line expected, got %d", len(accepted))
	}
	task, ok := accepted[0]["harness_task"].(map[string]any)
	if !ok || task["owner"] != agent.HarnessOwner || task["id"] != d.client(0).task {
		t.Fatalf("a Harness ID is logged only with its owner: %+v", accepted[0])
	}
	for _, field := range []string{"trace_id", "task_id", "action_id", "attempt_id", "run_id", "ts", "level", "event", "module", "schema_version"} {
		if _, ok := accepted[0][field]; !ok {
			t.Fatalf("log line misses %q: %+v", field, accepted[0])
		}
	}
	if accepted[0]["module"] != "RenCrow_CORE" {
		t.Fatalf("module = %v", accepted[0]["module"])
	}
	if got := len(d.log.events(t, eventDelegateFinished)); got != 1 {
		t.Fatalf("one finish line expected, got %d", got)
	}
}

func TestDelegationStartInputSatisfiesTheProtocolSchema(t *testing.T) {
	d := newDeployment(t)
	if _, err, _ := delegate(t, d, "x"); err != nil {
		t.Fatal(err)
	}
	// The fake refuses a payload that fails protocol.Encode like the real client,
	// so a delegation that succeeded sent schema-valid, canonical payloads. Check
	// the IDs that CORE issues are also in the Harness's ID grammar.
	start := d.client(0).starts[0]
	for name, id := range map[string]string{"task": start.Upstream.TaskID, "trace": start.Upstream.TraceID, "turn": *start.Upstream.TurnID, "action": *start.Upstream.ActionID, "attempt": *start.Upstream.AttemptID} {
		if id == "" {
			t.Fatalf("%s id is empty", name)
		}
	}
	if _, err := protocol.Encode(start); err != nil {
		t.Fatalf("the start payload must satisfy the schema: %v", err)
	}
}

func TestDelegationAnUnknownOutcomeStaysUnknownWhenLaterSendsFindTheConnectionClosed(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, _ protocol.StartInput) (protocol.StartResult, error) {
			if n == 1 {
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, client.ErrClosed) // written, then the connection ended
			}
			return protocol.StartResult{}, client.ErrClosed // later sends find it closed: not evidence that nothing was delivered
		}
		return c, nil // Done stays open: the connection is not replaced
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("a delivered request must not be reported as refused: %v", err)
	}
	if errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("an unknown outcome is not a block: %v", err)
	}
	if len(d.rec.completed) != 0 || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning ||
		d.rec.attempt.NativeDelegation.Start.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("later transport failures remain unknown without closing the pair: action=%s attempt=%s mutation=%+v", d.rec.action.Status, d.rec.attempt.Status, d.rec.attempt.NativeDelegation.Start)
	}
}

func TestDelegationAReplacementThatCannotBeStartedLeavesTheOutcomeUnknown(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(n int, _ client.Config) (*fakeClient, error) {
		if n > 1 {
			return nil, errors.New("down")
		}
		c := newFakeClient()
		c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
			c.end()
			return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, client.ErrClosed)
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("the request may have reached the first Harness: %v", err)
	}
}

func TestDelegationMutationDeadlineHonorsTurnCancellation(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "x")
	ctx, cancel := context.WithCancel(ctx)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onOpen = func(_ int, _ protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
			cancel() // the turn is cancelled while the session is being opened
			return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: c.session()}, nil
		}
		return c, nil
	}
	_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("a response lost as the turn is cancelled remains unknown: %v", err)
	}
	c := d.client(0)
	if len(c.starts) != 0 || len(c.awaits) != 0 {
		t.Fatalf("cancellation during open must prevent Start and Await: starts=%d awaits=%d", len(c.starts), len(c.awaits))
	}
	if len(c.openCtxErrs) != 1 || c.openCtxErrs[0] != nil || !c.openHadDeadline[0] {
		t.Fatalf("session/open receives the caller-bounded deadline: errors=%v deadlines=%v", c.openCtxErrs, c.openHadDeadline)
	}
	if d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
		t.Fatalf("cancelled open delivery leaves the Action active: action=%s attempt=%s", d.rec.action.Status, d.rec.attempt.Status)
	}
}

func TestDelegationCancellationWithoutTerminalRunResultKeepsTheActionOpen(t *testing.T) {
	cases := []struct {
		name           string
		awaitErr       error
		cancel         bool
		interruptError error
		wantInterrupts int
	}{
		{name: "run remains active", awaitErr: client.ErrRunStillActive, cancel: true, wantInterrupts: 1},
		{name: "interrupt transport unknown", awaitErr: client.ErrRunStillActive, cancel: true, interruptError: client.ErrClosed, wantInterrupts: 1},
		{name: "connection ended before cancellation", awaitErr: client.ErrClosed, wantInterrupts: 0},
		{name: "core shutdown", awaitErr: client.ErrStopping, wantInterrupts: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			ctx, input, messages := newTurn(t, "x")
			ctx, cancel := context.WithCancel(ctx)
			defer cancel()
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				awaits := 0
				c.onAwait = func(ctx context.Context, _ string, opts client.AwaitOptions) (protocol.RunResult, error) {
					if opts.InterruptKeyPrefix != "" {
						return protocol.RunResult{}, errors.New("implicit interrupt is forbidden")
					}
					awaits++
					if tc.cancel && awaits == 1 {
						cancel()
						<-ctx.Done()
						return protocol.RunResult{}, ctx.Err()
					}
					return protocol.RunResult{}, tc.awaitErr
				}
				c.onInterrupt = func(context.Context, string, string) (protocol.InterruptReceipt, error) {
					return protocol.InterruptReceipt{}, tc.interruptError
				}
				return c, nil
			}
			_, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("err = %v", err)
			}
			if len(d.rec.completed) != 0 || d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning {
				t.Fatalf("nonterminal await result keeps the pair active: action=%s attempt=%s completions=%+v", d.rec.action.Status, d.rec.attempt.Status, d.rec.completed)
			}
			if d.rec.attempt.NativeDelegation.RunResult != nil {
				t.Fatalf("no fabricated RunResult may be stored: %q", d.rec.attempt.NativeDelegation.RunResult)
			}
			if len(d.client(0).interrupts) != tc.wantInterrupts {
				t.Fatalf("interrupt calls=%d want=%d", len(d.client(0).interrupts), tc.wantInterrupts)
			}
		})
	}
}

func TestDelegationConcurrentInvocationUsesOneOpenAndOneStart(t *testing.T) {
	d := newDeployment(t)
	ctx, input, messages := newTurn(t, "same run")
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if err == nil && !result.Accepted() {
				err = errors.New("completed Run was not accepted")
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent delegation: %v", err)
		}
	}
	c := d.client(0)
	if len(d.rec.created) != 1 || len(c.opens) != 1 || len(c.starts) != 1 || len(c.awaits) != 1 || len(d.rec.completed) != 1 {
		t.Fatalf("same Task/Run must use one Action, Open, Start, Await and completion: actions=%d opens=%d starts=%d awaits=%d completions=%d",
			len(d.rec.created), len(c.opens), len(c.starts), len(c.awaits), len(d.rec.completed))
	}
}

func TestDelegationStaleTaskFenceStopsBeforeHarnessRPC(t *testing.T) {
	d := newDeployment(t)
	d.tasks.fenceError = errors.New("task writer generation is stale")
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingBlocked) {
		t.Fatalf("a stale generation blocks before sending: %v", err)
	}
	if d.startCalls() != 0 || len(d.rec.created) != 0 {
		t.Fatalf("stale Task fence must issue zero Action and Harness mutations: clients=%d actions=%d", d.startCalls(), len(d.rec.created))
	}
	if d.rec.action.ActionID != "" || d.rec.attempt.AttemptID != "" {
		t.Fatalf("stale Task fence must not create an owner pair: action=%+v attempt=%+v", d.rec.action, d.rec.attempt)
	}
}

func TestUnknownMutationRemainsUnknownWhenRetryFenceRefusesBeforeSecondRPC(t *testing.T) {
	d := newDeployment(t)
	d.tasks.fenceError = errors.New("Task fence refused after generation changed")
	d.tasks.fenceErrorAtCall = 3
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onOpen = func(n int, _ protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
			if n == 1 {
				return protocol.SessionOpenResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
			}
			return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: c.session()}, nil
		}
		return c, nil
	}
	_, err, _ := delegate(t, d, "x")
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
		t.Fatalf("a fence refusal cannot make a prior possibly delivered Open blocked: %v", err)
	}
	c := d.client(0)
	if len(c.opens) != 1 || len(c.starts) != 0 {
		t.Fatalf("retry fence refusal must prevent another RPC: opens=%d starts=%d", len(c.opens), len(c.starts))
	}
	if d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning || len(d.rec.completed) != 0 {
		t.Fatalf("unknown delivery must retain the active owner pair and parent: action=%s attempt=%s completions=%d", d.rec.action.Status, d.rec.attempt.Status, len(d.rec.completed))
	}
	if stored := d.rec.attempt.NativeDelegation.Open; stored == nil || stored.Status != domainaction.NativeMutationStatusDeliveryUnknown {
		t.Fatalf("the first Open uncertainty must remain stored: %+v", stored)
	}
}

func TestPersistenceUncertainRemoteMutationRemainsUnknown(t *testing.T) {
	for _, stage := range []domainaction.NativeMutationSlot{
		domainaction.NativeMutationSlotOpen,
		domainaction.NativeMutationSlotStart,
	} {
		t.Run(string(stage), func(t *testing.T) {
			d := newDeployment(t)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				if stage == domainaction.NativeMutationSlotOpen {
					c.onOpen = func(int, protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
						return protocol.SessionOpenResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{
							Code: protocol.CodePersistenceUncertain, Message: "commit outcome unknown", Retryable: true,
						}}
					}
				}
				if stage == domainaction.NativeMutationSlotStart {
					c.onStart = func(int, protocol.StartInput) (protocol.StartResult, error) {
						return protocol.StartResult{}, &client.RemoteError{RPCCode: -32000, Info: protocol.ErrorInfo{
							Code: protocol.CodePersistenceUncertain, Message: "commit outcome unknown", Retryable: true,
						}}
					}
				}
				return c, nil
			}
			_, err, _ := delegate(t, d, "x")
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("PERSISTENCE_UNCERTAIN must stay unknown for %s: %v", stage, err)
			}
			c := d.client(0)
			if stage == domainaction.NativeMutationSlotOpen {
				if len(c.opens) != maxSends || len(c.starts) != 0 {
					t.Fatalf("unknown Open retries must reuse the same mutation without advancing: opens=%d starts=%d", len(c.opens), len(c.starts))
				}
				for i := 1; i < len(c.opens); i++ {
					if !reflect.DeepEqual(c.opens[0], c.opens[i]) {
						t.Fatalf("unknown Open retries must preserve frozen bytes: first=%+v retry=%+v", c.opens[0], c.opens[i])
					}
				}
			} else {
				if len(c.opens) != 1 || len(c.starts) != maxSends || len(c.awaits) != 0 {
					t.Fatalf("unknown Start retries must not await a Run: opens=%d starts=%d awaits=%d", len(c.opens), len(c.starts), len(c.awaits))
				}
				for i := 1; i < len(c.starts); i++ {
					if !reflect.DeepEqual(c.starts[0], c.starts[i]) {
						t.Fatalf("unknown Start retries must preserve frozen bytes: first=%+v retry=%+v", c.starts[0], c.starts[i])
					}
				}
			}
			stored := nativeMutation(d.rec.attempt, stage)
			if stored == nil || stored.Status != domainaction.NativeMutationStatusDeliveryUnknown {
				t.Fatalf("PERSISTENCE_UNCERTAIN must not be classified as a definite rejection: mutation=%+v", stored)
			}
			if d.rec.action.Status != domainaction.StatusOpen || d.rec.attempt.Status != domainaction.AttemptStatusRunning || len(d.rec.completed) != 0 {
				t.Fatalf("unknown mutation must retain the active owner pair: action=%s attempt=%s completions=%d", d.rec.action.Status, d.rec.attempt.Status, len(d.rec.completed))
			}
		})
	}
}

func TestMutationSendRunsInsideShortFenceAndRetryWaitRunsOutside(t *testing.T) {
	d := newDeployment(t)
	d.startFn = func(int, client.Config) (*fakeClient, error) {
		c := newFakeClient()
		c.onStart = func(n int, in protocol.StartInput) (protocol.StartResult, error) {
			d.tasks.mu.Lock()
			active := d.tasks.fenceActive
			d.tasks.mu.Unlock()
			if !active {
				return protocol.StartResult{}, errors.New("Start ran outside the Task fence")
			}
			if n == 1 {
				return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
			}
			return c.startResult(in), nil
		}
		return c, nil
	}
	var backoffInsideFence bool
	d.runtime.backoff = func(int) time.Duration {
		d.tasks.mu.Lock()
		backoffInsideFence = backoffInsideFence || d.tasks.fenceActive
		d.tasks.mu.Unlock()
		return 0
	}
	if result, err, _ := delegate(t, d, "x"); err != nil || !result.Accepted() {
		t.Fatalf("frozen retry: result=%+v err=%v", result, err)
	}
	if backoffInsideFence {
		t.Fatal("retry backoff must run after releasing the Task fence")
	}
}

type faultActionOwner struct {
	ActionOwner
	failSlot     domainaction.NativeMutationSlot
	failOutcome  bool
	failComplete bool
}

func (f *faultActionOwner) RecordNativeMutationOutcome(ctx context.Context, in actionmanager.RecordNativeMutationOutcomeInput) (domainaction.Action, domainaction.Attempt, error) {
	if f.failOutcome && in.Slot == f.failSlot && in.Status == domainaction.NativeMutationStatusAccepted {
		f.failOutcome = false
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("injected persistence failure")
	}
	return f.ActionOwner.RecordNativeMutationOutcome(ctx, in)
}

func (f *faultActionOwner) CompleteNativeDelegation(ctx context.Context, in actionmanager.CompleteNativeDelegationInput) (domainaction.Action, domainaction.Attempt, error) {
	if f.failComplete {
		return domainaction.Action{}, domainaction.Attempt{}, errors.New("injected final persistence failure")
	}
	return f.ActionOwner.CompleteNativeDelegation(ctx, in)
}

func installActionOwner(t *testing.T, d *testDeployment, actions ActionOwner) {
	t.Helper()
	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatalf("close replaced Runtime: %v", err)
	}
	runtime, err := NewRuntime(d.settings, actions, d.tasks, Options{
		Start: d.start, Log: d.log.write, Backoff: func(int) time.Duration { return 0 },
		Now: func() time.Time { d.mu.Lock(); defer d.mu.Unlock(); return d.now },
	})
	if err != nil {
		t.Fatalf("NewRuntime: %v", err)
	}
	d.runtime = runtime
	t.Cleanup(func() { _ = runtime.Close(context.Background()) })
}

func persistedPair(t *testing.T, owner *actionmanager.Manager) (domainaction.Action, domainaction.Attempt) {
	t.Helper()
	actions, err := owner.ListActions(context.Background(), domainaction.Filter{})
	if err != nil || len(actions) != 1 {
		t.Fatalf("persisted Actions=%d err=%v", len(actions), err)
	}
	attempts, err := owner.ListAttempts(context.Background(), domainaction.AttemptFilter{ActionID: actions[0].ActionID})
	if err != nil || len(attempts) != 1 {
		t.Fatalf("persisted Attempts=%d err=%v", len(attempts), err)
	}
	return actions[0], attempts[0]
}

func TestDeliveryUnknownSurvivesRealStoreCloseReopenWithoutResend(t *testing.T) {
	cases := []struct {
		name          string
		slot          domainaction.NativeMutationSlot
		failPersist   bool
		lostResponse  bool
		wantOpenCalls int
		wantStartCall int
	}{
		{name: "lost Open response", slot: domainaction.NativeMutationSlotOpen, lostResponse: true, wantOpenCalls: maxSends},
		{name: "Open result persistence failure", slot: domainaction.NativeMutationSlotOpen, failPersist: true, wantOpenCalls: 1},
		{name: "lost Start response", slot: domainaction.NativeMutationSlotStart, lostResponse: true, wantOpenCalls: 1, wantStartCall: maxSends},
		{name: "Start result persistence failure", slot: domainaction.NativeMutationSlotStart, failPersist: true, wantOpenCalls: 1, wantStartCall: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newDeployment(t)
			root := t.TempDir()
			store, err := actionpersistence.NewJSONLStore(root)
			if err != nil {
				t.Fatal(err)
			}
			owner := actionmanager.New(store)
			faults := &faultActionOwner{ActionOwner: owner, failSlot: tc.slot, failOutcome: tc.failPersist}
			installActionOwner(t, d, faults)
			d.startFn = func(int, client.Config) (*fakeClient, error) {
				c := newFakeClient()
				if tc.slot == domainaction.NativeMutationSlotOpen {
					c.onOpen = func(_ int, _ protocol.SessionOpenInput) (protocol.SessionOpenResult, error) {
						if tc.lostResponse {
							return protocol.SessionOpenResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
						}
						return protocol.SessionOpenResult{ReceiptID: harnessID("rcp"), Session: c.session()}, nil
					}
				}
				if tc.slot == domainaction.NativeMutationSlotStart {
					c.onStart = func(_ int, in protocol.StartInput) (protocol.StartResult, error) {
						if tc.lostResponse {
							return protocol.StartResult{}, errors.Join(client.ErrOutcomeUnknown, context.DeadlineExceeded)
						}
						return c.startResult(in), nil
					}
				}
				return c, nil
			}
			ctx, input, messages := newTurn(t, "x")
			_, err = d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) {
				t.Fatalf("uncertain delivery must remain unknown: %v", err)
			}
			first := d.client(0)
			if len(first.opens) != tc.wantOpenCalls || len(first.starts) != tc.wantStartCall {
				t.Fatalf("wire calls Open=%d Start=%d want %d/%d", len(first.opens), len(first.starts), tc.wantOpenCalls, tc.wantStartCall)
			}
			if err := d.runtime.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			reopened, err := actionpersistence.NewJSONLStore(root)
			if err != nil {
				t.Fatalf("reopen Action store: %v", err)
			}
			reopenedOwner := actionmanager.New(reopened)
			installActionOwner(t, d, reopenedOwner)
			before := d.startCalls()
			_, err = d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
			if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) || d.startCalls() != before {
				t.Fatalf("reopened unknown outcome must refuse without client/RPC: err=%v starts=%d want=%d", err, d.startCalls(), before)
			}
			action, attempt := persistedPair(t, reopenedOwner)
			if action.Status != domainaction.StatusOpen || attempt.Status != domainaction.AttemptStatusRunning || len(attempt.NativeDelegation.RunResult) != 0 {
				t.Fatalf("reopened unknown must remain active without fabricated result: action=%s attempt=%s result=%q", action.Status, attempt.Status, attempt.NativeDelegation.RunResult)
			}
			mutation := attempt.NativeDelegation.Open
			if tc.slot == domainaction.NativeMutationSlotStart {
				if attempt.NativeDelegation.Open == nil || attempt.NativeDelegation.Open.Status != domainaction.NativeMutationStatusAccepted {
					t.Fatalf("accepted Open must survive before Start unknown: %+v", attempt.NativeDelegation.Open)
				}
				mutation = attempt.NativeDelegation.Start
			}
			if mutation == nil || mutation.Status != domainaction.NativeMutationStatusDeliveryUnknown {
				t.Fatalf("reopened mutation status=%+v, want delivery_unknown", mutation)
			}
		})
	}
}

func TestFinalRunResultPersistenceFailureReturnsUnknownWithoutProjectionOrSuccessLog(t *testing.T) {
	d := newDeployment(t)
	root := t.TempDir()
	store, err := actionpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	owner := actionmanager.New(store)
	faults := &faultActionOwner{ActionOwner: owner, failComplete: true}
	installActionOwner(t, d, faults)
	ctx, input, messages := newTurn(t, "x")
	result, err := d.runtime.DelegateNativeCoding(ctx, agent.NativeCodingRequest{Input: input, Messages: messages})
	if !errors.Is(err, agent.ErrNativeCodingOutcomeUnknown) || result.ActionID != "" || result.Status != "" {
		t.Fatalf("failed final persistence must return error without projection: result=%+v err=%v", result, err)
	}
	if len(d.log.events(t, eventDelegateFinished)) != 0 {
		t.Fatal("success must not be logged before final result persistence")
	}
	if err := d.runtime.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	reopened, err := actionpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	action, attempt := persistedPair(t, actionmanager.New(reopened))
	if action.Status != domainaction.StatusOpen || attempt.Status != domainaction.AttemptStatusRunning || len(attempt.NativeDelegation.RunResult) != 0 {
		t.Fatalf("failed completion must remain open with no RunResult: action=%s attempt=%s result=%q", action.Status, attempt.Status, attempt.NativeDelegation.RunResult)
	}
}
