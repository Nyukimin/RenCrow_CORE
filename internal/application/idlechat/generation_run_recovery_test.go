package idlechat

import (
	"context"
	"path/filepath"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestGenerationResumePersistsArtifactRevisionWithSuccessorIdentity(t *testing.T) {
	ctx := context.Background()
	owner := newTestIdleChatRunIssuer(t)
	taskID, oldID, err := issueIdleChatRun(ctx, owner, "recovery", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := completeGenerationRun(ctx, owner, taskID, oldID, domaintask.StatusWaiting, "paused", "retry"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store := NewGenerationCheckpointStore(path)
	cp := GenerationCheckpoint{Key: "story:prepare", Kind: "story", TaskID: taskID, RunID: oldID, Stage: "resume_pending",
		StoryArtifact: &StoryEpisodeArtifact{TaskID: taskID, RunID: oldID, Revision: 1}}
	if err := store.Put(cp); err != nil {
		t.Fatal(err)
	}
	newRun, err := resumeIdleChatRun(ctx, owner, &cp, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileGenerationResume(ctx, owner, &cp, store); err != nil {
		t.Fatal(err)
	}
	// Simulate another crash immediately after the shared recovery write.
	store = NewGenerationCheckpointStore(path)
	reloaded, ok := store.Get(cp.Key)
	if !ok || reloaded.RunID != newRun.RunID || reloaded.StoryArtifact.RunID != newRun.RunID || reloaded.StoryArtifact.Revision != 2 {
		t.Fatalf("recovered binding was not atomic: %+v", reloaded)
	}
	if err := reconcileGenerationResume(ctx, owner, &reloaded, store); err != nil {
		t.Fatal(err)
	}
	if reloaded.StoryArtifact.Revision != 2 {
		t.Fatal("same successor advanced revision twice")
	}
}

func TestGenerationRerunPersistsExactSuccessor(t *testing.T) {
	ctx := context.Background()
	owner := newTestIdleChatRunIssuer(t)
	taskID, oldID, err := issueIdleChatRun(ctx, owner, "revision", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := completeGenerationRun(ctx, owner, taskID, oldID, domaintask.StatusSucceeded, "saved", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store := NewGenerationCheckpointStore(path)
	cp := GenerationCheckpoint{Key: "story:prepare", Kind: "story", TaskID: taskID, RunID: oldID, Stage: "rerun_pending", StoryArtifact: &StoryEpisodeArtifact{TaskID: taskID, RunID: oldID, Revision: 3}}
	if err := store.Put(cp); err != nil {
		t.Fatal(err)
	}
	successor, err := rerunIdleChatRun(ctx, owner, &cp, store)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileGenerationRerun(ctx, owner, &cp, store); err != nil {
		t.Fatal(err)
	}
	store = NewGenerationCheckpointStore(path)
	got, ok := store.Get(cp.Key)
	if !ok || got.RunID != successor.RunID || got.StoryArtifact.RunID != successor.RunID || got.StoryArtifact.Revision != 4 {
		t.Fatalf("successor not atomically persisted: %+v", got)
	}
	if err := reconcileGenerationRerun(ctx, owner, &got, store); err != nil {
		t.Fatal(err)
	}
	if got.StoryArtifact.Revision != 4 {
		t.Fatal("retry advanced the same successor revision twice")
	}
}

func TestGenerationRerunSaveFailureRetainsVerifiedSuccessorForWaiting(t *testing.T) {
	ctx := context.Background()
	owner := newTestIdleChatRunIssuer(t)
	taskID, oldID, err := issueIdleChatRun(ctx, owner, "revision", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := completeGenerationRun(ctx, owner, taskID, oldID, domaintask.StatusFailed, "repair needed", ""); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store := NewGenerationCheckpointStore(path)
	cp := GenerationCheckpoint{Key: "story:prepare", Kind: "story", TaskID: taskID, RunID: oldID, Stage: "rerun_pending", StoryArtifact: &StoryEpisodeArtifact{TaskID: taskID, RunID: oldID, Revision: 1}}
	if err := store.Put(cp); err != nil {
		t.Fatal(err)
	}
	successor, err := rerunIdleChatRun(ctx, owner, &cp, store)
	if err != nil {
		t.Fatal(err)
	}
	store.path = t.TempDir() // Rename onto a directory deterministically fails.
	if err := reconcileGenerationRerun(ctx, owner, &cp, store); err == nil {
		t.Fatal("expected persistence failure")
	}
	if cp.RunID != successor.RunID || cp.StoryArtifact.Revision != 2 {
		t.Fatal("lost verified successor on save failure")
	}
	if err := finishGenerationRun(ctx, owner, cp, domaintask.StatusWaiting, "save interrupted", "retry checkpoint save"); err != nil {
		t.Fatal(err)
	}
	store = NewGenerationCheckpointStore(path)
	persisted, ok := store.Get(cp.Key)
	if !ok || persisted.RunID != oldID || persisted.StoryArtifact.Revision != 1 {
		t.Fatal("failed save replaced durable predecessor")
	}
	if err := reconcileGenerationRerun(ctx, owner, &persisted, store); err != nil {
		t.Fatal(err)
	}
	if persisted.RunID != successor.RunID || persisted.StoryArtifact.Revision != 2 {
		t.Fatal("recovery did not bind exactly once")
	}
}
