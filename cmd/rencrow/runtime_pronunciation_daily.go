package main

import (
	"context"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	pronunciationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/pronunciationcheck"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	pronunciationDailySourceID     = "runtime.tts-pronunciation-check"
	pronunciationDailyDefinitionID = "tts.pronunciation_check"
	pronunciationScheduleReadLimit = 1000
	pronunciationRunReadLimit      = 1000
)

func registerRuntimePronunciationDailySource(deps *Dependencies, cfg *config.Config) {
	if deps == nil {
		return
	}
	configured := cfg != nil && cfg.TTS.PronunciationCheck.Enabled
	const sourceRef = "config.tts.pronunciation_check"
	descriptor := taskcenter.DailySourceDescriptor{
		SourceID: pronunciationDailySourceID,
		Definitions: []taskcenter.DailyDefinitionDescriptor{{
			DefinitionID: pronunciationDailyDefinitionID,
			KindID:       "quality.tts_pronunciation_check",
			Label:        "TTS pronunciation check",
			OwnerRef:     "tts.pronunciation_check",
			ResourceType: "scheduler_schedule",
			DataScope:    tool.DataScopePublic,
			Implementation: runtimeDailyAxis(taskcenter.DailyImplementationImplemented,
				"application.pronunciationcheck", ""),
			SourceRefs: []taskcenter.DailyResourceRef{
				runtimeDailyResourceRef(pronunciationDailySourceID, "tts.pronunciation_check", "configuration_definition", sourceRef, "", tool.DataScopePublic, "Pronunciation check feature configuration"),
				runtimeDailyResourceRef(pronunciationDailySourceID, "tts.pronunciation_check", "readonly_query", "scheduler.schedule", "", tool.DataScopePublic, "Pronunciation check scheduler records"),
			},
		}},
	}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, _ tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		now := time.Now().UTC()
		configuredAxis := runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable")
		effectiveAxis := runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
		if cfg != nil {
			configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "feature_disabled")
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "feature_disabled")
			if configured {
				configuredAxis = runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
				if deps.pronunciationCheckCancel == nil {
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, sourceRef, "scheduler_runtime_not_started")
				} else {
					effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "scheduler.schedule", "matching_schedule_not_observed")
				}
			}
		}
		observation := basePronunciationDailyObservation(configuredAxis, effectiveAxis, sourceRef)
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		store := deps.schedulerStore
		if store == nil {
			if configured {
				observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "scheduler.store", "scheduler_store_unavailable")
				observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "tts.pronunciation_check", Code: taskcenter.DailyIssueSourceReported}}
			} else {
				observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, sourceRef, "feature_disabled")
			}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
		}
		schedules, err := store.ListSchedules(ctx, pronunciationScheduleReadLimit)
		if err != nil {
			if ctx.Err() != nil {
				return taskcenter.DailySourceSnapshot{}, ctx.Err()
			}
			observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "scheduler.store", "schedule_query_failed")
			observation.Issues = []taskcenter.DailyIssue{{OwnerRef: "tts.pronunciation_check", Code: taskcenter.DailyIssueSourceReported}}
			return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
		}

		ownerSchedules := make([]domainscheduler.Schedule, 0, 1)
		for _, schedule := range schedules {
			if schedule.Target == pronunciationapp.ScheduledTarget {
				ownerSchedules = append(ownerSchedules, schedule)
			}
		}
		sort.Slice(ownerSchedules, func(i, j int) bool {
			if ownerSchedules[i].NextRunAt.Equal(ownerSchedules[j].NextRunAt) {
				return ownerSchedules[i].ScheduleID < ownerSchedules[j].ScheduleID
			}
			if ownerSchedules[i].NextRunAt.IsZero() {
				return false
			}
			if ownerSchedules[j].NextRunAt.IsZero() {
				return true
			}
			return ownerSchedules[i].NextRunAt.Before(ownerSchedules[j].NextRunAt)
		})

		logs, logsErr := store.ListRunLogs(ctx, pronunciationRunReadLimit)
		logsAvailable := logsErr == nil
		if logsErr != nil && ctx.Err() != nil {
			return taskcenter.DailySourceSnapshot{}, ctx.Err()
		}
		if !logsAvailable {
			logs = nil
		}
		ownerScheduleIDs := make(map[modulecore.ScheduleID]struct{}, len(ownerSchedules))
		for _, schedule := range ownerSchedules {
			ownerScheduleIDs[schedule.ScheduleID] = struct{}{}
		}
		ownerLogs := make([]domainscheduler.RunLog, 0, len(ownerSchedules))
		for _, runLog := range logs {
			if _, ok := ownerScheduleIDs[runLog.ScheduleID]; ok {
				ownerLogs = append(ownerLogs, runLog)
			}
		}
		sort.Slice(ownerLogs, func(i, j int) bool { return ownerLogs[i].StartedAt.After(ownerLogs[j].StartedAt) })

		schedulesTruncated := len(schedules) >= pronunciationScheduleReadLimit
		logsTruncated := len(logs) >= pronunciationRunReadLimit
		configuredSchedulesEnabled := hasEnabledPronunciationSchedule(ownerSchedules)
		if configured {
			switch {
			case schedulesTruncated:
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "scheduler.schedule", "schedule_query_limit_reached")
			case !configuredSchedulesEnabled && len(ownerSchedules) > 0:
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, "scheduler.schedule", "schedule_disabled")
			case len(ownerSchedules) == 0:
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler.schedule", "matching_schedule_unavailable")
			case deps.pronunciationCheckCancel == nil:
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler", "scheduler_runtime_not_started")
			default:
				effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "scheduler.schedule", "")
			}
		}
		observation.Configured = configuredAxis
		observation.Effective = effectiveAxis
		observation.Observed = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "scheduler.store", "")
		if schedulesTruncated {
			observation.TargetCount = runtimeDailyUnknown[int]("scheduler.schedule.count", "schedule_query_limit_reached")
		} else {
			observation.TargetCount = runtimeDailyKnown(len(ownerSchedules), "scheduler.schedule.count")
		}
		refs := make([]taskcenter.DailyResourceRef, 0, min(len(ownerSchedules)+len(ownerLogs), taskcenter.DailyMaxNativeRefs))
		for _, schedule := range ownerSchedules {
			if len(refs) >= taskcenter.DailyMaxNativeRefs {
				break
			}
			refs = append(refs, runtimeDailyResourceRef(pronunciationDailySourceID, "tts.pronunciation_check", "scheduler_schedule", "scheduler.schedule", string(schedule.ScheduleID), tool.DataScopePublic, "Pronunciation check schedule"))
		}
		for _, entry := range ownerLogs {
			if len(refs) >= taskcenter.DailyMaxNativeRefs {
				break
			}
			refs = append(refs, runtimeDailyResourceRef(pronunciationDailySourceID, "tts.pronunciation_check", "scheduler_run_log", "scheduler.run_log", string(entry.RunID), tool.DataScopePublic, "Pronunciation check scheduler run"))
		}
		observation.NativeRefs = refs
		queryTruncated := schedulesTruncated || logsTruncated || !logsAvailable
		observation.NativeRefsTruncated = queryTruncated || len(ownerSchedules)+len(ownerLogs) > len(refs)
		if queryTruncated {
			reason := "native_query_limit_reached"
			if !logsAvailable {
				reason = "run_log_query_failed"
			}
			observation.NativeRefCount = runtimeDailyUnknown[int]("scheduler.store", reason)
			observation.OccurrenceCount = runtimeDailyUnknown[int]("scheduler.run_log", reason)
			observation.OccurrencesTruncated = queryTruncated
		} else {
			observation.NativeRefCount = runtimeDailyKnown(len(ownerSchedules)+len(ownerLogs), "scheduler.store.references.count")
			observation.OccurrenceCount = runtimeDailyKnown(len(ownerLogs), "scheduler.run_log.count")
		}
		if queryTruncated || configured && len(ownerSchedules) == 0 {
			observation.Issues = append(observation.Issues, taskcenter.DailyIssue{OwnerRef: "tts.pronunciation_check", Code: taskcenter.DailyIssueSourceReported})
		}
		if len(ownerLogs) > 0 && logsAvailable {
			observation.LatestEvidence = pronunciationDailyEvidence(ownerLogs[0])
		} else if !logsAvailable {
			observation.LatestEvidence = runtimeDailyUnknownEvidence("scheduler.run_log", "run_log_query_failed")
		}
		observation.Occurrences = pronunciationDailyOccurrences(ownerLogs, taskcenter.DailyMaxOccurrences)
		observation.OccurrencesTruncated = observation.OccurrencesTruncated || len(ownerLogs) > len(observation.Occurrences)
		observation.NextRunAt = pronunciationDailyNextRun(ownerSchedules, configured, schedulesTruncated)
		if schedulesTruncated {
			observation.Timing = pronunciationDailyTiming(nil, sourceRef)
		} else {
			observation.Timing = pronunciationDailyTiming(ownerSchedules, sourceRef)
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}

