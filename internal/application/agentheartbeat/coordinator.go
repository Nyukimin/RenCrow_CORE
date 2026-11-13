package agentheartbeat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const (
	Interval                = 10 * time.Second
	defaultDiscoveryTimeout = 2 * time.Second
)

var (
	ErrSchedulerClosed = errors.New("agent heartbeat scheduler is closed")
	ErrUnknownActor    = errors.New("agent is not registered with the heartbeat scheduler")
)

// Work describes a task discovered without claiming or executing it. Run owns
// those effects after the scheduler has reserved the actor's single work slot.
type Work struct {
	ID        string
	Priority  domaintask.Priority
	CreatedAt time.Time
	Run       func(context.Context, <-chan struct{}) error
}

// Source discovers and plans one candidate for an actor. It must honor ctx;
// the scheduler bounds each call and never overlaps scans for the same actor.
type Source func(context.Context, string) (Work, bool, error)

type Sources struct {
	Board   Source
	Private Source
	Idle    Source
}

type Scheduler struct {
	mu               sync.Mutex
	agents           map[string]*agentState
	sources          Sources
	report           func(actor, stage string, err error)
	reportMu         sync.Mutex
	changed          chan struct{}
	discoveryTimeout time.Duration
	nextOrder        uint64
	closed           bool
	done             chan struct{}
}

type agentState struct {
	actor  string
	active *reservation

	waiters []*waiter

	scanning   bool
	scanCancel context.CancelFunc
	scanDirty  bool
	nextScan   context.Context

	rescanAfterRun bool
	rescanContext  context.Context
}

type reservation struct {
	scheduler  *Scheduler
	actor      string
	priority   domaintask.Priority
	background bool
	idle       bool
	stop       chan struct{}
	stopClosed bool
	cancel     context.CancelFunc
}

type waiter struct {
	priority domaintask.Priority
	order    uint64
	ready    chan struct{}
	lease    *reservation
}

type reservationContextKey struct{}

// New creates a per-actor scheduler. Actor order is retained and duplicate or
// empty names are ignored.
func New(actors []string, sources Sources, report func(actor, stage string, err error)) *Scheduler {
	s := &Scheduler{
		agents:           make(map[string]*agentState, len(actors)),
		sources:          sources,
		report:           report,
		done:             make(chan struct{}),
		changed:          make(chan struct{}),
		discoveryTimeout: defaultDiscoveryTimeout,
	}
	for _, actor := range actors {
		if actor == "" {
			continue
		}
		if _, exists := s.agents[actor]; !exists {
			s.agents[actor] = &agentState{actor: actor}
		}
	}
	return s
}

// Tick starts a nonblocking discovery pass for every registered actor.
func (s *Scheduler) Tick(ctx context.Context) {
	if s == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	actors := make([]string, 0, len(s.agents))
	for actor := range s.agents {
		actors = append(actors, actor)
	}
	s.mu.Unlock()
	sort.Strings(actors)
	for _, actor := range actors {
		s.kickScan(ctx, actor)
	}
}

// Start performs an immediate heartbeat, then starts one nonblocking tick per
// Interval. Cancelling ctx closes the scheduler and cooperatively stops active
// background work; a reservation remains held until its owner returns.
func (s *Scheduler) Start(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		s.shutdown()
		return nil
	}
	s.Tick(ctx)
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.shutdown()
			return nil
		case <-ticker.C:
			s.Tick(ctx)
		}
	}
}

// Wait waits for shutdown and for all actor scans and reservations to finish.
// Use a fresh bounded context after Start returns; canceled owner work may need
// to finish its checkpoint and finalization before the slot is released.
func (s *Scheduler) Wait(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		s.mu.Lock()
		if s.closed && s.drainedLocked() {
			s.mu.Unlock()
			return nil
		}
		changed := s.changed
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}

