package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type storyRevisionGeneratorResponse struct {
	payload string
	err     error
}

type storyRevisionTestGenerator struct {
	responses []storyRevisionGeneratorResponse
	prompts   []string
}

type storyRevisionCleanupFailOwner struct {
	*taskmanager.Manager
	checkpointStore *GenerationCheckpointStore
	blockedPath     string
	failCleanup     bool
}

func (o *storyRevisionCleanupFailOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	task, err := o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
	if err == nil && o.failCleanup {
		if err := os.Mkdir(o.blockedPath, 0o700); err != nil {
			return task, err
		}
		o.checkpointStore.path = o.blockedPath
		o.failCleanup = false
	}
	return task, err
}

func (g *storyRevisionTestGenerator) Generate(_ context.Context, prompt string) (string, error) {
	g.prompts = append(g.prompts, prompt)
	if len(g.responses) == 0 {
		return "", errors.New("story revision generator was called unexpectedly")
	}
	response := g.responses[0]
	g.responses = g.responses[1:]
	return response.payload, response.err
}

func storyRevisionCheckpointFor(artifact StoryEpisodeArtifact, operation, phase, stage string) GenerationCheckpoint {
	artifact = cloneStoryEpisode(artifact)
	return GenerationCheckpoint{
		Key: storyGenerationCheckpointKey, Kind: "story", TaskID: artifact.TaskID, RunID: artifact.RunID, Stage: stage,
		StoryArtifact: &artifact,
		StoryRevision: &storyRevisionCheckpoint{Operation: operation, Phase: phase, SourceRunID: artifact.RunID},
	}
}

func storyRevisionTitleResponse(title string) storyRevisionGeneratorResponse {
	return storyRevisionGeneratorResponse{payload: `{"story_title":"` + title + `"}`}
}

func storyRevisionReviewResponse(valid bool) storyRevisionGeneratorResponse {
	payload, _ := json.Marshal(StorySemanticReview{Valid: valid})
	return storyRevisionGeneratorResponse{payload: string(payload)}
}

func TestStoryRevisionRequiresCanonicalRunOwnerBeforeLLM(t *testing.T) {
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{storyRevisionTitleResponse("should not be generated")}}
	service := NewStoryEpisodeService(store, generator, nil)

	err := service.PrepareToTarget(context.Background())
	if err == nil || !strings.Contains(err.Error(), "run issuer") {
		t.Fatalf("prepare error=%v, want canonical run issuer/owner error", err)
	}
	if len(generator.prompts) != 0 {
		t.Fatalf("generator calls=%d, want 0", len(generator.prompts))
	}
}

