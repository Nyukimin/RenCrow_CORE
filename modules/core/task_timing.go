package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	TaskRecurrenceNone     = "none"
	TaskRecurrenceDaily    = "daily"
	TaskRecurrenceWeekly   = "weekly"
	TaskRecurrenceInterval = "interval"
)

// TaskTiming is the shared, pure time-condition projection for a task.
// Nil optional fields mean explicitly unspecified for a newly planned item.
type TaskTiming struct {
	Timezone                 string                   `json:"timezone"`
	ScheduledStartAt         *time.Time               `json:"scheduled_start_at"`
	NotBefore                *time.Time               `json:"not_before"`
	AllowedWindows           []TaskTimeWindow         `json:"allowed_windows"`
	DeadlineAt               *time.Time               `json:"deadline_at"`
	Recurrence               *TaskRecurrence          `json:"recurrence"`
	EstimatedDurationSeconds *int64                   `json:"estimated_duration_seconds"`
	CompletionCondition      *TaskCompletionCondition `json:"completion_condition,omitempty"`
}

// TaskCompletionCondition records the owner's intended stop rule. It does not
// start a timer or claim that an execution has satisfied the rule.
type TaskCompletionCondition struct {
	Kind            string `json:"kind"`
	Criteria        string `json:"criteria,omitempty"`
	TargetCount     int64  `json:"target_count,omitempty"`
	CountUnit       string `json:"count_unit,omitempty"`
	DurationSeconds int64  `json:"duration_seconds,omitempty"`
}

func (c TaskCompletionCondition) Validate() error {
	if c.Criteria != strings.TrimSpace(c.Criteria) || c.CountUnit != strings.TrimSpace(c.CountUnit) {
		return errors.New("completion condition text must be trimmed")
	}
	switch c.Kind {
	case "unspecified":
		if c.Criteria != "" || c.TargetCount != 0 || c.CountUnit != "" || c.DurationSeconds != 0 {
			return errors.New("unspecified completion condition must not have parameters")
		}
	case "until_complete":
		if c.Criteria == "" || c.TargetCount != 0 || c.CountUnit != "" || c.DurationSeconds != 0 {
			return errors.New("until_complete requires only completion criteria")
		}
	case "count":
		if c.TargetCount <= 0 || c.CountUnit == "" || c.Criteria != "" || c.DurationSeconds != 0 {
			return errors.New("count requires a positive target and count unit")
		}
	case "duration":
		if c.DurationSeconds <= 0 || c.Criteria != "" || c.TargetCount != 0 || c.CountUnit != "" {
			return errors.New("duration requires a positive activity duration")
		}
	default:
		return errors.New("invalid completion condition kind")
	}
	return nil
}

// MarshalJSON keeps the windows field an array when no windows are specified.
func (t TaskTiming) MarshalJSON() ([]byte, error) {
	type taskTimingJSON TaskTiming
	if t.AllowedWindows == nil {
		t.AllowedWindows = []TaskTimeWindow{}
	}
	return json.Marshal(taskTimingJSON(t))
}

// MarshalJSON keeps recurrence collection fields arrays when empty.
func (r TaskRecurrence) MarshalJSON() ([]byte, error) {
	type taskRecurrenceJSON TaskRecurrence
	if r.Times == nil {
		r.Times = []string{}
	}
	if r.Weekdays == nil {
		r.Weekdays = []time.Weekday{}
	}
	return json.Marshal(taskRecurrenceJSON(r))
}

// TaskTimeWindow is a local-time interval in TaskTiming.Timezone.
type TaskTimeWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// TaskRecurrence defines a local-time recurrence. "none" means no automatic repeat.
type TaskRecurrence struct {
	Frequency       string         `json:"frequency"`
	Times           []string       `json:"times"`
	Weekdays        []time.Weekday `json:"weekdays"`
	IntervalSeconds *int64         `json:"interval_seconds"`
}

