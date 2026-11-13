package agentheartbeat

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestSelectUsesPriorityAgeAndStableID(t *testing.T) {
	base := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	got, ok := Select([]Work{
		{ID: "normal-old", Priority: domaintask.PriorityNormal, CreatedAt: base},
		{ID: "critical-new", Priority: domaintask.PriorityCritical, CreatedAt: base.Add(time.Hour)},
		{ID: "high-later-id", Priority: domaintask.PriorityHigh, CreatedAt: base},
		{ID: "high-z", Priority: domaintask.PriorityHigh, CreatedAt: base.Add(-time.Hour)},
		{ID: "high-a", Priority: domaintask.PriorityHigh, CreatedAt: base.Add(-time.Hour)},
	})
	if !ok || got.ID != "critical-new" {
		t.Fatalf("Select() = (%+v, %t), want the critical-priority item", got, ok)
	}
	got, ok = Select([]Work{
		{ID: "high-later-id", Priority: domaintask.PriorityHigh, CreatedAt: base},
		{ID: "high-z", Priority: domaintask.PriorityHigh, CreatedAt: base.Add(-time.Hour)},
		{ID: "high-a", Priority: domaintask.PriorityHigh, CreatedAt: base.Add(-time.Hour)},
	})
	if !ok || got.ID != "high-a" {
		t.Fatalf("Select() = (%+v, %t), want oldest high-priority item with stable ID high-a", got, ok)
	}
	if _, ok := Select(nil); ok {
		t.Fatal("Select(nil) reported a work item")
	}
}

func TestTickIsolatesBlockedAgentFromOtherAgent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	alphaEntered := make(chan struct{})
	betaRan := make(chan struct{})
	scheduler := New([]string{"alpha", "beta"}, Sources{
		Board: func(ctx context.Context, actor string) (Work, bool, error) {
			if actor == "alpha" {
				close(alphaEntered)
				<-ctx.Done()
				return Work{}, false, ctx.Err()
			}
			return Work{}, false, nil
		},
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "beta" {
				return Work{}, false, nil
			}
			return Work{ID: "beta-idle", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				close(betaRan)
				return nil
			}}, true, nil
		},
	}, nil)

	scheduler.Tick(ctx)
	waitSignal(t, alphaEntered, time.Second, "alpha source was not called")
	waitSignal(t, betaRan, time.Second, "blocked alpha discovery stalled beta")
}

func TestTickReportsBoardErrorAndFallsThroughToPrivate(t *testing.T) {
	boardReported := make(chan error, 1)
	privateRan := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{}, false, errors.New("board unavailable")
		},
		Private: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "private-1", Priority: domaintask.PriorityNormal, Run: func(context.Context, <-chan struct{}) error {
				close(privateRan)
				return nil
			}}, true, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			t.Error("idle activity ran while private work was available")
			return Work{}, false, nil
		},
	}, func(actor, stage string, err error) {
		if actor == "mio" && stage == "board" {
			boardReported <- err
		}
	})

	scheduler.Tick(context.Background())
	waitSignal(t, privateRan, time.Second, "private work did not run after BOARD error")
	select {
	case err := <-boardReported:
		if err == nil || err.Error() != "board unavailable" {
			t.Fatalf("reported BOARD error = %v, want original source error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BOARD source error was not reported")
	}
}

func TestTickRunsBoardWorkBeforePrivateOrIdle(t *testing.T) {
	boardRan := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "board-1", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				close(boardRan)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			t.Error("private source ran despite available BOARD work")
			return Work{}, false, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			t.Error("idle source ran despite available BOARD work")
			return Work{}, false, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, boardRan, time.Second, "BOARD work did not run")
}

func TestTickUsesIdleOnlyWhenBothTaskSourcesAreEmpty(t *testing.T) {
	idleRan := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{
		Board:   func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "idle", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				close(idleRan)
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, idleRan, time.Second, "idle activity did not run after both task queues were empty")
}

