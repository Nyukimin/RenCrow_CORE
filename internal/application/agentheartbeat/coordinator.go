package agentheartbeat

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	Interval                = 10 * time.Second
	defaultDiscoveryTimeout = 2 * time.Second
	maxSeriesWorkIDs        = 4096
)

var (
	ErrSchedulerClosed = errors.New("agent heartbeat scheduler is closed")
	ErrUnknownActor    = errors.New("agent is not registered with the heartbeat scheduler")
)

// Work describes a task discovered without claiming or executing it. Run owns
// those effects after the scheduler has reserved the actor's single work slot.
type Work struct {
	ID               string
	Priority         domaintask.Priority
	CreatedAt        time.Time
	ActivityKind     ActivityKind
	VisibilityTaskID modulecore.TaskID
	// Participants adds actors to reserve with idle Work. It is ignored for
	// BOARD and private work.
	Participants []string
	Run          func(context.Context, <-chan struct{}) error
}

// ActivityKind is a fixed, safe category for a live activity projection.
type ActivityKind string

const (
	ActivityKindBoard      ActivityKind = "board"
	ActivityKindPrivate    ActivityKind = "private"
	ActivityKindGame       ActivityKind = "game"
	ActivityKindRoutine    ActivityKind = "routine"
	ActivityKindForeground ActivityKind = "foreground"
)

func activityKindFor(source workSource, requested ActivityKind) ActivityKind {
	switch source {
	case workSourceBoard:
		return ActivityKindBoard
	case workSourcePrivate:
		return ActivityKindPrivate
	case workSourceIdle:
		switch requested {
		case ActivityKindGame, ActivityKindRoutine:
			return requested
		default:
			return ""
		}
	default:
		return ActivityKindForeground
	}
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
	started          bool
	closed           bool
	startCtx         context.Context
	done             chan struct{}
}

type agentState struct {
	actor  string
	active *reservation

	waiters []*waiter

	scanning          bool
	scanCancel        context.CancelFunc
	scanDirty         bool
	nextScan          context.Context
	nextScanTick      uint64
	nextScanAllowIdle bool
	scanGeneration    uint64
	scanTick          uint64

	rescanAfterRun  bool
	rescanContext   context.Context
	rescanTick      uint64
	rescanAllowIdle bool
	pendingWake     bool
	seriesTick      uint64
	seriesSeen      map[string]struct{}
	seriesSaturated bool
}

