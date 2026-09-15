package idlechat

import (
	"context"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestTopicCompletionMaintenanceBypassesRefillGates(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	_, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, forecastTopicRecoveryResult())
	addForecastCompletionStock(t, fixture, checkpoint)
	fixture.orchestrator.mu.Lock()
	fixture.orchestrator.manualMode = true
	fixture.orchestrator.lastActivity = time.Now().Add(-2 * time.Hour)
	fixture.orchestrator.externalLLMBusy = func() bool { return true }
	fixture.orchestrator.mu.Unlock()

	if err := fixture.orchestrator.retryPendingTopicCompletions(); err != nil {
		t.Fatalf("retry pending topic completions: %v", err)
	}
	closed, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read maintained forecast Run: %v", err)
	}
	if closed.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("maintained forecast Run status=%s, want succeeded", closed.Status)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("provider calls=%d, want zero", fixture.generator.calls)
	}
}

func TestTopicCompletionMaintenanceSkipsActiveProducer(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	_, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, forecastTopicRecoveryResult())
	addForecastCompletionStock(t, fixture, checkpoint)
	if !fixture.orchestrator.tryBeginTopicProduction() {
		t.Fatal("topic producer gate was not acquired")
	}
	defer fixture.orchestrator.endTopicProduction()

	if err := fixture.orchestrator.retryPendingTopicCompletions(); err != nil {
		t.Fatalf("retry while producer active: %v", err)
	}
	current, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read active forecast Run: %v", err)
	}
	if current.Status != domaintask.RunStatusRunning {
		t.Fatalf("active forecast Run status=%s, want running", current.Status)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("provider calls=%d, want zero", fixture.generator.calls)
	}
}

func TestStopFlushesForecastCompletionAfterGenerationJoin(t *testing.T) {
	fixture := newForecastTopicRecoveryFixture(t)
	_, run, checkpoint := seedRunningForecastCompletionCheckpoint(t, fixture, nil)
	checkpoint.Stage = "seeds"
	checkpoint.ForecastSeeds = []string{"保存済みシード"}
	if err := fixture.checkpoints.Put(checkpoint); err != nil {
		t.Fatalf("persist resultless forecast checkpoint: %v", err)
	}

	started := make(chan struct{})
	release := make(chan struct{})
	if !fixture.orchestrator.startGenerationWork(func() {
		close(started)
		<-release
	}) {
		t.Fatal("generation work was not admitted")
	}
	waitForGenerationWorkSignal(t, started, "forecast generation work start")
	stopDone := make(chan struct{})
	go func() {
		fixture.orchestrator.Stop()
		close(stopDone)
	}()
	select {
	case <-stopDone:
		t.Fatal("Stop returned before forecast generation work joined")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	waitForGenerationWorkSignal(t, stopDone, "forecast Stop completion")

	closed, err := fixture.owner.GetRun(context.Background(), run.RunID)
	if err != nil {
		t.Fatalf("read stopped forecast Run: %v", err)
	}
	if closed.Status != domaintask.RunStatusWaiting {
		t.Fatalf("stopped forecast Run status=%s, want waiting", closed.Status)
	}
	if fixture.generator.calls != 0 {
		t.Fatalf("provider calls=%d, want zero", fixture.generator.calls)
	}
	persisted, ok := fixture.checkpoints.Get(checkpoint.Key)
	if !ok || persisted.TaskID != checkpoint.TaskID || persisted.RunID != checkpoint.RunID {
		t.Fatalf("forecast checkpoint after Stop=%+v ok=%t, want retained exact pair", persisted, ok)
	}
}
