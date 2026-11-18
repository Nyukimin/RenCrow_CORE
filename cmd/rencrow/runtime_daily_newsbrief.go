package main

import (
	"context"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domainnews "github.com/Nyukimin/RenCrow_CORE/internal/domain/newsbrief"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	dailyNewsBriefSourceID        = "runtime.daily-news-brief"
	dailyNewsDatabaseDefinitionID = "newsdb.daily_items"
	dailyMorningBriefDefinitionID = "newsbrief.daily_morning"
	dailyNewsBriefReadLimit       = 200 // matches L1Reader.Read's bounded RecentNewsItems query
)

func registerRuntimeDailyNewsBriefSource(deps *Dependencies, reader domainnews.DailyNewsBriefReader) {
	if deps == nil {
		return
	}
	sourceRef := "conversation.l1.news_items"
	definitions := []taskcenter.DailyDefinitionDescriptor{
		{
			DefinitionID: dailyNewsDatabaseDefinitionID, KindID: "news.newsdb",
			Label: "NewsDB current-day items", OwnerRef: "conversation.l1.news_items", ResourceType: "news_item_set", DataScope: tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented, "conversation.l1.news_items", ""),
			SourceRefs:     []taskcenter.DailyResourceRef{runtimeDailyResourceRef(dailyNewsBriefSourceID, "conversation.l1.news_items", "readonly_query", "conversation.l1.news_items", "", tool.DataScopePublic, "Current-day NewsDB items")},
		},
		{
			DefinitionID: dailyMorningBriefDefinitionID, KindID: "news.daily_brief",
			Label: "Daily news brief reader projection", OwnerRef: "newsbrief.daily_morning", ResourceType: "daily_news_brief_read_projection", DataScope: tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented, "application.newsbrief.L1Reader", ""),
			SourceRefs:     []taskcenter.DailyResourceRef{runtimeDailyResourceRef(dailyNewsBriefSourceID, "newsbrief.daily_morning", "readonly_query", "newsbrief.daily_morning", "", tool.DataScopePublic, "Morning brief reader")},
		},
	}
	readerConfigured := reader != nil
	descriptor := taskcenter.DailySourceDescriptor{SourceID: dailyNewsBriefSourceID, Definitions: definitions}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		now := time.Now().UTC()
		configuredState := taskcenter.DailyConfiguredUnconfigured
		effectiveState := taskcenter.DailyEffectiveUnavailable
		configuredReason := "persistent_news_reader_unavailable"
		effectiveReason := "persistent_news_reader_unavailable"
		if readerConfigured {
			configuredState = taskcenter.DailyConfiguredConfigured
			effectiveState = taskcenter.DailyEffectiveUnsupported
			configuredReason = ""
			effectiveReason = "request_path_has_no_daily_scheduler"
		}
		if reader == nil {
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{
				unavailableDailyNewsObservation(dailyNewsDatabaseDefinitionID, configuredState, effectiveState, sourceRef, configuredReason, effectiveReason),
				unavailableDailyNewsObservation(dailyMorningBriefDefinitionID, configuredState, effectiveState, sourceRef, configuredReason, effectiveReason),
			}, Issues: []taskcenter.DailyIssue{}}, nil
		}
		brief, err := reader.Read(ctx, now)
		if err != nil {
			if ctx.Err() != nil {
				return taskcenter.DailySourceSnapshot{}, ctx.Err()
			}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{
				unavailableDailyNewsObservation(dailyNewsDatabaseDefinitionID, taskcenter.DailyConfiguredConfigured, taskcenter.DailyEffectiveUnavailable, sourceRef, "", "news_reader_unavailable"),
				unavailableDailyNewsObservation(dailyMorningBriefDefinitionID, taskcenter.DailyConfiguredConfigured, taskcenter.DailyEffectiveUnavailable, sourceRef, "", "news_reader_unavailable"),
			}, Issues: []taskcenter.DailyIssue{}}, nil
		}
		refs := make([]taskcenter.DailyResourceRef, 0, min(len(brief.Items), taskcenter.DailyMaxNativeRefs))
		for _, item := range brief.Items[:min(len(brief.Items), taskcenter.DailyMaxNativeRefs)] {
			refs = append(refs, runtimeDailyResourceRef(dailyNewsBriefSourceID, "conversation.l1.news_items", "news_item", "conversation.l1.news_item", item.ID, tool.DataScopePublic, "NewsDB item"))
		}
		count := runtimeDailyKnown(len(brief.Items), "conversation.l1.news_items.count")
		countTruncated := len(brief.Items) > len(refs) || len(brief.Items) >= dailyNewsBriefReadLimit
		if len(brief.Items) >= dailyNewsBriefReadLimit {
			count = runtimeDailyUnknown[int]("conversation.l1.news_items.count", "native_query_limit_reached")
		}
		newsObserved := runtimeDailyAxis(taskcenter.DailyObservedAvailable, sourceRef, "")
		briefObserved := runtimeDailyAxis(taskcenter.DailyObservedAvailable, "newsbrief.daily_morning.reader", "")
		evidence := runtimeDailyUnknownEvidence(sourceRef, "daily_run_receipt_unavailable")
		newsTiming := runtimeDailyTimingUnsupported("conversation.l1.news_items", "collection_schedule_not_owned_here")
		newsTiming.ScheduledStartAt = runtimeDailyNone[time.Time]("conversation.l1.news_items", "read_projection_without_schedule")
		newsTiming.NotBefore = runtimeDailyNone[time.Time]("conversation.l1.news_items", "no_not_before_gate")
		newsTiming.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow]("conversation.l1.news_items", "no_time_window_gate")
		newsTiming.DeadlineAt = runtimeDailyNone[time.Time]("conversation.l1.news_items", "no_task_deadline")
		briefTiming := runtimeDailyTimingUnsupported("newsbrief.daily_morning", "brief_is_read_projection")
		briefTiming.ScheduledStartAt = runtimeDailyNone[time.Time]("newsbrief.daily_morning", "read_projection_without_fixed_start")
		briefTiming.NotBefore = runtimeDailyNone[time.Time]("newsbrief.daily_morning", "no_not_before_gate")
		briefTiming.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow]("newsbrief.daily_morning", "no_time_window_gate")
		briefTiming.DeadlineAt = runtimeDailyNone[time.Time]("newsbrief.daily_morning", "no_task_deadline")
		newsObservation := taskcenter.DailyObservation{
			DefinitionID: dailyNewsDatabaseDefinitionID,
			Configured:   runtimeDailyAxis(configuredState, "conversation.l1.news_items", configuredReason),
			Effective:    runtimeDailyAxis(effectiveState, "conversation.l1.news_items", effectiveReason),
			Observed:     newsObserved, Timing: newsTiming,
			NextRunAt:   runtimeDailyUnknown[time.Time]("conversation.l1.news_items", "collection_next_run_unavailable"),
			TargetCount: runtimeDailyUnsupported[int]("conversation.l1.news_items", "item_count_is_not_target_count"),
			SourceRefs:  []taskcenter.DailyResourceRef{runtimeDailyResourceRef(dailyNewsBriefSourceID, "conversation.l1.news_items", "readonly_query", sourceRef, "", tool.DataScopePublic, "Current-day NewsDB items")},
			NativeRefs:  refs, NativeRefCount: count, NativeRefsTruncated: countTruncated,
			LatestEvidence: evidence, Occurrences: []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnsupported[int]("conversation.l1.news_items", "item_history_is_not_run_occurrence_history"),
		}
		briefObservation := taskcenter.DailyObservation{
			DefinitionID: dailyMorningBriefDefinitionID,
			Configured:   runtimeDailyAxis(configuredState, "newsbrief.daily_morning.reader", configuredReason),
			Effective:    runtimeDailyAxis(effectiveState, "newsbrief.daily_morning.reader", effectiveReason),
			Observed:     briefObserved, Timing: briefTiming,
			NextRunAt:   runtimeDailyUnknown[time.Time]("newsbrief.daily_morning", "read_projection_without_scheduler"),
			TargetCount: runtimeDailyUnsupported[int]("newsbrief.daily_morning", "brief_has_no_target_count"),
			SourceRefs:  []taskcenter.DailyResourceRef{runtimeDailyResourceRef(dailyNewsBriefSourceID, "newsbrief.daily_morning", "readonly_query", "newsbrief.daily_morning", "", tool.DataScopePublic, "Morning brief reader")},
			NativeRefs:  append([]taskcenter.DailyResourceRef(nil), refs...), NativeRefCount: count, NativeRefsTruncated: countTruncated,
			LatestEvidence: evidence, Occurrences: []taskcenter.DailyOccurrence{},
			OccurrenceCount: runtimeDailyUnsupported[int]("newsbrief.daily_morning", "brief_read_is_not_daily_run_history"),
		}
		if brief.Status == domainnews.StatusPartial {
			newsObservation.Issues = []taskcenter.DailyIssue{{OwnerRef: "conversation.l1.news_items", Code: taskcenter.DailyIssueSourceReported}}
			briefObservation.Issues = []taskcenter.DailyIssue{{OwnerRef: "newsbrief.daily_morning", Code: taskcenter.DailyIssueSourceReported}}
		}
		return taskcenter.DailySourceSnapshot{
			ObservedAt: now, Observations: []taskcenter.DailyObservation{newsObservation, briefObservation},
			Issues: []taskcenter.DailyIssue{},
		}, nil
	}))
}

