package scheduler

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	if schedule.Timing != nil {
		if err := schedule.Timing.Validate(); err != nil {
			return fmt.Errorf("timing: %w", err)
		}
	}
	typedRecurrence := schedule.Timing != nil && schedule.Timing.Recurrence != nil && schedule.Timing.Recurrence.Frequency != modulecore.TaskRecurrenceNone
	if typedRecurrence {
		if strings.TrimSpace(schedule.Schedule) != "" {
			return errors.New("schedule must be empty when timing recurrence is configured")
		}
		if _, err := NextRunAfterSchedule(schedule, time.Now().UTC().Add(-time.Second)); err != nil {
			return err
		}
	} else {
		if strings.TrimSpace(schedule.Schedule) == "" {
			return errors.New("schedule is required without a timing recurrence")
		}
		if schedule.Enabled {
			if _, err := NextRunAfterSchedule(schedule, time.Now().UTC().Add(-time.Second)); err != nil {
				return err
			}
		} else if err := validateScheduleSyntax(schedule.Schedule); err != nil {
			return err
		}
	}
	if schedule.PendingOccurrenceAt != nil {
		if schedule.PendingOccurrenceAt.IsZero() || schedule.NextRunAt.IsZero() || schedule.PendingOccurrenceAt.After(schedule.NextRunAt) {
			return errors.New("pending_occurrence_at must be nonzero and no later than next_run_at")
		}
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
	if !log.TaskID.IsZero() {
		if err := log.TaskID.Validate(); err != nil {
			return fmt.Errorf("task_id: %w", err)
		}
		if log.TaskID.String() == string(log.ScheduleID) || log.TaskID.String() == string(log.RunID) {
			return errors.New("task_id, schedule_id, and run_id must be distinct when task_id is present")
		}
	}
	if string(log.ScheduleID) == string(log.RunID) {
		return errors.New("schedule_id and run_id must be distinct")
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

// NextRunAfterSchedule resolves the next slot using TaskTiming recurrence when
// configured; records without recurrence retain their existing UTC expression.
func NextRunAfterSchedule(schedule Schedule, after time.Time) (time.Time, error) {
	if schedule.Timing == nil {
		return NextRunAfter(schedule.Schedule, after)
	}
	timing := *schedule.Timing
	if err := timing.Validate(); err != nil {
		return time.Time{}, err
	}
	if timing.Recurrence != nil && timing.Recurrence.Frequency != modulecore.TaskRecurrenceNone && strings.TrimSpace(schedule.Schedule) != "" {
		return time.Time{}, errors.New("schedule must be empty when timing recurrence is configured")
	}
	if timing.Recurrence == nil || timing.Recurrence.Frequency == modulecore.TaskRecurrenceNone {
		return nextLegacyWithTiming(schedule.Schedule, timing, after)
	}
	if timing.Recurrence.Frequency == modulecore.TaskRecurrenceInterval {
		if len(timing.AllowedWindows) > 0 {
			return time.Time{}, errors.New("allowed_windows with interval recurrence is unsupported")
		}
		if *timing.Recurrence.IntervalSeconds > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Time{}, errors.New("interval_seconds is too large")
		}
		interval := time.Duration(*timing.Recurrence.IntervalSeconds) * time.Second
		bound := lowerTimingBound(after, timing)
		candidate := after.Add(interval)
		if !candidate.After(bound) {
			delta := bound.Sub(after)
			steps := delta / interval
			if delta%interval != 0 {
				steps++
			}
			if steps < 1 {
				steps = 1
			}
			if steps > time.Duration(1<<62)/interval {
				return time.Time{}, errors.New("interval recurrence timing bound is out of range")
			}
			candidate = after.Add(steps * interval)
		}
		if !timing.Ready(candidate) {
			return time.Time{}, errors.New("interval recurrence candidate failed readiness gates")
		}
		return candidate.UTC(), nil
	}
	recurrence := *timing.Recurrence
	if len(timing.AllowedWindows) > 0 {
		recurrence.Times = recurrenceTimesWithinWindows(recurrence.Times, timing.AllowedWindows)
		if len(recurrence.Times) == 0 {
			return time.Time{}, errors.New("recurrence does not fit allowed_windows")
		}
	}
	searchAfter := lowerTimingBound(after, timing)
	if searchAfter.After(after) {
		searchAfter = searchAfter.Add(-time.Nanosecond)
	}
	candidate, err := recurrence.NextOccurrence(searchAfter, timing.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	if !timing.Ready(candidate) {
		return time.Time{}, errors.New("timing recurrence candidate failed readiness gates")
	}
	return candidate.UTC(), nil
}

func lowerTimingBound(after time.Time, timing modulecore.TaskTiming) time.Time {
	bound := after
	for _, value := range []*time.Time{timing.ScheduledStartAt, timing.NotBefore} {
		if value != nil && value.After(bound) {
			bound = *value
		}
	}
	return bound
}

func nextLegacyWithTiming(expression string, timing modulecore.TaskTiming, after time.Time) (time.Time, error) {
	if len(timing.AllowedWindows) > 0 {
		if strings.HasPrefix(strings.TrimSpace(expression), "every ") {
			return time.Time{}, errors.New("allowed_windows with every schedule is unsupported")
		}
		if !legacyScheduleFitsWindows(expression, timing.AllowedWindows, timing.Timezone) {
			return time.Time{}, errors.New("schedule does not fit allowed_windows")
		}
	}
	searchAfter := after
	bound := lowerTimingBound(after, timing)
	if bound.After(searchAfter) {
		searchAfter = bound.Add(-time.Nanosecond)
	}
	if strings.HasPrefix(strings.TrimSpace(expression), "every ") {
		interval, err := time.ParseDuration(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "every ")))
		if err != nil || interval <= 0 {
			return time.Time{}, errors.New("invalid every schedule")
		}
		candidate := after.Add(interval)
		if !candidate.After(bound) {
			delta := bound.Sub(after)
			steps := delta/interval + 1
			if delta%interval == 0 {
				steps = delta / interval
			}
			if steps < 1 || steps > time.Duration(1<<62)/interval {
				return time.Time{}, errors.New("every schedule timing bound is out of range")
			}
			candidate = after.Add(steps * interval)
		}
		if !timing.Ready(candidate) {
			return time.Time{}, errors.New("every schedule candidate failed readiness gates")
		}
		return candidate, nil
	}
	candidate, err := NextRunAfter(expression, searchAfter)
	if err != nil {
		return time.Time{}, err
	}
	if !timing.Ready(candidate) {
		return time.Time{}, errors.New("schedule candidate failed readiness gates")
	}
	return candidate, nil
}