func TestTickDoesNotStartIdleAfterPrivateSourceError(t *testing.T) {
	privateReported := make(chan error, 1)
	idleCalls := make(chan struct{}, 1)
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Private: func(context.Context, string) (Work, bool, error) {
			return Work{}, false, errors.New("private queue unavailable")
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			idleCalls <- struct{}{}
			return Work{}, false, nil
		},
	}, func(_, stage string, err error) {
		if stage == "private" {
			privateReported <- err
		}
	})
	scheduler.Tick(context.Background())
	select {
	case err := <-privateReported:
		if err == nil || err.Error() != "private queue unavailable" {
			t.Fatalf("reported private error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("private source error was not reported")
	}
	select {
	case <-idleCalls:
		t.Fatal("idle work ran without a successful empty private scan")
	case <-time.After(30 * time.Millisecond):
	}
}

func TestTickRejectsFoundWorkAfterSourceDeadline(t *testing.T) {
	staleWorkRan := make(chan struct{})
	privateWorkRan := make(chan struct{})
	boardReported := make(chan error, 1)
	scheduler := New([]string{"mio"}, Sources{
		Board: func(ctx context.Context, _ string) (Work, bool, error) {
			<-ctx.Done()
			return Work{ID: "stale", Priority: domaintask.PriorityCritical, Run: func(context.Context, <-chan struct{}) error {
				close(staleWorkRan)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "private", Priority: domaintask.PriorityNormal, Run: func(context.Context, <-chan struct{}) error {
				close(privateWorkRan)
				return nil
			}}, true, nil
		},
	}, func(_, stage string, err error) {
		if stage == "board" {
			boardReported <- err
		}
	})
	scheduler.Tick(context.Background())
	select {
	case <-privateWorkRan:
	case <-staleWorkRan:
		t.Fatal("BOARD result was accepted after its discovery deadline")
	case <-time.After(3 * time.Second):
		t.Fatal("timed-out BOARD scan neither fell through to private work nor returned")
	}
	select {
	case err := <-boardReported:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("BOARD timeout report = %v, want context deadline exceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("BOARD timeout was not reported")
	}
}

func TestAcquireCancelsInFlightDiscovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scanEntered := make(chan struct{})
	scanCanceled := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{
		Board: func(ctx context.Context, _ string) (Work, bool, error) {
			close(scanEntered)
			<-ctx.Done()
			close(scanCanceled)
			return Work{}, false, ctx.Err()
		},
	}, nil)
	scheduler.Tick(ctx)
	waitSignal(t, scanEntered, time.Second, "BOARD discovery did not start")
	release, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityNormal)
	if err != nil {
		t.Fatalf("Acquire() = %v", err)
	}
	defer release()
	waitSignal(t, scanCanceled, time.Second, "foreground acquisition did not cancel the outstanding discovery")
}

