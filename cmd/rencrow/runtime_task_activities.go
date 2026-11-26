package main

import (
	"net/http"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
)

func registerRuntimeTaskActivitiesRoute(mux *http.ServeMux, cfg *config.Config, deps *Dependencies) {
	if mux == nil {
		return
	}
	var reader viewer.TaskActivitiesReader
	var goals viewer.TaskActivitiesGoalReader
	var publications viewer.TaskActivitiesPublicationReader
	var userID string
	if deps != nil {
		if deps.taskManager != nil {
			if candidate, ok := any(deps.taskManager).(viewer.TaskActivitiesReader); ok {
				reader = candidate
			}
		}
		if deps.taskCenter != nil {
			if candidate, ok := any(deps.taskCenter).(viewer.TaskActivitiesGoalReader); ok {
				goals = candidate
			}
		}
		if deps.sessionVisibility != nil {
			if candidate, ok := any(deps.sessionVisibility.reader).(viewer.TaskActivitiesPublicationReader); ok {
				publications = candidate
			}
		}
		userID = deps.taskCenterUserID
	}
	_, token := viewerEventsCMDOwnerCredentials(cfg)
	mux.HandleFunc("GET /viewer/board/task-activities", viewer.NewTaskActivitiesHandler(reader, goals, userID, token, publications))
	mux.HandleFunc("GET /viewer/board/system-jobs", viewer.NewSystemJobsHandler(newSystemJobsReader(), userID, token))
	var taskOverviewCenter *taskcenter.Center
	if deps != nil {
		taskOverviewCenter = deps.taskCenterCenter
	}
	mux.HandleFunc("GET /viewer/board/task-overview", viewer.NewTaskOverviewHandler(taskOverviewCenter, taskOverviewCenter, userID, token))
	var schedulerStore viewer.SchedulerStore
	var heartbeatStore viewer.RegisteredScheduleHeartbeatReader
	var protectedScheduleIDs []string
	if deps != nil {
		schedulerStore = deps.schedulerStore
		heartbeatStore = deps.workstreamStore
		if deps.taskMeetingScheduleID != "" {
			protectedScheduleIDs = []string{string(deps.taskMeetingScheduleID)}
		}
	}
	mux.HandleFunc("GET /viewer/board/registered-schedules", viewer.NewRegisteredSchedulesHandler(
		schedulerStore, heartbeatStore, newRegisteredCronScheduleReader(), userID, token,
		[]string{taskMeetingScheduledTarget, pronunciationapp.ScheduledTarget}, protectedScheduleIDs,
	))
}
