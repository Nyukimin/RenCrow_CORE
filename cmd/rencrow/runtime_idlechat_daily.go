package main

import (
	"context"
	"sort"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const idleChatDailySourceID = "runtime.idlechat-daily"

const (
	idleChatDailySeedDefinitionID  = "idlechat.daily_seed_collection"
	idleChatTopicStockDefinitionID = "idlechat.topic_stock"
	idleChatStoryDefinitionID      = "idlechat.night_story_stock"
	idleChatWatchdogDefinitionID   = "idlechat.watchdog"
)

func registerRuntimeIdleChatDailySource(deps *Dependencies, cfg *config.Config) {
	if deps == nil {
		return
	}
	configKnown := cfg != nil
	idleEnabled := configKnown && cfg.IdleChat.Enabled
	storyEnabled := configKnown && cfg.IdleChat.EpisodePreparation.EnabledValue()
	definitions := []taskcenter.DailyDefinitionDescriptor{
		{
			DefinitionID: idleChatDailySeedDefinitionID, KindID: "news.daily_seed_collection",
			Label: "Daily news seed collection", OwnerRef: "idlechat.daily_seed", ResourceType: "daily_seed_snapshot", DataScope: tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented, "application.idlechat.DailySeedCollectionSnapshot", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(idleChatDailySourceID, "idlechat.daily_seed", "configuration_definition", "config.idlechat.news_sources", "", tool.DataScopePublic, "Daily news seed configuration"),
			},
		},
		{
			DefinitionID: idleChatTopicStockDefinitionID, KindID: "idlechat.topic_stock",
			Label: "IdleChat topic stock", OwnerRef: "idlechat.topic_stock", ResourceType: "topic_stock_snapshot", DataScope: tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented, "application.idlechat.DailySeedCollectionSnapshot.word_pool", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(idleChatDailySourceID, "idlechat.topic_stock", "configuration_definition", "config.idlechat.episode_preparation.schedule", "", tool.DataScopePublic, "IdleChat topic stock window"),
			},
		},
		{
			DefinitionID: idleChatStoryDefinitionID, KindID: "idlechat.night_story",
			Label: "Night story episode stock", OwnerRef: "idlechat.night_preparation", ResourceType: "story_episode_stock", DataScope: tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented, "application.idlechat.StoryEpisodeStockSnapshot", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(idleChatDailySourceID, "idlechat.night_preparation", "configuration_definition", "config.idlechat.episode_preparation.schedule", "", tool.DataScopePublic, "Night story preparation window"),
			},
		},
		{
			DefinitionID: idleChatWatchdogDefinitionID, KindID: "ops.idlechat_watchdog",
			Label: "IdleChat watchdog", OwnerRef: "heartbeat.idlechat_watchdog", ResourceType: "watchdog_snapshot", DataScope: tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented, "application.idlechat.WatchdogSnapshot", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(idleChatDailySourceID, "heartbeat.idlechat_watchdog", "configuration_definition", "config.heartbeat.enabled", "", tool.DataScopePublic, "Heartbeat watchdog configuration"),
			},
		},
	}
	descriptor := taskcenter.DailySourceDescriptor{SourceID: idleChatDailySourceID, Definitions: definitions}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		orchestrator := deps.idleChatOrch
		seed := idlechat.DailySeedCollectionSnapshot{}
		story := idlechat.StoryEpisodeStockSnapshot{}
		watchdog := idlechat.WatchdogSnapshot{}
		if orchestrator != nil {
			seed = orchestrator.DailySeedCollectionSnapshot(now)
			story = orchestrator.StoryEpisodeStockSnapshot()
			watchdog = orchestrator.WatchdogSnapshot(now)
		}
		windowTiming := runtimeDailyTimingUnsupported("config.idlechat.episode_preparation.schedule", "native_window_not_recurrence")
		windowTiming.ScheduledStartAt = runtimeDailyNone[time.Time]("config.idlechat.episode_preparation.schedule", "window_schedule")
		if deps.idleChatNightPreparationAvailable && cfg != nil {
			schedule := cfg.IdleChat.EpisodePreparation.Schedule
			windowTiming.Timezone = runtimeDailyKnown(schedule.Timezone, "config.idlechat.episode_preparation.schedule.timezone")
			windows := []modulecore.TaskTimeWindow{{Start: schedule.Start, End: schedule.End}}
			windowTiming.AllowedWindows = runtimeDailyKnown(windows, "config.idlechat.episode_preparation.schedule.window")
		}
		seedConfigured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, "config.idlechat.enabled", "configuration_unavailable")
		seedEffective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "config.idlechat.enabled", "configuration_unavailable")
		if configKnown {
			seedConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, "config.idlechat.enabled", "idlechat_disabled")
			seedEffective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, "config.idlechat.enabled", "idlechat_disabled")
			if idleEnabled {
				seedConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, "config.idlechat.news_sources", "")
				if orchestrator == nil {
					seedEffective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "idlechat.daily_seed_scheduler", "orchestrator_unavailable")
				} else {
					seedEffective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "idlechat.daily_seed_scheduler", "")
				}
			}
		}
		seedObserved := runtimeDailyAxis(taskcenter.DailyObservedUnknown, "idlechat.daily_seed_cache", "snapshot_unavailable")
		seedNext := runtimeDailyUnknown[time.Time]("idlechat.daily_seed_snapshot.next_run_at", "scheduler_unavailable")
		seedEvidence := runtimeDailyUnknownEvidence("idlechat.daily_seed_cache", "no_cache_evidence")
		if orchestrator != nil {
			if seed.Status == "ready" {
				seedObserved = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "idlechat.daily_seed_cache", "")
				if seed.FetchedAt != nil && !seed.FetchedAt.IsZero() {
					seedEvidence = taskcenter.DailyEvidence{
						State:         taskcenter.DailyValueKnown,
						LastAttemptAt: runtimeDailyUnknown[time.Time]("idlechat.daily_seed_cache", "attempt_receipt_unavailable"),
						LastSuccessAt: runtimeDailyUnknown[time.Time]("idlechat.daily_seed_cache", "cache_timestamp_is_not_run_success"),
						LastResult:    runtimeDailyUnknown[taskcenter.DailyOutcome]("idlechat.daily_seed_cache", "cache_is_not_run_receipt"),
						Refs:          []taskcenter.DailyResourceRef{runtimeDailyResourceRef(idleChatDailySourceID, "idlechat.daily_seed", "cache_snapshot", "idlechat.daily_seed_cache", "", tool.DataScopePublic, "Daily seed cache")},
					}
				}
			} else if seed.Status == "empty" {
				seedObserved = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "idlechat.daily_seed_cache", "cache_empty")
			} else {
				seedObserved = runtimeDailyAxis(taskcenter.DailyObservedUnknown, "idlechat.daily_seed_cache", "snapshot_state_unknown")
			}
			if idleEnabled && !seed.NextRunAt.IsZero() {
				seedNext = runtimeDailyKnown(seed.NextRunAt.UTC(), "idlechat.daily_seed_snapshot.next_run_at")
			} else if configKnown && !idleEnabled {
				seedNext = runtimeDailyNone[time.Time]("config.idlechat.enabled", "idlechat_disabled")
			}
		} else if configKnown && !idleEnabled {
			seedObserved = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "config.idlechat.enabled", "idlechat_disabled")
			seedNext = runtimeDailyNone[time.Time]("config.idlechat.enabled", "idlechat_disabled")
		}
		seedTiming := runtimeDailyTimingUnsupported("idlechat.daily_seed_snapshot.schedule", "native_daily_schedule_not_normalized")
		seedTiming.ScheduledStartAt = runtimeDailyNone[time.Time]("idlechat.daily_seed_snapshot.schedule", "next_run_is_separate")
		if configKnown && !idleEnabled {
			seedTiming.Timezone = runtimeDailyNone[string]("config.idlechat.enabled", "idlechat_disabled")
			seedTiming.Recurrence = runtimeDailyNone[modulecore.TaskRecurrence]("config.idlechat.enabled", "idlechat_disabled")
		} else if idleEnabled && orchestrator != nil {
			seedTiming.Timezone = runtimeDailyKnown("JST", "idlechat.daily_seed_snapshot.timezone")
			seedTiming.Recurrence = runtimeDailyKnown(modulecore.TaskRecurrence{Frequency: "daily", Times: []string{seed.Schedule}}, "idlechat.daily_seed_snapshot.schedule")
		} else {
			seedTiming.Timezone = runtimeDailyUnknown[string]("idlechat.daily_seed_snapshot", "scheduler_unavailable")
			seedTiming.Recurrence = runtimeDailyUnknown[modulecore.TaskRecurrence]("idlechat.daily_seed_snapshot", "scheduler_unavailable")
		}
		seedObservation := taskcenter.DailyObservation{
			DefinitionID: idleChatDailySeedDefinitionID,
			Configured:   seedConfigured,
			Effective:    seedEffective,
			Observed:     seedObserved,
			Timing:       seedTiming,
			NextRunAt:    seedNext,
			TargetCount:  runtimeDailyUnknown[int]("idlechat.daily_seed_snapshot", "target_not_exposed"),
			SourceRefs: []taskcenter.DailyResourceRef{runtimeDailyResourceRef(
				idleChatDailySourceID, "idlechat.daily_seed", "cache_snapshot", "idlechat.daily_seed_cache", "", tool.DataScopePublic, "Daily seed cache")},
			NativeRefs:      []taskcenter.DailyResourceRef{},
			NativeRefCount:  runtimeDailyUnknown[int]("idlechat.daily_seed_cache", "cache_item_references_not_projected"),
			LatestEvidence:  seedEvidence,
			Occurrences:     []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnsupported[int]("idlechat.daily_seed_cache", "cache_items_are_not_occurrences"),
		}
		topicConfigured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, "config.idlechat.enabled", "configuration_unavailable")
		topicEffective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "config.idlechat.enabled", "configuration_unavailable")
		if configKnown {
			topicConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, "config.idlechat.enabled", "idlechat_disabled")
			topicEffective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, "config.idlechat.enabled", "idlechat_disabled")
			if idleEnabled {
				topicConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, "config.idlechat.news_sources", "")
				if deps.idleChatNightPreparationAvailable && orchestrator != nil {
					topicEffective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "idlechat.night_preparation_scheduler", "")
				} else {
					topicEffective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "idlechat.night_preparation_scheduler", "scheduler_unavailable")
				}
			}
		}
		topicObserved := runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "idlechat.topic_stock", "runtime_unavailable")
		topicTarget := runtimeDailyUnknown[int]("idlechat.topic_stock", "stock_snapshot_unavailable")
		if orchestrator != nil {
			topicObserved = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "idlechat.daily_seed_snapshot.word_pool", "")
			topicTarget = runtimeDailyKnown(seed.WordPool.Limit, "idlechat.daily_seed_snapshot.word_pool.limit")
		}
		topicObservation := taskcenter.DailyObservation{
			DefinitionID: idleChatTopicStockDefinitionID,
			Configured:   topicConfigured, Effective: topicEffective, Observed: topicObserved,
			Timing: windowTiming, NextRunAt: runtimeDailyUnknown[time.Time]("idlechat.topic_stock", "next_refill_not_exposed"),
			TargetCount: topicTarget,
			SourceRefs:  []taskcenter.DailyResourceRef{runtimeDailyResourceRef(idleChatDailySourceID, "idlechat.topic_stock", "word_pool_snapshot", "idlechat.daily_seed_snapshot.word_pool", "", tool.DataScopePublic, "IdleChat topic stock")},
			NativeRefs:  []taskcenter.DailyResourceRef{}, NativeRefCount: runtimeDailyUnknown[int]("idlechat.topic_stock", "word_pool_item_references_not_projected"),
			LatestEvidence: seedEvidence, Occurrences: []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnsupported[int]("idlechat.topic_stock", "stock_snapshot_is_not_occurrence_history"),
		}
		storyConfigured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, "config.idlechat.episode_preparation.enabled", "configuration_unavailable")
		storyEffective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "config.idlechat.episode_preparation.enabled", "configuration_unavailable")
		if configKnown {
			storyConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, "config.idlechat.episode_preparation.enabled", "story_preparation_disabled")
			storyEffective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, "config.idlechat.episode_preparation.enabled", "story_preparation_disabled")
			if storyEnabled {
				storyConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, "config.idlechat.episode_preparation", "")
				switch {
				case !idleEnabled:
					storyEffective = runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "config.idlechat.enabled", "idlechat_disabled")
				case orchestrator == nil:
					storyEffective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "idlechat.night_preparation_scheduler", "orchestrator_unavailable")
				case !deps.idleChatNightPreparationAvailable:
					storyEffective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "idlechat.night_preparation_scheduler", "scheduler_unavailable")
				case !story.Enabled:
					storyEffective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "idlechat.story_episode_stock", "story_stock_unavailable")
				default:
					storyEffective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "idlechat.night_preparation_scheduler", "")
				}
			}
		}
		storyObserved := runtimeDailyAxis(taskcenter.DailyObservedUnknown, "idlechat.story_episode_stock", "snapshot_unavailable")
		storyTarget := runtimeDailyUnknown[int]("idlechat.story_episode_stock", "snapshot_unavailable")
		storyRefs := []taskcenter.DailyResourceRef{}
		storyRefCount := 0
		storyRefsTruncated := false
		storyEvidence := runtimeDailyUnknownEvidence("idlechat.story_episode_stock", "no_artifact_evidence")
		if orchestrator != nil {
			if story.Enabled {
				storyObserved = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "idlechat.story_episode_stock", "")
				storyTarget = runtimeDailyKnown(story.Target, "idlechat.story_episode_stock.target")
				storyRefs, storyRefCount, storyRefsTruncated, storyEvidence = projectStoryEpisodeStock(story)
			} else if configKnown && !storyEnabled {
				storyObserved = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "idlechat.story_episode_stock", "feature_disabled")
			} else {
				storyObserved = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "idlechat.story_episode_stock", "snapshot_unavailable")
			}
		}
		storyTiming := runtimeDailyTimingUnsupported("config.idlechat.episode_preparation.schedule", "night_window_not_recurrence")
		storyTiming.ScheduledStartAt = runtimeDailyNone[time.Time]("config.idlechat.episode_preparation.schedule", "window_schedule")
		if storyEnabled && deps.idleChatNightPreparationAvailable {
			schedule := cfg.IdleChat.EpisodePreparation.Schedule
			storyTiming.Timezone = runtimeDailyKnown(schedule.Timezone, "config.idlechat.episode_preparation.schedule.timezone")
			windows := []modulecore.TaskTimeWindow{{Start: schedule.Start, End: schedule.End}}
			storyTiming.AllowedWindows = runtimeDailyKnown(windows, "config.idlechat.episode_preparation.schedule.window")
		} else if configKnown && !storyEnabled {
			storyTiming.Timezone = runtimeDailyNone[string]("config.idlechat.episode_preparation.enabled", "story_preparation_disabled")
			storyTiming.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow]("config.idlechat.episode_preparation.enabled", "story_preparation_disabled")
		} else {
			storyTiming.Timezone = runtimeDailyUnknown[string]("config.idlechat.episode_preparation.schedule", "schedule_unavailable")
			storyTiming.AllowedWindows = runtimeDailyUnknown[[]modulecore.TaskTimeWindow]("config.idlechat.episode_preparation.schedule", "schedule_unavailable")
		}
		storyObservation := taskcenter.DailyObservation{
			DefinitionID: idleChatStoryDefinitionID,
			Configured:   storyConfigured, Effective: storyEffective, Observed: storyObserved,
			Timing: storyTiming, NextRunAt: runtimeDailyUnknown[time.Time]("idlechat.night_preparation_scheduler", "next_run_not_exposed"),
			TargetCount: storyTarget, NativeRefs: storyRefs, NativeRefCount: runtimeDailyUnknown[int]("idlechat.story_episode_stock", "artifact_reference_count_not_read"),
			NativeRefsTruncated: storyRefsTruncated, LatestEvidence: storyEvidence,
			Occurrences: []taskcenter.DailyOccurrence{}, OccurrenceCount: runtimeDailyUnknown[int]("taskmanager.run", "canonical_execution_outcome_not_joined"),
			OccurrencesTruncated: false,
		}
		if orchestrator != nil && story.Enabled {
			storyObservation.NativeRefCount = runtimeDailyKnown(storyRefCount, "idlechat.story_episode_stock.native_refs.count")
		}
		watchdogConfigured := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, "config.heartbeat.enabled", "configuration_unavailable")
		watchdogEffective := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "config.heartbeat.enabled", "configuration_unavailable")
		if configKnown {
			watchdogConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, "config.heartbeat.enabled", "heartbeat_disabled")
			watchdogEffective = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, "config.heartbeat.enabled", "heartbeat_disabled")
			if cfg.Heartbeat.Enabled {
				watchdogConfigured = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, "config.heartbeat.enabled", "")
				if deps.heartbeatSvc != nil && orchestrator != nil {
					watchdogEffective = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "heartbeat.idlechat_watchdog", "")
				} else {
					watchdogEffective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "heartbeat.idlechat_watchdog", "monitor_unavailable")
				}
			}
		}
		watchdogObserved := runtimeDailyAxis(taskcenter.DailyObservedUnknown, "idlechat.watchdog_snapshot", "runtime_unavailable")
		if orchestrator != nil {
			watchdogObserved = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "idlechat.watchdog_snapshot", "")
		} else if configKnown && !cfg.Heartbeat.Enabled {
			watchdogObserved = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "config.heartbeat.enabled", "heartbeat_disabled")
		}
		watchdogObservation := taskcenter.DailyObservation{
			DefinitionID: idleChatWatchdogDefinitionID,
			Configured:   watchdogConfigured, Effective: watchdogEffective, Observed: watchdogObserved,
			Timing:      runtimeDailyTimingUnsupported("heartbeat.idlechat_watchdog", "poll_loop_not_daily_schedule"),
			NextRunAt:   runtimeDailyUnsupported[time.Time]("heartbeat.idlechat_watchdog", "poller_is_not_task_occurrence"),
			TargetCount: runtimeDailyUnsupported[int]("idlechat.watchdog_snapshot", "monitor_not_target_count"),
			NativeRefs:  []taskcenter.DailyResourceRef{}, NativeRefCount: runtimeDailyUnknown[int]("idlechat.watchdog_snapshot", "native_snapshot_references_not_projected"),
			LatestEvidence: runtimeDailyUnknownEvidence("idlechat.watchdog_snapshot", "snapshot_is_not_run_receipt"),
			Occurrences:    []taskcenter.DailyOccurrence{}, OccurrenceCount: runtimeDailyUnsupported[int]("idlechat.watchdog_snapshot", "snapshot_is_not_occurrence_history"),
		}
		if orchestrator != nil && watchdog.ChatActive && !watchdog.UpdatedAt.IsZero() {
			watchdogObservation.LatestEvidence = taskcenter.DailyEvidence{
				State:         taskcenter.DailyValueKnown,
				LastAttemptAt: runtimeDailyUnknown[time.Time]("idlechat.watchdog_snapshot", "watchdog_has_no_run_receipt"),
				LastSuccessAt: runtimeDailyUnknown[time.Time]("idlechat.watchdog_snapshot", "watchdog_is_not_work_success"),
				LastResult:    runtimeDailyUnknown[taskcenter.DailyOutcome]("idlechat.watchdog_snapshot", "monitor_state_not_task_result"),
				Refs:          []taskcenter.DailyResourceRef{runtimeDailyResourceRef(idleChatDailySourceID, "heartbeat.idlechat_watchdog", "watchdog_snapshot", "idlechat.watchdog_snapshot", "", tool.DataScopePublic, "IdleChat watchdog state")},
			}
		}
		return taskcenter.DailySourceSnapshot{
			ObservedAt:   now,
			Observations: []taskcenter.DailyObservation{seedObservation, topicObservation, storyObservation, watchdogObservation},
			Issues:       []taskcenter.DailyIssue{},
		}, nil
	}))
}