func basePronunciationDailyObservation(configured, effective taskcenter.DailyAxis, sourceRef string) taskcenter.DailyObservation {
	timing := pronunciationDailyTiming(nil, sourceRef)
	return taskcenter.DailyObservation{
		DefinitionID:         pronunciationDailyDefinitionID,
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
		LatestEvidence:       runtimeDailyUnknownEvidence("scheduler.run_log", "run_receipt_unavailable"),
		Occurrences:          []taskcenter.DailyOccurrence{},
		OccurrenceCount:      runtimeDailyUnknown[int]("scheduler.run_log", "run_history_unavailable"),
		OccurrencesTruncated: false,
		Issues:               []taskcenter.DailyIssue{},
	}
}

func pronunciationDailyNextRun(schedules []domainscheduler.Schedule, configured, truncated bool) taskcenter.DailyValue[time.Time] {
	if !configured {
		return runtimeDailyNone[time.Time]("config.tts.pronunciation_check", "feature_disabled")
	}
	if truncated {
		return runtimeDailyUnknown[time.Time]("scheduler.schedule.next_run_at", "schedule_query_limit_reached")
	}
	var earliest time.Time
	for _, schedule := range schedules {
		if !schedule.Enabled {
			continue
		}
		if schedule.NextRunAt.IsZero() {
			return runtimeDailyUnknown[time.Time]("scheduler.schedule.next_run_at", "enabled_schedule_has_no_next_run")
		}
		if earliest.IsZero() || schedule.NextRunAt.Before(earliest) {
			earliest = schedule.NextRunAt
		}
	}
	if earliest.IsZero() {
		if len(schedules) > 0 {
			return runtimeDailyNone[time.Time]("scheduler.schedule.next_run_at", "all_matching_schedules_disabled")
		}
		return runtimeDailyUnknown[time.Time]("scheduler.schedule.next_run_at", "matching_schedule_unavailable")
	}
	return runtimeDailyKnown(earliest.UTC(), "scheduler.schedule.next_run_at")
}

