package idlechat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type wordTopicRecoveryOwner struct {
	*taskmanager.Manager
	startCalls       int
	startReasons     []domaintask.RunStartReason
	completeCalls    int
	failCompleteOnce error
	afterStart       func(domaintask.Run)
	afterComplete    func()
}

func (o *wordTopicRecoveryOwner) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	o.startCalls++
	run, err := o.Manager.StartRunWithReason(ctx, taskID, reason)
	if err == nil {
		o.startReasons = append(o.startReasons, reason)
		if o.afterStart != nil {
			o.afterStart(run)
		}
	}
	return run, err
}

func (o *wordTopicRecoveryOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	o.completeCalls++
	if o.failCompleteOnce != nil {
		err := o.failCompleteOnce
		o.failCompleteOnce = nil
		return domaintask.Task{}, err
	}
	task, err := o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
	if err == nil && o.afterComplete != nil {
		o.afterComplete()
	}
	return task, err
}

type wordTopicRecoveryFixture struct {
	owner          *wordTopicRecoveryOwner
	orchestrator   *IdleChatOrchestrator
	stock          *wordTopicStock
	checkpoints    *GenerationCheckpointStore
	stockPath      string
	checkpointPath string
	generator      *savedWordTopicGenerator
}

func newWordTopicRecoveryFixture(t *testing.T) *wordTopicRecoveryFixture {
	t.Helper()
	root := t.TempDir()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	owner := &wordTopicRecoveryOwner{Manager: manager}
	stockPath := filepath.Join(root, "word_topic_stock.json")
	checkpointPath := filepath.Join(root, "generation_checkpoints.json")
	stock := newWordTopicStock(stockPath)
	checkpoints := NewGenerationCheckpointStore(checkpointPath)
	generator := &savedWordTopicGenerator{}
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"shiro"}, 60, 1, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetGenerationCheckpointStore(checkpoints)
	orchestrator.SetTopicCodexGenerator(generator)
	orchestrator.mu.Lock()
	orchestrator.wordTopicStock = stock
	orchestrator.mu.Unlock()
	t.Cleanup(func() {
		if orchestrator.cancel != nil {
			orchestrator.cancel()
		}
		if err := manager.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	return &wordTopicRecoveryFixture{
		owner: owner, orchestrator: orchestrator, stock: stock, checkpoints: checkpoints,
		stockPath: stockPath, checkpointPath: checkpointPath, generator: generator,
	}
}

func (f *wordTopicRecoveryFixture) seedWaitingCheckpoint(t *testing.T) (domaintask.Task, domaintask.Run, GenerationCheckpoint) {
	return f.seedWaitingCheckpointWithResult(t, wordTopicRecoveryResult())
}

func (f *wordTopicRecoveryFixture) seedWaitingCheckpointWithResult(t *testing.T, result *TopicGenerationResult) (domaintask.Task, domaintask.Run, GenerationCheckpoint) {
	t.Helper()
	ctx := context.Background()
	task, err := f.owner.Create(ctx, domaintask.Task{
		Title:    "IdleChat word topic recovery",
		Route:    domaintask.RouteGeneral,
		Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := f.owner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}
	if _, err := f.owner.CompleteRun(ctx, task.TaskID, run.RunID, "Shiro", domaintask.StatusWaiting, "paused", "retry from saved generation checkpoint"); err != nil {
		t.Fatalf("wait first run: %v", err)
	}
	seed := wordTopicRecoveryResult().Seed
	if result != nil {
		seed = result.Seed
	}
	checkpoint := GenerationCheckpoint{
		Key: "word:" + string(TopicCategorySingle), Kind: "word", TaskID: task.TaskID, RunID: run.RunID,
		Stage: "result", Attempt: 1, Category: TopicCategorySingle,
		Seed: seed, Result: result,
	}
	if result == nil {
		checkpoint.Stage = "seed"
	}
	if err := f.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("seed generation checkpoint: %v", err)
	}
	return task, run, checkpoint
}

func (f *wordTopicRecoveryFixture) reloadPersistentState(t *testing.T) {
	t.Helper()
	if f.checkpoints.path != f.checkpointPath {
		f.checkpoints.path = f.checkpointPath
	}
	f.checkpoints = NewGenerationCheckpointStore(f.checkpointPath)
	f.stock = newWordTopicStock(f.stockPath)
	f.orchestrator.SetGenerationCheckpointStore(f.checkpoints)
	f.orchestrator.mu.Lock()
	f.orchestrator.wordTopicStock = f.stock
	f.orchestrator.mu.Unlock()
}

func wordTopicRecoveryResult() *TopicGenerationResult {
	const topic = "生成AIを店頭端末に入れるとき誰が最後の判断を持つか"
	seed := TopicSeed{Category: TopicCategorySingle, Genre1: "生成AI", Genre1Kind: topicWordKindStatic}
	candidate := TopicCandidate{Topic: topic, InterestingnessAxis: "観察", OpeningHook: "店頭の選択", Avoid: "抽象的なAI論"}
	return &TopicGenerationResult{
		Topic: topic, Category: TopicCategorySingle, Strategy: string(StrategySingleGenre),
		InterestingnessAxis: "観察", OpeningHook: candidate.OpeningHook, Avoid: candidate.Avoid,
		Seed: seed, Candidates: []TopicCandidate{candidate}, Provider: "CodexExe", Initiator: "shiro",
	}
}

type savedWordTopicGenerator struct{ calls int }

func (g *savedWordTopicGenerator) Generate(context.Context, string) (string, error) {
	g.calls++
	return "", errors.New("saved checkpoint must not call CodexExe")
}

func requireWordTopicRuns(t *testing.T, owner *wordTopicRecoveryOwner, taskID modulecore.TaskID) []domaintask.Run {
	t.Helper()
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("list task runs: %v", err)
	}
	return runs
}

