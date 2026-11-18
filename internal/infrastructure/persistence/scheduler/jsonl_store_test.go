package scheduler

import (
	"context"
	"strings"
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
		Timing:     &modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}},
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
		TaskID:      modulecore.NewTaskID(), // historical field remains readable
		Trigger:     "due",                  // legacy due records may lack an occurrence key
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now.Add(time.Second),
		Summary:     "ok",
	}); err != nil {
		t.Fatalf("SaveRunLog() error = %v", err)
	}
	schedules, err := store.ListSchedules(ctx, 10)
	if err != nil || len(schedules) != 1 || schedules[0].Enabled || schedules[0].DisabledBy != "coder" || schedules[0].Timing == nil || schedules[0].Timing.Recurrence == nil {
		t.Fatalf("schedules=%#v err=%v", schedules, err)
	}
	logs, err := store.ListRunLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].ScheduleID != scheduleID {
		t.Fatalf("logs=%#v err=%v", logs, err)
	}
}

func TestJSONLStoreFiltersLatestRowsBeforeVisibleLimitAndFindsExactIdentity(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 11, 18, 12, 0, 0, 0, time.UTC)
	publicIDs := []modulecore.ScheduleID{modulecore.NewScheduleID(), modulecore.NewScheduleID()}
	for index, scheduleID := range publicIDs {
		if err := store.SaveSchedule(ctx, domainscheduler.Schedule{
			ScheduleID: scheduleID, Name: "public", Schedule: "every 1h", Target: "backlog", Enabled: true,
			CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Duration(index) * time.Second), NextRunAt: now,
			Timing: &modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	privateID := modulecore.NewScheduleID()
	if err := store.SaveSchedule(ctx, domainscheduler.Schedule{
		ScheduleID: privateID, Name: "private", Schedule: "every 1h", Target: "taskmeeting.daily_report", Enabled: true,
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(time.Hour), NextRunAt: now,
		Timing: &modulecore.TaskTiming{Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceNone}},
	}); err != nil {
		t.Fatal(err)
	}

	visible, hiddenIDs, err := store.ListSchedulesFiltered(ctx, 2, func(schedule domainscheduler.Schedule) bool {
		return strings.TrimSpace(schedule.Target) != "taskmeeting.daily_report"
	})
	if err != nil || len(visible) != 2 || visible[0].ScheduleID != publicIDs[1] || visible[1].ScheduleID != publicIDs[0] {
		t.Fatalf("filtered schedules=%+v err=%v", visible, err)
	}
	if len(hiddenIDs) != 1 || hiddenIDs[0] != string(privateID) {
		t.Fatalf("excluded schedule identities=%v, want private %s", hiddenIDs, privateID)
	}
	found, ok, err := store.FindScheduleByID(ctx, string(privateID))
	if err != nil || !ok || found.ScheduleID != privateID || found.Target != "taskmeeting.daily_report" {
		t.Fatalf("exact private lookup schedule=%+v found=%t err=%v", found, ok, err)
	}
	if _, ok, err := store.FindScheduleByID(ctx, string(modulecore.NewScheduleID())); err != nil || ok {
		t.Fatalf("missing exact lookup found=%t err=%v", ok, err)
	}
}
