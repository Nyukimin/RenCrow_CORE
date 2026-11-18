package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	logMaintenanceDailySourceID     = "runtime.logging-rotation"
	logMaintenanceDailyDefinitionID = "logging.size_rotation"
	logMaintenanceDailyOwnerRef     = "internal.infrastructure.logging.RotatingWriter"
)

// registerRuntimeLogMaintenanceDailySource records the writer's size-triggered
// rotation mechanism. It does not inspect log files, paths, or raw-log values.
func registerRuntimeLogMaintenanceDailySource(deps *Dependencies) {
	if deps == nil {
		return
	}
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: logMaintenanceDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: logMaintenanceDailyDefinitionID,
			KindID:       "maintenance.log_rotation",
			Label:        "Size-triggered log rotation",
			OwnerRef:     logMaintenanceDailyOwnerRef,
			ResourceType: "size_triggered_log_rotation",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(
				taskcenter.DailyImplementationImplemented,
				logMaintenanceDailyOwnerRef+".Write",
				"rotation_is_size_triggered",
			),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(logMaintenanceDailySourceID, logMaintenanceDailyOwnerRef, "runtime_mechanism", logMaintenanceDailyOwnerRef+".Write", "", tool.DataScopePublic, "Rotating writer threshold behavior"),
				runtimeDailyResourceRef(logMaintenanceDailySourceID, logMaintenanceDailyOwnerRef, "runtime_mechanism", "internal.infrastructure.llm.middleware.openRawLogSink", "", tool.DataScopePublic, "Raw log sink owner"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		const sourceRef = logMaintenanceDailyOwnerRef
		timing := runtimeDailyTimingUnsupported(sourceRef, "size_threshold_is_not_a_daily_schedule")
		timing.Timezone = runtimeDailyNone[string](sourceRef, "no_time_zone_gate")
		timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "write_threshold_trigger")
		timing.NotBefore = runtimeDailyNone[time.Time](sourceRef, "no_time_gate")
		timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](sourceRef, "no_time_window_gate")
		timing.DeadlineAt = runtimeDailyNone[time.Time](sourceRef, "no_deadline_gate")
		timing.Recurrence = runtimeDailyNone[modulecore.TaskRecurrence](sourceRef, "event_driven_size_threshold")
		observation := taskcenter.DailyObservation{
			DefinitionID:    logMaintenanceDailyDefinitionID,
			Configured:      runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "sink_configuration_not_exposed"),
			Effective:       runtimeDailyAxis(taskcenter.DailyEffectiveUnsupported, sourceRef, "not_a_daily_scheduled_owner"),
			Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "writer_status_not_exposed"),
			Timing:          timing,
			NextRunAt:       runtimeDailyUnsupported[time.Time](sourceRef, "write_time_not_predictable"),
			TargetCount:     runtimeDailyUnsupported[int](sourceRef, "rotation_has_no_task_targets"),
			NativeRefs:      []taskcenter.DailyResourceRef{},
			NativeRefCount:  runtimeDailyUnknown[int](sourceRef, "rotation_history_not_exposed"),
			LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, "rotation_receipt_not_exposed"),
			Occurrences:     []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnsupported[int](sourceRef, "write_rotations_are_not_daily_task_occurrences"),
			Issues:          []taskcenter.DailyIssue{},
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: time.Now().UTC(), Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}
