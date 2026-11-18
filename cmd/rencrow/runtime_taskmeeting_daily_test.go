package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	schedulerapp "github.com/Nyukimin/RenCrow_CORE/internal/application/scheduler"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	schedulerpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/scheduler"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskMeetingDailyProjectsPersistedOccurrenceSummary(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	cfg := validRuntimeTaskMeetingConfig()
	workstreamStore, err := workstreampersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "meeting-daily.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error=%v", err)
	}
	t.Cleanup(func() { _ = workstreamStore.Close() })
	deps := &Dependencies{
		schedulerStore:                schedulerpersistence.NewJSONLStore(t.TempDir()),
		taskMeetingScheduleID:         modulecore.NewScheduleID(),
		taskMeetingOccurrenceExecutor: &recordingTaskMeetingOccurrenceExecutor{},
		taskMeetingBindingsReady:      true,
		workstreamStore:               workstreamStore,
	}
	timing := cfg.Heartbeat.Routine.DailyReportMeetings.TaskTiming()
	service := schedulerapp.NewService(deps.schedulerStore, nil)
	createdID, err := ensureTaskMeetingSchedule(context.Background(), deps.schedulerStore, service, deps.taskMeetingScheduleID, timing)
	if err != nil || createdID != deps.taskMeetingScheduleID {
		t.Fatalf("ensure schedule ID=%s err=%v", createdID, err)
	}
	seedTaskMeetingDailyOccurrence(t, workstreamStore, cfg, deps.taskMeetingScheduleID)

	registerRuntimeTaskMeetingDailySource(deps, cfg)
	center, err := taskcenter.NewCenter(nil, deps.taskCenterDailySources, time.Now)
	if err != nil {
		t.Fatalf("NewCenter() error=%v", err)
	}
	scope, err := tool.NewToolExecutionScope(
		"daily-meeting-test", tool.ActorKindUser, "owner-1", "owner-1",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := center.ListDaily(tool.WithToolExecutionScope(context.Background(), scope), taskcenter.DailyQuery{Limit: 10})
	if err != nil {
		t.Fatalf("ListDaily() error=%v", err)
	}
	agentScope, err := tool.NewToolExecutionScope(
		"daily-meeting-agent-test", tool.ActorKindAgent, "mio", "owner-1",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("Agent NewToolExecutionScope() error=%v", err)
	}
	agentResult, err := center.ListDaily(tool.WithToolExecutionScope(context.Background(), agentScope), taskcenter.DailyQuery{Limit: 10})
	if err != nil || len(agentResult.Definitions) != 1 || len(agentResult.Definitions[0].Occurrences) != 1 {
		t.Fatalf("registered in-audience Agent Daily read = %#v, error=%v", agentResult, err)
	}
	if len(result.Definitions) != 1 {
		t.Fatalf("definitions=%+v", result.Definitions)
	}
	definition := result.Definitions[0]
	if definition.DefinitionID != taskMeetingDailyDefinitionID || definition.DataScope != tool.DataScopeUser {
		t.Fatalf("definition identity/scope=%+v", definition.DailyDefinitionDescriptor)
	}
	if definition.Effective.State != taskcenter.DailyEffectiveEnabled || definition.TargetCount.State != taskcenter.DailyValueKnown || definition.TargetCount.Value == nil || *definition.TargetCount.Value != 8 {
		t.Fatalf("configured meeting state=%+v targets=%+v", definition.Effective, definition.TargetCount)
	}
	if definition.Observed.State != taskcenter.DailyObservedAvailable || definition.LatestEvidence.State != taskcenter.DailyValueUnknown || definition.OccurrenceCount.State != taskcenter.DailyValueKnown || definition.OccurrenceCount.Value == nil || *definition.OccurrenceCount.Value != 1 || len(definition.Occurrences) != 1 {
		t.Fatalf("persisted meeting summary was not projected: observed=%+v evidence=%+v count=%+v occurrences=%+v", definition.Observed, definition.LatestEvidence, definition.OccurrenceCount, definition.Occurrences)
	}
	projected := definition.Occurrences[0]
	if projected.State != taskcenter.DailyOccurrenceScheduled || projected.ScheduledAt == nil || projected.ScheduledAt.Value == nil || projected.ScheduledAt.Value.IsZero() ||
		projected.LastAttemptAt.State != taskcenter.DailyValueUnknown || projected.LastSuccessAt.State != taskcenter.DailyValueUnknown || projected.Result.State != taskcenter.DailyValueUnknown {
		t.Fatalf("original slot or unknown execution history was misprojected: %+v", projected)
	}
	progress := projected.MeetingProgress
	if progress == nil || progress.Attendance.Current.State != taskcenter.DailyValueUnknown || progress.Attendance.Total.Value == nil || *progress.Attendance.Total.Value != 8 ||
		progress.DeliveredReports.Current.Value == nil || *progress.DeliveredReports.Current.Value != 0 ||
		progress.AnsweredConcerns.Current.Value == nil || *progress.AnsweredConcerns.Current.Value != 0 ||
		progress.AnsweredConcerns.Total.Value == nil || *progress.AnsweredConcerns.Total.Value != 0 ||
		progress.UnresolvedConcerns.Value == nil || *progress.UnresolvedConcerns.Value != 0 || progress.Minutes.State != taskcenter.DailyValueNone {
		t.Fatalf("source-backed meeting progress confused missing attendance with known empty history: %+v", progress)
	}
	if definition.Timing.Timezone.Value == nil || *definition.Timing.Timezone.Value != domainmeeting.TimezoneJST || definition.Timing.Recurrence.Value == nil || len(definition.Timing.Recurrence.Value.Times) != 3 {
		t.Fatalf("configured timing projection=%+v", definition.Timing)
	}
	if definition.Timing.AllowedWindows.Value == nil || len(*definition.Timing.AllowedWindows.Value) != 0 || definition.Timing.DeadlineAt.State != taskcenter.DailyValueNone {
		t.Fatalf("explicit timing absence/empty projection=%+v", definition.Timing)
	}
	if definition.NextRunAt.State != taskcenter.DailyValueKnown || len(definition.NativeRefs) != 1 || definition.NativeRefs[0].NativeID != string(deps.taskMeetingScheduleID) {
		t.Fatalf("actual scheduler reference was not projected: next=%+v refs=%+v", definition.NextRunAt, definition.NativeRefs)
	}
	encoded, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	for _, privateValue := range []string{"owner-1", "world-private", "room-private"} {
		if strings.Contains(string(encoded), privateValue) {
			t.Fatalf("private runtime config leaked through Daily DTO (%s)", privateValue)
		}
	}
}

