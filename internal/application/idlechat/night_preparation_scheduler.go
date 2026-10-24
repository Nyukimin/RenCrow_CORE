package idlechat

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"
)

var ErrNightPreparationBlocked = errors.New("idlechat night preparation is blocked by schedule or foreground policy")

const nightPreparationPolicyCheckInterval = 30 * time.Second

type nightPreparationSchedulerContextKey struct{}
type nightPreparationStoryTargetContextKey struct{}

const nightPreparationStoryPrepareReason = "viewer_story_prepare"

// NightPreparationSchedule describes an allowed local-time interval. The
// interval is half-open: start is included and end is excluded.
type NightPreparationSchedule struct {
	Timezone             string
	Start                string
	End                  string
	CatchUpOutsideWindow bool
}

// NightPreparationScheduler owns admission for all IdleChat background
// preparation requests. Request only records/coalesces work; dispatch happens
// asynchronously when Tick observes an allowed window and no foreground use.
type NightPreparationScheduler struct {
	mu sync.Mutex

	location         *time.Location
	startMinute      int
	endMinute        int
	now              func() time.Time
	foregroundActive func() bool
	dispatch         func(context.Context, []string)

	pendingReasons      []string
	pendingSet          map[string]struct{}
	storyTarget         int
	dispatching         bool
	dispatchStoryTarget int
	dispatchCancel      context.CancelFunc
	managedCalls        map[uint64]context.CancelFunc
	nextCallID          uint64
	closed              bool
	done                chan struct{}
}

func NewNightPreparationScheduler(
	schedule NightPreparationSchedule,
	now func() time.Time,
	foregroundActive func() bool,
	dispatch func(context.Context, []string),
) (*NightPreparationScheduler, error) {
	if now == nil {
		return nil, errors.New("night preparation clock is required")
	}
	if foregroundActive == nil {
		return nil, errors.New("night preparation foreground policy is required")
	}
	if dispatch == nil {
		return nil, errors.New("night preparation dispatcher is required")
	}
	if schedule.CatchUpOutsideWindow {
		return nil, errors.New("night preparation catch-up outside the window is not supported")
	}
	locationName := schedule.Timezone
	location, err := time.LoadLocation(locationName)
	if err != nil || locationName == "" || strings.TrimSpace(locationName) != locationName || locationName == "Local" {
		return nil, fmt.Errorf("night preparation timezone must be a valid IANA timezone: %q", schedule.Timezone)
	}
	startMinute, err := parseNightPreparationClockTime(schedule.Start)
	if err != nil {
		return nil, fmt.Errorf("night preparation start must use HH:MM 24-hour time: %w", err)
	}
	endMinute, err := parseNightPreparationClockTime(schedule.End)
	if err != nil {
		return nil, fmt.Errorf("night preparation end must use HH:MM 24-hour time: %w", err)
	}
	if startMinute == endMinute {
		return nil, errors.New("night preparation start and end must differ")
	}

	return &NightPreparationScheduler{
		location:         location,
		startMinute:      startMinute,
		endMinute:        endMinute,
		now:              now,
		foregroundActive: foregroundActive,
		dispatch:         dispatch,
		pendingSet:       make(map[string]struct{}),
		managedCalls:     make(map[uint64]context.CancelFunc),
		done:             make(chan struct{}),
	}, nil
}

func parseNightPreparationClockTime(value string) (int, error) {
	if strings.TrimSpace(value) != value {
		return 0, fmt.Errorf("invalid clock time %q", value)
	}
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return 0, fmt.Errorf("invalid clock time %q", value)
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}

// Request records a reason for preparation and asks the scheduler to re-check
// admission. Calls outside the window return immediately and remain pending
// for deterministic reevaluation at the next allowed window.
func (s *NightPreparationScheduler) Request(reason string) bool {
	if s == nil {
		return false
	}
	reason = strings.TrimSpace(reason)
	if reason == "" || reason == nightPreparationStoryPrepareReason {
		return false
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return false
	}
	if _, exists := s.pendingSet[reason]; !exists {
		s.pendingSet[reason] = struct{}{}
		s.pendingReasons = append(s.pendingReasons, reason)
	}
	s.mu.Unlock()
	s.Tick()
	return true
}

// RequestStoryPreparationTarget records one bounded ready-count demand. The
// absolute target is captured by the caller when it receives the request and
// repeated requests coalesce to the larger target. Unlike ordinary reasons,
// this demand remains pending until its owner reports the requested ready
// count, including across a completed but partial dispatch.
func (s *NightPreparationScheduler) RequestStoryPreparationTarget(target int) (string, bool) {
	if s == nil || target < 1 {
		return "", false
	}
	state := "deferred"
	if s.policyAllowsNow() {
		state = "queued"
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return "", false
	}
	if target > s.storyTarget {
		s.storyTarget = target
	}
	s.mu.Unlock()
	s.Tick()
	return state, true
}

