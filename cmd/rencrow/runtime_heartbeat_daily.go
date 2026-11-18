package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/heartbeat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	heartbeatDailySourceID                 = "runtime.heartbeat"
	heartbeatCoreDefinitionID              = "heartbeat.core"
	heartbeatGmailDefinitionID             = "heartbeat.gmail_intake"
	heartbeatXBookmarksDefinitionID        = "heartbeat.x_bookmarks"
	heartbeatBacklogDefinitionID           = "heartbeat.backlog_intake"
	heartbeatAtlasDefinitionID             = "heartbeat.atlas_maturation"
	heartbeatEconomicDiscoveryDefinitionID = "heartbeat.economic_discovery"
	heartbeatWorkspaceFileDefinitionID     = "heartbeat.workspace_file"
)

// registerRuntimeHeartbeatDailySource projects the Heartbeat composition and
// its directly configured child routines. It only reads the already-built
// runtime references and the loaded configuration; it never starts a worker.
func registerRuntimeHeartbeatDailySource(deps *Dependencies, cfg *config.Config, l1Store *l1sqlite.L1SQLiteStore) {
	if deps == nil {
		return
	}
	const (
		configRef = "config.heartbeat"
		owner     = "application.heartbeat.HeartbeatService"
	)
	definitions := []taskcenter.DailyDefinitionDescriptor{
		heartbeatDailyDefinition(heartbeatCoreDefinitionID, "heartbeat.tick", "Heartbeat tick", owner, "heartbeat_schedule", "config.heartbeat.interval"),
		heartbeatDailyDefinition(heartbeatGmailDefinitionID, "heartbeat.gmail_intake", "Gmail intake", "heartbeat.gmail", "heartbeat_child_routine", "config.heartbeat.gmail"),
		heartbeatDailyDefinition(heartbeatXBookmarksDefinitionID, "heartbeat.x_bookmarks", "X Bookmarks intake", "heartbeat.x_bookmarks", "heartbeat_child_routine", "config.heartbeat.x_bookmarks"),
		heartbeatDailyDefinition(heartbeatBacklogDefinitionID, "backlog.heartbeat_intake", "Backlog heartbeat intake", "backlog.heartbeat_intake", "heartbeat_child_routine", "heartbeat.backlog_store"),
		heartbeatDailyDefinition(heartbeatAtlasDefinitionID, "atlas.heartbeat_maturation", "Atlas maturation revalidation", "atlas.heartbeat_maturation", "heartbeat_child_routine", "heartbeat.atlas_service"),
		heartbeatDailyDefinition(heartbeatEconomicDiscoveryDefinitionID, "economic.heartbeat_discovery", "Economic opportunity discovery", "economic.heartbeat_discovery", "heartbeat_child_routine", "config.economic_objective"),
		heartbeatDailyDefinition(heartbeatWorkspaceFileDefinitionID, "heartbeat.workspace_file", "HEARTBEAT workspace routine", "heartbeat.workspace_routine", "workspace_instruction_source", "heartbeat.workspace_routine"),
	}
	descriptor := taskcenter.DailySourceDescriptor{SourceID: heartbeatDailySourceID, Definitions: definitions}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		heartbeatEnabled := cfg != nil && cfg.Heartbeat.Enabled
		parentInterval := time.Duration(0)
		if cfg != nil && cfg.Heartbeat.Interval > 0 {
			parentInterval = time.Duration(cfg.Heartbeat.Interval) * time.Minute
		}
		serviceReady := deps.heartbeatSvc != nil
		observations := make([]taskcenter.DailyObservation, 0, len(definitions))
		observations = append(observations,
			heartbeatRoutineObservation(heartbeatCoreDefinitionID, configRef+".interval", cfg != nil, heartbeatEnabled, heartbeatEnabled, serviceReady, parentInterval, configRef+".interval"),
			heartbeatRoutineObservation(heartbeatGmailDefinitionID, "config.heartbeat.gmail.interval_minutes", cfg != nil, cfg != nil && cfg.Heartbeat.Gmail.Enabled, heartbeatEnabled, serviceReady && deps.gmailIntake != nil, heartbeatGmailInterval(cfg), "config.heartbeat.gmail.interval_minutes"),
			heartbeatRoutineObservation(heartbeatXBookmarksDefinitionID, "config.heartbeat.x_bookmarks.interval_minutes", cfg != nil, cfg != nil && cfg.Heartbeat.XBookmarks.Enabled, heartbeatEnabled, serviceReady && l1Store != nil, heartbeatXBookmarksInterval(cfg), "config.heartbeat.x_bookmarks.interval_minutes"),
			heartbeatStoreRoutineObservation(heartbeatBacklogDefinitionID, "heartbeat.backlog_store", cfg != nil, deps.backlogStore != nil, heartbeatEnabled, serviceReady, 0, "backlog.heartbeat_intake"),
			heartbeatStoreRoutineObservation(heartbeatAtlasDefinitionID, "heartbeat.atlas_service", cfg != nil, deps.atlasService != nil, heartbeatEnabled, serviceReady, 0, "atlas.heartbeat_maturation"),
			heartbeatEconomicDiscoveryObservation(cfg, deps, serviceReady, parentInterval),
			heartbeatWorkspaceFileObservation(cfg != nil, heartbeatEnabled, serviceReady, parentInterval),
		)
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: observations, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}