func TestTickContinuesTaskProbesDuringIdleWithoutStartingAnotherIdle(t *testing.T) {
	var boardCalls atomic.Int32
	var idleCalls atomic.Int32
	idleStarted := make(chan struct{})
	idleStopped := make(chan struct{})
	finish := make(chan struct{})
	var finishOnce sync.Once
	finishIdle := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishIdle()
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			boardCalls.Add(1)
			return Work{}, false, nil
		},
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(context.Context, string) (Work, bool, error) {
			idleCalls.Add(1)
			return Work{ID: "leisure", Priority: domaintask.PriorityLow, Run: func(_ context.Context, stop <-chan struct{}) error {
				close(idleStarted)
				defer close(idleStopped)
				select {
				case <-stop:
					<-finish
					return nil
				case <-finish:
					return nil
				}
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, idleStarted, time.Second, "idle activity did not start")
	scheduler.Tick(context.Background())
	deadline := time.Now().Add(time.Second)
	for boardCalls.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if boardCalls.Load() < 2 {
		t.Fatal("BOARD was not probed during active idle work")
	}
	if got := idleCalls.Load(); got != 1 {
		t.Fatalf("idle source calls while leisure was active = %d, want 1", got)
	}
	finishIdle()
	waitSignal(t, idleStopped, time.Second, "idle work did not finish")
}

func TestTickPreemptsLowerPriorityPrivateRunForHigherPriorityBoardWork(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	privateStarted := make(chan struct{})
	privateStopSeen := make(chan struct{})
	releasePrivate := make(chan struct{})
	privateReturned := make(chan struct{})
	boardProbeDuringPrivate := make(chan struct{})
	boardStarted := make(chan struct{})
	releaseBoard := make(chan struct{})
	boardReturned := make(chan struct{})
	var releasePrivateOnce sync.Once
	var releaseBoardOnce sync.Once
	defer releasePrivateOnce.Do(func() { close(releasePrivate) })
	defer releaseBoardOnce.Do(func() { close(releaseBoard) })

	var boardCalls atomic.Int32
	var privateCalls atomic.Int32
	var idleCalls atomic.Int32
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			call := boardCalls.Add(1)
			if call == 1 {
				return Work{}, false, nil
			}
			if call == 2 {
				close(boardProbeDuringPrivate)
			}
			return Work{ID: "board-high", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				if boardCalls.Load() < 3 {
					t.Errorf("BOARD candidate ran after %d scans, want a fresh scan after interrupt", boardCalls.Load())
				}
				select {
				case <-privateReturned:
				default:
					t.Error("BOARD work started before the private run returned")
				}
				close(boardStarted)
				<-releaseBoard
				close(boardReturned)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			if privateCalls.Add(1) != 1 {
				return Work{}, false, nil
			}
			return heldPrivateWork("private-normal", domaintask.PriorityNormal, privateStarted, privateStopSeen, releasePrivate, privateReturned), true, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			idleCalls.Add(1)
			return Work{}, false, nil
		},
	}, nil)

	scheduler.Tick(ctx)
	waitSignal(t, privateStarted, time.Second, "private background work did not start")
	scheduler.Tick(ctx)
	waitSignal(t, boardProbeDuringPrivate, time.Second, "BOARD was not probed during active private work")
	waitSignal(t, privateStopSeen, time.Second, "higher-priority BOARD candidate did not request the private run to stop")
	if got := privateCalls.Load(); got != 1 {
		t.Fatalf("private source calls during the active private run = %d, want 1", got)
	}
	if got := idleCalls.Load(); got != 0 {
		t.Fatalf("idle source calls while work was active = %d, want 0", got)
	}
	select {
	case <-boardStarted:
		t.Fatal("BOARD work started before the private run returned")
	default:
	}

	releasePrivateOnce.Do(func() { close(releasePrivate) })
	waitSignal(t, privateReturned, time.Second, "private run did not return after its checkpoint was released")
	waitSignal(t, boardStarted, time.Second, "BOARD candidate was not revalidated and started after the private run returned")
	releaseBoardOnce.Do(func() { close(releaseBoard) })
	waitSignal(t, boardReturned, time.Second, "BOARD work did not finish")
}

func TestTickDoesNotPreemptPrivateRunForEqualPriorityBoardWork(t *testing.T) {
	privateStarted := make(chan struct{})
	privateStopSeen := make(chan struct{})
	finishPrivate := make(chan struct{})
	privateReturned := make(chan struct{})
	boardProbeDuringPrivate := make(chan struct{})
	boardStarted := make(chan struct{})
	releaseBoard := make(chan struct{})
	boardReturned := make(chan struct{})
	var releasePrivateOnce sync.Once
	var releaseBoardOnce sync.Once
	defer releasePrivateOnce.Do(func() { close(finishPrivate) })
	defer releaseBoardOnce.Do(func() { close(releaseBoard) })

	var boardCalls atomic.Int32
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			call := boardCalls.Add(1)
			if call == 1 {
				return Work{}, false, nil
			}
			if call == 2 {
				close(boardProbeDuringPrivate)
			}
			return Work{ID: "board-high", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				if boardCalls.Load() < 3 {
					t.Errorf("equal-priority BOARD work ran after %d scans, want a fresh scan after private completion", boardCalls.Load())
				}
				close(boardStarted)
				<-releaseBoard
				close(boardReturned)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			return heldPrivateWork("private-high", domaintask.PriorityHigh, privateStarted, privateStopSeen, finishPrivate, privateReturned), true, nil
		},
	}, nil)

	scheduler.Tick(context.Background())
	waitSignal(t, privateStarted, time.Second, "private background work did not start")
	scheduler.Tick(context.Background())
	waitSignal(t, boardProbeDuringPrivate, time.Second, "BOARD was not probed during active private work")
	waitForScanStopped(t, scheduler, "mio")
	select {
	case <-privateStopSeen:
		t.Fatal("equal-priority BOARD work stopped the private run")
	case <-time.After(30 * time.Millisecond):
	}
	scheduler.mu.Lock()
	rescanAfterRun := scheduler.agents["mio"].rescanAfterRun
	scheduler.mu.Unlock()
	if rescanAfterRun {
		t.Fatal("equal-priority BOARD work scheduled an unnecessary rescan after the private run")
	}
	select {
	case <-boardStarted:
		t.Fatal("equal-priority BOARD work started before the private run returned")
	default:
	}

	releasePrivateOnce.Do(func() { close(finishPrivate) })
	waitSignal(t, privateReturned, time.Second, "private run did not finish")
	waitForReservationReleased(t, scheduler, "mio")
	if got := boardCalls.Load(); got != 2 {
		t.Fatalf("BOARD scans after equal-priority work finished = %d, want no immediate rescan", got)
	}
	scheduler.Tick(context.Background())
	waitSignal(t, boardStarted, time.Second, "BOARD work did not start after the private run finished")
	releaseBoardOnce.Do(func() { close(releaseBoard) })
	waitSignal(t, boardReturned, time.Second, "BOARD work did not finish")
}