func TestStoryRevisionBackfillUsesSameTaskAndFreshRun(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = ""
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{storyRevisionTitleResponse("経費になる鬼ヶ島")}}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetRunIssuer(owner)

	if err := service.BackfillReadyTitles(context.Background()); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: artifact.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].TaskID != runs[1].TaskID || runs[0].Status != domaintask.RunStatusSucceeded || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("runs=%+v", runs)
	}
	if got := service.Snapshot(); got.Ready != 1 || got.Episodes[0].StoryTitle != "経費になる鬼ヶ島" {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestStoryRevisionTitleProgressSurvivesReviewFailureWithoutRegeneratingTitle(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = artifact.Source.Title
	artifact.ProductionStatus = StoryProductionNeedsRepair
	artifact.Validation = StoryValidationResult{Valid: false, Errors: []StoryValidationError{{Code: "title_violation", Field: "story_title"}}}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	reviewErr := errors.New("review unavailable")
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{
		storyRevisionTitleResponse("鬼ヶ島の棚卸し"),
		{err: reviewErr},
	}}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetRunIssuer(owner)

	if err := service.RepairNeedsRepair(context.Background()); err == nil {
		t.Fatal("first repair must retain the review checkpoint")
	}
	checkpointStore := service.generationCheckpointStore()
	checkpoint, ok := checkpointStore.Get(storyGenerationCheckpointKey)
	if !ok || checkpoint.StoryRevision == nil || checkpoint.StoryRevision.Operation != storyRevisionOperationTitle || checkpoint.StoryRevision.Phase != storyRevisionPhaseTitle || checkpoint.StoryArtifact == nil || checkpoint.StoryArtifact.StoryTitle != "鬼ヶ島の棚卸し" {
		t.Fatalf("checkpoint=%+v ok=%t", checkpoint, ok)
	}
	if got := len(generator.prompts); got != 2 {
		t.Fatalf("first prompts=%d, want title and review", got)
	}
	generator.responses = append(generator.responses, storyRevisionReviewResponse(true))
	if err := service.RepairNeedsRepair(context.Background()); err != nil {
		t.Fatalf("review retry: %v", err)
	}
	titleCalls := 0
	for _, prompt := range generator.prompts {
		if strings.Contains(prompt, "完成作品タイトル担当") {
			titleCalls++
		}
	}
	if titleCalls != 1 || len(generator.prompts) != 3 {
		t.Fatalf("prompts=%d titleCalls=%d, want 3/1", len(generator.prompts), titleCalls)
	}
	if got := service.Snapshot(); got.Ready != 1 || got.Episodes[0].StoryTitle != "鬼ヶ島の棚卸し" {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestStoryRevisionSuffixProgressSurvivesReviewFailureWithoutRegeneratingSuffix(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.ProductionStatus = StoryProductionNeedsRepair
	artifact.Validation = StoryValidationResult{Valid: false, FirstInvalidTurn: 5, Errors: []StoryValidationError{{Code: "continuity_violation", TurnIndex: 5}}}
	for i := range artifact.Turns {
		artifact.Turns[i].MessageID = "revision-prefix-" + string(rune('a'+i))
	}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	suffixPayload, err := json.Marshal(struct {
		Turns []StoryEpisodeTurn `json:"turns"`
	}{Turns: artifact.Turns[4:]})
	if err != nil {
		t.Fatal(err)
	}
	reviewErr := errors.New("review unavailable")
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{
		{payload: string(suffixPayload)},
		{err: reviewErr},
	}}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetRunIssuer(owner)

	if err := service.RepairNeedsRepair(context.Background()); err == nil {
		t.Fatal("first suffix repair must retain the review checkpoint")
	}
	checkpoint, ok := service.generationCheckpointStore().Get(storyGenerationCheckpointKey)
	if !ok || checkpoint.StoryRevision == nil || checkpoint.StoryRevision.Operation != storyRevisionOperationSuffix || checkpoint.StoryRevision.Phase != storyRevisionPhaseTitle {
		t.Fatalf("checkpoint=%+v ok=%t", checkpoint, ok)
	}
	if checkpoint.StoryArtifact == nil || len(checkpoint.StoryArtifact.Turns) < 4 {
		t.Fatalf("checkpoint artifact=%+v", checkpoint.StoryArtifact)
	}
	for i := 0; i < 4; i++ {
		if checkpoint.StoryArtifact.Turns[i].MessageID != artifact.Turns[i].MessageID {
			t.Fatalf("prefix message %d changed", i+1)
		}
	}
	generator.responses = append(generator.responses, storyRevisionReviewResponse(true))
	if err := service.RepairNeedsRepair(context.Background()); err != nil {
		t.Fatalf("suffix review retry: %v", err)
	}
	suffixCalls := 0
	for _, prompt := range generator.prompts {
		if strings.Contains(prompt, "suffix修復担当") {
			suffixCalls++
		}
	}
	if suffixCalls != 1 || len(generator.prompts) != 3 {
		t.Fatalf("prompts=%d suffixCalls=%d, want 3/1", len(generator.prompts), suffixCalls)
	}
	if got := service.Snapshot(); got.Ready != 1 {
		t.Fatalf("snapshot=%+v", got)
	}
}

func TestStoryRevisionCompletionRetryDoesNotRegenerateOrDuplicateAppend(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = ""
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	baseOwner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	owner := &storyCompletionFailOwner{Manager: baseOwner, failComplete: true}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{storyRevisionTitleResponse("鬼ヶ島の経費精算")}}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetRunIssuer(owner)

	if err := service.BackfillReadyTitles(context.Background()); err == nil {
		t.Fatal("completion failure must be returned")
	}
	checkpoint, ok := service.generationCheckpointStore().Get(storyGenerationCheckpointKey)
	if !ok || checkpoint.StoryRevision == nil || checkpoint.StoryRevision.Phase != storyRevisionPhaseFinal {
		t.Fatalf("checkpoint=%+v ok=%t", checkpoint, ok)
	}
	if got := service.Snapshot(); got.Ready != 0 || len(got.Episodes) != 0 {
		t.Fatalf("pending revision became visible: %+v", got)
	}
	owner.failComplete = false
	if err := service.BackfillReadyTitles(context.Background()); err != nil {
		t.Fatalf("completion retry: %v", err)
	}
	if len(generator.prompts) != 1 {
		t.Fatalf("generator calls=%d, want 1", len(generator.prompts))
	}
	payload, err := os.ReadFile(store.path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(splitStoryRevisionLines(payload)); got != 2 {
		t.Fatalf("persisted records=%d, want original plus one revision", got)
	}
}

func TestStoryRevisionCleanupRetryDoesNotRegenerateOrDuplicateAppend(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = ""
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	baseOwner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	checkpointPath := filepath.Join(t.TempDir(), "story.checkpoints.json")
	checkpointStore := NewGenerationCheckpointStore(checkpointPath)
	owner := &storyRevisionCleanupFailOwner{
		Manager:         baseOwner,
		checkpointStore: checkpointStore,
		blockedPath:     filepath.Join(t.TempDir(), "checkpoint-path-directory"),
		failCleanup:     true,
	}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{storyRevisionTitleResponse("クリーンアップ再試行")}}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)

	if err := service.BackfillReadyTitles(context.Background()); err == nil {
		t.Fatal("checkpoint cleanup failure must be returned")
	}
	if len(generator.prompts) != 1 {
		t.Fatalf("generator calls=%d, want 1", len(generator.prompts))
	}
	if got := len(splitStoryRevisionLines(func() []byte {
		payload, err := os.ReadFile(store.path)
		if err != nil {
			t.Fatal(err)
		}
		return payload
	}())); got != 2 {
		t.Fatalf("persisted records=%d, want original plus one revision", got)
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); !ok {
		t.Fatal("cleanup failure must retain checkpoint")
	}
	checkpointStore.path = checkpointPath
	if err := os.Remove(owner.blockedPath); err != nil {
		t.Fatal(err)
	}
	if err := service.BackfillReadyTitles(context.Background()); err != nil {
		t.Fatalf("cleanup retry: %v", err)
	}
	if len(generator.prompts) != 1 {
		t.Fatalf("generator calls after cleanup retry=%d, want 1", len(generator.prompts))
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); ok {
		t.Fatal("checkpoint remains after cleanup retry")
	}
}

