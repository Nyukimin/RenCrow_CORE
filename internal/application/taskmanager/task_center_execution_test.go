package taskmanager

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskCenterExecutionEnsureEffectFinishAndReceiptRecovery(t *testing.T) {
	ctx := context.Background()
	storeRoot, err := os.MkdirTemp(os.TempDir(), "task-center-execution-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(storeRoot) })
	store, err := taskpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if store != nil {
			if err := store.Close(); err != nil {
				t.Errorf("close TaskCenter store: %v", err)
			}
		}
	})
	manager := New(store, DefaultParallelLimits())
	now := time.Date(2026, 11, 18, 4, 5, 6, 0, time.UTC)
	manager.now = func() time.Time { return now }
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, err := domaintask.TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	kindInput := json.RawMessage(`{"information":"","kind":"movie","limit":1,"name":"The Film"}`)
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		t.Fatal(err)
	}
	goalID, workstreamID := modulecore.NewGoalID(), modulecore.NewWorkstreamID()
	draft := domaintask.Task{
		Title: "movie catalog lookup", Route: domaintask.RouteGeneral, Assignee: "mio",
		Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked,
		ReadOnly: true, GoalID: goalID, WorkstreamID: workstreamID,
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "user-1"},
		Timing:        &timing,
		TaskCenterExecution: &domaintask.TaskCenterExecution{
			KindID: TaskCenterMovieCatalogLookupKind, KindInputVersion: 1, KindInputSHA256: inputDigest,
			SourceGoalID: goalID, PreparedRevision: 3, AssignmentEpoch: 0, TimingSHA256: timingDigest,
		},
	}
	shared := domaintask.SharedRoleContext{UserIntent: "find one exact movie"}
	key := modulecore.NewRequestID()
	admit := TaskCenterExecutionAdmissionCheck(func(context.Context, domaintask.Task, domaintask.SharedRoleContext, json.RawMessage) error {
		return nil
	})
	readReceipt := domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck(func(context.Context, domaintask.TaskCenterExecutionReceiptReadInput) error { return nil })
	task, run, err := manager.EnsureTaskCenterExecution(ctx, key, 3, draft, shared, kindInput, admit, readReceipt)
	if err != nil {
		t.Fatalf("EnsureTaskCenterExecution: %v", err)
	}
	if task.TaskID == "" || task.TaskID != run.TaskID || task.Status != domaintask.StatusRunning || run.Status != domaintask.RunStatusRunning {
		t.Fatalf("Ensure returned Task=%#v Run=%#v", task, run)
	}
	if task.Activity == nil || task.Activity.Kind != domaintask.ActivityKindWork || len(task.Activity.Participants) != 1 || !task.Activity.Participants[0].IsCurrent {
		t.Fatalf("TaskCenter current activity metadata=%#v", task.Activity)
	}
	denied := false
	if _, err := manager.SnapshotCurrentActivitiesAuthorized(ctx, []string{"mio"}, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
		denied = true
		if input.TaskID != task.TaskID || input.WorkstreamID != workstreamID || input.GoalID != goalID {
			t.Fatalf("current activity authorization input=%#v", input)
		}
		return domaintask.ErrPrivateTaskAccessForbidden
	}); !errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) || !denied {
		t.Fatalf("private current activity authorization err=%v callback=%v", err, denied)
	}
	genericTasks, _, err := manager.ReadTaskRuns(ctx, []modulecore.TaskID{task.TaskID})
	if err != nil || len(genericTasks) != 0 {
		t.Fatalf("generic private Task projection=%#v err=%v, want hidden", genericTasks, err)
	}
	if _, err := manager.GetRun(ctx, run.RunID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("generic private Run read error=%v, want not-found", err)
	}
	if _, err := manager.GetTaskAuthorized(ctx, task.TaskID, func(context.Context, domaintask.TaskReadAuthorizationInput) error { return nil }); !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) {
		t.Fatalf("generic authorized Task read error=%v, want typed owner required", err)
	}
	shiroTask, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "public shiro task", Route: domaintask.RouteGeneral, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create later public current Task: %v", err)
	}
	privateAuthorizations := 0
	goalVisible := true
	if rows, err := manager.SnapshotCurrentActivitiesAuthorized(ctx, []string{"mio", "shiro"}, func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
		if input.TaskID == task.TaskID {
			privateAuthorizations++
			if !goalVisible {
				return domaintask.ErrPrivateTaskAccessForbidden
			}
		} else {
			goalVisible = false
		}
		return nil
	}); !errors.Is(err, domaintask.ErrPrivateTaskAccessForbidden) || rows != nil || privateAuthorizations != 2 {
		t.Fatalf("revoked current Goal authorization rows=%#v err=%v checks=%d", rows, err, privateAuthorizations)
	}
	if err := manager.ValidateRunExecution(ctx, task.TaskID, run.RunID, "mio"); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("generic execution validation error=%v, want private Task not-found", err)
	}

	result, err := manager.ExecuteTaskCenterKind(ctx, task.TaskID, run.RunID, "mio",
		func(_ context.Context, gotTask domaintask.Task, gotRun domaintask.Run, gotShared domaintask.SharedRoleContext) (json.RawMessage, error) {
			if gotTask.TaskID != task.TaskID || gotRun.RunID != run.RunID {
				t.Fatalf("admission received Task/Run %s/%s", gotTask.TaskID, gotRun.RunID)
			}
			if gotShared.TaskID != task.TaskID || gotShared.UserIntent != shared.UserIntent {
				t.Fatalf("admission received unexpected SharedRoleContext: %#v", gotShared)
			}
			return append(json.RawMessage(nil), kindInput...), nil
		},
		func(_ context.Context, input json.RawMessage) (json.RawMessage, error) {
			if !bytes.Equal(input, kindInput) {
				t.Fatalf("kind input=%s, want frozen bytes %s", input, kindInput)
			}
			return json.RawMessage(`{"kind":"movie","not_found":true}`), nil
		},
	)
	if err != nil {
		t.Fatalf("ExecuteTaskCenterKind: %v", err)
	}
	foreground, foregroundRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "Mio foreground work", Route: domaintask.RouteCHAT, Assignee: "mio",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create Mio foreground root: %v", err)
	}
	if _, err := manager.CompleteRun(ctx, foreground.TaskID, foregroundRun.RunID, "mio", domaintask.StatusSucceeded, "foreground completed", ""); err != nil {
		t.Fatalf("complete Mio foreground root: %v", err)
	}
	currentActivities, err := manager.SnapshotCurrentActivities(ctx, []string{"mio", "shiro"})
	if err != nil || currentActivities["mio"].Task.TaskID != task.TaskID || currentActivities["mio"].Task.Activity == nil ||
		currentActivities["mio"].Task.Activity.Kind != domaintask.ActivityKindWork || currentActivities["mio"].Participant.Revision < 3 ||
		currentActivities["shiro"].Task.TaskID != shiroTask.TaskID {
		t.Fatalf("current activities after foreground completion=%#v err=%v, want TaskCenter root restored for Mio and Shiro unchanged", currentActivities, err)
	}
	resultDigestBytes := sha256.Sum256(result)
	resultArtifactID, err := domaintask.TaskCenterResultArtifactID(task.TaskID, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	resultReference := domaintask.TaskCenterResultReference{
		ArtifactID: resultArtifactID, SourceGoalID: goalID, WorkstreamID: workstreamID,
		TaskID: task.TaskID, RunID: run.RunID, KindID: TaskCenterMovieCatalogLookupKind,
		KindInputSHA256: inputDigest, ResultSHA256: hex.EncodeToString(resultDigestBytes[:]),
		DataScope: domaintask.TaskCenterResultScopeUser, UserID: "user-1",
	}
	originalGeneration := run.WriterGeneration
	artifactProofAvailable := false
	finishAuth := domaintask.TaskCenterExecutionFinishAuthorizationCheck(func(_ context.Context, gotTask domaintask.Task, gotRun domaintask.Run, gotShared domaintask.SharedRoleContext, gotReference domaintask.TaskCenterResultReference) error {
		if gotTask.TaskID != task.TaskID || gotRun.RunID != run.RunID || gotReference != resultReference {
			t.Fatalf("finish authorization binding Task=%s Run=%s reference=%#v", gotTask.TaskID, gotRun.RunID, gotReference)
		}
		if gotShared.TaskID != task.TaskID || gotShared.UserIntent != shared.UserIntent {
			t.Fatalf("finish authorization received unexpected SharedRoleContext: %#v", gotShared)
		}
		if !artifactProofAvailable {
			return domaintask.ErrTaskCenterExecutionReceiptForbidden
		}
		return nil
	})
	if err := store.Close(); err != nil {
		t.Fatalf("close after durable result: %v", err)
	}
	store = nil
	store, err = taskpersistence.NewJSONLStore(storeRoot)
	if err != nil {
		t.Fatalf("reopen after durable result: %v", err)
	}
	manager = New(store, DefaultParallelLimits())
	manager.now = func() time.Time { return now }
	newGeneration, err := store.WriterGeneration()
	if err != nil || newGeneration <= originalGeneration {
		t.Fatalf("reopened writer generation=%d, old=%d, error=%v", newGeneration, originalGeneration, err)
	}
	newEffects := 0
	if _, err := manager.ExecuteTaskCenterKind(ctx, task.TaskID, run.RunID, "mio",
		func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) (json.RawMessage, error) {
			return kindInput, nil
		},
		func(context.Context, json.RawMessage) (json.RawMessage, error) { newEffects++; return result, nil }); err == nil || newEffects != 0 {
		t.Fatalf("new effect after restart err=%v calls=%d; old Run must stay generation-fenced", err, newEffects)
	}
	if _, _, _, err := manager.FinishTaskCenterExecution(ctx, task.TaskID, run.RunID, draft, "mio", domaintask.StatusSucceeded, "lookup completed", resultReference, finishAuth); err == nil {
		t.Fatal("historical finish closed the old Run without exact persisted-result proof")
	}
	artifactProofAvailable = true
	finished, finishedRun, recoveredReference, err := manager.FinishTaskCenterExecution(ctx, task.TaskID, run.RunID, draft, "mio", domaintask.StatusSucceeded, "lookup completed", resultReference, finishAuth)
	if err != nil {
		t.Fatalf("FinishTaskCenterExecution: %v", err)
	}
	if finished.Status != domaintask.StatusSucceeded || finishedRun.Status != domaintask.RunStatusSucceeded || recoveredReference != resultReference {
		t.Fatalf("finished Task=%#v Run=%#v reference=%#v", finished, finishedRun, recoveredReference)
	}
	if finished.Activity == nil || finished.Activity.Participants[0].IsCurrent {
		t.Fatalf("finished Task retained its current activity binding: %#v", finished.Activity)
	}
	evaluation, err := manager.SnapshotCurrentActivities(ctx, []string{"mio"})
	if err != nil || evaluation["mio"].Task.TaskID == finished.TaskID || evaluation["mio"].Task.Activity.Kind != domaintask.ActivityKindEvaluating {
		t.Fatalf("post-finish evaluation=%#v err=%v", evaluation, err)
	}
	if finishedRun.WriterGeneration != originalGeneration {
		t.Fatalf("historical finish relabeled Run generation=%d, want original %d", finishedRun.WriterGeneration, originalGeneration)
	}
	replayedTask, replayedRun, replayedReference, err := manager.FinishTaskCenterExecution(ctx, task.TaskID, run.RunID, draft, "mio", domaintask.StatusSucceeded, "lookup completed", resultReference, finishAuth)
	if err != nil || replayedTask.TaskID != task.TaskID || replayedRun.RunID != run.RunID || replayedReference != recoveredReference {
		t.Fatalf("finish receipt replay Task=%#v Run=%#v reference=%#v err=%v", replayedTask, replayedRun, replayedReference, err)
	}
	linkedTask, linkedRun, err := manager.FindTaskCenterExecutionReceipt(ctx, key, 3, draft, shared, kindInput, readReceipt)
	if err != nil || linkedTask.TaskID != task.TaskID || linkedRun.RunID != run.RunID || linkedRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("terminal handoff receipt Task=%#v Run=%#v err=%v", linkedTask, linkedRun, err)
	}
	changedReference := resultReference
	changedReference.ArtifactID += ":changed"
	if _, _, _, err := manager.FinishTaskCenterExecution(ctx, task.TaskID, run.RunID, draft, "mio", domaintask.StatusFailed, "changed result", changedReference, finishAuth); !errors.Is(err, taskpersistence.ErrTaskOperationConflict) && !errors.Is(err, domaintask.ErrTaskCenterExecutionReceiptConflict) {
		t.Fatalf("changed finish operation error=%v, want receipt conflict", err)
	}
}