func TestAcquireInvalidatesPrivatePriorityScanBeforeForegroundReservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	privateStarted := make(chan struct{})
	privateStopSeen := make(chan struct{})
	releasePrivate := make(chan struct{})
	privateReturned := make(chan struct{})
	boardScanStarted := make(chan struct{})
	boardScanCanceled := make(chan struct{})
	releaseBoardScan := make(chan struct{})
	boardRun := make(chan struct{})
	var releasePrivateOnce sync.Once
	var releaseBoardScanOnce sync.Once
	defer releasePrivateOnce.Do(func() { close(releasePrivate) })
	defer releaseBoardScanOnce.Do(func() { close(releaseBoardScan) })

	var boardCalls atomic.Int32
	scheduler := New([]string{"mio"}, Sources{
		Board: func(ctx context.Context, _ string) (Work, bool, error) {
			call := boardCalls.Add(1)
			if call == 1 {
				return Work{}, false, nil
			}
			if call != 2 {
				return Work{}, false, nil
			}
			close(boardScanStarted)
			<-ctx.Done()
			close(boardScanCanceled)
			<-releaseBoardScan
			return Work{ID: "stale-board", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				close(boardRun)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			return heldPrivateWork("private-normal", domaintask.PriorityNormal, privateStarted, privateStopSeen, releasePrivate, privateReturned), true, nil
		},
	}, nil)

	scheduler.Tick(ctx)
	waitSignal(t, privateStarted, time.Second, "private background work did not start")
	scheduler.Tick(ctx)
	waitSignal(t, boardScanStarted, time.Second, "BOARD-only priority scan did not start")

	acquired := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		release, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityHigh)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- release
	}()
	waitSignal(t, boardScanCanceled, time.Second, "foreground Acquire did not cancel the stale BOARD scan")
	waitSignal(t, privateStopSeen, time.Second, "foreground Acquire did not request the private run to stop")
	select {
	case release := <-acquired:
		release()
		t.Fatal("foreground reservation was granted before the private run returned")
	case err := <-acquireErr:
		t.Fatalf("Acquire() returned before the private run returned: %v", err)
	default:
	}

	releaseBoardScanOnce.Do(func() { close(releaseBoardScan) })
	waitForScanStopped(t, scheduler, "mio")
	select {
	case <-boardRun:
		t.Fatal("a stale BOARD candidate ran after its scan was canceled")
	default:
	}
	releasePrivateOnce.Do(func() { close(releasePrivate) })
	waitSignal(t, privateReturned, time.Second, "private run did not return after its checkpoint")
	release := receive(t, acquired, time.Second, "foreground reservation did not acquire after the private run returned")
	defer func() {
		cancel()
		release()
	}()
	scheduler.Tick(ctx)
	if got := boardCalls.Load(); got != 2 {
		t.Fatalf("BOARD scans while the foreground reservation was active = %d, want 2", got)
	}
}

