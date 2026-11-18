package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const (
	revenueDailySourceID     = "runtime.revenue-daily-report"
	revenueDailyDefinitionID = "revenue.daily_report"
	revenueDailyReadLimit    = 1000
)

func registerRuntimeRevenueDailySource(deps *Dependencies, cfg *config.Config) {
	if deps == nil {
		return
	}
	const sourceRef = "config.revenue.daily_routine"
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: revenueDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: revenueDailyDefinitionID,
			KindID:       "revenue.daily_report",
			Label:        "Revenue daily report artifacts",
			OwnerRef:     "revenue.daily_routine",
			ResourceType: "revenue_daily_report_history",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented,
				"application.revenue.DailyRoutineService", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(revenueDailySourceID, "revenue.daily_routine", "configuration_definition", sourceRef, "", tool.DataScopePublic, "Revenue daily report configuration"),
				runtimeDailyResourceRef(revenueDailySourceID, "revenue.daily_routine", "readonly_query", "revenue.daily_reports", "", tool.DataScopePublic, "Revenue daily report history"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		now := time.Now().UTC()
		configured, configKnown := false, cfg != nil
		if configKnown {
			configured = cfg.Revenue.IsEnabled()
		}
		configuredAxis := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
		effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
		if configKnown {
			configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "revenue_disabled")
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "revenue_disabled")
			if configured {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
				switch {
				case deps.heartbeatSvc == nil:
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "heartbeat.enabled", "heartbeat_unavailable")
				case deps.revenueDailyReadStore == nil:
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "report_reader_unavailable")
				default:
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "workstream.heartbeat_schedules", "matching_report_schedule_not_confirmed")
				}
			}
		}
		observation := revenueDailyObservation(configuredAxis, effectiveAxis, sourceRef)
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		store := deps.revenueDailyReadStore
		if store == nil {
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "revenue.daily_reports", "report_reader_unavailable")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "revenue.daily_routine", Code: taskcenter.DailyIssueSourceReported}}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
		}
		reports, err := store.ListDailyRoutineReports(ctx, revenueDailyReadLimit)
		if err != nil {
			if ctx.Err() != nil {
				return taskcenter.DailySourceSnapshot{}, ctx.Err()
			}
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "revenue.daily_reports", "report_query_failed")
			observation.NativeRefCount = runtimeDailyUnknown[int]("revenue.daily_reports.count", "report_query_failed")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "revenue.daily_routine", Code: taskcenter.DailyIssueSourceReported}}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
		}

		refs := make([]taskcenter.DailyResourceRef, 0, min(len(reports), taskcenter.DailyMaxNativeRefs))
		for _, report := range reports[:min(len(reports), taskcenter.DailyMaxNativeRefs)] {
			refs = append(refs, runtimeDailyResourceRef(revenueDailySourceID, "revenue.daily_routine", "daily_report_artifact", "revenue.daily_report", string(report.ArtifactID), tool.DataScopePublic, "Revenue daily report artifact"))
		}
		observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "revenue.daily_reports", "")
		observation.NativeRefs = refs
		observation.NativeRefsTruncated = len(reports) > len(refs) || len(reports) >= revenueDailyReadLimit
		observation.NativeRefCount = runtimeDailyKnown(len(reports), "revenue.daily_reports.count")
		if len(reports) >= revenueDailyReadLimit {
			observation.NativeRefCount = runtimeDailyUnknown[int]("revenue.daily_reports.count", "reader_limit_reached")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "revenue.daily_routine", Code: taskcenter.DailyIssueSourceReported}}
		}
		observation.LatestEvidence = runtimeDailyUnknownEvidence("revenue.daily_reports", "artifact_history_is_not_run_receipt")
		observation.Occurrences = []taskcenter.DailyOccurrence{}
		observation.OccurrenceCount = runtimeDailyUnknown[int]("revenue.daily_reports", "run_occurrence_history_unavailable")
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}

func revenueDailyObservation(configured, effective taskcenter.DailyAxis, sourceRef string) taskcenter.DailyObservation {
	return taskcenter.DailyObservation{
		DefinitionID:         revenueDailyDefinitionID,
		Configured:           configured,
		Effective:            effective,
		Observed:             runtimeDailyAxis(taskcenter.DailyObservedUnknown, sourceRef, "report_history_not_observed"),
		Timing:               runtimeDailyTimingUnsupported(sourceRef, "next_execution_schedule_owned_by_workstream"),
		NextRunAt:            runtimeDailyUnknown[time.Time]("workstream.heartbeat_schedules", "matching_report_schedule_not_confirmed"),
		TargetCount:          runtimeDailyUnsupported[int]("revenue.daily_reports", "report_history_is_not_target_count"),
		SourceRefs:           []taskcenter.DailyResourceRef{},
		NativeRefs:           []taskcenter.DailyResourceRef{},
		NativeRefCount:       runtimeDailyUnknown[int]("revenue.daily_reports.count", "report_history_not_observed"),
		NativeRefsTruncated:  false,
		LatestEvidence:       runtimeDailyUnknownEvidence("revenue.daily_reports", "artifact_history_is_not_run_receipt"),
		Occurrences:          []taskcenter.DailyOccurrence{},
		OccurrenceCount:      runtimeDailyUnknown[int]("revenue.daily_reports", "run_occurrence_history_unavailable"),
		OccurrencesTruncated: false,
		Issues:               []taskcenter.DailyIssue{},
	}
}
