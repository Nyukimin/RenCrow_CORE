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
	newID, err := resumeIdleChatRun(ctx, owner, taskID)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileGenerationResume(ctx, owner, &cp, store); err != nil {
		t.Fatal(err)
	}
	// Simulate another crash immediately after the shared recovery write.
	store = NewGenerationCheckpointStore(path)
	reloaded, ok := store.Get(cp.Key)
	if !ok || reloaded.RunID != newID || reloaded.StoryArtifact.RunID != newID || reloaded.StoryArtifact.Revision != 2 {
		t.Fatalf("recovered binding was not atomic: %+v", reloaded)
	}
	if err := reconcileGenerationResume(ctx, owner, &reloaded, store); err != nil {
		t.Fatal(err)
	}
	if reloaded.StoryArtifact.Revision != 2 {
		t.Fatal("same successor advanced revision twice")
	}
}
