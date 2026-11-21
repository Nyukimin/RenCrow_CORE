package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newPrivateTextFixture(t *testing.T) (*Manager, *taskpersistence.JSONLStore, domaintask.Task) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, err := manager.Create(context.Background(), domaintask.Task{
		Title:    "Draft a private note",
		Assignee: "mio",
		Route:    domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{
		CurrentPlan: "keep this context",
		PrivateText: &domaintask.PrivateTextCheckpoint{Output: "untrusted create input"},
	})
	if err != nil {
		t.Fatalf("create private-text Task: %v", err)
	}
	return manager, store, task
}

func TestTerminalForegroundRestoresActivePrivateRootWithoutChangingOtherActor(t *testing.T) {
	ctx := context.Background()
	manager, _, privateTask := newPrivateTextFixture(t)
	privateRun, err := manager.StartPrivateTextRun(ctx, privateTask.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SavePrivateTextOutput(ctx, privateTask.TaskID, privateRun.RunID, "mio", "private output"); err != nil {
		t.Fatal(err)
	}
	otherActorTask, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "Shiro work", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	foreground, foregroundRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "Mio foreground chat", Route: domaintask.RouteCHAT, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, foreground.TaskID, foregroundRun.RunID, "mio", domaintask.StatusSucceeded, "foreground completed", ""); err != nil {
		t.Fatal(err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"mio", "shiro"})
	if err != nil || current["mio"].Task.TaskID != privateTask.TaskID || current["mio"].Task.Activity == nil ||
		current["mio"].Task.Activity.Kind != domaintask.ActivityKindWork || current["shiro"].Task.TaskID != otherActorTask.TaskID {
		t.Fatalf("current activities after foreground completion=%#v err=%v, want private Work restored for Mio and Shiro unchanged", current, err)
	}
	if err := manager.CompletePrivateTextRun(ctx, privateTask.TaskID, privateRun.RunID, "mio", false); err != nil {
		t.Fatal(err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"mio", "shiro"})
	if err != nil || current["mio"].Task.Activity == nil || current["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating ||
		current["shiro"].Task.TaskID != otherActorTask.TaskID {
		t.Fatalf("current activities after private completion=%#v err=%v, want Mio Evaluating and Shiro unchanged", current, err)
	}
}

func TestPrivateTextRunPersistsPrivateCursorAndGenericContextCannotWriteIt(t *testing.T) {
	ctx := context.Background()
	manager, _, task := newPrivateTextFixture(t)
	queueID := modulecore.NewQueueItemID()
	if _, err := manager.StartPrivateTextRun(ctx, task.TaskID, queueID, "shiro", "", ""); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("first Run with wrong actor error = %v, want ErrRunConflict", err)
	}

	run, err := manager.StartPrivateTextRun(ctx, task.TaskID, queueID, "mio", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun(first): %v", err)
	}
	checkpoint, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok {
		t.Fatalf("GetPrivateTextCheckpoint() = (%#v, %v, %v), want checkpoint", checkpoint, ok, err)
	}
	if checkpoint.TaskID != task.TaskID || checkpoint.RunID != run.RunID || checkpoint.QueueItemID != queueID || checkpoint.ActorID != "mio" || checkpoint.Output != "" {
		t.Fatalf("first-run checkpoint = %#v", checkpoint)
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, run.RunID, "mio", "stored before generic update"); err != nil {
		t.Fatalf("SavePrivateTextOutput: %v", err)
	}

	privateText := "untrusted attempt to set checkpoint"
	if err := manager.UpdateContext(ctx, domaintask.SharedRoleContext{
		TaskID:      task.TaskID,
		CurrentPlan: "updated public context",
		PrivateText: &domaintask.PrivateTextCheckpoint{Output: privateText},
	}); err != nil {
		t.Fatalf("UpdateContext: %v", err)
	}
	shared, err := manager.Context(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	if shared.PrivateText != nil {
		t.Fatalf("generic Context exposed private checkpoint: %#v", shared.PrivateText)
	}
	checkpoint, ok, err = manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok || checkpoint.Output != "stored before generic update" {
		t.Fatalf("generic UpdateContext changed private checkpoint: (%#v, %v, %v)", checkpoint, ok, err)
	}
	if shared.CurrentPlan != "updated public context" {
		t.Fatalf("generic context update was not preserved: %#v", shared)
	}
}

func TestListPrivateTextRecoveryTasksRequiresExplicitStatusAndPositiveBound(t *testing.T) {
	ctx := context.Background()
	manager, _, task := newPrivateTextFixture(t)
	queueID := modulecore.NewQueueItemID()
	run, err := manager.StartPrivateTextRun(ctx, task.TaskID, queueID, "mio", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun: %v", err)
	}
	running, err := manager.ListPrivateTextRecoveryTasks(ctx, domaintask.StatusRunning, 1)
	if err != nil || len(running) != 1 || running[0].TaskID != task.TaskID {
		t.Fatalf("running recovery Tasks=%+v err=%v, want exact active Task", running, err)
	}
	if _, err := manager.ListPrivateTextRecoveryTasks(ctx, domaintask.StatusWaiting, 0); err == nil {
		t.Fatal("zero recovery limit was accepted")
	}
	if _, err := manager.ListPrivateTextRecoveryTasks(ctx, domaintask.StatusRunning, privateTextRecoveryTaskListMaxLimit+1); err == nil {
		t.Fatal("recovery limit above the owner bound was accepted")
	}
	if _, err := manager.ListPrivateTextRecoveryTasks(ctx, domaintask.StatusSucceeded, 1); err == nil {
		t.Fatal("non-active recovery status was accepted")
	}
	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, run.RunID, "mio", true); err != nil {
		t.Fatalf("pause private Run: %v", err)
	}
	waiting, err := manager.ListPrivateTextRecoveryTasks(ctx, domaintask.StatusWaiting, 1)
	if err != nil || len(waiting) != 1 || waiting[0].TaskID != task.TaskID || waiting[0].WaitingReason != domaintask.PrivateTextWaitingReason {
		t.Fatalf("waiting recovery Tasks=%+v err=%v, want exact private waiting Task", waiting, err)
	}
}

