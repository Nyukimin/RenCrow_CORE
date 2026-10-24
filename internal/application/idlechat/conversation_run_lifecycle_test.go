package idlechat

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newConversationLifecycleFixture(t *testing.T) (*IdleChatOrchestrator, *taskmanager.Manager) {
	t.Helper()
	owner := newTestIdleChatRunIssuer(t)
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	return orchestrator, owner
}

func setConversationChatActive(o *IdleChatOrchestrator) {
	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()
}

func assertConversationPair(t *testing.T, owner *taskmanager.Manager, taskID modulecore.TaskID, wantTask domaintask.Status, wantRun domaintask.RunStatus) {
	t.Helper()
	task, err := owner.Get(context.Background(), taskID)
	if err != nil {
		t.Fatalf("get conversation task %s: %v", taskID, err)
	}
	if task.Status != wantTask {
		t.Fatalf("conversation task %s status=%s, want %s", taskID, task.Status, wantTask)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("list conversation runs %s: %v", taskID, err)
	}
	if len(runs) != 1 {
		t.Fatalf("conversation task %s has %d runs, want one", taskID, len(runs))
	}
	if runs[0].Status != wantRun {
		t.Fatalf("conversation run %s status=%s, want %s", runs[0].RunID, runs[0].Status, wantRun)
	}
}

func activeConversationIDs(o *IdleChatOrchestrator) (modulecore.TaskID, modulecore.RunID) {
	o.emitMu.Lock()
	defer o.emitMu.Unlock()
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.activeTaskID, o.activeRunID
}

type blockingConversationCompletionOwner struct {
	*taskmanager.Manager
	started     chan struct{}
	release     chan struct{}
	startOnce   sync.Once
	releaseOnce sync.Once
	calls       atomic.Int32
}

func (o *blockingConversationCompletionOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	o.calls.Add(1)
	o.startOnce.Do(func() { close(o.started) })
	<-o.release
	return o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
}

func (o *blockingConversationCompletionOwner) unblock() {
	o.releaseOnce.Do(func() { close(o.release) })
}

