package idlechat

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	taskmanager "github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/session"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type forecastTopicRecoveryOwner struct {
	*taskmanager.Manager
	startCalls       int
	completeCalls    int
	failCompleteOnce error
}

func (o *forecastTopicRecoveryOwner) StartRunWithReason(ctx context.Context, taskID modulecore.TaskID, reason domaintask.RunStartReason) (domaintask.Run, error) {
	o.startCalls++
	return o.Manager.StartRunWithReason(ctx, taskID, reason)
}

func (o *forecastTopicRecoveryOwner) CompleteRun(ctx context.Context, taskID modulecore.TaskID, runID modulecore.RunID, actorID string, status domaintask.Status, summary, waitingReason string) (domaintask.Task, error) {
	o.completeCalls++
	if o.failCompleteOnce != nil {
		err := o.failCompleteOnce
		o.failCompleteOnce = nil
		return domaintask.Task{}, err
	}
	return o.Manager.CompleteRun(ctx, taskID, runID, actorID, status, summary, waitingReason)
}

type forecastTopicRecoveryFixture struct {
	owner          *forecastTopicRecoveryOwner
	orchestrator   *IdleChatOrchestrator
	stock          *forecastTopicStock
	checkpoints    *GenerationCheckpointStore
	stockPath      string
	checkpointPath string
	generator      *savedForecastTopicGenerator
	domain         ForecastDomain
}

func newForecastTopicRecoveryFixture(t *testing.T) *forecastTopicRecoveryFixture {
	t.Helper()
	root := t.TempDir()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatalf("open task store: %v", err)
	}
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	owner := &forecastTopicRecoveryOwner{Manager: manager}
	stockPath := filepath.Join(root, "forecast_topic_stock.json")
	checkpointPath := filepath.Join(root, "generation_checkpoints.json")
	stock := newForecastTopicStock(stockPath)
	checkpoints := NewGenerationCheckpointStore(checkpointPath)
	generator := &savedForecastTopicGenerator{}
	orchestrator := NewIdleChatOrchestrator(nil, session.NewCentralMemory(), []string{"shiro"}, 60, 1, 0.7, nil, "")
	orchestrator.SetRunIssuer(owner)
	orchestrator.SetGenerationCheckpointStore(checkpoints)
	orchestrator.mu.Lock()
	orchestrator.topicStockBuf = stock
	orchestrator.forecastTopicGenerator = generator.Generate
	orchestrator.mu.Unlock()
	t.Cleanup(func() {
		if orchestrator.cancel != nil {
			orchestrator.cancel()
		}
		if err := manager.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	return &forecastTopicRecoveryFixture{
		owner: owner, orchestrator: orchestrator, stock: stock, checkpoints: checkpoints,
		stockPath: stockPath, checkpointPath: checkpointPath, generator: generator, domain: forecastDomains[0],
	}
}

func (f *forecastTopicRecoveryFixture) seedWaitingCheckpoint(t *testing.T) (domaintask.Task, domaintask.Run, GenerationCheckpoint) {
	t.Helper()
	ctx := context.Background()
	task, err := f.owner.Create(ctx, domaintask.Task{
		Title:    "IdleChat forecast topic recovery",
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
	result := forecastTopicRecoveryResult()
	checkpoint := GenerationCheckpoint{
		Key: "forecast:" + f.domain.Name, Kind: "forecast", TaskID: task.TaskID, RunID: run.RunID,
		Stage: "result", Category: TopicCategoryForecast, Domain: f.domain,
		ForecastSeeds: []string{"保存済みシード"}, Result: result,
	}
	if err := f.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("seed generation checkpoint: %v", err)
	}
	return task, run, checkpoint
}

func (f *forecastTopicRecoveryFixture) reloadPersistentState(t *testing.T) {
	t.Helper()
	f.checkpoints = NewGenerationCheckpointStore(f.checkpointPath)
	f.stock = newForecastTopicStock(f.stockPath)
	f.orchestrator.SetGenerationCheckpointStore(f.checkpoints)
	f.orchestrator.mu.Lock()
	f.orchestrator.topicStockBuf = f.stock
	f.orchestrator.mu.Unlock()
}

func forecastTopicRecoveryResult() *TopicGenerationResult {
	return &TopicGenerationResult{
		Topic:    "保存済みの未来展望お題",
		Category: TopicCategoryForecast,
		Strategy: string(StrategyForecast),
		Seed:     TopicSeed{Category: TopicCategoryForecast, ForecastDomain: "AI技術", TrendKeywords: []string{"保存済みシード"}},
		Provider: "CodexExe", Initiator: "shiro",
	}
}

type savedForecastTopicGenerator struct {
	calls int
}

func (g *savedForecastTopicGenerator) Generate(domain ForecastDomain) (string, []string, *forecastTopicFailure) {
	g.calls++
	return domain.Name + "の再生成", []string{"再生成シード"}, nil
}

func requireForecastTopicRuns(t *testing.T, owner *forecastTopicRecoveryOwner, taskID modulecore.TaskID) []domaintask.Run {
	t.Helper()
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatalf("list task runs: %v", err)
	}
	return runs
}

func TestProduceForecastTopicResumesSavedResultWithoutGeneration(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, oldRun, _ := fixture.seedWaitingCheckpoint(t)

	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err != nil {
		t.Fatalf("produceForecastTopic() = %v", err)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("forecast generator calls = %d, want 0", fixture.generator.calls)
	}
	if fixture.stock.count(fixture.domain.Name) != 1 {
		t.Fatalf("stock count = %d, want 1", fixture.stock.count(fixture.domain.Name))
	}
	if _, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name); ok {
		t.Fatal("completed checkpoint was not deleted")
	}
	runs := requireForecastTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || runs[0].RunID != oldRun.RunID || runs[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs = %#v, want waiting predecessor and one successor", runs)
	}
	if runs[1].StartReason != domaintask.RunStartReasonCheckpointResume || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("resumed run = %#v", runs[1])
	}
}