func TestPrivateTextOutputIsBoundedImmutableAndCanResumeAfterSave(t *testing.T) {
	ctx := context.Background()
	manager, _, task := newPrivateTextFixture(t)
	firstQueue := modulecore.NewQueueItemID()
	firstRun, err := manager.StartPrivateTextRun(ctx, task.TaskID, firstQueue, "mio", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun(first): %v", err)
	}

	secret := "private output that must stay in the Task checkpoint"
	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, firstRun.RunID, "mio", false); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("success without saved output error = %v, want ErrRunConflict", err)
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", " \n"); err == nil {
		t.Fatal("whitespace-only output was accepted")
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "wrong-actor", secret); err == nil {
		t.Fatal("output from a different actor was accepted")
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", secret); err != nil {
		t.Fatalf("SavePrivateTextOutput: %v", err)
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", secret); err != nil {
		t.Fatalf("idempotent SavePrivateTextOutput: %v", err)
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", "different output"); err == nil {
		t.Fatal("checkpoint output was overwritten")
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("error exposed private output: %v", err)
	}

	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, firstRun.RunID, "mio", false); err != nil {
		t.Fatalf("CompletePrivateTextRun(success): %v", err)
	}
	checkpoint, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok || checkpoint.Output != secret {
		t.Fatalf("completed checkpoint = (%#v, %v, %v)", checkpoint, ok, err)
	}
	taskAfter, err := manager.Get(ctx, task.TaskID)
	if err != nil || taskAfter.Status != domaintask.StatusSucceeded || taskAfter.Summary != secret {
		t.Fatalf("successful Task = %#v, err=%v", taskAfter, err)
	}
	runAfter, err := manager.GetRun(ctx, firstRun.RunID)
	if err != nil || runAfter.Status != domaintask.RunStatusSucceeded || runAfter.Summary != secret {
		t.Fatalf("successful Run = %#v, err=%v", runAfter, err)
	}
}

