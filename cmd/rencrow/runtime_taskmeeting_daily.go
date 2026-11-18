package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	taskMeetingDailySourceID     = "runtime.taskmeeting"
	taskMeetingDailyDefinitionID = "meeting.daily_report"
)

// registerRuntimeTaskMeetingDailySource projects owner configuration, the
// actual Scheduler row, and bounded Workstream occurrence summaries. It does
// not read transcript bodies or synthesize execution evidence.
func registerRuntimeTaskMeetingDailySource(deps *Dependencies, cfg *config.Config) {
	if deps == nil {
		return
	}
	const owner = "application.taskmeeting"
	const sourceRef = "heartbeat.routine.daily_report_meetings"
	meeting := config.DailyReportMeetingsConfig{}
	ownerUserID := ""
	if cfg != nil {
		meeting = cfg.Heartbeat.Routine.DailyReportMeetings
		ownerUserID = meeting.OwnerUserID
		if ownerUserID == "" && cfg.LocalAgentOps.Enabled {
			ownerUserID = cfg.LocalAgentOps.UserID
		}
	}
	dataScope := tool.DataScopePublic
	if ownerUserID != "" {
		dataScope = tool.DataScopeUser
	}
	// The typed meeting projection/runner is implemented independently of
	// whether this instance is configured, registered, or currently enabled.
	// Those runtime conditions belong on the effective axis below.
	implementation := runtimeDailyAxis(taskcenter.DailyImplementationImplemented, owner, "")
	definition := taskcenter.DailyDefinitionDescriptor{
		DefinitionID:   taskMeetingDailyDefinitionID,
		KindID:         taskMeetingDailyDefinitionID,
		Label:          "Daily report meeting",
		OwnerRef:       owner,
		ResourceType:   "meeting_definition",
		DataScope:      dataScope,
		UserID:         ownerUserID,
		Implementation: implementation,
		SourceRefs: []taskcenter.DailyResourceRef{{
			SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "configuration_definition",
			Reference: sourceRef, DataScope: dataScope, UserID: ownerUserID,
			DisplayName: "Daily report meeting owner configuration", LinkRef: "spec:" + sourceRef,
		}},
	}
	descriptor := taskcenter.DailySourceDescriptor{SourceID: taskMeetingDailySourceID, Definitions: []taskcenter.DailyDefinitionDescriptor{definition}}
	registerTaskCenterDailySource(deps, newRuntimeDailySource(descriptor, func(ctx context.Context, scope tool.ToolExecutionScope) (taskcenter.DailySourceSnapshot, error) {
		if err := ctx.Err(); err != nil {
			return taskcenter.DailySourceSnapshot{}, err
		}
		now := time.Now().UTC()
		configuredAxis, effectiveAxis := taskMeetingDailyConfigAxes(cfg, deps, sourceRef)
		scheduleState := readTaskMeetingSchedules(ctx, deps.schedulerStore, deps.taskMeetingScheduleID)
		if scheduleState.err != nil && ctx.Err() != nil {
			return taskcenter.DailySourceSnapshot{}, ctx.Err()
		}
		if scheduleState.err != nil {
			effectiveAxis = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler.schedule", "schedule_query_failed")
		}
		if scheduleState.err == nil {
			effectiveAxis = taskMeetingScheduleEffectiveAxis(cfg, deps, scheduleState, sourceRef)
		}
		registeredActors := taskMeetingDailyRegisteredActors(meeting)
		occurrenceState := readTaskMeetingOccurrenceSummaries(ctx, deps, scope, ownerUserID, deps.taskMeetingScheduleID, registeredActors)
		timingProjection := taskMeetingDailyTiming(meeting, cfg != nil, sourceRef)
		if scheduleState.err == nil && len(scheduleState.schedules) == 1 && scheduleState.schedules[0].Timing != nil {
			timingProjection = taskMeetingDailyTimingFromTiming(*scheduleState.schedules[0].Timing, "scheduler.schedule.timing")
		}
		observedAxis := runtimeDailyAxis(taskcenter.DailyObservedUnavailable, "workstream.taskmeeting.occurrences", "meeting_read_model_unavailable")
		occurrenceCount := runtimeDailyUnknown[int]("workstream.taskmeeting.occurrences", "meeting_read_model_unavailable")
		occurrences := []taskcenter.DailyOccurrence{}
		occurrencesTruncated := false
		latestEvidence := runtimeDailyUnknownEvidence("workstream.taskmeeting.occurrences", "meeting_read_model_unavailable")
		occurrenceProjectionIncomplete := false
		if occurrenceState.err == nil {
			if occurrenceState.noWorkstream {
				observedAxis = runtimeDailyAxis(taskcenter.DailyObservedNotObserved, "workstream.taskmeeting.occurrences", "no_managed_workstream")
				occurrenceCount = runtimeDailyKnown(0, "workstream.taskmeeting.occurrences.total_count")
			} else {
				observedAxis = runtimeDailyAxis(taskcenter.DailyObservedAvailable, "workstream.taskmeeting.occurrences", "")
				occurrenceCount = runtimeDailyKnown(occurrenceState.totalCount, "workstream.taskmeeting.occurrences.total_count")
				occurrencesTruncated = occurrenceState.hasMore
				occurrences, latestEvidence, occurrenceProjectionIncomplete = projectTaskMeetingDailyOccurrences(occurrenceState.occurrences)
			}
		}
		observation := taskcenter.DailyObservation{
			DefinitionID: taskMeetingDailyDefinitionID,
			Configured:   configuredAxis,
			Effective:    effectiveAxis,
			Observed:     observedAxis,
			Timing:       timingProjection,
			NextRunAt:    taskMeetingDailyNextRun(cfg, scheduleState, sourceRef),
			TargetCount:  taskMeetingDailyTargetCount(cfg, deps, sourceRef),
			SourceRefs: []taskcenter.DailyResourceRef{{
				SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "configuration_definition",
				Reference: sourceRef, DataScope: dataScope, UserID: ownerUserID,
			}},
			NativeRefs:           scheduleState.refs(dataScope, ownerUserID),
			NativeRefCount:       scheduleState.referenceCount(sourceRef),
			NativeRefsTruncated:  scheduleState.truncated,
			LatestEvidence:       latestEvidence,
			Occurrences:          occurrences,
			OccurrenceCount:      occurrenceCount,
			OccurrencesTruncated: occurrencesTruncated,
			Issues:               []taskcenter.DailyIssue{},
		}
		if occurrenceState.err != nil {
			observation.Issues = append(observation.Issues, taskcenter.DailyIssue{SourceID: taskMeetingDailySourceID, OwnerRef: owner, Code: taskcenter.DailyIssueSourceReported})
		}
		if occurrenceProjectionIncomplete {
			observation.Issues = append(observation.Issues, taskcenter.DailyIssue{SourceID: taskMeetingDailySourceID, OwnerRef: owner, Code: taskcenter.DailyIssueSourceReported})
		}
		if scheduleState.err != nil {
			observation.NativeRefs = []taskcenter.DailyResourceRef{}
			observation.NativeRefCount = runtimeDailyUnknown[int]("scheduler.schedule", "schedule_query_failed")
			observation.NextRunAt = runtimeDailyUnknown[time.Time]("scheduler.schedule", "schedule_query_failed")
			observation.Issues = append(observation.Issues, taskcenter.DailyIssue{SourceID: taskMeetingDailySourceID, OwnerRef: owner, Code: taskcenter.DailyIssueSourceReported})
		}
		if scheduleState.truncated {
			observation.NativeRefCount = runtimeDailyUnknown[int]("scheduler.schedule", "schedule_query_limit_reached")
			observation.NativeRefsTruncated = true
			observation.Issues = append(observation.Issues, taskcenter.DailyIssue{SourceID: taskMeetingDailySourceID, OwnerRef: owner, Code: taskcenter.DailyIssueSourceReported})
		}
		if scheduleState.identityUnproven {
			observation.Effective = runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler.schedule", "owner_schedule_identity_unproven")
			observation.NativeRefCount = runtimeDailyUnknown[int]("scheduler.schedule", "owner_schedule_identity_unproven")
			observation.NextRunAt = runtimeDailyUnknown[time.Time]("scheduler.schedule", "owner_schedule_identity_unproven")
			observation.Issues = append(observation.Issues, taskcenter.DailyIssue{SourceID: taskMeetingDailySourceID, OwnerRef: owner, Code: taskcenter.DailyIssueSourceReported})
		}
		return taskcenter.DailySourceSnapshot{ObservedAt: now, Observations: []taskcenter.DailyObservation{observation}, Issues: []taskcenter.DailyIssue{}}, nil
	}))
}

