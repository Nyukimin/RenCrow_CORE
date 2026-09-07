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
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type pronunciationSchedulerExecutor struct {
	inner schedulerapp.Executor
}

func (e pronunciationSchedulerExecutor) ExecuteSchedule(ctx context.Context, schedule domainscheduler.Schedule) (string, error) {
	if schedule.Target != pronunciationapp.ScheduledTarget {
		return "scheduler run recorded without an executor", nil
	}
	return e.inner.ExecuteSchedule(ctx, schedule)
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
	scheduleID, err := ensurePronunciationCheckSchedule(context.Background(), deps.schedulerStore, schedulerService, settings.Schedule)
	if err != nil {
		log.Printf("[PronunciationCheck] failed to register CORE task: %v", err)
		return
	}
	deps.schedulerStatus = viewer.HandleSchedulerWithExecutor(deps.schedulerStore, executor)
	ctx, cancel := context.WithCancel(context.Background())
	deps.pronunciationCheckCancel = cancel
	go runPronunciationCheckScheduler(ctx, schedulerService, scheduleID)
	log.Printf("[PronunciationCheck] CORE task enabled (schedule_id=%s schedule=%s gpu=%s)", scheduleID, settings.Schedule, settings.GPUMatch)
}

func ensurePronunciationCheckSchedule(ctx context.Context, store viewer.SchedulerStore, service *schedulerapp.Service, scheduleExpr string) (modulecore.ScheduleID, error) {
	schedules, err := store.ListSchedules(ctx, 1000)
	if err != nil {
		return "", err
	}
	for _, schedule := range schedules {
		if schedule.Target != pronunciationapp.ScheduledTarget {
			continue
		}
		if schedule.Schedule == scheduleExpr {
			return schedule.ScheduleID, nil
		}
		schedule.Schedule = scheduleExpr
		schedule.Name = "TTS pronunciation daily check"
		schedule.Description = "Run one-sentence TTS pronunciation checks after the configured GPU becomes idle"
		schedule.UpdatedAt = time.Now().UTC()
		if schedule.Enabled {
			next, nextErr := domainscheduler.NextRunAfter(scheduleExpr, schedule.UpdatedAt)
			if nextErr != nil {
				return "", nextErr
			}
			schedule.NextRunAt = next
		}
		if err := store.SaveSchedule(ctx, schedule); err != nil {
			return "", err
		}
		return schedule.ScheduleID, nil
	}
	created, err := service.CreateSchedule(ctx, domainscheduler.Schedule{
		Name:        "TTS pronunciation daily check",
		Schedule:    scheduleExpr,
		Target:      pronunciationapp.ScheduledTarget,
		Description: "Run one-sentence TTS pronunciation checks after the configured GPU becomes idle",
	})
	if err != nil {
		return "", err
	}
	return created.ScheduleID, nil
}

func runPronunciationCheckScheduler(ctx context.Context, service *schedulerapp.Service, scheduleID modulecore.ScheduleID) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		if logEntry, ran, err := service.RunDueSchedule(ctx, string(scheduleID)); err != nil {
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
