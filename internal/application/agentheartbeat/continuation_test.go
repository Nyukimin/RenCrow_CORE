package agentheartbeat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestCompletedWorkImmediatelyPlansNextTasksFromCommittedSourceState(t *testing.T) {
	var boardCalls atomic.Int32
	var idleCalls atomic.Int32
	var mu sync.Mutex
	next := ""
	order := make(chan string, 3)
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			if boardCalls.Add(1) == 1 {
				return Work{ID: "first", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
					order <- "first"
					mu.Lock()
					next = "second"
					mu.Unlock()
					return nil
				}}, true, nil
			}
			return Work{}, false, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			mu.Lock()
			id := next
			mu.Unlock()
			if id == "" {
				return Work{}, false, nil
			}
			return Work{ID: id, Priority: domaintask.PriorityNormal, Run: func(context.Context, <-chan struct{}) error {
				order <- id
				mu.Lock()
				if id == "second" {
					next = "third"
				}
				mu.Unlock()
				return nil
			}}, true, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			idleCalls.Add(1)
			return Work{}, false, nil
		},
	}, nil)

	scheduler.Tick(context.Background())
	for _, want := range []string{"first", "second", "third"} {
		select {
		case got := <-order:
			if got != want {
				t.Fatalf("work order item = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("%s was not discovered after preceding completion without another Tick", want)
		}
	}
	if got := boardCalls.Load(); got < 2 {
		t.Fatalf("BOARD calls = %d, want a completion rescan", got)
	}
	if got := idleCalls.Load(); got != 0 {
		t.Fatalf("Idle calls during ordinary completion series = %d, want 0", got)
	}
}

func TestWakeBeforeStartIsDeferredToStartSweep(t *testing.T) {
	var calls atomic.Int32
	ran := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{Board: func(context.Context, string) (Work, bool, error) {
		calls.Add(1)
		return Work{ID: "wake-work", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
			close(ran)
			return nil
		}}, true, nil
	}}, nil)
	if err := scheduler.Wake("mio"); err != nil {
		t.Fatalf("Wake before Start: %v", err)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("source calls before Start = %d, want 0", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	waitSignal(t, ran, time.Second, "Start sweep did not observe the pre-Start Wake")
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after Start context cancellation")
	}
}

func TestWakeDoesNotResetRepeatedWorkSuppressionWithinSeries(t *testing.T) {
	var runs atomic.Int32
	started := make(chan struct{})
	runDone := make(chan struct{})
	secondRun := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{Board: func(context.Context, string) (Work, bool, error) {
		return Work{ID: "stale-candidate", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
			switch runs.Add(1) {
			case 1:
				close(started)
				close(runDone)
			case 2:
				close(secondRun)
			}
			return nil
		}}, true, nil
	}}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startedScheduler := make(chan error, 1)
	go func() { startedScheduler <- scheduler.Start(ctx) }()
	waitSignal(t, started, time.Second, "first candidate did not run")
	waitSignal(t, runDone, time.Second, "candidate did not finish")
	waitForScanStopped(t, scheduler, "mio")
	for i := 0; i < 8; i++ {
		if err := scheduler.Wake("mio"); err != nil {
			t.Fatalf("Wake %d: %v", i, err)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if got := runs.Load(); got != 1 {
		t.Fatalf("same source+ID ran %d times in one series after Wake hints, want 1", got)
	}
	scheduler.Tick(ctx)
	waitSignal(t, secondRun, time.Second, "new timer epoch did not make the same persisted candidate eligible again")
	if got := runs.Load(); got != 2 {
		t.Fatalf("same source+ID ran %d times after the next epoch, want 2", got)
	}
	cancel()
	select {
	case err := <-startedScheduler:
		if err != nil {
			t.Fatalf("Start returned %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop after cancellation")
	}
}
