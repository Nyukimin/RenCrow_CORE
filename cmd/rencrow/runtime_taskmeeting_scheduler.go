package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// taskMeetingScheduledTarget is the one runtime-owned scheduler target for
// daily report meetings. Its cadence is projected from the owner config.
const taskMeetingScheduledTarget = "taskmeeting.daily_report"

const taskMeetingScheduleReadLimit = 1000

var errTaskMeetingRequiresScheduledOccurrence = errors.New("task meeting requires a scheduler due occurrence")
var errTaskMeetingCheckpointPending = errors.New("task meeting checkpoint remains pending")

const taskMeetingSchedulerRetryAfter = 30 * time.Second
const taskMeetingSchedulerSafeSummary = "task meeting checkpoint processed"

type schedulerTargetRouter struct {
	pronunciation schedulerapp.Executor
	taskMeeting   taskMeetingOccurrenceExecutor
}

type taskMeetingOccurrenceExecutor interface {
	ExecuteScheduledOccurrence(context.Context, domainscheduler.Schedule, domainscheduler.ScheduledOccurrence) (string, error)
}

type taskMeetingExactScheduleReader interface {
	FindScheduleByID(context.Context, string) (domainscheduler.Schedule, bool, error)
}

type taskMeetingFilteredScheduleReader interface {
	ListSchedulesFiltered(context.Context, int, func(domainscheduler.Schedule) bool) ([]domainscheduler.Schedule, []string, error)
}

func (r schedulerTargetRouter) ExecuteSchedule(ctx context.Context, schedule domainscheduler.Schedule) (string, error) {
	switch schedule.Target {
	case pronunciationapp.ScheduledTarget:
		if r.pronunciation == nil {
			return "", schedulerapp.ErrExecutorUnavailable
		}
		return r.pronunciation.ExecuteSchedule(ctx, schedule)
	case taskMeetingScheduledTarget:
		return "", errTaskMeetingRequiresScheduledOccurrence
	default:
		return "", schedulerapp.ErrExecutorUnavailable
	}
}

func (r schedulerTargetRouter) ExecuteScheduledOccurrence(
	ctx context.Context,
	schedule domainscheduler.Schedule,
	occurrence domainscheduler.ScheduledOccurrence,
) (string, error) {
	switch schedule.Target {
	case pronunciationapp.ScheduledTarget:
		if r.pronunciation == nil {
			return "", schedulerapp.ErrExecutorUnavailable
		}
		// PronunciationCheck remains a legacy target. It does not consume the
		// meeting occurrence contract and keeps its existing execution path.
		return r.pronunciation.ExecuteSchedule(ctx, schedule)
	case taskMeetingScheduledTarget:
		if r.taskMeeting == nil {
			return deferTaskMeetingCheckpoint("", schedulerapp.ErrExecutorUnavailable)
		}
		if err := validateTaskMeetingScheduledOccurrence(schedule, occurrence); err != nil {
			if errors.Is(err, errTaskMeetingRequiresScheduledOccurrence) {
				return "", err
			}
			return deferTaskMeetingCheckpoint("", err)
		}
		_, err := r.taskMeeting.ExecuteScheduledOccurrence(ctx, schedule, occurrence)
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}
		if summary, deferErr := deferTaskMeetingCheckpoint("", err); deferErr != nil {
			return summary, deferErr
		}
		return taskMeetingSchedulerSafeSummary, nil
	default:
		return "", schedulerapp.ErrExecutorUnavailable
	}
}

func deferTaskMeetingCheckpoint(summary string, cause error) (string, error) {
	if cause == nil {
		return summary, nil
	}
	// Meeting failures stay on the same original due slot until the owner
	// reaches a durable terminal state. The generic scheduler log receives a
	// safe summary instead of private provider or transcript error text.
	return summary, schedulerapp.NewDeferredError(taskMeetingSchedulerRetryAfter, errTaskMeetingCheckpointPending)
}

