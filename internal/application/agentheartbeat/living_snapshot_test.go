package agentheartbeat

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestLivingSnapshotTracksLiveLeaseStopAndRelease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scanStarted := make(chan struct{})
	allowWork := make(chan struct{})
	workStarted := make(chan struct{})
	stopObserved := make(chan struct{})
	finishWork := make(chan struct{})
	var boardCalls int
	scheduler := New([]string{"mio"}, Sources{
		Board: func(ctx context.Context, _ string) (Work, bool, error) {
			boardCalls++
			if boardCalls != 1 {
				return Work{}, false, nil
			}
			close(scanStarted)
			select {
			case <-allowWork:
				return Work{ID: "private-queue-id", Priority: domaintask.PriorityLow, Run: func(_ context.Context, stop <-chan struct{}) error {
					close(workStarted)
					select {
					case <-stop:
						close(stopObserved)
						<-finishWork
					case <-finishWork:
					}
					return nil
				}}, true, nil
			case <-ctx.Done():
				return Work{}, false, ctx.Err()
			}
		},
	}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finishWork:
		default:
			close(finishWork)
		}
	})

	waitSignal(t, scanStarted, time.Second, "scheduler did not begin its first scan")
	activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || len(activities) != 1 || activities[0].ActorID != "mio" || activities[0].State != "checking" || activities[0].ActivityKind != "" {
		t.Fatalf("scan snapshot = (%+v, %t), want mio checking", activities, ready)
	}

	close(allowWork)
	waitSignal(t, workStarted, time.Second, "discovered work did not start")
	assertLivingActivity(t, scheduler, "running", "board")

	acquired := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		release, err := scheduler.Acquire(ctx, "mio", domaintask.PriorityCritical)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- release
	}()
	waitSignal(t, stopObserved, time.Second, "higher-priority acquisition did not stop the live lease")
	assertLivingActivity(t, scheduler, "stopping", "board")

	close(finishWork)
	select {
	case err := <-acquireErr:
		t.Fatalf("Acquire() after lease release: %v", err)
	case release := <-acquired:
		assertLivingActivity(t, scheduler, "running", "foreground")
		release()
	case <-time.After(time.Second):
		t.Fatal("foreground waiter did not acquire after the live lease returned")
	}

	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := scheduler.Wait(waitCtx); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
	if activities, ready := scheduler.LivingSnapshot(context.Background(), nil); ready || activities != nil {
		t.Fatalf("closed scheduler snapshot = (%+v, %t), want unavailable", activities, ready)
	}
}

func TestLivingSnapshotIsUnavailableUntilSchedulerStarts(t *testing.T) {
	scheduler := New([]string{"mio"}, Sources{}, nil)
	if activities, ready := scheduler.LivingSnapshot(context.Background(), nil); ready || activities != nil {
		t.Fatalf("unstarted scheduler snapshot = (%+v, %t), want unavailable", activities, ready)
	}
}

func TestAcquireGameTurnProjectsKnownGameIDOnlyForActiveLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	scheduler := New([]string{"mio"}, Sources{}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-started:
			if err != nil {
				t.Errorf("Start() = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("scheduler did not stop after cancellation")
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := scheduler.Wait(waitCtx); err != nil {
			t.Errorf("Wait() = %v", err)
		}
	})
	deadline := time.Now().Add(time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if _, ready = scheduler.LivingSnapshot(context.Background(), nil); ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !ready {
		t.Fatal("scheduler did not become ready")
	}

	release, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityLow, "nethack")
	if err != nil {
		t.Fatalf("AcquireGameTurn() error = %v", err)
	}
	defer release()
	activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || len(activities) != 1 || activities[0].State != "running" || activities[0].ActivityKind != string(ActivityKindGame) || activities[0].GameID != "nethack" {
		t.Fatalf("active game snapshot = (%+v, %t), want running/game/nethack", activities, ready)
	}
	release()
	activities, ready = scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || len(activities) != 1 || activities[0].State == "running" || activities[0].ActivityKind != "" || activities[0].GameID != "" {
		t.Fatalf("released game snapshot = (%+v, %t), want no active game descriptor", activities, ready)
	}

	unknownRelease, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityLow, "private-game-id")
	if err != nil {
		t.Fatalf("AcquireGameTurn(unknown) error = %v", err)
	}
	defer unknownRelease()
	activities, ready = scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || activities[0].ActivityKind != string(ActivityKindGame) || activities[0].GameID != "" {
		t.Fatalf("unknown game snapshot = (%+v, %t), want game category with no public ID", activities, ready)
	}
	unknownRelease()
}

