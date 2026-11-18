package main

import (
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func dailySegmentRotationObservation(definitionID, ownerRef, sourceRef string, configKnown, storeAvailable, localStore, enabled bool) taskcenter.DailyObservation {
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
	if configKnown {
		if enabled {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
		} else {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "rotation_disabled")
		}
	}
	effective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "owner_runtime_state_unavailable")
	switch {
	case !configKnown:
	case !storeAvailable:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, ownerRef, "store_owner_unavailable")
	case !localStore:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, ownerRef, "external_store_rotation_state_not_exposed")
	case !enabled:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "rotation_disabled")
	default:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, sourceRef, "write_triggered_utc_segment_rotation")
	}

	timing := runtimeDailyTimingUnsupported(sourceRef, "store_segment_rotation_is_not_a_scheduled_task")
	timing.Timezone = runtimeDailyUnknown[string](sourceRef, "store_rotation_state_unavailable")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "segment_change_is_write_triggered")
	timing.NotBefore = runtimeDailyNone[time.Time](sourceRef, "no_not_before_gate")
	timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](sourceRef, "no_time_window_gate")
	timing.DeadlineAt = runtimeDailyNone[time.Time](sourceRef, "no_deadline_gate")
	timing.Recurrence = runtimeDailyUnknown[modulecore.TaskRecurrence](sourceRef, "store_rotation_state_unavailable")
	if configKnown && localStore {
		if enabled {
			timing.Timezone = runtimeDailyKnown("UTC", sourceRef+".utc_day_boundary")
			timing.Recurrence = runtimeDailyUnsupported[modulecore.TaskRecurrence](sourceRef, "utc_day_boundary_is_checked_on_write")
		} else {
			timing.Timezone = runtimeDailyNone[string](sourceRef, "rotation_disabled")
			timing.Recurrence = runtimeDailyNone[modulecore.TaskRecurrence](sourceRef, "rotation_disabled")
		}
	}
	timing.EstimatedDurationSeconds = runtimeDailyUnsupported[int64](sourceRef, "rotation_duration_not_estimated")
	return taskcenter.DailyObservation{
		DefinitionID:    definitionID,
		Configured:      configured,
		Effective:       effective,
		Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, ownerRef, "store_rotation_status_not_exposed"),
		Timing:          timing,
		NextRunAt:       runtimeDailyUnknown[time.Time](sourceRef, "next_write_not_predictable"),
		TargetCount:     runtimeDailyUnsupported[int](sourceRef, "store_rotation_has_no_task_targets"),
		NativeRefs:      []taskcenter.DailyResourceRef{},
		NativeRefCount:  runtimeDailyUnknown[int](sourceRef, "segment_history_not_read"),
		LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, "rotation_has_no_task_run_receipt"),
		Occurrences:     []taskcenter.DailyOccurrence{},
		OccurrenceCount: runtimeDailyUnsupported[int](sourceRef, "segments_are_not_task_occurrences"),
		Issues:          []taskcenter.DailyIssue{},
	}
}

func storeRotationDailyDefinition(sourceID, definitionID, kindID, label, ownerRef, runtimeRef string, sourceRefs ...taskcenter.DailyResourceRef) taskcenter.DailyDefinitionDescriptor {
	return taskcenter.DailyDefinitionDescriptor{
		DefinitionID: definitionID,
		KindID:       kindID,
		Label:        label,
		OwnerRef:     ownerRef,
		ResourceType: "write_triggered_utc_segment_rotation",
		DataScope:    tool.DataScopePublic,
		Implementation: runtimeDailyAxis(
			taskcenter.DailyImplementationImplemented,
			runtimeRef,
			"segment_rotation_is_not_a_scheduled_task",
		),
		SourceRefs: sourceRefs,
	}
}

func storeRotationResourceRef(sourceID, ownerRef, configRef, displayName string) taskcenter.DailyResourceRef {
	return runtimeDailyResourceRef(sourceID, ownerRef, "configuration_field", configRef, "", tool.DataScopePublic, displayName)
}
