package idlechat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newPendingDialogueCheckpointFixture(t *testing.T, pending PendingCompletion) (string, DialogueInterestingnessConfig, *dialogueRecoveryOwner, *GenerationCheckpointStore, GenerationCheckpoint) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	task, err := owner.Create(context.Background(), domaintask.Task{
		Title:    "IdleChat pending dialogue completion",
		Route:    domaintask.RouteGeneral,
		Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start run: %v", err)
	}
	result := dialogueRecoveryInput()
	plan := NewDialogueDirector(config).BuildArcPlan(result)
	plan.TurnPlans = buildDialogueTurnPlans(2, dialogueCategorySpec(plan.Category))
	result.Category = plan.Category
	artifact := DialogueEpisodeArtifact{
		SchemaVersion:    DialogueEpisodeSchemaVersion,
		EpisodeID:        "dialogue-pending-completion",
		TaskID:           task.TaskID,
		RunID:            run.RunID,
		Revision:         1,
		SessionID:        "dialogue-pending-completion-session",
		InitiatedBy:      "shiro",
		TopicResult:      result,
		ArcPlan:          plan,
		Participants:     []string{"mio", "shiro"},
		ProductionStatus: DialogueProductionValidating,
		Validation:       DialogueEpisodeValidation{Valid: false, FirstInvalidTurn: 1},
	}
	checkpoint := GenerationCheckpoint{
		Key:               dialogueCheckpointKey(artifact.SessionID, result, 2),
		Kind:              "dialogue",
		TaskID:            task.TaskID,
		RunID:             run.RunID,
		Stage:             "seed",
		DialogueArtifact:  cloneDialogueEpisodePtr(artifact),
		PendingCompletion: &pending,
	}
	store := NewGenerationCheckpointStore(path + ".checkpoints.json")
	if err := store.Put(checkpoint); err != nil {
		t.Fatalf("save pending dialogue checkpoint: %v", err)
	}
	return path, config, owner, store, checkpoint
}

func preparePendingDialogueCompletion(t *testing.T) (string, DialogueInterestingnessConfig, *dialogueRecoveryOwner, *GenerationCheckpointStore, GenerationCheckpoint) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
	config := DefaultDialogueInterestingnessConfig()
	config.MaxTurnsPerTopic = 2
	owner := newDialogueRecoveryOwner(t)
	owner.failCompleteOnce = errors.New("injected dialogue completion failure")
	generator := &dialogueRecoveryGenerator{err: errors.New("injected dialogue provider failure")}
	service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetRunIssuer(owner)
	sessionID := "dialogue-pending-completion-session"
	result := dialogueRecoveryInput()
	if artifact, err := service.Prepare(context.Background(), sessionID, result, 2); err == nil || artifact.EpisodeID != "" {
		t.Fatalf("pending dialogue setup artifact=%+v err=%v", artifact, err)
	}
	store := NewGenerationCheckpointStore(path + ".checkpoints.json")
	key := dialogueCheckpointKey(sessionID, result, 2)
	checkpoint, ok := store.Get(key)
	if !ok || checkpoint.PendingCompletion == nil {
		t.Fatalf("pending dialogue checkpoint=%+v ok=%t", checkpoint, ok)
	}
	return path, config, owner, store, checkpoint
}