// PendingStoryPreparationTarget reports the scheduler-owned outstanding
// absolute target. It is intentionally volatile and owned only by this
// scheduler instance.
func (s *NightPreparationScheduler) PendingStoryPreparationTarget() (int, bool) {
	if s == nil {
		return 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.storyTarget, s.storyTarget > 0 && !s.closed
}

// CompleteStoryPreparationTarget clears the outstanding story demand only
// when the owner has reached the target dispatched to it. A larger target
// coalesced during that dispatch stays pending.
func (s *NightPreparationScheduler) CompleteStoryPreparationTarget(target int) bool {
	if s == nil || target < 1 {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.storyTarget == 0 || s.storyTarget > target {
		return false
	}
	s.storyTarget = 0
	return true
}

// Tick rechecks the schedule and foreground policy. Closing a gate cancels
// CORE-managed contexts; cancellation alone does not prove backend release.
func (s *NightPreparationScheduler) Tick() {
	if s == nil {
		return
	}
	allowed := s.policyAllowsNow()
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	if !allowed {
		s.cancelManagedLocked()
		s.mu.Unlock()
		return
	}
	if s.dispatching || (len(s.pendingReasons) == 0 && s.storyTarget == 0) {
		s.mu.Unlock()
		return
	}

	reasons := append([]string(nil), s.pendingReasons...)
	storyTarget := s.storyTarget
	if storyTarget > 0 {
		reasons = append(reasons, nightPreparationStoryPrepareReason)
	}
	s.pendingReasons = nil
	s.pendingSet = make(map[string]struct{})
	ctx, cancel := context.WithCancel(context.Background())
	ctx = withNightPreparationScheduler(ctx, s)
	ctx = withNightPreparationStoryTarget(ctx, storyTarget)
	s.dispatching = true
	s.dispatchStoryTarget = storyTarget
	s.dispatchCancel = cancel
	s.mu.Unlock()

	go func() {
		s.dispatch(ctx, reasons)
		dispatchCanceled := ctx.Err() != nil
		cancel()
		s.mu.Lock()
		s.dispatching = false
		s.dispatchCancel = nil
		s.dispatchStoryTarget = 0
		if dispatchCanceled && !s.closed {
			s.requeueReasonsLocked(reasons)
		}
		hasPending := !s.closed && (len(s.pendingReasons) > 0 || s.storyTarget > storyTarget)
		s.mu.Unlock()
		if hasPending {
			s.Tick()
		}
	}()
}

func (s *NightPreparationScheduler) requeueReasonsLocked(reasons []string) {
	if len(reasons) == 0 {
		return
	}
	pending := make([]string, 0, len(reasons)+len(s.pendingReasons))
	seen := make(map[string]struct{}, len(reasons)+len(s.pendingReasons))
	for _, reason := range append(append([]string(nil), reasons...), s.pendingReasons...) {
		if reason == nightPreparationStoryPrepareReason {
			continue
		}
		if _, exists := seen[reason]; exists {
			continue
		}
		seen[reason] = struct{}{}
		pending = append(pending, reason)
	}
	s.pendingReasons = pending
	s.pendingSet = seen
}

func (s *NightPreparationScheduler) policyAllowsNow() bool {
	return s.windowOpenAt(s.now()) && !s.foregroundActive()
}

func (s *NightPreparationScheduler) windowOpenAt(now time.Time) bool {
	local := now.In(s.location)
	minute := local.Hour()*60 + local.Minute()
	if s.startMinute < s.endMinute {
		return minute >= s.startMinute && minute < s.endMinute
	}
	return minute >= s.startMinute || minute < s.endMinute
}

// NotifyPolicyChanged rechecks admission immediately after a foreground
// transition or lease update.
func (s *NightPreparationScheduler) NotifyPolicyChanged() {
	if s != nil {
		s.Tick()
	}
}

// RunLLM checks admission at the external-request boundary and supplies a
// managed child context. Tick cancels that context when the window closes, a
// foreground user appears, or the scheduler closes.
func (s *NightPreparationScheduler) RunLLM(ctx context.Context, call func(context.Context) error) error {
	if s == nil || ctx == nil || call == nil {
		return ErrNightPreparationBlocked
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !s.policyAllowsNow() {
		s.Tick()
		return ErrNightPreparationBlocked
	}

	managedCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return ErrNightPreparationBlocked
	}
	s.nextCallID++
	callID := s.nextCallID
	s.managedCalls[callID] = cancel
	s.mu.Unlock()
	defer func() {
		cancel()
		s.mu.Lock()
		delete(s.managedCalls, callID)
		s.mu.Unlock()
	}()
	// Recheck without holding the scheduler lock. Presence snapshots acquire
	// the surface owner lock, and policy transitions can notify this scheduler.
	// Keeping those lock domains separate prevents a lock-order cycle.
	if !s.policyAllowsNow() {
		s.Tick()
		return ErrNightPreparationBlocked
	}
	if err := managedCtx.Err(); err != nil {
		return err
	}
	return call(managedCtx)
}

// Run watches schedule boundaries and policy changes that arrive through the
// injected notifier. Periodic reevaluation also handles lease TTL expiry.
func (s *NightPreparationScheduler) Run(ctx context.Context) {
	if s == nil || ctx == nil {
		return
	}
	defer s.Close()
	for {
		s.Tick()
		timer := time.NewTimer(s.nextWait(s.now()))
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-s.done:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return
		case <-timer.C:
		}
	}
}

func (s *NightPreparationScheduler) nextWait(now time.Time) time.Duration {
	localNow := now.In(s.location)
	day := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, s.location)
	start := time.Date(day.Year(), day.Month(), day.Day(), s.startMinute/60, s.startMinute%60, 0, 0, s.location)
	end := time.Date(day.Year(), day.Month(), day.Day(), s.endMinute/60, s.endMinute%60, 0, 0, s.location)
	if !start.After(localNow) {
		start = start.AddDate(0, 0, 1)
	}
	if !end.After(localNow) {
		end = end.AddDate(0, 0, 1)
	}
	boundary := start
	if end.Before(boundary) {
		boundary = end
	}
	wait := boundary.Sub(localNow)
	if wait <= 0 || wait > nightPreparationPolicyCheckInterval {
		return nightPreparationPolicyCheckInterval
	}
	return wait
}