func findWordTopicRun(t *testing.T, runs []domaintask.Run, runID modulecore.RunID) domaintask.Run {
	t.Helper()
	for _, run := range runs {
		if run.RunID == runID {
			return run
		}
	}
	t.Fatalf("run %s was not found in %#v", runID, runs)
	return domaintask.Run{}
}

func requireWordTopicCheckpoint(t *testing.T, store *GenerationCheckpointStore) GenerationCheckpoint {
	t.Helper()
	checkpoint, ok := store.Get("word:" + string(TopicCategorySingle))
	if !ok {
		t.Fatal("word checkpoint is missing")
	}
	return checkpoint
}

func TestProduceWordTopicRejectsMissingPersistenceConfiguration(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	for _, test := range []struct {
		name  string
		setup func()
	}{
		{name: "stock path", setup: func() { fixture.stock.path = "" }},
		{name: "checkpoint path", setup: func() { fixture.checkpoints.path = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture.stock.path = fixture.stockPath
			fixture.checkpoints.path = fixture.checkpointPath
			test.setup()
			starts := fixture.owner.startCalls
			err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
			if err == nil || !strings.Contains(err.Error(), "persistence") {
				t.Fatalf("produceWordTopic() error = %v, want persistence configuration error", err)
			}
			if fixture.owner.startCalls != starts {
				t.Fatalf("start calls = %d, want %d", fixture.owner.startCalls, starts)
			}
		})
	}
}