type taskMeetingScheduleSnapshot struct {
	schedules        []domainscheduler.Schedule
	truncated        bool
	identityUnproven bool
	err              error
}

type taskMeetingOccurrenceSummaryReader interface {
	FindTaskCenterWorkstreamID(context.Context, tool.ToolExecutionScope) (modulecore.WorkstreamID, error)
	ListTaskMeetingOccurrenceSummaries(
		context.Context,
		modulecore.WorkstreamID,
		string,
		modulecore.ScheduleID,
		int,
		workstreampersistence.TaskMeetingOccurrenceListAuthorizationCheck,
	) (workstreampersistence.TaskMeetingOccurrenceList, error)
}

type taskMeetingOccurrenceSummarySnapshot struct {
	occurrences  []workstreampersistence.TaskMeetingOccurrenceSummary
	totalCount   int
	hasMore      bool
	noWorkstream bool
	err          error
}

func readTaskMeetingOccurrenceSummaries(
	ctx context.Context,
	deps *Dependencies,
	scope tool.ToolExecutionScope,
	ownerUserID string,
	scheduleID modulecore.ScheduleID,
	registeredActors map[string]struct{},
) taskMeetingOccurrenceSummarySnapshot {
	if err := validateTaskMeetingDailyReadScope(ctx, scope, ownerUserID, registeredActors, deps != nil && deps.taskMeetingBindingsReady); err != nil {
		return taskMeetingOccurrenceSummarySnapshot{err: err}
	}
	if deps == nil || scheduleID == "" || scheduleID.Validate() != nil {
		return taskMeetingOccurrenceSummarySnapshot{err: errors.New("TaskMeeting occurrence identity is unavailable")}
	}
	reader, ok := deps.workstreamStore.(taskMeetingOccurrenceSummaryReader)
	if !ok || reader == nil {
		return taskMeetingOccurrenceSummarySnapshot{err: errors.New("TaskMeeting Workstream reader is unavailable")}
	}
	workstreamID, err := reader.FindTaskCenterWorkstreamID(ctx, scope)
	if errors.Is(err, domainworkstream.ErrTaskPlanNotFound) {
		return taskMeetingOccurrenceSummarySnapshot{noWorkstream: true}
	}
	if err != nil {
		return taskMeetingOccurrenceSummarySnapshot{err: err}
	}
	authorize := func(readCtx context.Context, gotWorkstreamID modulecore.WorkstreamID, gotOwnerUserID string, gotScheduleID modulecore.ScheduleID, summary *workstreampersistence.TaskMeetingOccurrenceSummary) error {
		if err := validateTaskMeetingDailyReadScope(readCtx, scope, ownerUserID, registeredActors, deps.taskMeetingBindingsReady); err != nil {
			return err
		}
		if gotWorkstreamID != workstreamID || gotOwnerUserID != ownerUserID || gotScheduleID != scheduleID {
			return domainworkstream.ErrTaskPlanForbidden
		}
		if summary == nil {
			return nil
		}
		if err := validateTaskMeetingDailySummary(*summary, workstreamID, ownerUserID, scheduleID); err != nil {
			return err
		}
		return authorizeTaskMeetingDailyActor(scope, *summary)
	}
	list, err := reader.ListTaskMeetingOccurrenceSummaries(ctx, workstreamID, ownerUserID, scheduleID, taskcenter.DailyMaxOccurrences, authorize)
	if err != nil {
		return taskMeetingOccurrenceSummarySnapshot{err: err}
	}
	if list.TotalCount < len(list.Occurrences) || list.HasMore != (list.TotalCount > len(list.Occurrences)) {
		return taskMeetingOccurrenceSummarySnapshot{err: errors.New("TaskMeeting occurrence list metadata is inconsistent")}
	}
	return taskMeetingOccurrenceSummarySnapshot{
		occurrences: append([]workstreampersistence.TaskMeetingOccurrenceSummary(nil), list.Occurrences...),
		totalCount:  list.TotalCount,
		hasMore:     list.HasMore,
	}
}

