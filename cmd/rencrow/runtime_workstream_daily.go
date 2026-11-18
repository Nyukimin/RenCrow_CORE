package main

import (
	"context"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/heartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

const (
	workstreamDailySourceID          = "runtime.workstream-heartbeat"
	workstreamDailyDefinitionID      = "workstream.heartbeat_schedules"
	workstreamDailyScheduleReadLimit = 1000
)

func registerRuntimeWorkstreamDailySource(deps *Dependencies, cfg *config.Config, store heartbeat.WorkstreamHeartbeatStore) {
	if deps == nil {
		return
	}
	const sourceRef = "config.workstream.heartbeat_schedules"
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: workstreamDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: workstreamDailyDefinitionID,
			KindID:       "workstream.heartbeat",
			Label:        "Workstream heartbeat schedules",
			OwnerRef:     "workstream.heartbeat_schedules",
			ResourceType: "workstream_heartbeat_schedule",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented,
				"domain.workstream.HeartbeatSchedule", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(workstreamDailySourceID, "workstream.heartbeat_schedules", "readonly_query", sourceRef, "", tool.DataScopePublic, "Workstream heartbeat schedules"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		now := time.Now().UTC()
		configKnown := cfg != nil
		configured := configKnown && cfg.Workstream.IsEnabled()
		configuredAxis := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
		if configKnown {
			configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "workstream_disabled")
			if configured {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
			}
		}
		effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
		switch {
		case !configKnown:
		case !configured:
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "workstream_disabled")
		case deps.heartbeatSvc == nil:
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.heartbeat.enabled", "heartbeat_unavailable")
		case store == nil:
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "schedule_store_unavailable")
		default:
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, sourceRef, "")
		}

		observation := baseWorkstreamDailyObservation(configuredAxis, effectiveAxis, sourceRef)
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		if store == nil {
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, sourceRef, "schedule_store_unavailable")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "workstream.heartbeat_schedules", Code: taskcenter.DailyIssueSourceReported}}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
		}

		schedules, err := store.ListHeartbeatSchedules(ctx, workstreamDailyScheduleReadLimit)
		if err != nil {
			if ctx.Err() != nil {
				return taskcenter.DailySourceSnapshot{}, ctx.Err()
			}
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, sourceRef, "schedule_query_failed")
			observation.NativeRefCount = runtimeDailyUnknown[int](sourceRef, "schedule_query_failed")
			observation.TargetCount = runtimeDailyUnknown[int](sourceRef, "schedule_query_failed")
			observation.NextRunAt = runtimeDailyUnknown[time.Time](sourceRef, "schedule_query_failed")
			observation.OccurrenceCount = runtimeDailyUnknown[int](sourceRef, "schedule_occurrence_history_unavailable")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "workstream.heartbeat_schedules", Code: taskcenter.DailyIssueSourceReported}}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
		}

		sort.Slice(schedules, func(i, j int) bool {
			if schedules[i].NextRunAt.Equal(schedules[j].NextRunAt) {
				return schedules[i].ScheduleID < schedules[j].ScheduleID
			}
			if schedules[i].NextRunAt.IsZero() {
				return false
			}
			if schedules[j].NextRunAt.IsZero() {
				return true
			}
			return schedules[i].NextRunAt.Before(schedules[j].NextRunAt)
		})
		refs := make([]taskcenter.DailyResourceRef, 0, min(len(schedules), taskcenter.DailyMaxNativeRefs))
		var latestAttempt time.Time
		var nextRun time.Time
		activeCount := 0
		for _, schedule := range schedules {
			ref := runtimeDailyResourceRef(workstreamDailySourceID, "workstream.heartbeat_schedules", "workstream_heartbeat_schedule", "workstream.heartbeat_schedule", string(schedule.ScheduleID), tool.DataScopePublic, "Workstream heartbeat schedule")
			if len(refs) < taskcenter.DailyMaxNativeRefs {
				refs = append(refs, ref)
			}
			if !schedule.LastRunAt.IsZero() && (latestAttempt.IsZero() || schedule.LastRunAt.After(latestAttempt)) {
				latestAttempt = schedule.LastRunAt
			}
			if schedule.Status != domainworkstream.StatusActive {
				continue
			}
			activeCount++
			if !schedule.NextRunAt.IsZero() && (nextRun.IsZero() || schedule.NextRunAt.Before(nextRun)) {
				nextRun = schedule.NextRunAt
			}
		}

		truncated := len(schedules) >= workstreamDailyScheduleReadLimit
		observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "workstream.heartbeat_schedules", "")
		observation.NativeRefs = refs
		observation.NativeRefsTruncated = truncated || len(schedules) > len(refs)
		observation.NativeRefCount = runtimeDailyKnown(len(schedules), "workstream.heartbeat_schedules.count")
		observation.TargetCount = runtimeDailyKnown(len(schedules), "workstream.heartbeat_schedules.count")
		if truncated {
			observation.NativeRefCount = runtimeDailyUnknown[int]("workstream.heartbeat_schedules.count", "native_query_limit_reached")
			observation.TargetCount = runtimeDailyUnknown[int]("workstream.heartbeat_schedules.count", "native_query_limit_reached")
			observation.NextRunAt = runtimeDailyUnknown[time.Time]("workstream.heartbeat_schedule.next_run_at", "native_query_limit_reached")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "workstream.heartbeat_schedules", Code: taskcenter.DailyIssueSourceReported}}
		} else if activeCount == 0 {
			observation.NextRunAt = runtimeDailyNone[time.Time]("workstream.heartbeat_schedules", "no_active_schedules")
		} else if nextRun.IsZero() {
			observation.NextRunAt = runtimeDailyUnknown[time.Time]("workstream.heartbeat_schedule.next_run_at", "active_schedule_has_no_next_run")
		} else {
			observation.NextRunAt = runtimeDailyKnown(nextRun.UTC(), "workstream.heartbeat_schedule.next_run_at")
		}
		observation.OccurrenceCount = runtimeDailyUnknown[int]("workstream.heartbeat_schedule.occurrences", "schedule_occurrence_history_unavailable")
		if !latestAttempt.IsZero() {
			observation.LatestEvidence = taskcenter.DailyEvidence{
				State:         taskcenter.DailyValueKnown,
				LastAttemptAt: runtimeDailyKnown(latestAttempt.UTC(), "workstream.heartbeat_schedule.last_run_at"),
				LastSuccessAt: runtimeDailyUnknown[time.Time]("workstream.heartbeat_schedule", "run_outcome_unavailable"),
				LastResult:    runtimeDailyUnknown[taskcenter.DailyOutcome]("workstream.heartbeat_schedule", "run_outcome_unavailable"),
				Refs:          append([]taskcenter.DailyResourceRef{}, refs...),
			}
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}

