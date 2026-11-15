package agentheartbeat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestWakeAfterRequestContextCancelUsesStartLifetimeContext(t *testing.T) {
	var eligible atomic.Bool
	var initialProbe atomic.Bool
	initialScan := make(chan struct{})
	run := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{Board: func(context.Context, string) (Work, bool, error) {
		if !eligible.Load() {
			if initialProbe.CompareAndSwap(false, true) {
				close(initialScan)
			}
			return Work{}, false, nil
		}
		return Work{ID: "committed-after-wake", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
			close(run)
			return nil
		}}, true, nil
	}}, nil)
	startCtx, stop := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(startCtx) }()
	defer func() { stop(); <-started }()
	waitSignal(t, initialScan, time.Second, "Start initial sweep did not probe BOARD")
	waitForScanStopped(t, scheduler, "mio")

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	release, acquired, err := scheduler.TryAcquire(requestCtx, "mio", domaintask.PriorityHigh)
	if err != nil {
		t.Fatalf("TryAcquire: %v", err)
	}
	if !acquired {
		t.Fatal("TryAcquire did not reserve the idle actor")
	}
	eligible.Store(true)
	if err := scheduler.Wake("mio"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	cancelRequest()
	release()
	waitSignal(t, run, time.Second, "Wake was lost when the short-lived Acquire context was canceled")
}

func TestWakeDuringScanIsRecheckedAfterCurrentScan(t *testing.T) {
	var calls atomic.Int32
	var eligible atomic.Bool
	entered := make(chan struct{})
	releaseProbe := make(chan struct{})
	run := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{Board: func(ctx context.Context, _ string) (Work, bool, error) {
		if calls.Add(1) == 1 {
			close(entered)
			select {
			case <-releaseProbe:
			case <-ctx.Done():
				return Work{}, false, ctx.Err()
			}
			return Work{}, false, nil
		}
		if !eligible.Load() {
			return Work{}, false, nil
		}
		return Work{ID: "wake-during-scan", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
			close(run)
			return nil
		}}, true, nil
	}}, nil)
	startCtx, stop := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(startCtx) }()
	defer func() { stop(); <-started }()
	waitSignal(t, entered, time.Second, "initial scan did not begin")
	eligible.Store(true)
	if err := scheduler.Wake("mio"); err != nil {
		t.Fatalf("Wake during scan: %v", err)
	}
	close(releaseProbe)
	waitSignal(t, run, time.Second, "Wake during an in-flight scan was lost")
}

func TestForegroundReleaseReplansWithoutWake(t *testing.T) {
	var eligible atomic.Bool
	var initialProbe atomic.Bool
	initialScan := make(chan struct{})
	run := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{Board: func(context.Context, string) (Work, bool, error) {
		if !eligible.Load() {
			if initialProbe.CompareAndSwap(false, true) {
				close(initialScan)
			}
			return Work{}, false, nil
		}
		return Work{ID: "arrived-during-foreground", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
			close(run)
			return nil
		}}, true, nil
	}}, nil)
	startCtx, stop := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(startCtx) }()
	defer func() { stop(); <-started }()
	waitSignal(t, initialScan, time.Second, "Start initial sweep did not probe BOARD")
	release, err := scheduler.Acquire(context.Background(), "mio", domaintask.PriorityNormal)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	eligible.Store(true)
	release()
	waitSignal(t, run, time.Second, "ordinary foreground release did not replan eligible work")
}

func TestPeriodicTickDuringPrivateRunRetainsIdlePermission(t *testing.T) {
	privateStarted := make(chan struct{})
	privateFinish := make(chan struct{})
	idleRan := make(chan struct{})
	var privateCalls atomic.Int32
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Private: func(context.Context, string) (Work, bool, error) {
			if privateCalls.Add(1) == 1 {
				return Work{ID: "private-held", Priority: domaintask.PriorityNormal, Run: func(context.Context, <-chan struct{}) error {
					close(privateStarted)
					<-privateFinish
					return nil
				}}, true, nil
			}
			return Work{}, false, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "idle-after-periodic", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				close(idleRan)
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, privateStarted, time.Second, "private work did not start")
	scheduler.Tick(context.Background())
	waitForScanStopped(t, scheduler, "mio")
	close(privateFinish)
	waitSignal(t, idleRan, time.Second, "completion rescan erased the pending periodic Idle permission")
}