func TestPrivateTextPauseResumeReopensTaskAndPreservesOutput(t *testing.T) {
	ctx := context.Background()
	manager, _, task := newPrivateTextFixture(t)
	firstQueue := modulecore.NewQueueItemID()
	firstRun, err := manager.StartPrivateTextRun(ctx, task.TaskID, firstQueue, "mio", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun(first): %v", err)
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", "saved before pause"); err != nil {
		t.Fatalf("SavePrivateTextOutput(before pause): %v", err)
	}
	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, firstRun.RunID, "mio", true); err != nil {
		t.Fatalf("CompletePrivateTextRun(pause): %v", err)
	}
	waitingTask, err := manager.Get(ctx, task.TaskID)
	if err != nil || waitingTask.Status != domaintask.StatusWaiting || waitingTask.WaitingReason != domaintask.PrivateTextWaitingReason {
		t.Fatalf("paused Task = %#v, err=%v", waitingTask, err)
	}
	waitingRun, err := manager.GetRun(ctx, firstRun.RunID)
	if err != nil || waitingRun.Status != domaintask.RunStatusWaiting {
		t.Fatalf("paused Run = %#v, err=%v", waitingRun, err)
	}
	if _, err := manager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", firstRun.RunID, strings.Repeat("0", 64)); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("tampered digest error = %v, want ErrRunConflict", err)
	}

	checkpoint, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok || checkpoint.Output != "saved before pause" {
		t.Fatalf("saved checkpoint after pause = (%#v, %v, %v)", checkpoint, ok, err)
	}
	digest := checkpoint.Digest()
	secondQueue := modulecore.NewQueueItemID()
	secondRun, err := manager.StartPrivateTextRun(ctx, task.TaskID, secondQueue, "mio", firstRun.RunID, digest)
	if err != nil {
		t.Fatalf("StartPrivateTextRun(successor): %v", err)
	}
	if secondRun.StartReason != domaintask.RunStartReasonCheckpointResume || secondRun.StartCheckpointSHA256 != digest {
		t.Fatalf("successor did not retain exact checkpoint evidence: %#v", secondRun)
	}
	resumedTask, err := manager.Get(ctx, task.TaskID)
	if err != nil || resumedTask.Status != domaintask.StatusRunning || resumedTask.WaitingReason != "" {
		t.Fatalf("resumed Task = %#v, err=%v", resumedTask, err)
	}
	resumedCheckpoint, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok || resumedCheckpoint.RunID != secondRun.RunID || resumedCheckpoint.QueueItemID != secondQueue || resumedCheckpoint.Output != "saved before pause" {
		t.Fatalf("successor checkpoint = (%#v, %v, %v)", resumedCheckpoint, ok, err)
	}
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, secondRun.RunID, "mio", "different output after resume"); err == nil {
		t.Fatal("resumed checkpoint output was overwritten")
	}
	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, secondRun.RunID, "mio", false); err != nil {
		t.Fatalf("CompletePrivateTextRun(after resume): %v", err)
	}
}

func TestPrivateTextRunRejectsStaleActorTitleAndCheckpointClaimsWithoutWrites(t *testing.T) {
	ctx := context.Background()
	manager, store, task := newPrivateTextFixture(t)
	firstRun, err := manager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun(first): %v", err)
	}
	secret := "saved private text that must not be copied into errors"
	if err := manager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", secret); err != nil {
		t.Fatalf("SavePrivateTextOutput: %v", err)
	}
	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, firstRun.RunID, "mio", true); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok {
		t.Fatalf("GetPrivateTextCheckpoint: (%#v, %v, %v)", checkpoint, ok, err)
	}
	digest := checkpoint.Digest()

	if _, err := manager.RecordAssignment(ctx, task.TaskID, "shiro", modulecore.NewEventID()); err != nil {
		t.Fatalf("RecordAssignment: %v", err)
	}
	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "shiro", firstRun.RunID, digest); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("old actor resume error = %v, want ErrRunConflict", err)
	}
	afterTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
		t.Fatal("stale actor claim changed Task or Run history")
	}

	// A title change invalidates the cursor binding and must not disclose it.
	tamperedTask := afterTask
	tamperedTask.Title = "tampered title"
	tamperedTask.Assignee = "mio"
	if err := store.SaveTask(ctx, tamperedTask); err != nil {
		t.Fatalf("tamper Task title: %v", err)
	}
	if _, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID); err == nil || ok {
		t.Fatalf("checkpoint survived title change: ok=%v err=%v", ok, err)
	} else if strings.Contains(err.Error(), secret) {
		t.Fatalf("title mismatch error exposed private output: %v", err)
	}
}