func validateTaskMeetingScheduledOccurrence(schedule domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) error {
	if schedule.ScheduleID == "" || schedule.Target != taskMeetingScheduledTarget || schedule.Timing == nil || occurrence.ScheduledAt.IsZero() || occurrence.OccurrenceKey == "" {
		return errTaskMeetingRequiresScheduledOccurrence
	}
	if err := domainmeeting.ValidateDailyMeetingTiming(occurrence.Timing); err != nil {
		return fmt.Errorf("task meeting requires the configured daily timing: %w", err)
	}
	if !sameTaskMeetingTiming(*schedule.Timing, occurrence.Timing) {
		return errors.New("task meeting scheduler timing changed before dispatch")
	}
	dueAt := schedule.NextRunAt
	if schedule.PendingOccurrenceAt != nil {
		dueAt = *schedule.PendingOccurrenceAt
	}
	if !dueAt.Equal(occurrence.ScheduledAt) {
		return errors.New("task meeting due instant does not match the persisted scheduler occurrence")
	}
	key, err := domainscheduler.OccurrenceKey(schedule, occurrence.ScheduledAt, domainmeeting.TimezoneJST)
	if err != nil {
		return err
	}
	if key != occurrence.OccurrenceKey {
		return errors.New("task meeting occurrence key does not match its persisted due instant")
	}
	return nil
}

func taskMeetingEffectivelyEnabled(cfg *config.Config) (bool, string) {
	if enabled, reason := taskMeetingFeatureFlagsEnabled(cfg); !enabled {
		return false, reason
	}
	if reason := taskMeetingWorldReadAvailabilityReason(cfg); reason != "" {
		return false, reason
	}
	return true, ""
}

func taskMeetingFeatureFlagsEnabled(cfg *config.Config) (bool, string) {
	if cfg == nil {
		return false, "configuration_unavailable"
	}
	if !cfg.Heartbeat.Routine.DailyReportMeetings.Enabled {
		return false, "feature_disabled"
	}
	if !cfg.Heartbeat.Enabled {
		return false, "parent_heartbeat_disabled"
	}
	return true, ""
}

func taskMeetingRuntimeBindingsReady(cfg *config.Config, agents agentRuntime, coders ...*coderAdapter) bool {
	if cfg == nil {
		return false
	}
	registered := registeredRuntimeTaskMeetingActors(cfg, agents, coders...)
	actors := cfg.Heartbeat.Routine.DailyReportMeetings.Actors
	if len(actors) != 8 {
		return false
	}
	seen := make(map[string]struct{}, len(actors))
	for _, actor := range actors {
		if !registered[actor.CoreActorID] {
			return false
		}
		if _, exists := seen[actor.CoreActorID]; exists {
			return false
		}
		seen[actor.CoreActorID] = struct{}{}
	}
	return len(seen) == 8
}

