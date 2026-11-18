package main

import (
	"context"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	knowledgememory "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	dailyIntakeSourceID     = "runtime.knowledge-memory-daily-intake"
	dailyIntakeDefinitionID = "knowledge_memory.daily_intake"
)

type runtimeDailyIntakeReadState struct {
	rules    knowledgememory.DailyIntakeRuleStore
	registry *l1sqlite.L1SQLiteStore
}

func registerRuntimeDailyIntakeSource(deps *Dependencies, cfg *config.Config, state *runtimeDailyIntakeReadState) {
	if deps == nil {
		return
	}
	userID := ""
	enabled := false
	if cfg != nil {
		userID = strings.TrimSpace(cfg.LocalAgentOps.UserID)
		enabled = cfg.KnowledgeMemory.IsEnabled()
	}
	dataScope := tool.DataScopePublic
	if userID != "" {
		dataScope = tool.DataScopeUser
	}
	const sourceRef = "config.knowledge_memory.daily_intake"
	definitionSourceRef := runtimeDailyResourceRef(dailyIntakeSourceID, "knowledge_memory.daily_intake", "configuration_definition", sourceRef, "", dataScope, "Knowledge Memory daily intake")
	definitionSourceRef.UserID = userID
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: dailyIntakeSourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: dailyIntakeDefinitionID,
			KindID:       "knowledge_memory.daily_intake",
			Label:        "Knowledge Memory daily intake",
			OwnerRef:     "knowledge_memory.daily_intake",
			ResourceType: "daily_intake_rule_set",
			DataScope:    dataScope,
			UserID:       userID,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented,
				"application.knowledgememory.RunDailyIntakeSweep", ""),
			SourceRefs: []taskcenter.DailyResourceRef{definitionSourceRef},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, scope tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		if userID != "" && (!scope.Allows(tool.DataScopeUser) || scope.AuthenticatedUserID != userID) {
			return taskcenter.DailySourceSnapshot{ObservedAt: time.Now().UTC(), Observations: []taskcenter.DailyObservation{}, Issues: []taskcenter.DailyIssue{}}, nil
		}
		now := time.Now().UTC()
		configuredAxis := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
		effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
		if cfg != nil {
			configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "knowledge_memory_disabled")
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "knowledge_memory_disabled")
			if enabled {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
				switch {
				case userID == "":
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "owner_user_unconfigured")
				case state == nil || state.rules == nil:
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "knowledge_memory.daily_intake_rules", "rule_store_unavailable")
				case state.registry == nil:
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "conversation.l1.source_registry", "l1_registry_unavailable")
				default:
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "knowledge_memory.daily_intake_sweeper", "")
				}
			}
		}
		timing := runtimeDailyTimingUnsupported("knowledge_memory.daily_intake_sweeper", "fixed_interval_not_a_wall_clock_schedule")
		timing.Timezone = runtimeDailyNone[string]("knowledge_memory.daily_intake_sweeper", "interval_schedule_has_no_timezone")
		timing.ScheduledStartAt = runtimeDailyNone[time.Time]("knowledge_memory.daily_intake_sweeper", "runs_immediately_then_by_interval")
		timing.NotBefore = runtimeDailyNone[time.Time]("knowledge_memory.daily_intake_sweeper", "no_not_before_gate")
		timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow]("knowledge_memory.daily_intake_sweeper", "no_time_window_gate")
		timing.DeadlineAt = runtimeDailyNone[time.Time]("knowledge_memory.daily_intake_sweeper", "no_task_deadline")
		timing.EstimatedDurationSeconds = runtimeDailyUnsupported[int64]("knowledge_memory.daily_intake_sweeper", "duration_not_estimated")
		intervalSeconds := int64((24 * time.Hour) / time.Second)
		timing.Recurrence = runtimeDailyKnown(modulecore.TaskRecurrence{Frequency: "interval", IntervalSeconds: &intervalSeconds}, "application.knowledgememory.daily_intake_sweeper.interval")

		observed := runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "knowledge_memory.daily_intake_rules", "no_scope_filtered_rule_reader")
		issues := []taskcenter.DailyIssue{}
		if enabled && userID != "" && state != nil && state.rules != nil {
			issues = append(issues, taskcenter.DailyIssue{OwnerRef: "knowledge_memory.daily_intake", Code: taskcenter.DailyIssueSourceReported})
		}
		observation := taskcenter.DailyObservation{
			DefinitionID:         dailyIntakeDefinitionID,
			Configured:           configuredAxis,
			Effective:            effectiveAxis,
			Observed:             observed,
			Timing:               timing,
			NextRunAt:            runtimeDailyUnknown[time.Time]("knowledge_memory.daily_intake_sweeper", "next_run_not_exposed"),
			TargetCount:          runtimeDailyUnknown[int]("knowledge_memory.daily_intake_rules.count", "scoped_rule_reader_unavailable"),
			SourceRefs:           []taskcenter.DailyResourceRef{},
			NativeRefs:           []taskcenter.DailyResourceRef{},
			NativeRefCount:       runtimeDailyUnknown[int]("knowledge_memory.daily_intake_rules.count", "scoped_rule_reader_unavailable"),
			NativeRefsTruncated:  false,
			LatestEvidence:       runtimeDailyUnknownEvidence("knowledge_memory.daily_intake", "no_durable_run_receipt"),
			Occurrences:          []taskcenter.DailyOccurrence{},
			OccurrenceCount:      runtimeDailyUnsupported[int]("knowledge_memory.daily_intake", "no_run_occurrence_port"),
			OccurrencesTruncated: false,
			Issues:               issues,
		}
		if !enabled {
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "knowledge_memory.daily_intake", "feature_disabled")
			observation.Issues = []taskcenter.DailyIssue{}
		}
		if userID == "" && enabled {
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "knowledge_memory.daily_intake", "owner_user_unconfigured")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "knowledge_memory.daily_intake", Code: taskcenter.DailyIssueSourceReported}}
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}