func TestRunFailureIsReportedOnceAndNextPersistedCandidateRuns(t *testing.T) {
	for _, panicRun := range []bool{false, true} {
		name := "error"
		if panicRun {
			name = "panic"
		}
		t.Run(name, func(t *testing.T) {
			var state atomic.Int32
			var firstRuns atomic.Int32
			secondRan := make(chan struct{})
			reported := make(chan error, 2)
			scheduler := New([]string{"mio"}, Sources{Board: func(context.Context, string) (Work, bool, error) {
				if state.Load() == 0 {
					return Work{ID: "fail-once", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
						firstRuns.Add(1)
						state.Store(1)
						if panicRun {
							panic("owner failure")
						}
						return context.DeadlineExceeded
					}}, true, nil
				}
				return Work{ID: "next-persisted", Priority: domaintask.PriorityNormal, Run: func(context.Context, <-chan struct{}) error {
					close(secondRan)
					return nil
				}}, true, nil
			}}, func(_, stage string, err error) {
				if stage == "run" {
					reported <- err
				}
			})
			scheduler.Tick(context.Background())
			waitSignal(t, secondRan, time.Second, "next committed candidate did not run after failure")
			if got := firstRuns.Load(); got != 1 {
				t.Fatalf("failing candidate ran %d times in one series, want 1", got)
			}
			select {
			case err := <-reported:
				if err == nil {
					t.Fatal("reported run failure was nil")
				}
			case <-time.After(time.Second):
				t.Fatal("run failure was not reported")
			}
		})
	}
}

func TestExpectedRunCancellationIsNotReportedAsFailure(t *testing.T) {
	started := make(chan struct{})
	reports := make(chan error, 1)
	scheduler := New([]string{"mio"}, Sources{Board: func(context.Context, string) (Work, bool, error) {
		return Work{ID: "cancel-on-shutdown", Priority: domaintask.PriorityHigh, Run: func(ctx context.Context, _ <-chan struct{}) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		}}, true, nil
	}}, func(_, stage string, err error) {
		if stage == "run" {
			reports <- err
		}
	})
	scheduler.Tick(context.Background())
	waitSignal(t, started, time.Second, "cancellable work did not start")
	scheduler.shutdown()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := scheduler.Wait(ctx); err != nil {
		t.Fatalf("Wait after shutdown: %v", err)
	}
	select {
	case err := <-reports:
		t.Fatalf("expected cancellation was reported as failure: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
}

func TestIdleWorkCompletionReplansPrivateWithoutStartingAnotherIdle(t *testing.T) {
	var eligible atomic.Bool
	privateRan := make(chan struct{})
	var idleCalls atomic.Int32
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Private: func(context.Context, string) (Work, bool, error) {
			if !eligible.Load() {
				return Work{}, false, nil
			}
			return Work{ID: "private-published-by-idle-owner", Priority: domaintask.PriorityNormal, Run: func(context.Context, <-chan struct{}) error {
				close(privateRan)
				return nil
			}}, true, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			if idleCalls.Add(1) != 1 {
				return Work{}, false, nil
			}
			return Work{ID: "idle-owner", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				eligible.Store(true)
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, privateRan, time.Second, "Idle completion did not check newly committed private work")
	if got := idleCalls.Load(); got != 1 {
		t.Fatalf("Idle calls = %d, want only the initial timer-style scan", got)
	}
}

func TestIdleGroupCompletionReplansParticipantTaskSources(t *testing.T) {
	var eligible atomic.Bool
	participantRan := make(chan struct{})
	scheduler := New([]string{"mio", "shiro"}, Sources{
		Board: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "shiro" || !eligible.Load() {
				return Work{}, false, nil
			}
			return Work{ID: "participant-private-arrival", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				close(participantRan)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "mio" {
				return Work{}, false, nil
			}
			return Work{ID: "mio-shiro-group", Priority: domaintask.PriorityLow, Participants: []string{"shiro"}, Run: func(context.Context, <-chan struct{}) error {
				eligible.Store(true)
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, participantRan, time.Second, "group release did not replan participant task sources")
}

func TestPeriodicIdlePermissionSurvivesGroupParticipantScanCancellation(t *testing.T) {
	groupStarted := make(chan struct{})
	groupFinish := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(groupFinish) }) }
	secondIdleRan := make(chan struct{})
	var mioIdleCalls atomic.Int32
	scheduler := New([]string{"mio", "shiro"}, Sources{
		Board:   func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "mio" {
				return Work{}, false, nil
			}
			if mioIdleCalls.Add(1) == 1 {
				return Work{ID: "first-group", Priority: domaintask.PriorityLow, Participants: []string{"shiro"}, Run: func(context.Context, <-chan struct{}) error {
					close(groupStarted)
					<-groupFinish
					return nil
				}}, true, nil
			}
			return Work{ID: "second-idle-after-periodic", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				close(secondIdleRan)
				return nil
			}}, true, nil
		},
	}, nil)
	startCtx, stop := context.WithCancel(context.Background())
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(startCtx) }()
	defer func() { finish(); stop(); <-started }()
	waitSignal(t, groupStarted, time.Second, "idle group did not start")
	scheduler.Tick(context.Background())
	waitForScanStopped(t, scheduler, "mio")
	waitForScanStopped(t, scheduler, "shiro")
	if err := scheduler.Wake("shiro"); err != nil {
		t.Fatalf("Wake during active idle group: %v", err)
	}
	finish()
	waitSignal(t, secondIdleRan, time.Second, "participant scan/group release discarded the pending periodic Idle permission")
}

