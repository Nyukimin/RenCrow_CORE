package scheduler

import (
	"testing"
	"time"
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

func TestValidateJobRejectsElapsedOneShot(t *testing.T) {
	job := Job{
		JobID:     "sched_1",
		Name:      "old one-shot",
		Schedule:  "at 2000-01-01T00:00:00Z",
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		UpdatedAt: time.Now().UTC(),
	}
	if err := ValidateJob(job); err == nil {
		t.Fatal("expected elapsed one-shot to be rejected")
	}
}

func TestValidateJobAllowsDisabledElapsedOneShot(t *testing.T) {
	job := Job{
		JobID:      "sched_1",
		Name:       "completed one-shot",
		Schedule:   "at 2000-01-01T00:00:00Z",
		Enabled:    false,
		CreatedAt:  time.Now().UTC(),
		UpdatedAt:  time.Now().UTC(),
		DisabledAt: time.Now().UTC(),
		DisabledBy: "scheduler",
	}
	if err := ValidateJob(job); err != nil {
		t.Fatalf("disabled elapsed one-shot should be valid: %v", err)
	}
}
