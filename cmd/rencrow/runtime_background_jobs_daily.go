package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	movieBackfillDailySourceID     = "runtime.movie-catalog-backfill"
	movieBackfillDailyDefinitionID = "movie_catalog.backfill"
)

// registerRuntimeMovieCatalogBackfillDailySource snapshots the state returned
// by the existing startup gate and projects configured cadence only. It does
// not query, start, retry, or collect catalog work.
func registerRuntimeMovieCatalogBackfillDailySource(deps *Dependencies, cfg *config.Config, started bool) {
	if deps == nil {
		return
	}
	const owner = "movie_catalog.backfill"
	const sourceRef = "movie_catalog.backfill.runtime"
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: movieBackfillDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: movieBackfillDailyDefinitionID,
			KindID:       "movie_catalog.backfill",
			Label:        "Movie catalog background enrichment",
			OwnerRef:     owner,
			ResourceType: "background_job",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented,
				"application.moviecatalog.BackfillService", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(movieBackfillDailySourceID, owner, "runtime_configuration", "config.movie_catalog.crawler_url", "", tool.DataScopePublic, "Movie catalog backfill configuration"),
				runtimeDailyResourceRef(movieBackfillDailySourceID, owner, "runtime_configuration", "env.RENCROW_MOVIE_CATALOG_BACKFILL", "", tool.DataScopePublic, "Movie catalog backfill startup gate"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		configuredAxis := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
		effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
		disabled := cfg != nil && movieCatalogBackfillDisabled()
		prerequisites := false
		if cfg != nil {
			prerequisites = resolveMovieCatalogBackfillDBPath(cfg.Storage.Databases.MovieCatalog) != "" && resolveMovieCatalogCrawlerBaseURL(cfg) != ""
			if disabled {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "environment_disabled")
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "environment_disabled")
			} else if prerequisites {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
				if started {
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, sourceRef, "")
				} else {
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "startup_gate_not_satisfied")
				}
			} else {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredUnconfigured, sourceRef, "required_catalog_or_crawler_missing")
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "required_catalog_or_crawler_missing")
			}
		}
		timing := runtimeDailyTimingUnsupported(sourceRef, "no_wall_clock_schedule")
		timing.Timezone = runtimeDailyNone[string](sourceRef, "interval_schedule_has_no_timezone")
		timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "interval_schedule_has_no_fixed_start")
		timing.NotBefore = runtimeDailyUnknown[time.Time](sourceRef, "startup_initial_delay_not_an_absolute_time")
		timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](sourceRef, "no_time_window_gate")
		timing.DeadlineAt = runtimeDailyNone[time.Time](sourceRef, "no_task_deadline")
		timing.EstimatedDurationSeconds = runtimeDailyUnsupported[int64](sourceRef, "no_duration_estimate")
		if cfg == nil {
			timing.Recurrence = runtimeDailyUnknown[modulecore.TaskRecurrence](sourceRef, "configuration_unavailable")
		} else {
			interval := movieCatalogBackfillDurationEnv("RENCROW_MOVIE_CATALOG_BACKFILL_INTERVAL_SEC", 5*time.Minute, time.Minute)
			seconds := int64(interval / time.Second)
			timing.Recurrence = runtimeDailyKnown(modulecore.TaskRecurrence{Frequency: "interval", IntervalSeconds: &seconds}, "env.RENCROW_MOVIE_CATALOG_BACKFILL_INTERVAL_SEC")
		}
		observation := taskcenter.DailyObservation{
			DefinitionID:    movieBackfillDailyDefinitionID,
			Configured:      configuredAxis,
			Effective:       effectiveAxis,
			Observed:        runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "no_durable_run_receipt"),
			Timing:          timing,
			NextRunAt:       runtimeDailyUnknown[time.Time](sourceRef, "next_background_tick_not_exposed"),
			TargetCount:     runtimeDailyUnknown[int](sourceRef, "backfill_target_count_not_exposed"),
			NativeRefs:      []taskcenter.DailyResourceRef{},
			NativeRefCount:  runtimeDailyUnknown[int](sourceRef, "no_native_run_receipt"),
			LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, "no_durable_run_receipt"),
			Occurrences:     []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnknown[int](sourceRef, "no_durable_run_receipt"),
			Issues:          []taskcenter.DailyIssue{},
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}