func TestGroupReservationRestoresDirtyParticipantTimerIdlePermission(t *testing.T) {
	startCtx, cancel := context.WithCancel(context.Background())
	groupStarted := make(chan struct{})
	groupFinish := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(groupFinish) }) }
	firstParticipantProbe := make(chan struct{})
	firstParticipantCanceled := make(chan struct{})
	participantIdleRan := make(chan struct{})
	var participantBoardCalls atomic.Int32
	var participantIdleCalls atomic.Int32
	scheduler := New([]string{"mio", "shiro"}, Sources{
		Board: func(ctx context.Context, actor string) (Work, bool, error) {
			if actor != "shiro" {
				return Work{}, false, nil
			}
			if participantBoardCalls.Add(1) == 1 {
				close(firstParticipantProbe)
				<-ctx.Done()
				close(firstParticipantCanceled)
				return Work{}, false, ctx.Err()
			}
			return Work{}, false, nil
		},
		Private: func(context.Context, string) (Work, bool, error) { return Work{}, false, nil },
		Idle: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "shiro" || participantIdleCalls.Add(1) != 1 {
				return Work{}, false, nil
			}
			return Work{ID: "participant-idle-from-pending-timer", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				close(participantIdleRan)
				return nil
			}}, true, nil
		},
	}, nil)
	scheduler.mu.Lock()
	scheduler.started = true
	scheduler.startCtx = startCtx
	primary := scheduler.agents["mio"]
	primary.scanGeneration = 1
	primary.scanTick = 7
	scheduler.mu.Unlock()
	defer func() {
		finish()
		cancel()
		scheduler.shutdown()
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := scheduler.Wait(waitCtx); err != nil {
			t.Errorf("Wait: %v", err)
		}
	}()

	scheduler.kickScanMode(startCtx, "shiro", 7, true)
	waitSignal(t, firstParticipantProbe, time.Second, "participant scan did not start")
	scheduler.kickScanMode(startCtx, "shiro", 8, true)
	scheduler.startIdleGroup(startCtx, "mio", Work{ID: "group-with-pending-participant-timer", Priority: domaintask.PriorityLow, Participants: []string{"shiro"}, Run: func(context.Context, <-chan struct{}) error {
		close(groupStarted)
		<-groupFinish
		return nil
	}}, 1, 7)
	waitSignal(t, firstParticipantCanceled, time.Second, "group admission did not cancel the participant scan")
	waitSignal(t, groupStarted, time.Second, "group did not reserve the participant")
	finish()
	waitSignal(t, participantIdleRan, time.Second, "group reservation lost the pending timer Idle permission")
}

