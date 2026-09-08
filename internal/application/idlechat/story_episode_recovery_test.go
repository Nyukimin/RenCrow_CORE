package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type storyRecoveryGenerator struct {
	responses []string
	err       error
	calls     int
}

func (g *storyRecoveryGenerator) Generate(_ context.Context, _ string) (string, error) {
	g.calls++
	if g.err != nil {
		return "", g.err
	}
	if len(g.responses) == 0 {
		return "", errors.New("story recovery generator was called unexpectedly")
	}
	response := g.responses[0]
	g.responses = g.responses[1:]
	return response, nil
}

type storyCompletionFailOwner struct {
	*taskmanager.Manager
	failComplete bool
}

func (o *storyCompletionFailOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	if o.failComplete {
		return domaintask.Task{}, errors.New("injected story completion failure")
	}
	return o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
}

func storyRecoveryGeneratorResponses(t *testing.T, review StorySemanticReview) []string {
	t.Helper()
	artifact := validStoryEpisodeFixture()
	artifact.EpisodeID = ""
	artifact.TaskID = ""
	artifact.RunID = ""
	artifactJSON, err := json.Marshal(artifact)
	if err != nil {
		t.Fatal(err)
	}
	reviewJSON, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	return []string{string(artifactJSON), string(reviewJSON)}
}

func storyRecoveryCheckpoint(t *testing.T, owner *taskmanager.Manager, stage string) (GenerationCheckpoint, *GenerationCheckpointStore) {
	t.Helper()
	ctx := context.Background()
	task, err := owner.Create(ctx, domaintask.Task{Title: "story recovery", Route: domaintask.RouteGeneral, Assignee: "Shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	run, err := owner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	artifact := validStoryEpisodeFixture()
	artifact.EpisodeID = "recovery-artifact"
	artifact.TaskID = task.TaskID
	artifact.RunID = run.RunID
	artifact.CreatedAt = time.Now().UTC()
	review := StorySemanticReview{Valid: true}
	checkpoint := GenerationCheckpoint{
		Key: storyGenerationCheckpointKey, Kind: "story", TaskID: task.TaskID, RunID: run.RunID, Stage: stage,
		StoryArtifact: &artifact, StoryReview: &review,
	}
	path := filepath.Join(t.TempDir(), "story.checkpoints.json")
	return checkpoint, NewGenerationCheckpointStore(path)
}

func TestStoryEpisodeGenerationFailureRetainsWaitingCheckpoint(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "episodes.jsonl"), 1)
	generator := &storyRecoveryGenerator{err: errors.New("codex unavailable")}
	owner := newTestIdleChatRunIssuer(t)
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetRunIssuer(owner)
	service.maxAttempts = 1

	if err := service.PrepareToTarget(context.Background()); err == nil {
		t.Fatal("generation failure must be returned")
	}
	checkpointStore := service.generationCheckpointStore()
	checkpoint, ok := checkpointStore.Get(storyGenerationCheckpointKey)
	if !ok || checkpoint.Stage != "seed" || checkpoint.StorySeed == nil {
		t.Fatalf("checkpoint=%+v ok=%t", checkpoint, ok)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs=%+v, want one waiting run", runs)
	}
	if got := service.Snapshot(); got.Ready != 0 || got.Missing != 1 || len(got.Episodes) != 0 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestStoryEpisodeSavedArtifactResumeDoesNotGenerateAgain(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "episodes.jsonl"), 1)
	owner := newTestIdleChatRunIssuer(t)
	checkpoint, checkpointStore := storyRecoveryCheckpoint(t, owner, "review")
	if _, err := owner.CompleteRun(context.Background(), checkpoint.TaskID, checkpoint.RunID, "Shiro", domaintask.StatusWaiting, "paused", "retry story generation"); err != nil {
		t.Fatal(err)
	}
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRecoveryGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)
	service.maxAttempts = 1

	if err := service.PrepareToTarget(context.Background()); err != nil {
		t.Fatalf("resume saved artifact: %v", err)
	}
	if generator.calls != 0 {
		t.Fatalf("generator calls=%d, want 0", generator.calls)
	}
	if got := service.Snapshot(); got.Ready != 1 || len(got.Episodes) != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); ok {
		t.Fatal("checkpoint remains after resumed artifact completion")
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: checkpoint.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Status != domaintask.RunStatusWaiting || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("runs=%+v", runs)
	}
	if got := service.Snapshot().Episodes[0]; got.Revision != checkpoint.StoryArtifact.Revision+1 || got.RunID == checkpoint.RunID {
		t.Fatalf("resumed artifact=%+v", got)
	}
}

