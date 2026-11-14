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
	// Participants adds actors to reserve with idle Work. It is ignored for
	// BOARD and private work.
	Participants []string
	Run          func(context.Context, <-chan struct{}) error
}

// Source discovers and plans one candidate for an actor. It must honor ctx;
// the scheduler bounds each call and never overlaps scans for the same actor.
type Source func(context.Context, string) (Work, bool, error)

type Sources struct {
	Board   Source
	Private Source
	Idle    Source
	// IdleRequiresBoard blocks Idle unless Board is configured and successfully
	// confirms no work. A nil Board is unavailable when this is true.
	IdleRequiresBoard bool
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
	tickGeneration   uint64
	closed           bool
	done             chan struct{}
}

type agentState struct {
	actor  string
	active *reservation

	waiters []*waiter

	scanning       bool
	scanCancel     context.CancelFunc
	scanDirty      bool
	nextScan       context.Context
	nextScanTick   uint64
	scanGeneration uint64
	scanTick       uint64

	rescanAfterRun bool
	rescanContext  context.Context
}

type reservation struct {
	scheduler  *Scheduler
	actor      string
	actors     []string
	priority   domaintask.Priority
	background bool
	idle       bool
	source     workSource
	stop       chan struct{}
	stopClosed bool
	cancel     context.CancelFunc
}

type workSource uint8

