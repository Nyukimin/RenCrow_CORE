package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
)

const boardCommunicationServiceTestWait = 350 * time.Millisecond

func TestBoardCommunicationServiceRunsStartupCatchupAndCoalescesLocalKicksWithoutSSE(t *testing.T) {
	stream := newBoardCommunicationServiceTestStream()
	lifecycle, eventsOpened, closeCalls := boardCommunicationServiceTestLifecycle(stream)
	var calls atomic.Int32
	firstStarted := make(chan struct{})
	firstRelease := make(chan struct{})
	secondStarted := make(chan struct{})
	secondFinished := make(chan struct{})
	thirdStarted := make(chan struct{})
	thirdRelease := make(chan struct{})
	process := func(ctx context.Context) (boardCommunicationServiceStep, error) {
		switch calls.Add(1) {
		case 1:
			close(firstStarted)
			select {
			case <-firstRelease:
			case <-ctx.Done():
				return boardCommunicationServiceStep{}, ctx.Err()
			}
		case 2:
			close(secondStarted)
			close(secondFinished)
		case 3:
			close(thirdStarted)
			<-thirdRelease
		}
		return boardCommunicationServiceStep{}, nil
	}
	service := newBoardCommunicationService(boardCommunicationServiceDeps{
		enabled: true, lifecycle: lifecycle, process: process, now: time.Now,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runBoardCommunicationServiceTestAsync(service, ctx)

	boardCommunicationServiceTestWaitFor(t, firstStarted, "startup catch-up")
	boardCommunicationServiceTestWaitFor(t, eventsOpened, "authenticated SSE open")
	boardCommunicationServiceTestWaitFor(t, stream.nextStarted, "continuous SSE reader")
	if status := service.Status(); !status.Enabled || !status.Ready || status.State != boardCommunicationServiceStateReady {
		t.Fatalf("service status after connected startup = %+v, want ready", status)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("startup catch-up count = %d, want 1", got)
	}

	for i := 0; i < 5; i++ {
		service.NotifyDurableAcceptance()
	}
	service.NotifyPublicationSettled()
	close(firstRelease)
	boardCommunicationServiceTestWaitFor(t, secondStarted, "coalesced local kick step")
	boardCommunicationServiceTestWaitFor(t, secondFinished, "coalesced local kick completion")
	if got := calls.Load(); got != 2 {
		t.Fatalf("local accept/settle burst catch-up count = %d, want one coalesced step after startup", got)
	}
	if got := stream.nextCalls.Load(); got != 1 {
		t.Fatalf("SSE Next calls = %d, want one blocked read and no event required for local wake", got)
	}

	service.NotifyPublicationSettled()
	boardCommunicationServiceTestWaitFor(t, thirdStarted, "settlement step")
	cancel()
	select {
	case err := <-done:
		t.Fatalf("service Run returned before its active owner step finished: %v", err)
	default:
	}
	close(thirdRelease)
	if err := boardCommunicationServiceTestWaitForResult(t, done); err != nil {
		t.Fatalf("service Run after cancellation = %v, want nil", err)
	}
	if stream.closeCalls.Load() != 1 || closeCalls.Load() != 1 {
		t.Fatalf("shutdown close counts stream=%d generation=%d, want 1/1", stream.closeCalls.Load(), closeCalls.Load())
	}
}

func TestBoardCommunicationServiceSchedulesKnownDueWithoutPolling(t *testing.T) {
	stream := newBoardCommunicationServiceTestStream()
	lifecycle, eventsOpened, _ := boardCommunicationServiceTestLifecycle(stream)
	base := time.Date(2026, 11, 5, 8, 0, 0, 0, time.UTC)
	dueAt := base.Add(10 * time.Second)
	var nowNanos atomic.Int64
	nowNanos.Store(base.UnixNano())
	now := func() time.Time { return time.Unix(0, nowNanos.Load()).UTC() }
	timers := make(chan *boardCommunicationServiceTestTimer, 2)
	var timerCalls atomic.Int32
	newTimer := func(duration time.Duration) boardCommunicationServiceTimer {
		timerCalls.Add(1)
		timer := &boardCommunicationServiceTestTimer{duration: duration, fired: make(chan time.Time, 1)}
		timers <- timer
		return timer
	}
	var calls atomic.Int32
	firstFinished := make(chan struct{})
	secondFinished := make(chan struct{})
	process := func(context.Context) (boardCommunicationServiceStep, error) {
		if calls.Add(1) == 1 {
			close(firstFinished)
			return boardCommunicationServiceStep{NextDueAt: dueAt}, nil
		}
		close(secondFinished)
		return boardCommunicationServiceStep{}, nil
	}
	service := newBoardCommunicationService(boardCommunicationServiceDeps{
		enabled: true, lifecycle: lifecycle, process: process, now: now, newTimer: newTimer,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := runBoardCommunicationServiceTestAsync(service, ctx)
	boardCommunicationServiceTestWaitFor(t, eventsOpened, "SSE open")
	boardCommunicationServiceTestWaitFor(t, firstFinished, "startup process result")
	var timer *boardCommunicationServiceTestTimer
	select {
	case timer = <-timers:
	case <-time.After(boardCommunicationServiceTestWait):
		t.Fatal("known durable due time did not create a bounded timer")
	}
	if timer.duration != 10*time.Second {
		t.Fatalf("timer duration = %s, want exact injected-clock delay 10s", timer.duration)
	}
	if got := timerCalls.Load(); got != 1 {
		t.Fatalf("timer factory calls = %d, want one known due timer", got)
	}
	nowNanos.Store(dueAt.UnixNano())
	timer.fired <- dueAt
	boardCommunicationServiceTestWaitFor(t, secondFinished, "due retry processing")
	if got := calls.Load(); got != 2 {
		t.Fatalf("process calls after due timer = %d, want 2", got)
	}
	if got := timerCalls.Load(); got != 1 {
		t.Fatalf("timer factory calls after no known due = %d, want no polling timer", got)
	}
	cancel()
	if err := boardCommunicationServiceTestWaitForResult(t, done); err != nil {
		t.Fatalf("service Run after cancellation = %v, want nil", err)
	}
}

func TestBoardCommunicationServiceUnavailableIsOptionalAndNotReady(t *testing.T) {
	var openCalls atomic.Int32
	waits := make(chan time.Duration, 1)
	lifecycle := boardCommunicationLifecycleDeps{
		enabled: true,
		open: func(context.Context) (boardCommunicationLifecycleGeneration, error) {
			openCalls.Add(1)
			return boardCommunicationLifecycleGeneration{}, errors.New("private pin or transport detail")
		},
		wait: func(ctx context.Context, delay time.Duration) error {
			select {
			case waits <- delay:
			case <-ctx.Done():
				return ctx.Err()
			}
			<-ctx.Done()
			return ctx.Err()
		},
	}
	service := newBoardCommunicationService(boardCommunicationServiceDeps{
		enabled: true, lifecycle: lifecycle,
		process: func(context.Context) (boardCommunicationServiceStep, error) {
			return boardCommunicationServiceStep{}, nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := runBoardCommunicationServiceTestAsync(service, ctx)
	select {
	case <-waits:
	case <-time.After(boardCommunicationServiceTestWait):
		cancel()
		t.Fatal("unavailable transport did not enter bounded reconnect wait")
	}
	status := service.Status()
	if !status.Enabled || status.Ready || status.State != boardCommunicationServiceStateUnavailable {
		t.Fatalf("unavailable optional service status = %+v, want enabled and not ready", status)
	}
	if openCalls.Load() != 1 {
		t.Fatalf("connection open calls = %d, want one before bounded wait", openCalls.Load())
	}
	cancel()
	if err := boardCommunicationServiceTestWaitForResult(t, done); err != nil {
		t.Fatalf("optional service shutdown = %v, want nil", err)
	}

	var disabledOpenCalls atomic.Int32
	disabled := newBoardCommunicationService(boardCommunicationServiceDeps{
		enabled: false,
		lifecycle: boardCommunicationLifecycleDeps{enabled: true, open: func(context.Context) (boardCommunicationLifecycleGeneration, error) {
			disabledOpenCalls.Add(1)
			return boardCommunicationLifecycleGeneration{}, nil
		}},
		process: func(context.Context) (boardCommunicationServiceStep, error) {
			t.Fatal("disabled optional service ran a processor")
			return boardCommunicationServiceStep{}, nil
		},
	})
	if err := disabled.Run(context.Background()); err != nil {
		t.Fatalf("disabled service Run() = %v, want nil", err)
	}
	disabled.NotifyDurableAcceptance()
	if got := disabled.Status(); got.Enabled || got.Ready || got.State != boardCommunicationServiceStateDisabled {
		t.Fatalf("disabled service status = %+v", got)
	}
	if disabledOpenCalls.Load() != 0 || len(disabled.localWake) != 0 {
		t.Fatalf("disabled service performed work: opens=%d local wakes=%d", disabledOpenCalls.Load(), len(disabled.localWake))
	}
}

type boardCommunicationServiceTestStream struct {
	closed      chan struct{}
	nextStarted chan struct{}
	closeOnce   sync.Once
	nextOnce    sync.Once
	nextCalls   atomic.Int32
	closeCalls  atomic.Int32
}

func newBoardCommunicationServiceTestStream() *boardCommunicationServiceTestStream {
	return &boardCommunicationServiceTestStream{closed: make(chan struct{}), nextStarted: make(chan struct{})}
}

func (s *boardCommunicationServiceTestStream) Next() (boardcommunication.BoardEvent, error) {
	s.nextCalls.Add(1)
	s.nextOnce.Do(func() { close(s.nextStarted) })
	<-s.closed
	return boardcommunication.BoardEvent{}, errors.New("synthetic stream closed")
}

func (s *boardCommunicationServiceTestStream) Close() error {
	s.closeCalls.Add(1)
	s.closeOnce.Do(func() { close(s.closed) })
	return nil
}

type boardCommunicationServiceTestTimer struct {
	duration time.Duration
	fired    chan time.Time
	stopped  atomic.Bool
}

func (t *boardCommunicationServiceTestTimer) C() <-chan time.Time { return t.fired }

func (t *boardCommunicationServiceTestTimer) Stop() bool { return !t.stopped.Swap(true) }

func boardCommunicationServiceTestLifecycle(stream *boardCommunicationServiceTestStream) (boardCommunicationLifecycleDeps, <-chan struct{}, *atomic.Int32) {
	opened := make(chan struct{}, 1)
	var closeCalls atomic.Int32
	lifecycle := boardCommunicationLifecycleDeps{
		enabled: true,
		open: func(context.Context) (boardCommunicationLifecycleGeneration, error) {
			return boardCommunicationLifecycleGeneration{
				openEvents: func(context.Context, int64) (boardCommunicationEventStream, error) {
					opened <- struct{}{}
					return stream, nil
				},
				catchUp: func(context.Context) error { return nil },
				close:   func() { closeCalls.Add(1) },
			}, nil
		},
	}
	return lifecycle, opened, &closeCalls
}

func runBoardCommunicationServiceTestAsync(service *boardCommunicationService, ctx context.Context) <-chan error {
	done := make(chan error, 1)
	go func() { done <- service.Run(ctx) }()
	return done
}

func boardCommunicationServiceTestWaitFor(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(boardCommunicationServiceTestWait):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func boardCommunicationServiceTestWaitForResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(boardCommunicationServiceTestWait):
		t.Fatal("timed out waiting for service shutdown")
		return nil
	}
}
