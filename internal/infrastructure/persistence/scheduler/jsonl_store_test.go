package scheduler

import (
	"context"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStorePersistsLatestSchedulesAndRunAudit(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 7, 0, 0, 0, time.UTC)
	scheduleID := modulecore.NewScheduleID()
	schedule := domainscheduler.Schedule{
		ScheduleID: scheduleID,
		Name:       "Backlog heartbeat",
		Schedule:   "every 15m",
		Prompt:     "process backlog",
		Target:     "backlog",
		Enabled:    true,
		CreatedAt:  now,
		UpdatedAt:  now,
		NextRunAt:  now.Add(15 * time.Minute),
	}
	if err := store.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule() error = %v", err)
	}
	schedule.Enabled = false
	schedule.UpdatedAt = now.Add(time.Minute)
	schedule.DisabledAt = now.Add(time.Minute)
	schedule.DisabledBy = "coder"
	if err := store.SaveSchedule(ctx, schedule); err != nil {
		t.Fatalf("SaveSchedule(disabled) error = %v", err)
	}
	if err := store.SaveRunLog(ctx, domainscheduler.RunLog{
		RunID:       modulecore.NewRunID(),
		ScheduleID:  scheduleID,
		TaskID:      modulecore.NewTaskID(),
		Trigger:     "manual",
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now.Add(time.Second),
		Summary:     "ok",
	}); err != nil {
		t.Fatalf("SaveRunLog() error = %v", err)
	}
	schedules, err := store.ListSchedules(ctx, 10)
	if err != nil || len(schedules) != 1 || schedules[0].Enabled || schedules[0].DisabledBy != "coder" {
		t.Fatalf("schedules=%#v err=%v", schedules, err)
	}
	logs, err := store.ListRunLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].ScheduleID != scheduleID {
		t.Fatalf("logs=%#v err=%v", logs, err)
	}
}