func heartbeatDailyDefinition(definitionID, kindID, label, ownerRef, resourceType, reference string) taskcenter.DailyDefinitionDescriptor {
	refs := []taskcenter.DailyResourceRef{
		runtimeDailyResourceRef(heartbeatDailySourceID, ownerRef, "runtime_configuration", reference, "", tool.DataScopePublic, label),
	}
	if reference != "config.heartbeat.interval" {
		refs = append(refs, runtimeDailyResourceRef(heartbeatDailySourceID, ownerRef, "parent_runtime_gate", "config.heartbeat.interval", "", tool.DataScopePublic, "Parent Heartbeat poll interval gate"))
	}
	return taskcenter.DailyDefinitionDescriptor{
		DefinitionID: definitionID,
		KindID:       kindID,
		Label:        label,
		OwnerRef:     ownerRef,
		ResourceType: resourceType,
		DataScope:    tool.DataScopePublic,
		Implementation: runtimeDailyAxis(
			taskcenter.DailyImplementationImplemented,
			"cmd.rencrow/runtime_heartbeat.go",
			"",
		),
		SourceRefs: refs,
	}
}

func heartbeatRoutineObservation(definitionID, sourceRef string, configKnown, childConfigured, parentEnabled, effective bool, interval time.Duration, intervalSource string) taskcenter.DailyObservation {
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
	if configKnown {
		configured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "routine_disabled")
		if childConfigured {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
		}
	}
	effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
	switch {
	case !configKnown:
	case !childConfigured:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "routine_disabled")
	case !parentEnabled:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.heartbeat.enabled", "heartbeat_disabled")
	case !effective:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "runtime_dependency_unavailable")
	default:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, sourceRef, "")
	}
	return heartbeatUnknownRoutineObservation(definitionID, configured, effectiveAxis, interval, sourceRef, intervalSource)
}

func heartbeatStoreRoutineObservation(definitionID, sourceRef string, configKnown, ownerAvailable, parentEnabled, serviceReady bool, interval time.Duration, intervalSource string) taskcenter.DailyObservation {
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "child_schedule_configuration_unavailable")
	effective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "runtime_state_unavailable")
	switch {
	case !configKnown:
	case !parentEnabled:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.heartbeat.enabled", "heartbeat_disabled")
	case !serviceReady:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "heartbeat.enabled", "heartbeat_runtime_unavailable")
	case !ownerAvailable:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "owner_store_unavailable")
	default:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, sourceRef, "")
	}
	return heartbeatUnknownRoutineObservation(definitionID, configured, effective, interval, sourceRef, intervalSource)
}

func heartbeatEconomicDiscoveryObservation(cfg *config.Config, deps *Dependencies, serviceReady bool, interval time.Duration) taskcenter.DailyObservation {
	const sourceRef = "config.economic_objective"
	configured := cfg != nil && cfg.EconomicObjective.Enabled && cfg.EconomicObjective.HeartbeatDiscoveryEnabled && cfg.EconomicObjective.DraftOnlyEnabled()
	configuredAxis := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
	if cfg != nil {
		configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "discovery_not_configured")
		if configured {
			configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
		}
	}
	effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
	switch {
	case cfg == nil:
	case !configured:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "discovery_not_configured")
	case cfg == nil || !cfg.Heartbeat.Enabled || !serviceReady:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "heartbeat.enabled", "heartbeat_unavailable")
	case deps.revenueStore == nil:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "revenue.discovery_store", "owner_store_unavailable")
	case !hasEconomicDiscoveryStore(deps.revenueStore):
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "revenue.discovery_store", "owner_operation_unavailable")
	default:
		effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, sourceRef, "")
	}
	return heartbeatUnknownRoutineObservation(heartbeatEconomicDiscoveryDefinitionID, configuredAxis, effectiveAxis, interval, sourceRef, "config.heartbeat.interval")
}