func TestProduceWordTopicResumesSavedResultWithRealTaskManager(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, oldRun, _ := fixture.seedWaitingCheckpoint(t)

	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err != nil {
		t.Fatalf("produceWordTopic() = %v", err)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("CodexExe calls = %d, want 0", fixture.generator.calls)
	}
	if fixture.stock.count(TopicCategorySingle) != 1 {
		t.Fatalf("stock count = %d, want 1", fixture.stock.count(TopicCategorySingle))
	}
	if _, ok := fixture.checkpoints.Get("word:" + string(TopicCategorySingle)); ok {
		t.Fatal("completed checkpoint was not deleted")
	}
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 {
		t.Fatalf("runs = %d, want first waiting plus one resumed run", len(runs))
	}
	if findWordTopicRun(t, runs, oldRun.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("old run = %#v, want waiting", oldRun)
	}
	resumed := runs[1]
	if resumed.StartReason != domaintask.RunStartReasonCheckpointResume || resumed.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("resumed run = %#v", resumed)
	}
	updated, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domaintask.StatusSucceeded {
		t.Fatalf("task status = %s, want succeeded", updated.Status)
	}
	playable := fixture.orchestrator.availableTopicStockPlaybackItems()
	if len(playable) != 1 || playable[0].ID != wordPlaybackID(resumed.RunID) {
		t.Fatalf("playable items = %#v", playable)
	}
}

func TestProduceWordTopicKeepsCheckpointAndRunWaitingWhenStockSaveFails(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, oldRun, _ := fixture.seedWaitingCheckpoint(t)
	badStockPath := filepath.Join(t.TempDir(), "stock-directory")
	if err := os.Mkdir(badStockPath, 0o755); err != nil {
		t.Fatalf("create bad stock path: %v", err)
	}
	fixture.stock.path = badStockPath

	err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "stock") {
		t.Fatalf("produceWordTopic() error = %v, want stock save failure", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 0 {
		t.Fatalf("generator calls=%d stock total=%d, want 0/0", fixture.generator.calls, fixture.stock.total())
	}
	checkpoint := requireWordTopicCheckpoint(t, fixture.checkpoints)
	if checkpoint.RunID == oldRun.RunID || checkpoint.Result == nil {
		t.Fatalf("checkpoint after stock failure = %#v, want resumed run and saved result", checkpoint)
	}
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || findWordTopicRun(t, runs, oldRun.RunID).Status != domaintask.RunStatusWaiting || findWordTopicRun(t, runs, checkpoint.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after stock failure = %#v", runs)
	}
	updated, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domaintask.StatusWaiting {
		t.Fatalf("task status = %s, want waiting", updated.Status)
	}
	pending, err := fixture.orchestrator.wordTopicPublicationPending(TopicCategorySingle, checkpoint.RunID)
	if err != nil || !pending {
		t.Fatalf("publication pending = %v, err=%v; want true", pending, err)
	}
	other, err := fixture.orchestrator.wordTopicPublicationPending(TopicCategoryDouble, checkpoint.RunID)
	if err != nil || other {
		t.Fatalf("different-category publication pending = %v, err=%v; want false", other, err)
	}
}

func TestProduceWordTopicRetriesAfterCompletionFailureWithoutStartingAnotherRun(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, _, _ := fixture.seedWaitingCheckpoint(t)
	fixture.owner.failCompleteOnce = errors.New("injected CompleteRun failure")

	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err == nil {
		t.Fatal("first produce must report CompleteRun failure")
	}
	checkpoint := requireWordTopicCheckpoint(t, fixture.checkpoints)
	if fixture.stock.total() != 1 || fixture.generator.calls != 0 {
		t.Fatalf("after failed completion: stock=%d generator=%d", fixture.stock.total(), fixture.generator.calls)
	}
	if pending, err := fixture.orchestrator.wordTopicPublicationPending(TopicCategorySingle, checkpoint.RunID); err != nil || !pending {
		t.Fatalf("pending after failed completion = %v, err=%v", pending, err)
	}
	if got := fixture.orchestrator.availableTopicStockPlaybackItems(); len(got) != 0 {
		t.Fatalf("pending item was exposed to playback: %#v", got)
	}
	if _, err := fixture.orchestrator.takeWordTopic(StrategySingleGenre); err == nil || !strings.Contains(err.Error(), "pending") {
		t.Fatalf("pending item playback error = %v", err)
	}
	fixture.reloadPersistentState(t)
	starts := fixture.owner.startCalls
	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err != nil {
		t.Fatalf("retry produceWordTopic() = %v", err)
	}
	if fixture.owner.startCalls != starts {
		t.Fatalf("retry started another Run: calls=%d want=%d", fixture.owner.startCalls, starts)
	}
	if fixture.stock.total() != 1 || fixture.generator.calls != 0 {
		t.Fatalf("after retry: stock=%d generator=%d", fixture.stock.total(), fixture.generator.calls)
	}
	if _, ok := fixture.checkpoints.Get("word:" + string(TopicCategorySingle)); ok {
		t.Fatal("checkpoint remained after successful retry")
	}
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("runs after retry = %#v", runs)
	}
}

