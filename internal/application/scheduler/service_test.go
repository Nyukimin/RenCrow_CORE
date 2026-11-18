package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type memoryStore struct {
	schedules []domainscheduler.Schedule
	logs      []domainscheduler.RunLog
}

func (s *memoryStore) ListSchedules(context.Context, int) ([]domainscheduler.Schedule, error) {
	return append([]domainscheduler.Schedule(nil), s.schedules...), nil
}

func (s *memoryStore) SaveSchedule(_ context.Context, schedule domainscheduler.Schedule) error {
	for i := range s.schedules {
		if s.schedules[i].ScheduleID == schedule.ScheduleID {
			s.schedules[i] = schedule
			return nil
		}
	}
	s.schedules = append(s.schedules, schedule)
	return nil
}

func (s *memoryStore) SaveRunLog(_ context.Context, log domainscheduler.RunLog) error {
	s.logs = append(s.logs, log)
	return nil
}

func (s *memoryStore) ListRunLogs(context.Context, int) ([]domainscheduler.RunLog, error) {
	return append([]domainscheduler.RunLog(nil), s.logs...), nil
}

type recordingExecutor struct {
	scheduleID modulecore.ScheduleID
}

type occurrenceRecordingExecutor struct {
	occurrences []domainscheduler.ScheduledOccurrence
}

func (e *occurrenceRecordingExecutor) ExecuteSchedule(context.Context, domainscheduler.Schedule) (string, error) {
	return "legacy fallback called", nil
}

func (e *occurrenceRecordingExecutor) ExecuteScheduledOccurrence(_ context.Context, _ domainscheduler.Schedule, occurrence domainscheduler.ScheduledOccurrence) (string, error) {
	e.occurrences = append(e.occurrences, occurrence)
	return "occurrence executed", nil
}

type deferredExecutor struct{}

const executorUnavailableErrorText = "scheduler executor unavailable"

func (deferredExecutor) ExecuteSchedule(context.Context, domainscheduler.Schedule) (string, error) {
	return "GPU is busy", NewDeferredError(5*time.Minute, errors.New("gpu busy"))
}

func (e *recordingExecutor) ExecuteSchedule(_ context.Context, schedule domainscheduler.Schedule) (string, error) {
	e.scheduleID = schedule.ScheduleID
	return "executed " + schedule.Name, nil
}

func TestServiceDefersScheduleWithoutAdvancingToNextSchedule(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	scheduleID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "TTS pronunciation daily",
		Schedule: "cron 30 19 * * *", Target: "tts_pronunciation_check", Enabled: true,
		CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour), NextRunAt: now,
	}}}
	svc := NewService(store, deferredExecutor{}).WithNow(func() time.Time { return now })
	log, err := svc.RunSchedule(context.Background(), string(scheduleID), "due")
	if err != nil {
		t.Fatalf("RunSchedule() error = %v", err)
	}
	if log.Status != "deferred" || log.Error != "" {
		t.Fatalf("log = %+v", log)
	}
	if log.ScheduleID != scheduleID {
		t.Fatalf("ScheduleID=%v want=%v", log.ScheduleID, scheduleID)
	}
	if log.RunID == "" || log.OccurrenceKey == "" || strings.HasPrefix(string(log.RunID), "schedrun_") {
		t.Fatalf("RunID/OccurrenceKey must be canonical scheduler identities: log=%+v", log)
	}
	if !log.TaskID.IsZero() {
		t.Fatalf("new scheduler execution must not mint a TaskManager ID: %+v", log)
	}
	encoded, err := json.Marshal(log)
	if err != nil || !strings.Contains(string(encoded), `"task_id":""`) {
		t.Fatalf("RunLog must preserve the legacy task_id JSON field without minting an ID: json=%s err=%v", encoded, err)
	}
	if want := now.Add(5 * time.Minute); !store.schedules[0].NextRunAt.Equal(want) {
		t.Fatalf("NextRunAt=%v want=%v", store.schedules[0].NextRunAt, want)
	}
}

func TestManualDeferredRunDoesNotInventDueOccurrence(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	scheduleID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "manual", Schedule: "every 24h", Enabled: true,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now.Add(time.Hour),
	}}}
	svc := NewService(store, deferredExecutor{}).WithNow(func() time.Time { return now })
	log, err := svc.RunSchedule(context.Background(), string(scheduleID), "manual")
	if err != nil {
		t.Fatalf("manual deferred run error = %v", err)
	}
	if log.Status != "deferred" || log.OccurrenceKey != "" || store.schedules[0].PendingOccurrenceAt != nil {
		t.Fatalf("manual retry must not invent a scheduled occurrence: log=%+v schedule=%+v", log, store.schedules[0])
	}
}

