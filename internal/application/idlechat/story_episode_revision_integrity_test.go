package idlechat

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func newStoryRevisionIntegrityService(t *testing.T, stage, episodeID string) (*StoryEpisodeService, *storyRevisionTestGenerator) {
	t.Helper()
	source := validStoryEpisodeFixture()
	source.EpisodeID = "integrity-source"
	source.ProductionStatus = StoryProductionReady
	source.Validation = StoryValidationResult{Valid: true}
	owner, source := storyServiceArtifactWithTerminalRun(t, source)

	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(source); err != nil {
		t.Fatal(err)
	}
	var ok bool
	if source, ok = store.get(source.EpisodeID); !ok {
		t.Fatal("source artifact was not stored")
	}
	successor, err := owner.StartRunWithReason(context.Background(), source.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	pending := cloneStoryEpisode(source)
	pending.EpisodeID = episodeID
	pending.RunID = successor.RunID
	pending.Revision++
	checkpoint := storyRevisionCheckpointFor(pending, storyRevisionOperationTitle, storyRevisionPhaseInput, stage)
	checkpoint.StoryRevision.SourceRunID = source.RunID
	checkpointStore := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "story.checkpoints.json"))
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)
	return service, generator
}

func TestStoryRevisionIntegrityRejectsSourceRunEpisodeMismatchBeforeLLM(t *testing.T) {
	service, generator, sourceEpisodeID := newStoryRevisionSourceMismatchService(t)

	err := service.PrepareToTarget(context.Background())
	if err == nil || !strings.Contains(err.Error(), "source Run belongs to another episode") {
		t.Fatalf("error=%v, want source Run belongs to another episode", err)
	}
	if len(generator.prompts) != 0 {
		t.Fatalf("generator calls=%d, want 0 for rejected source/artifact identity", len(generator.prompts))
	}
	snapshot := service.Snapshot()
	if snapshot.Enabled || snapshot.Ready != 0 || len(snapshot.Episodes) != 0 {
		t.Fatalf("snapshot=%+v, want disabled with source episode %q unpublished", snapshot, sourceEpisodeID)
	}
}

func TestStoryRevisionIntegrityRejectsReviewStageWithInputPhaseBeforeLLM(t *testing.T) {
	service, generator := newStoryRevisionIntegrityService(t, "review", "integrity-source")

	err := service.PrepareToTarget(context.Background())
	if err == nil || !strings.Contains(err.Error(), "stage") || !strings.Contains(err.Error(), "phase") {
		t.Fatalf("error=%v, want explicit stage/phase mismatch", err)
	}
	if len(generator.prompts) != 0 {
		t.Fatalf("generator calls=%d, want 0 for rejected stage/phase mismatch", len(generator.prompts))
	}
}

func TestStoryRevisionAppendPreservesPlaybackAfterStaleSnapshot(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	original := validStoryEpisodeFixture()
	original.EpisodeID = "integrity-playback"
	original.ProductionStatus = StoryProductionReady
	original.Validation = StoryValidationResult{Valid: true}
	if err := store.append(original); err != nil {
		t.Fatal(err)
	}

	stale, ok := store.get(original.EpisodeID)
	if !ok {
		t.Fatal("original artifact was not stored")
	}
	playedAt := time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)
	if err := store.markPlayed(original.EpisodeID, playedAt); err != nil {
		t.Fatal(err)
	}
	stale.RunID = modulecore.NewRunID()
	stale.Revision++
	if err := store.append(stale); err != nil {
		t.Fatal(err)
	}

	got, ok := store.get(original.EpisodeID)
	if !ok {
		t.Fatal("successor artifact was not stored")
	}
	if got.PlayCount != 1 || got.LastPlayedAt == nil || !got.LastPlayedAt.Equal(playedAt) {
		t.Fatalf("successor playback=%+v, want count=1 and timestamp=%s", got, playedAt)
	}
}

func newStoryRevisionSourceMismatchService(t *testing.T) (*StoryEpisodeService, *storyRevisionTestGenerator, string) {
	t.Helper()
	source := validStoryEpisodeFixture()
	source.EpisodeID = "integrity-source"
	source.ProductionStatus = StoryProductionReady
	source.Validation = StoryValidationResult{Valid: true}
	owner, source := storyServiceArtifactWithTerminalRun(t, source)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(source); err != nil {
		t.Fatal(err)
	}
	if stored, ok := store.get(source.EpisodeID); ok {
		source = stored
	} else {
		t.Fatal("source artifact was not stored")
	}
	storedRun, err := owner.StartRunWithReason(context.Background(), source.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	stored := cloneStoryEpisode(source)
	stored.EpisodeID = "integrity-swapped"
	stored.RunID = storedRun.RunID
	stored.Revision++
	if _, err := owner.CompleteRun(context.Background(), source.TaskID, storedRun.RunID, "Shiro", domaintask.StatusSucceeded, "stored alternate episode", ""); err != nil {
		t.Fatal(err)
	}
	if err := store.append(stored); err != nil {
		t.Fatal(err)
	}
	if persisted, ok := store.get(stored.EpisodeID); ok {
		// Keep the persisted CreatedAt/UpdatedAt values in the checkpoint source.
		// The checkpoint below intentionally changes only execution identity.
		stored = persisted
	} else {
		t.Fatal("alternate artifact was not stored")
	}
	runningRun, err := owner.StartRunWithReason(context.Background(), source.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	pending := cloneStoryEpisode(stored)
	pending.RunID = runningRun.RunID
	pending.Revision++
	checkpoint := storyRevisionCheckpointFor(pending, storyRevisionOperationTitle, storyRevisionPhaseInput, "artifact")
	checkpoint.StoryRevision.SourceRunID = source.RunID
	checkpointStore := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "story.checkpoints.json"))
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)
	return service, generator, source.EpisodeID
}