func validateTaskMeetingDailyReadScope(
	ctx context.Context,
	expected tool.ToolExecutionScope,
	ownerUserID string,
	registeredActors map[string]struct{},
	bindingsReady bool,
) error {
	if ctx == nil || ctx.Err() != nil || expected.Validate() != nil || ownerUserID == "" ||
		expected.AuthenticatedUserID != ownerUserID || !expected.Allows(tool.DataScopeUser) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	switch expected.ActorKind {
	case tool.ActorKindUser:
		if expected.AuthenticationSource != tool.AuthenticationSourceHTTP || expected.ActorID != ownerUserID {
			return domainworkstream.ErrTaskPlanForbidden
		}
	case tool.ActorKindAgent:
		if expected.AuthenticationSource != tool.AuthenticationSourceAgentOrchestrator || !bindingsReady {
			return domainworkstream.ErrTaskPlanForbidden
		}
		if _, registered := registeredActors[expected.ActorID]; !registered {
			return domainworkstream.ErrTaskPlanForbidden
		}
	default:
		return domainworkstream.ErrTaskPlanForbidden
	}
	actual, found := tool.ToolExecutionScopeFromContext(ctx)
	if !found || actual.Validate() != nil || !reflect.DeepEqual(actual, expected) {
		return domainworkstream.ErrTaskPlanForbidden
	}
	return nil
}

func taskMeetingDailyRegisteredActors(cfg config.DailyReportMeetingsConfig) map[string]struct{} {
	actors := make(map[string]struct{}, len(cfg.Actors))
	if len(cfg.Actors) != 8 {
		return actors
	}
	for _, actor := range cfg.Actors {
		if actor.CoreActorID == "" || actor.CoreActorID != strings.TrimSpace(actor.CoreActorID) {
			return map[string]struct{}{}
		}
		if _, exists := actors[actor.CoreActorID]; exists {
			return map[string]struct{}{}
		}
		actors[actor.CoreActorID] = struct{}{}
	}
	return actors
}

