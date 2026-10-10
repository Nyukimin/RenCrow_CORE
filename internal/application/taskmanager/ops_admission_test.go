package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func acceptedOPSReceipt() domainconversation.AcceptedOPSInputReceipt {
	return domainconversation.AcceptedOPSInputReceipt{
		AcceptanceSequence: 1,
		RequestID:          "request-accepted-ops-1",
		OwnerID:            "user-1",
		ActorID:            "user-1",
		SessionID:          modulecore.NewSessionID(),
		ThreadID:           modulecore.NewThreadID(),
		ThreadSeq:          modulecore.ThreadSeq(1),
		ThreadKind:         modulecore.ThreadKindUserConversation,
		TaskID:             modulecore.NewTaskID(),
		TurnID:             modulecore.NewTurnID(),
		TraceID:            modulecore.NewTraceID(),
		UserMessageID:      modulecore.NewMessageID(),
		AgentMessageID:     modulecore.NewMessageID(),
		DeclaredOrigin:     domainconversation.AcceptedOPSInputOriginAutomation,
		PayloadSHA256:      strings.Repeat("a", 64),
		RawRecordID:        "raw_accepted_ops_1",
		ManifestID:         "manifest_accepted_ops_1",
		RawSHA256:          strings.Repeat("b", 64),
		ManifestSHA256:     strings.Repeat("c", 64),
		AcceptedAt:         time.Date(2026, 10, 9, 4, 0, 0, 0, time.UTC),
	}
}

func acceptedOPSUserContext(t *testing.T, requestID, ownerID string) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(
		requestID,
		domaintool.ActorKindUser,
		ownerID,
		ownerID,
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}

func newAcceptedOPSManager(t *testing.T, limits ParallelLimits) (*Manager, *taskpersistence.JSONLStore) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	registerTaskStoreCleanup(t, store)
	return New(store, limits), store
}

func TestManagerAdmitAcceptedOPSCreatesCanonicalTaskAndFirstRun(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)

	claimed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil {
		t.Fatalf("AdmitAcceptedOPS: %v", err)
	}
	if claimed.Status != AcceptedOPSClaimed || !claimed.MayExecute {
		t.Fatalf("first claim = status %q may_execute=%v, want claimed with execution right", claimed.Status, claimed.MayExecute)
	}
	if claimed.Task.TaskID != receipt.TaskID || claimed.Run.TaskID != receipt.TaskID || claimed.Run.StartReason != domaintask.RunStartReasonFirst {
		t.Fatalf("claim IDs/run = task=%s run_task=%s reason=%s, want original TaskID and first Run", claimed.Task.TaskID, claimed.Run.TaskID, claimed.Run.StartReason)
	}
	if claimed.Task.Route != domaintask.RouteOperations || claimed.Task.OwnerID != domaintask.AcceptedOPSAgentAssignee || claimed.Task.Assignee != domaintask.AcceptedOPSAgentAssignee {
		t.Fatalf("canonical Task owner/route/assignee = %q/%q/%q", claimed.Task.OwnerID, claimed.Task.Route, claimed.Task.Assignee)
	}
	if claimed.Task.OriginSessionID != receipt.SessionID || claimed.Task.OriginThreadID != receipt.ThreadID ||
		claimed.Task.OriginTurnID != receipt.TurnID || claimed.Task.OriginMessageID != receipt.UserMessageID {
		t.Fatalf("Task origin IDs do not match accepted receipt: Task=%+v receipt=%+v", claimed.Task, receipt)
	}
	if claimed.Task.AcceptedOPSClaim == nil ||
		claimed.Task.AcceptedOPSClaim.ReceiptRef.RequestID != receipt.RequestID ||
		claimed.Task.AcceptedOPSClaim.ReceiptRef.OwnerID != receipt.OwnerID ||
		claimed.Task.AcceptedOPSClaim.ReceiptRef.PayloadSHA256 != receipt.PayloadSHA256 ||
		claimed.Task.AcceptedOPSClaim.BackendSelection != domainconversation.BackendShiroNativeCodingV1 {
		t.Fatalf("persisted accepted OPS claim reference = %+v", claimed.Task.AcceptedOPSClaim)
	}
	shared, err := manager.Context(ctx, receipt.TaskID)
	if err != nil || shared.UserIntent != "Accepted OPS request" {
		t.Fatalf("SharedRoleContext = %+v err=%v, want fixed non-raw description", shared, err)
	}

	replayReceipt := receipt
	replayReceipt.IdempotentReplay = true
	replay, err := manager.AdmitAcceptedOPS(ctx, replayReceipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil {
		t.Fatalf("AdmitAcceptedOPS replay: %v", err)
	}
	if replay.Status != AcceptedOPSAlreadyRunning || replay.MayExecute || replay.Task.TaskID != claimed.Task.TaskID || replay.Run.RunID != claimed.Run.RunID {
		t.Fatalf("same-writer replay = %+v, want original Task/Run with no execution right", replay)
	}
	tasks, err := manager.List(ctx, domaintask.Filter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Tasks after replay = %d err=%v, want one", len(tasks), err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 1 || runs[0].RunID != claimed.Run.RunID {
		t.Fatalf("Runs after replay = %+v err=%v, want the one original Run", runs, err)
	}
}