func TestTickSkipsNonIdleWorkAndForegroundOwners(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var boardCalls atomic.Int32
	runStarted := make(chan struct{})
	stopSeen := make(chan struct{})
	finish := make(chan struct{})
	var finishOnce sync.Once
	finishRun := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishRun()
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			if boardCalls.Add(1) != 1 {
				return Work{}, false, nil
			}
			return Work{ID: "task", Priority: domaintask.PriorityNormal, Run: func(_ context.Context, stop <-chan struct{}) error {
				close(runStarted)
				<-stop
				close(stopSeen)
				<-finish
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(ctx)
	waitSignal(t, runStarted, time.Second, "background work did not start")

	acquired := make(chan func(), 1)
	go func() {
		release, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityHigh)
		if err == nil {
			acquired <- release
		}
	}()
	waitSignal(t, stopSeen, time.Second, "foreground waiter did not request task stop")
	scheduler.Tick(ctx)
	<-time.After(30 * time.Millisecond)
	if got := boardCalls.Load(); got != 1 {
		t.Fatalf("BOARD scans while a non-idle run and waiter are active = %d, want 1", got)
	}
	finishRun()
	release := receive(t, acquired, time.Second, "foreground waiter did not acquire after task finalized")
	scheduler.Tick(ctx)
	if got := boardCalls.Load(); got != 1 {
		t.Fatalf("BOARD scans while a foreground owner is active = %d, want 1", got)
	}
	release()
}

func TestHigherPriorityAcquireWaitsForWorkFinalization(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started := make(chan struct{})
	stopSeen := make(chan struct{})
	finish := make(chan struct{})
	var scheduler *Scheduler
	scheduler = New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "task", Priority: domaintask.PriorityNormal, Run: func(ctx context.Context, stop <-chan struct{}) error {
				close(started)
				select {
				case <-stop:
					close(stopSeen)
					<-finish
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(ctx)
	waitSignal(t, started, time.Second, "background task did not start")

	acquired := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		release, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityHigh)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- release
	}()
	waitSignal(t, stopSeen, time.Second, "higher priority request did not ask the active task to stop")
	select {
	case release := <-acquired:
		release()
		t.Fatal("foreground slot was granted before the active run finalized")
	case err := <-acquireErr:
		t.Fatalf("Acquire() returned before finalization: %v", err)
	case <-time.After(30 * time.Millisecond):
	}

	close(finish)
	select {
	case release := <-acquired:
		release()
	case err := <-acquireErr:
		t.Fatalf("Acquire() = %v", err)
	case <-time.After(time.Second):
		t.Fatal("foreground slot was not granted after the run finalized")
	}
}

func TestTickStopsIdleActivityForNewTaskAndWaitsForReturn(t *testing.T) {
	var boardCalls atomic.Int32
	idleStarted := make(chan struct{})
	idleStopSeen := make(chan struct{})
	idleFinish := make(chan struct{})
	var idleFinishOnce sync.Once
	finishIdle := func() { idleFinishOnce.Do(func() { close(idleFinish) }) }
	defer finishIdle()
	taskRan := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			if boardCalls.Add(1) == 1 {
				return Work{}, false, nil
			}
			return Work{ID: "arrived", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				close(taskRan)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "idle", Priority: domaintask.PriorityLow, Run: func(ctx context.Context, stop <-chan struct{}) error {
				close(idleStarted)
				select {
				case <-stop:
					close(idleStopSeen)
					<-idleFinish
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}, true, nil
		},
	}, nil)

	scheduler.Tick(context.Background())
	waitSignal(t, idleStarted, time.Second, "idle activity did not start")
	scheduler.Tick(context.Background())
	waitSignal(t, idleStopSeen, time.Second, "new BOARD work did not request idle activity to stop")
	select {
	case <-taskRan:
		t.Fatal("new BOARD work started before idle activity returned")
	case <-time.After(30 * time.Millisecond):
	}
	finishIdle()
	waitSignal(t, taskRan, time.Second, "new BOARD work did not start after idle activity returned")
}