// Acquire waits for the actor's work slot. Waiters are ordered by descending
// priority and FIFO within equal priorities. A higher-priority waiter requests
// a cooperative stop of lower-priority background work, but owns the slot only
// after that work has returned and finalized.
func (s *Scheduler) Acquire(ctx context.Context, actor string, priority domaintask.Priority) (func(), error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil {
		return nil, ErrSchedulerClosed
	}
	if current, ok := ctx.Value(reservationContextKey{}).(*reservation); ok && current.scheduler == s && current.actor == actor {
		s.mu.Lock()
		state := s.agents[actor]
		if state != nil && state.active == current {
			s.mu.Unlock()
			return func() {}, nil
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	state := s.agents[actor]
	if state == nil {
		s.mu.Unlock()
		return nil, ErrUnknownActor
	}
	if s.closed {
		s.mu.Unlock()
		return nil, ErrSchedulerClosed
	}
	s.cancelScanLocked(state)
	s.nextOrder++
	w := &waiter{priority: priority, order: s.nextOrder, ready: make(chan struct{})}
	state.waiters = append(state.waiters, w)
	sortWaiters(state.waiters)
	s.requestStopLocked(state, priority)
	s.grantNextLocked(state)
	s.mu.Unlock()

	select {
	case <-w.ready:
		s.mu.Lock()
		closed := s.closed
		ctxErr := ctx.Err()
		lease := w.lease
		granted := lease != nil && state.active == lease
		var rescan context.Context
		if granted && (closed || ctxErr != nil) {
			rescan = s.releaseLocked(state, lease)
			s.signalChangedLocked()
		}
		s.mu.Unlock()
		if closed {
			if rescan != nil {
				s.kickScan(rescan, actor)
			}
			return nil, ErrSchedulerClosed
		}
		if ctxErr != nil {
			if rescan != nil {
				s.kickScan(rescan, actor)
			}
			return nil, ctxErr
		}
		if !granted {
			return nil, ErrSchedulerClosed
		}
		return s.releaseFunc(lease), nil
	case <-ctx.Done():
		s.cancelWaiter(state, w)
		return nil, ctx.Err()
	case <-s.done:
		s.cancelWaiter(state, w)
		return nil, ErrSchedulerClosed
	}
}

// TryAcquire reserves the actor slot only when it is immediately idle. It does
// not queue, cancel a scan, or request a stop from existing work.
func (s *Scheduler) TryAcquire(ctx context.Context, actor string, priority domaintask.Priority) (func(), bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if s == nil {
		return nil, false, ErrSchedulerClosed
	}
	if current, ok := ctx.Value(reservationContextKey{}).(*reservation); ok && current.scheduler == s && current.actor == actor {
		s.mu.Lock()
		state := s.agents[actor]
		if state != nil && state.active == current {
			s.mu.Unlock()
			return func() {}, true, nil
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	state := s.agents[actor]
	if state == nil {
		s.mu.Unlock()
		return nil, false, ErrUnknownActor
	}
	if s.closed {
		s.mu.Unlock()
		return nil, false, ErrSchedulerClosed
	}
	// An in-progress scan may be about to discover BOARD work. Yield to it
	// instead of canceling it for this legacy low-priority execution.
	if state.scanning || state.active != nil || len(state.waiters) != 0 {
		s.mu.Unlock()
		return nil, false, nil
	}
	lease := &reservation{
		scheduler: s,
		actor:     actor,
		priority:  priority,
		stop:      make(chan struct{}),
	}
	state.active = lease
	state.rescanAfterRun = true
	state.rescanContext = ctx
	s.signalChangedLocked()
	s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		s.releaseFunc(lease)()
		return nil, false, err
	}
	return s.releaseFunc(lease), true, nil
}

// Select returns the highest-priority work, then the oldest item, then the
// lexicographically smallest stable ID. Input order resolves a fully equal tie.
func Select(works []Work) (Work, bool) {
	if len(works) == 0 {
		return Work{}, false
	}
	best := works[0]
	for _, candidate := range works[1:] {
		if priorityRank(candidate.Priority) > priorityRank(best.Priority) ||
			(priorityRank(candidate.Priority) == priorityRank(best.Priority) && candidate.CreatedAt.Before(best.CreatedAt)) ||
			(priorityRank(candidate.Priority) == priorityRank(best.Priority) && candidate.CreatedAt.Equal(best.CreatedAt) && candidate.ID < best.ID) {
			best = candidate
		}
	}
	return best, true
}

func (s *Scheduler) kickScan(parent context.Context, actor string) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	state := s.agents[actor]
	if state == nil || s.closed || parent.Err() != nil || len(state.waiters) != 0 ||
		(state.active != nil && !state.active.idle) {
		s.mu.Unlock()
		return
	}
	if state.scanning {
		state.scanDirty = true
		state.nextScan = parent
		s.mu.Unlock()
		return
	}
	scanCtx, cancel := context.WithCancel(parent)
	allowIdle := state.active == nil
	state.scanning = true
	state.scanCancel = cancel
	s.signalChangedLocked()
	s.mu.Unlock()
	go s.scanActor(parent, scanCtx, actor, allowIdle)
}

func (s *Scheduler) scanActor(parent, scanCtx context.Context, actor string, allowIdle bool) {
	defer func() {
		var next context.Context
		s.mu.Lock()
		if state := s.agents[actor]; state != nil {
			state.scanning = false
			state.scanCancel = nil
			if state.scanDirty {
				next = state.nextScan
				state.scanDirty = false
				state.nextScan = nil
			}
		}
		s.signalChangedLocked()
		s.mu.Unlock()
		if next != nil && next.Err() == nil {
			s.kickScan(next, actor)
		}
	}()

	work, idle, found := s.discover(scanCtx, actor, allowIdle)
	if !found || parent.Err() != nil || scanCtx.Err() != nil {
		return
	}
	s.startWork(parent, actor, work, idle)
}

func (s *Scheduler) discover(ctx context.Context, actor string, allowIdle bool) (Work, bool, bool) {
	if work, found, err := s.ask(s.sources.Board, ctx, actor); err != nil {
		s.reportError(actor, "board", err, ctx)
	} else if found {
		return work, false, true
	}
	if ctx.Err() != nil {
		return Work{}, false, false
	}

	if work, found, err := s.ask(s.sources.Private, ctx, actor); err != nil {
		s.reportError(actor, "private", err, ctx)
		return Work{}, false, false
	} else if found {
		return work, false, true
	}
	if ctx.Err() != nil || !allowIdle {
		return Work{}, false, false
	}

	if work, found, err := s.ask(s.sources.Idle, ctx, actor); err != nil {
		s.reportError(actor, "idle", err, ctx)
		return Work{}, false, false
	} else if found {
		return work, true, true
	}
	return Work{}, false, false
}

func (s *Scheduler) ask(source Source, parent context.Context, actor string) (work Work, found bool, err error) {
	if source == nil {
		return Work{}, false, nil
	}
	if err := parent.Err(); err != nil {
		return Work{}, false, err
	}
	ctx, cancel := context.WithTimeout(parent, s.discoveryTimeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			work = Work{}
			found = false
			err = fmt.Errorf("source panicked: %v", recovered)
		}
	}()
	work, found, err = source(ctx, actor)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Work{}, false, ctxErr
	}
	if err != nil {
		return Work{}, false, err
	}
	if !found {
		if err := ctx.Err(); err != nil {
			return Work{}, false, err
		}
		return Work{}, false, nil
	}
	if work.ID == "" {
		return Work{}, false, errors.New("source returned work with an empty ID")
	}
	if work.Run == nil {
		return Work{}, false, fmt.Errorf("source returned work %q without a runner", work.ID)
	}
	return work, true, nil
}