type reservation struct {
	scheduler        *Scheduler
	actor            string
	actors           []string
	priority         domaintask.Priority
	background       bool
	idle             bool
	source           workSource
	activityKind     ActivityKind
	visibilityTaskID modulecore.TaskID
	workID           string
	stop             chan struct{}
	stopClosed       bool
	cancel           context.CancelFunc
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

// LivingActivity is the privacy-safe activity state of one actor's scheduler
// slot. It excludes work IDs, errors, and source payloads. Title is populated
// only by an optional read-time lookup after publication visibility is checked.
type LivingActivity struct {
	ActorID      string
	State        string
	ActivityKind string
	Title        string
}

// ForegroundWaiterPriority is the privacy-safe scheduler projection for one
// queued slot waiter. Waiters do not carry task identity or title.
type ForegroundWaiterPriority struct {
	Position int
	Priority string
}

// ForegroundWaiterQueue is the ordered set of foreground requests waiting for
// one actor's active scheduler slot.
type ForegroundWaiterQueue struct {
	ActorID string
	Count   int
	Items   []ForegroundWaiterPriority
}

// LivingSnapshot returns mutex-owned scheduler facts for the LIVING read
// projection. Any optional title lookup runs after releasing the scheduler
// mutex and must enforce the caller's publication-visibility policy. ok is
// false until the owner has started and after it shuts down, so callers cannot
// turn a lost scheduler into an idle-looking response.
func (s *Scheduler) LivingSnapshot(ctx context.Context, titleLookup func(context.Context, modulecore.TaskID) (string, error)) ([]LivingActivity, bool) {
	return s.livingSnapshot(ctx, titleLookup, false)
}

// OwnerLivingSnapshot returns the same scheduler facts as LivingSnapshot but
// permits the authenticated CORE owner projection to resolve the current
// Task title for every source. Public callers must keep using LivingSnapshot
// with their publication visibility policy.
func (s *Scheduler) OwnerLivingSnapshot(ctx context.Context, titleLookup func(context.Context, modulecore.TaskID) (string, error)) ([]LivingActivity, bool) {
	return s.livingSnapshot(ctx, titleLookup, true)
}

// OwnerForegroundWaiterSnapshot returns only queued waiter priorities and
// positions. It exposes no work identity, input, or title.
func (s *Scheduler) OwnerForegroundWaiterSnapshot() ([]ForegroundWaiterQueue, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.started || s.closed {
		return nil, false
	}
	actors := make([]string, 0, len(s.agents))
	for actor := range s.agents {
		actors = append(actors, actor)
	}
	sort.Strings(actors)
	queues := make([]ForegroundWaiterQueue, 0, len(actors))
	for _, actor := range actors {
		state := s.agents[actor]
		waiters := append([]*waiter(nil), state.waiters...)
		sortWaiters(waiters)
		queue := ForegroundWaiterQueue{
			ActorID: actor,
			Count:   len(waiters),
			Items:   make([]ForegroundWaiterPriority, 0, len(waiters)),
		}
		for index, item := range waiters {
			queue.Items = append(queue.Items, ForegroundWaiterPriority{
				Position: index + 1,
				Priority: string(item.priority),
			})
		}
		queues = append(queues, queue)
	}
	return queues, true
}

func (s *Scheduler) livingSnapshot(ctx context.Context, titleLookup func(context.Context, modulecore.TaskID) (string, error), ownerView bool) ([]LivingActivity, bool) {
	if s == nil {
		return nil, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	if !s.started || s.closed {
		s.mu.Unlock()
		return nil, false
	}

	actors := make([]string, 0, len(s.agents))
	for actor := range s.agents {
		actors = append(actors, actor)
	}
	sort.Strings(actors)
	activities := make([]LivingActivity, 0, len(actors))
	titleCandidates := make([]struct {
		activityIndex int
		taskID        modulecore.TaskID
	}, 0, len(actors))
	for _, actor := range actors {
		state := s.agents[actor]
		activity := LivingActivity{ActorID: actor, State: "waiting"}
		switch {
		case state.active != nil:
			activity.State = "running"
			activity.ActivityKind = string(state.active.activityKind)
			if activity.ActivityKind == "" && state.active.source == workSourceNone {
				activity.ActivityKind = string(ActivityKindForeground)
			}
			if state.active.stopClosed {
				activity.State = "stopping"
			}
			if (ownerView || state.active.source == workSourcePrivate) && state.active.visibilityTaskID.Validate() == nil {
				titleCandidates = append(titleCandidates, struct {
					activityIndex int
					taskID        modulecore.TaskID
				}{activityIndex: len(activities), taskID: state.active.visibilityTaskID})
			}
		case state.scanning:
			activity.State = "checking"
		}
		activities = append(activities, activity)
	}
	s.mu.Unlock()

	if titleLookup != nil {
		for _, candidate := range titleCandidates {
			if err := ctx.Err(); err != nil {
				break
			}
			title, err := titleLookup(ctx, candidate.taskID)
			if err == nil && title != "" {
				activities[candidate.activityIndex].Title = title
			}
		}
	}
	return activities, true
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

// Wake requests a fresh Board-to-Private discovery pass after the caller has
// committed the state that can make work eligible. It is a coalesced hint; it
// does not persist queue state or permit idle work. Before Start, the initial
// Start sweep serves the hint.
func (s *Scheduler) Wake(actor string) error {
	if s == nil {
		return ErrSchedulerClosed
	}
	s.mu.Lock()
	state := s.agents[actor]
	if state == nil {
		s.mu.Unlock()
		return ErrUnknownActor
	}
	if s.closed {
		s.mu.Unlock()
		return ErrSchedulerClosed
	}
	if !s.started {
		state.pendingWake = true
		s.mu.Unlock()
		return nil
	}
	ctx := s.startCtx
	tick := state.seriesTick
	if tick == 0 {
		tick = s.tickGeneration
	}
	s.mu.Unlock()
	s.kickScanMode(ctx, actor, tick, false)
	return nil
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
	s.mu.Lock()
	s.started = true
	s.startCtx = ctx
	s.mu.Unlock()
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
		var rescan *rescanRequest
		if granted && (closed || ctxErr != nil) {
			rescan = s.releaseLocked(state, lease)
			s.signalChangedLocked()
		}
		s.mu.Unlock()
		if closed {
			if rescan != nil {
				s.kickRescanRequest(rescan)
			}
			return nil, ErrSchedulerClosed
		}
		if ctxErr != nil {
			if rescan != nil {
				s.kickRescanRequest(rescan)
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

func (s *Scheduler) kickScanAt(parent context.Context, actor string, tickGeneration uint64) {
	s.kickScanMode(parent, actor, tickGeneration, true)
}

func (s *Scheduler) kickScanMode(parent context.Context, actor string, tickGeneration uint64, allowIdle bool) {
	if parent == nil {
		parent = context.Background()
	}
	s.mu.Lock()
	state := s.agents[actor]
	activePrivate := state != nil && state.active != nil && state.active.background && state.active.source == workSourcePrivate && !state.active.idle
	if state == nil || s.closed || parent.Err() != nil {
		s.mu.Unlock()
		return
	}
	if tickGeneration > state.seriesTick {
		state.seriesTick = tickGeneration
		state.seriesSeen = make(map[string]struct{})
		state.seriesSaturated = false
		if state.active != nil && state.active.workID != "" {
			state.seriesSeen[workKey(state.active.source, state.active.workID)] = struct{}{}
		}
	}
	if len(state.waiters) != 0 || (state.active != nil && !state.active.idle && !activePrivate) {
		s.mergeRescanLocked(state, parent, tickGeneration, allowIdle)
		s.mu.Unlock()
		return
	}
	if state.scanning {
		if !state.scanDirty || tickGeneration > state.nextScanTick {
			state.nextScanTick = tickGeneration
			state.nextScanAllowIdle = allowIdle
		} else if tickGeneration == state.nextScanTick {
			state.nextScanAllowIdle = state.nextScanAllowIdle || allowIdle
		}
		state.scanDirty = true
		state.nextScan = parent
		if state.active != nil && allowIdle {
			s.mergeRescanLocked(state, parent, tickGeneration, true)
		}
		s.mu.Unlock()
		return
	}
	scanCtx, cancel := context.WithCancel(parent)
	requestedIdle := allowIdle
	allowIdle = allowIdle && state.active == nil
	if state.active != nil && requestedIdle {
		s.mergeRescanLocked(state, parent, tickGeneration, true)
	}
	var expectedActive *reservation
	if activePrivate {
		expectedActive = state.active
	}
	state.scanGeneration++
	generation := state.scanGeneration
	state.scanTick = tickGeneration
	state.scanning = true
	state.scanCancel = cancel
	state.pendingWake = false
	s.signalChangedLocked()
	s.mu.Unlock()
	go s.scanActor(parent, scanCtx, actor, allowIdle, expectedActive, generation, tickGeneration)
}

func (s *Scheduler) scanActor(parent, scanCtx context.Context, actor string, allowIdle bool, expectedActive *reservation, generation, tickGeneration uint64) {
	defer func() {
		var next context.Context
		var nextTick uint64
		var nextAllowIdle bool
		s.mu.Lock()
		if state := s.agents[actor]; state != nil {
			state.scanning = false
			state.scanCancel = nil
			if state.scanDirty {
				next = state.nextScan
				nextTick = state.nextScanTick
				nextAllowIdle = state.nextScanAllowIdle
				state.scanDirty = false
				state.nextScan = nil
				state.nextScanTick = 0
				state.nextScanAllowIdle = false
			}
		}
		s.signalChangedLocked()
		s.mu.Unlock()
		if next != nil && next.Err() == nil {
			s.kickScanMode(next, actor, nextTick, nextAllowIdle)
		}
	}()

	boardOnly := expectedActive != nil
	work, source := s.discover(scanCtx, actor, allowIdle, boardOnly)
	if source == workSourceNone || parent.Err() != nil || scanCtx.Err() != nil {
		return
	}
	s.mu.Lock()
	state := s.agents[actor]
	_, repeated := state.seriesSeen[workKey(source, work.ID)]
	saturated := state.seriesSaturated
	s.mu.Unlock()
	if repeated || saturated {
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
		s.mergeRescanLocked(state, parent, tickGeneration, false)
		if source == workSourceBoard && len(state.waiters) == 0 && expectedActive.background && expectedActive.source == workSourcePrivate &&
			!expectedActive.idle && priorityRank(work.Priority) > priorityRank(expectedActive.priority) {
			s.closeStopLocked(state.active)
		}
		s.mu.Unlock()
		return
	}
	if state.active != nil || len(state.waiters) != 0 {
		if state.active != nil && state.active.background &&
			((state.active.idle && source != workSourceIdle) || (source != workSourceIdle && priorityRank(work.Priority) > priorityRank(state.active.priority))) {
			s.closeStopLocked(state.active)
		}
		s.mergeRescanLocked(state, parent, tickGeneration, false)
		s.mu.Unlock()
		return
	}

	runCtx, cancel := context.WithCancel(parent)
	lease := &reservation{
		scheduler:    s,
		actor:        actor,
		priority:     work.Priority,
		background:   true,
		idle:         source == workSourceIdle,
		source:       source,
		activityKind: activityKindFor(source, work.ActivityKind),
		stop:         make(chan struct{}),
		cancel:       cancel,
		workID:       work.ID,
	}
	if source == workSourcePrivate {
		lease.visibilityTaskID = work.VisibilityTaskID
	}
	state.active = lease
	if state.seriesSeen == nil {
		state.seriesSeen = make(map[string]struct{})
	}
	markSeriesWork(state, source, work.ID)
	s.mergeRescanLocked(state, parent, tickGeneration, false)
	state.pendingWake = false
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
				s.kickScanMode(parent, participant, tickGeneration, false)
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
		scheduler:    s,
		actor:        actor,
		actors:       append([]string(nil), actors...),
		priority:     work.Priority,
		background:   true,
		idle:         true,
		source:       workSourceIdle,
		activityKind: activityKindFor(workSourceIdle, work.ActivityKind),
		stop:         make(chan struct{}),
		cancel:       cancel,
		workID:       work.ID,
	}
	for _, participant := range actors {
		state := s.agents[participant]
		if participant != actor && state.scanning {
			nextScan, nextTick, nextAllowIdle := state.nextScan, state.nextScanTick, state.nextScanAllowIdle
			keepNextScan := state.scanDirty && nextScan != nil
			s.cancelScanLocked(state)
			state.scanDirty = true
			if keepNextScan {
				// A newer heartbeat may already be queued. Preserve its context and
				// generation instead of replacing them with this older routine scan.
				state.nextScan = nextScan
				state.nextScanTick = nextTick
				state.nextScanAllowIdle = nextAllowIdle
			} else {
				state.nextScan = parent
				state.nextScanTick = tickGeneration
				state.nextScanAllowIdle = true
			}
		}
		state.active = lease
		s.mergeRescanLocked(state, parent, tickGeneration, false)
		if state.seriesSeen == nil {
			state.seriesSeen = make(map[string]struct{})
		}
		markSeriesWork(state, workSourceIdle, work.ID)
	}
	s.signalChangedLocked()
	runCtx = context.WithValue(runCtx, reservationContextKey{}, lease)
	s.mu.Unlock()
	go s.runWork(runCtx, lease, work)
}

func (s *Scheduler) runWork(ctx context.Context, lease *reservation, work Work) {
	defer func() {
		if recovered := recover(); recovered != nil {
			s.reportError(lease.actor, "run", fmt.Errorf("work %q panicked: %v", work.ID, recovered), nil)
		}
		if lease.cancel != nil {
			lease.cancel()
		}
		s.finishReservation(lease)
	}()
	if err := work.Run(ctx, lease.stop); err != nil {
		if runErr := ctx.Err(); runErr == nil || !errors.Is(err, runErr) {
			s.reportError(lease.actor, "run", err, nil)
		}
	}
}

func (s *Scheduler) releaseFunc(lease *reservation) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			if lease != nil && !lease.background {
				s.mu.Lock()
				if state := s.agents[lease.actor]; state != nil && state.active == lease {
					s.mergeRescanLocked(state, s.rescanContextLocked(context.Background()), state.seriesTick, false)
				}
				s.mu.Unlock()
			}
			s.finishReservation(lease)
		})
	}
}

func (s *Scheduler) finishReservation(lease *reservation) {
	s.mu.Lock()
	next := s.releaseGroupLocked(lease)
	s.signalChangedLocked()
	s.mu.Unlock()
	for _, scan := range next {
		s.kickRescanRequest(&scan)
	}
}

type rescanRequest struct {
	actor     string
	ctx       context.Context
	tick      uint64
	allowIdle bool
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
		if state.active == nil && len(state.waiters) == 0 {
			if request := s.takeRescanLocked(state, nil); request != nil {
				next = append(next, *request)
			}
		}
	}
	return next
}