func authorizeTaskMeetingDailyActor(scope tool.ToolExecutionScope, summary workstreampersistence.TaskMeetingOccurrenceSummary) error {
	if scope.ActorKind != tool.ActorKindAgent {
		return nil
	}
	for _, actorID := range summary.Audience {
		if actorID == scope.ActorID {
			return nil
		}
	}
	return domainworkstream.ErrTaskPlanForbidden
}

func validateTaskMeetingDailySummary(summary workstreampersistence.TaskMeetingOccurrenceSummary, workstreamID modulecore.WorkstreamID, ownerUserID string, scheduleID modulecore.ScheduleID) error {
	if summary.WorkstreamID != workstreamID || summary.OwnerUserID != ownerUserID || summary.Key.ScheduleID != scheduleID || summary.Key.Validate() != nil ||
		summary.CoordinatorActorID == "" || summary.ScheduleRevision == 0 || summary.RegistrationEvidenceID.Validate() != nil ||
		summary.ScheduledAt.IsZero() || summary.DeadlineAt.IsZero() || summary.LastCheckpointAt.IsZero() || len(summary.Audience) != 8 ||
		summary.ReportCount < 0 || summary.ReportCount > len(summary.Audience) || summary.AnswerCount < 0 || summary.DeliveredMessageCount < 0 ||
		summary.DeliveredReportCount < 0 || summary.DeliveredReportCount > summary.ReportCount ||
		summary.ReportedConcernCount < 0 || summary.AnsweredConcernCount < 0 || summary.AnsweredConcernCount > summary.ReportedConcernCount ||
		summary.UnresolvedConcernCount < 0 || summary.AnsweredConcernCount+summary.UnresolvedConcernCount != summary.ReportedConcernCount {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	if summary.AttendanceCount == nil {
		if summary.AttendanceScopeID != "" || summary.AttendanceObservedAt != nil {
			return domainworkstream.ErrTaskPlanUnavailable
		}
	} else if *summary.AttendanceCount < 0 || *summary.AttendanceCount > len(summary.Audience) ||
		summary.AttendanceScopeID.Validate() != nil || summary.AttendanceObservedAt == nil || summary.AttendanceObservedAt.IsZero() {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	if (summary.TaskRef == nil) != (summary.RunRef == nil) {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	if summary.TaskRef != nil && (summary.TaskRef.Validate() != nil || summary.RunRef.Validate() != nil) {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	if summary.Minutes != nil && (summary.Minutes.ArtifactID.Validate() != nil || summary.Minutes.WorkstreamID != workstreamID ||
		summary.Minutes.OwnerUserID != ownerUserID || summary.Minutes.OccurrenceKey != summary.Key.String() || summary.Minutes.CreatedAt.IsZero()) {
		return domainworkstream.ErrTaskPlanUnavailable
	}
	seenActors := make(map[string]struct{}, len(summary.Audience))
	for _, actorID := range summary.Audience {
		if actorID == "" || actorID != strings.TrimSpace(actorID) {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		if _, exists := seenActors[actorID]; exists {
			return domainworkstream.ErrTaskPlanUnavailable
		}
		seenActors[actorID] = struct{}{}
	}
	switch summary.Status {
	case domainmeeting.StatusScheduled, domainmeeting.StatusGathering, domainmeeting.StatusReporting, domainmeeting.StatusAnswering,
		domainmeeting.StatusFinishing, domainmeeting.StatusCompleted, domainmeeting.StatusPartial, domainmeeting.StatusMissed:
	default:
		return domainworkstream.ErrTaskPlanUnavailable
	}
	return nil
}

func projectTaskMeetingDailyOccurrences(summaries []workstreampersistence.TaskMeetingOccurrenceSummary) ([]taskcenter.DailyOccurrence, taskcenter.DailyEvidence, bool) {
	occurrences := make([]taskcenter.DailyOccurrence, 0, len(summaries))
	latestEvidence := runtimeDailyUnknownEvidence("workstream.taskmeeting.occurrences", "no_occurrence_history")
	incomplete := false
	for index, summary := range summaries {
		occurrence, evidence, issue := projectTaskMeetingDailyOccurrence(summary)
		incomplete = incomplete || issue
		occurrences = append(occurrences, occurrence)
		if index == 0 {
			latestEvidence = evidence
		}
	}
	return occurrences, latestEvidence, incomplete
}

func projectTaskMeetingDailyOccurrence(summary workstreampersistence.TaskMeetingOccurrenceSummary) (taskcenter.DailyOccurrence, taskcenter.DailyEvidence, bool) {
	const source = "workstream.taskmeeting.occurrences"
	id := taskMeetingDailyOccurrenceID(summary.Key)
	state := taskcenter.DailyOccurrenceUnknown
	result := runtimeDailyUnknown[taskcenter.DailyOutcome](source, "meeting_outcome_unconfirmed")
	lastSuccess := runtimeDailyUnknown[time.Time](source, "meeting_success_unconfirmed")
	issue := false
	switch summary.Status {
	case domainmeeting.StatusScheduled:
		state = taskcenter.DailyOccurrenceScheduled
	case domainmeeting.StatusGathering, domainmeeting.StatusReporting, domainmeeting.StatusAnswering, domainmeeting.StatusFinishing:
		state = taskcenter.DailyOccurrenceRunning
		result = runtimeDailyKnown(taskcenter.DailyOutcomeRunning, source+".status")
	case domainmeeting.StatusCompleted:
		if summary.FinishProofPersistedAt != nil && !summary.FinishProofPersistedAt.IsZero() && summary.Minutes != nil &&
			summary.Minutes.ArtifactID.Validate() == nil && !summary.Minutes.CreatedAt.IsZero() {
			state = taskcenter.DailyOccurrenceSucceeded
			result = runtimeDailyKnown(taskcenter.DailyOutcomeSuccess, source+".finish_proof")
			lastSuccess = runtimeDailyKnown(summary.FinishProofPersistedAt.UTC(), source+".finish_proof.persisted_at")
		} else {
			issue = true
		}
	case domainmeeting.StatusPartial:
		if summary.PartialObservedAt != nil && !summary.PartialObservedAt.IsZero() {
			state = taskcenter.DailyOccurrencePartial
			result = runtimeDailyKnown(taskcenter.DailyOutcomePartial, source+".partial_evidence")
		} else {
			issue = true
		}
	case domainmeeting.StatusMissed:
		state = taskcenter.DailyOccurrenceMissed
		result = runtimeDailyKnown(taskcenter.DailyOutcomeMissed, source+".status")
	}
	refs := taskMeetingDailyOccurrenceRefs(summary, id)
	occurrence := taskcenter.DailyOccurrence{
		OccurrenceID:    id,
		State:           state,
		ScheduledAt:     dailyValuePointer(runtimeDailyKnown(summary.ScheduledAt.UTC(), source+".scheduled_at")),
		NextRunAt:       runtimeDailyUnsupported[time.Time](source+".next_run_at", "occurrence_is_single_slot"),
		LastAttemptAt:   runtimeDailyUnknown[time.Time](source+".last_attempt_at", "meeting_owner_does_not_expose_attempt_time"),
		LastSuccessAt:   lastSuccess,
		Result:          result,
		NativeRefs:      refs,
		MeetingProgress: projectTaskMeetingDailyProgress(summary),
	}
	evidenceState := taskcenter.DailyValueUnknown
	if result.State == taskcenter.DailyValueKnown {
		evidenceState = taskcenter.DailyValueKnown
	}
	evidence := taskcenter.DailyEvidence{
		State:         evidenceState,
		LastAttemptAt: runtimeDailyUnknown[time.Time](source+".last_attempt_at", "meeting_owner_does_not_expose_attempt_time"),
		LastSuccessAt: lastSuccess,
		LastResult:    result,
		Refs:          refs,
	}
	return occurrence, evidence, issue
}

func projectTaskMeetingDailyProgress(summary workstreampersistence.TaskMeetingOccurrenceSummary) *taskcenter.DailyMeetingProgress {
	const source = "workstream.taskmeeting.occurrences"
	participantCount := len(summary.Audience)
	attendance := taskcenter.DailyProgressCount{
		Current: runtimeDailyUnknown[int](source+".attendance", "complete_presence_checkpoint_not_observed"),
		Total:   runtimeDailyKnown(participantCount, source+".audience"),
	}
	if summary.AttendanceCount != nil {
		attendance.Current = runtimeDailyKnown(*summary.AttendanceCount, source+".attendance")
	}
	minutes := taskcenter.DailyValue[taskcenter.DailyResourceRef]{
		State: taskcenter.DailyValueNone, SourceRef: source + ".minutes", ReasonCode: "not_persisted",
	}
	if summary.Minutes != nil {
		ref := taskcenter.DailyResourceRef{
			SourceID: taskMeetingDailySourceID, OwnerRef: "application.taskmeeting", ResourceType: "meeting_minutes",
			Reference: "workstream.taskmeeting.minutes", NativeID: string(summary.Minutes.ArtifactID),
			DataScope: tool.DataScopeUser, UserID: summary.OwnerUserID, DisplayName: "Meeting minutes reference",
		}
		minutes = runtimeDailyKnown(ref, source+".minutes")
	}
	return &taskcenter.DailyMeetingProgress{
		Attendance: attendance,
		DeliveredReports: taskcenter.DailyProgressCount{
			Current: runtimeDailyKnown(summary.DeliveredReportCount, source+".delivered_reports"),
			Total:   runtimeDailyKnown(participantCount, source+".audience"),
		},
		AnsweredConcerns: taskcenter.DailyProgressCount{
			Current: runtimeDailyKnown(summary.AnsweredConcernCount, source+".answered_concerns"),
			Total:   runtimeDailyKnown(summary.ReportedConcernCount, source+".reported_concerns_checkpoint"),
		},
		UnresolvedConcerns: runtimeDailyKnown(summary.UnresolvedConcernCount, source+".unresolved_concerns_checkpoint"),
		Minutes:            minutes,
	}
}

func taskMeetingDailyOccurrenceID(key domainmeeting.OccurrenceKey) string {
	return fmt.Sprintf("%s:%s:%s:%s", key.ScheduleID, key.LocalDate, strings.ReplaceAll(key.Slot, ":", "-"), key.Timezone)
}

func taskMeetingDailyOccurrenceRefs(summary workstreampersistence.TaskMeetingOccurrenceSummary, occurrenceID string) []taskcenter.DailyResourceRef {
	const owner = "application.taskmeeting"
	refs := []taskcenter.DailyResourceRef{{
		SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "meeting_occurrence",
		Reference: "workstream.taskmeeting.occurrence", NativeID: occurrenceID, DataScope: tool.DataScopeUser,
		UserID: summary.OwnerUserID, Revision: fmt.Sprint(summary.ScheduleRevision), DisplayName: "Daily report meeting occurrence",
	}}
	if summary.WorkstreamID != "" {
		refs = append(refs, taskcenter.DailyResourceRef{SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "workstream", Reference: "workstream.taskmeeting", NativeID: string(summary.WorkstreamID), DataScope: tool.DataScopeUser, UserID: summary.OwnerUserID})
	}
	if summary.TaskRef != nil && summary.RunRef != nil {
		refs = append(refs,
			taskcenter.DailyResourceRef{SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "task", Reference: "taskmanager.task", NativeID: string(*summary.TaskRef), DataScope: tool.DataScopeUser, UserID: summary.OwnerUserID},
			taskcenter.DailyResourceRef{SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "run", Reference: "taskmanager.run", NativeID: string(*summary.RunRef), DataScope: tool.DataScopeUser, UserID: summary.OwnerUserID},
		)
	}
	if summary.Minutes != nil {
		refs = append(refs, taskcenter.DailyResourceRef{SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "meeting_minutes", Reference: "workstream.taskmeeting.minutes", NativeID: string(summary.Minutes.ArtifactID), DataScope: tool.DataScopeUser, UserID: summary.OwnerUserID})
	}
	if summary.AttendanceCount != nil && summary.AttendanceScopeID.Validate() == nil {
		refs = append(refs, taskcenter.DailyResourceRef{SourceID: taskMeetingDailySourceID, OwnerRef: owner, ResourceType: "world_scope_evidence", Reference: "workstream.taskmeeting.attendance", NativeID: string(summary.AttendanceScopeID), DataScope: tool.DataScopeUser, UserID: summary.OwnerUserID})
	}
	return refs
}

func dailyValuePointer[T any](value taskcenter.DailyValue[T]) *taskcenter.DailyValue[T] {
	return &value
}

func readTaskMeetingSchedules(ctx context.Context, store viewer.SchedulerStore, ownerScheduleID modulecore.ScheduleID) taskMeetingScheduleSnapshot {
	if store == nil {
		return taskMeetingScheduleSnapshot{err: errors.New("scheduler store unavailable")}
	}
	if ownerScheduleID != "" {
		if exact, ok := store.(taskMeetingExactScheduleReader); ok {
			schedule, found, err := exact.FindScheduleByID(ctx, string(ownerScheduleID))
			if err != nil {
				return taskMeetingScheduleSnapshot{err: err}
			}
			if !found {
				return taskMeetingScheduleSnapshot{}
			}
			if schedule.ScheduleID != ownerScheduleID || !isTaskMeetingSchedule(schedule) {
				return taskMeetingScheduleSnapshot{identityUnproven: true}
			}
			return taskMeetingScheduleSnapshot{schedules: []domainscheduler.Schedule{schedule}}
		}
	}
	schedules, err := store.ListSchedules(ctx, taskMeetingScheduleReadLimit)
	if err != nil {
		return taskMeetingScheduleSnapshot{err: err}
	}
	if len(schedules) >= taskMeetingScheduleReadLimit {
		matched, unproven := taskMeetingOwnerScheduleRows(schedules, ownerScheduleID)
		return taskMeetingScheduleSnapshot{schedules: matched, truncated: true, identityUnproven: unproven}
	}
	matched, unproven := taskMeetingOwnerScheduleRows(schedules, ownerScheduleID)
	return taskMeetingScheduleSnapshot{schedules: matched, identityUnproven: unproven}
}

func taskMeetingOwnerScheduleRows(schedules []domainscheduler.Schedule, ownerScheduleID modulecore.ScheduleID) ([]domainscheduler.Schedule, bool) {
	owned := make([]domainscheduler.Schedule, 0, 1)
	unproven := false
	for _, schedule := range schedules {
		if schedule.ScheduleID == ownerScheduleID && ownerScheduleID != "" {
			if !isTaskMeetingSchedule(schedule) {
				unproven = true
				continue
			}
			owned = append(owned, schedule)
			continue
		}
		if isTaskMeetingSchedule(schedule) {
			unproven = true
		}
	}
	return owned, unproven
}

func (s taskMeetingScheduleSnapshot) refs(dataScope, userID string) []taskcenter.DailyResourceRef {
	refs := make([]taskcenter.DailyResourceRef, 0, len(s.schedules))
	for _, schedule := range s.schedules {
		refs = append(refs, taskcenter.DailyResourceRef{
			SourceID: taskMeetingDailySourceID, OwnerRef: "application.taskmeeting", ResourceType: "scheduler_schedule",
			Reference: "scheduler.schedule", NativeID: string(schedule.ScheduleID), DataScope: dataScope, UserID: userID,
			DisplayName: "Daily report meeting schedule",
		})
	}
	return refs
}

func (s taskMeetingScheduleSnapshot) referenceCount(sourceRef string) taskcenter.DailyValue[int] {
	if s.err != nil {
		return runtimeDailyUnknown[int]("scheduler.schedule", "schedule_query_failed")
	}
	if s.truncated {
		return runtimeDailyUnknown[int]("scheduler.schedule", "schedule_query_limit_reached")
	}
	if s.identityUnproven {
		return runtimeDailyUnknown[int]("scheduler.schedule", "owner_schedule_identity_unproven")
	}
	return runtimeDailyKnown(len(s.schedules), sourceRef+".schedule_count")
}

func taskMeetingDailyConfigAxes(cfg *config.Config, deps *Dependencies, sourceRef string) (taskcenter.DailyAxis, taskcenter.DailyAxis) {
	if cfg == nil {
		return runtimeDailyAxis(taskcenter.DailyConfiguredUnknown, sourceRef, "configuration_unavailable"),
			runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, sourceRef, "configuration_unavailable")
	}
	if !cfg.Heartbeat.Routine.DailyReportMeetings.Enabled {
		return runtimeDailyAxis(taskcenter.DailyConfiguredDisabled, sourceRef, "feature_disabled"),
			runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, sourceRef, "feature_disabled")
	}
	configured := runtimeDailyAxis(taskcenter.DailyConfiguredConfigured, sourceRef, "")
	if !cfg.Heartbeat.Enabled {
		return configured, runtimeDailyAxis(taskcenter.DailyEffectiveBlocked, "heartbeat.enabled", "parent_heartbeat_disabled")
	}
	if reason := taskMeetingWorldReadAvailabilityReason(cfg); reason != "" {
		return configured, runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "world_projection.credential", reason)
	}
	if !deps.taskMeetingBindingsReady {
		return configured, runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "runtime.agent_registration", "configured_actor_registration_unavailable")
	}
	if deps.taskMeetingScheduleID == "" {
		return configured, runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "application.taskmeeting.registration", "owner_schedule_registration_unavailable")
	}
	if deps.taskMeetingOccurrenceExecutor == nil {
		return configured, runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "application.taskmeeting", "meeting_executor_unavailable")
	}
	return configured, runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "scheduler.schedule", "matching_schedule_not_observed")
}