func TestProduceWordTopicReconcilesSuccessorAfterCheckpointPutFailure(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, oldRun, _ := fixture.seedWaitingCheckpoint(t)
	badCheckpointPath := filepath.Join(t.TempDir(), "checkpoint-directory")
	if err := os.Mkdir(badCheckpointPath, 0o755); err != nil {
		t.Fatalf("create bad checkpoint path: %v", err)
	}
	fixture.owner.afterStart = func(run domaintask.Run) {
		if run.StartReason == domaintask.RunStartReasonCheckpointResume {
			fixture.checkpoints.path = badCheckpointPath
		}
	}

	err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "generation checkpoints") {
		t.Fatalf("produceWordTopic() error = %v, want checkpoint Put failure", err)
	}
	checkpoint := requireWordTopicCheckpoint(t, fixture.checkpoints)
	if checkpoint.RunID != oldRun.RunID || checkpoint.Stage != "resume_pending" {
		t.Fatalf("checkpoint after successor Put failure = %#v, want old Run and resume_pending", checkpoint)
	}
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 {
		t.Fatalf("runs after successor Put failure = %d, want 2", len(runs))
	}
	if findWordTopicRun(t, runs, oldRun.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("old run after successor Put failure = %#v", oldRun)
	}
	var failedSuccessor domaintask.Run
	for _, run := range runs {
		if run.RunID != oldRun.RunID {
			failedSuccessor = run
		}
	}
	if failedSuccessor.StartReason != domaintask.RunStartReasonCheckpointResume || failedSuccessor.Status != domaintask.RunStatusWaiting {
		t.Fatalf("failed successor = %#v", failedSuccessor)
	}

	fixture.checkpoints.path = fixture.checkpointPath
	fixture.owner.afterStart = nil
	fixture.reloadPersistentState(t)
	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err != nil {
		t.Fatalf("reconciled produceWordTopic() = %v", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 1 {
		t.Fatalf("reconciled result: generator=%d stock=%d", fixture.generator.calls, fixture.stock.total())
	}
	if fixture.owner.startCalls != 3 {
		t.Fatalf("start calls = %d, want first + failed successor + repaired successor", fixture.owner.startCalls)
	}
	if _, ok := fixture.checkpoints.Get("word:" + string(TopicCategorySingle)); ok {
		t.Fatal("checkpoint remained after repaired resume")
	}
	runs = requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 3 {
		t.Fatalf("runs after repaired resume = %d, want 3", len(runs))
	}
	if runs[0].RunID != oldRun.RunID || runs[0].Status != domaintask.RunStatusWaiting || runs[1].RunID == oldRun.RunID || runs[1].Status != domaintask.RunStatusWaiting || runs[2].RunID == oldRun.RunID || runs[2].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("run history after repaired resume = %#v", runs)
	}
}