func TestManagerAdmitAcceptedOPSConcurrentClaimsHaveOneCreator(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	const callers = 12
	type response struct {
		claim AcceptedOPSClaimResult
		err   error
	}
	responses := make(chan response, callers)
	var wait sync.WaitGroup
	for range callers {
		wait.Add(1)
		go func() {
			defer wait.Done()
			claim, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
			responses <- response{claim: claim, err: err}
		}()
	}
	wait.Wait()
	close(responses)

	claimedCount := 0
	runIDs := map[modulecore.RunID]struct{}{}
	for response := range responses {
		if response.err != nil {
			t.Fatalf("concurrent AdmitAcceptedOPS: %v", response.err)
		}
		if response.claim.Task.TaskID != receipt.TaskID {
			t.Fatalf("concurrent claim TaskID = %s, want %s", response.claim.Task.TaskID, receipt.TaskID)
		}
		runIDs[response.claim.Run.RunID] = struct{}{}
		if response.claim.Status == AcceptedOPSClaimed && response.claim.MayExecute {
			claimedCount++
		} else if response.claim.Status != AcceptedOPSAlreadyRunning || response.claim.MayExecute {
			t.Fatalf("non-creator result = %+v, want already_running without execution right", response.claim)
		}
	}
	if claimedCount != 1 || len(runIDs) != 1 {
		t.Fatalf("creator count=%d distinct first Run IDs=%d, want exactly one each", claimedCount, len(runIDs))
	}
	tasks, err := manager.List(ctx, domaintask.Filter{})
	if err != nil || len(tasks) != 1 {
		t.Fatalf("Tasks after concurrent claim = %d err=%v, want one", len(tasks), err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 1 {
		t.Fatalf("Runs after concurrent claim = %d err=%v, want one", len(runs), err)
	}
}

func TestManagerAdmitAcceptedOPSRollsBackTaskAndContextWhenFirstRunFails(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	injected := errors.New("first Run append failed")
	manager := New(&faultTaskStore{Store: base, failOperation: "run", failErr: injected}, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	if _, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1); !errors.Is(err, injected) {
		t.Fatalf("AdmitAcceptedOPS failure = %v, want injected Run failure", err)
	}
	if _, err := base.GetTask(ctx, receipt.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("Task after failed claim = %v, want rollback", err)
	}
	if _, err := base.GetContext(ctx, receipt.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("context after failed claim = %v, want rollback", err)
	}
	runs, err := base.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs after failed claim = %+v err=%v, want none", runs, err)
	}

	recovered := New(base, DefaultParallelLimits())
	claimed, err := recovered.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed || !claimed.MayExecute {
		t.Fatalf("claim after rollback = %+v err=%v, want clean first claim", claimed, err)
	}
	if claimed.Task.TaskID != receipt.TaskID || claimed.Task.OriginTurnID != receipt.TurnID || claimed.Run.TaskID != receipt.TaskID {
		t.Fatalf("claim after rollback did not retain receipt IDs: %+v", claimed)
	}
}

func TestManagerAdmitAcceptedOPSReturnsOutcomeUnknownAfterWriterReopen(t *testing.T) {
	root := t.TempDir()
	opened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if opened != nil {
			_ = opened.Close()
		}
	})
	firstManager := New(opened, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	claimed, err := firstManager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed || !claimed.MayExecute {
		t.Fatalf("first claim = %+v err=%v", claimed, err)
	}
	firstGeneration, err := opened.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("close before reopen: %v", err)
	}
	opened = nil

	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	opened = reopened
	secondManager := New(reopened, DefaultParallelLimits())
	currentGeneration, err := reopened.WriterGeneration()
	if err != nil || currentGeneration <= firstGeneration {
		t.Fatalf("writer generations old=%d new=%d err=%v, want new generation", firstGeneration, currentGeneration, err)
	}
	replayed, err := secondManager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || replayed.Status != AcceptedOPSOutcomeUnknown || replayed.MayExecute {
		t.Fatalf("reopen replay = %+v err=%v, want outcome_unknown without execution right", replayed, err)
	}
	if replayed.Task.TaskID != receipt.TaskID || replayed.Run.RunID != claimed.Run.RunID || replayed.Run.WriterGeneration != firstGeneration {
		t.Fatalf("reopen replay lost canonical claim: %+v", replayed)
	}
	runs, err := secondManager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 1 || runs[0].RunID != claimed.Run.RunID {
		t.Fatalf("Runs after reopen = %+v err=%v, want original first Run only", runs, err)
	}
}

func TestManagerAdmitAcceptedOPSTerminalReplayDoesNotStartAnotherRun(t *testing.T) {
	for _, finish := range []struct {
		name string
		call func(*Manager, context.Context, modulecore.TaskID) error
	}{
		{name: "succeeded", call: func(manager *Manager, ctx context.Context, taskID modulecore.TaskID) error {
			_, err := manager.Succeed(ctx, taskID, "finished")
			return err
		}},
		{name: "cancelled", call: func(manager *Manager, ctx context.Context, taskID modulecore.TaskID) error {
			_, err := manager.Cancel(ctx, taskID, "cancelled")
			return err
		}},
	} {
		t.Run(finish.name, func(t *testing.T) {
			manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
			receipt := acceptedOPSReceipt()
			ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
			created, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
			if err != nil || created.Status != AcceptedOPSClaimed {
				t.Fatalf("first claim = %+v err=%v", created, err)
			}
			if err := finish.call(manager, ctx, receipt.TaskID); err != nil {
				t.Fatalf("finish Task: %v", err)
			}
			replayed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
			if err != nil || replayed.Status != AcceptedOPSTerminal || replayed.MayExecute || replayed.Run.RunID != created.Run.RunID {
				t.Fatalf("terminal replay = %+v err=%v, want original Run and no execution right", replayed, err)
			}
			runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
			if err != nil || len(runs) != 1 {
				t.Fatalf("Runs after terminal replay = %d err=%v, want one", len(runs), err)
			}
		})
	}
}