func pronunciationDailyTiming(schedules []domainscheduler.Schedule, sourceRef string) taskcenter.DailyTimingProjection {
	timing := runtimeDailyTimingUnsupported(sourceRef, "native_cron_not_normalized")
	timing.Timezone = runtimeDailyKnown("UTC", "domain.scheduler.NextRunAfter")
	timing.ScheduledStartAt = runtimeDailyNone[time.Time](sourceRef, "recurring_schedule")
	timing.NotBefore = runtimeDailyNone[time.Time](sourceRef, "no_not_before_gate")
	timing.AllowedWindows = runtimeDailyNone[[]modulecore.TaskTimeWindow](sourceRef, "no_time_window_gate")
	timing.DeadlineAt = runtimeDailyNone[time.Time](sourceRef, "no_task_deadline")
	timing.EstimatedDurationSeconds = runtimeDailyUnknown[int64](sourceRef, "duration_not_estimated")
	var normalized *modulecore.TaskRecurrence
	for _, schedule := range schedules {
		recurrence, ok := normalizePronunciationSchedule(schedule.Schedule)
		if !ok {
			return timing
		}
		if normalized != nil && !samePronunciationRecurrence(*normalized, recurrence) {
			return timing
		}
		recurrenceCopy := recurrence
		normalized = &recurrenceCopy
	}
	if normalized != nil {
		timing.Recurrence = runtimeDailyKnown(*normalized, "scheduler.schedule.schedule")
	}
	return timing
}

func normalizePronunciationSchedule(schedule string) (modulecore.TaskRecurrence, bool) {
	fields := strings.Fields(strings.TrimSpace(schedule))
	if len(fields) == 2 && fields[0] == "every" {
		interval, err := time.ParseDuration(fields[1])
		if err != nil || interval <= 0 || interval%time.Second != 0 {
			return modulecore.TaskRecurrence{}, false
		}
		seconds := int64(interval / time.Second)
		return modulecore.TaskRecurrence{Frequency: "interval", IntervalSeconds: &seconds}, true
	}
	if len(fields) != 6 || fields[0] != "cron" || fields[3] != "*" || fields[4] != "*" || fields[5] != "*" {
		return modulecore.TaskRecurrence{}, false
	}
	minute, errMinute := strconv.Atoi(fields[1])
	hour, errHour := strconv.Atoi(fields[2])
	if errMinute != nil || errHour != nil || minute < 0 || minute > 59 || hour < 0 || hour > 23 {
		return modulecore.TaskRecurrence{}, false
	}
	return modulecore.TaskRecurrence{Frequency: "daily", Times: []string{time.Date(2000, 1, 1, hour, minute, 0, 0, time.UTC).Format("15:04")}}, true
}