func TestReserveWordTopicProductionRecoversFullCategoryWithCheckpoint(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	_, _, _ = fixture.seedWaitingCheckpoint(t)
	entries := []struct{ seed, topic string }{
		{"郵便", "郵便受けの鍵を誰が預かるか"},
		{"防災", "防災倉庫の備品を誰が決めるか"},
		{"植物", "植物標本の名前を誰が残すか"},
		{"自転車", "自転車置き場の順番を誰が見守るか"},
		{"映画館", "映画館の座席を誰が選び直すか"},
		{"天気", "天気予報の掲示を誰が更新するか"},
		{"台所", "台所の道具を誰が受け継ぐか"},
		{"標本箱", "標本箱のラベルを誰が読み返すか"},
		{"商店街", "商店街の看板を誰が直すか"},
		{"図書館", "図書館の返却棚を誰が整えるか"},
		{"録音", "録音テープの声を誰が記録するか"},
		{"温室", "温室の窓を誰が開けるか"},
	}
	for i, entry := range entries {
		taskID, runID := testIdleChatRunIdentityPair()
		item := WordPreparedTopic{
			Category: TopicCategorySingle, Topic: entry.topic,
			Seed: TopicSeed{Category: TopicCategorySingle, Genre1: entry.seed}, Axis: "観察",
			TaskID: taskID, RunID: runID,
		}
		if added, err := fixture.stock.push(item); err != nil || !added {
			t.Fatalf("stock seed %d added=%v err=%v", i, added, err)
		}
	}
	if fixture.stock.count(TopicCategorySingle) != wordTopicStockCapacityPerCategory {
		t.Fatalf("single stock count = %d, want %d", fixture.stock.count(TopicCategorySingle), wordTopicStockCapacityPerCategory)
	}
	category, ok := fixture.orchestrator.reserveWordTopicProduction(fixture.stock, "recovery")
	if !ok || category != TopicCategorySingle {
		t.Fatalf("reserveWordTopicProduction() = %q, %v; want single,true", category, ok)
	}
	if !fixture.stock.anyFilling() {
		t.Fatal("recovery reservation did not mark category as filling")
	}
	fixture.stock.done(category, nil)
}

func TestProduceWordTopicAfterCheckpointCleanupFailureKeepsDurableStock(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, _, _ := fixture.seedWaitingCheckpoint(t)
	badCheckpointPath := filepath.Join(t.TempDir(), "cleanup-directory")
	if err := os.Mkdir(badCheckpointPath, 0o755); err != nil {
		t.Fatalf("create cleanup failure path: %v", err)
	}
	fixture.owner.afterComplete = func() {
		fixture.checkpoints.path = badCheckpointPath
	}

	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err == nil || !strings.Contains(err.Error(), "generation checkpoints") {
		t.Fatalf("produceWordTopic() error = %v, want cleanup failure", err)
	}
	checkpoint := requireWordTopicCheckpoint(t, fixture.checkpoints)
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if fixture.stock.total() != 1 || len(runs) != 2 || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("after cleanup failure stock=%d runs=%#v", fixture.stock.total(), runs)
	}
	if pending, err := fixture.orchestrator.wordTopicPublicationPending(TopicCategorySingle, checkpoint.RunID); err != nil || !pending {
		t.Fatalf("pending after cleanup failure = %v, err=%v", pending, err)
	}
	fixture.checkpoints.path = fixture.checkpointPath
	fixture.owner.afterComplete = nil
	fixture.reloadPersistentState(t)
	starts := fixture.owner.startCalls
	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err != nil {
		t.Fatalf("cleanup retry = %v", err)
	}
	if fixture.owner.startCalls != starts || fixture.generator.calls != 0 || fixture.stock.total() != 1 {
		t.Fatalf("cleanup retry start=%d/%d generator=%d stock=%d", fixture.owner.startCalls, starts, fixture.generator.calls, fixture.stock.total())
	}
	if _, ok := fixture.checkpoints.Get("word:" + string(TopicCategorySingle)); ok {
		t.Fatal("checkpoint remained after cleanup retry")
	}
}