func TestAcquireGameTurnReentryRestoresIdleGameDescriptor(t *testing.T) {
	scheduler := New([]string{"mio"}, Sources{}, nil)
	scheduler.started = true
	lease := &reservation{scheduler: scheduler, actor: "mio", idle: true, activityKind: ActivityKindGame}
	scheduler.agents["mio"].active = lease
	ctx := context.WithValue(context.Background(), reservationContextKey{}, lease)

	release, err := scheduler.AcquireGameTurn(ctx, "mio", domaintask.PriorityLow, "nethack")
	if err != nil {
		t.Fatalf("reentrant AcquireGameTurn() error = %v", err)
	}
	activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || activities[0].ActivityKind != string(ActivityKindGame) || activities[0].GameID != "nethack" {
		t.Fatalf("reentrant snapshot = (%+v, %t), want game/nethack", activities, ready)
	}
	release()
	activities, ready = scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || activities[0].ActivityKind != string(ActivityKindGame) || activities[0].GameID != "" {
		t.Fatalf("restored idle snapshot = (%+v, %t), want game category without game ID", activities, ready)
	}

	staleRelease, err := scheduler.AcquireGameTurn(ctx, "mio", domaintask.PriorityLow, "nethack")
	if err != nil {
		t.Fatalf("second reentrant AcquireGameTurn() error = %v", err)
	}
	newOwner := &reservation{scheduler: scheduler, actor: "mio", activityKind: ActivityKindBoard}
	scheduler.mu.Lock()
	scheduler.agents["mio"].active = newOwner
	scheduler.mu.Unlock()
	staleRelease()
	activities, ready = scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || activities[0].ActivityKind != string(ActivityKindBoard) || activities[0].GameID != "" {
		t.Fatalf("snapshot after stale reentrant release = (%+v, %t), want replacement board lease", activities, ready)
	}
}

func TestQueuedGameTurnIsNotProjectedBeforeItsLeaseStarts(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	workStarted := make(chan struct{})
	finishWork := make(chan struct{})
	var finishOnce sync.Once
	finish := func() { finishOnce.Do(func() { close(finishWork) }) }
	scheduler := New([]string{"mio"}, Sources{
		Board: func(context.Context, string) (Work, bool, error) {
			return Work{ID: "board-work", Priority: domaintask.PriorityLow, Run: func(context.Context, <-chan struct{}) error {
				close(workStarted)
				<-finishWork
				return nil
			}}, true, nil
		},
	}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		finish()
		select {
		case err := <-started:
			if err != nil {
				t.Errorf("Start() = %v", err)
			}
		case <-time.After(time.Second):
			t.Error("scheduler did not stop after cancellation")
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
		defer waitCancel()
		if err := scheduler.Wait(waitCtx); err != nil {
			t.Errorf("Wait() = %v", err)
		}
	})
	waitSignal(t, workStarted, time.Second, "board work did not start")

	canceledCtx, cancelWaiter := context.WithCancel(context.Background())
	canceledErr := make(chan error, 1)
	go func() {
		_, err := scheduler.AcquireGameTurn(canceledCtx, "mio", domaintask.PriorityLow, "nethack")
		canceledErr <- err
	}()
	waitForGameWaiter(t, scheduler)
	cancelWaiter()
	select {
	case err := <-canceledErr:
		if err != context.Canceled {
			t.Fatalf("canceled game waiter error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled game waiter did not return")
	}
	activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || activities[0].ActivityKind != string(ActivityKindBoard) || activities[0].GameID != "" {
		t.Fatalf("snapshot after canceled game waiter = (%+v, %t), want board without game descriptor", activities, ready)
	}

	acquired := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		release, err := scheduler.AcquireGameTurn(context.Background(), "mio", domaintask.PriorityLow, "nethack")
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- release
	}()
	activities, ready = scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || activities[0].State != "running" || activities[0].ActivityKind != string(ActivityKindBoard) || activities[0].GameID != "" {
		t.Fatalf("queued game snapshot = (%+v, %t), want active board without game ID", activities, ready)
	}
	waitForGameWaiter(t, scheduler)
	finish()
	select {
	case err := <-acquireErr:
		t.Fatalf("AcquireGameTurn() after board completion: %v", err)
	case release := <-acquired:
		activities, ready = scheduler.LivingSnapshot(context.Background(), nil)
		if !ready || activities[0].ActivityKind != string(ActivityKindGame) || activities[0].GameID != "nethack" {
			release()
			t.Fatalf("granted game snapshot = (%+v, %t), want game/nethack", activities, ready)
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("game turn waiter did not acquire after board work returned")
	}
}

func waitForGameWaiter(t *testing.T, scheduler *Scheduler) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		queues, ready := scheduler.OwnerForegroundWaiterSnapshot()
		if ready && len(queues) == 1 && queues[0].Count == 1 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("game turn waiter did not appear in the scheduler queue")
}