func TestManagerAdmitAcceptedOPSHistoryWithFirstInterruptedAndLaterActiveIsUnknown(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	claimed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed {
		t.Fatalf("first claim = %+v err=%v", claimed, err)
	}
	if _, err := manager.InterruptRun(ctx, receipt.TaskID, claimed.Run.RunID, "first writer stopped"); err != nil {
		t.Fatalf("interrupt first Run: %v", err)
	}
	secondRun, err := manager.StartRunWithReason(ctx, receipt.TaskID, domaintask.RunStartReasonProcessRestartResume)
	if err != nil || secondRun.RunID == claimed.Run.RunID || secondRun.Status != domaintask.RunStatusRunning {
		t.Fatalf("process-restart Run = %+v err=%v", secondRun, err)
	}

	replayed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || replayed.Status != AcceptedOPSOutcomeUnknown || replayed.MayExecute {
		t.Fatalf("replay with first interrupted and later active Run = %+v err=%v, want outcome_unknown without execution right", replayed, err)
	}
	if replayed.Run.RunID != claimed.Run.RunID {
		t.Fatalf("replay Run = %s, want canonical first Run %s", replayed.Run.RunID, claimed.Run.RunID)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil || len(runs) != 2 || runs[1].RunID != secondRun.RunID {
		t.Fatalf("Runs after replay = %+v err=%v, want unchanged two-Run history", runs, err)
	}
}

func newResumeEligibleAcceptedOPS(t *testing.T) (*Manager, domainconversation.AcceptedOPSInputReceipt, domaintask.Task, domaintask.Run, domaintask.NativeOPSResumeSource) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	revision := strings.Repeat("a", 64)
	manager, err := NewWithExpectedCriteriaRevision(store, DefaultParallelLimits(), revision)
	if err != nil {
		t.Fatal(err)
	}
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	claimed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed {
		t.Fatalf("initial accepted OPS claim=%+v err=%v", claimed, err)
	}
	if _, err := manager.Cancel(ctx, receipt.TaskID, "cancelled by owner"); err != nil {
		t.Fatalf("cancel source Task: %v", err)
	}
	closedTask, err := manager.Get(ctx, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	closedRun, err := manager.GetRun(ctx, claimed.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	source := domaintask.NativeOPSResumeSource{
		ActionID: modulecore.NewActionID(), AttemptID: modulecore.NewAttemptID(),
		HarnessTaskID: "harness-task-1", ThreadID: "harness-thread-1", RunID: "harness-run-1", ReceiptID: "harness-receipt-1",
		RunResultSHA256: strings.Repeat("b", 64),
	}
	return manager, receipt, closedTask, closedRun, source
}

func TestManagerExecuteNativeOPSResumeActionEffectAcceptsCurrentAndAuthorizedPriorGeneration(t *testing.T) {
	root := t.TempDir()
	manager, store, workerContext, userContext, task, run, claim, input := newNativeOPSResumeEffectFixture(t, root)
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			if err := store.Close(); err != nil {
				t.Errorf("close current-generation Resume store: %v", err)
			}
		}
	})
	effectCalls := 0
	if err := manager.ExecuteNativeOPSResumeActionEffect(workerContext, task.TaskID, run.RunID, domaintask.AcceptedOPSAgentAssignee, claim, func(context.Context) error {
		effectCalls++
		return nil
	}); err != nil || effectCalls != 1 {
		t.Fatalf("current-generation Resume Action effect calls=%d err=%v", effectCalls, err)
	}
	oldGeneration := claim.WriterGeneration
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	storeClosed = true
	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store = reopened
	storeClosed = false
	restarted, err := NewWithExpectedCriteriaRevision(reopened, DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	currentGeneration, err := reopened.WriterGeneration()
	if err != nil || currentGeneration <= oldGeneration {
		t.Fatalf("writer generations old=%d current=%d err=%v", oldGeneration, currentGeneration, err)
	}
	replay, err := restarted.AdmitNativeOPSResume(userContext, input)
	if err != nil || replay.MayExecute || replay.Claim != claim || replay.Run.RunID != run.RunID {
		t.Fatalf("prior-generation replay claim=%+v err=%v", replay, err)
	}
	effectCalls = 0
	if err := restarted.ExecuteNativeOPSResumeActionEffect(workerContext, task.TaskID, run.RunID, domaintask.AcceptedOPSAgentAssignee, claim, func(context.Context) error {
		effectCalls++
		return nil
	}); err != nil || effectCalls != 1 {
		t.Fatalf("authorized prior-generation known-result effect calls=%d err=%v", effectCalls, err)
	}
}

func TestManagerExecuteNativeOPSResumeActionEffectHoldsTaskFenceAcrossPersistenceCallback(t *testing.T) {
	manager, store, workerContext, userContext, task, run, claim, _ := newNativeOPSResumeEffectFixture(t, t.TempDir())
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Resume store: %v", err)
		}
	})
	entered := make(chan struct{})
	release := make(chan struct{})
	effectDone := make(chan error, 1)
	effectCalls := 0
	go func() {
		effectDone <- manager.ExecuteNativeOPSResumeActionEffect(workerContext, task.TaskID, run.RunID,
			domaintask.AcceptedOPSAgentAssignee, claim, func(context.Context) error {
				effectCalls++
				close(entered)
				<-release
				return nil
			})
	}()
	<-entered
	cancelStarted := make(chan struct{})
	cancelDone := make(chan error, 1)
	go func() {
		close(cancelStarted)
		_, err := manager.Cancel(userContext, task.TaskID, "cancellation requested during Resume Action persistence")
		cancelDone <- err
	}()
	<-cancelStarted
	select {
	case err := <-cancelDone:
		t.Fatalf("same-Task cancellation bypassed the Action persistence fence: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-effectDone; err != nil {
		t.Fatalf("fenced terminal Action persistence callback: %v", err)
	}
	if err := <-cancelDone; err != nil {
		t.Fatalf("cancellation should proceed after the fenced callback: %v", err)
	}
	if effectCalls != 1 {
		t.Fatalf("Task fence must admit exactly one terminal Action callback, got %d", effectCalls)
	}
}

