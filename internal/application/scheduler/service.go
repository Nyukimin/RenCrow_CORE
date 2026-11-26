package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type DeferredError struct {
	RetryAfter time.Duration
	cause      error
}

var ErrExecutorUnavailable = errors.New("scheduler executor unavailable")

func NewDeferredError(retryAfter time.Duration, cause error) *DeferredError {
	return &DeferredError{RetryAfter: retryAfter, cause: cause}
}

func (e *DeferredError) Error() string {
	if e == nil || e.cause == nil {
		return "scheduled schedule deferred"
	}
	return e.cause.Error()
}

func (e *DeferredError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

type Store interface {
	ListSchedules(ctx context.Context, limit int) ([]domainscheduler.Schedule, error)
	SaveSchedule(ctx context.Context, schedule domainscheduler.Schedule) error
	SaveRunLog(ctx context.Context, log domainscheduler.RunLog) error
	ListRunLogs(ctx context.Context, limit int) ([]domainscheduler.RunLog, error)
}

type Executor interface {
	ExecuteSchedule(ctx context.Context, schedule domainscheduler.Schedule) (string, error)
}

// OccurrenceExecutor receives the concrete due slot and its timing policy.
// Legacy executors remain supported through Executor.
type OccurrenceExecutor interface {
	ExecuteScheduledOccurrence(ctx context.Context, schedule domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) (string, error)
}

type Service struct {
	store    Store
	executor Executor
	now      func() time.Time
}

func NewService(store Store, executor Executor) *Service {
	return &Service{store: store, executor: executor, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *Service) CreateSchedule(ctx context.Context, schedule domainscheduler.Schedule) (domainscheduler.Schedule, error) {
	if s == nil || s.store == nil {
		return domainscheduler.Schedule{}, fmt.Errorf("scheduler store unavailable")
	}
	now := s.now().UTC()
	if schedule.ScheduleID == "" {
		schedule.ScheduleID = modulecore.NewScheduleID()
	}
	if schedule.CreatedAt.IsZero() {
		schedule.CreatedAt = now
	}
	if schedule.Timing == nil {
		schedule.Timing = &modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}}
	}
	schedule.UpdatedAt = now
	schedule.Enabled = true
	next, err := domainscheduler.NextRunAfterSchedule(schedule, now)
	if err != nil {
		return domainscheduler.Schedule{}, err
	}
	schedule.NextRunAt = next
	if err := s.store.SaveSchedule(ctx, schedule); err != nil {
		return domainscheduler.Schedule{}, err
	}
	return schedule, nil
}

func (s *Service) DueSchedules(ctx context.Context, limit int) ([]domainscheduler.DueSchedule, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("scheduler store unavailable")
	}
	schedules, err := s.store.ListSchedules(ctx, limit)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := make([]domainscheduler.DueSchedule, 0)
	for _, schedule := range schedules {
		if !schedule.Enabled || schedule.NextRunAt.IsZero() || schedule.NextRunAt.After(now) || !timingAllowsStart(schedule, now) {
			continue
		}
		scheduled := schedule.NextRunAt
		if schedule.PendingOccurrenceAt != nil {
			scheduled = *schedule.PendingOccurrenceAt
		}
		zone := "UTC"
		if schedule.Timing != nil && schedule.Timing.Timezone != "" {
			zone = schedule.Timing.Timezone
		}
		occurrenceKey, identityErr := domainscheduler.OccurrenceKey(schedule, scheduled, zone)
		if identityErr != nil {
			return nil, identityErr
		}
		out = append(out, domainscheduler.DueSchedule{Schedule: schedule, Scheduled: scheduled, OccurrenceKey: occurrenceKey})
	}
	return out, nil
}