func seedTaskMeetingDailyOccurrence(t *testing.T, store *workstreampersistence.SQLiteStore, cfg *config.Config, scheduleID modulecore.ScheduleID) {
	t.Helper()
	meetingConfig := cfg.Heartbeat.Routine.DailyReportMeetings
	actors := make([]string, 0, len(meetingConfig.Actors))
	bindings := make([]domainmeeting.WorldActorBinding, 0, len(meetingConfig.Actors))
	for _, actor := range meetingConfig.Actors {
		actors = append(actors, actor.CoreActorID)
		bindings = append(bindings, domainmeeting.WorldActorBinding{CoreActorID: actor.CoreActorID, WorldActorID: actor.WorldActorID})
	}
	configSnapshot := domainmeeting.ConfigSnapshot{
		ScheduleID: scheduleID, Revision: meetingConfig.Revision, Timing: meetingConfig.TaskTiming(),
		ConfiguredActors: append([]string(nil), actors...), WorldActorBindings: bindings, RegisteredActors: append([]string(nil), actors...),
		RegistrationEvidenceID: modulecore.NewEvidenceID(), Rooms: domainmeeting.RoomRefs{Living: meetingConfig.LivingRoomRef, Dining: meetingConfig.DiningRoomRef},
		MeetingDeadlineAfterSecs: meetingConfig.MeetingDeadlineAfterSecs,
	}
	key := domainmeeting.OccurrenceKey{ScheduleID: scheduleID, LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST}
	meeting, err := domainmeeting.NewOccurrence(configSnapshot, key)
	if err != nil {
		t.Fatalf("NewOccurrence() error=%v", err)
	}
	writeScope, err := tool.NewToolExecutionScope("meeting-daily-writer", tool.ActorKindAgent, meetingConfig.CoordinatorActorID,
		meetingConfig.OwnerUserID, []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatalf("writer scope error=%v", err)
	}
	access := domainmeeting.StoreAccess{
		CoordinatorActorID: meetingConfig.CoordinatorActorID, OwnerUserID: meetingConfig.OwnerUserID,
		Audience: append([]string(nil), actors...), AuthorityEvidence: modulecore.NewEvidenceID(),
	}
	if _, err := store.LoadOrCreateMeetingOccurrence(tool.WithToolExecutionScope(context.Background(), writeScope), access, key, meeting); err != nil {
		t.Fatalf("LoadOrCreateMeetingOccurrence() error=%v", err)
	}
}

