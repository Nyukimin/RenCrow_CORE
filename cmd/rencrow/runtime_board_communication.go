package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/boardcommunication"
)

var ErrBoardCommunicationServiceUnavailable = errors.New("BOARD communication service unavailable")

const (
	boardCommunicationServiceStateDisabled    = "disabled"
	boardCommunicationServiceStateStarting    = "starting"
	boardCommunicationServiceStateReady       = "ready"
	boardCommunicationServiceStateUnavailable = "unavailable"
)

// boardCommunicationServiceStep carries only a next retry instant discovered
// from durable inbox or publication state. A zero time means there is no known
// local due work to schedule.
type boardCommunicationServiceStep struct {
	NextDueAt time.Time
}

type boardCommunicationServiceTimer interface {
	C() <-chan time.Time
	Stop() bool
}

type boardCommunicationServiceDeps struct {
	enabled   bool
	lifecycle boardCommunicationLifecycleDeps
	process   func(context.Context) (boardCommunicationServiceStep, error)
	now       func() time.Time
	newTimer  func(time.Duration) boardCommunicationServiceTimer
}

type boardCommunicationServiceStatus struct {
	Enabled bool
	Ready   bool
	State   string
}

type boardCommunicationService struct {
	deps                 boardCommunicationServiceDeps
	localWake            chan struct{}
	mu                   sync.RWMutex
	status               boardCommunicationServiceStatus
	runMu                sync.Mutex
	running              bool
	connectionGeneration atomic.Uint64
	caughtUpGeneration   atomic.Uint64
}

// newBoardCommunicationService is the optional CORE-owned composition point
// for the pinned connection lifecycle and durable inbox/publication owners.
// The service remains unavailable until those callbacks are composed.
func newBoardCommunicationService(deps boardCommunicationServiceDeps) *boardCommunicationService {
	state := boardCommunicationServiceStateDisabled
	if deps.enabled {
		state = boardCommunicationServiceStateStarting
	}
	return &boardCommunicationService{
		deps: deps, localWake: make(chan struct{}, 1),
		status: boardCommunicationServiceStatus{Enabled: deps.enabled, State: state},
	}
}

// NotifyDurableAcceptance coalesces an owner-request wake after its acceptance
// receipt has been stored.
func (s *boardCommunicationService) NotifyDurableAcceptance() {
	s.notifyLocalWake()
}

// NotifyPublicationSettled coalesces a follow-up after a durable publication
// or terminal receipt has settled.
func (s *boardCommunicationService) NotifyPublicationSettled() {
	s.notifyLocalWake()
}

func (s *boardCommunicationService) notifyLocalWake() {
	if s == nil || !s.deps.enabled {
		return
	}
	select {
	case s.localWake <- struct{}{}:
	default:
	}
}

