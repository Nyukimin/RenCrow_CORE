package agentheartbeat

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func TestGameGroupReservationIsAtomicAndFencesLegacyAcquisition(t *testing.T) {
	scheduler := New([]string{"mio", "shiro"}, Sources{}, nil)
	legacyRelease, acquired, err := scheduler.TryAcquire(context.Background(), "mio", domaintask.PriorityNormal)
	if err != nil || !acquired {
		t.Fatalf("TryAcquire before group = (%t, %v), want acquired", acquired, err)
	}
	if reserved, err := scheduler.TryReserveGameGroup(context.Background(), "task-1", "survival_garden", []string{"mio", "shiro"}); err != nil || reserved {
		t.Fatalf("TryReserve while mio active = (%t, %v), want atomic rejection", reserved, err)
	}
	if release, ok, err := scheduler.TryAcquire(context.Background(), "shiro", domaintask.PriorityNormal); err != nil || !ok {
		if release != nil {
			release()
		}
		t.Fatalf("failed group reservation partially claimed shiro: acquired=%t err=%v", ok, err)
	} else {
		release()
	}
	legacyRelease()
	for _, actor := range []string{"mio", "shiro"} {
		waitForAgentState(t, scheduler, actor, func(state *agentState) bool {
			return !state.scanning && state.active == nil && len(state.waiters) == 0
		})
	}

	if reserved, err := scheduler.TryReserveGameGroup(context.Background(), "task-1", "survival_garden", []string{"mio", "shiro"}); err != nil || !reserved {
		t.Fatalf("TryReserve after both actors idle = (%t, %v), want success", reserved, err)
	}
	if ok, err := scheduler.CanLaunchLegacyGame([]string{"mio", "shiro"}); err != nil || ok {
		t.Fatalf("legacy launch during Task group = (%t, %v), want rejected", ok, err)
	}
	for _, actor := range []string{"mio", "shiro"} {
		if scheduler.CanStartIdle(actor) {
			t.Fatalf("CanStartIdle(%q) allowed a reserved actor", actor)
		}
		if release, ok, err := scheduler.TryAcquire(context.Background(), actor, domaintask.PriorityNormal); err != nil || ok {
			if release != nil {
				release()
			}
			t.Fatalf("TryAcquire(%q) during group = (%t, %v), want blocked", actor, ok, err)
		}
	}
	if _, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityHigh, "survival_garden"); !errors.Is(err, ErrGameActorReserved) {
		t.Fatalf("legacy AcquireGameTurn during Task group = %v, want ErrGameActorReserved", err)
	}
}