func TestIdleGroupParticipantProbeDoesNotResetSeenBoardCandidateOrFallThrough(t *testing.T) {
	var boardRuns atomic.Int32
	var privateCalls atomic.Int32
	var idleCalls atomic.Int32
	scheduler := New([]string{"mio", "shiro"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "same-board-id", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				boardRuns.Add(1)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			privateCalls.Add(1)
			return Work{}, false, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			idleCalls.Add(1)
			return Work{}, false, nil
		},
	}, nil)
	scheduler.mu.Lock()
	primary := scheduler.agents["mio"]
	primary.scanGeneration = 1
	primary.scanTick = 7
	primary.seriesTick = 7
	primary.seriesSeen = map[string]struct{}{workKey(workSourceBoard, "same-board-id"): {}}
	participant := scheduler.agents["shiro"]
	participant.seriesTick = 7
	participant.seriesSeen = map[string]struct{}{workKey(workSourceBoard, "same-board-id"): {}}
	scheduler.mu.Unlock()
	scheduler.startIdleGroup(context.Background(), "mio", Work{ID: "group-candidate", Priority: domaintask.PriorityLow, Participants: []string{"shiro"}, Run: func(context.Context, <-chan struct{}) error { return nil }}, 1, 7)
	waitForScanStopped(t, scheduler, "shiro")
	if got := boardRuns.Load(); got != 0 {
		t.Fatalf("repeated BOARD candidate ran %d times", got)
	}
	if got := privateCalls.Load(); got != 0 {
		t.Fatalf("Private was probed %d times after a repeated BOARD candidate", got)
	}
	if got := idleCalls.Load(); got != 0 {
		t.Fatalf("Idle was probed %d times after a repeated BOARD candidate", got)
	}
	scheduler.mu.Lock()
	seriesTick := scheduler.agents["shiro"].seriesTick
	scheduler.mu.Unlock()
	if got := seriesTick; got != 7 {
		t.Fatalf("group participant scan reset its series to tick %d, want 7", got)
	}
}

func TestCanceledGrantedWaiterRescansOnStartContextWithoutIdleOrDuplicateRun(t *testing.T) {
	var runs atomic.Int32
	var privateCalls atomic.Int32
	var idleCalls atomic.Int32
	boardQueried := make(chan struct{})
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			select {
			case <-boardQueried:
			default:
				close(boardQueried)
			}
			return Work{ID: "already-visited", Priority: domaintask.PriorityHigh, Run: func(context.Context, <-chan struct{}) error {
				runs.Add(1)
				return nil
			}}, true, nil
		},
		Private: func(context.Context, string) (Work, bool, error) {
			privateCalls.Add(1)
			return Work{}, false, nil
		},
		Idle: func(context.Context, string) (Work, bool, error) {
			idleCalls.Add(1)
			return Work{}, false, nil
		},
	}, nil)
	startCtx, cancelStart := context.WithCancel(context.Background())
	defer cancelStart()
	lease := &reservation{scheduler: scheduler, actor: "mio", priority: domaintask.PriorityHigh, stop: make(chan struct{})}
	scheduler.mu.Lock()
	scheduler.started = true
	scheduler.startCtx = startCtx
	state := scheduler.agents["mio"]
	state.active = lease
	state.seriesTick = 3
	state.seriesSeen = map[string]struct{}{workKey(workSourceBoard, "already-visited"): {}}
	scheduler.mu.Unlock()
	if err := scheduler.Wake("mio"); err != nil {
		t.Fatalf("Wake: %v", err)
	}
	w := &waiter{ready: make(chan struct{}), lease: lease}
	scheduler.cancelWaiter(state, w)
	waitSignal(t, boardQueried, time.Second, "canceled waiter release did not requery BOARD")
	waitForScanStopped(t, scheduler, "mio")
	if got := runs.Load(); got != 0 {
		t.Fatalf("same-epoch candidate ran %d times after canceled waiter release", got)
	}
	if got := privateCalls.Load(); got != 0 {
		t.Fatalf("Private fallthrough calls = %d after same-ID BOARD candidate", got)
	}
	if got := idleCalls.Load(); got != 0 {
		t.Fatalf("Idle calls after foreground cancellation release = %d, want 0", got)
	}
	scheduler.mu.Lock()
	seriesTick := state.seriesTick
	scheduler.mu.Unlock()
	if seriesTick != 3 {
		t.Fatalf("foreground cancellation release reset epoch to %d, want 3", seriesTick)
	}
}