func TestProduceWordTopicKeepsResultlessCheckpointAndWaitsAfterGenerationError(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	fixture.orchestrator.topicGenerationConfig.MaxAttempts = 1
	task, oldRun, _ := fixture.seedWaitingCheckpointWithResult(t, nil)

	err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "saved checkpoint must not call CodexExe") {
		t.Fatalf("produceWordTopic() error = %v, want generator error", err)
	}
	if fixture.generator.calls != 1 || fixture.stock.total() != 0 {
		t.Fatalf("generator calls=%d stock=%d, want 1/0", fixture.generator.calls, fixture.stock.total())
	}
	checkpoint := requireWordTopicCheckpoint(t, fixture.checkpoints)
	if checkpoint.Result != nil || checkpoint.RunID == oldRun.RunID || checkpoint.Stage != "seed" {
		t.Fatalf("resultless checkpoint after generation error = %#v", checkpoint)
	}
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || findWordTopicRun(t, runs, oldRun.RunID).Status != domaintask.RunStatusWaiting || findWordTopicRun(t, runs, checkpoint.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after resultless generation error = %#v", runs)
	}
	updated, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Status != domaintask.StatusWaiting {
		t.Fatalf("task status = %s, want waiting", updated.Status)
	}
	reloaded := NewGenerationCheckpointStore(fixture.checkpointPath)
	persisted, ok := reloaded.Get("word:" + string(TopicCategorySingle))
	if !ok || persisted.Result != nil || persisted.RunID != checkpoint.RunID {
		t.Fatalf("persisted resultless checkpoint = %#v ok=%v", persisted, ok)
	}
}

func TestProduceWordTopicRejectsAmbiguousResumeSuccessors(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, oldRun, checkpoint := fixture.seedWaitingCheckpoint(t)
	checkpoint.Stage = "resume_pending"
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		run, err := fixture.owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonCheckpointResume)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.owner.CompleteRun(context.Background(), task.TaskID, run.RunID, "Shiro", domaintask.StatusWaiting, "paused", "resume later"); err != nil {
			t.Fatal(err)
		}
	}
	starts, completions := fixture.owner.startCalls, fixture.owner.completeCalls
	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("multiple successors must be rejected: %v", err)
	}
	if fixture.owner.startCalls != starts || fixture.owner.completeCalls != completions || fixture.generator.calls != 0 || fixture.stock.total() != 0 {
		t.Fatal("ambiguous recovery changed execution or stock")
	}
	if got := requireWordTopicCheckpoint(t, fixture.checkpoints); got.RunID != oldRun.RunID || got.Stage != "resume_pending" {
		t.Fatalf("ambiguous recovery changed checkpoint: %#v", got)
	}
}

func TestProduceWordTopicRejectsCompletedRunWithoutSavedArtifact(t *testing.T) {
	f := newWordTopicRecoveryFixture(t)
	f.seedWaitingCheckpoint(t)
	f.owner.afterComplete = func() { f.checkpoints.path = t.TempDir() }
	if err := f.orchestrator.produceWordTopic(f.stock, TopicCategorySingle); err == nil {
		t.Fatal("expected cleanup failure")
	}
	f.owner.afterComplete = nil
	f.reloadPersistentState(t)
	cp := requireWordTopicCheckpoint(t, f.checkpoints)
	if _, err := f.stock.takeByRunID(cp.RunID); err != nil {
		t.Fatal(err)
	}
	starts := f.owner.startCalls
	if err := f.orchestrator.produceWordTopic(f.stock, TopicCategorySingle); err == nil {
		t.Fatal("missing completed artifact accepted")
	}
	if _, ok := f.checkpoints.Get(cp.Key); !ok {
		t.Fatal("missing artifact evidence was erased")
	}
	if f.owner.startCalls != starts || f.generator.calls != 0 {
		t.Fatal("missing artifact caused regeneration")
	}
}