func TestStoryRevisionRerunPendingAdoptsExistingNewRun(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = ""
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	artifact, _ = store.get(artifact.EpisodeID) // Use the persisted source timestamps.
	checkpointStore := NewGenerationCheckpointStore(store.path + ".checkpoints.json")
	checkpoint := storyRevisionCheckpointFor(artifact, storyRevisionOperationBackfill, storyRevisionPhaseInput, "rerun_pending")
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	newRun, err := rerunIdleChatRun(context.Background(), owner, &checkpoint, checkpointStore)
	if err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{storyRevisionTitleResponse("既存再実行の題")}}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)

	if err := service.PrepareToTarget(context.Background()); err != nil {
		t.Fatalf("recover rerun: %v", err)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: artifact.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[1].RunID != newRun.RunID || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("runs=%+v", runs)
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); ok {
		t.Fatal("rerun checkpoint remains")
	}
}

func TestStoryRevisionPendingOldRevisionIsHidden(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = ""
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	artifact, _ = store.get(artifact.EpisodeID) // Use the persisted source timestamps.
	checkpointStore := NewGenerationCheckpointStore(store.path + ".checkpoints.json")
	checkpoint := storyRevisionCheckpointFor(artifact, storyRevisionOperationBackfill, storyRevisionPhaseInput, "rerun_pending")
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := validateStoryGenerationCheckpoint(checkpoint); err != nil {
		t.Fatalf("valid pending checkpoint rejected: %v", err)
	}
	if _, err := owner.StartRunWithReason(context.Background(), artifact.TaskID, domaintask.RunStartReasonExplicitRerun); err != nil {
		t.Fatal(err)
	}
	service := NewStoryEpisodeService(store, &storyRevisionTestGenerator{}, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	if got := service.Snapshot(); got.Ready != 0 || len(got.Episodes) != 0 {
		t.Fatalf("pending old revision visible: %+v", got)
	}
	if _, ok := service.NextReady(); ok {
		t.Fatal("pending old revision selected")
	}
	if _, ok := service.Episode(artifact.EpisodeID); ok {
		t.Fatal("pending old revision returned by id")
	}
}

func TestStoryRevisionTerminalRunWithoutSavedArtifactFailsClosed(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	newRun, err := owner.StartRunWithReason(context.Background(), artifact.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.CompleteRun(context.Background(), artifact.TaskID, newRun.RunID, "Shiro", domaintask.StatusSucceeded, "terminal without artifact", ""); err != nil {
		t.Fatal(err)
	}
	sourceRunID := artifact.RunID
	artifact.RunID = newRun.RunID
	artifact.Revision++
	checkpoint := storyRevisionCheckpointFor(artifact, storyRevisionOperationTitle, storyRevisionPhaseFinal, "review")
	checkpoint.StoryRevision.SourceRunID = sourceRunID
	checkpoint.StoryReview = &StorySemanticReview{Valid: true}
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	checkpointStore := NewGenerationCheckpointStore(store.path + ".checkpoints.json")
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)

	if err := service.PrepareToTarget(context.Background()); err == nil {
		t.Fatal("terminal run without saved artifact must fail closed")
	} else if !strings.Contains(err.Error(), "no persisted artifact") {
		t.Fatalf("terminal run error=%v, want missing artifact", err)
	}
	if len(generator.prompts) != 0 {
		t.Fatalf("generator calls=%d, want 0", len(generator.prompts))
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); !ok {
		t.Fatal("checkpoint was discarded")
	}
}

func TestStoryRevisionFinalPhaseWithoutReviewFailsClosed(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	sourceRunID := artifact.RunID
	newRun, err := owner.StartRunWithReason(context.Background(), artifact.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	artifact.RunID = newRun.RunID
	artifact.Revision++
	checkpoint := storyRevisionCheckpointFor(artifact, storyRevisionOperationTitle, storyRevisionPhaseFinal, "review")
	checkpoint.StoryRevision.SourceRunID = sourceRunID
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	checkpointStore := NewGenerationCheckpointStore(store.path + ".checkpoints.json")
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	generator := &storyRevisionTestGenerator{}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)

	if err := service.PrepareToTarget(context.Background()); err == nil {
		t.Fatal("final title revision without review must fail closed")
	} else if !strings.Contains(err.Error(), "no semantic review") {
		t.Fatalf("final checkpoint error=%v, want missing semantic review", err)
	}
	if len(generator.prompts) != 0 {
		t.Fatalf("generator calls=%d, want 0", len(generator.prompts))
	}
}

func TestStoryRevisionRerunPendingBoundRunSurvivesReload(t *testing.T) {
	artifact := validStoryEpisodeFixture()
	artifact.StoryTitle = ""
	artifact.ProductionStatus = StoryProductionReady
	artifact.Validation = StoryValidationResult{Valid: true}
	owner, artifact := storyServiceArtifactWithTerminalRun(t, artifact)
	store := newStoryEpisodeStore(filepath.Join(t.TempDir(), "story_episodes.jsonl"), 1)
	if err := store.append(artifact); err != nil {
		t.Fatal(err)
	}
	artifact, _ = store.get(artifact.EpisodeID)
	checkpoint := storyRevisionCheckpointFor(artifact, storyRevisionOperationBackfill, storyRevisionPhaseInput, "rerun_pending")
	checkpointStore := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "story.checkpoints.json"))
	if err := checkpointStore.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	newRun, err := rerunIdleChatRun(context.Background(), owner, &checkpoint, checkpointStore)
	if err != nil {
		t.Fatal(err)
	}
	if err := reconcileGenerationRerun(context.Background(), owner, &checkpoint, checkpointStore); err != nil {
		t.Fatal(err)
	}
	// Crash after the successor binding is durable but before Stage is advanced.
	checkpointStore = NewGenerationCheckpointStore(checkpointStore.path)
	store = newStoryEpisodeStore(store.path, 1)
	generator := &storyRevisionTestGenerator{responses: []storyRevisionGeneratorResponse{storyRevisionTitleResponse("再読込後のタイトル")}}
	service := NewStoryEpisodeService(store, generator, nil)
	service.SetGenerationCheckpointStore(checkpointStore)
	service.SetRunIssuer(owner)
	if err := service.PrepareToTarget(context.Background()); err != nil {
		t.Fatalf("recover bound rerun: %v", err)
	}
	if len(generator.prompts) != 1 {
		t.Fatalf("generator calls=%d, want 1", len(generator.prompts))
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: artifact.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[1].RunID != newRun.RunID || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("runs=%+v", runs)
	}
	if _, ok := checkpointStore.Get(storyGenerationCheckpointKey); ok {
		t.Fatal("bound rerun checkpoint remains")
	}
}

func splitStoryRevisionLines(payload []byte) [][]byte {
	var lines [][]byte
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
