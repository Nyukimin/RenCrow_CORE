package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	resilienceDailySourceID     = "runtime.resilience-status"
	resilienceDailyDefinitionID = "ops.resilience_status"
	resilienceDailyOwnerRef     = "cmd.rencrow.resilience"
)

// registerRuntimeResilienceDailySource exposes only the existence of the
// manual status owner. Incident details are private operational data and this
// source deliberately does not call Store.List.
func registerRuntimeResilienceDailySource(deps *Dependencies) {
	if deps == nil {
		return
	}
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: resilienceDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: resilienceDailyDefinitionID,
			KindID:       "ops.resilience_status",
			Label:        "Resilience incident status owner",
			OwnerRef:     resilienceDailyOwnerRef,
			ResourceType: "manual_incident_status_cli",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(
				taskcenter.DailyImplementationImplemented,
				"internal.application.resilience.Store.List",
				"manual_status_read_has_no_daily_scheduler",
			),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(resilienceDailySourceID, resilienceDailyOwnerRef, "manual_read_operation", "rencrow.resilience.status", "", tool.DataScopePublic, "Manual resilience status command"),
				runtimeDailyResourceRef(resilienceDailySourceID, resilienceDailyOwnerRef, "readonly_port", "internal.application.resilience.Store.List", "", tool.DataScopePublic, "Incident list read port"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		const sourceRef = "internal.application.resilience.Store.List"
		timing := runtimeDailyTimingUnsupported(sourceRef, "manual_status_owner_has_no_daily_schedule")
		timing.Timezone = runtimeDailyNone[string](sourceRef, "no_schedule_timezone")
		timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "manual_status_read")
		timing.NotBefore = runtimeDailyNone[time.Time](sourceRef, "no_not_before_gate")
		timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](sourceRef, "no_time_window_gate")
		timing.DeadlineAt = runtimeDailyNone[time.Time](sourceRef, "no_deadline_gate")
		timing.Recurrence = runtimeDailyNone[modulecore.TaskRecurrence](sourceRef, "manual_cli_only")
		observation := taskcenter.DailyObservation{
			DefinitionID:    resilienceDailyDefinitionID,
			Configured:      runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "incident_store_not_queried"),
			Effective:       runtimeDailyAxis(taskcenter.DailyEffectiveUnsupported, sourceRef, "no_daily_resilience_scheduler"),
			Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "incident_store_not_read_by_daily_projection"),
			Timing:          timing,
			NextRunAt:       runtimeDailyUnsupported[time.Time](sourceRef, "manual_status_has_no_next_run"),
			TargetCount:     runtimeDailyUnsupported[int](sourceRef, "manual_status_has_no_targets"),
			NativeRefs:      []taskcenter.DailyResourceRef{},
			NativeRefCount:  runtimeDailyUnknown[int](sourceRef, "incident_details_not_read"),
			LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, "incident_details_not_read"),
			Occurrences:     []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnknown[int](sourceRef, "no_daily_run_receipt_port"),
			Issues:          []taskcenter.DailyIssue{},
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: time.Now().UTC(), Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}
