package scheduler

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNextRunAfterScheduleSupportsLocalDailySlotsAndStableIdentity(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	base := time.Date(2026, 11, 17, 7, 15, 0, 0, loc)
	start := time.Date(2026, 11, 17, 8, 0, 0, 0, loc)
	timing := &modulecore.TaskTiming{
		Timezone: "Asia/Tokyo", ScheduledStartAt: &start,
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00", "17:00"}},
	}
	schedule := Schedule{ScheduleID: modulecore.NewScheduleID(), Timing: timing}
	first, err := NextRunAfterSchedule(schedule, base)
	if err != nil || !first.Equal(start.UTC()) {
		t.Fatalf("first=%s err=%v", first, err)
	}
	second, err := NextRunAfterSchedule(schedule, first)
	if err != nil || !second.Equal(time.Date(2026, 11, 17, 12, 0, 0, 0, loc).UTC()) {
		t.Fatalf("second=%s err=%v", second, err)
	}
	id1, err := OccurrenceKey(schedule, second, "Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	id2, err := OccurrenceKey(schedule, second.In(time.FixedZone("JST", 9*60*60)), "Asia/Tokyo")
	if err != nil || id1 != id2 {
		t.Fatalf("local slot identity changed: %q != %q, err=%v", id1, id2, err)
	}
}

func TestOccurrenceKeyDistinguishesIntervalInstantsAcrossDSTFold(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)
	if first.In(loc).Format("2006-01-02 15:04") != second.In(loc).Format("2006-01-02 15:04") {
		t.Fatalf("test instants are not the same folded wall slot: %s %s", first.In(loc), second.In(loc))
	}
	scheduleID := modulecore.NewScheduleID()
	interval := int64(3600)
	intervalSchedule := Schedule{ScheduleID: scheduleID, Timing: &modulecore.TaskTiming{
		Timezone:   "America/New_York",
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceInterval, IntervalSeconds: &interval},
	}}
	firstKey, err := OccurrenceKey(intervalSchedule, first, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	secondKey, err := OccurrenceKey(intervalSchedule, second, "America/New_York")
	if err != nil || firstKey == secondKey {
		t.Fatalf("interval instants must have distinct keys: first=%q second=%q err=%v", firstKey, secondKey, err)
	}
	dailySchedule := Schedule{ScheduleID: scheduleID, Timing: &modulecore.TaskTiming{
		Timezone:   "America/New_York",
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"01:30"}},
	}}
	dailyFirst, err := OccurrenceKey(dailySchedule, first, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	dailySecond, err := OccurrenceKey(dailySchedule, second, "America/New_York")
	if err != nil || dailyFirst != dailySecond {
		t.Fatalf("daily wall slot should have one stable key across DST fold: %q != %q err=%v", dailyFirst, dailySecond, err)
	}
	legacySchedule := Schedule{ScheduleID: scheduleID, Schedule: "cron 30 1 * * *"}
	legacyFirst, err := OccurrenceKey(legacySchedule, first, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	legacySecond, err := OccurrenceKey(legacySchedule, second, "America/New_York")
	if err != nil || legacyFirst == legacySecond {
		t.Fatalf("legacy instants must remain distinct across DST fold: %q != %q err=%v", legacyFirst, legacySecond, err)
	}
}

func TestTimingFastForwardsDailySlotsAndAllowsNightWindows(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	notBefore := time.Date(2035, 2, 3, 22, 0, 0, 0, loc)
	timing := &modulecore.TaskTiming{
		Timezone: "Asia/Tokyo", NotBefore: &notBefore,
		AllowedWindows: []modulecore.TaskTimeWindow{{Start: "22:00", End: "06:00"}},
		Recurrence:     &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"01:00", "22:30"}},
	}
	schedule := Schedule{ScheduleID: modulecore.NewScheduleID(), Timing: timing}
	next, err := NextRunAfterSchedule(schedule, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	want := time.Date(2035, 2, 3, 22, 30, 0, 0, loc)
	if err != nil || !next.Equal(want.UTC()) {
		t.Fatalf("next=%s want=%s err=%v", next, want, err)
	}
}

func TestTimingEveryPreservesIntervalAnchorAcrossNotBefore(t *testing.T) {
	base := time.Date(2026, 11, 17, 7, 0, 0, 0, time.UTC)
	notBefore := base.Add(150 * time.Minute)
	timing := &modulecore.TaskTiming{Timezone: "UTC", NotBefore: &notBefore}
	schedule := Schedule{ScheduleID: modulecore.NewScheduleID(), Schedule: "every 1h", Timing: timing}
	next, err := NextRunAfterSchedule(schedule, base)
	want := base.Add(3 * time.Hour)
	if err != nil || !next.Equal(want) {
		t.Fatalf("next=%s want=%s err=%v", next, want, err)
	}
}

