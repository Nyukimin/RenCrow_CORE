package idlechat

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
)

type cancelOnJudgeTopicProvider struct {
	calls        int
	judgeStarted chan struct{}
}

func (p *cancelOnJudgeTopicProvider) Name() string { return "resume-test" }

func (p *cancelOnJudgeTopicProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	if p.calls == 1 {
		return llm.GenerateResponse{Content: topicCandidatesJSON("郵便受けから始まる、町の小さな観察記録", "観察")}, nil
	}
	close(p.judgeStarted)
	<-ctx.Done()
	return llm.GenerateResponse{}, ctx.Err()
}

func TestTopicGeneratorResumesAtJudgeWithoutRegeneratingCandidates(t *testing.T) {
	provider := &cancelOnJudgeTopicProvider{judgeStarted: make(chan struct{})}
	generator := NewTopicGenerator(provider, TopicGenerationConfig{
		Enabled: true, CandidatesPerAttempt: 1, MaxAttempts: 1, JudgeEnabled: true,
		MinJudgeTotal: 24, MinCategoryFit: 4, MinSafety: 4,
	})
	ctx, cancel := context.WithCancel(context.Background())
	var saved TopicGenerationResumeState
	done := make(chan error, 1)
	go func() {
		_, err := generator.GenerateInterestingTopicResumable(ctx, TopicCategorySingle, TopicSeed{
			Category: TopicCategorySingle, Genre1: "郵便",
		}, nil, TopicGenerationResumeState{}, func(state TopicGenerationResumeState) error {
			saved = state
			return nil
		})
		done <- err
	}()
	select {
	case <-provider.judgeStarted:
	case <-time.After(time.Second):
		t.Fatal("judge did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first generation error = %v, want context canceled", err)
	}
	if saved.Attempt != 1 || len(saved.Candidates) != 1 || saved.Result != nil {
		t.Fatalf("saved resume state = %+v", saved)
	}

	judgeProvider := &queuedForecastProvider{responses: []string{topicJudgeJSON(saved.Candidates[0].Topic)}}
	result, err := NewTopicGenerator(judgeProvider, TopicGenerationConfig{
		Enabled: true, CandidatesPerAttempt: 1, MaxAttempts: 1, JudgeEnabled: true,
		MinJudgeTotal: 24, MinCategoryFit: 4, MinSafety: 4,
	}).GenerateInterestingTopicResumable(context.Background(), TopicCategorySingle, TopicSeed{
		Category: TopicCategorySingle, Genre1: "郵便",
	}, nil, saved, nil)
	if err != nil {
		t.Fatalf("resume generation: %v", err)
	}
	if result.Topic != saved.Candidates[0].Topic || judgeProvider.requests != 1 {
		t.Fatalf("result=%+v requests=%d, want judge-only resume", result, judgeProvider.requests)
	}
}

func TestGenerationCheckpointStorePersistsCompletedStage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoints.json")
	store := NewGenerationCheckpointStore(path)
	want := GenerationCheckpoint{
		Key: "word:single", Kind: "word", GenerationID: "generation-1", Stage: "candidates", Attempt: 1,
		Category: TopicCategorySingle, Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "郵便"},
		Candidates: []TopicCandidate{{Topic: "郵便受けから始まる観察", InterestingnessAxis: "観察"}},
	}
	if err := store.Put(want); err != nil {
		t.Fatal(err)
	}
	reloaded := NewGenerationCheckpointStore(path)
	got, ok := reloaded.Get(want.Key)
	if !ok || got.Stage != want.Stage || got.GenerationID != want.GenerationID || len(got.Candidates) != 1 {
		t.Fatalf("reloaded checkpoint = %+v ok=%t", got, ok)
	}
	if err := reloaded.Delete(want.Key); err != nil {
		t.Fatal(err)
	}
	if _, ok := NewGenerationCheckpointStore(path).Get(want.Key); ok {
		t.Fatal("deleted checkpoint was reloaded")
	}
}

func TestConversationActivityCancelsStockGenerationAndDisabledPlaybackStillAllowsRefill(t *testing.T) {
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), nil, 5, 1, 0.7, nil, "")
	orchestrator.disabled = true
	if !orchestrator.forecastTopicRefillAvailable() {
		t.Fatal("disabled playback must not disable background Stock refill")
	}
	if !orchestrator.tryBeginTopicProduction() {
		t.Fatal("failed to start Stock generation")
	}
	ctx := orchestrator.topicProductionContext()
	orchestrator.NotifyActivity()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("conversation activity did not cancel Stock generation")
	}
	orchestrator.endTopicProduction()
}

type artifactThenBlockStoryGenerator struct {
	artifact      string
	reviewStarted chan struct{}
	calls         int
}

func (g *artifactThenBlockStoryGenerator) Generate(ctx context.Context, _ string) (string, error) {
	g.calls++
	if g.calls == 1 {
		return g.artifact, nil
	}
	close(g.reviewStarted)
	<-ctx.Done()
	return "", ctx.Err()
}

func TestStoryPreparationResumesAtSemanticReview(t *testing.T) {
	dir := t.TempDir()
	fixture := validStoryEpisodeFixture()
	fixture.EpisodeID = ""
	fixture.GenerationID = ""
	payload, err := json.Marshal(fixture)
	if err != nil {
		t.Fatal(err)
	}
	checkpoints := NewGenerationCheckpointStore(filepath.Join(dir, "checkpoints.json"))
	storePath := filepath.Join(dir, "episodes.jsonl")
	firstGenerator := &artifactThenBlockStoryGenerator{artifact: string(payload), reviewStarted: make(chan struct{})}
	first := NewPersistentStoryEpisodeService(storePath, 1, firstGenerator, nil)
	first.maxAttempts = 1
	first.SetGenerationCheckpointStore(checkpoints)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- first.PrepareToTarget(ctx) }()
	select {
	case <-firstGenerator.reviewStarted:
	case <-time.After(time.Second):
		t.Fatal("semantic review did not start")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first prepare error = %v", err)
	}
	checkpoint, ok := checkpoints.Get("story:prepare")
	if !ok || checkpoint.Stage != "artifact" || checkpoint.StoryArtifact == nil {
		t.Fatalf("story checkpoint = %+v ok=%t", checkpoint, ok)
	}

	reviewJSON, _ := json.Marshal(StorySemanticReview{Valid: true})
	secondGenerator := &queuedStoryCodexGenerator{responses: []string{string(reviewJSON)}}
	second := NewPersistentStoryEpisodeService(storePath, 1, secondGenerator, nil)
	second.maxAttempts = 1
	second.SetGenerationCheckpointStore(checkpoints)
	if err := second.PrepareToTarget(context.Background()); err != nil {
		t.Fatalf("resumed prepare: %v", err)
	}
	if len(secondGenerator.prompts) != 1 || second.Snapshot().Ready != 1 {
		t.Fatalf("resume prompts=%d snapshot=%+v", len(secondGenerator.prompts), second.Snapshot())
	}
	if _, ok := checkpoints.Get("story:prepare"); ok {
		t.Fatal("story checkpoint was not cleared after append")
	}
}
