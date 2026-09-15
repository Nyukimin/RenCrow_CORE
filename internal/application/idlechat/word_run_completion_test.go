package idlechat

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func seedRunningWordCompletionCheckpointForCategory(t *testing.T, fixture *wordTopicRecoveryFixture, category TopicCategory) (domaintask.Task, domaintask.Run, GenerationCheckpoint) {
	t.Helper()
	ctx := context.Background()
	task, err := fixture.owner.Create(ctx, domaintask.Task{
		Title:    "IdleChat word completion retry",
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
	result := wordTopicRecoveryResult()
	result.Category = category
	result.Seed.Category = category
	checkpoint := GenerationCheckpoint{
		Key:      "word:" + string(category),
		Kind:     "word",
		TaskID:   task.TaskID,
		RunID:    run.RunID,
		Stage:    "seed",
		Attempt:  1,
		Category: category,
		Seed:     result.Seed,
	}
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist running checkpoint: %v", err)
	}
	return task, run, checkpoint
}

func seedRunningWordCompletionCheckpoint(t *testing.T, fixture *wordTopicRecoveryFixture) (domaintask.Task, domaintask.Run, GenerationCheckpoint) {
	return seedRunningWordCompletionCheckpointForCategory(t, fixture, TopicCategorySingle)
}

func TestWordCompletionFailureKeepsRunningPairBeforeResume(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, run, checkpoint := seedRunningWordCompletionCheckpoint(t, fixture)
	fixture.owner.failCompleteOnce = errors.New("injected word completion failure")

	err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "injected word completion failure") {
		t.Fatalf("produceWordTopic() error = %v, want injected completion failure", err)
	}
	if fixture.owner.completeCalls != 1 {
		t.Fatalf("CompleteRun calls = %d, want 1 before resume admission", fixture.owner.completeCalls)
	}
	if fixture.owner.startCalls != 1 {
		t.Fatalf("StartRun calls = %d, want 1 after failed completion", fixture.owner.startCalls)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("provider calls = %d, want 0 after failed completion", fixture.generator.calls)
	}
	persisted, found := fixture.checkpoints.Get(checkpoint.Key)
	if !found {
		t.Fatal("running checkpoint was dropped after completion failure")
	}
	if persisted.RunID != run.RunID || persisted.Stage != checkpoint.Stage {
		t.Fatalf("persisted checkpoint = %+v, want exact running pair %+v", persisted, checkpoint)
	}
	current, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read retained Run: %v", err)
	}
	if current.TaskID != task.TaskID || current.Status != domaintask.RunStatusRunning {
		t.Fatalf("retained Run = %+v, want task %s running", current, task.TaskID)
	}

	// The owner has healed. Admission must close the retained pair first, then
	// issue one checkpoint successor and only then reach the provider.
	err = fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "saved checkpoint must not call CodexExe") {
		t.Fatalf("healed produceWordTopic() error = %v, want provider failure", err)
	}
	if fixture.owner.startCalls != 2 {
		t.Fatalf("StartRun calls after healed retry = %d, want 2", fixture.owner.startCalls)
	}
	if fixture.generator.calls == 0 {
		t.Fatal("provider was not called after healed retry")
	}
	current, err = fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read closed predecessor Run: %v", err)
	}
	if current.Status != domaintask.RunStatusWaiting {
		t.Fatalf("predecessor Run status = %s, want waiting", current.Status)
	}
}

