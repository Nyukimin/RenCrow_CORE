package main

import (
	"context"
	"testing"

	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
)

func TestEnsurePronunciationCheckScheduleRegistersSingleCORETask(t *testing.T) {
	store := schedulerpersistence.NewJSONLStore(t.TempDir())
	service := schedulerapp.NewService(store, nil)
	var firstID string
	for range 2 {
		scheduleID, err := ensurePronunciationCheckSchedule(context.Background(), store, service, "every 24h")
		if err != nil {
			t.Fatalf("ensurePronunciationCheckSchedule() error = %v", err)
		}
		if firstID == "" {
			firstID = string(scheduleID)
		} else if string(scheduleID) != firstID {
			t.Fatalf("expected stable schedule_id, got %s then %s", firstID, scheduleID)
		}
	}
	schedules, err := store.ListSchedules(context.Background(), 10)
	if err != nil {
		t.Fatalf("ListSchedules() error = %v", err)
	}
	if len(schedules) != 1 || schedules[0].Target != pronunciationapp.ScheduledTarget || !schedules[0].Enabled {
		t.Fatalf("schedules = %+v", schedules)
	}
	if err := schedules[0].ScheduleID.Validate(); err != nil {
		t.Fatalf("ScheduleID.Validate() error = %v", err)
	}
}

func TestPronunciationSchedulerExecutorPreservesUnrelatedSchedules(t *testing.T) {
	summary, err := (pronunciationSchedulerExecutor{}).ExecuteSchedule(context.Background(), domainscheduler.Schedule{Target: "unrelated"})
	if err != nil || summary != "scheduler run recorded without an executor" {
		t.Fatalf("summary=%q err=%v", summary, err)
	}
}