func TestAcquirePrioritizesWaitersAndPreservesFIFOWithinPriority(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	scheduler := New([]string{"mio"}, Sources{}, nil)
	baseRelease, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityNormal)
	if err != nil {
		t.Fatalf("initial Acquire() = %v", err)
	}

	type waiter struct {
		name     string
		priority domaintask.Priority
	}
	queued := []waiter{
		{name: "normal", priority: domaintask.PriorityNormal},
		{name: "high-1", priority: domaintask.PriorityHigh},
		{name: "high-2", priority: domaintask.PriorityHigh},
	}
	entered := make(chan string, len(queued))
	releases := make(map[string]chan struct{}, len(queued))
	for _, item := range queued {
		item := item
		releases[item.name] = make(chan struct{})
		go func() {
			release, err := scheduler.Acquire(ctx, "mio", item.priority)
			if err != nil {
				entered <- "error: " + err.Error()
				return
			}
			entered <- item.name
			<-releases[item.name]
			release()
		}()
		waitForQueued(t, scheduler, "mio", len(releases))
	}

	baseRelease()
	for _, want := range []string{"high-1", "high-2", "normal"} {
		select {
		case got := <-entered:
			if got != want {
				t.Fatalf("next waiter = %q, want %q", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("waiter %q was not granted", want)
		}
		close(releases[want])
	}
}

func TestRunContextReentryIsExactAndNestedReleaseDoesNotFreeSlot(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	nested := make(chan error, 1)
	finish := make(chan struct{})
	started := make(chan struct{})
	var scheduler *Scheduler
	scheduler = New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "nested", Priority: domaintask.PriorityNormal, Run: func(runCtx context.Context, _ <-chan struct{}) error {
				release, err := scheduler.Acquire(runCtx, "mio", domaintask.PriorityCritical)
				if err == nil {
					release()
				}
				nested <- err
				close(started)
				<-finish
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(ctx)
	waitSignal(t, started, time.Second, "work did not test active-context reentry")
	select {
	case err := <-nested:
		if err != nil {
			t.Fatalf("same-run Acquire() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("same-run Acquire() did not return immediately")
	}

	acquired := make(chan func(), 1)
	go func() {
		release, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityNormal)
		if err == nil {
			acquired <- release
		}
	}()
	waitForQueued(t, scheduler, "mio", 1)
	select {
	case release := <-acquired:
		release()
		t.Fatal("nested no-op release freed the actual work slot")
	case <-time.After(30 * time.Millisecond):
	}
	close(finish)
	select {
	case release := <-acquired:
		release()
	case <-time.After(time.Second):
		t.Fatal("queued foreground work did not acquire after Work.Run returned")
	}
}

func TestStaleRunContextCannotBypassNewReservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	firstCtx := make(chan context.Context, 1)
	firstDone := make(chan struct{})
	secondStarted := make(chan struct{})
	secondStop := make(chan struct{})
	secondFinish := make(chan struct{})
	var mu sync.Mutex
	second := false
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			mu.Lock()
			isSecond := second
			mu.Unlock()
			if !isSecond {
				return Work{ID: "first", Priority: domaintask.PriorityLow, Run: func(runCtx context.Context, _ <-chan struct{}) error {
					firstCtx <- context.WithoutCancel(runCtx)
					close(firstDone)
					return nil
				}}, true, nil
			}
			return Work{ID: "second", Priority: domaintask.PriorityNormal, Run: func(_ context.Context, stop <-chan struct{}) error {
				close(secondStarted)
				<-stop
				close(secondStop)
				<-secondFinish
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(ctx)
	staleCtx := receive(t, firstCtx, time.Second, "first run context was not recorded")
	waitSignal(t, firstDone, time.Second, "first run did not finish")
	mu.Lock()
	second = true
	mu.Unlock()
	scheduler.Tick(ctx)
	waitSignal(t, secondStarted, time.Second, "second reservation did not start")

	acquired := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		release, err := scheduler.Acquire(staleCtx, "mio", domaintask.PriorityCritical)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- release
	}()
	waitSignal(t, secondStop, time.Second, "stale run context bypassed instead of waiting behind the new owner")
	select {
	case release := <-acquired:
		release()
		t.Fatal("stale reservation token acquired while a newer owner was still active")
	case err := <-acquireErr:
		t.Fatalf("stale-context Acquire() returned early: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(secondFinish)
	select {
	case release := <-acquired:
		release()
	case err := <-acquireErr:
		t.Fatalf("stale-context Acquire() = %v", err)
	case <-time.After(time.Second):
		t.Fatal("stale-context waiter did not acquire after the new owner finished")
	}
}

func TestStartTicksImmediatelyAndClosesOnShutdown(t *testing.T) {
	if Interval != 10*time.Second {
		t.Fatalf("Interval = %s, want 10s", Interval)
	}
	ctx, cancel := context.WithCancel(context.Background())
	probed := make(chan struct{}, 1)
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			probed <- struct{}{}
			return Work{}, false, nil
		},
	}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	waitSignal(t, probed, time.Second, "Start() did not run an initial heartbeat")
	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start() did not return on context cancellation")
	}
	if _, err := scheduler.Acquire(context.Background(), "mio", domaintask.PriorityNormal); !errors.Is(err, ErrSchedulerClosed) {
		t.Fatalf("Acquire() after Start shutdown = %v, want ErrSchedulerClosed", err)
	}
}

func TestWaitDrainsBackgroundRunAfterStartShutdown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	stopped := make(chan struct{})
	finish := make(chan struct{})
	var finishOnce sync.Once
	finishRun := func() { finishOnce.Do(func() { close(finish) }) }
	defer finishRun()
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "owned", Priority: domaintask.PriorityNormal, Run: func(_ context.Context, stop <-chan struct{}) error {
				close(started)
				<-stop
				close(stopped)
				<-finish
				return nil
			}}, true, nil
		},
	}, nil)
	startReturned := make(chan struct{})
	go func() {
		_ = scheduler.Start(ctx)
		close(startReturned)
	}()
	waitSignal(t, started, time.Second, "background run did not start")
	cancel()
	waitSignal(t, startReturned, time.Second, "Start() did not return on cancellation")
	waitSignal(t, stopped, time.Second, "shutdown did not request background work to stop")

	waitCtx, waitCancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	err := scheduler.Wait(waitCtx)
	waitCancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		finishRun()
		t.Fatalf("Wait() before Work.Run returned = %v, want deadline exceeded", err)
	}
	finishRun()
	if err := scheduler.Wait(context.Background()); err != nil {
		t.Fatalf("Wait() after Work.Run returned = %v", err)
	}
}