func taskMeetingScheduleEffectiveAxis(cfg *config.Config, deps *Dependencies, snapshot taskMeetingScheduleSnapshot, sourceRef string) taskcenter.DailyAxis {
	configured, effective := taskMeetingDailyConfigAxes(cfg, deps, sourceRef)
	if configured.State != taskcenter.DailyConfiguredConfigured || effective.State == taskcenter.DailyEffectiveBlocked || effective.State == taskcenter.DailyEffectiveDisabled {
		return effective
	}
	if effective.State == taskcenter.DailyEffectiveUnavailable {
		return effective
	}
	if snapshot.identityUnproven {
		return runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler.schedule", "owner_schedule_identity_unproven")
	}
	if snapshot.truncated {
		return runtimeDailyAxis(taskcenter.DailyEffectiveUnknown, "scheduler.schedule", "schedule_query_limit_reached")
	}
	if len(snapshot.schedules) == 0 {
		return runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler.schedule", "matching_schedule_unavailable")
	}
	if len(snapshot.schedules) != 1 {
		return runtimeDailyAxis(taskcenter.DailyEffectiveUnavailable, "scheduler.schedule", "multiple_owner_schedules")
	}
	if !snapshot.schedules[0].Enabled {
		return runtimeDailyAxis(taskcenter.DailyEffectiveDisabled, "scheduler.schedule", "schedule_disabled")
	}
	return runtimeDailyAxis(taskcenter.DailyEffectiveEnabled, "scheduler.schedule", "")
}