func ensureTaskMeetingSchedule(
	ctx context.Context,
	store viewer.SchedulerStore,
	service *schedulerapp.Service,
	ownerScheduleID modulecore.ScheduleID,
	timing modulecore.TaskTiming,
) (modulecore.ScheduleID, error) {
	if store == nil || service == nil {
		return "", errors.New("task meeting scheduler store is unavailable")
	}
	if ownerScheduleID == "" || ownerScheduleID.Validate() != nil {
		return "", errors.New("task meeting owner schedule registration is unavailable")
	}
	if err := domainmeeting.ValidateDailyMeetingTiming(timing); err != nil {
		return "", fmt.Errorf("task meeting timing: %w", err)
	}
	var owned *domainscheduler.Schedule
	if exact, ok := store.(taskMeetingExactScheduleReader); ok {
		schedule, found, err := exact.FindScheduleByID(ctx, string(ownerScheduleID))
		if err != nil {
			return "", err
		}
		if found {
			if schedule.ScheduleID != ownerScheduleID || schedule.Target != taskMeetingScheduledTarget {
				return "", errors.New("owner-issued task meeting schedule ID belongs to another target")
			}
			owned = &schedule
		}
		rogueIDs, err := unregisteredTaskMeetingScheduleIDs(ctx, store, ownerScheduleID)
		if err != nil {
			return "", err
		}
		if len(rogueIDs) > 0 {
			return "", errors.New("an unregistered task meeting schedule target is present")
		}
	} else {
		schedules, err := store.ListSchedules(ctx, taskMeetingScheduleReadLimit)
		if err != nil {
			return "", err
		}
		if len(schedules) >= taskMeetingScheduleReadLimit {
			return "", errors.New("task meeting scheduler target scan reached its bound")
		}
		for _, schedule := range schedules {
			if schedule.ScheduleID == ownerScheduleID {
				if schedule.Target != taskMeetingScheduledTarget {
					return "", errors.New("owner-issued task meeting schedule ID belongs to another target")
				}
				copySchedule := schedule
				owned = &copySchedule
			}
			if isTaskMeetingSchedule(schedule) && schedule.ScheduleID != ownerScheduleID {
				return "", errors.New("an unregistered task meeting schedule target is present")
			}
		}
	}
	if owned == nil {
		created, createErr := service.CreateSchedule(ctx, domainscheduler.Schedule{
			ScheduleID:  ownerScheduleID,
			Name:        "Daily report meeting",
			Target:      taskMeetingScheduledTarget,
			Description: "Owner-configured daily report meeting",
			Timing:      &timing,
		})
		if createErr != nil {
			return "", createErr
		}
		return created.ScheduleID, nil
	}

	schedule := *owned
	if err := schedule.ScheduleID.Validate(); err != nil {
		return "", fmt.Errorf("task meeting schedule ID: %w", err)
	}
	if schedule.PendingOccurrenceAt != nil && (schedule.NextRunAt.IsZero() || schedule.PendingOccurrenceAt.After(schedule.NextRunAt)) {
		return "", errors.New("task meeting pending occurrence is inconsistent")
	}
	changedTiming := schedule.Timing == nil || !sameTaskMeetingTiming(*schedule.Timing, timing)
	changed := changedTiming || schedule.Schedule != "" || !schedule.Enabled || schedule.Name == "" ||
		schedule.Description != "Owner-configured daily report meeting" || schedule.NextRunAt.IsZero()
	if !changed {
		return schedule.ScheduleID, nil
	}
	if schedule.Name == "" {
		schedule.Name = "Daily report meeting"
	}
	schedule.Schedule = ""
	schedule.Description = "Owner-configured daily report meeting"
	schedule.Timing = cloneRuntimeTaskTiming(&timing)
	now := time.Now().UTC()
	if !schedule.Enabled {
		schedule.Enabled = true
		schedule.DisabledAt = time.Time{}
		schedule.DisabledBy = ""
	}
	if schedule.PendingOccurrenceAt != nil {
		if schedule.NextRunAt.IsZero() || schedule.PendingOccurrenceAt.After(schedule.NextRunAt) {
			return "", errors.New("task meeting pending occurrence is inconsistent")
		}
	} else if changedTiming || schedule.NextRunAt.IsZero() {
		next, nextErr := domainscheduler.NextRunAfterSchedule(schedule, now)
		if nextErr != nil {
			return "", nextErr
		}
		schedule.NextRunAt = next
	}
	schedule.UpdatedAt = now
	if err := store.SaveSchedule(ctx, schedule); err != nil {
		return "", err
	}
	return schedule.ScheduleID, nil
}

// disableTaskMeetingSchedules touches only the reserved owner target. A
// default-disabled feature performs no write when no old owner schedule exists.
func disableTaskMeetingSchedule(ctx context.Context, store viewer.SchedulerStore, ownerScheduleID modulecore.ScheduleID) error {
	if store == nil {
		return nil
	}
	if ownerScheduleID == "" {
		return nil
	}
	if err := ownerScheduleID.Validate(); err != nil {
		return err
	}
	if exact, ok := store.(taskMeetingExactScheduleReader); ok {
		schedule, found, err := exact.FindScheduleByID(ctx, string(ownerScheduleID))
		if err != nil || !found {
			return err
		}
		if schedule.ScheduleID != ownerScheduleID || schedule.Target != taskMeetingScheduledTarget {
			return errors.New("owner-issued task meeting schedule ID does not identify its registered target")
		}
		if !schedule.Enabled {
			return nil
		}
		schedule.Enabled = false
		schedule.DisabledAt = time.Now().UTC()
		schedule.DisabledBy = "owner_config"
		schedule.UpdatedAt = schedule.DisabledAt
		return store.SaveSchedule(ctx, schedule)
	}
	schedules, err := store.ListSchedules(ctx, taskMeetingScheduleReadLimit)
	if err != nil {
		return err
	}
	if len(schedules) >= taskMeetingScheduleReadLimit {
		return errors.New("task meeting scheduler target scan reached its bound")
	}
	for _, schedule := range schedules {
		if schedule.ScheduleID != ownerScheduleID {
			continue
		}
		if schedule.Target != taskMeetingScheduledTarget {
			return errors.New("owner-issued task meeting schedule ID belongs to another target")
		}
		if !schedule.Enabled {
			return nil
		}
		schedule.Enabled = false
		schedule.DisabledAt = time.Now().UTC()
		schedule.DisabledBy = "owner_config"
		schedule.UpdatedAt = schedule.DisabledAt
		if err := store.SaveSchedule(ctx, schedule); err != nil {
			return err
		}
		return nil
	}
	return nil
}