func TestTaskMeetingDailySuccessRequiresFinalProofAndActualMinutes(t *testing.T) {
	scheduledAt := time.Date(2026, 11, 18, 8, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	finishAt := scheduledAt.Add(time.Hour)
	attendance := 8
	summary := workstreampersistence.TaskMeetingOccurrenceSummary{
		Key:      domainmeeting.OccurrenceKey{ScheduleID: modulecore.NewScheduleID(), LocalDate: "2026-11-18", Slot: "08:00", Timezone: domainmeeting.TimezoneJST},
		Audience: []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"},
		Status:   domainmeeting.StatusCompleted, ScheduledAt: scheduledAt, FinishProofPersistedAt: &finishAt,
		AttendanceCount: &attendance, AttendanceScopeID: modulecore.NewEvidenceID(), AttendanceObservedAt: &finishAt,
		DeliveredReportCount: 8, ReportedConcernCount: 2, AnsweredConcernCount: 1, UnresolvedConcernCount: 1,
	}
	withoutMinutes, evidence, issue := projectTaskMeetingDailyOccurrence(summary)
	if withoutMinutes.State != taskcenter.DailyOccurrenceUnknown || evidence.LastResult.State != taskcenter.DailyValueUnknown || !issue {
		t.Fatalf("completed status without a persisted minutes reference appeared successful: occurrence=%+v evidence=%+v issue=%v", withoutMinutes, evidence, issue)
	}
	artifactID := modulecore.NewArtifactID()
	minutesCreatedAt := finishAt.Add(time.Second)
	summary.Minutes = &workstreampersistence.TaskMeetingMinutesReference{
		ArtifactID: artifactID, WorkstreamID: modulecore.NewWorkstreamID(), OwnerUserID: "owner-1",
		OccurrenceKey: summary.Key.String(), CreatedAt: minutesCreatedAt,
	}
	withMinutes, evidence, issue := projectTaskMeetingDailyOccurrence(summary)
	if issue || withMinutes.State != taskcenter.DailyOccurrenceSucceeded || withMinutes.Result.Value == nil || *withMinutes.Result.Value != taskcenter.DailyOutcomeSuccess ||
		withMinutes.LastSuccessAt.Value == nil || !withMinutes.LastSuccessAt.Value.Equal(finishAt) || evidence.LastAttemptAt.State != taskcenter.DailyValueUnknown ||
		!containsDailyNativeRef(withMinutes.NativeRefs, string(artifactID)) {
		t.Fatalf("verified completion did not project final evidence and actual minutes: occurrence=%+v evidence=%+v issue=%v", withMinutes, evidence, issue)
	}
	progress := withMinutes.MeetingProgress
	if progress == nil || progress.Attendance.Current.Value == nil || *progress.Attendance.Current.Value != 8 ||
		progress.DeliveredReports.Current.Value == nil || *progress.DeliveredReports.Current.Value != 8 ||
		progress.AnsweredConcerns.Current.Value == nil || *progress.AnsweredConcerns.Current.Value != 1 ||
		progress.AnsweredConcerns.Total.Value == nil || *progress.AnsweredConcerns.Total.Value != 2 ||
		progress.UnresolvedConcerns.Value == nil || *progress.UnresolvedConcerns.Value != 1 ||
		progress.Minutes.State != taskcenter.DailyValueKnown || progress.Minutes.Value == nil || progress.Minutes.Value.NativeID != string(artifactID) {
		t.Fatalf("verified owner checkpoint counters/minutes ref were not projected: %+v", progress)
	}
}

func containsDailyNativeRef(refs []taskcenter.DailyResourceRef, nativeID string) bool {
	for _, ref := range refs {
		if ref.NativeID == nativeID {
			return true
		}
	}
	return false
}

func TestTaskMeetingDailyKeepsParentHeartbeatEffectiveBlock(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	cfg := validRuntimeTaskMeetingConfig()
	cfg.Heartbeat.Enabled = false
	configured, effective := taskMeetingDailyConfigAxes(cfg, &Dependencies{taskMeetingBindingsReady: true, taskMeetingOccurrenceExecutor: &recordingTaskMeetingOccurrenceExecutor{}, taskMeetingScheduleID: modulecore.NewScheduleID()}, "heartbeat.routine.daily_report_meetings")
	if configured.State != taskcenter.DailyConfiguredConfigured || effective.State != taskcenter.DailyEffectiveBlocked || effective.ReasonCode != "parent_heartbeat_disabled" {
		t.Fatalf("configured=%+v effective=%+v", configured, effective)
	}
}

func TestTaskMeetingDailyReportsWorldCredentialUnavailable(t *testing.T) {
	t.Setenv("WORLD_READ_TOKEN", "")
	cfg := validRuntimeTaskMeetingConfig()
	configured, effective := taskMeetingDailyConfigAxes(cfg, &Dependencies{
		taskMeetingBindingsReady: true, taskMeetingOccurrenceExecutor: &recordingTaskMeetingOccurrenceExecutor{},
		taskMeetingScheduleID: modulecore.NewScheduleID(),
	}, "heartbeat.routine.daily_report_meetings")
	if configured.State != taskcenter.DailyConfiguredConfigured || effective.State != taskcenter.DailyEffectiveUnavailable ||
		effective.SourceRef != "world_projection.credential" || effective.ReasonCode != "world_credential_unavailable" {
		t.Fatalf("missing WORLD credential was not projected as unavailable: configured=%+v effective=%+v", configured, effective)
	}
}

func TestTaskMeetingDailyImplementationAxisDoesNotDependOnActivation(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	cfg.Heartbeat.Routine.DailyReportMeetings.Enabled = false
	deps := &Dependencies{}
	registerRuntimeTaskMeetingDailySource(deps, cfg)
	if len(deps.taskCenterDailySources) != 1 {
		t.Fatalf("daily source count = %d, want 1", len(deps.taskCenterDailySources))
	}
	definition := deps.taskCenterDailySources[0].Descriptor().Definitions[0]
	if definition.Implementation.State != taskcenter.DailyImplementationImplemented || definition.Implementation.ReasonCode != "" {
		t.Fatalf("disabled but implemented feature reported as unimplemented: %+v", definition.Implementation)
	}
	configured, effective := taskMeetingDailyConfigAxes(cfg, deps, "heartbeat.routine.daily_report_meetings")
	if configured.State != taskcenter.DailyConfiguredDisabled || effective.State != taskcenter.DailyEffectiveDisabled {
		t.Fatalf("activation state was not kept on configured/effective axes: configured=%+v effective=%+v", configured, effective)
	}
}

func TestTaskMeetingDailyAgentReadRequiresRegisteredTrustedActorAndSavedAudience(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	registered := taskMeetingDailyRegisteredActors(cfg.Heartbeat.Routine.DailyReportMeetings)
	trustedAgent, err := tool.NewToolExecutionScope(
		"meeting-daily-agent-read", tool.ActorKindAgent, "mio", "owner-1",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	ctx := tool.WithToolExecutionScope(context.Background(), trustedAgent)
	if err := validateTaskMeetingDailyReadScope(ctx, trustedAgent, "owner-1", registered, true); err != nil {
		t.Fatalf("registered trusted agent read scope rejected: %v", err)
	}
	if err := authorizeTaskMeetingDailyActor(trustedAgent, workstreampersistence.TaskMeetingOccurrenceSummary{Audience: []string{"mio", "shiro"}}); err != nil {
		t.Fatalf("saved audience participant rejected: %v", err)
	}
	if err := authorizeTaskMeetingDailyActor(trustedAgent, workstreampersistence.TaskMeetingOccurrenceSummary{Audience: []string{"shiro"}}); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("actor outside saved occurrence audience error = %v, want forbidden", err)
	}

	unregistered, err := tool.NewToolExecutionScope(
		"meeting-daily-unregistered-read", tool.ActorKindAgent, "intruder", "owner-1",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(unregistered) error = %v", err)
	}
	if err := validateTaskMeetingDailyReadScope(tool.WithToolExecutionScope(context.Background(), unregistered), unregistered, "owner-1", registered, true); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("unregistered actor scope error = %v, want forbidden", err)
	}
	if err := validateTaskMeetingDailyReadScope(ctx, trustedAgent, "owner-2", registered, true); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("foreign authenticated owner scope error = %v, want forbidden", err)
	}
	if err := validateTaskMeetingDailyReadScope(ctx, trustedAgent, "owner-1", registered, false); !errors.Is(err, domainworkstream.ErrTaskPlanForbidden) {
		t.Fatalf("actor read without actual runtime registration error = %v, want forbidden", err)
	}

	userScope, err := tool.NewToolExecutionScope(
		"meeting-daily-user-read", tool.ActorKindUser, "owner-1", "owner-1",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(user) error = %v", err)
	}
	if err := validateTaskMeetingDailyReadScope(tool.WithToolExecutionScope(context.Background(), userScope), userScope, "owner-1", registered, false); err != nil {
		t.Fatalf("authenticated owner HTTP read scope rejected: %v", err)
	}
}