func projectStoryEpisodeStock(snapshot idlechat.StoryEpisodeStockSnapshot) ([]taskcenter.DailyResourceRef, int, bool, taskcenter.DailyEvidence) {
	artifacts := append([]idlechat.StoryEpisodeArtifact(nil), snapshot.Episodes...)
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].UpdatedAt.After(artifacts[j].UpdatedAt) })
	totalRefs := storyNativeRefCount(artifacts)
	refs := make([]taskcenter.DailyResourceRef, 0, min(totalRefs, taskcenter.DailyMaxNativeRefs))
	for _, artifact := range artifacts {
		appendBoundedDailyRef(&refs, runtimeDailyResourceRef(idleChatDailySourceID, "idlechat.night_preparation", "story_episode_artifact", "idlechat.story_episode", artifact.EpisodeID, tool.DataScopePublic, "Story episode artifact"))
		if artifact.TaskID != "" {
			appendBoundedDailyRef(&refs, runtimeDailyResourceRef(idleChatDailySourceID, "taskmanager.task", "task_manager_task", "taskmanager.task", string(artifact.TaskID), tool.DataScopePublic, "TaskManager task referenced by story artifact"))
		}
		if artifact.RunID != "" {
			appendBoundedDailyRef(&refs, runtimeDailyResourceRef(idleChatDailySourceID, "taskmanager.run", "task_manager_run", "taskmanager.run", string(artifact.RunID), tool.DataScopePublic, "TaskManager run referenced by story artifact"))
		}
	}
	return refs, totalRefs, len(refs) < totalRefs, runtimeDailyUnknownEvidence("taskmanager.run", "canonical_run_outcome_not_joined")
}

func storyNativeRefCount(artifacts []idlechat.StoryEpisodeArtifact) int {
	total := 0
	for _, artifact := range artifacts {
		total++ // persisted story artifact
		if artifact.TaskID != "" {
			total++
		}
		if artifact.RunID != "" {
			total++
		}
	}
	return total
}

func appendBoundedDailyRef(refs *[]taskcenter.DailyResourceRef, ref taskcenter.DailyResourceRef) {
	if refs == nil || len(*refs) >= taskcenter.DailyMaxNativeRefs {
		return
	}
	*refs = append(*refs, ref)
}