func TestPrivateTextStartCheckpointCASAllowsOneSuccessor(t *testing.T) {
	ctx := context.Background()
	manager, _, task := newPrivateTextFixture(t)
	firstRun, err := manager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun(first): %v", err)
	}
	if err := manager.CompletePrivateTextRun(ctx, task.TaskID, firstRun.RunID, "mio", true); err != nil {
		t.Fatal(err)
	}
	checkpoint, ok, err := manager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !ok {
		t.Fatalf("GetPrivateTextCheckpoint: (%#v, %v, %v)", checkpoint, ok, err)
	}
	digest := checkpoint.Digest()

	type startResult struct {
		run domaintask.Run
		err error
	}
	results := make(chan startResult, 2)
	var starts sync.WaitGroup
	for range 2 {
		starts.Add(1)
		go func() {
			defer starts.Done()
			results <- func() startResult {
				run, err := manager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", firstRun.RunID, digest)
				return startResult{run: run, err: err}
			}()
		}()
	}
	starts.Wait()
	close(results)
	successes, conflicts := 0, 0
	for result := range results {
		if result.err == nil {
			successes++
		} else if errors.Is(result.err, ErrRunConflict) {
			conflicts++
		} else {
			t.Fatalf("unexpected successor error: %v", result.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successor CAS results = successes %d, conflicts %d, want 1 each", successes, conflicts)
	}
}

func TestPrivateTextCheckpointSurvivesTaskStoreReopen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	firstStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	firstManager := New(firstStore, DefaultParallelLimits())
	task, err := firstManager.Create(ctx, domaintask.Task{
		Title:    "Durable private text",
		Assignee: "mio",
		Route:    domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create private-text Task: %v", err)
	}
	firstRun, err := firstManager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatalf("start first private-text Run: %v", err)
	}
	output := "durable output survives Task store reopen"
	if err := firstManager.SavePrivateTextOutput(ctx, task.TaskID, firstRun.RunID, "mio", output); err != nil {
		t.Fatalf("save private-text output: %v", err)
	}
	if err := firstManager.CompletePrivateTextRun(ctx, task.TaskID, firstRun.RunID, "mio", true); err != nil {
		t.Fatalf("pause private-text Run: %v", err)
	}
	if err := firstStore.Close(); err != nil {
		t.Fatalf("close first Task store: %v", err)
	}

	secondStore, err := taskpersistence.NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	registerTaskStoreCleanup(t, secondStore)
	secondManager := New(secondStore, DefaultParallelLimits())
	checkpoint, found, err := secondManager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !found || checkpoint.Output != output || checkpoint.RunID != firstRun.RunID {
		t.Fatalf("checkpoint after Task store reopen = (%#v, %v, %v)", checkpoint, found, err)
	}
	resumed, err := secondManager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", firstRun.RunID, checkpoint.Digest())
	if err != nil {
		t.Fatalf("resume after Task store reopen: %v", err)
	}
	checkpoint, found, err = secondManager.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !found || checkpoint.RunID != resumed.RunID || checkpoint.Output != output {
		t.Fatalf("resumed checkpoint after Task store reopen = (%#v, %v, %v)", checkpoint, found, err)
	}
	if err := secondManager.CompletePrivateTextRun(ctx, task.TaskID, resumed.RunID, "mio", false); err != nil {
		t.Fatalf("complete after Task store reopen: %v", err)
	}
}

