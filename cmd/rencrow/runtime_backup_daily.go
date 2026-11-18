package main

import (
	"context"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	backupDailySourceID      = "runtime.backup-configuration"
	backupDailyDefinitionID  = "backup.external_runner"
	backupDailyOwnerRef      = "config.backup"
	backupDailyConfiguration = "config.backup.core_source"
)

// registerRuntimeBackupDailySource projects CORE's external-backup
// configuration contract. It does not probe an OS timer, a backup service, or
// any configured destination path.
func registerRuntimeBackupDailySource(deps *Dependencies, cfg *config.Config) {
	if deps == nil {
		return
	}
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: backupDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: backupDailyDefinitionID,
			KindID:       "backup.external_maintenance",
			Label:        "External backup runner configuration",
			OwnerRef:     backupDailyOwnerRef,
			ResourceType: "external_backup_configuration",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(
				taskcenter.DailyImplementationUnimplemented,
				"internal.adapter.config.BackupConfig",
				"external_runner_not_owned_by_core",
			),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(backupDailySourceID, backupDailyOwnerRef, "configuration_field", "backup.core_source", "", tool.DataScopePublic, "Backup source configuration"),
				runtimeDailyResourceRef(backupDailySourceID, backupDailyOwnerRef, "configuration_field", "backup.core_snapshot_root", "", tool.DataScopePublic, "Core snapshot destination configuration"),
				runtimeDailyResourceRef(backupDailySourceID, backupDailyOwnerRef, "configuration_field", "backup.knowledge_source", "", tool.DataScopePublic, "Knowledge backup source configuration"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		configured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, backupDailyConfiguration, "configuration_unavailable")
		if cfg != nil {
			// A successfully loaded BackupConfig is validated as a complete cohort:
			// core_source is required whenever the owner reports itself configured.
			if strings.TrimSpace(cfg.Backup.CoreSource) == "" {
				configured = runtimeDailyAxis(taskcenter.DailyConfiguredUnconfigured, backupDailyConfiguration, "backup_configuration_absent")
			} else {
				configured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, backupDailyConfiguration, "")
			}
		}
		timing := runtimeDailyTimingUnknown("external.backup_timer", "external_timer_not_observed")
		timing.AllowedWindows = runtimeDailyUnsupported[[]modulecore.TaskTimeWindow]("external.backup_timer", "timer_schedule_not_exposed")
		timing.EstimatedDurationSeconds = runtimeDailyUnsupported[int64]("external.backup_timer", "duration_not_exposed")
		observation := taskcenter.DailyObservation{
			DefinitionID:    backupDailyDefinitionID,
			Configured:      configured,
			Effective:       runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "external.backup_timer", "external_timer_not_observed"),
			Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "external.backup_timer", "no_core_timer_status_port"),
			Timing:          timing,
			NextRunAt:       runtimeDailyUnknown[time.Time]("external.backup_timer", "external_timer_not_observed"),
			TargetCount:     runtimeDailyUnsupported[int](backupDailyOwnerRef, "backup_target_count_not_a_daily_task_target"),
			NativeRefs:      []taskcenter.DailyResourceRef{},
			NativeRefCount:  runtimeDailyUnknown[int](backupDailyOwnerRef, "external_backup_status_not_observed"),
			LatestEvidence:  runtimeDailyUnknownEvidence("external.backup_timer", "external_backup_receipt_not_observed"),
			Occurrences:     []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnknown[int]("external.backup_timer", "external_backup_receipt_not_observed"),
			Issues:          []taskcenter.DailyIssue{},
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}
