package main

import (
	"context"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type runtimeLivingTaskProjectionReader struct {
	tasks boardAgentWorkTaskOwner
}

func newRuntimeLivingTaskProjectionReader(deps *Dependencies) viewer.LivingHeartbeatActivityReader {
	if deps == nil {
		return runtimeLivingTaskProjectionReader{}
	}
	return runtimeLivingTaskProjectionReader{tasks: deps.taskManager}
}

func (r runtimeLivingTaskProjectionReader) CurrentRunningTaskIDs(ctx context.Context) (map[string][]modulecore.TaskID, error) {
	if r.tasks == nil {
		return nil, fmt.Errorf("canonical Task owner is unavailable")
	}
	result := make(map[string][]modulecore.TaskID, len(agentHeartbeatActorIDs))
	for _, actorID := range agentHeartbeatActorIDs {
		projection, err := readActorTaskProjection(ctx, r.tasks, actorID, domaintask.StatusRunning)
		if err != nil {
			return nil, fmt.Errorf("read running actor Tasks for %s: %w", actorID, err)
		}
		ids := make([]modulecore.TaskID, 0, len(projection.Current))
		for _, task := range projection.Current {
			if task.Assignee == actorID && task.Status == domaintask.StatusRunning {
				ids = append(ids, task.TaskID)
			}
		}
		result[actorID] = ids
	}
	return result, nil
}

func (r runtimeLivingTaskProjectionReader) CurrentActivities(ctx context.Context) (map[string]viewer.LivingHeartbeatCurrentActivity, error) {
	if r.tasks == nil {
		return nil, fmt.Errorf("canonical Task owner is unavailable")
	}
	reader, ok := r.tasks.(interface {
		SnapshotCurrentActivities(context.Context, []string) (map[string]domaintask.CurrentActivitySnapshot, error)
	})
	if !ok {
		return nil, fmt.Errorf("canonical current activity snapshot is unavailable")
	}
	snapshots, err := reader.SnapshotCurrentActivities(ctx, agentHeartbeatActorIDs)
	if err != nil {
		return nil, fmt.Errorf("read canonical current activities: %w", err)
	}
	result := make(map[string]viewer.LivingHeartbeatCurrentActivity, len(agentHeartbeatActorIDs))
	for _, actorID := range agentHeartbeatActorIDs {
		snapshot, found := snapshots[actorID]
		if !found {
			return nil, fmt.Errorf("canonical current activity missing for %s", actorID)
		}
		kind, label, phase, revision, valid := publicCurrentActivity(snapshot)
		if !valid {
			return nil, fmt.Errorf("canonical current activity is invalid for %s", actorID)
		}
		activity := viewer.LivingHeartbeatCurrentActivity{
			Task: snapshot.Task, Kind: kind, Label: label, Phase: phase, Revision: revision,
			State: "running", ActivityKind: "task",
		}
		switch snapshot.Task.Activity.Kind {
		case domaintask.ActivityKindChat:
			activity.ActivityKind = "foreground"
		case domaintask.ActivityKindHobby, domaintask.ActivityKindRest, domaintask.ActivityKindSleep,
			domaintask.ActivityKindMoving, domaintask.ActivityKindEvaluating, domaintask.ActivityKindRoutine:
			activity.ActivityKind = "routine"
		case domaintask.ActivityKindGame:
			activity.ActivityKind = "game"
			if snapshot.Task.Activity.Game != nil {
				activity.GameID = safeGameID(snapshot.Task.Activity.Game.GameID)
			}
		}
		if snapshot.Task.Activity.Kind == domaintask.ActivityKindEvaluating {
			activity.State = "waiting"
		} else if snapshot.Task.Status != domaintask.StatusRunning {
			activity.State = "waiting"
		}
		switch snapshot.Task.Activity.Kind {
		case domaintask.ActivityKindRest:
			activity.RoutineAvailability, activity.OngoingActivity = "available", "rest"
		case domaintask.ActivityKindSleep:
			activity.RoutineAvailability, activity.OngoingActivity = "available", "sleep_window"
		case domaintask.ActivityKindMoving:
			activity.RoutineAvailability, activity.OngoingActivity = "available", "moving_home"
		case domaintask.ActivityKindEvaluating:
			activity.RoutineAvailability, activity.OngoingActivity = "available", "waiting"
		}
		result[actorID] = activity
	}
	return result, nil
}

func safeGameID(label string) string {
	switch label {
	case "nethack", "NetHack":
		return "nethack"
	case "territory_commander", "Territory Commander":
		return "territory_commander"
	case "survival_garden", "Survival Garden":
		return "survival_garden"
	case "herzog_zwei", "Herzog Zwei", "ヘルツォーク・ツヴァイ":
		return "herzog_zwei"
	default:
		return ""
	}
}
