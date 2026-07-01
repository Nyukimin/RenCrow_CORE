package scheduler

import (
	"context"
	"fmt"
	"strings"
	"time"

	domainscheduler "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/scheduler"
)

type Store interface {
	ListJobs(ctx context.Context, limit int) ([]domainscheduler.Job, error)
	SaveJob(ctx context.Context, job domainscheduler.Job) error
	SaveRunLog(ctx context.Context, log domainscheduler.RunLog) error
	ListRunLogs(ctx context.Context, limit int) ([]domainscheduler.RunLog, error)
}

type Executor interface {
	ExecuteScheduledJob(ctx context.Context, job domainscheduler.Job) (string, error)
}

type Service struct {
	store    Store
	executor Executor
	now      func() time.Time
}

func NewService(store Store, executor Executor) *Service {
	return &Service{store: store, executor: executor, now: func() time.Time { return time.Now().UTC() }}
}

func (s *Service) WithNow(now func() time.Time) *Service {
	if now != nil {
		s.now = now
	}
	return s
}

func (s *Service) CreateJob(ctx context.Context, job domainscheduler.Job) (domainscheduler.Job, error) {
	if s == nil || s.store == nil {
		return domainscheduler.Job{}, fmt.Errorf("scheduler store unavailable")
	}
	now := s.now().UTC()
	if job.CreatedAt.IsZero() {
		job.CreatedAt = now
	}
	job.UpdatedAt = now
	job.Enabled = true
	next, err := domainscheduler.NextRunAfter(job.Schedule, now)
	if err != nil {
		return domainscheduler.Job{}, err
	}
	job.NextRunAt = next
	if err := s.store.SaveJob(ctx, job); err != nil {
		return domainscheduler.Job{}, err
	}
	return job, nil
}

func (s *Service) DueJobs(ctx context.Context, limit int) ([]domainscheduler.DueJob, error) {
	if s == nil || s.store == nil {
		return nil, fmt.Errorf("scheduler store unavailable")
	}
	jobs, err := s.store.ListJobs(ctx, limit)
	if err != nil {
		return nil, err
	}
	now := s.now().UTC()
	out := make([]domainscheduler.DueJob, 0)
	for _, job := range jobs {
		if !job.Enabled || job.NextRunAt.IsZero() || job.NextRunAt.After(now) {
			continue
		}
		out = append(out, domainscheduler.DueJob{Job: job, Scheduled: job.NextRunAt})
	}
	return out, nil
}

func (s *Service) RunJob(ctx context.Context, jobID string, trigger string) (domainscheduler.RunLog, error) {
	if s == nil || s.store == nil {
		return domainscheduler.RunLog{}, fmt.Errorf("scheduler store unavailable")
	}
	job, err := s.findJob(ctx, jobID)
	if err != nil {
		return domainscheduler.RunLog{}, err
	}
	now := s.now().UTC()
	log := domainscheduler.RunLog{
		RunID:     "schedrun_" + job.JobID + "_" + now.Format("20060102150405.000000000"),
		JobID:     job.JobID,
		Trigger:   firstNonEmpty(trigger, "manual"),
		Status:    "completed",
		StartedAt: now,
	}
	if s.executor != nil {
		summary, execErr := s.executor.ExecuteScheduledJob(ctx, job)
		log.Summary = strings.TrimSpace(summary)
		if execErr != nil {
			log.Status = "failed"
			log.Error = execErr.Error()
		}
	} else {
		log.Summary = "scheduler run recorded without executor"
	}
	log.CompletedAt = s.now().UTC()
	job.LastRunAt = log.StartedAt
	if next, err := domainscheduler.NextRunAfter(job.Schedule, log.StartedAt); err == nil {
		job.NextRunAt = next
	} else {
		job.Enabled = false
		job.DisabledAt = log.CompletedAt
		job.DisabledBy = "scheduler"
	}
	job.UpdatedAt = log.CompletedAt
	if err := s.store.SaveRunLog(ctx, log); err != nil {
		return domainscheduler.RunLog{}, err
	}
	if err := s.store.SaveJob(ctx, job); err != nil {
		return domainscheduler.RunLog{}, err
	}
	return log, nil
}

func (s *Service) DisableJob(ctx context.Context, jobID string, disabledBy string) (domainscheduler.Job, error) {
	if s == nil || s.store == nil {
		return domainscheduler.Job{}, fmt.Errorf("scheduler store unavailable")
	}
	job, err := s.findJob(ctx, jobID)
	if err != nil {
		return domainscheduler.Job{}, err
	}
	now := s.now().UTC()
	job.Enabled = false
	job.DisabledAt = now
	job.DisabledBy = firstNonEmpty(disabledBy, "system")
	job.UpdatedAt = now
	if err := s.store.SaveJob(ctx, job); err != nil {
		return domainscheduler.Job{}, err
	}
	return job, nil
}

func (s *Service) findJob(ctx context.Context, jobID string) (domainscheduler.Job, error) {
	jobID = strings.TrimSpace(jobID)
	if jobID == "" {
		return domainscheduler.Job{}, fmt.Errorf("job_id is required")
	}
	jobs, err := s.store.ListJobs(ctx, 1000)
	if err != nil {
		return domainscheduler.Job{}, err
	}
	for _, job := range jobs {
		if job.JobID == jobID {
			return job, nil
		}
	}
	return domainscheduler.Job{}, fmt.Errorf("scheduler job not found: %s", jobID)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