func (s *Scheduler) mergeRescanLocked(state *agentState, ctx context.Context, tick uint64, allowIdle bool) {
	if state == nil {
		return
	}
	if s.started && s.startCtx != nil {
		ctx = s.startCtx
	}
	if !state.rescanAfterRun || tick > state.rescanTick {
		state.rescanAllowIdle = allowIdle
		state.rescanTick = tick
	} else if tick == state.rescanTick {
		state.rescanAllowIdle = state.rescanAllowIdle || allowIdle
	}
	if ctx != nil {
		state.rescanContext = ctx
	}
	state.rescanAfterRun = true
}

func (s *Scheduler) rescanContextLocked(fallback context.Context) context.Context {
	if s.started && s.startCtx != nil {
		return s.startCtx
	}
	return fallback
}

func (s *Scheduler) takeRescanLocked(state *agentState, fallback context.Context) *rescanRequest {
	if state == nil || state.active != nil || len(state.waiters) != 0 {
		return nil
	}
	if !state.rescanAfterRun && !state.pendingWake {
		return nil
	}
	if !s.started && state.pendingWake && !state.rescanAfterRun {
		return nil
	}
	ctx := state.rescanContext
	if s.started && s.startCtx != nil {
		ctx = s.startCtx
	}
	if ctx == nil {
		ctx = fallback
	}
	if ctx == nil {
		return nil
	}
	request := &rescanRequest{actor: state.actor, ctx: ctx, tick: state.seriesTick, allowIdle: state.rescanAllowIdle}
	state.rescanAfterRun = false
	state.rescanContext = nil
	state.rescanTick = 0
	state.rescanAllowIdle = false
	state.pendingWake = false
	return request
}

