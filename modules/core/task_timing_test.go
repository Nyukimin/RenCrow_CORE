package core

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestTaskTimingJSONIncludesAllSevenFields(t *testing.T) {
	data, err := json.Marshal(TaskTiming{})
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"timezone", "scheduled_start_at", "not_before", "allowed_windows", "deadline_at", "recurrence", "estimated_duration_seconds"} {
		if !strings.Contains(string(data), `"`+key+`"`) {
			t.Errorf("JSON omitted %q: %s", key, data)
		}
	}
	if !strings.Contains(string(data), `"allowed_windows":[]`) {
		t.Errorf("empty allowed_windows must be an array: %s", data)
	}
	timing := TaskTiming{Recurrence: &TaskRecurrence{Frequency: TaskRecurrenceNone}}
	data, err = json.Marshal(timing)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"times":[]`) || !strings.Contains(string(data), `"weekdays":[]`) {
		t.Errorf("empty recurrence collections must be arrays: %s", data)
	}
}

func TestTaskTimingValidateAllowsUnspecifiedAndRejectsBadConditions(t *testing.T) {
	if err := (TaskTiming{}).Validate(); err != nil {
		t.Fatalf("all-unspecified timing: %v", err)
	}
	bad := []TaskTiming{
		{Timezone: "No/Such_Zone", ScheduledStartAt: timePtr(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))},
		{ScheduledStartAt: timePtr(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))},
		{Timezone: "UTC", NotBefore: timePtr(time.Date(2030, 1, 2, 0, 0, 0, 0, time.UTC)), DeadlineAt: timePtr(time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC))},
		{EstimatedDurationSeconds: int64Ptr(-1)},
		{Timezone: "UTC", Recurrence: &TaskRecurrence{Frequency: "daily", Times: []string{"24:00"}}},
	}
	for i, timing := range bad {
		if err := timing.Validate(); err == nil {
			t.Errorf("case %d: expected validation error", i)
		}
	}
}

func TestTaskTimingReadyAndOvernightWindow(t *testing.T) {
	loc := time.FixedZone("+09", 9*60*60)
	timing := TaskTiming{Timezone: "Asia/Tokyo", AllowedWindows: []TaskTimeWindow{{Start: "22:00", End: "02:00"}}}
	if err := timing.Validate(); err != nil {
		t.Fatal(err)
	}
	if !timing.Ready(time.Date(2030, 1, 1, 23, 30, 0, 0, loc)) {
		t.Fatal("expected overnight window to admit 23:30")
	}
	if !timing.Ready(time.Date(2030, 1, 2, 1, 30, 0, 0, loc)) {
		t.Fatal("expected overnight window to admit 01:30")
	}
	if timing.Ready(time.Date(2030, 1, 2, 2, 0, 0, 0, loc)) {
		t.Fatal("window end must be exclusive")
	}
}

func TestTaskTimingDeadlineDoesNotBlockAnOverdueStart(t *testing.T) {
	now := time.Date(2030, 1, 2, 12, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Hour)
	timing := TaskTiming{Timezone: "UTC", DeadlineAt: &deadline}
	if err := timing.Validate(); err != nil {
		t.Fatal(err)
	}
	if !timing.Ready(now) {
		t.Fatal("deadline is not a start gate; owner decides overdue handling")
	}
}

func TestTaskRecurrenceNextOccurrence(t *testing.T) {
	r := TaskRecurrence{Frequency: "weekly", Times: []string{"08:00", "17:00"}, Weekdays: []time.Weekday{time.Monday}}
	after := time.Date(2030, 1, 7, 8, 0, 0, 0, time.UTC) // Monday at first slot.
	next, err := r.NextOccurrence(after, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2030, 1, 7, 17, 0, 0, 0, time.UTC)
	if !next.Equal(want) {
		t.Fatalf("next=%s want=%s", next, want)
	}
	interval := int64(3600)
	next, err = (TaskRecurrence{Frequency: "interval", IntervalSeconds: &interval}).NextOccurrence(after, "UTC")
	if err != nil {
		t.Fatal(err)
	}
	if !next.Equal(after.Add(time.Hour)) {
		t.Fatalf("interval next=%s", next)
	}
}

func timePtr(v time.Time) *time.Time { return &v }
func int64Ptr(v int64) *int64        { return &v }
