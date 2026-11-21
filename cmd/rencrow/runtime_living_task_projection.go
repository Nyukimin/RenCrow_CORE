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

func newRuntimeLivingTaskProjectionReader(deps *Dependencies) viewer.LivingHeartbeatCurrentTaskReader {
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
