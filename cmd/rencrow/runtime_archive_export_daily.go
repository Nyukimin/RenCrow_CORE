package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const (
	archiveExportDailySourceID     = "runtime.archive-export-coverage"
	archiveExportDailyDefinitionID = "memory.archive_parquet_export"
	archiveExportDailyOwnerRef     = "application.archive.parquet_export"
)

// registerRuntimeArchiveParquetDailySource records the existing export helper
// as unwired. The helper's default interval is not a runtime schedule because
// no composition starts the job.
func registerRuntimeArchiveParquetDailySource(deps *Dependencies) {
	if deps == nil {
		return
	}
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: archiveExportDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: archiveExportDailyDefinitionID,
			KindID:       "memory.archive_export",
			Label:        "Conversation archive Parquet export helper",
			OwnerRef:     archiveExportDailyOwnerRef,
			ResourceType: "unwired_export_helper",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(
				taskcenter.DailyImplementationUnimplemented,
				"internal.application.archive.ParquetExportJob",
				"runtime_composition_not_wired",
			),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(archiveExportDailySourceID, archiveExportDailyOwnerRef, "owner_helper", "internal.application.archive.ParquetExportJob.Start", "", tool.DataScopePublic, "Archive export lifecycle helper"),
				runtimeDailyResourceRef(archiveExportDailySourceID, archiveExportDailyOwnerRef, "owner_contract", "internal.domain.memory.ConversationArchiveParquetExportResult", "", tool.DataScopePublic, "Parquet export result contract"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		const sourceRef = "internal.application.archive.ParquetExportJob"
		observation := taskcenter.DailyObservation{
			DefinitionID:    archiveExportDailyDefinitionID,
			Configured:      runtimeDailyAxis(taskcenter.DailyConfiguredUnconfigured, sourceRef, "no_runtime_schedule_configuration"),
			Effective:       runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "runtime_composition_not_wired"),
			Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "no_runtime_export_runner"),
			Timing:          runtimeDailyTimingUnsupported(sourceRef, "helper_default_is_not_runtime_schedule"),
			NextRunAt:       runtimeDailyUnknown[time.Time](sourceRef, "no_runtime_export_schedule"),
			TargetCount:     runtimeDailyUnsupported[int](sourceRef, "export_target_count_not_projected"),
			NativeRefs:      []taskcenter.DailyResourceRef{},
			NativeRefCount:  runtimeDailyUnknown[int](sourceRef, "export_receipts_not_read"),
			LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, "export_receipts_not_read"),
			Occurrences:     []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnknown[int](sourceRef, "runtime_export_receipts_unavailable"),
			Issues:          []taskcenter.DailyIssue{},
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: time.Now().UTC(), Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}