func TestForecastTopicRecoveryRunsWhenPendingArtifactFillsDomain(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, oldRun, checkpoint := fixture.seedWaitingCheckpoint(t)
	if added, err := fixture.stock.push(fixture.domain.Name, PreparedTopic{
		Domain: fixture.domain, Topic: checkpoint.Result.Topic, Seeds: append([]string(nil), checkpoint.ForecastSeeds...),
		TaskID: checkpoint.TaskID, RunID: checkpoint.RunID, InitiatedBy: "shiro", Created: time.Now().UTC(),
	}); err != nil || !added {
		t.Fatalf("save pending artifact = %v, %v", added, err)
	}
	otherTaskID, otherRunID := testIdleChatRunIdentityPair()
	if added, err := fixture.stock.push(fixture.domain.Name, PreparedTopic{
		Domain: fixture.domain, Topic: "既存の満杯お題", TaskID: otherTaskID, RunID: otherRunID, InitiatedBy: "shiro", Created: time.Now().UTC(),
	}); err != nil || !added {
		t.Fatalf("fill domain stock = %v, %v", added, err)
	}

	domain, ok := fixture.orchestrator.reserveForecastTopicProduction(fixture.stock, "startup")
	if !ok || domain.Name != fixture.domain.Name {
		t.Fatalf("reserveForecastTopicProduction() = %#v, %v; want pending checkpoint domain,true", domain, ok)
	}
	fixture.stock.doneFilling(domain.Name, nil)
	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err != nil {
		t.Fatalf("recover full domain = %v", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.count(fixture.domain.Name) != forecastTopicStockSize {
		t.Fatalf("recovery generation=%d stock=%d, want 0/%d", fixture.generator.calls, fixture.stock.count(fixture.domain.Name), forecastTopicStockSize)
	}
	if _, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name); ok {
		t.Fatal("completed full-domain checkpoint was not deleted")
	}
	runs := requireForecastTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || runs[0].RunID != oldRun.RunID || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("full-domain recovery runs = %#v", runs)
	}
}

func TestProduceForecastTopicKeepsCheckpointAndWaitsWhenStockSaveFails(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, oldRun, _ := fixture.seedWaitingCheckpoint(t)
	badStockPath := filepath.Join(t.TempDir(), "stock-directory")
	if err := os.Mkdir(badStockPath, 0o755); err != nil {
		t.Fatalf("create bad stock path: %v", err)
	}
	fixture.stock.path = badStockPath

	err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain)
	if err == nil || !strings.Contains(err.Error(), "stock") {
		t.Fatalf("produceForecastTopic() error = %v, want stock save failure", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 0 {
		t.Fatalf("generator calls=%d stock total=%d, want 0/0", fixture.generator.calls, fixture.stock.total())
	}
	checkpoint, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name)
	if !ok || checkpoint.RunID == oldRun.RunID || checkpoint.Result == nil {
		t.Fatalf("checkpoint after stock failure = %#v ok=%v", checkpoint, ok)
	}
	runs := requireForecastTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || runs[0].Status != domaintask.RunStatusWaiting || runs[1].Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after stock failure = %#v", runs)
	}
}

func TestProduceForecastTopicRejectsSucceededRunWithoutExactPublishedArtifact(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, _, checkpoint := fixture.seedWaitingCheckpoint(t)
	succeeded, err := fixture.owner.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatalf("start successor: %v", err)
	}
	if _, err := fixture.owner.CompleteRun(context.Background(), task.TaskID, succeeded.RunID, "Shiro", domaintask.StatusSucceeded, "already completed", ""); err != nil {
		t.Fatalf("complete successor: %v", err)
	}
	checkpoint.RunID = succeeded.RunID
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("save succeeded checkpoint: %v", err)
	}

	err = fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain)
	if err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("produceForecastTopic() error = %v, want missing artifact", err)
	}
	if _, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name); !ok {
		t.Fatal("succeeded checkpoint was deleted without a published artifact")
	}
	if fixture.stock.total() != 0 || fixture.generator.calls != 0 {
		t.Fatalf("missing artifact recovery changed stock=%d generation=%d", fixture.stock.total(), fixture.generator.calls)
	}
}

