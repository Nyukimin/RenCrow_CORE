package main

import (
	"net/http"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
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
}