func TestTaskMeetingRuntimeBindingsRequireEightActualRegistrations(t *testing.T) {
	cfg := validRuntimeTaskMeetingConfig()
	agents := agentRuntime{
		Mio:   &agent.MioAgent{},
		Shiro: &agent.ShiroAgent{},
		Heavy: &agent.HeavyAgent{},
		Wild:  &agent.WildAgent{},
	}
	coders := []*coderAdapter{{}, {}, {}, {}}
	if !taskMeetingRuntimeBindingsReady(cfg, agents, coders...) {
		t.Fatal("exact eight runtime actor bindings were not recognized")
	}
	coders[3] = nil
	if taskMeetingRuntimeBindingsReady(cfg, agents, coders...) {
		t.Fatal("missing coder registration was accepted")
	}
	cfg.Heartbeat.Routine.DailyReportMeetings.Actors[7].CoreActorID = "coder4"
	coders[3] = &coderAdapter{}
	if taskMeetingRuntimeBindingsReady(cfg, agents, coders...) {
		t.Fatal("configuration slot name was accepted as a registered actor ID")
	}
}

func validRuntimeTaskMeetingConfig() *config.Config {
	actors := []config.DailyReportMeetingActor{
		{CoreActorID: "mio", WorldActorID: "world-private-mio"},
		{CoreActorID: "shiro", WorldActorID: "world-private-shiro"},
		{CoreActorID: "kuro", WorldActorID: "world-private-kuro"},
		{CoreActorID: "midori", WorldActorID: "world-private-midori"},
		{CoreActorID: "aka", WorldActorID: "world-private-aka"},
		{CoreActorID: "ao", WorldActorID: "world-private-ao"},
		{CoreActorID: "kin", WorldActorID: "world-private-kin"},
		{CoreActorID: "gin", WorldActorID: "world-private-gin"},
	}
	return &config.Config{
		Coder1:        config.CoderConfig{Enabled: true, Name: "aka"},
		Coder2:        config.CoderConfig{Enabled: true, Name: "ao"},
		Coder3:        config.CoderConfig{Enabled: true, Name: "kin"},
		Coder4:        config.CoderConfig{Enabled: true, Name: "gin"},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "owner-1"},
		WorldProjection: config.WorldProjectionConfig{
			BaseURL: "http://world.example.test", TokenEnv: "WORLD_READ_TOKEN",
			WorldID: "world-private", TimelineID: "timeline-private", TimelineEpoch: "epoch-private",
		},
		Heartbeat: config.HeartbeatConfig{Enabled: true, Routine: config.HeartbeatRoutineConfig{DailyReportMeetings: config.DailyReportMeetingsConfig{
			Enabled: true, Revision: 1, OwnerUserID: "owner-1", DataScope: tool.DataScopeUser,
			CoordinatorActorID: "shiro", Actors: actors,
			LivingRoomRef: "room-private-living", DiningRoomRef: "room-private-dining",
			Timezone: domainmeeting.TimezoneJST, Times: []string{"08:00", "12:00", "17:00"}, MeetingDeadlineAfterSecs: 3600,
		}}},
	}
}
