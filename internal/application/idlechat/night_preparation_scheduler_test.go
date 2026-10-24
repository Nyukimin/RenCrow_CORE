package idlechat

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestNightPreparationSchedulerDefersOutsideWindowUntilWindowOpens(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Int64
	setNow := func(value time.Time) { current.Store(value.UnixNano()) }
	now := func() time.Time { return time.Unix(0, current.Load()).In(loc) }
	setNow(time.Date(2026, time.October, 24, 1, 59, 0, 0, loc))
	dispatched := make(chan []string, 2)
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		func() bool { return false },
		func(_ context.Context, reasons []string) { dispatched <- reasons },
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	if !scheduler.Request("startup") {
		t.Fatal("outside-window request was rejected")
	}
	select {
	case reasons := <-dispatched:
		t.Fatalf("dispatched outside window: %v", reasons)
	default:
	}

	setNow(time.Date(2026, time.October, 24, 2, 0, 0, 0, loc))
	scheduler.Tick()
	select {
	case reasons := <-dispatched:
		if len(reasons) != 1 || reasons[0] != "startup" {
			t.Fatalf("dispatch reasons = %v, want [startup]", reasons)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred request was not dispatched at window start")
	}
}

func TestNightPreparationSchedulerDefersStoryTargetUntilOwnerWindowAndKeepsItPending(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Int64
	setNow := func(value time.Time) { current.Store(value.UnixNano()) }
	now := func() time.Time { return time.Unix(0, current.Load()).In(loc) }
	setNow(time.Date(2026, time.October, 24, 1, 59, 0, 0, loc))
	type dispatchReceipt struct {
		reasons []string
		target  int
	}
	dispatched := make(chan dispatchReceipt, 2)
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		func() bool { return false },
		func(ctx context.Context, reasons []string) {
			dispatched <- dispatchReceipt{reasons: reasons, target: nightPreparationStoryTargetFromContext(ctx)}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	state, accepted := scheduler.RequestStoryPreparationTarget(3)
	if !accepted || state != "deferred" {
		t.Fatalf("request = (%q, %t), want deferred acceptance", state, accepted)
	}
	select {
	case got := <-dispatched:
		t.Fatalf("story owner dispatched before night window: %+v", got)
	default:
	}
	if target, pending := scheduler.PendingStoryPreparationTarget(); !pending || target != 3 {
		t.Fatalf("pending target = (%d, %t), want (3, true)", target, pending)
	}

	setNow(time.Date(2026, time.October, 24, 2, 0, 0, 0, loc))
	scheduler.Tick()
	select {
	case got := <-dispatched:
		if len(got.reasons) != 1 || got.reasons[0] != nightPreparationStoryPrepareReason || got.target != 3 {
			t.Fatalf("owner dispatch = %+v, want fixed reason and target 3", got)
		}
	case <-time.After(time.Second):
		t.Fatal("deferred story target was not sent to its owner at night-window start")
	}
	if target, pending := scheduler.PendingStoryPreparationTarget(); !pending || target != 3 {
		t.Fatalf("dispatch alone cleared demand: target=(%d, %t), want (3, true)", target, pending)
	}
	if !scheduler.CompleteStoryPreparationTarget(3) {
		t.Fatal("owner completion did not clear target 3")
	}
	if target, pending := scheduler.PendingStoryPreparationTarget(); pending || target != 0 {
		t.Fatalf("completed target remains pending: (%d, %t)", target, pending)
	}
}

func TestNightPreparationSchedulerCoalescesTargetAndRetainsPartialCompletionForRetry(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) }
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	dispatches := make(chan int, 3)
	var dispatchCount atomic.Int64
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		func() bool { return false },
		func(ctx context.Context, reasons []string) {
			target := nightPreparationStoryTargetFromContext(ctx)
			if len(reasons) != 1 || reasons[0] != nightPreparationStoryPrepareReason {
				t.Errorf("reasons = %v, want fixed Story prepare reason", reasons)
			}
			dispatches <- target
			if dispatchCount.Add(1) == 1 {
				close(firstStarted)
				<-releaseFirst
			}
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	if state, accepted := scheduler.RequestStoryPreparationTarget(2); !accepted || state != "queued" {
		t.Fatalf("initial request = (%q, %t), want queued acceptance", state, accepted)
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first target dispatch did not start")
	}
	if got := <-dispatches; got != 2 {
		t.Fatalf("first target = %d, want 2", got)
	}
	if _, accepted := scheduler.RequestStoryPreparationTarget(1); !accepted {
		t.Fatal("lower coalesced request was rejected")
	}
	if _, accepted := scheduler.RequestStoryPreparationTarget(5); !accepted {
		t.Fatal("higher coalesced request was rejected")
	}
	close(releaseFirst)
	select {
	case got := <-dispatches:
		if got != 5 {
			t.Fatalf("coalesced target = %d, want max target 5", got)
		}
	case <-time.After(time.Second):
		t.Fatal("higher coalesced target was not dispatched after current owner returned")
	}
	if scheduler.CompleteStoryPreparationTarget(2) {
		t.Fatal("partial completion incorrectly cleared higher target")
	}
	if target, pending := scheduler.PendingStoryPreparationTarget(); !pending || target != 5 {
		t.Fatalf("target after partial completion = (%d, %t), want (5, true)", target, pending)
	}

	// A completed dispatch that made no progress does not spin. The next policy
	// tick retries the same captured target instead of recomputing a relative
	// count from a partially updated stock snapshot.
	waitStoryDemandSchedulerIdle(t, scheduler)
	scheduler.Tick()
	select {
	case got := <-dispatches:
		if got != 5 {
			t.Fatalf("retry target = %d, want original target 5", got)
		}
	case <-time.After(time.Second):
		t.Fatal("pending partial target was not retried on the next scheduler tick")
	}
	if !scheduler.CompleteStoryPreparationTarget(5) {
		t.Fatal("owner completion did not clear coalesced target")
	}
}

func TestNightPreparationSchedulerRejectsStoryDemandWhenUnavailable(t *testing.T) {
	var nilScheduler *NightPreparationScheduler
	if state, accepted := nilScheduler.RequestStoryPreparationTarget(1); accepted || state != "" {
		t.Fatalf("nil scheduler accepted request: state=%q accepted=%t", state, accepted)
	}
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) },
		func() bool { return false },
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	scheduler.Close()
	if state, accepted := scheduler.RequestStoryPreparationTarget(1); accepted || state != "" {
		t.Fatalf("closed scheduler accepted request: state=%q accepted=%t", state, accepted)
	}
}