func TestServiceRunsOnlyRequestedDueSchedule(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	targetID := modulecore.NewScheduleID()
	otherID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{
		{ScheduleID: targetID, Name: "TTS pronunciation daily", Schedule: "every 24h", Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now},
		{ScheduleID: otherID, Name: "Unrelated", Schedule: "every 24h", Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now},
	}}
	executor := &recordingExecutor{}
	svc := NewService(store, executor).WithNow(func() time.Time { return now })
	log, ran, err := svc.RunDueSchedule(context.Background(), string(targetID))
	if err != nil || !ran || log.ScheduleID != targetID || executor.scheduleID != targetID {
		t.Fatalf("log=%+v ran=%v executor=%+v err=%v", log, ran, executor, err)
	}
	if len(store.logs) != 1 {
		t.Fatalf("logs=%+v", store.logs)
	}
}

func TestDueSchedulePassesStableOccurrenceAndAllTimingFieldsToExecutor(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 8, 0, 0, 0, loc).UTC()
	scheduled := now
	notBefore := now.Add(-time.Hour)
	deadline := now.Add(time.Hour)
	estimate := int64(1200)
	timing := &modulecore.TaskTiming{
		Timezone: "Asia/Tokyo", ScheduledStartAt: &scheduled, NotBefore: &notBefore,
		AllowedWindows: []modulecore.TaskTimeWindow{{Start: "08:00", End: "09:00"}},
		DeadlineAt:     &deadline, EstimatedDurationSeconds: &estimate,
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00"}},
	}
	scheduleID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "daily slots", Schedule: "cron 0 0 * * *", Enabled: true, Timing: timing,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now,
	}}}
	executor := &occurrenceRecordingExecutor{}
	svc := NewService(store, executor).WithNow(func() time.Time { return now })
	log, ran, err := svc.RunDueSchedule(context.Background(), string(scheduleID))
	if err != nil || !ran || len(executor.occurrences) != 1 {
		t.Fatalf("log=%+v ran=%v occurrences=%+v err=%v", log, ran, executor.occurrences, err)
	}
	occurrence := executor.occurrences[0]
	if occurrence.ScheduledAt != now || occurrence.OccurrenceKey != log.OccurrenceKey ||
		occurrence.Timing.Timezone != "Asia/Tokyo" || occurrence.Timing.ScheduledStartAt == nil || !occurrence.Timing.ScheduledStartAt.Equal(scheduled) ||
		occurrence.Timing.NotBefore == nil || !occurrence.Timing.NotBefore.Equal(notBefore) ||
		occurrence.Timing.DeadlineAt == nil || !occurrence.Timing.DeadlineAt.Equal(deadline) ||
		occurrence.Timing.EstimatedDurationSeconds == nil || *occurrence.Timing.EstimatedDurationSeconds != estimate ||
		len(occurrence.Timing.AllowedWindows) != 1 || occurrence.Timing.Recurrence == nil {
		t.Fatalf("executor did not receive concrete occurrence timing: %+v", occurrence)
	}
	wantKey := string(scheduleID) + "|Asia/Tokyo|wall|2026-11-17|08:00:00.000000000"
	if occurrence.OccurrenceKey != wantKey {
		t.Fatalf("occurrence key=%q want composite key %q", occurrence.OccurrenceKey, wantKey)
	}
}

func TestTypedRecurrenceSkipsMissedSlotsAfterActualStart(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	scheduled := time.Date(2026, 11, 17, 12, 0, 0, 0, loc)
	now := time.Date(2026, 11, 17, 18, 0, 0, 0, loc).UTC()
	scheduleID := modulecore.NewScheduleID()
	timing := &modulecore.TaskTiming{
		Timezone:   "Asia/Tokyo",
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00", "17:00"}},
	}
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "daily slots", Enabled: true, Timing: timing,
		CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour), NextRunAt: scheduled.UTC(),
	}}}
	svc := NewService(store, &recordingExecutor{}).WithNow(func() time.Time { return now })
	if _, ran, err := svc.RunDueSchedule(context.Background(), string(scheduleID)); err != nil || !ran {
		t.Fatalf("late typed recurrence should run its due occurrence: ran=%v err=%v", ran, err)
	}
	want := time.Date(2026, 11, 18, 8, 0, 0, 0, loc).UTC()
	if !store.schedules[0].NextRunAt.Equal(want) {
		t.Fatalf("missed slots should be skipped from actual start: next=%s want=%s", store.schedules[0].NextRunAt, want)
	}
}

