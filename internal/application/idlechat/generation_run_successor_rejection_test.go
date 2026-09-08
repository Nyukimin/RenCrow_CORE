package idlechat

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newGenerationSuccessorCheckpoint(t *testing.T, stage string, taskID modulecore.TaskID, runID modulecore.RunID) (*GenerationCheckpointStore, GenerationCheckpoint) {
	t.Helper()
	checkpoint := GenerationCheckpoint{
		Key: "story:successor", Kind: "story", TaskID: taskID, RunID: runID, Stage: stage,
		StoryArtifact: &StoryEpisodeArtifact{TaskID: taskID, RunID: runID, Revision: 3},
	}
	store := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err := store.Put(checkpoint); err != nil {
		t.Fatalf("save successor checkpoint: %v", err)
	}
	return store, checkpoint
}

func generationSuccessorTerminalRuns(taskID modulecore.TaskID, previousID modulecore.RunID, previousStatus domaintask.RunStatus, successorReason domaintask.RunStartReason, previousStart, successorStart time.Time) []domaintask.Run {
	previous := wordLifecycleRun(taskID, previousID, previousStatus, previousStart)
	successor := wordLifecycleRun(taskID, modulecore.NewRunID(), domaintask.RunStatusRunning, successorStart)
	successor.StartReason = successorReason
	return []domaintask.Run{previous, successor}
}

func assertGenerationSuccessorRejectedWithoutMutation(t *testing.T, owner *wordRunLifecycleOwnerFake, store *GenerationCheckpointStore, checkpoint *GenerationCheckpoint, wantError string, reconcile func() error) {
	t.Helper()
	beforeCheckpoint := cloneGenerationCheckpoint(*checkpoint)
	beforeStored, ok := store.Get(checkpoint.Key)
	if !ok {
		t.Fatal("checkpoint missing before rejection")
	}
	err := reconcile()
	if err == nil {
		t.Fatal("successor reconciliation unexpectedly succeeded")
	}
	if wantError != "" && !strings.Contains(err.Error(), wantError) {
		t.Fatalf("successor rejection error = %v, want substring %q", err, wantError)
	}
	if !reflect.DeepEqual(*checkpoint, beforeCheckpoint) {
		t.Fatalf("checkpoint pointer mutated on rejection: before=%+v after=%+v", beforeCheckpoint, *checkpoint)
	}
	afterStored, ok := store.Get(checkpoint.Key)
	if !ok || !reflect.DeepEqual(afterStored, beforeStored) {
		t.Fatalf("persisted checkpoint mutated on rejection: before=%+v after=%+v ok=%t", beforeStored, afterStored, ok)
	}
	if len(owner.completeCalls) != 0 || len(owner.verifyCalls) != 0 {
		t.Fatalf("rejection attempted owner completion: complete=%d verify=%d", len(owner.completeCalls), len(owner.verifyCalls))
	}
}

func TestGenerationSuccessorRejectsWrongReasonWithoutMutation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "rerun_pending", taskID, oldID)
	start := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	owner := &wordRunLifecycleOwnerFake{
		task: wordLifecycleTask(taskID, domaintask.StatusRunning),
		runs: generationSuccessorTerminalRuns(taskID, oldID, domaintask.RunStatusSucceeded, domaintask.RunStartReasonCheckpointResume, start, start.Add(time.Second)),
	}
	assertGenerationSuccessorRejectedWithoutMutation(t, owner, store, &checkpoint, "does not match persisted intent", func() error {
		return reconcileGenerationRerun(context.Background(), owner, &checkpoint, store)
	})
}

func TestGenerationSuccessorRejectsWrongStageWithoutMutation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "resume_pending", taskID, oldID)
	owner := &wordRunLifecycleOwnerFake{}
	assertGenerationSuccessorRejectedWithoutMutation(t, owner, store, &checkpoint, "matching persisted intent", func() error {
		return reconcileGenerationRerun(context.Background(), owner, &checkpoint, store)
	})
}

func TestGenerationSuccessorRejectsAmbiguousLaterRunsWithoutMutation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "rerun_pending", taskID, oldID)
	start := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	runs := generationSuccessorTerminalRuns(taskID, oldID, domaintask.RunStatusFailed, domaintask.RunStartReasonExplicitRerun, start, start.Add(time.Second))
	second := wordLifecycleRun(taskID, modulecore.NewRunID(), domaintask.RunStatusRunning, start.Add(2*time.Second))
	second.StartReason = domaintask.RunStartReasonExplicitRerun
	runs = append(runs, second)
	owner := &wordRunLifecycleOwnerFake{task: wordLifecycleTask(taskID, domaintask.StatusRunning), runs: runs}
	assertGenerationSuccessorRejectedWithoutMutation(t, owner, store, &checkpoint, "ambiguous", func() error {
		return reconcileGenerationRerun(context.Background(), owner, &checkpoint, store)
	})
}

func TestGenerationSuccessorRejectsEqualStartedAtWithoutMutation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "rerun_pending", taskID, oldID)
	start := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	owner := &wordRunLifecycleOwnerFake{
		task: wordLifecycleTask(taskID, domaintask.StatusRunning),
		runs: generationSuccessorTerminalRuns(taskID, oldID, domaintask.RunStatusCancelled, domaintask.RunStartReasonExplicitRerun, start, start),
	}
	assertGenerationSuccessorRejectedWithoutMutation(t, owner, store, &checkpoint, "ambiguous", func() error {
		return reconcileGenerationRerun(context.Background(), owner, &checkpoint, store)
	})
}

func TestGenerationSuccessorRejectsMissingOwnerWithoutMutation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "rerun_pending", taskID, oldID)
	assertGenerationSuccessorRejectedWithoutMutation(t, &wordRunLifecycleOwnerFake{}, store, &checkpoint, "run issuer is not configured", func() error {
		return reconcileGenerationRerun(context.Background(), nil, &checkpoint, store)
	})
}

func TestGenerationSuccessorRejectsTiedPredecessorWithLaterRun(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "rerun_pending", taskID, oldID)
	start := time.Date(2026, 9, 8, 4, 0, 0, 0, time.UTC)
	runs := generationSuccessorTerminalRuns(taskID, oldID, domaintask.RunStatusSucceeded, domaintask.RunStartReasonExplicitRerun, start, start.Add(time.Second))
	runs = append(runs, wordLifecycleRun(taskID, modulecore.NewRunID(), domaintask.RunStatusFailed, start))
	owner := &wordRunLifecycleOwnerFake{task: wordLifecycleTask(taskID, domaintask.StatusRunning), runs: runs}
	assertGenerationSuccessorRejectedWithoutMutation(t, owner, store, &checkpoint, "predecessor timestamp is ambiguous", func() error {
		return reconcileGenerationRerun(context.Background(), owner, &checkpoint, store)
	})
}

func TestGenerationSuccessorRejectsOwnerHistoryErrorWithoutMutation(t *testing.T) {
	taskID := modulecore.NewTaskID()
	oldID := modulecore.NewRunID()
	store, checkpoint := newGenerationSuccessorCheckpoint(t, "rerun_pending", taskID, oldID)
	wantErr := errors.New("owner history unavailable")
	owner := &wordRunLifecycleOwnerFake{listErr: wantErr}
	assertGenerationSuccessorRejectedWithoutMutation(t, owner, store, &checkpoint, "owner history unavailable", func() error {
		return reconcileGenerationRerun(context.Background(), owner, &checkpoint, store)
	})
}
