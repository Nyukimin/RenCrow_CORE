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
	if schedule.Target != pronunciationapp.ScheduledTarget || e.inner == nil {
		return "", schedulerapp.ErrExecutorUnavailable
	}
	return e.inner.ExecuteSchedule(ctx, schedule)
}

func buildPronunciationCheckRuntime(cfg *config.Config, deps *Dependencies, agents agentRuntime, coders ...*coderAdapter) {
	registerRuntimePronunciationDailySource(deps, cfg)
	if deps == nil {
		return
	}
	if cfg == nil {
		registerRuntimeTaskMeetingDailySource(deps, nil)
		return
	}
	if deps.taskMeetingOccurrenceExecutor == nil {
		deps.taskMeetingOccurrenceExecutor = newRuntimeTaskMeetingOccurrenceExecutor(cfg, deps, agents, coders...)
	}
	deps.taskMeetingBindingsReady = taskMeetingRuntimeBindingsReady(cfg, agents, coders...)
	registrationID, registrationReady, registrationErr := registerRuntimeTaskMeetingSchedule(
		context.Background(), cfg, deps, agents, coders...,
	)
	deps.taskMeetingScheduleID = registrationID
	if registrationErr != nil {
		log.Printf("[TaskMeeting] owner registration unavailable; meeting schedule will remain inactive")
	}
	settings := cfg.TTS.PronunciationCheck
	var pronunciationExecutor schedulerapp.Executor
	var pronunciationScheduleID modulecore.ScheduleID
	if settings.Enabled && deps.schedulerStore != nil {
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
		candidateExecutor := pronunciationSchedulerExecutor{inner: pronunciationapp.NewScheduledExecutor(service)}
		schedulerService := schedulerapp.NewService(deps.schedulerStore, candidateExecutor)
		scheduleID, err := ensurePronunciationCheckSchedule(context.Background(), deps.schedulerStore, schedulerService, settings.Schedule)
		if err != nil {
			log.Printf("[PronunciationCheck] failed to register CORE task: %v", err)
		} else {
			pronunciationExecutor = candidateExecutor
			pronunciationScheduleID = scheduleID
			log.Printf("[PronunciationCheck] CORE task enabled (schedule_id=%s schedule=%s gpu=%s)", scheduleID, settings.Schedule, settings.GPUMatch)
		}
	}

	router := schedulerTargetRouter{
		pronunciation: pronunciationExecutor,
		taskMeeting:   deps.taskMeetingOccurrenceExecutor,
	}
	schedulerService := schedulerapp.NewService(deps.schedulerStore, router)
	targets := make([]modulecore.ScheduleID, 0, 2)
	if pronunciationScheduleID != "" {
		targets = append(targets, pronunciationScheduleID)
	}
	meetingEnabled, meetingDisabledReason := taskMeetingEffectivelyEnabled(cfg)
	if meetingEnabled && registrationErr == nil && registrationReady && deps.schedulerStore != nil && router.taskMeeting != nil && deps.taskMeetingBindingsReady && deps.taskMeetingScheduleID != "" {
		meetingConfig := cfg.Heartbeat.Routine.DailyReportMeetings
		projectionTiming, timingErr := taskMeetingPendingProjectionTiming(
			context.Background(), cfg, deps, deps.taskMeetingScheduleID, meetingConfig.TaskTiming(),
		)
		if timingErr != nil {
			log.Printf("[TaskMeeting] frozen pending occurrence config is unavailable; no meeting schedule was activated")
			if disableErr := disableTaskMeetingSchedule(context.Background(), deps.schedulerStore, deps.taskMeetingScheduleID); disableErr != nil {
				log.Printf("[TaskMeeting] prior owner schedule could not be disabled safely")
			}
		} else {
			registeredID, err := ensureTaskMeetingSchedule(context.Background(), deps.schedulerStore, schedulerService, deps.taskMeetingScheduleID, projectionTiming)
			if err != nil {
				log.Printf("[TaskMeeting] scheduler owner unavailable; no meeting schedule was activated")
				if disableErr := disableTaskMeetingSchedule(context.Background(), deps.schedulerStore, deps.taskMeetingScheduleID); disableErr != nil {
					log.Printf("[TaskMeeting] prior owner schedule could not be disabled safely")
				}
			} else {
				targets = append(targets, registeredID)
			}
		}
	} else if deps.schedulerStore != nil {
		if err := disableTaskMeetingSchedule(context.Background(), deps.schedulerStore, deps.taskMeetingScheduleID); err != nil {
			log.Printf("[TaskMeeting] prior owner schedule could not be disabled safely")
		} else if deps.taskMeetingScheduleID != "" && (meetingDisabledReason != "feature_disabled" || cfg.Heartbeat.Routine.DailyReportMeetings.Enabled) {
			log.Printf("[TaskMeeting] meeting schedule remains disabled by owner configuration/runtime availability")
		}
	}
	protectedIDs, err := protectedTaskMeetingScheduleIDs(context.Background(), deps.schedulerStore)
	if err != nil {
		protectedIDs = nil
		log.Printf("[Scheduler] owner schedule policy scan unavailable; generic scheduler reads will fail closed")
	}
	if deps.taskMeetingScheduleID != "" {
		protectedIDs = append(protectedIDs, string(deps.taskMeetingScheduleID))
	}
	deps.schedulerStatus = viewer.HandleSchedulerWithExecutorAndProtectedTargets(
		deps.schedulerStore, router, []string{taskMeetingScheduledTarget}, protectedIDs,
	)
	registerRuntimeTaskMeetingDailySource(deps, cfg)
	if len(targets) > 0 {
		cancel, done := startSharedSchedulerLoop(context.Background(), schedulerService, targets)
		deps.schedulerRuntimeCancel = cancel
		deps.schedulerRuntimeDone = done
		if pronunciationScheduleID != "" {
			deps.pronunciationCheckCancel = cancel
		}
	}
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