func TestWordCompletionClosesRunningSavedResultWithoutProvider(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, run, checkpoint := seedRunningWordCompletionCheckpoint(t, fixture)
	result := wordTopicRecoveryResult()
	checkpoint.Stage = "result"
	checkpoint.Result = result
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist saved-result checkpoint: %v", err)
	}
	added, err := fixture.stock.push(WordPreparedTopic{
		Category:    result.Category,
		Topic:       result.Topic,
		Seed:        result.Seed,
		Axis:        result.InterestingnessAxis,
		OpeningHook: result.OpeningHook,
		Avoid:       result.Avoid,
		TaskID:      task.TaskID,
		RunID:       run.RunID,
		InitiatedBy: "shiro",
	})
	if err != nil || !added {
		t.Fatalf("save matching word stock item: added=%t err=%v", added, err)
	}

	if err := fixture.orchestrator.finalizePendingWordRuns(context.Background()); err != nil {
		t.Fatalf("finalize saved-result word completion: %v", err)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("provider calls=%d, want zero", fixture.generator.calls)
	}
	if fixture.owner.startCalls != 1 {
		t.Fatalf("StartRun calls=%d, want one original Run", fixture.owner.startCalls)
	}
	if fixture.stock.count(TopicCategorySingle) != 1 {
		t.Fatalf("saved stock count=%d, want one retained item", fixture.stock.count(TopicCategorySingle))
	}
	if _, ok := fixture.checkpoints.Get(checkpoint.Key); ok {
		t.Fatal("saved-result checkpoint remained after successful closure")
	}
	closed, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read closed saved-result Run: %v", err)
	}
	if closed.TaskID != task.TaskID || closed.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("closed saved-result Run=%+v, want exact succeeded pair", closed)
	}
	updated, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("read saved-result Task: %v", err)
	}
	if updated.Status != domaintask.StatusSucceeded {
		t.Fatalf("saved-result Task status=%s, want succeeded", updated.Status)
	}
}