func recurrenceTimesWithinWindows(times []string, windows []modulecore.TaskTimeWindow) []string {
	filtered := make([]string, 0, len(times))
	for _, raw := range times {
		at, err := time.Parse("15:04", raw)
		if err != nil {
			continue
		}
		minute := at.Hour()*60 + at.Minute()
		for _, window := range windows {
			start, startErr := time.Parse("15:04", window.Start)
			end, endErr := time.Parse("15:04", window.End)
			if startErr != nil || endErr != nil {
				continue
			}
			startMinute, endMinute := start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
			if (startMinute < endMinute && minute >= startMinute && minute < endMinute) ||
				(startMinute > endMinute && (minute >= startMinute || minute < endMinute)) {
				filtered = append(filtered, raw)
				break
			}
		}
	}
	return filtered
}

func legacyScheduleFitsWindows(expression string, windows []modulecore.TaskTimeWindow, timezone string) bool {
	if !strings.HasPrefix(strings.TrimSpace(expression), "cron ") {
		return false
	}
	if timezone != "UTC" {
		return false
	}
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(expression), "cron ")))
	if len(fields) != 5 {
		return false
	}
	minute, minuteErr := strconv.Atoi(fields[0])
	hour, hourErr := strconv.Atoi(fields[1])
	if minuteErr != nil || hourErr != nil || minute < 0 || minute > 59 || hour < 0 || hour > 23 {
		return false
	}
	clock := time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC).Format("15:04")
	for _, window := range windows {
		start, startErr := time.Parse("15:04", window.Start)
		end, endErr := time.Parse("15:04", window.End)
		if startErr != nil || endErr != nil {
			continue
		}
		at, _ := time.Parse("15:04", clock)
		value, begin, finish := at.Hour()*60+at.Minute(), start.Hour()*60+start.Minute(), end.Hour()*60+end.Minute()
		if (begin < finish && value >= begin && value < finish) || (begin > finish && (value >= begin || value < finish)) {
			return true
		}
	}
	return false
}

// OccurrenceKey returns a stable daily/weekly wall slot or a distinct instant
// for interval and legacy schedules. Configuration revisions do not participate.
func OccurrenceKey(schedule Schedule, scheduled time.Time, timezone string) (string, error) {
	if err := schedule.ScheduleID.Validate(); err != nil {
		return "", fmt.Errorf("schedule_id: %w", err)
	}
	if scheduled.IsZero() || strings.TrimSpace(timezone) == "" {
		return "", errors.New("scheduled time and timezone are required")
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return "", fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	local := scheduled.In(loc)
	if schedule.Timing != nil && schedule.Timing.Recurrence != nil {
		switch schedule.Timing.Recurrence.Frequency {
		case modulecore.TaskRecurrenceDaily, modulecore.TaskRecurrenceWeekly:
			return string(schedule.ScheduleID) + "|" + timezone + "|wall|" + local.Format("2006-01-02") + "|" + local.Format("15:04:05.000000000"), nil
		}
	}
	return string(schedule.ScheduleID) + "|" + timezone + "|instant|" + scheduled.UTC().Format(time.RFC3339Nano), nil
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