func TestOwnerLivingSnapshotCanResolveNonPrivateActivityTitleWithoutChangingPublicView(t *testing.T) {
	taskID := modulecore.NewTaskID()
	scheduler := New([]string{"mio"}, Sources{}, nil)
	scheduler.started = true
	scheduler.agents["mio"].active = &reservation{
		actor: "mio", source: workSourceBoard, activityKind: ActivityKindBoard, visibilityTaskID: taskID,
	}
	lookup := func(_ context.Context, got modulecore.TaskID) (string, error) {
		if got != taskID {
			t.Fatalf("title lookup task ID = %s, want %s", got, taskID)
		}
		return "owner-only board title", nil
	}

	ownerView, ready := scheduler.OwnerLivingSnapshot(context.Background(), lookup)
	if !ready || len(ownerView) != 1 || ownerView[0].State != "running" || ownerView[0].ActivityKind != string(ActivityKindBoard) || ownerView[0].Title != "owner-only board title" {
		t.Fatalf("owner snapshot = (%+v, %t), want board activity title", ownerView, ready)
	}

	publicView, ready := scheduler.LivingSnapshot(context.Background(), lookup)
	if !ready || len(publicView) != 1 || publicView[0].Title != "" {
		t.Fatalf("public snapshot = (%+v, %t), want no non-private activity title", publicView, ready)
	}
}

func TestLivingActivityKindUsesExactSourceAndRoutineAnnotation(t *testing.T) {
	tests := []struct {
		name      string
		source    workSource
		requested ActivityKind
		want      ActivityKind
	}{
		{name: "board source is board", source: workSourceBoard, requested: ActivityKindRoutine, want: ActivityKindBoard},
		{name: "private source is private", source: workSourcePrivate, requested: ActivityKindGame, want: ActivityKindPrivate},
		{name: "explicit game", source: workSourceIdle, requested: ActivityKindGame, want: ActivityKindGame},
		{name: "explicit routine", source: workSourceIdle, requested: ActivityKindRoutine, want: ActivityKindRoutine},
		{name: "unknown idle is omitted", source: workSourceIdle, requested: "unexpected", want: ""},
		{name: "foreground admission", source: workSourceNone, want: ActivityKindForeground},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := activityKindFor(tt.source, tt.requested); got != tt.want {
				t.Fatalf("activityKindFor(%d,%q)=%q want=%q", tt.source, tt.requested, got, tt.want)
			}
		})
	}
}