func (s *Service) RunDueSchedule(ctx context.Context, scheduleID string) (domainscheduler.RunLog, bool, error) {
	wantID := strings.TrimSpace(scheduleID)
	schedule, err := s.findSchedule(ctx, wantID)
	if err != nil {
		return domainscheduler.RunLog{}, false, err
	}
	now := s.now().UTC()
	if !schedule.Enabled || schedule.NextRunAt.IsZero() || schedule.NextRunAt.After(now) {
		return domainscheduler.RunLog{}, false, nil
	}
	if !timingAllowsStart(schedule, now) {
		// A due slot that has passed its start gate is skipped. Move to the
		// next eligible slot so a closed window cannot leave a stale due row.
		next, nextErr := domainscheduler.NextRunAfterSchedule(schedule, now)
		if nextErr != nil {
			return domainscheduler.RunLog{}, false, nextErr
		}
		schedule.NextRunAt = next
		schedule.PendingOccurrenceAt = nil
		schedule.UpdatedAt = now
		if err := s.store.SaveSchedule(ctx, schedule); err != nil {
			return domainscheduler.RunLog{}, false, err
		}
		return domainscheduler.RunLog{}, false, nil
	}
	scheduled := schedule.NextRunAt
	if schedule.PendingOccurrenceAt != nil {
		scheduled = *schedule.PendingOccurrenceAt
	}
	zone := "UTC"
	if schedule.Timing != nil && schedule.Timing.Timezone != "" {
		zone = schedule.Timing.Timezone
	}
	occurrenceKey, err := domainscheduler.OccurrenceKey(schedule, scheduled, zone)
	if err != nil {
		return domainscheduler.RunLog{}, false, err
	}
	log, err := s.runOccurrence(ctx, schedule, scheduled, occurrenceKey, "due")
	return log, true, err
}

func (s *Service) RunSchedule(ctx context.Context, scheduleID string, trigger string) (domainscheduler.RunLog, error) {
	if s == nil || s.store == nil {
		return domainscheduler.RunLog{}, fmt.Errorf("scheduler store unavailable")
	}
	schedule, err := s.findSchedule(ctx, scheduleID)
	if err != nil {
		return domainscheduler.RunLog{}, err
	}
	return s.runOccurrence(ctx, schedule, time.Time{}, "", trigger)
}

func (s *Service) runOccurrence(ctx context.Context, schedule domainscheduler.Schedule, scheduled time.Time, occurrenceKey string, trigger string) (domainscheduler.RunLog, error) {
	now := s.now().UTC()
	if !timingAllowsStart(schedule, now) {
		return domainscheduler.RunLog{}, errors.New("schedule timing does not allow start")
	}
	if scheduled.IsZero() {
		scheduled = now
	}
	isDue := strings.EqualFold(strings.TrimSpace(trigger), "due")
	if occurrenceKey == "" && isDue {
		zone := "UTC"
		if schedule.Timing != nil && schedule.Timing.Timezone != "" {
			zone = schedule.Timing.Timezone
		}
		var err error
		occurrenceKey, err = domainscheduler.OccurrenceKey(schedule, scheduled, zone)
		if err != nil {
			return domainscheduler.RunLog{}, err
		}
	}
	log := domainscheduler.RunLog{
		RunID:         modulecore.NewRunID(),
		ScheduleID:    schedule.ScheduleID,
		Trigger:       firstNonEmpty(trigger, "manual"),
		OccurrenceKey: occurrenceKey,
		Status:        "completed",
		StartedAt:     now,
	}
	var deferredRetryAfter time.Duration
	if s.executor != nil {
		var summary string
		var execErr error
		executionSchedule := schedule
		executionSchedule.Timing = cloneTaskTiming(schedule.Timing)
		occurrenceTiming := cloneTaskTiming(schedule.Timing)
		var occurrencePolicy modulecore.TaskTiming
		if occurrenceTiming != nil {
			occurrencePolicy = *occurrenceTiming
		}
		if executor, ok := s.executor.(OccurrenceExecutor); ok && isDue {
			summary, execErr = executor.ExecuteScheduledOccurrence(ctx, executionSchedule, domainscheduler.ScheduledOccurrence{
				OccurrenceKey: occurrenceKey, ScheduledAt: scheduled, Timing: occurrencePolicy,
			})
		} else {
			summary, execErr = s.executor.ExecuteSchedule(ctx, executionSchedule)
		}
		log.Summary = strings.TrimSpace(summary)
		if execErr != nil {
			var deferred *DeferredError
			if errors.As(execErr, &deferred) {
				log.Status = "deferred"
				log.Summary = firstNonEmpty(log.Summary, deferred.Error())
				deferredRetryAfter = deferred.RetryAfter
			} else {
				log.Status = "failed"
				log.Error = execErr.Error()
			}
		}
	} else {
		log.Status = "failed"
		log.Summary = "scheduler run recorded without executor"
		log.Error = ErrExecutorUnavailable.Error()
	}
	log.CompletedAt = s.now().UTC()
	schedule.LastRunAt = log.StartedAt
	if log.Status == "deferred" && deferredRetryAfter > 0 {
		if isDue && schedule.PendingOccurrenceAt == nil {
			pending := scheduled
			schedule.PendingOccurrenceAt = &pending
		}
		schedule.NextRunAt = log.CompletedAt.Add(deferredRetryAfter)
	} else if next, err := domainscheduler.NextRunAfterSchedule(schedule, log.StartedAt); err == nil {
		// Advance from the actual start so missed slots are skipped consistently.
		schedule.PendingOccurrenceAt = nil
		schedule.NextRunAt = next
	} else {
		schedule.Enabled = false
		schedule.DisabledAt = log.CompletedAt
		schedule.DisabledBy = "scheduler"
	}
	schedule.UpdatedAt = log.CompletedAt
	if err := s.store.SaveRunLog(ctx, log); err != nil {
		return domainscheduler.RunLog{}, err
	}
	if err := s.store.SaveSchedule(ctx, schedule); err != nil {
		return domainscheduler.RunLog{}, err
	}
	return log, nil
}

