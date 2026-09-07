package scheduler

import (
	"context"
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

type deferredExecutor struct{}

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
	if log.TaskID.IsZero() || log.RunID == "" || strings.HasPrefix(string(log.RunID), "schedrun_") {
		t.Fatalf("TaskID/RunID must be minted canonical IDs: log=%+v", log)
	}
	if want := now.Add(5 * time.Minute); !store.schedules[0].NextRunAt.Equal(want) {
		t.Fatalf("NextRunAt=%v want=%v", store.schedules[0].NextRunAt, want)
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
	if log.TaskID.IsZero() || log.RunID == "" {
		t.Fatalf("RunSchedule must mint TaskID and RunID: log=%+v", log)
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