func TestProduceForecastTopicPersistsCustomResultBeforeStockFailure(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	_, oldRun, checkpoint := fixture.seedWaitingCheckpoint(t)
	checkpoint.Result = nil
	checkpoint.Stage = "seed"
	checkpoint.ForecastSeeds = nil
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("save resultless checkpoint: %v", err)
	}
	badStockPath := filepath.Join(t.TempDir(), "stock-directory")
	if err := os.Mkdir(badStockPath, 0o755); err != nil {
		t.Fatalf("create bad stock path: %v", err)
	}
	fixture.stock.path = badStockPath

	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err == nil || !strings.Contains(err.Error(), "stock") {
		t.Fatalf("first produce error = %v, want stock save failure", err)
	}
	if fixture.generator.calls != 1 || fixture.stock.total() != 0 {
		t.Fatalf("after custom generation failure calls=%d stock=%d, want 1/0", fixture.generator.calls, fixture.stock.total())
	}
	pending, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name)
	if !ok || pending.Result == nil || pending.RunID == oldRun.RunID || pending.Stage != "result" {
		t.Fatalf("pending custom result checkpoint = %#v ok=%v", pending, ok)
	}

	fixture.stock.path = fixture.stockPath
	fixture.reloadPersistentState(t)
	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err != nil {
		t.Fatalf("retry produceForecastTopic() = %v", err)
	}
	if fixture.generator.calls != 1 || fixture.stock.total() != 1 {
		t.Fatalf("retry regenerated or lost stock: calls=%d stock=%d", fixture.generator.calls, fixture.stock.total())
	}
}

func TestProduceForecastTopicRetriesAfterCompletionFailureWithoutNewRun(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, _, _ := fixture.seedWaitingCheckpoint(t)
	fixture.owner.failCompleteOnce = errors.New("injected CompleteRun failure")

	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err == nil {
		t.Fatal("first produce must report CompleteRun failure")
	}
	checkpoint, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name)
	if !ok || fixture.stock.total() != 1 || fixture.generator.calls != 0 {
		t.Fatalf("after failed completion checkpoint=%#v ok=%v stock=%d generation=%d", checkpoint, ok, fixture.stock.total(), fixture.generator.calls)
	}
	fixture.reloadPersistentState(t)
	starts := fixture.owner.startCalls
	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err != nil {
		t.Fatalf("retry produceForecastTopic() = %v", err)
	}
	if fixture.owner.startCalls != starts {
		t.Fatalf("retry started another Run: calls=%d want=%d", fixture.owner.startCalls, starts)
	}
	if fixture.stock.total() != 1 || fixture.generator.calls != 0 {
		t.Fatalf("after retry stock=%d generation=%d", fixture.stock.total(), fixture.generator.calls)
	}
	if _, ok := fixture.checkpoints.Get("forecast:" + fixture.domain.Name); ok {
		t.Fatal("checkpoint remained after successful retry")
	}
	runs := requireForecastTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 2 || runs[1].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("runs after retry = %#v", runs)
	}
}

func TestForecastTopicStockMutationFailurePreservesMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "stock-directory")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatal(err)
	}
	stock := newForecastTopicStock(path)
	domain := forecastDomains[0]
	taskID, runID := testIdleChatRunIdentityPair()
	item := PreparedTopic{Domain: domain, Topic: "保存失敗時も残るお題", TaskID: taskID, RunID: runID}
	if added, err := stock.push(domain.Name, item); err == nil || added {
		t.Fatalf("push = %v, %v; want persistence error and no publication", added, err)
	}
	if stock.total() != 0 {
		t.Fatalf("stock after failed push = %d, want 0", stock.total())
	}
	stock.stock[domain.Name] = []PreparedTopic{item}
	if got, err := stock.pop(domain.Name); err == nil || got != nil {
		t.Fatalf("pop = %#v, %v; want persistence error and no consume", got, err)
	}
	if stock.count(domain.Name) != 1 {
		t.Fatalf("stock after failed pop = %d, want 1", stock.count(domain.Name))
	}
	if got, err := stock.takeByRunID(runID); err == nil || got != nil {
		t.Fatalf("takeByRunID = %#v, %v; want persistence error and no consume", got, err)
	}
	if stock.count(domain.Name) != 1 {
		t.Fatalf("stock after failed takeByRunID = %d, want 1", stock.count(domain.Name))
	}
}

func TestForecastTopicStockLoadErrorFailsClosedWithoutOverwritingSource(t *testing.T) {
	path := filepath.Join(t.TempDir(), "forecast_topic_stock.json")
	original := []byte("{not-json")
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}
	stock := newForecastTopicStock(path)
	taskID, runID := testIdleChatRunIdentityPair()
	added, err := stock.push(forecastDomains[0].Name, PreparedTopic{
		Domain: forecastDomains[0], Topic: "ロード失敗後に上書きしてはいけない", TaskID: taskID, RunID: runID,
	})
	if err == nil || added {
		t.Fatalf("push = %v, %v; want fail-closed load error", added, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("invalid source changed from %q to %q", original, got)
	}
}