func cloneRuntimeTaskTiming(timing *modulecore.TaskTiming) *modulecore.TaskTiming {
	if timing == nil {
		return nil
	}
	copyTiming := *timing
	if timing.CompletionCondition != nil {
		condition := *timing.CompletionCondition
		copyTiming.CompletionCondition = &condition
	}
	copyTiming.AllowedWindows = append([]modulecore.TaskTimeWindow(nil), timing.AllowedWindows...)
	if timing.Recurrence != nil {
		recurrence := *timing.Recurrence
		recurrence.Times = append([]string(nil), timing.Recurrence.Times...)
		recurrence.Weekdays = append([]time.Weekday(nil), timing.Recurrence.Weekdays...)
		if timing.Recurrence.IntervalSeconds != nil {
			seconds := *timing.Recurrence.IntervalSeconds
			recurrence.IntervalSeconds = &seconds
		}
		copyTiming.Recurrence = &recurrence
	}
	if timing.ScheduledStartAt != nil {
		value := *timing.ScheduledStartAt
		copyTiming.ScheduledStartAt = &value
	}
	if timing.NotBefore != nil {
		value := *timing.NotBefore
		copyTiming.NotBefore = &value
	}
	if timing.DeadlineAt != nil {
		value := *timing.DeadlineAt
		copyTiming.DeadlineAt = &value
	}
	if timing.EstimatedDurationSeconds != nil {
		value := *timing.EstimatedDurationSeconds
		copyTiming.EstimatedDurationSeconds = &value
	}
	return &copyTiming
}

func startSharedSchedulerLoop(ctx context.Context, service *schedulerapp.Service, scheduleIDs []modulecore.ScheduleID) (context.CancelFunc, <-chan struct{}) {
	runtimeCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	if service == nil || len(scheduleIDs) == 0 {
		cancel()
		close(done)
		return func() {}, done
	}
	ids := append([]modulecore.ScheduleID(nil), scheduleIDs...)
	go func() {
		defer close(done)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		inflight := make(map[modulecore.ScheduleID]bool, len(ids))
		completed := make(chan modulecore.ScheduleID, len(ids))
		launchOwnedSchedules := func() {
			if runtimeCtx.Err() != nil {
				return
			}
			for _, scheduleID := range ids {
				if inflight[scheduleID] {
					continue
				}
				inflight[scheduleID] = true
				go runDueScheduleWorker(runtimeCtx, service, scheduleID, completed)
			}
		}
		launchOwnedSchedules()
		for {
			select {
			case <-runtimeCtx.Done():
				for len(inflight) > 0 {
					scheduleID := <-completed
					delete(inflight, scheduleID)
				}
				return
			case scheduleID := <-completed:
				delete(inflight, scheduleID)
			case <-ticker.C:
				launchOwnedSchedules()
			}
		}
	}()
	return cancel, done
}

func runDueScheduleWorker(ctx context.Context, service *schedulerapp.Service, scheduleID modulecore.ScheduleID, completed chan<- modulecore.ScheduleID) {
	defer func() { completed <- scheduleID }()
	if service == nil || ctx.Err() != nil {
		return
	}
	entry, ran, err := service.RunDueSchedule(ctx, string(scheduleID))
	if err != nil {
		log.Printf("[Scheduler] a registered owner schedule could not be advanced")
		return
	}
	if ran {
		log.Printf("[Scheduler] registered owner schedule processed (status=%s)", entry.Status)
	}
}