func TestRunDueSkipsOccurrenceAfterAllowedWindowAndAdvances(t *testing.T) {
	loc, err := time.LoadLocation("Asia/Tokyo")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, loc).UTC()
	scheduleID := modulecore.NewScheduleID()
	timing := &modulecore.TaskTiming{
		Timezone: "Asia/Tokyo", AllowedWindows: []modulecore.TaskTimeWindow{{Start: "08:00", End: "09:00"}},
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"08:00", "12:00"}},
	}
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "morning-only", Enabled: true, Timing: timing,
		CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour),
		NextRunAt: time.Date(2026, 11, 17, 8, 0, 0, 0, loc).UTC(),
	}}}
	svc := NewService(store, &recordingExecutor{}).WithNow(func() time.Time { return now })
	if _, ran, err := svc.RunDueSchedule(context.Background(), string(scheduleID)); err != nil || ran {
		t.Fatalf("closed window should skip the stale occurrence: ran=%v err=%v", ran, err)
	}
	want := time.Date(2026, 11, 18, 8, 0, 0, 0, loc).UTC()
	if !store.schedules[0].NextRunAt.Equal(want) || len(store.logs) != 0 {
		t.Fatalf("stale window occurrence should advance without execution: schedule=%+v logs=%+v", store.schedules[0], store.logs)
	}
}

func TestTimingDeadlineRemainsOwnerCompletionTarget(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	deadline := now.Add(-time.Second)
	scheduleID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "expired", Schedule: "every 1h", Enabled: true,
		Timing:    &modulecore.TaskTiming{Timezone: "UTC", DeadlineAt: &deadline},
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now,
	}}}
	executor := &recordingExecutor{}
	svc := NewService(store, executor).WithNow(func() time.Time { return now })
	if due, err := svc.DueSchedules(context.Background(), 10); err != nil || len(due) != 1 {
		t.Fatalf("deadline target alone should not reject a start: due=%+v err=%v", due, err)
	}
	log, ran, err := svc.RunDueSchedule(context.Background(), string(scheduleID))
	if err != nil || !ran || log.Status != "completed" || executor.scheduleID != scheduleID {
		t.Fatalf("deadline target must not auto-kill execution: log=%+v ran=%v err=%v", log, ran, err)
	}
}

func TestServiceDueFireUsesSchedulerOccurrenceKeyAndPreventsSequentialRefire(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	scheduleID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID, Name: "TTS pronunciation daily",
		Schedule: "every 24h", Target: "tts_pronunciation_check", Enabled: true,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now,
	}}}
	svc := NewService(store, &recordingExecutor{}).WithNow(func() time.Time { return now })
	ctx := context.Background()

	log, ran, err := svc.RunDueSchedule(ctx, string(scheduleID))
	if err != nil || !ran || log.ScheduleID != scheduleID {
		t.Fatalf("first fire log=%+v ran=%v err=%v", log, ran, err)
	}
	firstRunID, firstOccurrenceKey := log.RunID, log.OccurrenceKey
	if string(firstRunID) == string(scheduleID) || firstOccurrenceKey == "" {
		t.Fatalf("fire must keep scheduler run and occurrence identities distinct: log=%+v", log)
	}

	if due, err := svc.DueSchedules(ctx, 10); err != nil || len(due) != 0 {
		t.Fatalf("fired occurrence must leave the due set: due=%#v err=%v", due, err)
	}
	if second, ranAgain, err := svc.RunDueSchedule(ctx, string(scheduleID)); err != nil || ranAgain {
		t.Fatalf("duplicate fire of the same occurrence must be rejected: log=%+v ran=%v err=%v", second, ranAgain, err)
	}
	if len(store.logs) != 1 || store.logs[0].RunID != firstRunID || store.logs[0].OccurrenceKey != firstOccurrenceKey {
		t.Fatalf("run logs must keep exactly one occurrence: logs=%+v", store.logs)
	}
}