func TestPrivateTextFirstRunAndCheckpointResumeBindWorkAcrossChatInterruption(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	rest, err := manager.CreateCurrentActivity(ctx, domaintask.Task{Title: "rest", Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.ActivityMetadata{
		Kind: domaintask.ActivityKindRest, Label: "Resting", Phase: "resting", Passive: true,
		Participants: []domaintask.ActivityParticipant{{ActorID: "mio"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	privateTask, err := manager.Create(ctx, domaintask.Task{Title: "private writing", Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	firstRun, err := manager.StartPrivateTextRun(ctx, privateTask.TaskID, modulecore.NewQueueItemID(), "mio", "", "")
	if err != nil {
		t.Fatalf("first private-text Run: %v", err)
	}
	current, err := manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != privateTask.TaskID || current["mio"].Task.Activity.Kind != domaintask.ActivityKindWork {
		t.Fatalf("private first Run current=%#v err=%v", current, err)
	}
	closedRest, err := manager.Get(ctx, rest.TaskID)
	if err != nil || closedRest.Status != domaintask.StatusCancelled {
		t.Fatalf("interrupted rest Task=%#v err=%v", closedRest, err)
	}
	if err := manager.SavePrivateTextOutput(ctx, privateTask.TaskID, firstRun.RunID, "mio", "saved checkpoint"); err != nil {
		t.Fatal(err)
	}
	if err := manager.CompletePrivateTextRun(ctx, privateTask.TaskID, firstRun.RunID, "mio", true); err != nil {
		t.Fatal(err)
	}
	checkpoint, found, err := manager.GetPrivateTextCheckpoint(ctx, privateTask.TaskID)
	if err != nil || !found {
		t.Fatalf("private-text checkpoint=%#v found=%v err=%v", checkpoint, found, err)
	}
	chatTask, chatRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "chat interruption", Route: domaintask.RouteCHAT, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, chatTask.TaskID, chatRun.RunID, "mio", domaintask.StatusWaiting, "chat paused", "chat interruption"); err != nil {
		t.Fatal(err)
	}
	resumedRun, err := manager.StartPrivateTextRun(ctx, privateTask.TaskID, modulecore.NewQueueItemID(), "mio", firstRun.RunID, checkpoint.Digest())
	if err != nil {
		t.Fatalf("resume private-text checkpoint: %v", err)
	}
	current, err = manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || current["mio"].Task.TaskID != privateTask.TaskID || current["mio"].Task.Activity.Kind != domaintask.ActivityKindWork || current["mio"].Task.Activity.Label != "Working" || current["mio"].Task.Activity.Phase != "active" {
		t.Fatalf("private resumed Run current=%#v err=%v", current, err)
	}
	if resumedRun.TaskID != privateTask.TaskID || resumedRun.RunID == firstRun.RunID || resumedRun.StartReason != domaintask.RunStartReasonCheckpointResume {
		t.Fatalf("resumed Run=%#v", resumedRun)
	}
}

func TestPrivateTextActivityBindingFailureRollsBackRunAndCheckpoint(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	baseManager := New(store, DefaultParallelLimits())
	task, err := baseManager.Create(ctx, domaintask.Task{Title: "private writing", Route: domaintask.RouteGeneral, Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("activity metadata save failed")
	failingManager := New(&faultTaskStore{Store: store, failOperation: "activity", failErr: injected}, DefaultParallelLimits())
	if _, err := failingManager.StartPrivateTextRun(ctx, task.TaskID, modulecore.NewQueueItemID(), "mio", "", ""); !errors.Is(err, injected) {
		t.Fatalf("StartPrivateTextRun failure=%v, want metadata save error", err)
	}
	persisted, err := baseManager.Get(ctx, task.TaskID)
	if err != nil || persisted.Status != domaintask.StatusQueued || persisted.Activity != nil {
		t.Fatalf("Task after failed start=%#v err=%v", persisted, err)
	}
	runs, err := baseManager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs after failed start=%#v err=%v", runs, err)
	}
	if _, found, err := baseManager.GetPrivateTextCheckpoint(ctx, task.TaskID); err != nil || found {
		t.Fatalf("checkpoint after failed start found=%v err=%v", found, err)
	}
}
