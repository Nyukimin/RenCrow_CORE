package idlechat

import (
	"context"
	"errors"
	"strings"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestProduceForecastTopicClosesRunningCheckpointBeforeResume(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, nil)
	checkpoint.Stage = "seeds"
	checkpoint.ForecastSeeds = []string{"保存済みシード"}
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist resultless forecast checkpoint: %v", err)
	}
	fixture.owner.failCompleteOnce = errors.New("injected forecast completion failure")

	err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain)
	if err == nil || !strings.Contains(err.Error(), "injected forecast completion failure") {
		t.Fatalf("first produceForecastTopic() error=%v, want completion failure", err)
	}
	if fixture.owner.startCalls != 1 || fixture.generator.calls != 0 {
		t.Fatalf("failed closure issued start/provider calls=%d/%d, want 1/0", fixture.owner.startCalls, fixture.generator.calls)
	}
	retained, ok := fixture.checkpoints.Get(checkpoint.Key)
	if !ok || retained.TaskID != task.TaskID || retained.RunID != run.RunID || retained.Stage != checkpoint.Stage {
		t.Fatalf("retained forecast checkpoint=%+v ok=%t, want exact running pair", retained, ok)
	}
	current, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read retained forecast Run: %v", err)
	}
	if current.Status != domaintask.RunStatusRunning {
		t.Fatalf("retained forecast Run status=%s, want running", current.Status)
	}

	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err != nil {
		t.Fatalf("healed produceForecastTopic()=%v", err)
	}
	if fixture.owner.startCalls != 2 || fixture.generator.calls != 1 {
		t.Fatalf("healed closure start/provider calls=%d/%d, want 2/1", fixture.owner.startCalls, fixture.generator.calls)
	}
	closed, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read closed predecessor Run: %v", err)
	}
	if closed.Status != domaintask.RunStatusWaiting {
		t.Fatalf("predecessor Run status=%s, want waiting", closed.Status)
	}
}

func seedRunningForecastCompletionCheckpoint(t *testing.T, fixture *forecastTopicRecoveryFixture, result *TopicGenerationResult) (domaintask.Task, domaintask.Run, GenerationCheckpoint) {
	t.Helper()
	ctx := context.Background()
	task, err := fixture.owner.Create(ctx, domaintask.Task{
		Title:    "IdleChat forecast completion maintenance",
		Route:    domaintask.RouteGeneral,
		Assignee: "Shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create task: %v", err)
	}
	run, err := fixture.owner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start first run: %v", err)
	}
	checkpoint := GenerationCheckpoint{
		Key: "forecast:" + fixture.domain.Name, Kind: "forecast", TaskID: task.TaskID, RunID: run.RunID,
		Stage: "result", Category: TopicCategoryForecast, Domain: fixture.domain,
		ForecastSeeds: []string{"保存済みシード"}, Result: result,
	}
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist running forecast checkpoint: %v", err)
	}
	return task, run, checkpoint
}

func addForecastCompletionStock(t *testing.T, fixture *forecastTopicRecoveryFixture, checkpoint GenerationCheckpoint) {
	t.Helper()
	if checkpoint.Result == nil {
		t.Fatal("completion stock requires a saved result")
	}
	added, err := fixture.stock.push(fixture.domain.Name, PreparedTopic{
		Domain: fixture.domain, Topic: checkpoint.Result.Topic, Seeds: append([]string(nil), checkpoint.ForecastSeeds...),
		TaskID: checkpoint.TaskID, RunID: checkpoint.RunID, InitiatedBy: "shiro",
	})
	if err != nil || !added {
		t.Fatalf("save matching forecast stock item: added=%t err=%v", added, err)
	}
}

