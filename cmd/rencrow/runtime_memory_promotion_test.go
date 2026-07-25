package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
)

type memoryPromotionRunnerStub struct {
	calls  atomic.Int32
	called chan struct{}
}

func (s *memoryPromotionRunnerStub) RunOne(context.Context) (memorypromotionapp.RunResult, error) {
	count := s.calls.Add(1)
	if count == 1 {
		select {
		case s.called <- struct{}{}:
		default:
		}
		return memorypromotionapp.RunResult{Processed: true, MessageCount: 1}, nil
	}
	return memorypromotionapp.RunResult{}, nil
}

func TestMemoryPromotionWorkerWaitsForIdleGraceThenDrains(t *testing.T) {
	tracker := newLLMBusyTracker()
	endBusy := tracker.Begin(context.Background(), "chat")
	runner := &memoryPromotionRunnerStub{called: make(chan struct{}, 1)}
	cancel := startMemoryPromotionWorkerRunner(
		runner, tracker, 20*time.Millisecond, time.Second, 5*time.Millisecond,
		backgroundJobFailureReporter{},
	)
	defer cancel()

	select {
	case <-runner.called:
		t.Fatal("worker ran while LLM was busy")
	case <-time.After(40 * time.Millisecond):
	}
	endBusy()
	select {
	case <-runner.called:
	case <-time.After(time.Second):
		t.Fatal("worker did not run after idle grace")
	}
}
