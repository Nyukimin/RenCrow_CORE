package idlechat

import (
	"context"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestGenerationCheckpointSHA256IgnoresOnlyUpdatedAt(t *testing.T) {
	checkpoint := GenerationCheckpoint{
		Key: "word:single", Kind: "word", TaskID: modulecore.NewTaskID(), RunID: modulecore.NewRunID(),
		Stage: "candidates", Attempt: 2, Category: TopicCategorySingle,
		Seed:       TopicSeed{Category: TopicCategorySingle, Genre1: "科学", TrendKeywords: []string{"保存済み"}},
		Recent:     []RecentTopic{{Topic: "既出", Category: TopicCategorySingle}},
		Candidates: []TopicCandidate{{Topic: "候補", InterestingnessAxis: "軸"}},
		Result:     &TopicGenerationResult{Topic: "確定", Category: TopicCategorySingle, Candidates: []TopicCandidate{{Topic: "確定", InterestingnessAxis: "別軸"}}},
		UpdatedAt:  time.Date(2026, 9, 8, 1, 2, 3, 0, time.UTC),
	}
	first, err := generationCheckpointSHA256(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.UpdatedAt = time.Date(2026, 9, 8, 4, 5, 6, 0, time.UTC)
	second, err := generationCheckpointSHA256(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("UpdatedAt changed checkpoint digest: first=%s second=%s", first, second)
	}
	checkpoint.Stage = "result"
	third, err := generationCheckpointSHA256(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("checkpoint content change did not change digest")
	}
}

func TestResumeIdleChatRunUsesExactPersistedCheckpointAndAssignee(t *testing.T) {
	ctx := context.Background()
	owner := newTestIdleChatRunIssuer(t)
	taskID, predecessorID, err := issueIdleChatRun(ctx, owner, "checkpoint successor", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := completeGenerationRun(ctx, owner, taskID, predecessorID, domaintask.StatusWaiting, "paused", "retry"); err != nil {
		t.Fatal(err)
	}
	store := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	checkpoint := GenerationCheckpoint{
		Key: "story:prepare", Kind: "story", TaskID: taskID, RunID: predecessorID, Stage: "resume_pending",
		StoryArtifact: &StoryEpisodeArtifact{TaskID: taskID, RunID: predecessorID, Revision: 1},
	}
	if err := store.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	wantDigest, err := generationCheckpointSHA256(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	issued, err := resumeIdleChatRun(ctx, owner, &checkpoint, store)
	if err != nil {
		t.Fatal(err)
	}
	if issued.TaskID != taskID || issued.RunID == predecessorID || issued.Assignee != "Shiro" || issued.StartReason != domaintask.RunStartReasonCheckpointResume || issued.StartCheckpointSHA256 != wantDigest {
		t.Fatalf("issued successor = %#v, want exact task, actor, reason, and digest", issued)
	}
	if got, err := owner.GetRun(ctx, issued.RunID); err != nil || got.StartCheckpointSHA256 != wantDigest {
		t.Fatalf("persisted successor = %#v err=%v, want digest %s", got, err, wantDigest)
	}
}

func TestGenerationResumeRejectsUnrelatedSuccessorDigestWithoutMutation(t *testing.T) {
	ctx := context.Background()
	owner := newTestIdleChatRunIssuer(t)
	taskID, predecessorID, err := issueIdleChatRun(ctx, owner, "checkpoint adoption", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := completeGenerationRun(ctx, owner, taskID, predecessorID, domaintask.StatusWaiting, "paused", "retry"); err != nil {
		t.Fatal(err)
	}
	store := NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "checkpoints.json"))
	checkpoint := GenerationCheckpoint{
		Key: "word:single", Kind: "word", TaskID: taskID, RunID: predecessorID, Stage: "resume_pending",
		Category: TopicCategorySingle, Seed: TopicSeed{Category: TopicCategorySingle, Genre1: "科学"},
	}
	if err := store.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.StartRunWithReason(ctx, taskID, domaintask.RunStartReasonExplicitRerun); err != nil {
		t.Fatal(err)
	}
	before := cloneGenerationCheckpoint(checkpoint)
	beforeStored, ok := store.Get(checkpoint.Key)
	if !ok {
		t.Fatal("checkpoint was not persisted")
	}
	if err := reconcileGenerationResume(ctx, owner, &checkpoint, store); err == nil {
		t.Fatal("unrelated explicit rerun was adopted")
	} else if !containsGenerationCheckpointError(err, "start reason") && !containsGenerationCheckpointError(err, "checkpoint digest") {
		t.Fatalf("unrelated successor error = %v, want reason or digest rejection", err)
	}
	if !reflect.DeepEqual(checkpoint, before) {
		t.Fatalf("caller checkpoint mutated after rejection: before=%#v after=%#v", before, checkpoint)
	}
	afterStored, ok := store.Get(checkpoint.Key)
	if !ok || !reflect.DeepEqual(afterStored, beforeStored) {
		t.Fatalf("persisted checkpoint mutated after rejection: before=%#v after=%#v", beforeStored, afterStored)
	}
}

type concurrentForecastCallProvider struct {
	response string
	entered  *atomic.Int32
	release  <-chan struct{}
	calls    atomic.Int32
}

func (p *concurrentForecastCallProvider) Generate(ctx context.Context, _ llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls.Add(1)
	p.entered.Add(1)
	select {
	case <-p.release:
		return llm.GenerateResponse{Content: p.response}, nil
	case <-ctx.Done():
		return llm.GenerateResponse{}, ctx.Err()
	}
}

func (p *concurrentForecastCallProvider) Name() string { return "call-local-forecast" }

func TestGenerationForecastCallLocalProvidersRemainBoundToConcurrentRuns(t *testing.T) {
	ctx := context.Background()
	owner1 := newTestIdleChatRunIssuer(t)
	owner2 := newTestIdleChatRunIssuer(t)
	issuer1 := idlechatRunIssuer(owner1)
	issuer2 := idlechatRunIssuer(owner2)
	o := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"shiro"}, 60, 1, 0.7, nil, "")
	o.SetRunIssuer(issuer1)
	o.SetGenerationCheckpointStore(NewGenerationCheckpointStore(filepath.Join(t.TempDir(), "forecast.checkpoints.json")))
	o.SetTopicGenerationConfig(TopicGenerationConfig{CandidatesPerAttempt: 1, MaxAttempts: 1, JudgeEnabled: false})

	task1, run1, err := issueIdleChatRun(ctx, issuer1, "forecast call one", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	task2, run2, err := issueIdleChatRun(ctx, issuer2, "forecast call two", "shiro", domaintask.RunStartReasonFirst, "")
	if err != nil {
		t.Fatal(err)
	}
	store := o.generationCheckpointStore()
	domain := ForecastDomain{Name: "AI技術"}
	checkpoint1 := GenerationCheckpoint{
		Key: "forecast:call-one", Kind: "forecast", TaskID: task1, RunID: run1, Stage: "seeds",
		Category: TopicCategoryForecast, Domain: domain, ForecastSeeds: []string{"seed-one"}, Recent: []RecentTopic{{Topic: "既存のお題一"}},
		Seed: TopicSeed{Category: TopicCategoryForecast, ForecastDomain: domain.Name, TrendKeywords: []string{"seed-one"}},
	}
	checkpoint2 := checkpoint1
	checkpoint2.Key = "forecast:call-two"
	checkpoint2.TaskID = task2
	checkpoint2.RunID = run2
	checkpoint2.Recent = []RecentTopic{{Topic: "既存のお題二"}}
	if err := store.Put(checkpoint1); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(checkpoint2); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	var entered atomic.Int32
	provider1 := &concurrentForecastCallProvider{response: topicCandidatesJSON("AI技術が個人の記憶整理をどう変えるか", "変化の分岐"), entered: &entered, release: release}
	provider2 := &concurrentForecastCallProvider{response: topicCandidatesJSON("AI技術が地域医療の判断をどう変えるか", "変化の分岐"), entered: &entered, release: release}
	guarded1 := newRunGuardedLLMProvider(issuer1, task1, run1, "Shiro", provider1)
	guarded2 := newRunGuardedLLMProvider(issuer2, task2, run2, "Shiro", provider2)

	type result struct {
		topic string
		err   *forecastTopicFailure
	}
	results := make(chan result, 2)
	go func() {
		topic, failure := o.generateForecastTopicWithCheckpoint(ctx, domain, &checkpoint1, guarded1, "run-one")
		results <- result{topic: topic, err: failure}
	}()
	go func() {
		topic, failure := o.generateForecastTopicWithCheckpoint(ctx, domain, &checkpoint2, guarded2, "run-two")
		results <- result{topic: topic, err: failure}
	}()
	deadline := time.Now().Add(2 * time.Second)
	for entered.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if entered.Load() != 2 {
		close(release)
		t.Fatalf("both concurrent forecast providers did not enter: %d", entered.Load())
	}
	close(release)
	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("concurrent call-local generation errors: first=%+v second=%+v", first.err, second.err)
	}
	if (first.topic != "AI技術が個人の記憶整理をどう変えるか" && first.topic != "AI技術が地域医療の判断をどう変えるか") || (second.topic != "AI技術が個人の記憶整理をどう変えるか" && second.topic != "AI技術が地域医療の判断をどう変えるか") || first.topic == second.topic {
		t.Fatalf("concurrent call-local topics crossed providers: first=%q second=%q", first.topic, second.topic)
	}
	if provider1.calls.Load() != 1 || provider2.calls.Load() != 1 {
		t.Fatalf("provider calls = %d/%d, want one call per Run", provider1.calls.Load(), provider2.calls.Load())
	}
}

func containsGenerationCheckpointError(err error, want string) bool {
	return err != nil && strings.Contains(err.Error(), want)
}