func hasEconomicDiscoveryStore(value any) bool {
	_, ok := value.(heartbeat.EconomicObjectiveDiscoveryStore)
	return ok
}

func heartbeatWorkspaceFileObservation(configKnown, heartbeatEnabled, serviceReady bool, interval time.Duration) taskcenter.DailyObservation {
	const sourceRef = "heartbeat.workspace_routine"
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "routine_file_not_read_by_daily_projection")
	effective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "agent_specific_routine_owner_unlinked")
	switch {
	case !configKnown:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
	case !heartbeatEnabled:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.heartbeat.enabled", "heartbeat_disabled")
	case !serviceReady:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.heartbeat.enabled", "heartbeat_unavailable")
	default:
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "routine_owner_unlinked")
	}
	observation := heartbeatUnknownRoutineObservation(heartbeatWorkspaceFileDefinitionID, configured, effective, interval, sourceRef, "config.heartbeat.interval")
	observation.Issues = []taskcenter.DailyIssue{{
		OwnerRef: sourceRef,
		Code:     taskcenter.DailyIssueSourceReported,
	}}
	return observation
}

func heartbeatUnknownRoutineObservation(definitionID string, configured, effective taskcenter.DailyAxis, interval time.Duration, sourceRef, intervalSource string) taskcenter.DailyObservation {
	timing := runtimeDailyTimingUnsupported(sourceRef, "heartbeat_routine_timing_not_owned")
	timing.Timezone = runtimeDailyNone[string](sourceRef, "interval_schedule_has_no_timezone")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "interval_schedule_has_no_fixed_start")
	timing.NotBefore = runtimeDailyNone[time.Time](sourceRef, "no_not_before_gate")
	timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](sourceRef, "no_time_window_gate")
	timing.DeadlineAt = runtimeDailyNone[time.Time](sourceRef, "no_deadline_gate")
	timing.EstimatedDurationSeconds = runtimeDailyUnknown[int64](sourceRef, "duration_not_observed")
	if interval > 0 {
		seconds := int64(interval / time.Second)
		recurrence := modulecore.TaskRecurrence{Frequency: "interval", IntervalSeconds: &seconds}
		timing.Recurrence = runtimeDailyKnown(recurrence, intervalSource)
	} else {
		reason := "interval_not_configured"
		if configured.State == taskcenter.DailyConfiguredUnknown {
			reason = "configuration_unavailable"
		} else {
			switch definitionID {
			case heartbeatBacklogDefinitionID:
				reason = "child_cadence_not_exposed"
			case heartbeatAtlasDefinitionID:
				reason = "atlas_owner_gate_not_projected"
			case heartbeatWorkspaceFileDefinitionID:
				reason = "workspace_routine_cadence_not_exposed"
			case heartbeatEconomicDiscoveryDefinitionID:
				reason = "heartbeat_tick_interval_not_configured"
			}
		}
		timing.Recurrence = runtimeDailyUnknown[modulecore.TaskRecurrence](sourceRef, reason)
	}
	return taskcenter.DailyObservation{
		DefinitionID:         definitionID,
		Configured:           configured,
		Effective:            effective,
		Observed:             runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "run_receipt_unavailable"),
		Timing:               timing,
		NextRunAt:            runtimeDailyUnknown[time.Time](sourceRef, "next_heartbeat_tick_not_exposed"),
		TargetCount:          runtimeDailyUnknown[int](sourceRef, "target_count_not_exposed"),
		NativeRefs:           []taskcenter.DailyResourceRef{},
		NativeRefCount:       runtimeDailyUnknown[int](sourceRef, "native_receipt_not_exposed"),
		NativeRefsTruncated:  false,
		LatestEvidence:       runtimeDailyUnknownEvidence(sourceRef, "run_receipt_unavailable"),
		Occurrences:          []taskcenter.DailyOccurrence{},
		OccurrenceCount:      runtimeDailyUnknown[int](sourceRef, "run_receipt_unavailable"),
		OccurrencesTruncated: false,
		Issues:               []taskcenter.DailyIssue{},
	}
}

func heartbeatGmailInterval(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Heartbeat.Gmail.IntervalMinutes <= 0 {
		return 0
	}
	return time.Duration(cfg.Heartbeat.Gmail.IntervalMinutes) * time.Minute
}

func heartbeatXBookmarksInterval(cfg *config.Config) time.Duration {
	if cfg == nil || cfg.Heartbeat.XBookmarks.IntervalMinutes <= 0 {
		return 0
	}
	return time.Duration(cfg.Heartbeat.XBookmarks.IntervalMinutes) * time.Minute
}
