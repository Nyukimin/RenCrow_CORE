package main

import (
	"context"
	"errors"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	runtimeMemoryDailySourceID                = "runtime.memory-daily"
	profilePromotionDailyDefinitionID         = "memory.profile_promotion"
	memoryLifecycleDailyDefinitionID          = "memory.lifecycle_maintenance"
	profilePromotionDailyOwnerRef             = "memory.profile_promotion"
	memoryLifecycleDailyOwnerRef              = "memory.lifecycle_maintenance"
	profilePromotionDailyWorkerSourceRef      = "runtime.memory_promotion_worker"
	profilePromotionDailyHistorySourceRef     = "conversation.profile_promotion_jobs"
	memoryLifecycleDailyRunnerSourceRef       = "conversation.memory_lifecycle_runner"
	memoryLifecycleDailyHistorySourceRef      = "conversation.memory_lifecycle_receipt"
	profilePromotionSessionBoundHistoryReason = "session_bound_history_not_read"
	memoryLifecycleMissingReceiptReason       = "no_read_only_run_receipt"
	profilePromotionEventDrivenTimingReason   = "event_driven_worker_no_daily_schedule"
	memoryLifecycleSubsecondIntervalReason    = "subsecond_interval_not_representable"
)

// registerRuntimeMemoryDailySource projects only generic configuration and
// runtime wiring state. It deliberately does not read ProfilePromotion jobs
// or invoke Memory lifecycle maintenance.
func registerRuntimeMemoryDailySource(
	deps *Dependencies,
	cfg *config.Config,
	l1Store *l1sqlite.L1SQLiteStore,
	lifecycleCfg memoryLifecycleJobConfig,
) {
	if deps == nil {
		return
	}
	promotionWorkerAvailable := deps.memoryPromotionCancel != nil
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: runtimeMemoryDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{
			{
				DefinitionID: profilePromotionDailyDefinitionID,
				KindID:       "memory.profile_promotion",
				Label:        "Profile promotion",
				OwnerRef:     profilePromotionDailyOwnerRef,
				ResourceType: "background_worker",
				DataScope:    tool.DataScopePublic,
				Implementation: runtimeDailyAxis(
					taskcenter.DailyImplementationImplemented,
					"application.memorypromotion.Service",
					"",
				),
				SourceRefs: []taskcenter.DailyResourceRef{
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, profilePromotionDailyOwnerRef, "configuration_field", "config.conversation.profile_promotion_enabled", "", tool.DataScopePublic, "Profile promotion setting"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, profilePromotionDailyOwnerRef, "configuration_field", "config.conversation.enabled", "", tool.DataScopePublic, "Conversation runtime gate"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, profilePromotionDailyOwnerRef, "worker_status", "runtime.memory_promotion_worker", "", tool.DataScopePublic, "Profile promotion worker status"),
				},
			},
			{
				DefinitionID: memoryLifecycleDailyDefinitionID,
				KindID:       "memory.lifecycle_maintenance",
				Label:        "Memory lifecycle maintenance",
				OwnerRef:     memoryLifecycleDailyOwnerRef,
				ResourceType: "maintenance_runner",
				DataScope:    tool.DataScopePublic,
				Implementation: runtimeDailyAxis(
					taskcenter.DailyImplementationImplemented,
					"l1sqlite.RunMemoryLifecycleMaintenance",
					"",
				),
				SourceRefs: []taskcenter.DailyResourceRef{
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "resolved_configuration", "runtime.memory_lifecycle.interval", "", tool.DataScopePublic, "Resolved Memory lifecycle interval"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "configuration_field", "RENCROW_MEMORY_LIFECYCLE_INTERVAL_MS", "", tool.DataScopePublic, "Memory lifecycle millisecond interval override"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "configuration_field", "RENCROW_MEMORY_LIFECYCLE_INTERVAL_SEC", "", tool.DataScopePublic, "Memory lifecycle second interval override"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "configuration_field", "RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_MS", "", tool.DataScopePublic, "Memory lifecycle accelerated clock input"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "configuration_field", "RENCROW_MEMORY_LIFECYCLE_ACCEL_MONTH_SEC", "", tool.DataScopePublic, "Memory lifecycle accelerated clock input"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "configuration_resolver", "cmd.rencrow.memoryLifecycleJobConfigFromEnv", "", tool.DataScopePublic, "Memory lifecycle runtime configuration resolver"),
					runtimeDailyResourceRef(runtimeMemoryDailySourceID, memoryLifecycleDailyOwnerRef, "runtime_runner", "runtime.conversation_background_jobs", "", tool.DataScopePublic, "Conversation background job wiring"),
				},
			},
		},
	}
	source := newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if ctx == nil {
			return taskcenter.DailySourceSnapshot{}, errors.New("daily source context is required")
		}
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		return taskcenter.DailySourceSnapshot{
			ObservedAt: now,
			Observations: []taskcenter.DailyObservation{
				profilePromotionDailyObservation(cfg, l1Store != nil, promotionWorkerAvailable),
				memoryLifecycleDailyObservation(l1Store != nil, lifecycleCfg.Interval),
			},
			Issues: []taskcenter.DailyIssue{},
		}, nil
	})
	registerTaskCenterDailySource(deps, source)
}