func (t TaskTiming) Validate() error {
	if t.CompletionCondition != nil {
		if err := t.CompletionCondition.Validate(); err != nil {
			return fmt.Errorf("completion_condition: %w", err)
		}
	}
	if t.EstimatedDurationSeconds != nil && *t.EstimatedDurationSeconds < 0 {
		return errors.New("estimated_duration_seconds must not be negative")
	}
	for _, condition := range []struct {
		name  string
		value *time.Time
	}{
		{name: "scheduled_start_at", value: t.ScheduledStartAt},
		{name: "not_before", value: t.NotBefore},
		{name: "deadline_at", value: t.DeadlineAt},
	} {
		if condition.value != nil && condition.value.IsZero() {
			return fmt.Errorf("%s must not be zero", condition.name)
		}
	}
	if t.DeadlineAt != nil {
		if t.ScheduledStartAt != nil && t.ScheduledStartAt.After(*t.DeadlineAt) {
			return errors.New("scheduled_start_at must not be after deadline_at")
		}
		if t.NotBefore != nil && t.NotBefore.After(*t.DeadlineAt) {
			return errors.New("not_before must not be after deadline_at")
		}
	}
	for i, window := range t.AllowedWindows {
		start, err := parseTaskClock(window.Start)
		if err != nil {
			return fmt.Errorf("allowed_windows[%d].start: %w", i, err)
		}
		end, err := parseTaskClock(window.End)
		if err != nil {
			return fmt.Errorf("allowed_windows[%d].end: %w", i, err)
		}
		if start == end {
			return fmt.Errorf("allowed_windows[%d] start and end must differ", i)
		}
	}
	if t.Recurrence != nil {
		if err := t.Recurrence.Validate(); err != nil {
			return fmt.Errorf("recurrence: %w", err)
		}
	}
	needsTimezone := t.ScheduledStartAt != nil || t.NotBefore != nil || t.DeadlineAt != nil || len(t.AllowedWindows) > 0 ||
		(t.Recurrence != nil && t.Recurrence.Frequency != TaskRecurrenceNone)
	if t.Timezone != "" {
		if _, err := time.LoadLocation(t.Timezone); err != nil {
			return fmt.Errorf("invalid timezone %q: %w", t.Timezone, err)
		}
	}
	if needsTimezone {
		if t.Timezone == "" {
			return errors.New("timezone is required for time conditions")
		}
	}
	return nil
}

// Ready reports whether generic execution can start now, including its ability
// to enforce the completion condition. Deadline handling belongs to the owner.
func (t TaskTiming) Ready(now time.Time) bool {
	if t.Validate() != nil || now.IsZero() {
		return false
	}
	// Generic execution cannot yet verify explicit completion rules. Keep such
	// tasks out of executable queues until their owner supports the rule.
	if t.CompletionCondition != nil && t.CompletionCondition.Kind != "unspecified" {
		return false
	}
	return t.ScheduleReady(now)
}

// ScheduleReady checks only start-time constraints. Callers must separately
// establish that their execution owner can enforce any completion condition.
func (t TaskTiming) ScheduleReady(now time.Time) bool {
	if t.Validate() != nil || now.IsZero() {
		return false
	}
	if t.ScheduledStartAt != nil && now.Before(*t.ScheduledStartAt) {
		return false
	}
	if t.NotBefore != nil && now.Before(*t.NotBefore) {
		return false
	}
	if len(t.AllowedWindows) == 0 {
		return true
	}
	loc, err := time.LoadLocation(t.Timezone)
	if err != nil {
		return false
	}
	local := now.In(loc)
	minute := local.Hour()*60 + local.Minute()
	for _, window := range t.AllowedWindows {
		start, _ := parseTaskClock(window.Start)
		end, _ := parseTaskClock(window.End)
		if start < end && minute >= start && minute < end {
			return true
		}
		if start > end && (minute >= start || minute < end) {
			return true
		}
	}
	return false
}

