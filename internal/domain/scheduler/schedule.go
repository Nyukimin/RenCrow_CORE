package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func ValidateSchedule(schedule Schedule) error {
	if schedule.ScheduleID == "" {
		return errors.New("schedule_id is required")
	}
	if err := schedule.ScheduleID.Validate(); err != nil {
		return fmt.Errorf("schedule_id: %w", err)
	}
	if strings.TrimSpace(schedule.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(schedule.Schedule) == "" {
		return errors.New("schedule is required")
	}
	if schedule.Enabled {
		if _, err := NextRunAfter(schedule.Schedule, time.Now().UTC().Add(-time.Second)); err != nil {
			return err
		}
	} else if err := validateScheduleSyntax(schedule.Schedule); err != nil {
		return err
	}
	if schedule.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	if schedule.UpdatedAt.IsZero() {
		return errors.New("updated_at is required")
	}
	return nil
}

func validateScheduleSyntax(schedule string) error {
	schedule = strings.TrimSpace(schedule)
	if strings.HasPrefix(schedule, "at ") {
		_, err := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(schedule, "at ")))
		if err != nil {
			return fmt.Errorf("invalid at schedule: %w", err)
		}
		return nil
	}
	if strings.HasPrefix(schedule, "every ") {
		interval, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(schedule, "every ")))
		if err != nil || interval <= 0 {
			return errors.New("invalid every schedule")
		}
		return nil
	}
	if strings.HasPrefix(schedule, "cron ") {
		_, err := nextCronAfter(strings.TrimSpace(strings.TrimPrefix(schedule, "cron ")), time.Now().UTC())
		return err
	}
	return errors.New("schedule must start with at, every, or cron")
}

func ValidateRunLog(log RunLog) error {
	if log.RunID == "" {
		return errors.New("run_id is required")
	}
	if err := log.RunID.Validate(); err != nil {
		return fmt.Errorf("run_id: %w", err)
	}
	if log.ScheduleID == "" {
		return errors.New("schedule_id is required")
	}
	if err := log.ScheduleID.Validate(); err != nil {
		return fmt.Errorf("schedule_id: %w", err)
	}
	if log.TaskID.IsZero() {
		return errors.New("task_id is required")
	}
	if err := log.TaskID.Validate(); err != nil {
		return fmt.Errorf("task_id: %w", err)
	}
	if string(log.ScheduleID) == log.TaskID.String() || string(log.ScheduleID) == string(log.RunID) || log.TaskID.String() == string(log.RunID) {
		return errors.New("schedule_id, task_id, and run_id must be distinct")
	}
	if strings.TrimSpace(log.Trigger) == "" {
		return errors.New("trigger is required")
	}
	if strings.TrimSpace(log.Status) == "" {
		return errors.New("status is required")
	}
	if log.StartedAt.IsZero() {
		return errors.New("started_at is required")
	}
	return nil
}

func NextRunAfter(schedule string, after time.Time) (time.Time, error) {
	schedule = strings.TrimSpace(schedule)
	if schedule == "" {
		return time.Time{}, errors.New("schedule is required")
	}
	if strings.HasPrefix(schedule, "at ") {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(strings.TrimPrefix(schedule, "at ")))
		if err != nil {
			return time.Time{}, fmt.Errorf("invalid at schedule: %w", err)
		}
		if !at.After(after) {
			return time.Time{}, errors.New("at schedule is already elapsed")
		}
		return at.UTC(), nil
	}
	if strings.HasPrefix(schedule, "every ") {
		interval, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(schedule, "every ")))
		if err != nil || interval <= 0 {
			return time.Time{}, errors.New("invalid every schedule")
		}
		return after.UTC().Add(interval), nil
	}
	if strings.HasPrefix(schedule, "cron ") {
		return nextCronAfter(strings.TrimSpace(strings.TrimPrefix(schedule, "cron ")), after)
	}
	return time.Time{}, errors.New("schedule must start with at, every, or cron")
}

func nextCronAfter(expr string, after time.Time) (time.Time, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		return time.Time{}, errors.New("cron schedule must have 5 fields")
	}
	minute, err := parseCronField(fields[0], 0, 59)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron minute: %w", err)
	}
	hour, err := parseCronField(fields[1], 0, 23)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron hour: %w", err)
	}
	day, err := parseCronField(fields[2], 1, 31)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron day: %w", err)
	}
	month, err := parseCronField(fields[3], 1, 12)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron month: %w", err)
	}
	weekday, err := parseCronField(fields[4], 0, 6)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid cron weekday: %w", err)
	}
	candidate := after.UTC().Truncate(time.Minute).Add(time.Minute)
	deadline := candidate.AddDate(1, 0, 0)
	for candidate.Before(deadline) {
		if cronFieldMatches(month, int(candidate.Month())) &&
			cronFieldMatches(day, candidate.Day()) &&
			cronFieldMatches(weekday, int(candidate.Weekday())) &&
			cronFieldMatches(hour, candidate.Hour()) &&
			cronFieldMatches(minute, candidate.Minute()) {
			return candidate, nil
		}
		candidate = candidate.Add(time.Minute)
	}
	return time.Time{}, errors.New("cron schedule has no run within one year")
}

func parseCronField(raw string, min int, max int) (map[int]bool, error) {
	raw = strings.TrimSpace(raw)
	if raw == "*" {
		return nil, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return nil, err
	}
	if value < min || value > max {
		return nil, fmt.Errorf("value %d out of range %d-%d", value, min, max)
	}
	return map[int]bool{value: true}, nil
}

func cronFieldMatches(allowed map[int]bool, value int) bool {
	return allowed == nil || allowed[value]
}
