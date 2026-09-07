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
	schedule.UpdatedAt = now
	schedule.Enabled = true
	next, err := domainscheduler.NextRunAfter(schedule.Schedule, now)
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
		if !schedule.Enabled || schedule.NextRunAt.IsZero() || schedule.NextRunAt.After(now) {
			continue
		}
		out = append(out, domainscheduler.DueSchedule{Schedule: schedule, Scheduled: schedule.NextRunAt})
	}
	return out, nil
}

func (s *Service) RunDueSchedule(ctx context.Context, scheduleID string) (domainscheduler.RunLog, bool, error) {
	due, err := s.DueSchedules(ctx, 1000)
	if err != nil {
		return domainscheduler.RunLog{}, false, err
	}
	wantID := strings.TrimSpace(scheduleID)
	for _, item := range due {
		if string(item.Schedule.ScheduleID) == wantID {
			log, err := s.RunSchedule(ctx, wantID, "due")
			return log, true, err
		}
	}
	return domainscheduler.RunLog{}, false, nil
}

func (s *Service) RunSchedule(ctx context.Context, scheduleID string, trigger string) (domainscheduler.RunLog, error) {
	if s == nil || s.store == nil {
		return domainscheduler.RunLog{}, fmt.Errorf("scheduler store unavailable")
	}
	schedule, err := s.findSchedule(ctx, scheduleID)
	if err != nil {
		return domainscheduler.RunLog{}, err
	}
	now := s.now().UTC()
	log := domainscheduler.RunLog{
		RunID:      modulecore.NewRunID(),
		ScheduleID: schedule.ScheduleID,
		TaskID:     modulecore.NewTaskID(),
		Trigger:    firstNonEmpty(trigger, "manual"),
		Status:     "completed",
		StartedAt:  now,
	}
	var deferredRetryAfter time.Duration
	if s.executor != nil {
		summary, execErr := s.executor.ExecuteSchedule(ctx, schedule)
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
		log.Summary = "scheduler run recorded without executor"
	}
	log.CompletedAt = s.now().UTC()
	schedule.LastRunAt = log.StartedAt
	if log.Status == "deferred" && deferredRetryAfter > 0 {
		schedule.NextRunAt = log.CompletedAt.Add(deferredRetryAfter)
	} else if next, err := domainscheduler.NextRunAfter(schedule.Schedule, log.StartedAt); err == nil {
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
