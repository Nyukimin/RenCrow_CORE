package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	memorypromotionapp "github.com/Nyukimin/RenCrow_CORE/internal/application/memorypromotion"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type memoryPromotionRunnerStub struct {
	calls  atomic.Int32
	called chan struct{}
}

type blockingMemoryPromotionRunnerStub struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingMemoryPromotionRunnerStub) RunOne(ctx context.Context) (memorypromotionapp.RunResult, error) {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return memorypromotionapp.RunResult{}, nil
	case <-ctx.Done():
		return memorypromotionapp.RunResult{}, ctx.Err()
	}
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

func TestMemoryPromotionWorkerKeepsIdleGraceWhileIdleChatIsBusy(t *testing.T) {
	tracker := newLLMBusyTracker()
	endIdleChat := tracker.Begin(context.Background(), "idlechat")
	runner := &memoryPromotionRunnerStub{called: make(chan struct{}, 1)}
	cancel := startMemoryPromotionWorkerRunner(
		runner, tracker, 40*time.Millisecond, time.Second, 5*time.Millisecond,
		backgroundJobFailureReporter{},
	)
	defer cancel()

	time.Sleep(60 * time.Millisecond)
	endIdleChat()
	select {
	case <-runner.called:
	case <-time.After(30 * time.Millisecond):
		t.Fatal("worker restarted idle grace after idlechat released the LLM")
	}
}

func TestMemoryPromotionWorkerPausesIdleChatWhileHoldingIdleLease(t *testing.T) {
	tracker := newLLMBusyTracker()
	runner := &blockingMemoryPromotionRunnerStub{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	cancel := startMemoryPromotionWorkerRunner(
		runner, tracker, 5*time.Millisecond, time.Second, 5*time.Millisecond,
		backgroundJobFailureReporter{},
	)
	defer cancel()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("worker did not acquire the idle lease")
	}
	if !tracker.ExternalBusy() {
		t.Fatal("idle lease must be externally busy while ProfilePromotion is running")
	}

	close(runner.release)
	deadline := time.Now().Add(time.Second)
	for tracker.ExternalBusy() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tracker.ExternalBusy() {
		t.Fatal("idle lease remained externally busy after ProfilePromotion released it")
	}
}

func TestMemoryPromotionWorkerReservesAfterGraceAndDrainsAfterRunningIdleChat(t *testing.T) {
	tracker := newLLMBusyTracker()
	endRunningIdleChat := tracker.Begin(llm.WithBusySource(context.Background(), "idlechat"), "chat")
	idleChatEnded := false
	runner := &memoryPromotionRunnerStub{called: make(chan struct{}, 1)}
	cancel := startMemoryPromotionWorkerRunner(
		runner, tracker, 20*time.Millisecond, time.Second, 5*time.Millisecond,
		backgroundJobFailureReporter{},
	)
	defer cancel()
	defer func() {
		if !idleChatEnded {
			endRunningIdleChat()
		}
	}()

	// The first IdleChat remains active beyond idle grace. A new IdleChat must
	// be rejected after the worker has reserved its turn, while the first one
	// continues to own its existing busy slot.
	time.Sleep(50 * time.Millisecond)
	endNewIdleChat, ok := tracker.TryBegin(llm.WithBusySource(context.Background(), "idlechat"), "chat")
	if ok {
		endNewIdleChat()
		t.Fatal("new IdleChat was accepted after ProfilePromotion reservation")
	}

	endRunningIdleChat()
	idleChatEnded = true
	select {
	case <-runner.called:
	case <-time.After(time.Second):
		t.Fatal("ProfilePromotion did not run after the current IdleChat ended")
	}
}

func TestMemoryPromotionWorkerCancellationReleasesIdleReservation(t *testing.T) {
	tracker := newLLMBusyTracker()
	endRunningIdleChat := tracker.Begin(llm.WithBusySource(context.Background(), "idlechat"), "chat")
	runner := &memoryPromotionRunnerStub{called: make(chan struct{}, 1)}
	cancel := startMemoryPromotionWorkerRunner(
		runner, tracker, 20*time.Millisecond, time.Second, 5*time.Millisecond,
		backgroundJobFailureReporter{},
	)
	time.Sleep(50 * time.Millisecond)
	tracker.mu.Lock()
	reservationHeld := tracker.idleReservation != nil
	tracker.mu.Unlock()
	if !reservationHeld {
		t.Fatal("worker did not acquire an idle reservation before cancellation")
	}
	cancel()
	defer endRunningIdleChat()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		release, ok := tracker.TryReserveIdleReservation("other_background")
		if ok {
			release()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("worker cancellation did not release its idle reservation")
}