func (s *Scheduler) kickRescanRequest(request *rescanRequest) {
	if request == nil || request.ctx == nil || request.ctx.Err() != nil {
		return
	}
	s.kickScanMode(request.ctx, request.actor, request.tick, request.allowIdle)
}

func (s *Scheduler) releaseLocked(state *agentState, lease *reservation) *rescanRequest {
	if state.active != lease {
		return nil
	}
	if !lease.background {
		s.mergeRescanLocked(state, s.rescanContextLocked(context.Background()), state.seriesTick, false)
	}
	state.active = nil
	if state.waiters != nil {
		s.grantNextLocked(state)
	}
	return s.takeRescanLocked(state, nil)
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
	var next *rescanRequest
	if w.lease != nil && state.active == w.lease {
		next = s.releaseLocked(state, w.lease)
	} else {
		s.grantNextLocked(state)
	}
	s.signalChangedLocked()
	s.mu.Unlock()
	s.kickRescanRequest(next)
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
	state.nextScanAllowIdle = false
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

func workKey(source workSource, id string) string {
	return fmt.Sprintf("%d:%s", source, id)
}

func markSeriesWork(state *agentState, source workSource, id string) {
	key := workKey(source, id)
	if _, exists := state.seriesSeen[key]; exists {
		return
	}
	if len(state.seriesSeen) >= maxSeriesWorkIDs {
		state.seriesSaturated = true
		return
	}
	state.seriesSeen[key] = struct{}{}
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