func TestTimingRejectsUnsupportedIntervalWindowCombination(t *testing.T) {
	interval := int64(600)
	timing := &modulecore.TaskTiming{
		Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{{Start: "22:00", End: "06:00"}},
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceInterval, IntervalSeconds: &interval},
	}
	if _, err := NextRunAfterSchedule(Schedule{ScheduleID: modulecore.NewScheduleID(), Timing: timing}, time.Now().UTC()); err == nil {
		t.Fatal("expected interval recurrence with local windows to be explicitly unsupported")
	}
}

func TestValidateScheduleRejectsTwoCadenceSources(t *testing.T) {
	now := time.Now().UTC()
	schedule := Schedule{
		ScheduleID: modulecore.NewScheduleID(), Name: "conflicting cadence", Schedule: "cron 0 8 * * *", Enabled: false,
		CreatedAt: now, UpdatedAt: now,
		Timing: &modulecore.TaskTiming{
			Timezone:   "UTC",
			Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00"}},
		},
	}
	if err := ValidateSchedule(schedule); err == nil {
		t.Fatal("legacy schedule and typed recurrence must not define competing cadences")
	}
}

func TestTimingDeadlineAndEstimateDoNotBecomeExecutionTimeout(t *testing.T) {
	deadline := time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	estimate := int64(7200)
	timing := modulecore.TaskTiming{Timezone: "UTC", DeadlineAt: &deadline, EstimatedDurationSeconds: &estimate}
	if err := timing.Validate(); err != nil {
		t.Fatalf("advisory estimate/deadline should validate: %v", err)
	}
	if !timing.Ready(deadline.Add(time.Second)) {
		t.Fatal("shared readiness must leave deadline policy to the owner")
	}
	base := deadline.Add(time.Second)
	schedule := Schedule{
		ScheduleID: modulecore.NewScheduleID(), Schedule: "cron 0 10 * * *", Timing: &timing,
	}
	if next, err := NextRunAfterSchedule(schedule, base); err != nil || !next.After(deadline) {
		t.Fatalf("deadline target must not prevent a later schedule slot: next=%s err=%v", next, err)
	}
}

func TestNextRunAfterSupportsAtEveryAndCron(t *testing.T) {
	base := time.Date(2026, 6, 22, 7, 12, 30, 0, time.UTC)
	at, err := NextRunAfter("at 2026-06-22T08:00:00Z", base)
	if err != nil || !at.Equal(time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC)) {
		t.Fatalf("at=%s err=%v", at, err)
	}
	every, err := NextRunAfter("every 30m", base)
	if err != nil || !every.Equal(base.Add(30*time.Minute)) {
		t.Fatalf("every=%s err=%v", every, err)
	}
	cron, err := NextRunAfter("cron 15 8 * * *", base)
	if err != nil || !cron.Equal(time.Date(2026, 6, 22, 8, 15, 0, 0, time.UTC)) {
		t.Fatalf("cron=%s err=%v", cron, err)
	}
}

func TestValidateScheduleRejectsElapsedOneShot(t *testing.T) {
	schedule := Schedule{
		ScheduleID: modulecore.NewScheduleID(),
		Name:       "old one-shot",
		Schedule:   "at 2000-01-01T00:00:00Z",
		Enabled:    true,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
	}
	if err := ValidateSchedule(schedule); err == nil {
		t.Fatal("expected elapsed one-shot to be rejected")
	}
}

func TestValidateScheduleAllowsDisabledElapsedOneShot(t *testing.T) {
	schedule := Schedule{
		ScheduleID: modulecore.NewScheduleID(),
		Name:       "completed one-shot",
		Schedule:   "at 2000-01-01T00:00:00Z",
		Enabled:    false,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		DisabledAt: time.Now().UTC(),
		DisabledBy: "scheduler",
	}
	if err := ValidateSchedule(schedule); err != nil {
		t.Fatalf("disabled elapsed one-shot should be valid: %v", err)
	}
}

func TestValidateRunLogAcceptsDistinctCanonicalIDs(t *testing.T) {
	log := RunLog{
		RunID:      modulecore.NewRunID(),
		ScheduleID: modulecore.NewScheduleID(),
		TaskID:     modulecore.NewTaskID(),
		Trigger:    "due",
		Status:     "completed",
		StartedAt:  time.Now().UTC(),
	}
	if err := ValidateRunLog(log); err != nil {
		t.Fatalf("ValidateRunLog() error = %v", err)
	}
}