func TestManagerExecuteNativeOPSResumeActionEffectRefusesChangedTaskAuthorityBeforeCallback(t *testing.T) {
	for _, change := range []string{"cancelled", "newer_run", "wrong_user", "wrong_request", "changed_source", "changed_trace", "future_generation", "zero_generation"} {
		t.Run(change, func(t *testing.T) {
			manager, store, workerContext, userContext, task, run, claim, _ := newNativeOPSResumeEffectFixture(t, t.TempDir())
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close Resume store: %v", err)
				}
			})
			ctx := workerContext
			switch change {
			case "cancelled":
				if _, err := manager.Cancel(userContext, task.TaskID, "cancel before Resume finalization"); err != nil {
					t.Fatal(err)
				}
			case "newer_run":
				if _, err := manager.CompleteRun(userContext, task.TaskID, run.RunID, domaintask.AcceptedOPSAgentAssignee, domaintask.StatusFailed, "resume failed", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.StartRunWithReason(userContext, task.TaskID, domaintask.RunStartReasonExplicitRerun); err != nil {
					t.Fatal(err)
				}
			case "wrong_user":
				claim.OwnerUserID = "different-user"
			case "wrong_request":
				ctx, _ = domaintool.DeriveAgentToolExecutionScope(acceptedOPSUserContext(t, "different-request", "user-1"), "different-request", "shiro", "worker", "ops", true)
			case "changed_source":
				checkpointID := "different-checkpoint"
				claim.Source.CheckpointID = &checkpointID
			case "changed_trace":
				claim.TraceID = modulecore.NewTraceID()
			case "future_generation":
				claim.WriterGeneration++
			case "zero_generation":
				claim.WriterGeneration = 0
			}
			beforeTask, err := manager.Get(userContext, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := manager.ListRuns(userContext, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil {
				t.Fatal(err)
			}
			called := false
			err = manager.ExecuteNativeOPSResumeActionEffect(ctx, task.TaskID, run.RunID, domaintask.AcceptedOPSAgentAssignee, claim, func(context.Context) error {
				called = true
				return nil
			})
			if err == nil || called {
				t.Fatalf("changed authority must refuse before Action callback: called=%v err=%v", called, err)
			}
			afterTask, err := manager.Get(userContext, task.TaskID)
			if err != nil || !reflect.DeepEqual(afterTask, beforeTask) {
				t.Fatalf("refused Action effect changed Task: before=%+v after=%+v err=%v", beforeTask, afterTask, err)
			}
			afterRuns, err := manager.ListRuns(userContext, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil || !reflect.DeepEqual(afterRuns, runs) {
				t.Fatalf("refused Action effect changed Runs: before=%+v after=%+v err=%v", runs, afterRuns, err)
			}
		})
	}
}

func TestManagerCompleteNativeOPSResumeRunCurrentAndPriorGenerationIsExactAndIdempotent(t *testing.T) {
	for _, priorGeneration := range []bool{false, true} {
		t.Run(map[bool]string{false: "current_generation", true: "authorized_prior_generation"}[priorGeneration], func(t *testing.T) {
			root := t.TempDir()
			manager, store, workerContext, _, task, run, claim, _ := newNativeOPSResumeEffectFixture(t, root)
			storeClosed := false
			t.Cleanup(func() {
				if !storeClosed {
					if err := store.Close(); err != nil {
						t.Errorf("close Resume store: %v", err)
					}
				}
			})
			if priorGeneration {
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				storeClosed = true
				reopened, err := taskpersistence.NewJSONLStore(root)
				if err != nil {
					t.Fatal(err)
				}
				store = reopened
				storeClosed = false
				manager, err = NewWithExpectedCriteriaRevision(reopened, DefaultParallelLimits(), strings.Repeat("a", 64))
				if err != nil {
					t.Fatal(err)
				}
			}
			completed, err := manager.CompleteNativeOPSResumeRun(workerContext, task.TaskID, run.RunID,
				domaintask.AcceptedOPSAgentAssignee, claim, domaintask.StatusFailed, "native Resume failed", "")
			if err != nil || completed.Status != domaintask.StatusFailed || completed.ExpectedCriteriaRevision != task.ExpectedCriteriaRevision ||
				len(completed.NativeResumeClaims) != 1 || completed.NativeResumeClaims[0] != claim {
				t.Fatalf("Resume Task completion=%+v err=%v", completed, err)
			}
			storedRun, err := manager.GetRun(workerContext, run.RunID)
			if err != nil || storedRun.Status != domaintask.RunStatusFailed || storedRun.WriterGeneration != claim.WriterGeneration {
				t.Fatalf("Resume Run completion=%+v err=%v", storedRun, err)
			}
			updatedAt := completed.UpdatedAt
			replayed, err := manager.CompleteNativeOPSResumeRun(workerContext, task.TaskID, run.RunID,
				domaintask.AcceptedOPSAgentAssignee, claim, domaintask.StatusFailed, "native Resume failed", "")
			if err != nil || replayed.Status != domaintask.StatusFailed || !replayed.UpdatedAt.Equal(updatedAt) {
				t.Fatalf("identical Task finalization replay rewrote or changed terminal state: task=%+v err=%v", replayed, err)
			}
		})
	}
}

func TestManagerCompleteNativeOPSResumeRunRefusesCancellationAndNewerRun(t *testing.T) {
	for _, change := range []string{"cancelled", "newer_run"} {
		t.Run(change, func(t *testing.T) {
			manager, store, workerContext, userContext, task, run, claim, _ := newNativeOPSResumeEffectFixture(t, t.TempDir())
			t.Cleanup(func() {
				if err := store.Close(); err != nil {
					t.Errorf("close Resume store: %v", err)
				}
			})
			switch change {
			case "cancelled":
				if _, err := manager.Cancel(userContext, task.TaskID, "cancel before Resume completion"); err != nil {
					t.Fatal(err)
				}
			case "newer_run":
				if _, err := manager.CompleteRun(userContext, task.TaskID, run.RunID, domaintask.AcceptedOPSAgentAssignee, domaintask.StatusFailed, "resume failed", ""); err != nil {
					t.Fatal(err)
				}
				if _, err := manager.StartRunWithReason(userContext, task.TaskID, domaintask.RunStartReasonExplicitRerun); err != nil {
					t.Fatal(err)
				}
			}
			before, err := manager.Get(userContext, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := manager.CompleteNativeOPSResumeRun(workerContext, task.TaskID, run.RunID,
				domaintask.AcceptedOPSAgentAssignee, claim, domaintask.StatusFailed, "native Resume failed", ""); err == nil {
				t.Fatalf("must refuse %s before Task completion", change)
			}
			after, err := manager.Get(userContext, task.TaskID)
			if err != nil || !reflect.DeepEqual(after, before) {
				t.Fatalf("refused %s completion changed Task: before=%+v after=%+v err=%v", change, before, after, err)
			}
		})
	}
}

func TestManagerExecuteNativeOPSResumeClaimPreventsReassignment(t *testing.T) {
	manager, store, _, userContext, task, _, _, _ := newNativeOPSResumeEffectFixture(t, t.TempDir())
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close Resume store: %v", err)
		}
	})
	before, err := manager.Get(userContext, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.RecordAssignment(userContext, task.TaskID, "Mio", modulecore.NewEventID()); err == nil {
		t.Fatal("accepted OPS Task with an immutable Resume claim must not be reassigned")
	}
	after, err := manager.Get(userContext, task.TaskID)
	if err != nil || !reflect.DeepEqual(after, before) {
		t.Fatalf("refused reassignment changed Task: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestManagerVerifyNativeOPSResumeReplayAllowsPriorGenerationOnlyForExactLatestClaim(t *testing.T) {
	root := t.TempDir()
	manager, store, workerContext, _, task, run, claim, _ := newNativeOPSResumeEffectFixture(t, root)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("close replay Task store: %v", err)
		}
	})
	manager, err = NewWithExpectedCriteriaRevision(reopened, DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.VerifyNativeOPSResumeReplay(workerContext, task.TaskID, run.RunID,
		domaintask.AcceptedOPSAgentAssignee, claim, domaintask.StatusFailed); err != nil {
		t.Fatalf("prior-generation known-result replay should remain read-only and authorized: %v", err)
	}
	if _, err := manager.CompleteNativeOPSResumeRun(workerContext, task.TaskID, run.RunID,
		domaintask.AcceptedOPSAgentAssignee, claim, domaintask.StatusCancelled, "cancelled", ""); err != nil {
		t.Fatal(err)
	}
	if err := manager.VerifyNativeOPSResumeReplay(workerContext, task.TaskID, run.RunID,
		domaintask.AcceptedOPSAgentAssignee, claim, domaintask.StatusFailed); err == nil {
		t.Fatal("read-only replay with a conflicting terminal Task outcome must be refused")
	}
}