func protectedTaskMeetingScheduleIDs(ctx context.Context, store viewer.SchedulerStore) ([]string, error) {
	if store == nil {
		return nil, nil
	}
	if filtered, ok := store.(taskMeetingFilteredScheduleReader); ok {
		_, excludedIDs, err := filtered.ListSchedulesFiltered(ctx, 1, func(schedule domainscheduler.Schedule) bool {
			return !isTaskMeetingSchedule(schedule)
		})
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(excludedIDs))
		for _, scheduleID := range excludedIDs {
			if strings.TrimSpace(scheduleID) != "" {
				ids = append(ids, strings.TrimSpace(scheduleID))
			}
		}
		return ids, nil
	}
	schedules, err := store.ListSchedules(ctx, taskMeetingScheduleReadLimit)
	if err != nil {
		return nil, err
	}
	if len(schedules) >= taskMeetingScheduleReadLimit {
		return nil, errors.New("task meeting scheduler target scan reached its bound")
	}
	ids := make([]string, 0, 1)
	for _, schedule := range schedules {
		if isTaskMeetingSchedule(schedule) && schedule.ScheduleID != "" {
			ids = append(ids, string(schedule.ScheduleID))
		}
	}
	return ids, nil
}

func isTaskMeetingSchedule(schedule domainscheduler.Schedule) bool {
	return strings.TrimSpace(schedule.Target) == taskMeetingScheduledTarget
}

func sameTaskMeetingTiming(left, right modulecore.TaskTiming) bool {
	if left.Timezone != right.Timezone || !sameOptionalTaskMeetingTime(left.ScheduledStartAt, right.ScheduledStartAt) ||
		!sameOptionalTaskMeetingTime(left.NotBefore, right.NotBefore) || !sameOptionalTaskMeetingTime(left.DeadlineAt, right.DeadlineAt) ||
		!sameOptionalTaskMeetingInt64(left.EstimatedDurationSeconds, right.EstimatedDurationSeconds) || len(left.AllowedWindows) != len(right.AllowedWindows) {
		return false
	}
	for index := range left.AllowedWindows {
		if left.AllowedWindows[index] != right.AllowedWindows[index] {
			return false
		}
	}
	if left.Recurrence == nil || right.Recurrence == nil {
		return left.Recurrence == nil && right.Recurrence == nil
	}
	if left.Recurrence.Frequency != right.Recurrence.Frequency || len(left.Recurrence.Times) != len(right.Recurrence.Times) ||
		len(left.Recurrence.Weekdays) != len(right.Recurrence.Weekdays) || !sameOptionalTaskMeetingInt64(left.Recurrence.IntervalSeconds, right.Recurrence.IntervalSeconds) {
		return false
	}
	for index := range left.Recurrence.Times {
		if left.Recurrence.Times[index] != right.Recurrence.Times[index] {
			return false
		}
	}
	for index := range left.Recurrence.Weekdays {
		if left.Recurrence.Weekdays[index] != right.Recurrence.Weekdays[index] {
			return false
		}
	}
	return true
}

func sameOptionalTaskMeetingTime(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}

func sameOptionalTaskMeetingInt64(left, right *int64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func unregisteredTaskMeetingScheduleIDs(ctx context.Context, store viewer.SchedulerStore, ownerScheduleID modulecore.ScheduleID) ([]string, error) {
	if filtered, ok := store.(taskMeetingFilteredScheduleReader); ok {
		_, excludedIDs, err := filtered.ListSchedulesFiltered(ctx, 1, func(schedule domainscheduler.Schedule) bool {
			return !isTaskMeetingSchedule(schedule)
		})
		if err != nil {
			return nil, err
		}
		rogue := make([]string, 0, len(excludedIDs))
		for _, scheduleID := range excludedIDs {
			scheduleID = strings.TrimSpace(scheduleID)
			if scheduleID != "" && scheduleID != string(ownerScheduleID) {
				rogue = append(rogue, scheduleID)
			}
		}
		return rogue, nil
	}
	schedules, err := store.ListSchedules(ctx, taskMeetingScheduleReadLimit)
	if err != nil {
		return nil, err
	}
	if len(schedules) >= taskMeetingScheduleReadLimit {
		return nil, errors.New("task meeting scheduler target scan reached its bound")
	}
	rogue := make([]string, 0)
	for _, schedule := range schedules {
		if isTaskMeetingSchedule(schedule) && schedule.ScheduleID != ownerScheduleID {
			rogue = append(rogue, string(schedule.ScheduleID))
		}
	}
	return rogue, nil
}

func waitForSchedulerWorkers(done <-chan struct{}, timeout time.Duration) bool {
	if done == nil {
		return true
	}
	if timeout <= 0 {
		select {
		case <-done:
			return true
		default:
			return false
		}
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
