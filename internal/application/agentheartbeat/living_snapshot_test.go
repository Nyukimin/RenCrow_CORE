package agentheartbeat

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
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