func newNativeOPSResumeEffectFixture(t *testing.T, root string) (*Manager, *taskpersistence.JSONLStore, context.Context, context.Context, domaintask.Task, domaintask.Run, domaintask.NativeOPSResumeClaim, NativeOPSResumeInput) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewWithExpectedCriteriaRevision(store, DefaultParallelLimits(), strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	receipt := acceptedOPSReceipt()
	sourceUserContext := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	accepted, err := manager.AdmitAcceptedOPS(sourceUserContext, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || accepted.Status != AcceptedOPSClaimed {
		t.Fatalf("admit initial native OPS Task=%+v err=%v", accepted, err)
	}
	if _, err := manager.Cancel(sourceUserContext, receipt.TaskID, "source run cancelled"); err != nil {
		t.Fatalf("close source OPS Task: %v", err)
	}
	sourceRun, err := manager.GetRun(sourceUserContext, accepted.Run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	requestID := "req-native-resume-effect-fence"
	userContext := acceptedOPSUserContext(t, requestID, receipt.OwnerID)
	source := domaintask.NativeOPSResumeSource{
		ActionID: modulecore.NewActionID(), AttemptID: modulecore.NewAttemptID(), HarnessTaskID: "harness-task-effect",
		ThreadID: "harness-thread-effect", RunID: "harness-run-effect", ReceiptID: "harness-receipt-effect", RunResultSHA256: strings.Repeat("d", 64),
	}
	input := NativeOPSResumeInput{TaskID: receipt.TaskID, ExpectedCoreRunID: sourceRun.RunID, Source: source}
	resume, err := manager.AdmitNativeOPSResume(userContext, input)
	if err != nil || !resume.MayExecute || resume.Claim.Validate(resume.Task) != nil {
		t.Fatalf("admit fenced Resume=%+v err=%v", resume, err)
	}
	workerContext, err := domaintool.DeriveAgentToolExecutionScope(userContext, requestID,
		domaintask.AcceptedOPSAgentAssignee, "worker", "ops", true)
	if err != nil {
		t.Fatal(err)
	}
	return manager, store, workerContext, userContext, resume.Task, resume.Run, resume.Claim, input
}

func TestManagerAdmitNativeOPSResumeAtomicallyAddsSameTaskRunTraceAndClaim(t *testing.T) {
	manager, receipt, task, sourceRun, source := newResumeEligibleAcceptedOPS(t)
	ctx := acceptedOPSUserContext(t, "req-native-resume-1", receipt.OwnerID)
	result, err := manager.AdmitNativeOPSResume(ctx, NativeOPSResumeInput{
		TaskID: receipt.TaskID, ExpectedCoreRunID: sourceRun.RunID, Source: source,
	})
	if err != nil {
		t.Fatalf("AdmitNativeOPSResume: %v", err)
	}
	if !result.MayExecute || result.Status != NativeOPSResumeClaimed || result.Task.TaskID != task.TaskID || result.Task.Status != domaintask.StatusRunning ||
		result.Run.TaskID != task.TaskID || result.Run.StartReason != domaintask.RunStartReasonExplicitRerun || result.Run.Status != domaintask.RunStatusRunning ||
		result.Run.RunID == sourceRun.RunID || result.Run.TraceID == "" || result.Run.TraceID != result.Claim.TraceID {
		t.Fatalf("resume claim/result=%+v, want one new same-Task explicit Run and Trace", result)
	}
	if result.Task.ExpectedCriteriaRevision != task.ExpectedCriteriaRevision || result.Task.AcceptedOPSClaim == nil || *result.Task.AcceptedOPSClaim != *task.AcceptedOPSClaim {
		t.Fatalf("resume changed frozen criteria or original accepted claim: before=%+v after=%+v", task, result.Task)
	}
	if result.Claim.RequestID != "req-native-resume-1" || result.Claim.OwnerUserID != receipt.OwnerID || result.Claim.ExpectedCoreRunID != sourceRun.RunID ||
		result.Claim.NewCoreRunID != result.Run.RunID || result.Claim.Source != source {
		t.Fatalf("resume Task claim does not bind authenticated request, old Run, new Run, and source: %+v", result.Claim)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 || runs[1].RunID != result.Run.RunID {
		t.Fatalf("same-Task Run history=%+v err=%v, want original and one explicit resume", runs, err)
	}
}

func TestManagerAdmitNativeOPSResumeSameRequestReturnsOneUnknownClaimAndDifferentPayloadConflicts(t *testing.T) {
	manager, receipt, task, sourceRun, source := newResumeEligibleAcceptedOPS(t)
	ctx := acceptedOPSUserContext(t, "req-native-resume-replay", receipt.OwnerID)
	input := NativeOPSResumeInput{TaskID: task.TaskID, ExpectedCoreRunID: sourceRun.RunID, Source: source}
	first, err := manager.AdmitNativeOPSResume(ctx, input)
	if err != nil || !first.MayExecute {
		t.Fatalf("first resume admission=%+v err=%v", first, err)
	}
	replay, err := manager.AdmitNativeOPSResume(ctx, input)
	if err != nil || replay.MayExecute || replay.Status != NativeOPSResumeOutcomeUnknown || replay.Run.RunID != first.Run.RunID || replay.Claim != first.Claim {
		t.Fatalf("same-request replay=%+v err=%v, want original claim as unknown without execution", replay, err)
	}
	different := input
	different.ExpectedCoreRunID = modulecore.NewRunID()
	if _, err := manager.AdmitNativeOPSResume(ctx, different); !errors.Is(err, ErrNativeOPSResumeRejected) {
		t.Fatalf("same request with a different payload error=%v, want conflict", err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("same-request replay created extra Runs: %d err=%v", len(runs), err)
	}
}

func TestManagerAdmitNativeOPSResumeReplayRejectsWhenClaimRunIsNoLongerLatest(t *testing.T) {
	manager, receipt, task, sourceRun, source := newResumeEligibleAcceptedOPS(t)
	ctx := acceptedOPSUserContext(t, "req-native-resume-old-run", receipt.OwnerID)
	input := NativeOPSResumeInput{TaskID: task.TaskID, ExpectedCoreRunID: sourceRun.RunID, Source: source}
	first, err := manager.AdmitNativeOPSResume(ctx, input)
	if err != nil || !first.MayExecute {
		t.Fatalf("first resume admission=%+v err=%v", first, err)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, first.Run.RunID, domaintask.AcceptedOPSAgentAssignee, domaintask.StatusFailed, "failed", ""); err != nil {
		t.Fatalf("complete first resume Run: %v", err)
	}
	if _, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun); err != nil {
		t.Fatalf("start later explicit Run: %v", err)
	}
	if _, err := manager.AdmitNativeOPSResume(ctx, input); !errors.Is(err, ErrNativeOPSResumeRejected) {
		t.Fatalf("replay of an older claimed Run error=%v, want rejection", err)
	}
}

func TestManagerAdmitNativeOPSResumeRejectsWrongUserStaleRunAndInvalidSourceBeforeWrites(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *Manager, domainconversation.AcceptedOPSInputReceipt, domaintask.Run, *NativeOPSResumeInput, context.Context)
	}{
		{name: "wrong authenticated user", setup: func(_ *testing.T, _ *Manager, receipt domainconversation.AcceptedOPSInputReceipt, _ domaintask.Run, _ *NativeOPSResumeInput, ctx context.Context) {
			_ = ctx
		}},
		{name: "stale expected core run", setup: func(_ *testing.T, _ *Manager, _ domainconversation.AcceptedOPSInputReceipt, _ domaintask.Run, input *NativeOPSResumeInput, _ context.Context) {
			input.ExpectedCoreRunID = modulecore.NewRunID()
		}},
		{name: "source action missing", setup: func(_ *testing.T, _ *Manager, _ domainconversation.AcceptedOPSInputReceipt, _ domaintask.Run, input *NativeOPSResumeInput, _ context.Context) {
			input.Source.ActionID = ""
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager, receipt, task, sourceRun, source := newResumeEligibleAcceptedOPS(t)
			requestID, userID := "req-native-resume-denied", receipt.OwnerID
			input := NativeOPSResumeInput{TaskID: task.TaskID, ExpectedCoreRunID: sourceRun.RunID, Source: source}
			ctx := acceptedOPSUserContext(t, requestID, userID)
			if tc.name == "wrong authenticated user" {
				ctx = acceptedOPSUserContext(t, requestID, "other-user")
			}
			tc.setup(t, manager, receipt, sourceRun, &input, ctx)
			if _, err := manager.AdmitNativeOPSResume(ctx, input); !errors.Is(err, ErrNativeOPSResumeRejected) {
				t.Fatalf("AdmitNativeOPSResume error=%v, want owner rejection", err)
			}
			runs, err := manager.ListRuns(acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID), domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil || len(runs) != 1 || runs[0].RunID != sourceRun.RunID {
				t.Fatalf("denied resume changed Run history: %+v err=%v", runs, err)
			}
			stored, err := manager.Get(acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID), task.TaskID)
			if err != nil || stored.Status != domaintask.StatusCancelled || stored.ExpectedCriteriaRevision != task.ExpectedCriteriaRevision || len(stored.NativeResumeClaims) != 0 {
				t.Fatalf("denied resume changed Task: %+v err=%v", stored, err)
			}
		})
	}
}