func (s *boardCommunicationService) Status() boardCommunicationServiceStatus {
	if s == nil {
		return boardCommunicationServiceStatus{State: boardCommunicationServiceStateUnavailable}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

func (s *boardCommunicationService) setStatus(state string, ready bool) {
	s.mu.Lock()
	s.status.State = state
	s.status.Ready = ready
	s.mu.Unlock()
}

type boardCommunicationServiceCatchUpRequest struct {
	ctx  context.Context
	done chan error
}

type boardCommunicationServiceRealTimer struct{ timer *time.Timer }

func (t *boardCommunicationServiceRealTimer) C() <-chan time.Time { return t.timer.C }

func (t *boardCommunicationServiceRealTimer) Stop() bool { return t.timer.Stop() }

type boardCommunicationServiceActiveStream struct {
	mu     sync.Mutex
	stream boardCommunicationEventStream
}

type boardCommunicationServiceEventStream struct {
	inner     boardCommunicationEventStream
	closeOnce sync.Once
	closeErr  error
}

func (s *boardCommunicationServiceEventStream) Next() (boardcommunication.BoardEvent, error) {
	return s.inner.Next()
}

func (s *boardCommunicationServiceEventStream) Close() error {
	s.closeOnce.Do(func() { s.closeErr = s.inner.Close() })
	return s.closeErr
}

func (s *boardCommunicationServiceActiveStream) set(stream boardCommunicationEventStream) {
	s.mu.Lock()
	s.stream = stream
	s.mu.Unlock()
}

func (s *boardCommunicationServiceActiveStream) clear() {
	s.mu.Lock()
	s.stream = nil
	s.mu.Unlock()
}

func (s *boardCommunicationServiceActiveStream) close() {
	s.mu.Lock()
	stream := s.stream
	s.mu.Unlock()
	if stream != nil {
		_ = stream.Close()
	}
}

// Run owns one processor while the existing lifecycle remains responsible for
// the pinned connection and its single continuous SSE reader. Inbox and
// publication callbacks provide bounded durable catch-up work.
func (s *boardCommunicationService) Run(ctx context.Context) error {
	if s == nil {
		return ErrBoardCommunicationServiceUnavailable
	}
	if !s.deps.enabled {
		s.setStatus(boardCommunicationServiceStateDisabled, false)
		return nil
	}
	if ctx == nil || s.deps.lifecycle.open == nil || s.deps.process == nil {
		s.setStatus(boardCommunicationServiceStateUnavailable, false)
		return ErrBoardCommunicationServiceUnavailable
	}
	s.runMu.Lock()
	if s.running {
		s.runMu.Unlock()
		return ErrBoardCommunicationServiceUnavailable
	}
	s.running = true
	s.runMu.Unlock()
	defer func() {
		s.runMu.Lock()
		s.running = false
		s.runMu.Unlock()
	}()
	if ctx.Err() != nil {
		s.setStatus(boardCommunicationServiceStateUnavailable, false)
		return nil
	}

	s.setStatus(boardCommunicationServiceStateStarting, false)
	runCtx, cancel := context.WithCancel(ctx)
	catchUps := make(chan boardCommunicationServiceCatchUpRequest)
	activeStream := &boardCommunicationServiceActiveStream{}
	processorDone := make(chan struct{})
	go func() {
		defer close(processorDone)
		s.runProcessor(runCtx, catchUps, activeStream)
	}()

	lifecycleErr := s.runLifecycle(runCtx, catchUps, activeStream)
	cancel()
	<-processorDone
	s.setStatus(boardCommunicationServiceStateUnavailable, false)
	if ctx.Err() != nil {
		return nil
	}
	if lifecycleErr != nil {
		return ErrBoardCommunicationServiceUnavailable
	}
	return nil
}

func (s *boardCommunicationService) runLifecycle(
	ctx context.Context,
	catchUps chan<- boardCommunicationServiceCatchUpRequest,
	activeStream *boardCommunicationServiceActiveStream,
) error {
	lifecycle := s.deps.lifecycle
	lifecycle.enabled = true
	originalOpen := lifecycle.open
	lifecycle.open = func(ctx context.Context) (boardCommunicationLifecycleGeneration, error) {
		generation, err := originalOpen(ctx)
		if err != nil {
			s.setStatus(boardCommunicationServiceStateUnavailable, false)
			return boardCommunicationLifecycleGeneration{}, err
		}
		openEvents := generation.openEvents
		closeGeneration := generation.close
		generation.openEvents = func(ctx context.Context, after int64) (boardCommunicationEventStream, error) {
			if openEvents == nil {
				s.setStatus(boardCommunicationServiceStateUnavailable, false)
				return nil, ErrBoardCommunicationServiceUnavailable
			}
			stream, err := openEvents(ctx, after)
			if err != nil || stream == nil {
				s.setStatus(boardCommunicationServiceStateUnavailable, false)
				if err == nil {
					err = ErrBoardCommunicationServiceUnavailable
				}
				return nil, err
			}
			stream = &boardCommunicationServiceEventStream{inner: stream}
			activeStream.set(stream)
			s.connectionGeneration.Add(1)
			s.setStatus(boardCommunicationServiceStateReady, true)
			return stream, nil
		}
		generation.catchUp = func(catchUpCtx context.Context) error {
			return s.requestCatchUp(catchUpCtx, catchUps)
		}
		generation.close = func() {
			activeStream.clear()
			s.setStatus(boardCommunicationServiceStateUnavailable, false)
			if closeGeneration != nil {
				closeGeneration()
			}
		}
		return generation, nil
	}
	return runBoardCommunicationLifecycle(ctx, lifecycle)
}

func (s *boardCommunicationService) requestCatchUp(ctx context.Context, catchUps chan<- boardCommunicationServiceCatchUpRequest) error {
	request := boardCommunicationServiceCatchUpRequest{ctx: ctx, done: make(chan error, 1)}
	select {
	case catchUps <- request:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-request.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *boardCommunicationService) runProcessor(
	ctx context.Context,
	catchUps <-chan boardCommunicationServiceCatchUpRequest,
	activeStream *boardCommunicationServiceActiveStream,
) {
	var dueTimer boardCommunicationServiceTimer
	stopDueTimer := func() {
		if dueTimer == nil {
			return
		}
		timerChannel := dueTimer.C()
		if !dueTimer.Stop() {
			select {
			case <-timerChannel:
			default:
			}
		}
		dueTimer = nil
	}
	defer stopDueTimer()

	process := func(processCtx context.Context, closeStreamOnError bool) error {
		if !s.Status().Ready {
			return ErrBoardCommunicationServiceUnavailable
		}
		stopDueTimer()
		// A durable catch-up observes any local notifications queued before this
		// step. Notifications arriving during the step remain queued for another.
		for {
			select {
			case <-s.localWake:
			default:
				goto drained
			}
		}
	drained:
		// The injected owner port must bound one step and honor cancellation;
		// it catches up durable inbox work and settles eligible publications.
		step, err := s.deps.process(processCtx)
		if err != nil {
			s.setStatus(boardCommunicationServiceStateUnavailable, false)
			if closeStreamOnError {
				activeStream.close()
			}
			return err
		}
		if step.NextDueAt.IsZero() {
			return nil
		}
		if !s.Status().Ready {
			return nil
		}
		now := time.Now
		if s.deps.now != nil {
			now = s.deps.now
		}
		delay := step.NextDueAt.Sub(now())
		if delay < 0 {
			delay = 0
		}
		newTimer := s.deps.newTimer
		if newTimer == nil {
			newTimer = func(delay time.Duration) boardCommunicationServiceTimer {
				return &boardCommunicationServiceRealTimer{timer: time.NewTimer(delay)}
			}
		}
		dueTimer = newTimer(delay)
		if dueTimer == nil {
			s.setStatus(boardCommunicationServiceStateUnavailable, false)
			activeStream.close()
			return ErrBoardCommunicationServiceUnavailable
		}
		return nil
	}

	for {
		if ctx.Err() != nil {
			return
		}
		// Give a lifecycle catch-up priority over a coalesced local notification;
		// that durable step covers notifications accumulated while disconnected.
		select {
		case request := <-catchUps:
			err := process(request.ctx, false)
			if err == nil {
				s.caughtUpGeneration.Store(s.connectionGeneration.Load())
			}
			request.done <- err
			continue
		default:
		}
		var localWake <-chan struct{}
		generation := s.connectionGeneration.Load()
		localReady := s.Status().Ready && generation != 0 && s.caughtUpGeneration.Load() == generation
		if localReady {
			localWake = s.localWake
		}
		var due <-chan time.Time
		if localReady && dueTimer != nil {
			due = dueTimer.C()
		}
		select {
		case <-ctx.Done():
			return
		case request := <-catchUps:
			err := process(request.ctx, false)
			if err == nil {
				s.caughtUpGeneration.Store(s.connectionGeneration.Load())
			}
			request.done <- err
		case <-localWake:
			_ = process(ctx, true)
		case <-due:
			dueTimer = nil
			_ = process(ctx, true)
		}
	}
}