func TestForecastCompletionClosesRunningSavedResultWithoutProvider(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, forecastTopicRecoveryResult())
	addForecastCompletionStock(t, fixture, checkpoint)

	if err := fixture.orchestrator.finalizePendingForecastRuns(context.Background()); err != nil {
		t.Fatalf("finalize saved-result forecast completion: %v", err)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("provider calls=%d, want zero", fixture.generator.calls)
	}
	if fixture.owner.startCalls != 1 || fixture.owner.completeCalls != 1 {
		t.Fatalf("owner calls start=%d complete=%d, want 1/1", fixture.owner.startCalls, fixture.owner.completeCalls)
	}
	if fixture.stock.count(fixture.domain.Name) != 1 {
		t.Fatalf("saved forecast stock count=%d, want one retained item", fixture.stock.count(fixture.domain.Name))
	}
	if _, ok := fixture.checkpoints.Get(checkpoint.Key); ok {
		t.Fatal("saved-result forecast checkpoint remained after successful closure")
	}
	closed, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read closed forecast Run: %v", err)
	}
	if closed.TaskID != task.TaskID || closed.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("closed forecast Run=%+v, want exact succeeded pair", closed)
	}
	updated, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("read closed forecast Task: %v", err)
	}
	if updated.Status != domaintask.StatusSucceeded {
		t.Fatalf("closed forecast Task status=%s, want succeeded", updated.Status)
	}
}

func TestForecastCompletionClosesRunningWithoutPublishedResultAsWaiting(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	_, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, nil)
	checkpoint.Stage = "seeds"
	checkpoint.ForecastSeeds = []string{"保存済みシード"}
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist resultless forecast checkpoint: %v", err)
	}

	if err := fixture.orchestrator.finalizePendingForecastRuns(context.Background()); err != nil {
		t.Fatalf("finalize resultless forecast completion: %v", err)
	}
	if fixture.generator.calls != 0 || fixture.owner.startCalls != 1 {
		t.Fatalf("provider/start calls=%d/%d, want 0/1", fixture.generator.calls, fixture.owner.startCalls)
	}
	closed, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read waiting forecast Run: %v", err)
	}
	if closed.Status != domaintask.RunStatusWaiting {
		t.Fatalf("forecast Run status=%s, want waiting", closed.Status)
	}
	persisted, ok := fixture.checkpoints.Get(checkpoint.Key)
	if !ok || persisted.TaskID != checkpoint.TaskID || persisted.RunID != checkpoint.RunID || persisted.Stage != checkpoint.Stage {
		t.Fatalf("retained forecast checkpoint=%+v ok=%t, want exact waiting pair", persisted, ok)
	}
}

func TestForecastCompletionRejectsMismatchedStockBeforeOwnerEffect(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, forecastTopicRecoveryResult())
	added, err := fixture.stock.push(fixture.domain.Name, PreparedTopic{
		Domain: fixture.domain, Topic: "別の保存済みお題", Seeds: append([]string(nil), checkpoint.ForecastSeeds...),
		TaskID: task.TaskID, RunID: run.RunID, InitiatedBy: "shiro",
	})
	if err != nil || !added {
		t.Fatalf("save mismatched forecast stock item: added=%t err=%v", added, err)
	}

	err = fixture.orchestrator.finalizePendingForecastRuns(context.Background())
	if err == nil || !strings.Contains(err.Error(), "mismatched") {
		t.Fatalf("finalize mismatched forecast completion error=%v, want mismatch", err)
	}
	if fixture.owner.completeCalls != 0 || fixture.generator.calls != 0 {
		t.Fatalf("owner/provider effects complete=%d provider=%d, want 0/0", fixture.owner.completeCalls, fixture.generator.calls)
	}
	current, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read retained forecast Run: %v", err)
	}
	if current.TaskID != task.TaskID || current.Status != domaintask.RunStatusRunning {
		t.Fatalf("retained forecast Run=%+v, want exact running pair", current)
	}
	if _, ok := fixture.checkpoints.Get(checkpoint.Key); !ok {
		t.Fatal("mismatched forecast checkpoint was dropped")
	}
}