func TestDialogueCompletionIntentReloadFinalizesExactRunWithoutProvider(t *testing.T) {
	path, config, owner, store, checkpoint := preparePendingDialogueCompletion(t)
	if checkpoint.PendingCompletion.Status != domaintask.StatusWaiting {
		t.Fatalf("pending status=%s, want waiting", checkpoint.PendingCompletion.Status)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		t.Fatalf("list runs before reload finalization: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != checkpoint.RunID || runs[0].Status != domaintask.RunStatusRunning {
		t.Fatalf("runs before reload finalization=%+v", runs)
	}

	retryGenerator := &dialogueRecoveryGenerator{err: errors.New("reload finalization must not call provider")}
	reloaded := NewPersistentDialogueEpisodeService(path, retryGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	reloaded.SetGenerationCheckpointStore(store)
	reloaded.SetRunIssuer(owner)
	if err := reloaded.FinalizePendingRuns(context.Background()); err != nil {
		t.Fatalf("finalize pending dialogue completion: %v", err)
	}
	if retryGenerator.calls != 0 {
		t.Fatalf("provider calls during completion finalization=%d, want zero", retryGenerator.calls)
	}
	if owner.startCalls != 1 {
		t.Fatalf("run starts during completion finalization=%d, want one original Run", owner.startCalls)
	}

	cleared, ok := store.Get(checkpoint.Key)
	if !ok {
		t.Fatal("waiting completion checkpoint was deleted")
	}
	if cleared.PendingCompletion != nil || cleared.TaskID != checkpoint.TaskID || cleared.RunID != checkpoint.RunID || cleared.Stage != checkpoint.Stage {
		t.Fatalf("cleared waiting checkpoint=%+v", cleared)
	}
	runs, err = owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		t.Fatalf("list runs after reload finalization: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != checkpoint.RunID || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after reload finalization=%+v", runs)
	}
	task, err := owner.Get(context.Background(), checkpoint.TaskID)
	if err != nil {
		t.Fatalf("get task after reload finalization: %v", err)
	}
	if task.Status != domaintask.StatusWaiting {
		t.Fatalf("task after reload finalization=%s, want waiting", task.Status)
	}
}

func TestDialogueCompletionRejectsCorruptIntentsBeforeMutation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GenerationCheckpoint)
		want   string
	}{
		{
			name: "missing artifact",
			mutate: func(checkpoint *GenerationCheckpoint) {
				checkpoint.DialogueArtifact = nil
			},
			want: "artifact is missing",
		},
		{
			name: "pair mismatch",
			mutate: func(checkpoint *GenerationCheckpoint) {
				checkpoint.DialogueArtifact.RunID = modulecore.NewRunID()
			},
			want: "Run identity",
		},
		{
			name: "invalid status",
			mutate: func(checkpoint *GenerationCheckpoint) {
				checkpoint.PendingCompletion.Status = domaintask.StatusRunning
			},
			want: "status is not accepted",
		},
		{
			name: "missing waiting reason",
			mutate: func(checkpoint *GenerationCheckpoint) {
				checkpoint.PendingCompletion.Reason = " "
			},
			want: "requires a reason",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path, config, owner, store, checkpoint := newPendingDialogueCheckpointFixture(t, PendingCompletion{
				Status:  domaintask.StatusWaiting,
				Summary: "dialogue provider failed",
				Reason:  "retry after cleanup",
			})
			test.mutate(&checkpoint)
			if err := store.Put(checkpoint); err != nil {
				t.Fatalf("persist corrupt intent: %v", err)
			}
			generator := &dialogueRecoveryGenerator{err: errors.New("invalid intent must not call provider")}
			service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
			service.SetGenerationCheckpointStore(store)
			service.SetRunIssuer(owner)
			err := service.FinalizePendingRuns(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("FinalizePendingRuns error=%v, want %q", err, test.want)
			}
			if generator.calls != 0 {
				t.Fatalf("provider calls=%d, want zero", generator.calls)
			}
			runs, listErr := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
			if listErr != nil {
				t.Fatalf("list runs after rejected intent: %v", listErr)
			}
			if len(runs) != 1 || runs[0].RunID != checkpoint.RunID || runs[0].Status != domaintask.RunStatusRunning {
				t.Fatalf("runs after rejected intent=%+v", runs)
			}
		})
	}

	t.Run("malformed checkpoint store", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "dialogue_episodes.jsonl")
		checkpointPath := path + ".checkpoints.json"
		if err := os.WriteFile(checkpointPath, []byte("{"), 0o600); err != nil {
			t.Fatalf("write malformed checkpoint store: %v", err)
		}
		owner := newDialogueRecoveryOwner(t)
		task, err := owner.Create(context.Background(), domaintask.Task{
			Title:    "Malformed pending dialogue checkpoint",
			Route:    domaintask.RouteGeneral,
			Assignee: "Shiro",
		}, domaintask.SharedRoleContext{})
		if err != nil {
			t.Fatalf("create task: %v", err)
		}
		if _, err := owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst); err != nil {
			t.Fatalf("start run: %v", err)
		}
		store := NewGenerationCheckpointStore(checkpointPath)
		generator := &dialogueRecoveryGenerator{err: errors.New("malformed intent must not call provider")}
		service := NewPersistentDialogueEpisodeService(path, generator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, DefaultDialogueInterestingnessConfig())
		service.SetGenerationCheckpointStore(store)
		service.SetRunIssuer(owner)
		if err := service.FinalizePendingRuns(context.Background()); err == nil || !strings.Contains(err.Error(), "checkpoint store unavailable") {
			t.Fatalf("FinalizePendingRuns malformed store error=%v", err)
		}
		if generator.calls != 0 {
			t.Fatalf("provider calls=%d, want zero", generator.calls)
		}
		runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
		if err != nil {
			t.Fatalf("list runs after malformed store: %v", err)
		}
		if len(runs) != 1 || runs[0].Status != domaintask.RunStatusRunning {
			t.Fatalf("runs after malformed store=%+v, want original running run", runs)
		}
		got, err := owner.Get(context.Background(), task.TaskID)
		if err != nil {
			t.Fatalf("get task after malformed store: %v", err)
		}
		if got.Status != domaintask.StatusRunning {
			t.Fatalf("task after malformed store=%s, want running", got.Status)
		}
	})
}