func TestManagerAdmitAcceptedOPSRunningTaskWithFirstClosedRunIsUnknown(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	claimed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed {
		t.Fatalf("first claim = %+v err=%v", claimed, err)
	}
	if _, err := manager.InterruptRun(ctx, receipt.TaskID, claimed.Run.RunID, "writer stopped"); err != nil {
		t.Fatalf("interrupt first Run: %v", err)
	}

	replayed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || replayed.Status != AcceptedOPSOutcomeUnknown || replayed.MayExecute {
		t.Fatalf("replay with running Task and closed first Run = %+v err=%v, want outcome_unknown without execution right", replayed, err)
	}
	if replayed.Run.RunID != claimed.Run.RunID {
		t.Fatalf("replay Run = %s, want canonical first Run %s", replayed.Run.RunID, claimed.Run.RunID)
	}
}

func TestManagerAdmitAcceptedOPSTerminalTaskWithRunningFirstRunIsUnknown(t *testing.T) {
	manager, store := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	claimed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed {
		t.Fatalf("first claim = %+v err=%v", claimed, err)
	}
	terminalTask := claimed.Task
	terminalTask.Status = domaintask.StatusSucceeded
	finishedAt := claimed.Run.StartedAt.Add(time.Second)
	terminalTask.FinishedAt = &finishedAt
	terminalTask.UpdatedAt = finishedAt
	terminalTask.Summary = "fixture terminal Task with active first Run"
	if err := store.SaveTask(ctx, terminalTask); err != nil {
		t.Fatalf("save mismatched terminal Task fixture: %v", err)
	}

	replayed, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || replayed.Status != AcceptedOPSOutcomeUnknown || replayed.MayExecute {
		t.Fatalf("replay with terminal Task and running first Run = %+v err=%v, want outcome_unknown without execution right", replayed, err)
	}
	if replayed.Run.RunID != claimed.Run.RunID {
		t.Fatalf("replay Run = %s, want canonical first Run %s", replayed.Run.RunID, claimed.Run.RunID)
	}
}