func (s *Scheduler) startWork(parent context.Context, actor string, work Work, idle bool) {
	s.mu.Lock()
	state := s.agents[actor]
	if state == nil || s.closed || parent.Err() != nil {
		s.mu.Unlock()
		return
	}
	if state.active != nil || len(state.waiters) != 0 {
		if state.active != nil && state.active.background &&
			((state.active.idle && !idle) || (!idle && priorityRank(work.Priority) > priorityRank(state.active.priority))) {
			s.closeStopLocked(state.active)
		}
		state.rescanAfterRun = true
		state.rescanContext = parent
		s.mu.Unlock()
		return
	}

	runCtx, cancel := context.WithCancel(parent)
	lease := &reservation{
		scheduler:  s,
		actor:      actor,
		priority:   work.Priority,
		background: true,
		idle:       idle,
		stop:       make(chan struct{}),
		cancel:     cancel,
	}
	state.active = lease
	s.signalChangedLocked()
	runCtx = context.WithValue(runCtx, reservationContextKey{}, lease)
	s.mu.Unlock()
	go s.runWork(runCtx, lease, work)
}

func (s *Scheduler) runWork(ctx context.Context, lease *reservation, work Work) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.reportError(lease.actor, "run", fmt.Errorf("work %q panicked: %v", work.ID, recovered), ctx)
		}
		if lease.cancel != nil {
			lease.cancel()
		}
		s.finishReservation(lease)
	}()
	if err := work.Run(ctx, lease.stop); err != nil && ctx.Err() == nil {
		s.reportError(lease.actor, "run", err, ctx)
	}
}

func (s *Scheduler) releaseFunc(lease *reservation) func() {
	var once sync.Once
	return func() { once.Do(func() { s.finishReservation(lease) }) }
}

