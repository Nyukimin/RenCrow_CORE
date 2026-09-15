package idlechat

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
	createdTask modulecore.TaskID
}

func (o *failingConversationAdmissionOwner) Create(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, error) {
	task, err := o.Manager.Create(ctx, draft, shared)
	if err == nil {
		o.createdTask = task.TaskID
	}
	return task, err
}

func (o *failingConversationAdmissionOwner) StartRunWithReason(context.Context, modulecore.TaskID, domaintask.RunStartReason) (domaintask.Run, error) {
	return domaintask.Run{}, o.startErr
}

func (o *failingConversationAdmissionOwner) Cancel(ctx context.Context, taskID modulecore.TaskID, summary string) (domaintask.Task, error) {
	o.cancelCalls.Add(1)
	return o.Manager.Cancel(ctx, taskID, summary)
}

func TestIssueIdleChatRunCancelsOnlyNewTaskAfterAdmissionFailure(t *testing.T) {
	manager := newTestIdleChatRunIssuer(t)
	owner := &failingConversationAdmissionOwner{Manager: manager, startErr: errors.New("parallel limit reached")}
	_, _, err := issueIdleChatRun(context.Background(), owner, "IdleChat test", "shiro", domaintask.RunStartReasonFirst, "")
	if err == nil || !strings.Contains(err.Error(), "parallel limit reached") {
		t.Fatalf("issueIdleChatRun error=%v, want original admission error", err)
	}
	if owner.cancelCalls.Load() != 1 {
		t.Fatalf("new task cancellation calls=%d, want one", owner.cancelCalls.Load())
	}
	task, err := owner.Get(context.Background(), owner.createdTask)
	if err != nil {
		t.Fatalf("get cancelled admission task: %v", err)
	}
	if task.Status != domaintask.StatusCancelled {
		t.Fatalf("new task status=%s, want cancelled", task.Status)
	}

	existing, err := owner.Create(context.Background(), domaintask.Task{Title: "existing IdleChat task", Route: domaintask.RouteGeneral, Assignee: "Shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = issueIdleChatRun(context.Background(), owner, "existing IdleChat task", "shiro", domaintask.RunStartReasonFirst, existing.TaskID)
	if err == nil {
		t.Fatal("existing task admission unexpectedly succeeded")
	}
	if owner.cancelCalls.Load() != 1 {
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

type noCancelConversationOwner struct {
	createCalls atomic.Int32
}

func (o *noCancelConversationOwner) Create(context.Context, domaintask.Task, domaintask.SharedRoleContext) (domaintask.Task, error) {
	o.createCalls.Add(1)
	return domaintask.Task{TaskID: modulecore.NewTaskID()}, nil
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

type probingConversationOwner struct {
	*taskmanager.Manager
	probe func()
}

func (o *probingConversationOwner) Create(ctx context.Context, draft domaintask.Task, shared domaintask.SharedRoleContext) (domaintask.Task, error) {
	if o.probe != nil {
		o.probe()
	}
	return o.Manager.Create(ctx, draft, shared)
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
	probes := make(chan struct{}, 2)
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
	for i := 0; i < 2; i++ {
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

func (o *blockingConversationAdmissionOwner) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	run, err := o.Manager.StartRunWithReason(ctx, taskID, reason)
	if o.startCalls.Add(1) == 1 {
		close(o.started)
		<-o.release
	}
	return run, err
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