func TestNightPreparationSchedulerRequeuesCanceledDispatchReasons(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) }
	var foreground atomic.Bool
	firstStarted := make(chan struct{})
	firstFinished := make(chan struct{})
	secondDispatch := make(chan []string, 1)
	var dispatchCount atomic.Int64
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		foreground.Load,
		func(ctx context.Context, reasons []string) {
			if dispatchCount.Add(1) == 1 {
				close(firstStarted)
				<-ctx.Done()
				close(firstFinished)
				return
			}
			secondDispatch <- reasons
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	if !scheduler.Request("startup") {
		t.Fatal("startup request was rejected")
	}
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first dispatch did not start")
	}
	foreground.Store(true)
	scheduler.NotifyPolicyChanged()
	select {
	case <-firstFinished:
	case <-time.After(time.Second):
		t.Fatal("foreground transition did not cancel first dispatch")
	}

	foreground.Store(false)
	if !scheduler.Request("monitor_tick") {
		t.Fatal("reevaluation request was rejected")
	}
	select {
	case reasons := <-secondDispatch:
		if len(reasons) != 2 || reasons[0] != "startup" || reasons[1] != "monitor_tick" {
			t.Fatalf("requeued dispatch reasons = %v, want [startup monitor_tick]", reasons)
		}
	case <-time.After(time.Second):
		t.Fatal("canceled work was not requeued for deterministic reevaluation")
	}
}