func TestStoryEpisodeRecoveryRejectsTerminalRunWithoutArtifact(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "episodes.jsonl"), 1)
	owner := newTestIdleChatRunIssuer(t)
	checkpoint, checkpointStore := storyRecoveryCheckpoint(t, owner, "review")
	if _, err := owner.CompleteRun(context.Background(), checkpoint.TaskID, checkpoint.RunID, "Shiro", domaintask.StatusSucceeded, "already complete", ""); err != nil {
		t.Fatal(err)
	}
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRecoveryGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)
	service.maxAttempts = 1

	if err := service.PrepareToTarget(context.Background()); err == nil {
		t.Fatal("terminal run without artifact must be rejected")
	}
	if generator.calls != 0 {
		t.Fatalf("generator calls=%d, want 0", generator.calls)
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); !ok {
		t.Fatal("checkpoint was discarded after missing terminal artifact")
	}
}

func TestStoryEpisodeRecoveryRejectsConflictingArtifact(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "episodes.jsonl"), 1)
	owner := newTestIdleChatRunIssuer(t)
	checkpoint, checkpointStore := storyRecoveryCheckpoint(t, owner, "review")
	conflicting := *checkpoint.StoryArtifact
	conflicting.StoryTitle = "別の保存作品"
	conflicting.ProductionStatus = StoryProductionReady
	conflicting.Validation = StoryValidationResult{Valid: true}
	if err := store.append(conflicting); err != nil {
		t.Fatal(err)
	}
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRecoveryGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)
	service.maxAttempts = 1

	if err := service.PrepareToTarget(context.Background()); err == nil {
		t.Fatal("conflicting terminal artifact must be rejected")
	}
	if generator.calls != 0 {
		t.Fatalf("generator calls=%d, want 0", generator.calls)
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); !ok {
		t.Fatal("checkpoint was discarded after conflicting artifact")
	}
}

func TestStoryEpisodeCompletionRetryDoesNotDuplicateAppend(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "episodes.jsonl"), 1)
	baseOwner := newTestIdleChatRunIssuer(t)
	owner := &storyCompletionFailOwner{Manager: baseOwner, failComplete: true}
	generator := &storyRecoveryGenerator{responses: storyRecoveryGeneratorResponses(t, StorySemanticReview{Valid: true})}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetRunIssuer(owner)
	service.maxAttempts = 1

	if err := service.PrepareToTarget(context.Background()); err == nil {
		t.Fatal("completion failure must be returned")
	}
	checkpointStore := service.generationCheckpointStore()
	checkpoint, ok := checkpointStore.Get(storyGenerationCheckpointKey)
	if !ok || checkpoint.StoryArtifact == nil || checkpoint.StoryReview == nil {
		t.Fatalf("checkpoint=%+v ok=%t", checkpoint, ok)
	}
	if got := service.Snapshot(); got.Ready != 0 || len(got.Episodes) != 0 {
		t.Fatalf("pending artifact must stay unpublished: %+v", got)
	}
	owner.failComplete = false
	if err := service.PrepareToTarget(context.Background()); err != nil {
		t.Fatalf("completion retry: %v", err)
	}
	if generator.calls != 2 {
		t.Fatalf("generator calls=%d, want initial artifact and review only", generator.calls)
	}
	if got := service.Snapshot(); got.Ready != 1 || len(got.Episodes) != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
	payload, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	var records []StoryEpisodeArtifact
	for _, line := range splitStoryRecoveryLines(payload) {
		var artifact StoryEpisodeArtifact
		if err := json.Unmarshal(line, &artifact); err != nil {
			t.Fatal(err)
		}
		records = append(records, artifact)
	}
	if len(records) != 1 {
		t.Fatalf("persisted records=%d, want one exact append", len(records))
	}
}

