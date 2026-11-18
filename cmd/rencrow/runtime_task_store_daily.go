package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

const (
	taskStoreRotationDailySourceID     = "runtime.task-store-rotation"
	taskStoreRotationDailyDefinitionID = "maintenance.task_store_rotation"
	taskStoreRotationDailyOwnerRef     = "task.store.jsonl"
)

// registerRuntimeTaskStoreRotationDailySource binds Task segment metadata to
// the actual store selected by the canonical Task owner.
func registerRuntimeTaskStoreRotationDailySource(deps *Dependencies, cfg *config.Config, store domaintask.Store) {
	if deps == nil {
		return
	}
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: taskStoreRotationDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{
			storeRotationDailyDefinition(
				taskStoreRotationDailySourceID,
				taskStoreRotationDailyDefinitionID,
				"maintenance.task_store_rotation",
				"Task store segment rotation",
				taskStoreRotationDailyOwnerRef,
				"internal.infrastructure.persistence.task.JSONLStore",
				storeRotationResourceRef(taskStoreRotationDailySourceID, taskStoreRotationDailyOwnerRef, "task_store.rotation.enabled", "Task store rotation setting"),
				storeRotationResourceRef(taskStoreRotationDailySourceID, taskStoreRotationDailyOwnerRef, "task_store.rotation.size_valve_bytes", "Task store segment size valve"),
			),
		},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		_, localStore := store.(*taskpersistence.JSONLStore)
		available := store != nil
		enabled := cfg != nil && cfg.TaskStore.Rotation.Enabled
		observation := dailySegmentRotationObservation(
			taskStoreRotationDailyDefinitionID,
			taskStoreRotationDailyOwnerRef,
			"task_store.rotation",
			cfg != nil,
			available,
			localStore,
			enabled,
		)
		return taskcenter.DailySourceSnapshot{
			ObservedAt: time.Now().UTC(), Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{},
		}, nil
	}))
}