func TestNightPreparationSchedulerUsesHalfOpenWindowAndCancelsManagedLLM(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	var current atomic.Int64
	setNow := func(value time.Time) { current.Store(value.UnixNano()) }
	now := func() time.Time { return time.Unix(0, current.Load()).In(loc) }
	setNow(time.Date(2026, time.October, 24, 5, 59, 59, 0, loc))
	var foreground atomic.Bool
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		foreground.Load,
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- scheduler.RunLLM(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("managed LLM call did not start inside the window")
	}

	setNow(time.Date(2026, time.October, 24, 6, 0, 0, 0, loc))
	scheduler.Tick()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("managed LLM result = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("window end did not cancel managed LLM context")
	}

	called := false
	err = scheduler.RunLLM(context.Background(), func(context.Context) error {
		called = true
		return nil
	})
	if err == nil || called {
		t.Fatalf("outside-window RunLLM = (%v, called=%t), want blocked without callback", err, called)
	}

	setNow(time.Date(2026, time.October, 24, 3, 0, 0, 0, loc))
	if err := scheduler.RunLLM(context.Background(), func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatalf("RunLLM inside window: %v", err)
	}
}

func TestNightPreparationSchedulerForegroundTransitionCancelsManagedLLM(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) }
	var foreground atomic.Bool
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		now,
		foreground.Load,
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	started := make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		finished <- scheduler.RunLLM(context.Background(), func(ctx context.Context) error {
			close(started)
			<-ctx.Done()
			return ctx.Err()
		})
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("managed LLM call did not start")
	}

	foreground.Store(true)
	scheduler.NotifyPolicyChanged()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("managed LLM result = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("foreground transition did not cancel managed LLM context")
	}
	called := false
	if err := scheduler.RunLLM(context.Background(), func(context.Context) error { called = true; return nil }); err == nil || called {
		t.Fatalf("foreground-blocked RunLLM = (called=%t, err=%v), want blocked", called, err)
	}
}

func TestNightPreparationSchedulerSupportsOvernightHalfOpenWindow(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) }
	called := false
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "22:00", End: "06:00"},
		now,
		func() bool { return false },
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()
	if err := scheduler.RunLLM(context.Background(), func(context.Context) error { called = true; return nil }); err != nil || !called {
		t.Fatalf("overnight in-window RunLLM = (called=%t, err=%v), want call", called, err)
	}
}

func TestNightPreparationSchedulerDoesNotHoldLockWhileReadingForegroundPolicy(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	var checks atomic.Int64
	secondCheck := make(chan struct{})
	releaseCheck := make(chan struct{})
	scheduler, err := NewNightPreparationScheduler(
		NightPreparationSchedule{Timezone: "Asia/Tokyo", Start: "02:00", End: "06:00"},
		func() time.Time { return time.Date(2026, time.October, 24, 3, 0, 0, 0, loc) },
		func() bool {
			if checks.Add(1) == 2 {
				close(secondCheck)
				<-releaseCheck
			}
			return false
		},
		func(context.Context, []string) {},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer scheduler.Close()

	called := make(chan struct{}, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- scheduler.RunLLM(context.Background(), func(context.Context) error {
			called <- struct{}{}
			return nil
		})
	}()
	select {
	case <-secondCheck:
	case <-time.After(time.Second):
		t.Fatal("RunLLM did not reach its second policy check")
	}

	closed := make(chan struct{})
	go func() {
		scheduler.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(time.Second):
		close(releaseCheck)
		t.Fatal("Close blocked behind the foreground policy callback")
	}
	close(releaseCheck)
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("RunLLM succeeded after concurrent scheduler close")
		}
	case <-time.After(time.Second):
		t.Fatal("RunLLM did not finish after scheduler close")
	}
	select {
	case <-called:
		t.Fatal("external call ran after concurrent scheduler close")
	default:
	}
}