func TestStoryEpisodePublicationGateKeepsOtherRunsVisible(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "episodes.jsonl"), 1)
	pending := validStoryEpisodeFixture()
	pending.EpisodeID = "pending"
	pending.ProductionStatus = StoryProductionReady
	pending.Validation = StoryValidationResult{Valid: true}
	other := pending
	other.EpisodeID = "other"
	other.TaskID = modulecore.NewTaskID()
	other.RunID = modulecore.NewRunID()
	if err := store.append(pending); err != nil {
		t.Fatal(err)
	}
	if err := store.append(other); err != nil {
		t.Fatal(err)
	}
	checkpoint := GenerationCheckpoint{Key: storyGenerationCheckpointKey, Kind: "story", TaskID: pending.TaskID, RunID: pending.RunID, Stage: "review", StoryArtifact: &pending, StoryReview: &StorySemanticReview{Valid: true}}
	checkpointStore := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	service := NewStoryEpisodeService(store, &storyRecoveryGenerator{}, nil)
	service.SetGenerationCheckpointStore(checkpointStore)

	snapshot := service.Snapshot()
	if snapshot.Ready != 1 || len(snapshot.Episodes) != 1 || snapshot.Episodes[0].EpisodeID != other.EpisodeID {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	if _, ok := service.NextReady(); !ok {
		t.Fatal("unrelated ready artifact was hidden")
	}
	if _, ok := service.Episode(pending.EpisodeID); ok {
		t.Fatal("pending artifact was published")
	}
	if err := service.MarkPlayed(pending.EpisodeID, time.Now()); err == nil {
		t.Fatal("pending artifact was playable")
	}
	if err := service.MarkPlayed(other.EpisodeID, time.Now()); err != nil {
		t.Fatalf("unrelated artifact was blocked: %v", err)
	}
}

func splitStoryRecoveryLines(payload []byte) [][]byte {
	lines := make([][]byte, 0)
	for start := 0; start < len(payload); {
		end := start
		for end < len(payload) && payload[end] != '\n' {
			end++
		}
		if end > start {
			lines = append(lines, payload[start:end])
		}
		if end == len(payload) {
			break
		}
		start = end + 1
	}
	return lines
}

func TestStoryEpisodeSuccessorCheckpointHidesPreviousSavedRevision(t *testing.T) {
	ctx := context.Background()
	owner := newTestIdleChatRunIssuer(t)
	cp, checkpoints := storyRecoveryCheckpoint(t, owner, "review")
	path := filepath.Join(t.TempDir(), "episodes.jsonl")
	stock := newStoryEpisodeStore(path, 1)
	old := cloneStoryEpisode(*cp.StoryArtifact)
	old.Validation = ValidateStoryEpisode(old, *cp.StoryReview)
	old.ProductionStatus = StoryProductionReady
	if err := stock.append(old); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.CompleteRun(ctx, cp.TaskID, cp.RunID, "Shiro", domaintask.StatusWaiting, "paused", "retry saved episode"); err != nil {
		t.Fatal(err)
	}
	cp.Stage = "resume_pending"
	if err := checkpoints.Put(cp); err != nil {
		t.Fatal(err)
	}
	issued, err := resumeIdleChatRun(ctx, owner, &cp, checkpoints)
	if err != nil {
		t.Fatal(err)
	}
	cp.RunID = issued.RunID
	cp.StoryArtifact.RunID = issued.RunID
	cp.StoryArtifact.Revision++
	if err := checkpoints.Put(cp); err != nil {
		t.Fatal(err)
	}
	// Crash before appending the successor revision: only the old ready row is on disk.
	service := NewPersistentStoryEpisodeService(path, 1, &storyRecoveryGenerator{}, nil)
	service.SetGenerationCheckpointStore(NewGenerationCheckpointStore(checkpoints.path))
	service.SetRunIssuer(owner)
	if service.Snapshot().Ready != 0 {
		t.Error("previous revision became visible while successor publication was pending")
	}
	if _, ok := service.NextReady(); ok {
		t.Error("previous revision was selected")
	}
	if _, ok := service.Episode(old.EpisodeID); ok {
		t.Error("previous revision was returned by id")
	}
	if err := service.MarkPlayed(old.EpisodeID, time.Now()); err == nil {
		t.Error("previous revision was marked played")
	}
}