func TestManagerAdmitAcceptedOPSBlocksCapacityWithoutPartialClaim(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, ParallelLimits{Global: 1, PerModule: 1, CodingTasks: 1, LongResearchTasks: 1, DestructiveTasks: 1})
	ctx := context.Background()
	occupying, err := manager.Create(ctx, domaintask.Task{Title: "occupy capacity", Route: domaintask.RouteGeneral}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartRunWithReason(ctx, occupying.TaskID, domaintask.RunStartReasonFirst); err != nil {
		t.Fatal(err)
	}
	receipt := acceptedOPSReceipt()
	userCtx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	blocked, err := manager.AdmitAcceptedOPS(userCtx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || blocked.Status != AcceptedOPSBlocked || blocked.MayExecute {
		t.Fatalf("capacity claim = %+v err=%v, want blocked without execution right", blocked, err)
	}
	if _, err := manager.Get(userCtx, receipt.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("Task after blocked claim = %v, want no partial Task", err)
	}
	if _, err := manager.Context(userCtx, receipt.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("context after blocked claim = %v, want no partial context", err)
	}
	if _, err := manager.Succeed(ctx, occupying.TaskID, "capacity released"); err != nil {
		t.Fatal(err)
	}
	claimed, err := manager.AdmitAcceptedOPS(userCtx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || claimed.Status != AcceptedOPSClaimed || !claimed.MayExecute || claimed.Task.TaskID != receipt.TaskID {
		t.Fatalf("claim after capacity release = %+v err=%v", claimed, err)
	}
}

func TestManagerAdmitAcceptedOPSRejectsScopeAndReceiptMismatchesBeforeMutation(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	counting := &countingTaskTransactions{Store: base}
	manager := New(counting, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	validCtx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)

	tests := []struct {
		name    string
		ctx     context.Context
		in      domainconversation.AcceptedOPSInputReceipt
		backend domainconversation.BackendSelection
	}{
		{name: "missing scope", ctx: context.Background(), in: receipt, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "public only", ctx: acceptedOPSContextWithScope(t, receipt.RequestID, domaintool.ActorKindUser, receipt.OwnerID, receipt.OwnerID, []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceHTTP), in: receipt, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "agent owner forgery", ctx: acceptedOPSContextWithScope(t, receipt.RequestID, domaintool.ActorKindAgent, "shiro", receipt.OwnerID, []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator), in: receipt, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "scope request mismatch", ctx: acceptedOPSUserContext(t, "different-request", receipt.OwnerID), in: receipt, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "unsupported backend", ctx: validCtx, in: receipt, backend: domainconversation.BackendSelection("arbitrary_backend")},
		{name: "invalid receipt", ctx: validCtx, in: domainconversation.AcceptedOPSInputReceipt{}, backend: domainconversation.BackendShiroNativeCodingV1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			counting.calls = 0
			if _, err := manager.AdmitAcceptedOPS(test.ctx, test.in, test.backend); !errors.Is(err, ErrAcceptedOPSClaimRejected) {
				t.Fatalf("AdmitAcceptedOPS error = %v, want ErrAcceptedOPSClaimRejected", err)
			}
			if counting.calls != 0 {
				t.Fatalf("TaskTransaction calls = %d, want rejected before mutation boundary", counting.calls)
			}
		})
	}
	items, err := base.ListTasks(context.Background(), domaintask.Filter{})
	if err != nil || len(items) != 0 {
		t.Fatalf("Tasks after invalid admission = %d err=%v, want none", len(items), err)
	}
}

func TestManagerAdmitAcceptedOPSRejectsChangedCanonicalReceiptWithoutMutation(t *testing.T) {
	manager, _ := newAcceptedOPSManager(t, DefaultParallelLimits())
	receipt := acceptedOPSReceipt()
	ctx := acceptedOPSUserContext(t, receipt.RequestID, receipt.OwnerID)
	created, err := manager.AdmitAcceptedOPS(ctx, receipt, domainconversation.BackendShiroNativeCodingV1)
	if err != nil || created.Status != AcceptedOPSClaimed {
		t.Fatalf("initial claim = %+v err=%v", created, err)
	}
	beforeTask, err := manager.Get(ctx, receipt.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
	if err != nil {
		t.Fatal(err)
	}

	changedOwner := receipt
	changedOwner.OwnerID = "user-2"
	changedOwner.ActorID = changedOwner.OwnerID
	changedRequest := receipt
	changedRequest.RequestID = "request-accepted-ops-2"
	changedPayload := receipt
	changedPayload.PayloadSHA256 = strings.Repeat("d", 64)
	changedOrigin := receipt
	changedOrigin.TurnID = modulecore.NewTurnID()
	tests := []struct {
		name    string
		ctx     context.Context
		receipt domainconversation.AcceptedOPSInputReceipt
		backend domainconversation.BackendSelection
	}{
		{name: "owner", ctx: acceptedOPSUserContext(t, changedOwner.RequestID, changedOwner.OwnerID), receipt: changedOwner, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "request", ctx: acceptedOPSUserContext(t, changedRequest.RequestID, changedRequest.OwnerID), receipt: changedRequest, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "payload hash", ctx: ctx, receipt: changedPayload, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "origin IDs", ctx: ctx, receipt: changedOrigin, backend: domainconversation.BackendShiroNativeCodingV1},
		{name: "backend", ctx: ctx, receipt: receipt, backend: domainconversation.BackendSelection("different_backend")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.AdmitAcceptedOPS(test.ctx, test.receipt, test.backend); !errors.Is(err, ErrAcceptedOPSClaimRejected) {
				t.Fatalf("changed receipt error = %v, want ErrAcceptedOPSClaimRejected", err)
			}
			afterTask, err := manager.Get(ctx, receipt.TaskID)
			if err != nil || !reflect.DeepEqual(afterTask, beforeTask) {
				t.Fatalf("Task changed after rejection: before=%+v after=%+v err=%v", beforeTask, afterTask, err)
			}
			afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: receipt.TaskID})
			if err != nil || len(afterRuns) != len(beforeRuns) || afterRuns[0].RunID != beforeRuns[0].RunID {
				t.Fatalf("Runs changed after rejection: before=%+v after=%+v err=%v", beforeRuns, afterRuns, err)
			}
		})
	}
}

type countingTaskTransactions struct {
	Store
	calls int
}

func (s *countingTaskTransactions) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(Store) error) error {
	s.calls++
	return s.Store.TaskTransaction(ctx, taskID, fn)
}

func acceptedOPSContextWithScope(
	t *testing.T,
	requestID string,
	actorKind domaintool.ActorKind,
	actorID string,
	authenticatedUserID string,
	allowedScopes []string,
	authSource domaintool.AuthenticationSource,
) context.Context {
	t.Helper()
	scope, err := domaintool.NewToolExecutionScope(requestID, actorKind, actorID, authenticatedUserID, allowedScopes, authSource)
	if err != nil {
		t.Fatalf("NewToolExecutionScope: %v", err)
	}
	return domaintool.WithToolExecutionScope(context.Background(), scope)
}