func (r TaskRecurrence) NextOccurrence(after time.Time, timezone string) (time.Time, error) {
	if err := r.Validate(); err != nil {
		return time.Time{}, err
	}
	if r.Frequency == TaskRecurrenceNone {
		return time.Time{}, nil
	}
	loc, err := time.LoadLocation(timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid timezone %q: %w", timezone, err)
	}
	if r.Frequency == TaskRecurrenceInterval {
		seconds := *r.IntervalSeconds
		if seconds > int64((time.Duration(1<<63-1))/time.Second) {
			return time.Time{}, errors.New("interval_seconds is too large")
		}
		return after.Add(time.Duration(seconds) * time.Second), nil
	}

	times := make([]int, 0, len(r.Times))
	for _, value := range r.Times {
		minute, _ := parseTaskClock(value)
		times = append(times, minute)
	}
	sort.Ints(times)
	weekdays := make(map[time.Weekday]struct{}, len(r.Weekdays))
	for _, day := range r.Weekdays {
		weekdays[day] = struct{}{}
	}
	localAfter := after.In(loc)
	for dayOffset := 0; dayOffset <= 370; dayOffset++ {
		date := localAfter.AddDate(0, 0, dayOffset)
		if r.Frequency == TaskRecurrenceWeekly {
			if _, ok := weekdays[date.Weekday()]; !ok {
				continue
			}
		}
		for _, minute := range times {
			hour, min := minute/60, minute%60
			candidate := time.Date(date.Year(), date.Month(), date.Day(), hour, min, 0, 0, loc)
			candidateLocal := candidate.In(loc)
			// Skip nonexistent wall-clock times during a daylight-saving jump.
			if candidateLocal.Year() != date.Year() || candidateLocal.YearDay() != date.YearDay() || candidateLocal.Hour() != hour || candidateLocal.Minute() != min {
				continue
			}
			if candidate.After(after) {
				return candidate, nil
			}
		}
	}
	return time.Time{}, errors.New("no recurrence occurrence found within 370 days")
}

func (r TaskRecurrence) Validate() error {
	switch r.Frequency {
	case TaskRecurrenceNone:
		if len(r.Times) != 0 || len(r.Weekdays) != 0 || r.IntervalSeconds != nil {
			return errors.New("none recurrence must not set times, weekdays, or interval_seconds")
		}
		return nil
	case TaskRecurrenceDaily:
		if len(r.Times) == 0 || len(r.Weekdays) != 0 || r.IntervalSeconds != nil {
			return errors.New("daily recurrence requires times and forbids weekdays and interval_seconds")
		}
	case TaskRecurrenceWeekly:
		if len(r.Times) == 0 || len(r.Weekdays) == 0 || r.IntervalSeconds != nil {
			return errors.New("weekly recurrence requires times and weekdays and forbids interval_seconds")
		}
	case TaskRecurrenceInterval:
		if r.IntervalSeconds == nil || *r.IntervalSeconds <= 0 || len(r.Times) != 0 || len(r.Weekdays) != 0 {
			return errors.New("interval recurrence requires positive interval_seconds and forbids times and weekdays")
		}
	default:
		return fmt.Errorf("invalid recurrence frequency %q", r.Frequency)
	}
	seenTimes := make(map[int]struct{}, len(r.Times))
	for _, value := range r.Times {
		minute, err := parseTaskClock(value)
		if err != nil {
			return fmt.Errorf("invalid recurrence time %q: %w", value, err)
		}
		if _, exists := seenTimes[minute]; exists {
			return fmt.Errorf("duplicate recurrence time %q", value)
		}
		seenTimes[minute] = struct{}{}
	}
	seenDays := make(map[time.Weekday]struct{}, len(r.Weekdays))
	for _, day := range r.Weekdays {
		if day < time.Sunday || day > time.Saturday {
			return fmt.Errorf("invalid recurrence weekday %d", day)
		}
		if _, exists := seenDays[day]; exists {
			return fmt.Errorf("duplicate recurrence weekday %d", day)
		}
		seenDays[day] = struct{}{}
	}
	return nil
}

func parseTaskClock(value string) (int, error) {
	parsed, err := time.Parse("15:04", value)
	if err != nil || parsed.Format("15:04") != value {
		return 0, fmt.Errorf("must be a valid HH:MM time")
	}
	return parsed.Hour()*60 + parsed.Minute(), nil
}