func waitForWordGenerationAdmissionClosed(t *testing.T, orchestrator *IdleChatOrchestrator) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for {
		orchestrator.generationWorkMu.Lock()
		closed := orchestrator.generationWorkClosed
		orchestrator.generationWorkMu.Unlock()
		if closed {
			return
		}
		select {
		case <-deadline.C:
			t.Fatal("Stop did not close generation admission")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestWordCompletionStopClosesRunningPairsAfterGenerationJoin(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	singleTask, singleRun, singleCheckpoint := seedRunningWordCompletionCheckpointForCategory(t, fixture, TopicCategorySingle)
	doubleTask, doubleRun, doubleCheckpoint := seedRunningWordCompletionCheckpointForCategory(t, fixture, TopicCategoryDouble)
	pairs := []struct {
		task       domaintask.Task
		run        domaintask.Run
		checkpoint GenerationCheckpoint
	}{
		{task: singleTask, run: singleRun, checkpoint: singleCheckpoint},
		{task: doubleTask, run: doubleRun, checkpoint: doubleCheckpoint},
	}
	started := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	if !fixture.orchestrator.startGenerationWork(func() {
		close(started)
		<-release
	}) {
		t.Fatal("generation work was not admitted")
	}
	waitForGenerationWorkSignal(t, started, "admitted generation work")
	stockBefore := fixture.stock.snapshot()

	stopDone := make(chan struct{})
	go func() {
		fixture.orchestrator.Stop()
		close(stopDone)
	}()
	waitForWordGenerationAdmissionClosed(t, fixture.orchestrator)
	for _, pair := range pairs {
		current, err := fixture.owner.GetRun(context.Background(), pair.run.RunID)
		if err != nil {
			t.Fatalf("read %s Run before release: %v", pair.checkpoint.Category, err)
		}
		if current.TaskID != pair.task.TaskID || current.Status != domaintask.RunStatusRunning {
			t.Fatalf("%s Run before release=%+v, want exact running Run", pair.checkpoint.Category, current)
		}
	}
	if fixture.generator.calls != 0 || fixture.owner.startCalls != len(pairs) || fixture.stock.total() != stockBefore.Total {
		t.Fatalf("state before release provider=%d starts=%d stock=%d, want 0/%d/%d", fixture.generator.calls, fixture.owner.startCalls, fixture.stock.total(), len(pairs), stockBefore.Total)
	}
	select {
	case <-stopDone:
		t.Fatal("Stop returned before admitted generation work released")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	waitForGenerationWorkSignal(t, stopDone, "Stop completion")

	if fixture.generator.calls != 0 || fixture.owner.startCalls != len(pairs) {
		t.Fatalf("state after Stop provider=%d starts=%d, want 0/%d", fixture.generator.calls, fixture.owner.startCalls, len(pairs))
	}
	stockAfter := fixture.stock.snapshot()
	if stockAfter.Total != stockBefore.Total || stockAfter.Filling || fixture.orchestrator.topicProductionBusy() {
		t.Fatalf("stock after Stop=%+v, want unchanged and idle", stockAfter)
	}
	for _, pair := range pairs {
		closed, err := fixture.owner.GetRun(context.Background(), pair.run.RunID)
		if err != nil {
			t.Fatalf("read %s Run after Stop: %v", pair.checkpoint.Category, err)
		}
		if closed.TaskID != pair.task.TaskID || closed.Status != domaintask.RunStatusWaiting {
			t.Fatalf("%s Run after Stop=%+v, want exact waiting Run", pair.checkpoint.Category, closed)
		}
		persisted, ok := fixture.checkpoints.Get(pair.checkpoint.Key)
		if !ok || persisted.RunID != pair.run.RunID || persisted.TaskID != pair.task.TaskID || persisted.Stage != pair.checkpoint.Stage {
			t.Fatalf("%s checkpoint after Stop=%+v ok=%t, want retained exact pair", pair.checkpoint.Category, persisted, ok)
		}
	}
}

func TestWordCompletionRejectsMissingOrMismatchedStock(t *testing.T) {
	tests := []struct {
		name  string
		want  string
		setup func(t *testing.T, fixture *wordTopicRecoveryFixture, task domaintask.Task, run domaintask.Run, checkpoint *GenerationCheckpoint)
	}{
		{
			name: "missing stock",
			want: "word topic stock is not configured",
			setup: func(_ *testing.T, fixture *wordTopicRecoveryFixture, _ domaintask.Task, _ domaintask.Run, _ *GenerationCheckpoint) {
				fixture.stock.path = ""
			},
		},
		{
			name: "foreign task",
			want: "saved word topic does not match checkpoint",
			setup: func(t *testing.T, fixture *wordTopicRecoveryFixture, _ domaintask.Task, run domaintask.Run, checkpoint *GenerationCheckpoint) {
				result := wordTopicRecoveryResult()
				added, err := fixture.stock.push(WordPreparedTopic{
					Category: result.Category, Topic: result.Topic, Seed: result.Seed, Axis: result.InterestingnessAxis,
					TaskID: modulecore.NewTaskID(), RunID: run.RunID, InitiatedBy: "shiro",
				})
				if err != nil || !added {
					t.Fatalf("save foreign-task word stock item: added=%t err=%v", added, err)
				}
				checkpoint.Stage = "result"
				checkpoint.Result = result
				if err := fixture.checkpoints.Put(*checkpoint); err != nil {
					t.Fatalf("persist foreign-task checkpoint: %v", err)
				}
			},
		},
		{
			name: "stale result",
			want: "saved word topic does not match checkpoint",
			setup: func(t *testing.T, fixture *wordTopicRecoveryFixture, task domaintask.Task, run domaintask.Run, checkpoint *GenerationCheckpoint) {
				result := wordTopicRecoveryResult()
				added, err := fixture.stock.push(WordPreparedTopic{
					Category: result.Category, Topic: result.Topic, Seed: result.Seed, Axis: result.InterestingnessAxis,
					TaskID: task.TaskID, RunID: run.RunID, InitiatedBy: "shiro",
				})
				if err != nil || !added {
					t.Fatalf("save stale-result stock item: added=%t err=%v", added, err)
				}
				result.Topic = "stale checkpoint topic"
				checkpoint.Stage = "result"
				checkpoint.Result = result
				if err := fixture.checkpoints.Put(*checkpoint); err != nil {
					t.Fatalf("persist stale-result checkpoint: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newWordTopicRecoveryFixture(t)
			task, run, checkpoint := seedRunningWordCompletionCheckpoint(t, fixture)
			test.setup(t, fixture, task, run, &checkpoint)
			starts, completions, providerCalls := fixture.owner.startCalls, fixture.owner.completeCalls, fixture.generator.calls
			stockBefore := fixture.stock.snapshot()

			err := fixture.orchestrator.finalizePendingWordRuns(context.Background())
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("finalizePendingWordRuns() error=%v, want %q", err, test.want)
			}
			if fixture.owner.startCalls != starts || fixture.owner.completeCalls != completions || fixture.generator.calls != providerCalls {
				t.Fatalf("owner/provider mutation starts=%d completions=%d provider=%d, want %d/%d/%d", fixture.owner.startCalls, fixture.owner.completeCalls, fixture.generator.calls, starts, completions, providerCalls)
			}
			stockAfter := fixture.stock.snapshot()
			if stockAfter.Total != stockBefore.Total || stockAfter.Filling != stockBefore.Filling {
				t.Fatalf("stock after rejected completion=%+v, want unchanged from %+v", stockAfter, stockBefore)
			}
			current, err := fixture.owner.GetRun(context.Background(), run.RunID)
			if err != nil {
				t.Fatalf("read retained Run: %v", err)
			}
			if current.TaskID != task.TaskID || current.Status != domaintask.RunStatusRunning {
				t.Fatalf("retained Run=%+v, want exact running pair", current)
			}
			updated, err := fixture.owner.Get(context.Background(), task.TaskID)
			if err != nil {
				t.Fatalf("read retained Task: %v", err)
			}
			if updated.Status != domaintask.StatusRunning {
				t.Fatalf("retained Task status=%s, want running", updated.Status)
			}
			persisted, ok := fixture.checkpoints.Get(checkpoint.Key)
			if !ok || persisted.RunID != checkpoint.RunID || persisted.TaskID != checkpoint.TaskID || persisted.Stage != checkpoint.Stage {
				t.Fatalf("checkpoint after rejected completion=%+v ok=%t, want unchanged exact pair", persisted, ok)
			}
		})
	}
}

func TestWordCompletionResumePendingRunningPredecessorFailsClosed(t *testing.T) {
	fixture := newWordTopicRecoveryFixture(t)
	task, run, checkpoint := seedRunningWordCompletionCheckpoint(t, fixture)
	checkpoint.Stage = "resume_pending"
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist resume-pending checkpoint: %v", err)
	}
	starts, completions, providerCalls, stockTotal := fixture.owner.startCalls, fixture.owner.completeCalls, fixture.generator.calls, fixture.stock.total()

	err := fixture.orchestrator.produceWordTopic(fixture.stock, TopicCategorySingle)
	if err == nil || !strings.Contains(err.Error(), "predecessor status") {
		t.Fatalf("resume_pending Running predecessor error=%v, want explicit predecessor-status failure", err)
	}
	if fixture.owner.startCalls != starts || fixture.owner.completeCalls != completions || fixture.generator.calls != providerCalls || fixture.stock.total() != stockTotal {
		t.Fatalf("resume_pending failure mutated starts=%d completions=%d provider=%d stock=%d, want %d/%d/%d/%d", fixture.owner.startCalls, fixture.owner.completeCalls, fixture.generator.calls, fixture.stock.total(), starts, completions, providerCalls, stockTotal)
	}
	current, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read resume-pending predecessor: %v", err)
	}
	if current.TaskID != task.TaskID || current.Status != domaintask.RunStatusRunning {
		t.Fatalf("resume-pending predecessor=%+v, want exact running pair", current)
	}
	updated, err := fixture.owner.Get(context.Background(), task.TaskID)
	if err != nil {
		t.Fatalf("read resume-pending Task: %v", err)
	}
	if updated.Status != domaintask.StatusRunning {
		t.Fatalf("resume-pending Task status=%s, want running", updated.Status)
	}
	persisted, ok := fixture.checkpoints.Get(checkpoint.Key)
	if !ok || persisted.RunID != run.RunID || persisted.TaskID != task.TaskID || persisted.Stage != "resume_pending" {
		t.Fatalf("resume-pending checkpoint=%+v ok=%t, want exact retained intent", persisted, ok)
	}
	t.Log("explicit source gap: resume_pending with a still-Running predecessor fails closed and retains the exact intent for owner-level recovery")
}