func profilePromotionDailyObservation(cfg *config.Config, l1Available, workerAvailable bool) taskcenter.DailyObservation {
	configSource := "config.conversation.profile_promotion_enabled"
	effectiveSource := profilePromotionDailyWorkerSourceRef
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, configSource, "configuration_unavailable")
	effective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, effectiveSource, "configuration_unavailable")
	if cfg != nil {
		if cfg.Conversation.ProfilePromotionEnabledValue() {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, configSource, "")
			switch {
			case !cfg.Conversation.Enabled:
				effective = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.conversation.enabled", "conversation_disabled")
			case !l1Available:
				effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "conversation.l1_store", "l1_store_unavailable")
			case !workerAvailable:
				effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, effectiveSource, "promotion_worker_unavailable")
			default:
				effective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, effectiveSource, "")
			}
		} else {
			configured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, configSource, "profile_promotion_disabled")
			effective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, configSource, "profile_promotion_disabled")
		}
	}

	timing := runtimeDailyTimingUnsupported(profilePromotionDailyWorkerSourceRef, profilePromotionEventDrivenTimingReason)
	timing.Timezone = runtimeDailyNone[string](profilePromotionDailyWorkerSourceRef, "no_wall_clock_schedule")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](profilePromotionDailyWorkerSourceRef, "event_driven_worker")
	timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](profilePromotionDailyWorkerSourceRef, "no_wall_clock_window")
	timing.DeadlineAt = runtimeDailyNone[time.Time](profilePromotionDailyWorkerSourceRef, "no_task_deadline")

	return taskcenter.DailyObservation{
		DefinitionID: profilePromotionDailyDefinitionID,
		Configured:   configured,
		Effective:    effective,
		Observed: runtimeDailyAxis(
			taskcenter.DailyObservedUnknown,
			profilePromotionDailyHistorySourceRef,
			profilePromotionSessionBoundHistoryReason,
		),
		Timing:               timing,
		NextRunAt:            runtimeDailyUnknown[time.Time](profilePromotionDailyHistorySourceRef, "next_event_not_observable"),
		TargetCount:          runtimeDailyUnknown[int](profilePromotionDailyHistorySourceRef, "session_bound_job_list_not_read"),
		SourceRefs:           []taskcenter.DailyResourceRef{},
		NativeRefs:           []taskcenter.DailyResourceRef{},
		NativeRefCount:       runtimeDailyUnknown[int](profilePromotionDailyHistorySourceRef, "session_bound_job_list_not_read"),
		LatestEvidence:       runtimeDailyUnknownEvidence(profilePromotionDailyHistorySourceRef, profilePromotionSessionBoundHistoryReason),
		Occurrences:          []taskcenter.DailyOccurrence{},
		OccurrenceCount:      runtimeDailyUnknown[int](profilePromotionDailyHistorySourceRef, profilePromotionSessionBoundHistoryReason),
		NativeRefsTruncated:  false,
		OccurrencesTruncated: false,
		Issues:               []taskcenter.DailyIssue{},
	}
}

func memoryLifecycleDailyObservation(l1Available bool, interval time.Duration) taskcenter.DailyObservation {
	const intervalSource = "runtime.memory_lifecycle.interval"
	if interval <= 0 {
		interval = 24 * time.Hour
	}
	effective := runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "conversation.l1_store", "l1_store_unavailable")
	if l1Available {
		effective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, memoryLifecycleDailyRunnerSourceRef, "")
	}

	timing := runtimeDailyTimingUnsupported(intervalSource, "interval_schedule_has_no_wall_clock_time")
	timing.Timezone = runtimeDailyNone[string](intervalSource, "interval_schedule_has_no_timezone")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](intervalSource, "runs_immediately_then_by_interval")
	timing.NotBefore = runtimeDailyNone[time.Time](intervalSource, "no_not_before_gate")
	timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](intervalSource, "no_time_window_gate")
	timing.DeadlineAt = runtimeDailyNone[time.Time](intervalSource, "no_task_deadline")
	timing.EstimatedDurationSeconds = runtimeDailyUnsupported[int64](intervalSource, "maintenance_duration_not_estimated")
	if interval%time.Second == 0 {
		intervalSeconds := int64(interval / time.Second)
		timing.Recurrence = runtimeDailyKnown(modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceInterval, IntervalSeconds: &intervalSeconds}, intervalSource)
	} else {
		timing.Recurrence = runtimeDailyUnsupported[modulecore.TaskRecurrence](intervalSource, memoryLifecycleSubsecondIntervalReason)
	}

	return taskcenter.DailyObservation{
		DefinitionID: memoryLifecycleDailyDefinitionID,
		Configured: runtimeDailyAxis(
			taskcenter.DailyConfiguredConfigured,
			intervalSource,
			"",
		),
		Effective:            effective,
		Observed:             runtimeDailyAxis(taskcenter.DailyObservedUnknown, memoryLifecycleDailyHistorySourceRef, memoryLifecycleMissingReceiptReason),
		Timing:               timing,
		NextRunAt:            runtimeDailyUnknown[time.Time](memoryLifecycleDailyHistorySourceRef, "next_run_start_time_unknown"),
		TargetCount:          runtimeDailyUnknown[int](memoryLifecycleDailyHistorySourceRef, "no_scope_safe_target_count"),
		SourceRefs:           []taskcenter.DailyResourceRef{},
		NativeRefs:           []taskcenter.DailyResourceRef{},
		NativeRefCount:       runtimeDailyUnknown[int](memoryLifecycleDailyHistorySourceRef, memoryLifecycleMissingReceiptReason),
		LatestEvidence:       runtimeDailyUnknownEvidence(memoryLifecycleDailyHistorySourceRef, memoryLifecycleMissingReceiptReason),
		Occurrences:          []taskcenter.DailyOccurrence{},
		OccurrenceCount:      runtimeDailyUnknown[int](memoryLifecycleDailyHistorySourceRef, memoryLifecycleMissingReceiptReason),
		NativeRefsTruncated:  false,
		OccurrencesTruncated: false,
		Issues:               []taskcenter.DailyIssue{},
	}
}