func TestServiceCreateDueRunAndDisableSchedule(t *testing.T) {
	now := time.Date(2026, 6, 22, 7, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	executor := &recordingExecutor{}
	svc := NewService(store, executor).WithNow(func() time.Time { return now })
	ctx := context.Background()

	scheduleID := modulecore.NewScheduleID()
	schedule, err := svc.CreateSchedule(ctx, domainscheduler.Schedule{
		ScheduleID: scheduleID,
		Name:       "Backlog heartbeat",
		Schedule:   "every 10m",
		Target:     "backlog",
		Prompt:     "process backlog",
	})
	if err != nil {
		t.Fatalf("CreateSchedule() error = %v", err)
	}
	if !schedule.Enabled || !schedule.NextRunAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("schedule=%#v", schedule)
	}
	if schedule.Timing == nil || schedule.Timing.Recurrence == nil || schedule.Timing.Recurrence.Frequency != modulecore.TaskRecurrenceNone {
		t.Fatalf("new schedule must persist the complete shared timing shape: %+v", schedule.Timing)
	}

	svc.WithNow(func() time.Time { return now.Add(11 * time.Minute) })
	due, err := svc.DueSchedules(ctx, 10)
	if err != nil || len(due) != 1 || due[0].Schedule.ScheduleID != scheduleID {
		t.Fatalf("due=%#v err=%v", due, err)
	}

	log, err := svc.RunSchedule(ctx, string(scheduleID), "manual")
	if err != nil {
		t.Fatalf("RunSchedule() error = %v", err)
	}
	if log.Status != "completed" || executor.scheduleID != scheduleID || len(store.logs) != 1 {
		t.Fatalf("log=%#v executor=%#v logs=%#v", log, executor, store.logs)
	}
	if log.RunID == "" || log.OccurrenceKey != "" {
		t.Fatalf("manual RunSchedule must mint only its scheduler RunID: log=%+v", log)
	}
	if !store.schedules[0].LastRunAt.Equal(now.Add(11*time.Minute)) || !store.schedules[0].NextRunAt.Equal(now.Add(21*time.Minute)) {
		t.Fatalf("schedule after run=%#v", store.schedules[0])
	}

	disabled, err := svc.DisableSchedule(ctx, string(scheduleID), "coder")
	if err != nil {
		t.Fatalf("DisableSchedule() error = %v", err)
	}
	if disabled.Enabled || disabled.DisabledBy != "coder" {
		t.Fatalf("disabled=%#v", disabled)
	}
}

func TestServiceRunScheduleRecordsFailureWhenExecutorUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	scheduleID := modulecore.NewScheduleID()
	store := &memoryStore{schedules: []domainscheduler.Schedule{{
		ScheduleID: scheduleID,
		Name:       "TTS pronunciation daily",
		Schedule:   "every 24h",
		Target:     "tts_pronunciation_check",
		Enabled:    true,
		CreatedAt:  now.Add(-time.Hour),
		UpdatedAt:  now.Add(-time.Hour),
		NextRunAt:  now,
	}}}
	svc := NewService(store, nil).WithNow(func() time.Time { return now })

	log, err := svc.RunSchedule(context.Background(), string(scheduleID), "due")
	if err != nil {
		t.Fatalf("RunSchedule() error = %v", err)
	}
	if log.Status != "failed" || log.Error != executorUnavailableErrorText {
		t.Fatalf("log = %+v", log)
	}
	if log.Summary != "scheduler run recorded without executor" {
		t.Fatalf("summary = %q", log.Summary)
	}
	if len(store.logs) != 1 || store.logs[0].Status != "failed" {
		t.Fatalf("persisted logs = %+v", store.logs)
	}
	if !store.schedules[0].LastRunAt.Equal(now) || !store.schedules[0].NextRunAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("schedule after unavailable run = %+v", store.schedules[0])
	}
}

func TestServiceCreateScheduleMintsScheduleIDWhenEmpty(t *testing.T) {
	now := time.Date(2026, 6, 22, 7, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	svc := NewService(store, nil).WithNow(func() time.Time { return now })
	schedule, err := svc.CreateSchedule(context.Background(), domainscheduler.Schedule{
		Name:     "Auto ID",
		Schedule: "every 1h",
	})
	if err != nil {
		t.Fatalf("CreateSchedule() error = %v", err)
	}
	if schedule.ScheduleID == "" {
		t.Fatal("expected ScheduleID to be minted")
	}
	if err := schedule.ScheduleID.Validate(); err != nil {
		t.Fatalf("ScheduleID.Validate() error = %v", err)
	}
}