func (s *Scheduler) finishReservation(lease *reservation) {
	s.mu.Lock()
	state := s.agents[lease.actor]
	var next context.Context
	if state != nil {
		next = s.releaseLocked(state, lease)
		s.signalChangedLocked()
	}
	s.mu.Unlock()
	if next != nil && next.Err() == nil {
		s.kickScan(next, lease.actor)
	}
}

func (s *Scheduler) releaseLocked(state *agentState, lease *reservation) context.Context {
	if state.active != lease {
		return nil
	}
	state.active = nil
	if state.waiters != nil {
		s.grantNextLocked(state)
	}
	if state.active == nil && len(state.waiters) == 0 && state.rescanAfterRun {
		next := state.rescanContext
		state.rescanAfterRun = false
		state.rescanContext = nil
		return next
	}
	return nil
}

func (s *Scheduler) grantNextLocked(state *agentState) {
	if s.closed || state.active != nil || len(state.waiters) == 0 {
		return
	}
	sortWaiters(state.waiters)
	w := state.waiters[0]
	state.waiters = state.waiters[1:]
	lease := &reservation{
		scheduler: s,
		actor:     state.actor,
		priority:  w.priority,
		stop:      make(chan struct{}),
	}
	state.active = lease
	w.lease = lease
	close(w.ready)
	s.signalChangedLocked()
}

func (s *Scheduler) requestStopLocked(state *agentState, priority domaintask.Priority) {
	active := state.active
	if active != nil && active.background &&
		(active.idle || priorityRank(priority) > priorityRank(active.priority)) {
		s.closeStopLocked(active)
	}
}

func (s *Scheduler) closeStopLocked(lease *reservation) {
	if lease != nil && !lease.stopClosed {
		close(lease.stop)
		lease.stopClosed = true
	}
}

func (s *Scheduler) cancelWaiter(state *agentState, w *waiter) {
	s.mu.Lock()
	for i, queued := range state.waiters {
		if queued == w {
			state.waiters = append(state.waiters[:i], state.waiters[i+1:]...)
			break
		}
	}
	var next context.Context
	if w.lease != nil && state.active == w.lease {
		next = s.releaseLocked(state, w.lease)
	} else {
		s.grantNextLocked(state)
	}
	s.signalChangedLocked()
	s.mu.Unlock()
	if next != nil && next.Err() == nil {
		s.kickScan(next, w.lease.actor)
	}
}

func (s *Scheduler) shutdown() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	close(s.done)
	for _, state := range s.agents {
		if state.scanCancel != nil {
			state.scanCancel()
		}
		state.scanDirty = false
		state.nextScan = nil
		if state.active != nil {
			s.closeStopLocked(state.active)
			if state.active.cancel != nil {
				state.active.cancel()
			}
		}
	}
	s.signalChangedLocked()
	s.mu.Unlock()
}

func (s *Scheduler) cancelScanLocked(state *agentState) {
	if state.scanCancel != nil {
		state.scanCancel()
	}
	state.scanDirty = false
	state.nextScan = nil
}

func (s *Scheduler) drainedLocked() bool {
	for _, state := range s.agents {
		if state.scanning || state.active != nil {
			return false
		}
	}
	return true
}

func (s *Scheduler) signalChangedLocked() {
	if s.changed == nil {
		s.changed = make(chan struct{})
		return
	}
	close(s.changed)
	s.changed = make(chan struct{})
}

func (s *Scheduler) reportError(actor, stage string, err error, ctx context.Context) {
	if err == nil || (ctx != nil && ctx.Err() != nil) || s.report == nil {
		return
	}
	s.reportMu.Lock()
	defer s.reportMu.Unlock()
	defer func() { _ = recover() }()
	s.report(actor, stage, err)
}

func sortWaiters(waiters []*waiter) {
	sort.SliceStable(waiters, func(i, j int) bool {
		left, right := priorityRank(waiters[i].priority), priorityRank(waiters[j].priority)
		if left != right {
			return left > right
		}
		return waiters[i].order < waiters[j].order
	})
}

func priorityRank(priority domaintask.Priority) int {
	switch priority {
	case domaintask.PriorityCritical:
		return 4
	case domaintask.PriorityHigh:
		return 3
	case domaintask.PriorityNormal:
		return 2
	case domaintask.PriorityLow:
		return 1
	default:
		return 0
	}
}