func TestGameGroupTurnBypassesQueuedOrdinaryWaiterAndClosingFencesTurns(t *testing.T) {
	scheduler := New([]string{"aka"}, Sources{}, nil)
	if reserved, err := scheduler.TryReserveGameGroup(context.Background(), "task-2", "herzog_zwei", []string{"aka"}); err != nil || !reserved {
		t.Fatalf("TryReserveGameGroup = (%t, %v)", reserved, err)
	}
	if err := scheduler.BindGameGroup("task-2", "hz-session-9"); err != nil {
		t.Fatalf("BindGameGroup = %v", err)
	}

	ordinaryCtx, cancelOrdinary := context.WithCancel(context.Background())
	defer cancelOrdinary()
	ordinary := make(chan func(), 1)
	ordinaryErr := make(chan error, 1)
	go func() {
		release, err := scheduler.Acquire(ordinaryCtx, "aka", domaintask.PriorityCritical)
		if err != nil {
			ordinaryErr <- err
			return
		}
		ordinary <- release
	}()
	waitForAgentState(t, scheduler, "aka", func(state *agentState) bool { return len(state.waiters) == 1 })

	firstTurn, err := scheduler.AcquireGameTaskTurn(context.Background(), "aka", domaintask.PriorityLow, "herzog_zwei", "task-2", "hz-session-9")
	if err != nil {
		t.Fatalf("AcquireGameTaskTurn behind ordinary waiter = %v", err)
	}
	scheduler.mu.Lock()
	active := scheduler.agents["aka"].active
	if active == nil || !active.gameTaskTurn || active.gameLaunchKey != "task-2" || active.gameSessionID != "hz-session-9" {
		scheduler.mu.Unlock()
		t.Fatalf("active lease = %+v, want exact Task turn", active)
	}
	scheduler.mu.Unlock()

	queuedTaskCtx, cancelQueuedTask := context.WithCancel(context.Background())
	defer cancelQueuedTask()
	queuedTaskErr := make(chan error, 1)
	go func() {
		_, err := scheduler.AcquireGameTaskTurn(queuedTaskCtx, "aka", domaintask.PriorityHigh, "herzog_zwei", "task-2", "hz-session-9")
		queuedTaskErr <- err
	}()
	waitForAgentState(t, scheduler, "aka", func(state *agentState) bool { return len(state.waiters) == 2 })

	if err := scheduler.MarkGameGroupClosing("task-2", "hz-session-9"); err != nil {
		t.Fatalf("MarkGameGroupClosing = %v", err)
	}
	if _, err := scheduler.AcquireGameTaskTurn(context.Background(), "aka", domaintask.PriorityHigh, "herzog_zwei", "task-2", "hz-session-9"); !errors.Is(err, ErrGameGroupMissing) {
		t.Fatalf("new turn after closing = %v, want fenced rejection", err)
	}
	firstTurn()
	select {
	case err := <-queuedTaskErr:
		t.Fatalf("queued turn crossed closing fence: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	if err := scheduler.ReleaseGameGroup("task-2"); err != nil {
		t.Fatalf("ReleaseGameGroup = %v", err)
	}
	if err := receive(t, queuedTaskErr, time.Second, "stale queued task turn was not released"); !errors.Is(err, ErrGameGroupReleased) {
		t.Fatalf("queued task turn after release = %v, want ErrGameGroupReleased", err)
	}
	ordinaryRelease := receive(t, ordinary, time.Second, "ordinary waiter did not wake after group release")
	ordinaryRelease()
}

func TestGameControllerClaimsExternalPlayerGroupAndFinishesAfterRelease(t *testing.T) {
	controllerStarted := make(chan struct{})
	controllerStopped := make(chan struct{})
	finishController := make(chan struct{})
	claimed := make(chan error, 1)
	var offered atomic.Bool
	players := []string{"aka", "ao", "kin", "gin"}
	var scheduler *Scheduler
	scheduler = New(append([]string{"mio"}, players...), Sources{
		Private: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "mio" || offered.Swap(true) {
				return Work{}, false, nil
			}
			return Work{
				ID:                   "game-controller",
				Priority:             domaintask.PriorityHigh,
				GameTaskControllerID: "task-3",
				Run: func(ctx context.Context, stop <-chan struct{}) error {
					reserved, err := scheduler.TryReserveGameGroup(ctx, "task-3", "survival_garden", players)
					if err == nil && !reserved {
						err = errors.New("controller could not claim its GAME actor group")
					}
					claimed <- err
					if err != nil {
						return err
					}
					close(controllerStarted)
					select {
					case <-ctx.Done():
						close(controllerStopped)
						return ctx.Err()
					case <-stop:
						close(controllerStopped)
						return errors.New("controller was stopped during terminal acknowledgement")
					case <-finishController:
					}
					close(controllerStopped)
					return nil
				},
			}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	if err := receive(t, claimed, time.Second, "controller Run did not reserve its group"); err != nil {
		t.Fatalf("TryReserveGameGroup from controller Run = %v", err)
	}
	waitSignal(t, controllerStarted, time.Second, "controller did not continue after group claim")
	scheduler.mu.Lock()
	owner := scheduler.agents["mio"]
	trackedOutsidePlayerGroup := owner.gameController != nil && owner.active == nil && owner.gameGroup == nil
	for _, actor := range players {
		state := scheduler.agents[actor]
		if state.gameGroup == nil || state.gameGroup.launchKey != "task-3" {
			trackedOutsidePlayerGroup = false
		}
	}
	scheduler.mu.Unlock()
	if !trackedOutsidePlayerGroup {
		t.Fatal("non-cognitive controller must be tracked outside the exact player group")
	}
	if release, ok, err := scheduler.TryAcquire(context.Background(), "mio", domaintask.PriorityNormal); err != nil || ok {
		if release != nil {
			release()
		}
		t.Fatalf("TryAcquire overlapped running controller = (%t, %v)", ok, err)
	}
	if release, ok, err := scheduler.TryAcquire(context.Background(), "aka", domaintask.PriorityNormal); err != nil || ok {
		if release != nil {
			release()
		}
		t.Fatalf("TryAcquire overlapped claimed GAME group = (%t, %v)", ok, err)
	}

	if err := scheduler.ReleaseGameGroup("task-3"); err != nil {
		t.Fatalf("ReleaseGameGroup = %v", err)
	}
	select {
	case <-controllerStopped:
		t.Fatal("ReleaseGameGroup canceled the controller before its queue acknowledgement could finish")
	case <-time.After(20 * time.Millisecond):
	}
	close(finishController)
	waitSignal(t, controllerStopped, time.Second, "controller did not finish its post-release acknowledgement")
	scheduler.shutdown()
	if err := scheduler.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after shutdown and controller return = %v", err)
	}
}

func TestGameControllerShutdownDrainsPreclaimController(t *testing.T) {
	controllerStarted := make(chan struct{})
	controllerStopped := make(chan struct{})
	finishController := make(chan struct{})
	var offered atomic.Bool
	scheduler := New([]string{"mio"}, Sources{
		Private: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "mio" || offered.Swap(true) {
				return Work{}, false, nil
			}
			return Work{
				ID:                   "game-controller",
				Priority:             domaintask.PriorityHigh,
				GameTaskControllerID: "task-3",
				Run: func(ctx context.Context, stop <-chan struct{}) error {
					close(controllerStarted)
					select {
					case <-ctx.Done():
					case <-stop:
					}
					close(controllerStopped)
					<-finishController
					return nil
				},
			}, true, nil
		},
	}, nil)
	scheduler.Tick(context.Background())
	waitSignal(t, controllerStarted, time.Second, "preclaim controller did not start")
	scheduler.mu.Lock()
	state := scheduler.agents["mio"]
	controllerTracked := state.gameController != nil && state.active == nil && state.gameGroup == nil
	scheduler.mu.Unlock()
	if !controllerTracked {
		t.Fatal("controller must be tracked without owning the cognitive active slot")
	}
	scheduler.shutdown()
	waitSignal(t, controllerStopped, time.Second, "shutdown did not cancel controller work")
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if err := scheduler.Wait(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		cancelWait()
		t.Fatalf("Wait before controller return = %v, want deadline", err)
	}
	cancelWait()
	close(finishController)
	if err := scheduler.Wait(context.Background()); err != nil {
		t.Fatalf("Wait after controller return = %v", err)
	}
}

func TestRestoreGameGroupsIsAllOrNoneAndStartupOnly(t *testing.T) {
	scheduler := New([]string{"ao", "kin", "gin"}, Sources{}, nil)
	groups := []GameGroupReservation{
		{LaunchKey: "task-a", GameID: "herzog_zwei", SessionID: "s-a", Actors: []string{"ao", "kin"}},
		{LaunchKey: "task-b", GameID: "survival_garden", SessionID: "s-b", Actors: []string{"kin", "gin"}},
	}
	if err := scheduler.RestoreGameGroups(groups); err == nil {
		t.Fatal("RestoreGameGroups accepted overlapping actor ownership")
	}
	scheduler.mu.Lock()
	for actor, state := range scheduler.agents {
		if state.gameGroup != nil {
			scheduler.mu.Unlock()
			t.Fatalf("failed restore partially reserved %s", actor)
		}
	}
	scheduler.mu.Unlock()

	valid := []GameGroupReservation{{LaunchKey: "task-a", GameID: "herzog_zwei", SessionID: "s-a", Actors: []string{"ao", "kin", "gin"}}}
	if err := scheduler.RestoreGameGroups(valid); err != nil {
		t.Fatalf("RestoreGameGroups valid input = %v", err)
	}
	if err := scheduler.RestoreGameGroups(valid); err == nil {
		t.Fatal("RestoreGameGroups accepted a second restore")
	}
}

func TestTryReserveGameGroupRacesLegacyTryAcquireAtomically(t *testing.T) {
	for iteration := 0; iteration < 32; iteration++ {
		scheduler := New([]string{"kin"}, Sources{}, nil)
		start := make(chan struct{})
		legacyResult := make(chan struct {
			release  func()
			acquired bool
			err      error
		}, 1)
		groupResult := make(chan struct {
			reserved bool
			err      error
		}, 1)
		go func() {
			<-start
			release, acquired, err := scheduler.TryAcquire(context.Background(), "kin", domaintask.PriorityNormal)
			legacyResult <- struct {
				release  func()
				acquired bool
				err      error
			}{release: release, acquired: acquired, err: err}
		}()
		go func() {
			<-start
			reserved, err := scheduler.TryReserveGameGroup(context.Background(), "race-task", "herzog_zwei", []string{"kin"})
			groupResult <- struct {
				reserved bool
				err      error
			}{reserved: reserved, err: err}
		}()
		close(start)
		legacy := receive(t, legacyResult, time.Second, "legacy TryAcquire race did not return")
		group := receive(t, groupResult, time.Second, "GAME group reservation race did not return")
		if legacy.err != nil || group.err != nil {
			t.Fatalf("iteration %d: TryAcquire err=%v, TryReserve err=%v", iteration, legacy.err, group.err)
		}
		if legacy.acquired == group.reserved {
			t.Fatalf("iteration %d: overlapping admissions: legacy acquired=%t, group reserved=%t", iteration, legacy.acquired, group.reserved)
		}
		if legacy.release != nil {
			legacy.release()
		}
	}
}

func TestTryReserveGameGroupAllowsOnlyMatchingControllerIdentity(t *testing.T) {
	scheduler := New([]string{"mio"}, Sources{}, nil)
	controllerCtx, cancelController := context.WithCancel(context.Background())
	defer cancelController()
	scheduler.mu.Lock()
	scheduler.agents["mio"].gameController = &gameControllerReservation{
		launchKey: "task-exact",
		stop:      make(chan struct{}),
		cancel:    func() { cancelController() },
	}
	scheduler.mu.Unlock()

	if reserved, err := scheduler.TryReserveGameGroup(context.Background(), "task-other", "nethack", []string{"mio"}); err != nil || reserved {
		t.Fatalf("TryReserve with conflicting controller ID = (%t, %v), want rejection", reserved, err)
	}
	if reserved, err := scheduler.TryReserveGameGroup(context.Background(), "task-exact", "nethack", []string{"mio"}); err != nil || !reserved {
		t.Fatalf("TryReserve with matching controller ID = (%t, %v), want claim", reserved, err)
	}
	if err := controllerCtx.Err(); err != nil {
		t.Fatalf("claim unexpectedly canceled matching controller context: %v", err)
	}
}

func waitForAgentState(t *testing.T, scheduler *Scheduler, actor string, ready func(*agentState) bool) {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		scheduler.mu.Lock()
		state := scheduler.agents[actor]
		if state != nil && ready(state) {
			scheduler.mu.Unlock()
			return
		}
		changed := scheduler.changed
		scheduler.mu.Unlock()
		select {
		case <-changed:
		case <-timer.C:
			t.Fatalf("scheduler state for %q did not reach the expected condition", actor)
		}
	}
}