func waitSignal(t *testing.T, ch <-chan struct{}, timeout time.Duration, message string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(timeout):
		t.Fatal(message)
	}
}

func receive[T any](t *testing.T, ch <-chan T, timeout time.Duration, message string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(timeout):
		var zero T
		t.Fatal(message)
		return zero
	}
}

func waitForQueued(t *testing.T, scheduler *Scheduler, actor string, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		state := scheduler.agents[actor]
		queued := 0
		if state != nil {
			queued = len(state.waiters)
		}
		scheduler.mu.Unlock()
		if queued == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("queued waiter count did not reach %d", want)
}

func waitForScanStopped(t *testing.T, scheduler *Scheduler, actor string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		state := scheduler.agents[actor]
		scanning := state != nil && state.scanning
		scheduler.mu.Unlock()
		if !scanning {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("canceled BOARD scan did not finish")
}

func waitForReservationReleased(t *testing.T, scheduler *Scheduler, actor string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		scheduler.mu.Lock()
		state := scheduler.agents[actor]
		active := state != nil && state.active != nil
		scheduler.mu.Unlock()
		if !active {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("actor reservation was not released")
}

func heldPrivateWork(id string, priority domaintask.Priority, started, stopSeen, finish, returned chan struct{}) Work {
	return Work{ID: id, Priority: priority, Run: func(ctx context.Context, stop <-chan struct{}) error {
		close(started)
		defer close(returned)
		select {
		case <-stop:
			close(stopSeen)
			<-finish
		case <-finish:
		case <-ctx.Done():
			return ctx.Err()
		}
		return nil
	}}
}
