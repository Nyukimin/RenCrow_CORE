package idlechat

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestProduceWordTopicWaitsWhenResumeCheckpointReconcileSaveFails(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, oldRun, checkpoint := fixture.seedWaitingCheckpoint(t)
	checkpoint.Stage = "resume_pending"
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("save resume intent: %v", err)
	}
	issued, err := resumeIdleChatRun(context.Background(), fixture.owner, &checkpoint, fixture.checkpoints)
	if err != nil {
		t.Fatalf("issue resume successor: %v", err)
	}

	badPath := filepath.Join(t.TempDir(), "checkpoint-directory")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatalf("create bad checkpoint path: %v", err)
	}
	failedStore := NewGenerationCheckpointStore(fixture.checkpointPath)
	failedStore.path = badPath
	fixture.orchestrator.SetGenerationCheckpointStore(failedStore)

	err = fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "generation checkpoints") {
		t.Fatalf("produceWordTopic() error = %v, want checkpoint save failure", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 0 {
		t.Fatalf("failed reconciliation changed generation=%d stock=%d", fixture.generator.calls, fixture.stock.total())
	}

	durable := NewGenerationCheckpointStore(fixture.checkpointPath)
	persisted, ok := durable.Get(checkpoint.Key)
	if !ok || persisted.RunID != oldRun.RunID || persisted.Stage != "resume_pending" {
		t.Fatalf("durable resume intent = %#v ok=%v, want original waiting Run", persisted, ok)
	}
	runs := requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if findWordTopicRun(t, runs, oldRun.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("old Run = %#v, want waiting", findWordTopicRun(t, runs, oldRun.RunID))
	}
	if findWordTopicRun(t, runs, issued.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("issued successor = %#v, want waiting", findWordTopicRun(t, runs, issued.RunID))
	}
	updatedTask, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("read task after failed reconciliation: %v", err)
	}
	if updatedTask.Status != domaintask.StatusWaiting {
		t.Fatalf("task after failed reconciliation = %s, want waiting", updatedTask.Status)
	}

	fixture.reloadPersistentState(t)
	if err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle); err != nil {
		t.Fatalf("retry produceWordTopic() = %v", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 1 {
		t.Fatalf("retry changed generation=%d stock=%d, want 0/1", fixture.generator.calls, fixture.stock.total())
	}
	if _, ok := fixture.checkpoints.Get(checkpoint.Key); ok {
		t.Fatal("resume checkpoint remained after retry")
	}
	runs = requireWordTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 3 || findWordTopicRun(t, runs, issued.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after retry = %#v, want failed successor waiting plus fresh successor", runs)
	}
	var retried domaintask.Run
	for _, run := range runs {
		if run.RunID != oldRun.RunID && run.RunID != issued.RunID {
			retried = run
		}
	}
	if retried.RunID == "" || retried.StartReason != domaintask.RunStartReasonCheckpointResume || retried.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("retry successor = %#v, want fresh checkpoint-resume success", retried)
	}
}

func TestProduceForecastTopicWaitsWhenResumeCheckpointReconcileSaveFails(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	task, oldRun, checkpoint := fixture.seedWaitingCheckpoint(t)
	checkpoint.Stage = "resume_pending"
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("save resume intent: %v", err)
	}
	issued, err := resumeIdleChatRun(context.Background(), fixture.owner, &checkpoint, fixture.checkpoints)
	if err != nil {
		t.Fatalf("issue resume successor: %v", err)
	}

	badPath := filepath.Join(t.TempDir(), "checkpoint-directory")
	if err := os.Mkdir(badPath, 0o755); err != nil {
		t.Fatalf("create bad checkpoint path: %v", err)
	}
	failedStore := NewGenerationCheckpointStore(fixture.checkpointPath)
	failedStore.path = badPath
	fixture.orchestrator.SetGenerationCheckpointStore(failedStore)

	err = fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain)
	if err == nil || !strings.Contains(err.Error(), "generation checkpoints") {
		t.Fatalf("produceForecastTopic() error = %v, want checkpoint save failure", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 0 {
		t.Fatalf("failed reconciliation changed generation=%d stock=%d", fixture.generator.calls, fixture.stock.total())
	}

	durable := NewGenerationCheckpointStore(fixture.checkpointPath)
	persisted, ok := durable.Get(checkpoint.Key)
	if !ok || persisted.RunID != oldRun.RunID || persisted.Stage != "resume_pending" {
		t.Fatalf("durable resume intent = %#v ok=%v, want original waiting Run", persisted, ok)
	}
	runs := requireForecastTopicRuns(t, fixture.owner, task.TaskID)
	if forecastTopicRunByID(t, runs, oldRun.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("old Run = %#v, want waiting", forecastTopicRunByID(t, runs, oldRun.RunID))
	}
	if forecastTopicRunByID(t, runs, issued.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("issued successor = %#v, want waiting", forecastTopicRunByID(t, runs, issued.RunID))
	}
	updatedTask, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("read task after failed reconciliation: %v", err)
	}
	if updatedTask.Status != domaintask.StatusWaiting {
		t.Fatalf("task after failed reconciliation = %s, want waiting", updatedTask.Status)
	}

	fixture.reloadPersistentState(t)
	if err := fixture.orchestrator.produceForecastTopic(fixture.stock, fixture.domain); err != nil {
		t.Fatalf("retry produceForecastTopic() = %v", err)
	}
	if fixture.generator.calls != 0 || fixture.stock.total() != 1 {
		t.Fatalf("retry changed generation=%d stock=%d, want 0/1", fixture.generator.calls, fixture.stock.total())
	}
	if _, ok := fixture.checkpoints.Get(checkpoint.Key); ok {
		t.Fatal("resume checkpoint remained after retry")
	}
	runs = requireForecastTopicRuns(t, fixture.owner, task.TaskID)
	if len(runs) != 3 || forecastTopicRunByID(t, runs, issued.RunID).Status != domaintask.RunStatusWaiting {
		t.Fatalf("runs after retry = %#v, want failed successor waiting plus fresh successor", runs)
	}
	var retried domaintask.Run
	for _, run := range runs {
		if run.RunID != oldRun.RunID && run.RunID != issued.RunID {
			retried = run
		}
	}
	if retried.RunID == "" || retried.StartReason != domaintask.RunStartReasonCheckpointResume || retried.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("retry successor = %#v, want fresh checkpoint-resume success", retried)
	}
}

func forecastTopicRunByID(t *testing.T, runs []domaintask.Run, runID modulecore.RunID) domaintask.Run {
	t.Helper()
	for _, run := range runs {
		if run.RunID == runID {
			return run
		}
	}
	t.Fatalf("run %s was not found in %#v", runID, runs)
	return domaintask.Run{}
}