const (
	workSourceNone workSource = iota
	workSourceBoard
	workSourcePrivate
	workSourceIdle
)

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
	tickGeneration := s.nextTickGenerationLocked()
	actors := make([]string, 0, len(s.agents))
	for actor := range s.agents {
		actors = append(actors, actor)
	}
	s.mu.Unlock()
	sort.Strings(actors)
	for _, actor := range actors {
		s.kickScanAt(ctx, actor, tickGeneration)
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
	if current, ok := ctx.Value(reservationContextKey{}).(*reservation); ok && current.scheduler == s && current.owns(actor) {
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
	if current, ok := ctx.Value(reservationContextKey{}).(*reservation); ok && current.scheduler == s && current.owns(actor) {
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

// CanStartIdle reports whether an actor currently has an unreserved work slot.
// It is advisory; idle group admission rechecks every participant atomically.
func (s *Scheduler) CanStartIdle(actorID string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.agents[actorID]
	return !s.closed && state != nil && state.active == nil && len(state.waiters) == 0
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
	s.mu.Lock()
	tickGeneration := s.nextTickGenerationLocked()
	s.mu.Unlock()
	s.kickScanAt(parent, actor, tickGeneration)
}

func (s *Scheduler) kickScanAt(parent context.Context, actor string, tickGeneration uint64) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	state := s.agents[actor]
	activePrivate := state != nil && state.active != nil && state.active.background && state.active.source == workSourcePrivate && !state.active.idle
	if state == nil || s.closed || parent.Err() != nil || len(state.waiters) != 0 ||
		(state.active != nil && !state.active.idle && !activePrivate) {
		s.mu.Unlock()
		return
	}
	if state.scanning {
		state.scanDirty = true
		state.nextScan = parent
		state.nextScanTick = tickGeneration
		s.mu.Unlock()
		return
	}
	scanCtx, cancel := context.WithCancel(parent)
	allowIdle := state.active == nil
	var expectedActive *reservation
	if activePrivate {
		expectedActive = state.active
	}
	state.scanGeneration++
	generation := state.scanGeneration
	state.scanTick = tickGeneration
	state.scanning = true
	state.scanCancel = cancel
	s.signalChangedLocked()
	s.mu.Unlock()
	go s.scanActor(parent, scanCtx, actor, allowIdle, expectedActive, generation, tickGeneration)
}

func (s *Scheduler) scanActor(parent, scanCtx context.Context, actor string, allowIdle bool, expectedActive *reservation, generation, tickGeneration uint64) {
	defer func() {
		var next context.Context
		var nextTick uint64
		s.mu.Lock()
		if state := s.agents[actor]; state != nil {
			state.scanning = false
			state.scanCancel = nil
			if state.scanDirty {
				next = state.nextScan
				nextTick = state.nextScanTick
				state.scanDirty = false
				state.nextScan = nil
				state.nextScanTick = 0
			}
		}
		s.signalChangedLocked()
		s.mu.Unlock()
		if next != nil && next.Err() == nil {
			s.kickScanAt(next, actor, nextTick)
		}
	}()

	boardOnly := expectedActive != nil
	work, source := s.discover(scanCtx, actor, allowIdle, boardOnly)
	if source == workSourceNone || parent.Err() != nil || scanCtx.Err() != nil {
		return
	}
	s.startWork(parent, actor, work, source, expectedActive, generation, tickGeneration)
}

func (s *Scheduler) discover(ctx context.Context, actor string, allowIdle bool, boardOnly bool) (Work, workSource) {
	work, source, boardAvailable, tasksEmpty := s.discoverTasks(ctx, actor, boardOnly)
	if source != workSourceNone {
		return work, source
	}
	if ctx.Err() != nil || boardOnly || !allowIdle || !tasksEmpty || (s.sources.IdleRequiresBoard && !boardAvailable) {
		return Work{}, workSourceNone
	}

	if work, found, err := s.ask(s.sources.Idle, ctx, actor); err != nil {
		s.reportError(actor, "idle", err, ctx)
		return Work{}, workSourceNone
	} else if found {
		return work, workSourceIdle
	}
	return Work{}, workSourceNone
}

func (s *Scheduler) discoverTasks(ctx context.Context, actor string, boardOnly bool) (Work, workSource, bool, bool) {
	boardAvailable := s.sources.Board != nil
	if work, found, err := s.ask(s.sources.Board, ctx, actor); err != nil {
		boardAvailable = false
		s.reportError(actor, "board", err, ctx)
	} else if found {
		return work, workSourceBoard, boardAvailable, false
	}
	if ctx.Err() != nil || boardOnly {
		return Work{}, workSourceNone, boardAvailable, false
	}

	if work, found, err := s.ask(s.sources.Private, ctx, actor); err != nil {
		s.reportError(actor, "private", err, ctx)
		return Work{}, workSourceNone, boardAvailable, false
	} else if found {
		return work, workSourcePrivate, boardAvailable, false
	}
	return Work{}, workSourceNone, boardAvailable, ctx.Err() == nil
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

func (s *Scheduler) startWork(parent context.Context, actor string, work Work, source workSource, expectedActive *reservation, generation, tickGeneration uint64) {
	if source == workSourceIdle && len(work.Participants) != 0 {
		s.startIdleGroup(parent, actor, work, generation, tickGeneration)
		return
	}
	s.mu.Lock()
	state := s.agents[actor]
	if state == nil || s.closed || parent.Err() != nil || state.scanGeneration != generation ||
		(expectedActive != nil && state.active != expectedActive) {
		s.mu.Unlock()
		return
	}
	if expectedActive != nil {
		if source == workSourceBoard && len(state.waiters) == 0 && expectedActive.background && expectedActive.source == workSourcePrivate &&
			!expectedActive.idle && priorityRank(work.Priority) > priorityRank(expectedActive.priority) {
			s.closeStopLocked(state.active)
			state.rescanAfterRun = true
			state.rescanContext = parent
		}
		s.mu.Unlock()
		return
	}
	if state.active != nil || len(state.waiters) != 0 {
		if state.active != nil && state.active.background &&
			((state.active.idle && source != workSourceIdle) || (source != workSourceIdle && priorityRank(work.Priority) > priorityRank(state.active.priority))) {
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
		idle:       source == workSourceIdle,
		source:     source,
		stop:       make(chan struct{}),
		cancel:     cancel,
	}
	state.active = lease
	s.signalChangedLocked()
	runCtx = context.WithValue(runCtx, reservationContextKey{}, lease)
	s.mu.Unlock()
	go s.runWork(runCtx, lease, work)
}

func (s *Scheduler) startIdleGroup(parent context.Context, actor string, work Work, generation, tickGeneration uint64) {
	actors := make([]string, 1, len(work.Participants)+1)
	actors[0] = actor
	seen := map[string]struct{}{actor: {}}
	for _, participant := range work.Participants {
		if participant == "" {
			s.reportError(actor, "idle", errors.New("idle work listed an empty participant"), parent)
			return
		}
		if _, duplicate := seen[participant]; duplicate {
			s.reportError(actor, "idle", fmt.Errorf("idle work listed participant %q more than once or as its primary actor", participant), parent)
			return
		}
		seen[participant] = struct{}{}
		actors = append(actors, participant)
	}

	// Fail early for participants already owned or queued. Repeat this check at
	// commit because an Acquire or another scan may race the bounded probes.
	s.mu.Lock()
	primary := s.agents[actor]
	if primary == nil || s.closed || parent.Err() != nil || primary.scanGeneration != generation || primary.scanTick != tickGeneration || primary.active != nil || len(primary.waiters) != 0 {
		s.mu.Unlock()
		return
	}
	for _, participant := range actors[1:] {
		state := s.agents[participant]
		if state == nil || state.active != nil || len(state.waiters) != 0 {
			s.mu.Unlock()
			return
		}
	}
	s.mu.Unlock()

	// Recheck every extra actor's task sources under one bounded context. These
	// probes are read-only; the lock below decides whether their slots can join
	// the same idle reservation.
	probeCtx, cancelProbe := context.WithTimeout(parent, s.discoveryTimeout)
	defer cancelProbe()
	for _, participant := range actors[1:] {
		_, source, boardAvailable, empty := s.discoverTasks(probeCtx, participant, false)
		if source != workSourceNone {
			// The probe itself never claims work. Ask the participant's normal
			// scan to pick it up promptly if its current scan did not already do so.
			if probeCtx.Err() == nil {
				s.kickScan(parent, participant)
			}
			return
		}
		if probeCtx.Err() != nil || !empty || (s.sources.IdleRequiresBoard && !boardAvailable) {
			return
		}
	}
	if probeCtx.Err() != nil {
		return
	}

	s.mu.Lock()
	primary = s.agents[actor]
	if primary == nil || s.closed || parent.Err() != nil || primary.scanGeneration != generation || primary.scanTick != tickGeneration || primary.active != nil || len(primary.waiters) != 0 {
		s.mu.Unlock()
		return
	}
	for _, participant := range actors[1:] {
		state := s.agents[participant]
		if state == nil || state.active != nil || len(state.waiters) != 0 {
			s.mu.Unlock()
			return
		}
	}

	runCtx, cancel := context.WithCancel(parent)
	lease := &reservation{
		scheduler:  s,
		actor:      actor,
		actors:     append([]string(nil), actors...),
		priority:   work.Priority,
		background: true,
		idle:       true,
		source:     workSourceIdle,
		stop:       make(chan struct{}),
		cancel:     cancel,
	}
	for _, participant := range actors {
		state := s.agents[participant]
		if participant != actor && state.scanning {
			nextScan, nextTick := state.nextScan, state.nextScanTick
			keepNextScan := state.scanDirty && nextScan != nil
			s.cancelScanLocked(state)
			state.scanDirty = true
			if keepNextScan {
				// A newer heartbeat may already be queued. Preserve its context and
				// generation instead of replacing them with this older routine scan.
				state.nextScan = nextScan
				state.nextScanTick = nextTick
			} else {
				state.nextScan = parent
				state.nextScanTick = tickGeneration
			}
		}
		state.active = lease
	}
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
	next := s.releaseGroupLocked(lease)
	s.signalChangedLocked()
	s.mu.Unlock()
	for _, scan := range next {
		if scan.ctx != nil && scan.ctx.Err() == nil {
			s.kickScan(scan.ctx, scan.actor)
		}
	}
}

type rescanRequest struct {
	actor string
	ctx   context.Context
}

func (s *Scheduler) releaseGroupLocked(lease *reservation) []rescanRequest {
	if lease == nil {
		return nil
	}
	actors := lease.actors
	if len(actors) == 0 {
		actors = []string{lease.actor}
	}
	released := make([]*agentState, 0, len(actors))
	for _, actor := range actors {
		state := s.agents[actor]
		if state == nil || state.active != lease {
			continue
		}
		state.active = nil
		released = append(released, state)
	}
	for _, state := range released {
		s.grantNextLocked(state)
	}
	var next []rescanRequest
	for _, state := range released {
		if state.active == nil && len(state.waiters) == 0 && state.rescanAfterRun {
			if state.rescanContext != nil {
				next = append(next, rescanRequest{actor: state.actor, ctx: state.rescanContext})
			}
			state.rescanAfterRun = false
			state.rescanContext = nil
		}
	}
	return next
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

func (lease *reservation) owns(actor string) bool {
	if lease == nil {
		return false
	}
	if len(lease.actors) == 0 {
		return lease.actor == actor
	}
	for _, reservedActor := range lease.actors {
		if reservedActor == actor {
			return true
		}
	}
	return false
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
	state.scanGeneration++
	state.scanDirty = false
	state.nextScan = nil
	state.nextScanTick = 0
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

func (s *Scheduler) nextTickGenerationLocked() uint64 {
	s.tickGeneration++
	return s.tickGeneration
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
