package agentheartbeat

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestTryAcquireDoesNotQueueAheadOfBoardWork(t *testing.T) {
	ctx := context.Background()
	boardStarted := make(chan struct{})
	boardFinished := make(chan struct{})
	boardStop := make(chan struct{})
	finishBoard := make(chan struct{})
	var boardCalls atomic.Int32
	scheduler := New([]string{"shiro"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			if boardCalls.Add(1) > 1 {
				return Work{}, false, nil
			}
			return Work{ID: "board-1", Priority: domaintask.PriorityHigh, Run: func(_ context.Context, stop <-chan struct{}) error {
				close(boardStarted)
				defer close(boardFinished)
				select {
				case <-stop:
					close(boardStop)
					<-finishBoard
				case <-finishBoard:
				}
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(ctx)
	waitSignal(t, boardStarted, time.Second, "BOARD work did not start")

	if release, acquired, err := scheduler.TryAcquire(ctx, "shiro", domaintask.PriorityLow); err != nil || acquired {
		if release != nil {
			release()
		}
		t.Fatalf("TryAcquire during BOARD work = (%t, %v), want immediate deferral", acquired, err)
	}
	select {
	case <-boardStop:
		t.Fatal("low-priority legacy work requested a stop from BOARD work")
	default:
	}

	close(finishBoard)
	waitSignal(t, boardFinished, time.Second, "BOARD work did not finish")
	// Acquiring and releasing a normal slot waits for the scheduler to finish
	// its reservation cleanup and proves the refused TryAcquire was not queued.
	release, err := scheduler.Acquire(ctx, "shiro", domaintask.PriorityHigh)
	if err != nil {
		t.Fatalf("Acquire after BOARD work: %v", err)
	}
	release()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		release, acquired, err := scheduler.TryAcquire(ctx, "shiro", domaintask.PriorityLow)
		if err != nil {
			t.Fatalf("TryAcquire after BOARD work: %v", err)
		}
		if acquired {
			release()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("TryAcquire did not reserve the actor after BOARD released it")
}

func TestTryAcquireYieldsToInProgressBoardDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	boardStarted := make(chan struct{})
	continueBoard := make(chan struct{})
	boardCancelled := make(chan struct{})
	boardFinished := make(chan struct{})
	var boardCalls atomic.Int32
	scheduler := New([]string{"shiro"}, Sources{
		Board: func(ctx context.Context, _ string) (Work, bool, error) {
			if boardCalls.Add(1) > 1 {
				return Work{}, false, nil
			}
			close(boardStarted)
			select {
			case <-ctx.Done():
				close(boardCancelled)
			case <-continueBoard:
				close(boardFinished)
			}
			return Work{}, false, nil
		},
	}, nil)
	scheduler.Tick(ctx)
	waitSignal(t, boardStarted, time.Second, "BOARD discovery did not start")
	if release, acquired, err := scheduler.TryAcquire(ctx, "shiro", domaintask.PriorityLow); err != nil || acquired {
		if release != nil {
			release()
		}
		t.Fatalf("TryAcquire during BOARD discovery acquired=%t err=%v, want immediate deferral", acquired, err)
	}
	select {
	case <-boardCancelled:
		t.Fatal("low-priority legacy work canceled BOARD discovery")
	default:
	}
	close(continueBoard)
	waitSignal(t, boardFinished, time.Second, "BOARD discovery did not complete")

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		release, acquired, err := scheduler.TryAcquire(ctx, "shiro", domaintask.PriorityLow)
		if err != nil {
			t.Fatalf("TryAcquire after BOARD discovery: %v", err)
		}
		if acquired {
			release()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("TryAcquire did not reserve the actor after BOARD discovery completed")
}

func TestTryAcquireReservesAnIdleActorSlot(t *testing.T) {
	scheduler := New([]string{"shiro"}, Sources{}, nil)
	release, acquired, err := scheduler.TryAcquire(context.Background(), "shiro", domaintask.PriorityLow)
	if err != nil || !acquired || release == nil {
		t.Fatalf("TryAcquire(idle actor) acquired=%t release-nil=%t err=%v, want a reservation", acquired, release == nil, err)
	}
	if second, ok, err := scheduler.TryAcquire(context.Background(), "shiro", domaintask.PriorityLow); err != nil || ok {
		if second != nil {
			second()
		}
		t.Fatalf("second TryAcquire acquired=%t err=%v, want immediate deferral", ok, err)
	}
	release()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		next, ok, err := scheduler.TryAcquire(context.Background(), "shiro", domaintask.PriorityLow)
		if err != nil {
			t.Fatalf("TryAcquire after release: %v", err)
		}
		if ok {
			next()
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("TryAcquire after release did not return the slot")
}

func TestTryAcquireReleaseRechecksActorWork(t *testing.T) {
	boardRechecked := make(chan struct{})
	var boardCalls atomic.Int32
	scheduler := New([]string{"shiro"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			if boardCalls.Add(1) == 1 {
				close(boardRechecked)
			}
			return Work{}, false, nil
		},
	}, nil)
	release, acquired, err := scheduler.TryAcquire(context.Background(), "shiro", domaintask.PriorityLow)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire() acquired=%t err=%v, want the slot", acquired, err)
	}
	release()
	waitSignal(t, boardRechecked, time.Second, "releasing the low-priority lease did not recheck BOARD work")
}
