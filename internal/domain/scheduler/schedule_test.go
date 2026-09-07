package scheduler

import (
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

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
		Trigger:    "manual",
		Status:     "completed",
		StartedAt:  time.Now().UTC(),
	}
	if err := ValidateRunLog(log); err != nil {
		t.Fatalf("ValidateRunLog() error = %v", err)
	}
}