func baseWorkstreamDailyObservation(configured, effective taskcenter.DailyAxis, sourceRef string) taskcenter.DailyObservation {
	timing := runtimeDailyTimingUnsupported(sourceRef, "native_schedule_text_not_normalized")
	timing.Timezone = runtimeDailyUnknown[string](sourceRef, "schedule_timezone_not_exposed")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "multiple_native_schedules")
	return taskcenter.DailyObservation{
		DefinitionID:         workstreamDailyDefinitionID,
		Configured:           configured,
		Effective:            effective,
		Observed:             runtimeDailyAxis(taskcenter.DailyObservedUnknown, sourceRef, "schedule_snapshot_unavailable"),
		Timing:               timing,
		NextRunAt:            runtimeDailyUnknown[time.Time](sourceRef, "schedule_snapshot_unavailable"),
		TargetCount:          runtimeDailyUnknown[int](sourceRef, "schedule_snapshot_unavailable"),
		SourceRefs:           []taskcenter.DailyResourceRef{},
		NativeRefs:           []taskcenter.DailyResourceRef{},
		NativeRefCount:       runtimeDailyUnknown[int](sourceRef, "schedule_snapshot_unavailable"),
		NativeRefsTruncated:  false,
		LatestEvidence:       runtimeDailyUnknownEvidence(sourceRef, "schedule_run_receipt_unavailable"),
		Occurrences:          []taskcenter.DailyOccurrence{},
		OccurrenceCount:      runtimeDailyUnknown[int](sourceRef, "schedule_occurrence_history_unavailable"),
		OccurrencesTruncated: false,
		Issues:               []taskcenter.DailyIssue{},
	}
}