func taskMeetingDailyNextRun(cfg *config.Config, snapshot taskMeetingScheduleSnapshot, sourceRef string) taskcenter.DailyValue[time.Time] {
	if snapshot.err != nil {
		return runtimeDailyUnknown[time.Time]("scheduler.schedule", "schedule_query_failed")
	}
	if snapshot.truncated {
		return runtimeDailyUnknown[time.Time]("scheduler.schedule", "schedule_query_limit_reached")
	}
	if snapshot.identityUnproven {
		return runtimeDailyUnknown[time.Time]("scheduler.schedule", "owner_schedule_identity_unproven")
	}
	if len(snapshot.schedules) > 1 {
		return runtimeDailyUnknown[time.Time]("scheduler.schedule", "multiple_owner_schedules")
	}
	if len(snapshot.schedules) == 1 {
		schedule := snapshot.schedules[0]
		if !schedule.Enabled {
			return runtimeDailyNone[time.Time]("scheduler.schedule.next_run_at", "schedule_disabled")
		}
		if schedule.NextRunAt.IsZero() {
			return runtimeDailyUnknown[time.Time]("scheduler.schedule.next_run_at", "next_run_not_recorded")
		}
		return runtimeDailyKnown(schedule.NextRunAt, "scheduler.schedule.next_run_at")
	}
	if cfg == nil || !cfg.Heartbeat.Routine.DailyReportMeetings.Enabled || !cfg.Heartbeat.Enabled {
		return runtimeDailyNone[time.Time](sourceRef, "schedule_not_enabled")
	}
	return runtimeDailyUnknown[time.Time]("scheduler.schedule", "matching_schedule_unavailable")
}