func TestStopRetriesPendingDialogueCompletionAfterGenerationJoin(t *testing.T) {
	path, config, owner, store, checkpoint := preparePendingDialogueCompletion(t)
	retryGenerator := &dialogueRecoveryGenerator{err: errors.New("Stop completion retry must not call provider")}
	service := NewPersistentDialogueEpisodeService(path, retryGenerator, map[string]string{"mio": "Mio", "shiro": "Shiro"}, config)
	service.SetGenerationCheckpointStore(store)
	service.SetRunIssuer(owner)

	o := NewIdleChatOrchestrator(nil, nil, []string{"mio", "shiro"}, 60, 1, 0.7, nil, "")
	o.SetRunIssuer(owner)
	o.SetDialogueEpisodeService(service)
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	if !o.startGenerationWork(func() {
		close(started)
		<-release
	}) {
		t.Fatal("generation work was not admitted")
	}
	waitForGenerationWorkSignal(t, started, "admitted generation work")

	stopDone := make(chan struct{})
	go func() {
		o.Stop()
		close(stopDone)
	}()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		o.generationWorkMu.Lock()
		closed := o.generationWorkClosed
		o.generationWorkMu.Unlock()
		if closed {
			break
		}
		select {
		case <-deadline.C:
			t.Fatal("Stop did not close generation admission")
		case <-time.After(time.Millisecond):
		}
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		t.Fatalf("list runs before releasing joined generation work: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != checkpoint.RunID || runs[0].Status != domaintask.RunStatusRunning {
		t.Fatalf("runs before releasing joined generation work=%+v, want exact running Run", runs)
	}
	releaseOnce.Do(func() { close(release) })
	waitForGenerationWorkSignal(t, stopDone, "Stop completion")

	if retryGenerator.calls != 0 {
		t.Fatalf("provider calls during Stop completion retry=%d, want zero", retryGenerator.calls)
	}
	if owner.startCalls != 1 {
		t.Fatalf("run starts during Stop completion retry=%d, want one original Run", owner.startCalls)
	}
	cleared, ok := store.Get(checkpoint.Key)
	if !ok || cleared.PendingCompletion != nil || cleared.TaskID != checkpoint.TaskID || cleared.RunID != checkpoint.RunID {
		t.Fatalf("checkpoint after Stop completion retry=%+v ok=%t", cleared, ok)
	}
	runs, err = owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		t.Fatalf("list runs after Stop completion retry: %v", err)
	}
	if len(runs) != 1 || runs[0].RunID != checkpoint.RunID || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after Stop completion retry=%+v", runs)
	}
}

func TestGenerationCheckpointPendingCompletionSnapshotIsDetached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dialogue-checkpoints.json")
	store := NewGenerationCheckpointStore(path)
	taskID := modulecore.NewTaskID()
	runID := modulecore.NewRunID()
	artifact := DialogueEpisodeArtifact{
		SchemaVersion: DialogueEpisodeSchemaVersion,
		EpisodeID:     "dialogue-snapshot",
		TaskID:        taskID,
		RunID:         runID,
		Revision:      1,
		SessionID:     "dialogue-snapshot-session",
		Participants:  []string{"mio", "shiro"},
		Turns: []DialogueEpisodeTurn{{
			TurnIndex: 1, MessageID: "message-snapshot", Speaker: "mio", DisplayText: "original display", SpeechText: "original speech",
		}},
	}
	checkpoint := GenerationCheckpoint{
		Key:              "dialogue:snapshot",
		Kind:             "dialogue",
		TaskID:           taskID,
		RunID:            runID,
		Stage:            "ready",
		DialogueArtifact: &artifact,
		PendingCompletion: &PendingCompletion{
			Status: domaintask.StatusWaiting, Summary: "original summary", Reason: "original reason",
		},
	}
	if err := store.Put(checkpoint); err != nil {
		t.Fatalf("save snapshot checkpoint: %v", err)
	}

	checkpoint.PendingCompletion.Reason = "mutated source reason"
	checkpoint.DialogueArtifact.Turns[0].DisplayText = "mutated source display"
	got, ok := store.Get(checkpoint.Key)
	if !ok || got.PendingCompletion == nil || got.PendingCompletion.Reason != "original reason" || got.DialogueArtifact.Turns[0].DisplayText != "original display" {
		t.Fatalf("store snapshot after source mutation=%+v ok=%t", got, ok)
	}

	got.PendingCompletion.Reason = "mutated returned reason"
	got.DialogueArtifact.Turns[0].DisplayText = "mutated returned display"
	pending := store.ListPending()
	if len(pending) != 1 {
		t.Fatalf("pending snapshots=%d, want one", len(pending))
	}
	pending[0].PendingCompletion.Reason = "mutated listed reason"
	pending[0].DialogueArtifact.Turns[0].DisplayText = "mutated listed display"
	again, ok := store.Get(checkpoint.Key)
	if !ok || again.PendingCompletion == nil || again.PendingCompletion.Reason != "original reason" || again.DialogueArtifact.Turns[0].DisplayText != "original display" {
		t.Fatalf("store snapshot after returned mutations=%+v ok=%t", again, ok)
	}
}