func TestLivingSnapshotProjectsEveryActorInSharedRoutineLease(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	routineStarted := make(chan struct{})
	stopObserved := make(chan struct{})
	finishRoutine := make(chan struct{})
	var routineCalls atomic.Int32
	scheduler := New([]string{"mio", "shiro"}, Sources{
		Idle: func(_ context.Context, actor string) (Work, bool, error) {
			if actor != "mio" || routineCalls.Add(1) > 1 {
				return Work{}, false, nil
			}
			return Work{
				ID: "agent-routine:mio:2026-11-14:morning_chat", ActivityKind: ActivityKindRoutine,
				Participants: []string{"shiro"},
				Run: func(_ context.Context, stop <-chan struct{}) error {
					close(routineStarted)
					select {
					case <-stop:
						close(stopObserved)
						<-finishRoutine
					case <-finishRoutine:
					}
					return nil
				},
			}, true, nil
		},
	}, nil)
	started := make(chan error, 1)
	go func() { started <- scheduler.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-finishRoutine:
		default:
			close(finishRoutine)
		}
	})
	waitSignal(t, routineStarted, time.Second, "shared routine work did not start")
	assertRoutineGroupSnapshot(t, scheduler, "running")

	acquired := make(chan func(), 1)
	acquireErr := make(chan error, 1)
	go func() {
		release, err := scheduler.Acquire(ctx, "shiro", domaintask.PriorityCritical)
		if err != nil {
			acquireErr <- err
			return
		}
		acquired <- release
	}()
	waitSignal(t, stopObserved, time.Second, "group lease did not receive stop request")
	assertRoutineGroupSnapshot(t, scheduler, "stopping")

	close(finishRoutine)
	select {
	case err := <-acquireErr:
		t.Fatalf("Acquire() after group release: %v", err)
	case release := <-acquired:
		activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
		if !ready || len(activities) != 2 || (activities[0].State != "waiting" && activities[0].State != "checking") || activities[0].ActivityKind != "" || activities[1].State != "running" || activities[1].ActivityKind != string(ActivityKindForeground) {
			t.Fatalf("snapshot after group release = (%+v, %t), want no mio lease and shiro foreground", activities, ready)
		}
		release()
	case <-time.After(time.Second):
		t.Fatal("foreground waiter did not acquire after group routine returned")
	}

	cancel()
	select {
	case err := <-started:
		if err != nil {
			t.Fatalf("Start() = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("scheduler did not stop")
	}
	waitCtx, waitCancel := context.WithTimeout(context.Background(), time.Second)
	defer waitCancel()
	if err := scheduler.Wait(waitCtx); err != nil {
		t.Fatalf("Wait() = %v", err)
	}
}

func TestOwnerForegroundWaiterSnapshotPreservesSchedulerPriorityOrder(t *testing.T) {
	scheduler := New([]string{"shiro", "mio"}, Sources{}, nil)
	if got, ready := scheduler.OwnerForegroundWaiterSnapshot(); ready || got != nil {
		t.Fatalf("snapshot before scheduler start = (%+v, %t), want unavailable", got, ready)
	}

	scheduler.mu.Lock()
	scheduler.started = true
	scheduler.agents["mio"].waiters = []*waiter{
		{priority: domaintask.PriorityLow, order: 1},
		{priority: domaintask.PriorityHigh, order: 2},
		{priority: domaintask.PriorityHigh, order: 3},
	}
	scheduler.mu.Unlock()

	got, ready := scheduler.OwnerForegroundWaiterSnapshot()
	if !ready || len(got) != 2 || got[0].ActorID != "mio" || got[1].ActorID != "shiro" {
		t.Fatalf("owner waiter snapshot = (%+v, %t), want every actor in stable order", got, ready)
	}
	waiters := got[0]
	if waiters.Count != 3 || len(waiters.Items) != 3 {
		t.Fatalf("mio waiter count = %+v, want 3", waiters)
	}
	wantPriorities := []string{"high", "high", "low"}
	for index, want := range wantPriorities {
		if waiters.Items[index].Position != index+1 || waiters.Items[index].Priority != want {
			t.Fatalf("mio waiter[%d] = %+v, want position %d priority %q", index, waiters.Items[index], index+1, want)
		}
	}
}

func assertRoutineGroupSnapshot(t *testing.T, scheduler *Scheduler, wantState string) {
	t.Helper()
	activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || len(activities) != 2 {
		t.Fatalf("group snapshot = (%+v, %t), want two actors", activities, ready)
	}
	for index, actor := range []string{"mio", "shiro"} {
		if activities[index].ActorID != actor || activities[index].State != wantState || activities[index].ActivityKind != string(ActivityKindRoutine) {
			t.Fatalf("group snapshot = %+v, want %s/%s/routine", activities[index], actor, wantState)
		}
	}
}

func assertLivingActivity(t *testing.T, scheduler *Scheduler, wantState, wantKind string) {
	t.Helper()
	activities, ready := scheduler.LivingSnapshot(context.Background(), nil)
	if !ready || len(activities) != 1 || activities[0].ActorID != "mio" || activities[0].State != wantState || activities[0].ActivityKind != wantKind {
		t.Fatalf("snapshot = (%+v, %t), want mio %s/%s", activities, ready, wantState, wantKind)
	}
}