func taskMeetingDailyTargetCount(cfg *config.Config, deps *Dependencies, sourceRef string) taskcenter.DailyValue[int] {
	if cfg == nil {
		return runtimeDailyUnknown[int](sourceRef, "configuration_unavailable")
	}
	actors := cfg.Heartbeat.Routine.DailyReportMeetings.Actors
	if len(actors) == 0 {
		return runtimeDailyKnown(0, sourceRef+".actors")
	}
	if !deps.taskMeetingBindingsReady {
		return runtimeDailyUnknown[int]("runtime.agent_registration", "configured_actor_registration_unavailable")
	}
	return runtimeDailyKnown(len(actors), sourceRef+".actors")
}

func taskMeetingDailyTiming(meeting config.DailyReportMeetingsConfig, hasConfig bool, sourceRef string) taskcenter.DailyTimingProjection {
	if !hasConfig {
		return runtimeDailyTimingUnknown(sourceRef, "configuration_unavailable")
	}
	return taskMeetingDailyTimingFromTiming(meeting.TaskTiming(), sourceRef)
}

func taskMeetingDailyTimingFromTiming(timing modulecore.TaskTiming, sourceRef string) taskcenter.DailyTimingProjection {
	if err := domainmeeting.ValidateDailyMeetingTiming(timing); err != nil {
		return runtimeDailyTimingUnknown(sourceRef, "timing_configuration_incomplete")
	}
	zone := timing.Timezone
	windows := make([]modulecore.TaskTimeWindow, len(timing.AllowedWindows))
	copy(windows, timing.AllowedWindows)
	recurrence := *timing.Recurrence
	recurrence.Times = append([]string(nil), timing.Recurrence.Times...)
	recurrence.Weekdays = append([]time.Weekday(nil), timing.Recurrence.Weekdays...)
	projection := taskcenter.DailyTimingProjection{
		Timezone:                 runtimeDailyKnown(zone, sourceRef+".timezone"),
		ScheduledStartAt:         runtimeDailyNone[time.Time](sourceRef+".scheduled_start_at", "recurrence_has_no_fixed_start_instant"),
		NotBefore:                runtimeDailyNone[time.Time](sourceRef+".not_before", "not_configured"),
		AllowedWindows:           runtimeDailyKnown(windows, sourceRef+".allowed_windows"),
		DeadlineAt:               runtimeDailyNone[time.Time](sourceRef+".deadline_at", "meeting_deadline_is_relative"),
		Recurrence:               runtimeDailyKnown(recurrence, sourceRef+".recurrence"),
		EstimatedDurationSeconds: runtimeDailyNone[int64](sourceRef+".estimated_duration_seconds", "not_configured"),
	}
	if timing.ScheduledStartAt != nil {
		projection.ScheduledStartAt = runtimeDailyKnown(*timing.ScheduledStartAt, sourceRef+".scheduled_start_at")
	}
	if timing.NotBefore != nil {
		projection.NotBefore = runtimeDailyKnown(*timing.NotBefore, sourceRef+".not_before")
	}
	if timing.DeadlineAt != nil {
		projection.DeadlineAt = runtimeDailyKnown(*timing.DeadlineAt, sourceRef+".deadline_at")
	}
	if timing.EstimatedDurationSeconds != nil {
		projection.EstimatedDurationSeconds = runtimeDailyKnown(*timing.EstimatedDurationSeconds, sourceRef+".estimated_duration_seconds")
	}
	return projection
}
