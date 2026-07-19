package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	pronunciationtool "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/pronunciationtool"
)

const pronunciationCheckJobID = "tts_pronunciation_daily"

type pronunciationSchedulerExecutor struct {
	inner schedulerapp.Executor
}

func (e pronunciationSchedulerExecutor) ExecuteScheduledJob(ctx context.Context, job domainscheduler.Job) (string, error) {
	if job.Target != pronunciationapp.ScheduledTarget {
		return "scheduler run recorded without an executor", nil
	}
	return e.inner.ExecuteScheduledJob(ctx, job)
}

func buildPronunciationCheckRuntime(cfg *config.Config, deps *Dependencies) {
	settings := cfg.TTS.PronunciationCheck
	if !settings.Enabled || deps == nil || deps.schedulerStore == nil {
		return
	}
	timeout := time.Duration(settings.TimeoutMinutes) * time.Minute
	toolClient := pronunciationtool.NewClient(
		settings.ToolBaseURL,
		&http.Client{Timeout: timeout},
		5*time.Second,
	)
	service := pronunciationapp.NewService(toolClient, toolClient, pronunciationapp.Config{
		GPUMatch: settings.GPUMatch, MinFreeMB: settings.MinFreeMB,
		MaxUtilizationPercent: settings.MaxUtilizationPercent,
		IdleSamples:           settings.IdleSamples,
		SampleInterval:        time.Duration(settings.SampleIntervalSeconds) * time.Second,
		RetryAfter:            time.Duration(settings.RetryIntervalSeconds) * time.Second,
	})
	executor := pronunciationSchedulerExecutor{inner: pronunciationapp.NewScheduledExecutor(service)}
	schedulerService := schedulerapp.NewService(deps.schedulerStore, executor)
	if err := ensurePronunciationCheckJob(context.Background(), deps.schedulerStore, schedulerService, settings.Schedule); err != nil {
		log.Printf("[PronunciationCheck] failed to register CORE task: %v", err)
		return
	}
	deps.schedulerStatus = viewer.HandleSchedulerWithExecutor(deps.schedulerStore, executor)
	ctx, cancel := context.WithCancel(context.Background())
	deps.pronunciationCheckCancel = cancel
	go runPronunciationCheckScheduler(ctx, schedulerService)
	log.Printf("[PronunciationCheck] CORE task enabled (job_id=%s schedule=%s gpu=%s)", pronunciationCheckJobID, settings.Schedule, settings.GPUMatch)
}

func ensurePronunciationCheckJob(ctx context.Context, store viewer.SchedulerStore, service *schedulerapp.Service, schedule string) error {
	jobs, err := store.ListJobs(ctx, 1000)
	if err != nil {
		return err
	}
	for _, job := range jobs {
		if job.JobID != pronunciationCheckJobID {
			continue
		}
		if job.Schedule == schedule && job.Target == pronunciationapp.ScheduledTarget {
			return nil
		}
		job.Schedule = schedule
		job.Target = pronunciationapp.ScheduledTarget
		job.Name = "TTS pronunciation daily check"
		job.Description = "Run one-sentence Irodori pronunciation checks after RTX 5060 Ti becomes idle"
		job.UpdatedAt = time.Now().UTC()
		if job.Enabled {
			next, nextErr := domainscheduler.NextRunAfter(schedule, job.UpdatedAt)
			if nextErr != nil {
				return nextErr
			}
			job.NextRunAt = next
		}
		return store.SaveJob(ctx, job)
	}
	_, err = service.CreateJob(ctx, domainscheduler.Job{
		JobID:       pronunciationCheckJobID,
		Name:        "TTS pronunciation daily check",
		Schedule:    schedule,
		Target:      pronunciationapp.ScheduledTarget,
		Description: "Run one-sentence Irodori pronunciation checks after RTX 5060 Ti becomes idle",
	})
	return err
}

func runPronunciationCheckScheduler(ctx context.Context, service *schedulerapp.Service) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if logEntry, ran, err := service.RunDueJob(ctx, pronunciationCheckJobID); err != nil {
			log.Printf("[PronunciationCheck] CORE task failed: %v", err)
		} else if ran {
			log.Printf("[PronunciationCheck] CORE task status=%s summary=%s", logEntry.Status, logEntry.Summary)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