func unavailableDailyNewsObservation(definitionID string, configuredState, effectiveState taskcenter.DailyAxisState, sourceRef, configuredReason, effectiveReason string) taskcenter.DailyObservation {
	ownerRef := "conversation.l1.news_items"
	if definitionID == dailyMorningBriefDefinitionID {
		ownerRef = "newsbrief.daily_morning"
	}
	return taskcenter.DailyObservation{
		DefinitionID: definitionID,
		Configured:   runtimeDailyAxis(configuredState, sourceRef, configuredReason),
		Effective:    runtimeDailyAxis(effectiveState, sourceRef, effectiveReason),
		Observed:     runtimeDailyAxis(taskcenter.DailyObservedUnavailable, sourceRef, effectiveReason),
		Timing:       runtimeDailyTimingUnsupported(sourceRef, "schedule_not_exposed"),
		NextRunAt:    runtimeDailyUnknown[time.Time](sourceRef, effectiveReason),
		TargetCount:  runtimeDailyUnknown[int](sourceRef, effectiveReason),
		SourceRefs:   []taskcenter.DailyResourceRef{}, NativeRefs: []taskcenter.DailyResourceRef{},
		NativeRefCount:  runtimeDailyUnknown[int](sourceRef, effectiveReason),
		LatestEvidence:  runtimeDailyUnknownEvidence(sourceRef, effectiveReason),
		Occurrences:     []taskcenter.DailyOccurrence{},
		OccurrenceCount: runtimeDailyUnsupported[int](sourceRef, "no_occurrence_history_port"),
		Issues:          []taskcenter.DailyIssue{{OwnerRef: ownerRef, Code: taskcenter.DailyIssueSourceReported}},
	}
}
