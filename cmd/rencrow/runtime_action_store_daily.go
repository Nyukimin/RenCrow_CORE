package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
)

const (
	actionStoreRotationDailySourceID     = "runtime.action-store-rotation"
	actionStoreRotationDailyDefinitionID = "maintenance.action_store_rotation"
	actionStoreRotationDailyOwnerRef     = "action.store.jsonl"
)

// registerRuntimeActionStoreRotationDailySource binds rotation metadata to the
// actual local Action store owned by this process.
func registerRuntimeActionStoreRotationDailySource(deps *Dependencies, cfg *config.Config, store *actionpersistence.JSONLStore) {
	if deps == nil {
		return
	}
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: actionStoreRotationDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{
			storeRotationDailyDefinition(
				actionStoreRotationDailySourceID,
				actionStoreRotationDailyDefinitionID,
				"maintenance.action_store_rotation",
				"Action store segment rotation",
				actionStoreRotationDailyOwnerRef,
				"internal.infrastructure.persistence.action.JSONLStore",
				storeRotationResourceRef(actionStoreRotationDailySourceID, actionStoreRotationDailyOwnerRef, "action_store.rotation.enabled", "Action store rotation setting"),
				storeRotationResourceRef(actionStoreRotationDailySourceID, actionStoreRotationDailyOwnerRef, "action_store.rotation.size_valve_bytes", "Action store segment size valve"),
			),
		},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		enabled := cfg != nil && cfg.ActionStore.Rotation.Enabled
		observation := dailySegmentRotationObservation(
			actionStoreRotationDailyDefinitionID,
			actionStoreRotationDailyOwnerRef,
			"action_store.rotation",
			cfg != nil,
			store != nil,
			store != nil,
			enabled,
		)
		return taskcenter.DailySourceSnapshot{
			ObservedAt: time.Now().UTC(), Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{},
		}, nil
	}))
}
