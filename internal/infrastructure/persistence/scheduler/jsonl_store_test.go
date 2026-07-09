package scheduler

import (
	"context"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

func TestJSONLStorePersistsLatestJobsAndRunAudit(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 6, 22, 7, 0, 0, 0, time.UTC)
	job := domainscheduler.Job{
		JobID:     "sched_backlog",
		Name:      "Backlog heartbeat",
		Schedule:  "every 15m",
		Prompt:    "process backlog",
		Target:    "backlog",
		Enabled:   true,
		CreatedAt: now,
		UpdatedAt: now,
		NextRunAt: now.Add(15 * time.Minute),
	}
	if err := store.SaveJob(ctx, job); err != nil {
		t.Fatalf("SaveJob() error = %v", err)
	}
	job.Enabled = false
	job.UpdatedAt = now.Add(time.Minute)
	job.DisabledAt = now.Add(time.Minute)
	job.DisabledBy = "coder"
	if err := store.SaveJob(ctx, job); err != nil {
		t.Fatalf("SaveJob(disabled) error = %v", err)
	}
	if err := store.SaveRunLog(ctx, domainscheduler.RunLog{
		RunID:       "schedrun_1",
		JobID:       job.JobID,
		Trigger:     "manual",
		Status:      "completed",
		StartedAt:   now,
		CompletedAt: now.Add(time.Second),
		Summary:     "ok",
	}); err != nil {
		t.Fatalf("SaveRunLog() error = %v", err)
	}
	jobs, err := store.ListJobs(ctx, 10)
	if err != nil || len(jobs) != 1 || jobs[0].Enabled || jobs[0].DisabledBy != "coder" {
		t.Fatalf("jobs=%#v err=%v", jobs, err)
	}
	logs, err := store.ListRunLogs(ctx, 10)
	if err != nil || len(logs) != 1 || logs[0].RunID != "schedrun_1" {
		t.Fatalf("logs=%#v err=%v", logs, err)
	}
}
