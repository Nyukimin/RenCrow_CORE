package scheduler

import (
	"context"
	"errors"
	"testing"
	"time"

	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
)

type memoryStore struct {
	jobs []domainscheduler.Job
	logs []domainscheduler.RunLog
}

func (s *memoryStore) ListJobs(context.Context, int) ([]domainscheduler.Job, error) {
	return append([]domainscheduler.Job(nil), s.jobs...), nil
}

func (s *memoryStore) SaveJob(_ context.Context, job domainscheduler.Job) error {
	for i := range s.jobs {
		if s.jobs[i].JobID == job.JobID {
			s.jobs[i] = job
			return nil
		}
	}
	s.jobs = append(s.jobs, job)
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
	jobID string
}

type deferredExecutor struct{}

func (deferredExecutor) ExecuteScheduledJob(context.Context, domainscheduler.Job) (string, error) {
	return "GPU is busy", NewDeferredError(5*time.Minute, errors.New("gpu busy"))
}

func (e *recordingExecutor) ExecuteScheduledJob(_ context.Context, job domainscheduler.Job) (string, error) {
	e.jobID = job.JobID
	return "executed " + job.Name, nil
}

func TestServiceDefersJobWithoutAdvancingToNextSchedule(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	store := &memoryStore{jobs: []domainscheduler.Job{{
		JobID: "tts_pronunciation_daily", Name: "TTS pronunciation daily",
		Schedule: "cron 30 19 * * *", Target: "tts_pronunciation_check", Enabled: true,
		CreatedAt: now.Add(-24 * time.Hour), UpdatedAt: now.Add(-24 * time.Hour), NextRunAt: now,
	}}}
	svc := NewService(store, deferredExecutor{}).WithNow(func() time.Time { return now })
	log, err := svc.RunJob(context.Background(), "tts_pronunciation_daily", "due")
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if log.Status != "deferred" || log.Error != "" {
		t.Fatalf("log = %+v", log)
	}
	if want := now.Add(5 * time.Minute); !store.jobs[0].NextRunAt.Equal(want) {
		t.Fatalf("NextRunAt=%v want=%v", store.jobs[0].NextRunAt, want)
	}
}

func TestServiceRunsOnlyRequestedDueJob(t *testing.T) {
	now := time.Date(2026, 7, 19, 19, 30, 0, 0, time.UTC)
	store := &memoryStore{jobs: []domainscheduler.Job{
		{JobID: "tts_pronunciation_daily", Name: "TTS pronunciation daily", Schedule: "every 24h", Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now},
		{JobID: "unrelated", Name: "Unrelated", Schedule: "every 24h", Enabled: true, CreatedAt: now.Add(-time.Hour), UpdatedAt: now.Add(-time.Hour), NextRunAt: now},
	}}
	executor := &recordingExecutor{}
	svc := NewService(store, executor).WithNow(func() time.Time { return now })
	log, ran, err := svc.RunDueJob(context.Background(), "tts_pronunciation_daily")
	if err != nil || !ran || log.JobID != "tts_pronunciation_daily" || executor.jobID != "tts_pronunciation_daily" {
		t.Fatalf("log=%+v ran=%v executor=%+v err=%v", log, ran, executor, err)
	}
	if len(store.logs) != 1 {
		t.Fatalf("logs=%+v", store.logs)
	}
}

func TestServiceCreateDueRunAndDisableJob(t *testing.T) {
	now := time.Date(2026, 6, 22, 7, 0, 0, 0, time.UTC)
	store := &memoryStore{}
	executor := &recordingExecutor{}
	svc := NewService(store, executor).WithNow(func() time.Time { return now })
	ctx := context.Background()

	job, err := svc.CreateJob(ctx, domainscheduler.Job{
		JobID:    "sched_backlog",
		Name:     "Backlog heartbeat",
		Schedule: "every 10m",
		Target:   "backlog",
		Prompt:   "process backlog",
	})
	if err != nil {
		t.Fatalf("CreateJob() error = %v", err)
	}
	if !job.Enabled || !job.NextRunAt.Equal(now.Add(10*time.Minute)) {
		t.Fatalf("job=%#v", job)
	}

	svc.WithNow(func() time.Time { return now.Add(11 * time.Minute) })
	due, err := svc.DueJobs(ctx, 10)
	if err != nil || len(due) != 1 || due[0].Job.JobID != "sched_backlog" {
		t.Fatalf("due=%#v err=%v", due, err)
	}

	log, err := svc.RunJob(ctx, "sched_backlog", "manual")
	if err != nil {
		t.Fatalf("RunJob() error = %v", err)
	}
	if log.Status != "completed" || executor.jobID != "sched_backlog" || len(store.logs) != 1 {
		t.Fatalf("log=%#v executor=%#v logs=%#v", log, executor, store.logs)
	}
	if !store.jobs[0].LastRunAt.Equal(now.Add(11*time.Minute)) || !store.jobs[0].NextRunAt.Equal(now.Add(21*time.Minute)) {
		t.Fatalf("job after run=%#v", store.jobs[0])
	}

	disabled, err := svc.DisableJob(ctx, "sched_backlog", "coder")
	if err != nil {
		t.Fatalf("DisableJob() error = %v", err)
	}
	if disabled.Enabled || disabled.DisabledBy != "coder" {
		t.Fatalf("disabled=%#v", disabled)
	}
}