func cloneTaskTiming(timing *modulecore.TaskTiming) *modulecore.TaskTiming {
	if timing == nil {
		return nil
	}
	cloned := *timing
	if timing.CompletionCondition != nil {
		condition := *timing.CompletionCondition
		cloned.CompletionCondition = &condition
	}
	cloned.ScheduledStartAt = cloneTimePtr(timing.ScheduledStartAt)
	cloned.NotBefore = cloneTimePtr(timing.NotBefore)
	cloned.DeadlineAt = cloneTimePtr(timing.DeadlineAt)
	if timing.EstimatedDurationSeconds != nil {
		value := *timing.EstimatedDurationSeconds
		cloned.EstimatedDurationSeconds = &value
	}
	cloned.AllowedWindows = append([]modulecore.TaskTimeWindow(nil), timing.AllowedWindows...)
	if timing.Recurrence != nil {
		recurrence := *timing.Recurrence
		recurrence.Times = append([]string(nil), timing.Recurrence.Times...)
		recurrence.Weekdays = append([]time.Weekday(nil), timing.Recurrence.Weekdays...)
		if timing.Recurrence.IntervalSeconds != nil {
			seconds := *timing.Recurrence.IntervalSeconds
			recurrence.IntervalSeconds = &seconds
		}
		cloned.Recurrence = &recurrence
	}
	return &cloned
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func timingAllowsStart(schedule domainscheduler.Schedule, now time.Time) bool {
	if schedule.Timing == nil {
		return true
	}
	timing := schedule.Timing
	if timing.Validate() != nil || !timing.Ready(now) {
		return false
	}
	return true
}

func (s *Service) DisableSchedule(ctx context.Context, scheduleID string, disabledBy string) (domainscheduler.Schedule, error) {
	if s == nil || s.store == nil {
		return domainscheduler.Schedule{}, fmt.Errorf("scheduler store unavailable")
	}
	schedule, err := s.findSchedule(ctx, scheduleID)
	if err != nil {
		return domainscheduler.Schedule{}, err
	}
	now := s.now().UTC()
	schedule.Enabled = false
	schedule.DisabledAt = now
	schedule.DisabledBy = firstNonEmpty(disabledBy, "system")
	schedule.UpdatedAt = now
	if err := s.store.SaveSchedule(ctx, schedule); err != nil {
		return domainscheduler.Schedule{}, err
	}
	return schedule, nil
}

func (s *Service) findSchedule(ctx context.Context, scheduleID string) (domainscheduler.Schedule, error) {
	scheduleID = strings.TrimSpace(scheduleID)
	if scheduleID == "" {
		return domainscheduler.Schedule{}, fmt.Errorf("schedule_id is required")
	}
	schedules, err := s.store.ListSchedules(ctx, 1000)
	if err != nil {
		return domainscheduler.Schedule{}, err
	}
	for _, schedule := range schedules {
		if string(schedule.ScheduleID) == scheduleID {
			return schedule, nil
		}
	}
	return domainscheduler.Schedule{}, fmt.Errorf("scheduler schedule not found: %s", scheduleID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
