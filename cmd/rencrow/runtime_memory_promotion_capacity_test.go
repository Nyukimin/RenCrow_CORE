package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
)

type capacityRefusedMemoryPromotionRunner struct {
	calls atomic.Int32
}

func (r *capacityRefusedMemoryPromotionRunner) RunOne(context.Context) (memorypromotionapp.RunResult, error) {
	r.calls.Add(1)
	return memorypromotionapp.RunResult{}, fmt.Errorf("start Memory Promotion run: %w", taskmanager.ErrParallelLimit)
}

// A refusal for lack of execution capacity is not a job failure: it is retried
// later, it is not reported as background_job.failed, and the retry spacing
// grows up to a bound instead of hammering the Task store.
func TestMemoryPromotionWorkerDefersCapacityRefusalWithBoundedBackoff(t *testing.T) {
	previous := memoryPromotionCapacityBackoffMax
	memoryPromotionCapacityBackoffMax = 40 * time.Millisecond
	t.Cleanup(func() { memoryPromotionCapacityBackoffMax = previous })

	tracker := newLLMBusyTracker()
	listener := &captureBackgroundJobEventListener{}
	runner := &capacityRefusedMemoryPromotionRunner{}
	cancel := startMemoryPromotionWorkerRunner(
		runner, tracker, 2*time.Millisecond, time.Second, time.Millisecond,
		newBackgroundJobFailureReporter(listener),
	)
	time.Sleep(400 * time.Millisecond)
	cancel()

	if events := listener.Events(); len(events) != 0 {
		t.Fatalf("a capacity refusal was reported as a job failure: %d events", len(events))
	}
	calls := runner.calls.Load()
	if calls < 3 {
		t.Fatalf("the worker gave up after %d attempts; a deferral must be retried", calls)
	}
	// Without a backoff the worker retries about every idle grace (a few ms),
	// which is dozens of attempts in this window; with the bounded backoff it
	// is bounded by the cap (40ms) plus the initial doubling steps.
	if calls > 30 {
		t.Fatalf("attempts=%d, want a backoff that spaces out retries", calls)
	}
}