func samePronunciationRecurrence(left, right modulecore.TaskRecurrence) bool {
	if left.Frequency != right.Frequency || len(left.Times) != len(right.Times) || len(left.Weekdays) != len(right.Weekdays) {
		return false
	}
	if (left.IntervalSeconds == nil) != (right.IntervalSeconds == nil) {
		return false
	}
	if left.IntervalSeconds != nil && *left.IntervalSeconds != *right.IntervalSeconds {
		return false
	}
	for i := range left.Times {
		if left.Times[i] != right.Times[i] {
			return false
		}
	}
	for i := range left.Weekdays {
		if left.Weekdays[i] != right.Weekdays[i] {
			return false
		}
	}
	return true
}

func pronunciationDailyEvidence(entry domainscheduler.RunLog) taskcenter.DailyEvidence {
	result := taskcenter.DailyOutcomeUnknown
	switch entry.Status {
	case "completed":
		result = taskcenter.DailyOutcomeSuccess
	case "failed":
		result = taskcenter.DailyOutcomeFailure
	case "deferred":
		result = taskcenter.DailyOutcomePartial
	}
	attempt := runtimeDailyUnknown[time.Time]("scheduler.run_log.started_at", "run_start_unavailable")
	if !entry.StartedAt.IsZero() {
		attempt = runtimeDailyKnown(entry.StartedAt.UTC(), "scheduler.run_log.started_at")
	}
	lastSuccess := runtimeDailyUnknown[time.Time]("scheduler.run_log.completed_at", "not_a_success_run")
	if result == taskcenter.DailyOutcomeSuccess && !entry.CompletedAt.IsZero() {
		lastSuccess = runtimeDailyKnown(entry.CompletedAt.UTC(), "scheduler.run_log.completed_at")
	}
	return taskcenter.DailyEvidence{
		State:         taskcenter.DailyValueKnown,
		LastAttemptAt: attempt,
		LastSuccessAt: lastSuccess,
		LastResult:    runtimeDailyKnown(result, "scheduler.run_log.status"),
		Refs:          []taskcenter.DailyResourceRef{runtimeDailyResourceRef(pronunciationDailySourceID, "tts.pronunciation_check", "scheduler_run_log", "scheduler.run_log", string(entry.RunID), tool.DataScopePublic, "Pronunciation check scheduler run")},
	}
}

func pronunciationDailyOccurrences(logs []domainscheduler.RunLog, limit int) []taskcenter.DailyOccurrence {
	occurrences := make([]taskcenter.DailyOccurrence, 0, min(len(logs), limit))
	for _, entry := range logs[:min(len(logs), limit)] {
		state := taskcenter.DailyOccurrenceUnknown
		result := taskcenter.DailyOutcomeUnknown
		switch entry.Status {
		case "completed":
			state, result = taskcenter.DailyOccurrenceSucceeded, taskcenter.DailyOutcomeSuccess
		case "failed":
			state, result = taskcenter.DailyOccurrenceFailed, taskcenter.DailyOutcomeFailure
		case "deferred":
			state, result = taskcenter.DailyOccurrencePartial, taskcenter.DailyOutcomePartial
		}
		lastAttempt := runtimeDailyUnknown[time.Time]("scheduler.run_log.started_at", "run_start_unavailable")
		if !entry.StartedAt.IsZero() {
			lastAttempt = runtimeDailyKnown(entry.StartedAt.UTC(), "scheduler.run_log.started_at")
		}
		lastSuccess := runtimeDailyUnknown[time.Time]("scheduler.run_log.completed_at", "not_a_success_run")
		if result == taskcenter.DailyOutcomeSuccess && !entry.CompletedAt.IsZero() {
			lastSuccess = runtimeDailyKnown(entry.CompletedAt.UTC(), "scheduler.run_log.completed_at")
		}
		occurrences = append(occurrences, taskcenter.DailyOccurrence{
			OccurrenceID:  string(entry.RunID),
			State:         state,
			NextRunAt:     runtimeDailyNone[time.Time]("scheduler.run_log", "past_occurrence"),
			LastAttemptAt: lastAttempt,
			LastSuccessAt: lastSuccess,
			Result:        runtimeDailyKnown(result, "scheduler.run_log.status"),
			NativeRefs:    []taskcenter.DailyResourceRef{runtimeDailyResourceRef(pronunciationDailySourceID, "tts.pronunciation_check", "scheduler_run_log", "scheduler.run_log", string(entry.RunID), tool.DataScopePublic, "Pronunciation check scheduler run")},
		})
	}
	return occurrences
}

func hasEnabledPronunciationSchedule(schedules []domainscheduler.Schedule) bool {
	for _, schedule := range schedules {
		if schedule.Enabled {
			return true
		}
	}
	return false
}