// Close blocks new work and cancels every managed CORE context.
func (s *NightPreparationScheduler) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	if !s.closed {
		s.closed = true
		close(s.done)
		s.pendingReasons = nil
		s.pendingSet = make(map[string]struct{})
		s.storyTarget = 0
		s.cancelManagedLocked()
	}
	s.mu.Unlock()
}

func (s *NightPreparationScheduler) cancelManagedLocked() {
	if s.dispatchCancel != nil {
		s.dispatchCancel()
	}
	for _, cancel := range s.managedCalls {
		cancel()
	}
}

func (o *IdleChatOrchestrator) SetNightPreparationScheduler(scheduler *NightPreparationScheduler) {
	if o == nil {
		return
	}
	o.mu.Lock()
	previous := o.nightPreparationScheduler
	o.nightPreparationScheduler = scheduler
	o.mu.Unlock()
	if previous != nil && previous != scheduler {
		previous.Close()
	}
}

func (o *IdleChatOrchestrator) currentNightPreparationScheduler() *NightPreparationScheduler {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.nightPreparationScheduler
}

func (o *IdleChatOrchestrator) RequestNightPreparation(reason string) bool {
	scheduler := o.currentNightPreparationScheduler()
	if scheduler == nil {
		return false
	}
	return scheduler.Request(reason)
}

// RequestNightStoryPreparation registers the viewer's absolute ready-count
// target with the scheduler, which owns it until the Story service reaches it.
func (o *IdleChatOrchestrator) RequestNightStoryPreparation(target int) (string, bool) {
	scheduler := o.currentNightPreparationScheduler()
	if scheduler == nil {
		return "", false
	}
	return scheduler.RequestStoryPreparationTarget(target)
}

func (o *IdleChatOrchestrator) NotifyNightPreparationPolicyChanged() {
	if scheduler := o.currentNightPreparationScheduler(); scheduler != nil {
		scheduler.NotifyPolicyChanged()
	}
}

func (o *IdleChatOrchestrator) RunNightPreparationLLM(ctx context.Context, call func(context.Context) error) error {
	scheduler := o.currentNightPreparationScheduler()
	if scheduler == nil {
		return ErrNightPreparationBlocked
	}
	return scheduler.RunLLM(ctx, call)
}

func withNightPreparationScheduler(ctx context.Context, scheduler *NightPreparationScheduler) context.Context {
	if ctx == nil || scheduler == nil {
		return ctx
	}
	return context.WithValue(ctx, nightPreparationSchedulerContextKey{}, scheduler)
}

func withNightPreparationStoryTarget(ctx context.Context, target int) context.Context {
	if ctx == nil || target < 1 {
		return ctx
	}
	return context.WithValue(ctx, nightPreparationStoryTargetContextKey{}, target)
}

func nightPreparationStoryTargetFromContext(ctx context.Context) int {
	if ctx == nil {
		return 0
	}
	target, _ := ctx.Value(nightPreparationStoryTargetContextKey{}).(int)
	return target
}

func runNightPreparationLLMFromContext(ctx context.Context, call func(context.Context) error) error {
	if ctx == nil || call == nil {
		return ErrNightPreparationBlocked
	}
	scheduler, ok := ctx.Value(nightPreparationSchedulerContextKey{}).(*NightPreparationScheduler)
	if !ok || scheduler == nil {
		return ErrNightPreparationBlocked
	}
	return scheduler.RunLLM(ctx, call)
}

func (o *IdleChatOrchestrator) runNightPreparationScheduler() {
	scheduler := o.currentNightPreparationScheduler()
	if scheduler == nil || o.ctx == nil {
		return
	}
	o.wg.Add(1)
	go func() {
		defer o.wg.Done()
		scheduler.Run(o.ctx)
	}()
}