func TestForegroundStopReturnsBeforeDefaultTaskOwnerCompletionAndEmitLock(t *testing.T) {
	o, manager := newConversationLifecycleFixture(t)
	if manager.IDIndexed() {
		t.Fatal("fixture must exercise the default index-disabled Task owner")
	}
	if manager.RunCompletionAdmissionAvailable() {
		t.Fatal("index-disabled Task owner unexpectedly supports RAM completion admission")
	}
	task, run, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "IdleChat foreground stop", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create conversation Task/Run: %v", err)
	}
	blockedOwner := &blockingConversationCompletionOwner{
		Manager: manager,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
	o.SetRunIssuer(blockedOwner)
	t.Cleanup(blockedOwner.unblock)
	ctx, cancel := context.WithCancel(context.Background())
	o.mu.Lock()
	o.chatActive = true
	o.manualMode = true
	o.activeGeneration = 12
	o.activeTaskID = task.TaskID
	o.activeRunID = run.RunID
	o.runCtx = ctx
	o.runCancel = cancel
	o.mu.Unlock()
	t.Cleanup(cancel)

	request := PlaybackStopRequest{RuntimeEpoch: "test-runtime", StateRevision: 3, Reason: "foreground_chat"}
	o.emitMu.Lock()
	stopDone := make(chan struct {
		receipt PlaybackStopReceipt
		err     error
	}, 1)
	go func() {
		receipt, err := o.StopPlaybackForForeground(context.Background(), request)
		stopDone <- struct {
			receipt PlaybackStopReceipt
			err     error
		}{receipt: receipt, err: err}
	}()
	var stopped struct {
		receipt PlaybackStopReceipt
		err     error
	}
	select {
	case stopped = <-stopDone:
	case <-time.After(time.Second):
		o.emitMu.Unlock()
		t.Fatal("foreground stop waited for emitMu")
	}
	o.emitMu.Unlock()
	if stopped.err != nil {
		t.Fatalf("StopPlaybackForForeground: %v", stopped.err)
	}
	if !stopped.receipt.PlaybackStopped || !stopped.receipt.ResumeBlocked || !stopped.receipt.CleanupPending || stopped.receipt.GenerationQuiesced {
		t.Fatalf("foreground stop receipt=%+v", stopped.receipt)
	}
	if ctx.Err() == nil {
		t.Fatal("foreground stop did not cancel the active IdleChat generation")
	}
	select {
	case <-blockedOwner.started:
	case <-time.After(time.Second):
		t.Fatal("managed finalizer did not reach the blocked synchronous Task owner")
	}
	o.mu.Lock()
	pending := o.pendingConversationRun != nil
	generation := o.activeGeneration
	o.mu.Unlock()
	if !pending {
		t.Fatal("pending Task/Run pair was cleared before Task owner completion")
	}

	// A retransmission of the same policy revision must not bump playback
	// generation or start a second CompleteRun while the first is blocked.
	replayed, err := o.StopPlaybackForForeground(context.Background(), request)
	if err != nil {
		t.Fatalf("replay foreground stop: %v", err)
	}
	if replayed.Generation != stopped.receipt.Generation || replayed.AppliedStateRevision != request.StateRevision {
		t.Fatalf("replayed receipt=%+v, initial=%+v", replayed, stopped.receipt)
	}
	if got := blockedOwner.calls.Load(); got != 1 {
		t.Fatalf("CompleteRun calls=%d while finalizer is blocked, want one", got)
	}
	if generation != stopped.receipt.Generation {
		t.Fatalf("active generation=%d, stop receipt generation=%d", generation, stopped.receipt.Generation)
	}

	done := o.conversationRunFinalizationWorkerDone()
	if done == nil {
		t.Fatal("managed conversation finalizer has no completion signal")
	}
	blockedOwner.unblock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("managed Task finalizer did not finish after owner release")
	}
	o.mu.Lock()
	pending = o.pendingConversationRun != nil
	o.mu.Unlock()
	if pending {
		t.Fatal("pending Task/Run pair remained after durable owner completion")
	}
	assertConversationPair(t, manager, task.TaskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

func TestForegroundStopReturnsWhileIndexedTaskPersistenceIsBlocked(t *testing.T) {
	store, err := taskpersistence.NewJSONLStoreWithOptions(t.TempDir(), taskpersistence.OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits(), taskmanager.WithIDIndex())
	queue, err := persistencequeue.NewPersistenceQueue(8, 8<<20)
	if err != nil {
		_ = manager.Close()
		t.Fatal(err)
	}
	if err := manager.SetPersistenceQueue(queue); err != nil {
		_ = manager.Close()
		_ = queue.Shutdown(context.Background())
		t.Fatalf("bind indexed Task owner to persistence queue: %v", err)
	}
	blockStarted := make(chan struct{})
	blockRelease := make(chan struct{})
	var releaseOnce sync.Once
	releaseBlocker := func() { releaseOnce.Do(func() { close(blockRelease) }) }
	t.Cleanup(func() {
		releaseBlocker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown persistence queue: %v", err)
		}
		if err := manager.Close(); err != nil {
			t.Errorf("close indexed Task store: %v", err)
		}
	})
	task, run, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "IdleChat queue-backed stop", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create conversation Task/Run: %v", err)
	}
	if _, err := queue.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerTask, Kind: "test.block", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(blockStarted)
			<-blockRelease
			return nil
		},
	}, 1); err != nil {
		t.Fatalf("enqueue Task persistence blocker: %v", err)
	}
	select {
	case <-blockStarted:
	case <-time.After(time.Second):
		t.Fatal("Task persistence worker did not reach the blocker")
	}

	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	o.SetRunIssuer(manager)
	o.mu.Lock()
	o.chatActive = true
	o.activeGeneration = 4
	o.activeTaskID = task.TaskID
	o.activeRunID = run.RunID
	o.runCtx, o.runCancel = context.WithCancel(context.Background())
	o.mu.Unlock()
	stopDone := make(chan error, 1)
	go func() {
		_, err := o.StopPlaybackForForeground(context.Background(), PlaybackStopRequest{
			RuntimeEpoch: "runtime-indexed", StateRevision: 5, Reason: "foreground_cmd",
		})
		stopDone <- err
	}()
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopPlaybackForForeground: %v", err)
		}
	case <-time.After(time.Second):
		releaseBlocker()
		t.Fatal("foreground stop waited for blocked Task persistence")
	}

	// Wait on the observable queue condition rather than sleeping. The finalizer
	// must enqueue its Task completion behind the controlled blocker and return
	// from admission while the ticket remains non-durable.
	deadline := time.Now().Add(time.Second)
	for queue.Status().Pending < 2 {
		if time.Now().After(deadline) {
			releaseBlocker()
			t.Fatalf("Task completion was not admitted; queue status=%+v", queue.Status())
		}
		runtime.Gosched()
	}
	done := o.conversationRunFinalizationWorkerDone()
	if done == nil {
		releaseBlocker()
		t.Fatal("managed completion worker is not tracking the admitted ticket")
	}
	select {
	case <-done:
		releaseBlocker()
		t.Fatal("finalizer completed while its Task persistence ticket was blocked")
	default:
	}
	releaseBlocker()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("indexed Task finalization did not finish after the queue drained")
	}
	assertConversationPair(t, manager, task.TaskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

func TestConversationRunLifecyclePersistsExplicitSuccess(t *testing.T) {
	o, owner := newConversationLifecycleFixture(t)
	setConversationChatActive(o)
	generation, err := o.startIdleRun()
	if err != nil {
		t.Fatalf("startIdleRun: %v", err)
	}
	taskID, runID := activeConversationIDs(o)
	if taskID == "" || runID == "" {
		t.Fatalf("owner pair was not installed: task=%q run=%q", taskID, runID)
	}
	o.markConversationRunSucceeded(generation, "verified playback boundary")
	if err := o.cancelIdleRunIfGeneration(generation); err != nil {
		t.Fatalf("finalize successful conversation: %v", err)
	}
	assertConversationPair(t, owner, taskID, domaintask.StatusSucceeded, domaintask.RunStatusSucceeded)
}

func TestConversationRunLifecycleCancellationReleasesThreeSequentialSlots(t *testing.T) {
	o, owner := newConversationLifecycleFixture(t)
	setConversationChatActive(o)
	for i := 0; i < 3; i++ {
		generation, err := o.startIdleRun()
		if err != nil {
			t.Fatalf("startIdleRun[%d]: %v", i, err)
		}
		taskID, runID := activeConversationIDs(o)
		if taskID == "" || runID == "" {
			t.Fatalf("owner pair[%d] was not installed: task=%q run=%q", i, taskID, runID)
		}
		if err := o.cancelIdleRunIfGeneration(generation); err != nil {
			t.Fatalf("cancel conversation[%d]: %v", i, err)
		}
		assertConversationPair(t, owner, taskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	}
	if tasks, err := owner.List(context.Background(), domaintask.Filter{Status: domaintask.StatusRunning}); err != nil {
		t.Fatalf("list running conversation tasks: %v", err)
	} else if len(tasks) != 0 {
		t.Fatalf("running conversation tasks=%d after sequential cancellation, want zero", len(tasks))
	}
}

func TestConversationRunLifecycleManualPreparationHandsOffOnePair(t *testing.T) {
	o, owner := newConversationLifecycleFixture(t)
	if err := o.StartStoryMode(); err != nil {
		t.Fatalf("StartStoryMode: %v", err)
	}
	o.mu.Lock()
	generation := o.activeGeneration
	o.mu.Unlock()
	if generation == 0 {
		t.Fatal("StartStoryMode did not reserve a generation")
	}
	handoffGeneration, err := o.activateIdleSession(canonicalIdleChatTestSessionID("manual-handoff"))
	if err != nil {
		t.Fatalf("activate manual story session: %v", err)
	}
	if handoffGeneration != generation {
		t.Fatalf("handoff generation=%d, want prepared generation %d", handoffGeneration, generation)
	}
	taskID, runID := activeConversationIDs(o)
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil {
		t.Fatalf("list handoff runs: %v", err)
	}
	if len(runs) != 1 || runs[0].TaskID != taskID || runs[0].RunID != runID {
		t.Fatalf("manual handoff owner pairs=%+v, want one pair task=%s run=%s", runs, taskID, runID)
	}
	o.markConversationRunSucceeded(generation, "manual story playback boundary")
	if err := o.cancelIdleRunIfGeneration(generation); err != nil {
		t.Fatalf("finalize manual handoff: %v", err)
	}
	assertConversationPair(t, owner, taskID, domaintask.StatusSucceeded, domaintask.RunStatusSucceeded)
}

func TestConversationRunLifecycleNoReadyManualStoryIsCancelled(t *testing.T) {
	o, owner := newConversationLifecycleFixture(t)
	if err := o.StartStoryMode(); err != nil {
		t.Fatalf("StartStoryMode: %v", err)
	}
	taskID, _ := activeConversationIDs(o)
	o.RunPreparedStorySession()
	assertConversationPair(t, owner, taskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

func TestConversationRunBindFailureMarksOwnerPairFailed(t *testing.T) {
	o, owner := newConversationLifecycleFixture(t)
	setConversationChatActive(o)
	if _, err := o.startIdleRun(); err != nil {
		t.Fatalf("startIdleRun: %v", err)
	}
	taskID, _ := activeConversationIDs(o)
	if _, err := o.activateIdleSession("invalid-session"); err == nil {
		t.Fatal("invalid session bind unexpectedly succeeded")
	}
	assertConversationPair(t, owner, taskID, domaintask.StatusFailed, domaintask.RunStatusFailed)
}

type failingConversationAdmissionOwner struct {
	*taskmanager.Manager
	startErr    error
	cancelCalls atomic.Int32
}

func (o *failingConversationAdmissionOwner) CreateAndStartRun(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error) {
	return domaintask.Task{}, domaintask.Run{}, o.startErr
}

func (o *failingConversationAdmissionOwner) StartRunWithReason(context.Context, modulecore.TaskID, domaintask.RunStartReason) (domaintask.Run, error) {
	return domaintask.Run{}, o.startErr
}

func (o *failingConversationAdmissionOwner) Cancel(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	o.cancelCalls.Add(1)
	return o.Manager.Cancel(ctx, taskID, summary)
}

func TestIssueIdleChatRunAdmissionFailureLeavesNoNewTaskAndKeepsExistingTask(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	owner := &failingConversationAdmissionOwner{Manager: manager, startErr: errors.New("parallel limit reached")}
	_, _, err := issueIdleChatRun(context.Background(), owner, "IdleChat test", "shiro", domaintask.RunStartReasonFirst, "")
	if err == nil || !strings.Contains(err.Error(), "parallel limit reached") {
		t.Fatalf("issueIdleChatRun error=%v, want original admission error", err)
	}
	// Admission is one transaction, so a refused new task needs no compensation.
	if owner.cancelCalls.Load() != 0 {
		t.Fatalf("new task cancellation calls=%d, want none", owner.cancelCalls.Load())
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("a refused admission left Tasks behind: %d err=%v", len(tasks), err)
	}

	existing, err := owner.Create(context.Background(), domaintask.Task{Title: "existing IdleChat task", Route: domaintask.RouteGeneral, Assignee: "Shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = issueIdleChatRun(context.Background(), owner, "existing IdleChat task", "shiro", domaintask.RunStartReasonFirst, existing.TaskID)
	if err == nil {
		t.Fatal("existing task admission unexpectedly succeeded")
	}
	if owner.cancelCalls.Load() != 0 {
		t.Fatalf("existing task was cancelled after admission failure: calls=%d", owner.cancelCalls.Load())
	}
	existingAfter, err := owner.Get(context.Background(), existing.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if existingAfter.Status != domaintask.StatusQueued {
		t.Fatalf("existing task status=%s after failed resume, want queued", existingAfter.Status)
	}
}

func TestIssueIdleChatRunRefusedByExecutionCapacityPersistsNothing(t *testing.T) {
	saturated := taskmanagertest.NewSaturated(t)

	_, _, err := issueIdleChatRun(context.Background(), saturated.Manager, "IdleChat test", "shiro", domaintask.RunStartReasonFirst, "")
	if !errors.Is(err, taskmanager.ErrParallelLimit) {
		t.Fatalf("issueIdleChatRun error=%v, want ErrParallelLimit", err)
	}
	saturated.AssertNothingPersisted(t)
}

type noCancelConversationOwner struct {
	createCalls atomic.Int32
}

func (o *noCancelConversationOwner) CreateAndStartRun(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error) {
	o.createCalls.Add(1)
	return domaintask.Task{TaskID: modulecore.NewTaskID()}, domaintask.Run{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID()}, nil
}

func (*noCancelConversationOwner) StartRunWithReason(context.Context, modulecore.TaskID, domaintask.RunStartReason) (domaintask.Run, error) {
	return domaintask.Run{TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID()}, nil
}

func TestIssueIdleChatRunRequiresCancellationBeforeCreate(t *testing.T) {
	owner := &noCancelConversationOwner{}
	if _, _, err := issueIdleChatRun(context.Background(), owner, "IdleChat test", "shiro", domaintask.RunStartReasonFirst, ""); err == nil || !strings.Contains(err.Error(), "cannot cancel") {
		t.Fatalf("issueIdleChatRun error=%v, want fail-closed cancellation contract", err)
	}
	if owner.createCalls.Load() != 0 {
		t.Fatalf("Create calls=%d, want zero before cancellation contract check", owner.createCalls.Load())
	}
}

type flakyConversationCompletionOwner struct {
	*taskmanager.Manager
	failCompletions int
	completeCalls   atomic.Int32
}

func (o *flakyConversationCompletionOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	o.completeCalls.Add(1)
	if o.failCompletions > 0 {
		o.failCompletions--
		return domaintask.Task{}, errors.New("injected conversation completion failure")
	}
	return o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
}

func TestConversationRunCompletionFailureRetainsPairAndBlocksThenRetries(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	owner := &flakyConversationCompletionOwner{Manager: manager, failCompletions: 2}
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	o.SetRunIssuer(owner)
	setConversationChatActive(o)
	generation, err := o.startIdleRun()
	if err != nil {
		t.Fatalf("startIdleRun: %v", err)
	}
	taskID, runID := activeConversationIDs(o)
	if err := o.cancelIdleRunIfGeneration(generation); err == nil || !strings.Contains(err.Error(), "injected conversation completion failure") {
		t.Fatalf("first finalization error=%v, want injected write failure", err)
	}
	o.mu.Lock()
	pending := *o.pendingConversationRun
	activeTask, activeRun := o.activeTaskID, o.activeRunID
	o.mu.Unlock()
	if pending.taskID != taskID || pending.runID != runID || activeTask != taskID || activeRun != runID {
		t.Fatalf("failed finalization lost exact pair: pending=%+v active=%s/%s", pending, activeTask, activeRun)
	}
	if _, err := o.startIdleRun(); err == nil || !errors.Is(err, errIdleChatConversationRunPending) {
		t.Fatalf("start during failed finalization error=%v, want pending", err)
	}
	if got := owner.completeCalls.Load(); got != 2 {
		t.Fatalf("completion attempts=%d, want failed cleanup plus one explicit retry", got)
	}
	if got, err := owner.ListRuns(context.Background(), domaintask.RunFilter{}); err != nil || len(got) != 1 {
		t.Fatalf("runs after blocked retry=%d err=%v, want one original run", len(got), err)
	}
	secondGeneration, err := o.startIdleRun()
	if err != nil {
		t.Fatalf("successful explicit retry and next start: %v", err)
	}
	if secondGeneration == generation {
		t.Fatalf("new generation=%d reused failed generation", secondGeneration)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil || len(runs) != 2 {
		t.Fatalf("runs after retry=%d err=%v, want original plus one new run", len(runs), err)
	}
	assertConversationPair(t, owner.Manager, taskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
	if err := o.cancelIdleRunIfGeneration(secondGeneration); err != nil {
		t.Fatalf("cancel new generation: %v", err)
	}
}

type ticketRetryConversationOwner struct {
	*taskmanager.Manager
	admitCalls       atomic.Int32
	waitCalls        atomic.Int32
	waitStarted      chan struct{}
	releaseFirstWait <-chan struct{}
	firstWaitOnce    sync.Once
}

func (*ticketRetryConversationOwner) RunCompletionAdmissionAvailable() bool { return true }

func (o *ticketRetryConversationOwner) AdmitRunCompletion(context.Context, modulecore.TaskID, modulecore.RunID, domaintask.Status, string, string) (uint64, error) {
	o.admitCalls.Add(1)
	return 73, nil
}

func (o *ticketRetryConversationOwner) WaitRunCompletion(ctx context.Context, _ uint64, taskID modulecore.TaskID, runID modulecore.RunID, status domaintask.Status) error {
	if o.waitCalls.Add(1) == 1 {
		if o.waitStarted != nil {
			o.firstWaitOnce.Do(func() { close(o.waitStarted) })
		}
		if o.releaseFirstWait != nil {
			select {
			case <-o.releaseFirstWait:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return errors.New("injected FIFO wait timeout after admission")
	}
	run, err := o.Manager.GetRun(ctx, runID)
	if err != nil {
		return err
	}
	if _, err := o.Manager.CompleteRun(ctx, taskID, runID, run.Assignee, status, "cancelled by foreground", "foreground"); err != nil {
		return err
	}
	return o.Manager.VerifyRunCompletion(ctx, taskID, runID, run.Assignee, status)
}

func newPendingConversationRun(t *testing.T, o *IdleChatOrchestrator, owner idlechatRunIssuer) (modulecore.TaskID, modulecore.RunID) {
	t.Helper()
	canonicalOwner, ok := owner.(interface {
		CreateAndStartRun(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error)
	})
	if !ok {
		t.Fatal("conversation owner cannot create Task/Run")
	}
	task, run, err := canonicalOwner.CreateAndStartRun(context.Background(), domaintask.Task{
		Title: "IdleChat ticket retry", Route: domaintask.RouteGeneral, Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create conversation Task/Run: %v", err)
	}
	o.SetRunIssuer(owner)
	o.mu.Lock()
	o.pendingConversationRun = &conversationRunFinalization{
		generation: 9, taskID: task.TaskID, runID: run.RunID,
		status: domaintask.StatusCancelled, summary: "cancelled by foreground", reason: "foreground",
	}
	o.mu.Unlock()
	return task.TaskID, run.RunID
}

func TestConversationRunRetainsCompletionTicketAfterWaitTimeout(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	waitStarted := make(chan struct{})
	releaseFirstWait := make(chan struct{})
	owner := &ticketRetryConversationOwner{
		Manager: manager, waitStarted: waitStarted, releaseFirstWait: releaseFirstWait,
	}
	taskID, _ := newPendingConversationRun(t, o, owner)
	firstDone := make(chan error, 1)
	go func() { firstDone <- o.finalizePendingConversationRun() }()
	select {
	case <-waitStarted:
	case <-time.After(time.Second):
		t.Fatal("completion finalizer did not reach its admitted FIFO ticket")
	}
	close(releaseFirstWait)
	select {
	case err := <-firstDone:
		if err == nil || !strings.Contains(err.Error(), "FIFO wait timeout") {
			t.Fatalf("first completion wait error=%v, want injected timeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("first completion finalizer did not return after ticket wait timeout")
	}
	if err := o.finalizePendingConversationRun(); err != nil {
		t.Fatalf("retry retained Task completion ticket: %v", err)
	}
	if got := owner.admitCalls.Load(); got != 1 {
		t.Fatalf("Task completion admissions=%d after timeout retry, want one immutable ticket", got)
	}
	if got := owner.waitCalls.Load(); got != 2 {
		t.Fatalf("ticket waits=%d, want original wait plus retry of same ticket", got)
	}
	assertConversationPair(t, manager, taskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

func TestManagedConversationFinalizerRetriesTransientTicketWait(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	owner := &ticketRetryConversationOwner{Manager: manager}
	taskID, _ := newPendingConversationRun(t, o, owner)
	o.schedulePendingConversationRunFinalization()
	done := o.conversationRunFinalizationWorkerDone()
	if done == nil {
		t.Fatal("managed finalizer worker was not registered")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("managed finalizer did not finish its bounded retry")
	}
	o.mu.Lock()
	pending := o.pendingConversationRun != nil
	o.mu.Unlock()
	if pending {
		t.Fatal("managed finalizer left the pair pending after a transient ticket wait error")
	}
	if got := owner.admitCalls.Load(); got != 1 {
		t.Fatalf("Task completion admissions=%d across managed retry, want one", got)
	}
	if got := owner.waitCalls.Load(); got != 2 {
		t.Fatalf("ticket waits=%d, want one retry of the same ticket", got)
	}
	assertConversationPair(t, manager, taskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

func TestManagedConversationFinalizerRetriesAndStopJoinsWorker(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	waitStarted := make(chan struct{})
	releaseFirstWait := make(chan struct{})
	owner := &ticketRetryConversationOwner{Manager: manager, waitStarted: waitStarted, releaseFirstWait: releaseFirstWait}
	taskID, _ := newPendingConversationRun(t, o, owner)
	o.schedulePendingConversationRunFinalization()
	done := o.conversationRunFinalizationWorkerDone()
	if done == nil {
		t.Fatal("managed finalizer worker was not registered")
	}
	select {
	case <-waitStarted:
	case <-time.After(time.Second):
		t.Fatal("managed finalizer did not reach its admitted FIFO ticket")
	}
	// A registered finalizer worker remains part of shutdown even after its
	// Task/Run state has become terminal; Stop must join its completion signal.
	o.conversationRunFinalizeWorkerMu.Lock()
	stopDone := make(chan struct{})
	go func() {
		o.Stop()
		close(stopDone)
	}()
	select {
	case <-o.ctx.Done():
	case <-time.After(time.Second):
		o.conversationRunFinalizeWorkerMu.Unlock()
		t.Fatal("Stop did not enter shutdown")
	}
	close(releaseFirstWait)
	deadline := time.Now().Add(3 * time.Second)
	for {
		o.mu.Lock()
		pending := o.pendingConversationRun != nil
		o.mu.Unlock()
		if !pending {
			break
		}
		if time.Now().After(deadline) {
			o.conversationRunFinalizeWorkerMu.Unlock()
			t.Fatal("managed retry did not complete the Task/Run pair")
		}
		runtime.Gosched()
	}
	select {
	case <-stopDone:
		o.conversationRunFinalizeWorkerMu.Unlock()
		t.Fatal("Stop returned while the managed finalizer completion signal was held")
	case <-time.After(100 * time.Millisecond):
	}
	o.conversationRunFinalizeWorkerMu.Unlock()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("managed finalizer did not finish after the owner became available")
	}
	select {
	case <-stopDone:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not join the managed finalizer worker")
	}
	if got := owner.admitCalls.Load(); got != 1 {
		t.Fatalf("Task completion admissions=%d across retry, want one", got)
	}
	if got := owner.waitCalls.Load(); got < 2 {
		t.Fatalf("ticket waits=%d, want bounded retry after transient failure", got)
	}
	assertConversationPair(t, manager, taskID, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

type probingConversationOwner struct {
	*taskmanager.Manager
	probe func()
}

func (o *probingConversationOwner) CreateAndStartRun(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error) {
	if o.probe != nil {
		o.probe()
	}
	return o.Manager.CreateAndStartRun(ctx, draft, shared)
}

func (o *probingConversationOwner) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	if o.probe != nil {
		o.probe()
	}
	return o.Manager.StartRunWithReason(ctx, taskID, reason)
}

func TestConversationRunOwnerIORunsOutsideOrchestratorLocks(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	// A new conversation task is admitted by one owner call (create and first
	// run in one transaction).
	probes := make(chan struct{}, 1)
	owner := &probingConversationOwner{Manager: manager, probe: func() {
		_ = o.IsChatActive()
		probes <- struct{}{}
	}}
	o.SetRunIssuer(owner)
	o.mu.Lock()
	o.chatActive = true
	o.mu.Unlock()
	done := make(chan error, 1)
	go func() {
		_, err := o.startIdleRun()
		done <- err
	}()
	for i := 0; i < 1; i++ {
		select {
		case <-probes:
		case <-time.After(time.Second):
			t.Fatalf("owner callback %d blocked behind orchestrator lock", i+1)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("startIdleRun: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("owner admission did not complete")
	}
	if err := o.cancelIdleRun(); err != nil {
		t.Fatalf("cleanup probed conversation: %v", err)
	}
}

type blockingConversationAdmissionOwner struct {
	*taskmanager.Manager
	started    chan struct{}
	release    chan struct{}
	startCalls atomic.Int32
}

func (o *blockingConversationAdmissionOwner) CreateAndStartRun(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, domaintask.Run, error) {
	task, run, err := o.Manager.CreateAndStartRun(ctx, draft, shared)
	if o.startCalls.Add(1) == 1 {
		close(o.started)
		<-o.release
	}
	return task, run, err
}

func TestConversationRunAdmissionRaceFinalizesInvalidatedPairBeforeNextStart(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	owner := &blockingConversationAdmissionOwner{Manager: manager, started: make(chan struct{}), release: make(chan struct{})}
	o.SetRunIssuer(owner)
	setConversationChatActive(o)
	firstDone := make(chan struct{})
	var firstGeneration uint64
	var firstErr error
	go func() {
		firstGeneration, firstErr = o.startIdleRun()
		close(firstDone)
	}()
	select {
	case <-owner.started:
	case <-time.After(time.Second):
		t.Fatal("owner admission did not reach the race window")
	}
	o.Interrupt("admission-race")
	setConversationChatActive(o)
	secondDone := make(chan struct{})
	var secondGeneration uint64
	var secondErr error
	go func() {
		secondGeneration, secondErr = o.startIdleRun()
		close(secondDone)
	}()
	close(owner.release)
	select {
	case <-firstDone:
	case <-time.After(2 * time.Second):
		t.Fatal("invalidated admission did not finalize")
	}
	if !errors.Is(firstErr, errIdleChatConversationRunInvalidated) {
		t.Fatalf("first admission error=%v, want invalidated admission", firstErr)
	}
	select {
	case <-secondDone:
	case <-time.After(2 * time.Second):
		t.Fatal("next admission did not run after prior finalization")
	}
	if secondErr != nil || secondGeneration == 0 || secondGeneration == firstGeneration {
		t.Fatalf("second admission generation=%d err=%v, want new successful generation", secondGeneration, secondErr)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("owner runs=%d, want exactly two serialized admissions", len(runs))
	}
	var cancelled, running int
	var activeTask modulecore.TaskID
	for _, run := range runs {
		switch run.Status {
		case domaintask.RunStatusCancelled:
			cancelled++
		case domaintask.RunStatusRunning:
			running++
			activeTask = run.TaskID
		}
	}
	if cancelled != 1 || running != 1 {
		t.Fatalf("serialized admission run statuses: cancelled=%d running=%d", cancelled, running)
	}
	if err := o.cancelIdleRunIfGeneration(secondGeneration); err != nil {
		t.Fatalf("cleanup second admission: %v", err)
	}
	assertConversationPair(t, owner.Manager, activeTask, domaintask.StatusCancelled, domaintask.RunStatusCancelled)
}

func TestConversationRunStopWaitsForAdmissionAndRejectsLaterStart(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	owner := &blockingConversationAdmissionOwner{Manager: manager, started: make(chan struct{}), release: make(chan struct{})}
	o.SetRunIssuer(owner)
	setConversationChatActive(o)
	startDone := make(chan struct{})
	go func() {
		_, _ = o.startIdleRun()
		close(startDone)
	}()
	select {
	case <-owner.started:
	case <-time.After(time.Second):
		t.Fatal("owner admission did not reach stop window")
	}
	stopDone := make(chan struct{})
	go func() {
		o.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned before in-flight admission was reconciled")
	case <-time.After(50 * time.Millisecond):
	}
	close(owner.release)
	select {
	case <-startDone:
	case <-time.After(2 * time.Second):
		t.Fatal("in-flight admission did not finish after release")
	}
	select {
	case <-stopDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not wait for admission finalization")
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusCancelled {
		t.Fatalf("stopped admission runs=%+v err=%v, want one cancelled run", runs, err)
	}
	if _, err := o.startIdleRun(); err == nil {
		t.Fatal("startIdleRun succeeded after Stop cancelled the orchestrator")
	}
}

func TestConversationRunStaleGenerationCannotFinalizeNewerPendingPair(t *testing.T) {
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"mio", "shiro"}, 5, 2, 0.7, nil, "")
	newTaskID, newRunID := testIdleChatRunIdentityPair()
	o.emitMu.Lock()
	o.mu.Lock()
	o.activeGeneration = 2
	o.pendingConversationRun = &conversationRunFinalization{generation: 2, taskID: newTaskID, runID: newRunID, status: domaintask.StatusCancelled}
	_, shouldFinalize, err := o.prepareConversationRunStopLocked(1, domaintask.StatusCancelled, "stale", "stale")
	pending := *o.pendingConversationRun
	o.mu.Unlock()
	o.emitMu.Unlock()
	if err != nil {
		t.Fatalf("stale cleanup preparation: %v", err)
	}
	if shouldFinalize {
		t.Fatal("stale generation was allowed to finalize newer pending pair")
	}
	if pending.generation != 2 || pending.taskID != newTaskID || pending.runID != newRunID {
		t.Fatalf("newer pending pair changed by stale cleanup: %+v", pending)
	}
}
