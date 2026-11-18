package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	worldmeeting "github.com/Nyukimin/RenCrow_CORE/internal/adapter/taskmeeting"
	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/worldread"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainscheduler "github.com/Nyukimin/RenCrow_CORE/internal/domain/scheduler"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainmeeting "github.com/Nyukimin/RenCrow_CORE/internal/domain/taskmeeting"
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	llmmiddleware "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	actionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/action"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	transportpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/transport"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskMeetingOccurrenceTaskTimingIsOneShotAtScheduledDue(t *testing.T) {
	due := time.Date(2026, 11, 18, 9, 0, 0, 0, time.FixedZone("JST", 9*60*60))
	deadline := due.Add(2 * time.Hour)
	duration := int64(900)
	base := modulecore.TaskTiming{
		Timezone: "Asia/Tokyo", NotBefore: func() *time.Time { value := due.Add(-time.Minute); return &value }(),
		DeadlineAt: &deadline, EstimatedDurationSeconds: &duration,
		Recurrence: &modulecore.TaskRecurrence{Frequency: modulecore.TaskRecurrenceDaily, Times: []string{"09:00"}},
	}
	actual, err := taskMeetingOccurrenceTaskTiming(base, due)
	if err != nil {
		t.Fatalf("project occurrence timing: %v", err)
	}
	if actual.ScheduledStartAt == nil || !actual.ScheduledStartAt.Equal(due) {
		t.Fatalf("scheduled start = %v, want original due %v", actual.ScheduledStartAt, due)
	}
	if actual.Recurrence == nil || actual.Recurrence.Frequency != modulecore.TaskRecurrenceNone || len(actual.Recurrence.Times) != 0 || len(actual.Recurrence.Weekdays) != 0 {
		t.Fatalf("occurrence timing retained daily recurrence: %+v", actual.Recurrence)
	}
	if actual.Timezone != base.Timezone || actual.NotBefore == nil || !actual.NotBefore.Equal(*base.NotBefore) || actual.DeadlineAt == nil || !actual.DeadlineAt.Equal(deadline) || actual.EstimatedDurationSeconds == nil || *actual.EstimatedDurationSeconds != duration {
		t.Fatalf("occurrence timing lost owner constraints: got=%+v base=%+v", actual, base)
	}
	if err := actual.Validate(); err != nil {
		t.Fatalf("projected timing is invalid: %v", err)
	}
}

func TestTaskMeetingRuntimeExecutorFailsClosedWithoutNativeOwners(t *testing.T) {
	if got := newRuntimeTaskMeetingOccurrenceExecutor(nil, nil, agentRuntime{}); got != nil {
		t.Fatalf("executor without native owner ports = %T, want nil", got)
	}
	if got := newRuntimeTaskMeetingOccurrenceExecutorWithClock(nil, nil, agentRuntime{}, time.Now); got != nil {
		t.Fatalf("clock executor without native owner ports = %T, want nil", got)
	}
	if runtimeTaskMeetingOwnersReady(nil) {
		t.Fatal("nil runtime dependencies reported owner ports ready")
	}
}

func TestRuntimeTaskMeetingOccurrenceRunsEightActorsThroughNativeOwners(t *testing.T) {
	setTaskMeetingWorldReadCredential(t)
	ctx := context.Background()
	dir := t.TempDir()
	now := time.Date(2026, time.November, 18, 8, 2, 0, 0, time.FixedZone("JST", 9*60*60))
	clock := func() time.Time { return now }
	queue, err := newRuntimePersistenceQueue()
	if err != nil {
		t.Fatalf("open shared persistence queue: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown shared persistence queue: %v", err)
		}
	}()
	taskPath := filepath.Join(dir, "tasks")
	eventPath := filepath.Join(dir, "events.sqlite")
	workstreamPath := filepath.Join(dir, "workstream.sqlite")
	taskStore, err := taskpersistence.NewJSONLStoreWithOptions(taskPath, taskpersistence.OpenOptions{Index: true, TaskMeetingClock: clock})
	if err != nil {
		t.Fatalf("open Task owner: %v", err)
	}
	if err := bindRuntimeOwnerPersistenceQueue(taskStore, queue); err != nil {
		t.Fatalf("bind Task owner to shared persistence queue: %v", err)
	}
	writeQueue, err := eventstore.NewPersistenceQueueWriteQueue(queue)
	if err != nil {
		t.Fatalf("construct EventStore shared queue adapter: %v", err)
	}
	eventStore, err := eventstore.NewSQLiteStoreWithWriteQueueAndTaskMeetingClock(eventPath, writeQueue, clock)
	if err != nil {
		t.Fatalf("open EventStore owner: %v", err)
	}
	workstreamStore, err := workstreampersistence.NewSQLiteStoreWithTaskMeetingClock(workstreamPath, clock)
	if err != nil {
		t.Fatalf("open Workstream owner: %v", err)
	}
	defer func() {
		if taskStore != nil {
			if err := taskStore.Close(); err != nil {
				t.Errorf("close Task owner: %v", err)
			}
		}
		if eventStore != nil {
			if err := eventStore.Close(); err != nil {
				t.Errorf("close EventStore owner: %v", err)
			}
		}
		if workstreamStore != nil {
			if err := workstreamStore.Close(); err != nil {
				t.Errorf("close Workstream owner: %v", err)
			}
		}
	}()
	manager := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits(), taskmanager.WithClock(clock))
	if err := bindRuntimeOwnerPersistenceQueue(manager, queue); err != nil {
		t.Fatalf("bind TaskManager to shared persistence queue: %v", err)
	}
	actionOwner, err := newRuntimeActionOwner(filepath.Join(dir, "runtime"), actionpersistence.OpenOptions{Index: true}, queue)
	if err != nil {
		t.Fatalf("open Action owner on shared queue: %v", err)
	}
	transportStore, err := transportpersistence.NewJSONLStore(filepath.Join(dir, "runtime", "state", "transport"))
	if err != nil {
		t.Fatalf("open Transport owner store: %v", err)
	}
	if err := transportStore.SetPersistenceQueue(queue); err != nil {
		t.Fatalf("bind Transport owner to shared queue: %v", err)
	}
	transportManager := transportmanager.New(transportStore, actionOwner.manager)
	provider := &runtimeTaskMeetingFakeProvider{}
	wrappedProvider, err := llmmiddleware.WithActionExecutionOwners(provider, actionOwner.manager, manager, transportManager, "mio")
	if err != nil {
		t.Fatalf("wrap deterministic provider with native Action/Transport owners: %v", err)
	}
	cfg := validRuntimeTaskMeetingConfig()
	deps := &Dependencies{
		taskStore: taskStore, taskManager: manager, canonicalEventStore: eventStore, workstreamStore: workstreamStore,
		taskCenterUserID: "owner-1", taskCenterUserToken: []byte("fixture-owner-token"),
	}
	actors := agentRuntime{
		Mio:   domainagent.NewMioAgent(wrappedProvider, nil, nil, nil, nil, nil).WithSystemPrompt("fixture mio"),
		Shiro: domainagent.NewShiroAgent(wrappedProvider, nil, nil, "fixture shiro", nil),
		Heavy: domainagent.NewHeavyAgent(wrappedProvider, "fixture kuro"),
		Wild:  domainagent.NewWildAgent(wrappedProvider, "fixture midori"),
	}
	coders := []*coderAdapter{
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "aka")},
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "ao")},
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "kin")},
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "gin")},
	}
	scheduleID, enabled, err := registerRuntimeTaskMeetingSchedule(ctx, cfg, deps, actors, coders...)
	if err != nil || !enabled || scheduleID.Validate() != nil {
		t.Fatalf("register actual meeting owner: id=%s enabled=%t err=%v", scheduleID, enabled, err)
	}
	timing := cfg.Heartbeat.Routine.DailyReportMeetings.TaskTiming()
	key, schedule, due := taskMeetingOccurrenceFixture(t, scheduleID, timing)
	resolved, err := resolveRuntimeTaskMeetingOccurrence(ctx, cfg, deps, schedule, due, actors, coders...)
	if err != nil {
		t.Fatalf("pin/read actual occurrence: %v", err)
	}
	if resolved.Key != key {
		t.Fatalf("resolved key=%s want=%s", resolved.Key, key)
	}
	statusActor := resolved.Declaration.Audience[0]
	publicTask, publicRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "fixture-public-task-title-must-not-leak", OwnerID: statusActor, Assignee: statusActor, Route: domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create real public self Task/Run: %v", err)
	}
	foreignTask, err := manager.Create(ctx, domaintask.Task{
		Title: "fixture-foreign-task-title-must-not-leak", OwnerID: statusActor, Assignee: "outsider", Route: domaintask.RouteGeneral,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create foreign public Task control: %v", err)
	}
	privateTask := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "fixture-private-task-title-must-not-leak", OwnerID: statusActor, Assignee: statusActor,
		Route: domaintask.RouteGeneral, WorkstreamID: modulecore.NewWorkstreamID(), GoalID: modulecore.NewGoalID(),
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "taskmeeting-fixture-user"},
	}
	privateTask.ApplyDefaults(clock())
	if err := taskStore.SaveTaskAuthorized(ctx, privateTask, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
		if input.TaskID != privateTask.TaskID || input.WorkstreamID != privateTask.WorkstreamID || input.GoalID != privateTask.GoalID || input.AuthenticatedUserID != "taskmeeting-fixture-user" {
			return errors.New("private Task fixture authorization tuple mismatch")
		}
		return nil
	}); err != nil {
		t.Fatalf("save private Task control through authorized owner port: %v", err)
	}
	provider.statusActor = statusActor
	provider.statusTaskRef = publicTask.TaskID
	provider.foreignTaskRef = foreignTask.TaskID
	provider.privateTaskRef = privateTask.TaskID
	executorValue := newRuntimeTaskMeetingOccurrenceExecutorWithClock(cfg, deps, actors, clock, coders...)
	executor, ok := executorValue.(*runtimeTaskMeetingOccurrenceExecutor)
	if !ok || executor == nil {
		t.Fatalf("executor did not bind native owners/actor roster: native_owners=%t bindings=%t registered=%d", runtimeTaskMeetingOwnersReady(deps), taskMeetingRuntimeBindingsReady(cfg, actors, coders...), len(registeredTaskMeetingActors(cfg, actors, coders...)))
	}
	executor.worldSnapshotReader = runtimeTaskMeetingFakeWorld{declaration: resolved.Declaration}
	setRuntimeTaskMeetingFakeGather(executor, resolved.Declaration)
	statuses, err := executor.taskMeetingShareableStatuses(ctx, statusActor)
	if err != nil || len(statuses) != 1 || statuses[0].ActorID != statusActor || statuses[0].TaskRef != publicTask.TaskID || statuses[0].Status != domaintask.StatusRunning {
		t.Fatalf("public self status projection = %+v err=%v, want only the exact public self Task", statuses, err)
	}
	if result, err := executor.ExecuteScheduledOccurrence(ctx, schedule, due); err != nil || result != taskMeetingSchedulerSafeSummary {
		t.Fatalf("execute native eight-actor occurrence: result=%q err=%v provider_calls=%d report_calls=%d", result, err, provider.calls, provider.reportCalls)
	}
	ownerCtx, err := executor.withOwnerScope(ctx, resolved.Declaration.CoordinatorActorID, resolved.Key.String()+"|verify")
	if err != nil {
		t.Fatalf("bind occurrence verification scope: %v", err)
	}
	access, err := (runtimeTaskMeetingAuthority{snapshot: resolved}).AuthorizeOccurrence(ownerCtx, resolved.Key, resolved.Config)
	if err != nil {
		t.Fatalf("authorize result read: %v", err)
	}
	meeting, found, err := workstreamStore.FindMeetingOccurrence(ownerCtx, access, resolved.Key)
	if err != nil || !found {
		t.Fatalf("read persisted meeting: found=%t err=%v", found, err)
	}
	if meeting.Status != domainmeeting.StatusCompleted || meeting.FinishEvidence == nil || meeting.FinishEvidence.TranscriptArtifactID.Validate() != nil || len(meeting.Reports) != domaintask.TaskMeetingAudienceSize || len(meeting.Answers) != 1 {
		t.Fatalf("meeting terminal projection status=%s reports=%d answers=%d finish=%+v", meeting.Status, len(meeting.Reports), len(meeting.Answers), meeting.FinishEvidence)
	}
	var selfReport *domainmeeting.Report
	for index := range meeting.Reports {
		report := &meeting.Reports[index]
		if report.Utterance.ActorID == statusActor {
			selfReport = report
			continue
		}
		if report.Current.State != domainmeeting.ReportFieldUndecided || report.Next.State != domainmeeting.ReportFieldUndecided || report.Current.TaskRef != nil || report.Next.TaskRef != nil {
			t.Fatalf("actor %s inferred no work from its partial public status view: current=%+v next=%+v", report.Utterance.ActorID, report.Current, report.Next)
		}
	}
	if selfReport == nil || selfReport.Current.State != "in_progress" || selfReport.Current.TaskRef == nil || *selfReport.Current.TaskRef != publicTask.TaskID || selfReport.Current.Text != "Working on a public task." || selfReport.Next.State != domainmeeting.ReportFieldUndecided {
		t.Fatalf("report did not use its supplied public self status: %+v", selfReport)
	}
	for _, expected := range []string{"native owner report", "Current: in_progress — Working on a public task.", "Next: undecided", "Need review timeline before release."} {
		if !strings.Contains(selfReport.Utterance.Body, expected) {
			t.Fatalf("canonical report body omitted structured field %q: %q", expected, selfReport.Utterance.Body)
		}
	}
	statusesAfterControls, err := executor.taskMeetingShareableStatuses(ctx, statusActor)
	if err != nil || len(statusesAfterControls) != 1 || statusesAfterControls[0].TaskRef != publicTask.TaskID || statusesAfterControls[0].Status != domaintask.StatusRunning {
		t.Fatalf("public status projection included meeting controls or changed after execution: %+v err=%v", statusesAfterControls, err)
	}
	if _, err := manager.CompleteRun(ctx, publicTask.TaskID, publicRun.RunID, statusActor, domaintask.StatusSucceeded, "fixture public task complete", ""); err != nil {
		t.Fatalf("close public status Task/Run after meeting: %v", err)
	}
	answerBody := meeting.Answers[0].Utterance.Body
	for _, expected := range []string{"Need review timeline before release.", "Answer type: concrete_support", "reviewed with the owner before release"} {
		if !strings.Contains(answerBody, expected) {
			t.Fatalf("canonical answer body omitted concern/answer field %q: %q", expected, answerBody)
		}
	}
	for _, report := range meeting.Reports {
		if report.Utterance.DeliveryState != domainmeeting.DeliveryDelivered || report.Utterance.DeliveryEvidence == nil || !report.Utterance.DeliveryEvidence.Delivered || len(report.Utterance.DeliveryEvidence.Audience) != domaintask.TaskMeetingAudienceSize || report.Utterance.SourceTaskRef == nil || report.Utterance.SourceRunRef == nil {
			t.Fatalf("report was not generated and delivered by native owners: %+v", report.Utterance)
		}
		task, run, _, err := taskStore.GetTaskMeetingInvocationState(ctx, *report.Utterance.SourceTaskRef, *report.Utterance.SourceRunRef, executor.authorizeTaskState(resolved))
		if err != nil || task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded || task.TaskMeetingResult == nil {
			t.Fatalf("native report Task/Run is not succeeded with exact output: task=%s run=%s err=%v", task.Status, run.Status, err)
		}
	}
	rootTask, rootRun, rootProof, err := manager.FindTaskMeetingRootByOccurrence(ctx, resolved.Key.String(), resolved.Declaration.CoordinatorActorID, func(context.Context) error { return nil }, executor.authorizeTaskState(resolved))
	if err != nil || rootTask.Status != domaintask.StatusSucceeded || rootRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("read completed native root: task=%s run=%s err=%v", rootTask.Status, rootRun.Status, err)
	}
	for _, actorID := range resolved.Declaration.Audience {
		contextTask, contextRun, shared, err := manager.FindTaskMeetingRecipientContextByActor(ctx, rootProof, actorID, executor.authorizeTaskState(resolved), executor.authorizeTaskState(resolved), func(context.Context) error { return nil })
		if err != nil || contextTask.Status != domaintask.StatusSucceeded || contextRun.Status != domaintask.RunStatusSucceeded || len(shared.TaskMeetingConsumedMessages) != domaintask.TaskMeetingAudienceSize+1 {
			t.Fatalf("actor %s context did not persist all eight real message consumptions: status=%s/%s consumed=%d err=%v", actorID, contextTask.Status, contextRun.Status, len(shared.TaskMeetingConsumedMessages), err)
		}
	}
	if provider.calls != domaintask.TaskMeetingAudienceSize+1 {
		t.Fatalf("LLM effect calls=%d want eight reports plus one substantive answer", provider.calls)
	}
	assertRuntimeTaskMeetingActionTransportLineage(t, ctx, taskStore, executor.authorizeTaskState(resolved), actionOwner.manager, transportManager, rootTask, meeting, provider.lineage)
	active := -1
	err = taskStore.ReadTransaction(ctx, func(store domaintask.Store) error {
		counter, ok := store.(domaintask.TaskMeetingActiveGenerationCounter)
		if !ok {
			return errors.New("Task store lacks typed active generation counter")
		}
		var err error
		active, err = counter.CountTaskMeetingActiveGenerations(ctx)
		return err
	})
	if err != nil || active != 0 {
		t.Fatalf("terminal meeting left active generation capacity=%d err=%v", active, err)
	}

	// Reopen each durable meeting owner and replay the same occurrence. The
	// historical receipt must be read before any new actor/provider effect.
	if err := taskStore.Close(); err != nil {
		t.Fatalf("close Task owner before replay: %v", err)
	}
	taskStore = nil
	if err := eventStore.Close(); err != nil {
		t.Fatalf("close EventStore owner before replay: %v", err)
	}
	eventStore = nil
	if err := workstreamStore.Close(); err != nil {
		t.Fatalf("close Workstream owner before replay: %v", err)
	}
	workstreamStore = nil
	taskStore, err = taskpersistence.NewJSONLStoreWithOptions(taskPath, taskpersistence.OpenOptions{Index: true, TaskMeetingClock: clock})
	if err != nil {
		t.Fatalf("reopen Task owner for historical replay: %v", err)
	}
	if err := bindRuntimeOwnerPersistenceQueue(taskStore, queue); err != nil {
		t.Fatalf("rebind Task owner to shared persistence queue: %v", err)
	}
	eventStore, err = eventstore.NewSQLiteStoreWithWriteQueueAndTaskMeetingClock(eventPath, writeQueue, clock)
	if err != nil {
		t.Fatalf("reopen EventStore owner for historical replay: %v", err)
	}
	workstreamStore, err = workstreampersistence.NewSQLiteStoreWithTaskMeetingClock(workstreamPath, clock)
	if err != nil {
		t.Fatalf("reopen Workstream owner for historical replay: %v", err)
	}
	manager = taskmanager.New(taskStore, taskmanager.DefaultParallelLimits(), taskmanager.WithClock(clock))
	if err := bindRuntimeOwnerPersistenceQueue(manager, queue); err != nil {
		t.Fatalf("rebind TaskManager to shared persistence queue: %v", err)
	}
	deps.taskStore, deps.taskManager, deps.canonicalEventStore, deps.workstreamStore = taskStore, manager, eventStore, workstreamStore
	wrappedProvider, err = llmmiddleware.WithActionExecutionOwners(provider, actionOwner.manager, manager, transportManager, "mio")
	if err != nil {
		t.Fatalf("rewrap provider with reopened native owners: %v", err)
	}
	actors = agentRuntime{
		Mio:   domainagent.NewMioAgent(wrappedProvider, nil, nil, nil, nil, nil).WithSystemPrompt("fixture mio"),
		Shiro: domainagent.NewShiroAgent(wrappedProvider, nil, nil, "fixture shiro", nil),
		Heavy: domainagent.NewHeavyAgent(wrappedProvider, "fixture kuro"),
		Wild:  domainagent.NewWildAgent(wrappedProvider, "fixture midori"),
	}
	coders = []*coderAdapter{
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "aka")},
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "ao")},
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "kin")},
		{domainCoder: domainagent.NewCoderAgent(wrappedProvider, nil, nil, "gin")},
	}
	executorValue = newRuntimeTaskMeetingOccurrenceExecutorWithClock(cfg, deps, actors, clock, coders...)
	executor, ok = executorValue.(*runtimeTaskMeetingOccurrenceExecutor)
	if !ok || executor == nil {
		t.Fatalf("executor did not recover native owners after restart: %T", executorValue)
	}
	executor.worldSnapshotReader = runtimeTaskMeetingFakeWorld{declaration: resolved.Declaration}
	setRuntimeTaskMeetingFakeGather(executor, resolved.Declaration)
	if result, err := executor.ExecuteScheduledOccurrence(ctx, schedule, due); err != nil || result != taskMeetingSchedulerSafeSummary {
		t.Fatalf("replay completed occurrence after owner restart: result=%q err=%v", result, err)
	}
	if provider.calls != domaintask.TaskMeetingAudienceSize+1 || len(provider.lineage) != domaintask.TaskMeetingAudienceSize+1 {
		t.Fatalf("completed receipt replay repeated provider work: calls=%d lineage=%d", provider.calls, len(provider.lineage))
	}
	replayCtx, err := executor.withOwnerScope(ctx, resolved.Declaration.CoordinatorActorID, resolved.Key.String()+"|restart-replay-verify")
	if err != nil {
		t.Fatalf("bind restarted owner verification scope: %v", err)
	}
	replayAccess, err := (runtimeTaskMeetingAuthority{snapshot: resolved}).AuthorizeOccurrence(replayCtx, resolved.Key, resolved.Config)
	if err != nil {
		t.Fatalf("authorize restarted occurrence read: %v", err)
	}
	replayedMeeting, replayFound, err := workstreamStore.FindMeetingOccurrence(replayCtx, replayAccess, resolved.Key)
	if err != nil || !replayFound || replayedMeeting.Status != domainmeeting.StatusCompleted || replayedMeeting.FinishEvidence == nil || replayedMeeting.FinishEvidence.TranscriptArtifactID != meeting.FinishEvidence.TranscriptArtifactID || !replayedMeeting.FinishEvidence.PersistedAt.Equal(meeting.FinishEvidence.PersistedAt) || len(replayedMeeting.Reports) != len(meeting.Reports) || len(replayedMeeting.Answers) != len(meeting.Answers) {
		t.Fatalf("completed occurrence historical receipt changed after restart: found=%t status=%s finish=%+v err=%v", replayFound, replayedMeeting.Status, replayedMeeting.FinishEvidence, err)
	}
	for i := range meeting.Reports {
		before, after := meeting.Reports[i].Utterance, replayedMeeting.Reports[i].Utterance
		if before.MessageID != after.MessageID || before.Body != after.Body || before.SourceTaskRef == nil || after.SourceTaskRef == nil || *before.SourceTaskRef != *after.SourceTaskRef || before.SourceRunRef == nil || after.SourceRunRef == nil || *before.SourceRunRef != *after.SourceRunRef {
			t.Fatalf("report %d historical identity/body changed after restart", i)
		}
	}
	for i := range meeting.Answers {
		before, after := meeting.Answers[i].Utterance, replayedMeeting.Answers[i].Utterance
		if before.MessageID != after.MessageID || before.Body != after.Body || before.SourceTaskRef == nil || after.SourceTaskRef == nil || *before.SourceTaskRef != *after.SourceTaskRef || before.SourceRunRef == nil || after.SourceRunRef == nil || *before.SourceRunRef != *after.SourceRunRef {
			t.Fatalf("answer %d historical identity/body changed after restart", i)
		}
	}
	replayedRoot, replayedRootRun, _, err := manager.FindTaskMeetingRootByOccurrence(ctx, resolved.Key.String(), resolved.Declaration.CoordinatorActorID, func(context.Context) error { return nil }, executor.authorizeTaskState(resolved))
	if err != nil || replayedRoot.TaskID != rootTask.TaskID || replayedRootRun.RunID != rootRun.RunID || replayedRoot.Status != domaintask.StatusSucceeded || replayedRootRun.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("root native identity/status changed after restart: task=%+v run=%+v err=%v", replayedRoot, replayedRootRun, err)
	}

	// A scheduler delivery after the bounded meeting window still produces a
	// real missed occurrence and canonical partial/minutes artifact before
	// closing its root and eight recipient-context Tasks.
	missedDueAt := due.ScheduledAt.AddDate(0, 0, 1)
	missedSchedule := schedule
	missedSchedule.NextRunAt = missedDueAt
	missedOccurrenceKey, err := domainscheduler.OccurrenceKey(missedSchedule, missedDueAt, domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatalf("derive second occurrence identity: %v", err)
	}
	missedOccurrence := domainscheduler.ScheduledOccurrence{OccurrenceKey: missedOccurrenceKey, ScheduledAt: missedDueAt, Timing: *missedSchedule.Timing}
	missedResolved, err := resolveRuntimeTaskMeetingOccurrence(ctx, cfg, deps, missedSchedule, missedOccurrence, actors, coders...)
	if err != nil {
		t.Fatalf("pin missed occurrence: %v", err)
	}
	// The exact pin already froze the WORLD credential reference. A missing
	// credential cannot block deadline/minutes settlement for this occurrence.
	t.Setenv("WORLD_READ_TOKEN", "")
	executor.worldSnapshotReader = nil
	now = missedDueAt.Add(time.Hour + time.Second)
	if result, err := executor.ExecuteScheduledOccurrence(ctx, missedSchedule, missedOccurrence); err != nil || result != taskMeetingSchedulerSafeSummary {
		t.Fatalf("execute missed occurrence through minutes/control closure: result=%q err=%v", result, err)
	}
	missedOwnerCtx, err := executor.withOwnerScope(ctx, missedResolved.Declaration.CoordinatorActorID, missedResolved.Key.String()+"|verify")
	if err != nil {
		t.Fatalf("bind missed occurrence verification scope: %v", err)
	}
	missedAccess, err := (runtimeTaskMeetingAuthority{snapshot: missedResolved}).AuthorizeOccurrence(missedOwnerCtx, missedResolved.Key, missedResolved.Config)
	if err != nil {
		t.Fatalf("authorize missed occurrence read: %v", err)
	}
	missedMeeting, found, err := workstreamStore.FindMeetingOccurrence(missedOwnerCtx, missedAccess, missedResolved.Key)
	if err != nil || !found || missedMeeting.Status != domainmeeting.StatusMissed || missedMeeting.PartialEvidence == nil || missedMeeting.PartialEvidence.ReasonCode != "missed" || missedMeeting.PartialMinutes == nil || missedMeeting.PartialMinutes.Reference.ArtifactID.Validate() != nil {
		t.Fatalf("missed occurrence lacks canonical partial minutes proof: found=%t status=%s partial=%+v minutes=%+v err=%v", found, missedMeeting.Status, missedMeeting.PartialEvidence, missedMeeting.PartialMinutes, err)
	}
	missedRoot, missedRootRun, missedRootProof, err := manager.FindTaskMeetingRootByOccurrence(ctx, missedResolved.Key.String(), missedResolved.Declaration.CoordinatorActorID, func(context.Context) error { return nil }, executor.authorizeTaskState(missedResolved))
	if err != nil || missedRoot.Status != domaintask.StatusFailed || missedRootRun.Status != domaintask.RunStatusFailed {
		t.Fatalf("missed occurrence root was not closed as failed: task=%s run=%s err=%v", missedRoot.Status, missedRootRun.Status, err)
	}
	for _, actorID := range missedResolved.Declaration.Audience {
		contextTask, contextRun, _, contextErr := manager.FindTaskMeetingRecipientContextByActor(ctx, missedRootProof, actorID, executor.authorizeTaskState(missedResolved), executor.authorizeTaskState(missedResolved), func(context.Context) error { return nil })
		if contextErr != nil || contextTask.Status != domaintask.StatusFailed || contextRun.Status != domaintask.RunStatusFailed {
			t.Fatalf("missed occurrence context %s was not closed as failed: task=%s run=%s err=%v", actorID, contextTask.Status, contextRun.Status, contextErr)
		}
	}
	if provider.calls != domaintask.TaskMeetingAudienceSize+1 {
		t.Fatalf("missed occurrence unexpectedly repeated LLM work: calls=%d", provider.calls)
	}
	t.Setenv("WORLD_READ_TOKEN", "fixture-world-read-credential")
	executor.worldSnapshotReader = runtimeTaskMeetingFakeWorld{declaration: missedResolved.Declaration}
	setRuntimeTaskMeetingFakeGather(executor, missedResolved.Declaration)

	unknownDueAt := due.ScheduledAt.AddDate(0, 0, 2)
	unknownSchedule := schedule
	unknownSchedule.NextRunAt = unknownDueAt
	unknownOccurrenceKey, err := domainscheduler.OccurrenceKey(unknownSchedule, unknownDueAt, domainmeeting.TimezoneJST)
	if err != nil {
		t.Fatalf("derive unknown occurrence identity: %v", err)
	}
	unknownOccurrence := domainscheduler.ScheduledOccurrence{OccurrenceKey: unknownOccurrenceKey, ScheduledAt: unknownDueAt, Timing: *unknownSchedule.Timing}
	unknownResolved, err := resolveRuntimeTaskMeetingOccurrence(ctx, cfg, deps, unknownSchedule, unknownOccurrence, actors, coders...)
	if err != nil {
		t.Fatalf("pin unknown occurrence: %v", err)
	}
	now = unknownDueAt.Add(2 * time.Minute)
	provider.failNext = true
	if _, err := executor.ExecuteScheduledOccurrence(ctx, unknownSchedule, unknownOccurrence); !errors.Is(err, domaintask.ErrTaskMeetingExecutionUnknown) {
		t.Fatalf("ended external report error did not produce a durable unknown receipt: %v", err)
	}
	callsWithUnknown := provider.calls
	now = unknownDueAt.Add(time.Hour + time.Second)
	if result, err := executor.ExecuteScheduledOccurrence(ctx, unknownSchedule, unknownOccurrence); err != nil || result != taskMeetingSchedulerSafeSummary {
		t.Fatalf("reconcile unknown occurrence through partial minutes/control closure: result=%q err=%v", result, err)
	}
	unknownOwnerCtx, err := executor.withOwnerScope(ctx, unknownResolved.Declaration.CoordinatorActorID, unknownResolved.Key.String()+"|verify")
	if err != nil {
		t.Fatalf("bind unknown occurrence verification scope: %v", err)
	}
	unknownAccess, err := (runtimeTaskMeetingAuthority{snapshot: unknownResolved}).AuthorizeOccurrence(unknownOwnerCtx, unknownResolved.Key, unknownResolved.Config)
	if err != nil {
		t.Fatalf("authorize unknown occurrence read: %v", err)
	}
	unknownMeeting, found, err := workstreamStore.FindMeetingOccurrence(unknownOwnerCtx, unknownAccess, unknownResolved.Key)
	if err != nil || !found || unknownMeeting.Status != domainmeeting.StatusPartial || unknownMeeting.PartialEvidence == nil || unknownMeeting.PartialEvidence.InterruptedTurn == nil || unknownMeeting.PartialMinutes == nil {
		t.Fatalf("unknown occurrence did not retain its interrupted turn and real minutes: found=%t status=%s partial=%+v minutes=%+v err=%v", found, unknownMeeting.Status, unknownMeeting.PartialEvidence, unknownMeeting.PartialMinutes, err)
	}
	interrupted := unknownMeeting.PartialEvidence.InterruptedTurn
	unknownOperationAuth := func(authCtx context.Context) error {
		if err := authCtx.Err(); err != nil {
			return err
		}
		if deps.taskCenterUserID != unknownResolved.Declaration.OwnerUserID {
			return eventstore.ErrTaskMeetingForbidden
		}
		return nil
	}
	unknownTask, unknownRun, err := manager.FindTaskMeetingGenerationInvocation(ctx, unknownResolved.Key.String(), domaintask.TaskMeetingPurposeReport, interrupted.ActorID, modulecore.RequestID(interrupted.GenerationRequestID), unknownOperationAuth, executor.authorizeTaskState(unknownResolved), executor.authorizeTaskState(unknownResolved))
	if err != nil || unknownTask.Status != domaintask.StatusFailed || unknownRun.Status != domaintask.RunStatusFailed {
		t.Fatalf("unknown child Task/Run was not preserved as terminal failed: task=%s run=%s err=%v", unknownTask.Status, unknownRun.Status, err)
	}
	if provider.calls != callsWithUnknown || len(provider.lineage) != callsWithUnknown {
		t.Fatalf("unknown receipt reconciliation repeated provider effect: calls=%d lineage=%d before=%d", provider.calls, len(provider.lineage), callsWithUnknown)
	}
	unknownCall := provider.lineage[len(provider.lineage)-1]
	if unknownTask.TaskMeetingInvocation == nil || unknownTask.TaskMeetingInvocation.ActorID != interrupted.ActorID || unknownTask.Assignee != interrupted.ActorID || unknownTask.TaskMeetingInvocation.GenerationRequestID == unknownCall.RequestID {
		t.Fatalf("unknown provider did not stay bound to its actual actor/generation identity: task=%+v call=%+v", unknownTask.TaskMeetingInvocation, unknownCall)
	}
	unknownReceipt, err := manager.GetTaskMeetingGenerationUnknown(ctx, unknownTask.TaskID, unknownRun.RunID, executor.authorizeTaskState(unknownResolved))
	if err != nil || unknownReceipt.TaskID != unknownTask.TaskID || unknownReceipt.RunID != unknownRun.RunID || unknownReceipt.GenerationRequestID != modulecore.RequestID(interrupted.GenerationRequestID) {
		t.Fatalf("exact unknown receipt not retained on interrupted turn: receipt=%+v err=%v", unknownReceipt, err)
	}
	unknownRoot, unknownRootRun, unknownRootProof, err := manager.FindTaskMeetingRootByOccurrence(ctx, unknownResolved.Key.String(), unknownResolved.Declaration.CoordinatorActorID, func(context.Context) error { return nil }, executor.authorizeTaskState(unknownResolved))
	if err != nil || unknownRoot.Status != domaintask.StatusFailed || unknownRootRun.Status != domaintask.RunStatusFailed {
		t.Fatalf("unknown occurrence root was not closed as failed: task=%s run=%s err=%v", unknownRoot.Status, unknownRootRun.Status, err)
	}
	for _, actorID := range unknownResolved.Declaration.Audience {
		contextTask, contextRun, _, contextErr := manager.FindTaskMeetingRecipientContextByActor(ctx, unknownRootProof, actorID, executor.authorizeTaskState(unknownResolved), executor.authorizeTaskState(unknownResolved), func(context.Context) error { return nil })
		if contextErr != nil || contextTask.Status != domaintask.StatusFailed || contextRun.Status != domaintask.RunStatusFailed {
			t.Fatalf("unknown occurrence context %s was not closed as failed: task=%s run=%s err=%v", actorID, contextTask.Status, contextRun.Status, contextErr)
		}
	}
	unknownAction, err := actionOwner.manager.GetAction(ctx, unknownCall.ActionID)
	if err != nil || unknownAction.TaskID != unknownTask.TaskID || unknownAction.RunID != unknownRun.RunID || unknownAction.Status != domainaction.StatusFailed {
		t.Fatalf("unknown provider action did not retain exact failed generation lineage: action=%+v err=%v", unknownAction, err)
	}
	unknownAttempts, err := actionOwner.manager.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: unknownCall.ActionID})
	if err != nil || len(unknownAttempts) != 1 || unknownAttempts[0].AttemptID != unknownCall.AttemptID || unknownAttempts[0].Status != domainaction.AttemptStatusFailed {
		t.Fatalf("unknown provider Action/Attempt mismatch: call=%+v attempts=%+v err=%v", unknownCall, unknownAttempts, err)
	}
	unknownTransport, err := transportManager.ListRequests(ctx, domaintransport.RequestFilter{ActionID: unknownCall.ActionID, AttemptID: unknownCall.AttemptID})
	if err != nil || len(unknownTransport) != 1 || unknownTransport[0].RequestID != unknownCall.RequestID || unknownTransport[0].TaskID != unknownTask.TaskID || unknownTransport[0].RunID != unknownRun.RunID || unknownTransport[0].Status != domaintransport.RequestStatusFailed {
		t.Fatalf("unknown provider Transport receipt mismatch: call=%+v requests=%+v err=%v", unknownCall, unknownTransport, err)
	}
	unknownResponses, err := transportManager.ListResponses(ctx, domaintransport.ResponseFilter{RequestID: unknownCall.RequestID})
	if err != nil || len(unknownResponses) != 0 {
		t.Fatalf("unknown provider outcome unexpectedly has a response receipt: %+v err=%v", unknownResponses, err)
	}
	queueStatus := queue.Status()
	if queueStatus.Failure != nil || queueStatus.Pending != 0 || queueStatus.Reserved != 0 || queueStatus.InFlight {
		t.Fatalf("shared runtime persistence queue did not drain cleanly: %+v", queueStatus)
	}
}

func assertRuntimeTaskMeetingActionTransportLineage(
	t *testing.T,
	ctx context.Context,
	taskReader domaintask.TaskMeetingInvocationReader,
	authorize domaintask.TaskMeetingReadAuthorizationCheck,
	actions *actionmanager.Manager,
	transports *transportmanager.Manager,
	root domaintask.Task,
	meeting domainmeeting.Meeting,
	calls []runtimeTaskMeetingProviderLineage,
) {
	t.Helper()
	expected := map[string]string{}
	for _, report := range meeting.Reports {
		if report.Utterance.SourceTaskRef == nil || report.Utterance.SourceRunRef == nil {
			t.Fatal("report is missing its native generation identity")
		}
		expected[string(*report.Utterance.SourceTaskRef)+"/"+string(*report.Utterance.SourceRunRef)] = report.Utterance.ActorID
	}
	for _, answer := range meeting.Answers {
		if answer.Utterance.SourceTaskRef == nil || answer.Utterance.SourceRunRef == nil {
			t.Fatal("answer is missing its native generation identity")
		}
		expected[string(*answer.Utterance.SourceTaskRef)+"/"+string(*answer.Utterance.SourceRunRef)] = answer.Utterance.ActorID
	}
	if len(calls) != len(expected) || len(calls) != domaintask.TaskMeetingAudienceSize+1 {
		t.Fatalf("provider lineage calls=%d unique native generation runs=%d", len(calls), len(expected))
	}
	for _, call := range calls {
		key := string(call.TaskID) + "/" + string(call.RunID)
		actorID, ok := expected[key]
		if !ok || call.TaskID.Validate() != nil || call.RunID.Validate() != nil || call.ActionID.Validate() != nil || call.AttemptID.Validate() != nil || call.RequestID.Validate() != nil {
			t.Fatalf("provider invocation was not bound to a real generation/Action/Transport tuple: %+v", call)
		}
		if call.TaskID == root.TaskID {
			t.Fatalf("provider call used non-LLM root control Task: %+v", call)
		}
		task, run, _, err := taskReader.GetTaskMeetingInvocationState(ctx, call.TaskID, call.RunID, authorize)
		if err != nil || task.TaskID != call.TaskID || run.RunID != call.RunID || task.TaskMeetingInvocation == nil || task.TaskMeetingInvocation.ActorID != actorID || task.Assignee != actorID || task.TaskMeetingInvocation.GenerationRequestID == call.RequestID || task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
			t.Fatalf("provider invocation did not use its actual succeeded TaskMeeting generation Task/Run: call=%+v task=%+v run=%+v err=%v", call, task, run, err)
		}
		action, err := actions.GetAction(ctx, call.ActionID)
		if err != nil || action.TaskID != call.TaskID || action.RunID != call.RunID || action.Kind != domainaction.KindLLM || action.Status != domainaction.StatusSucceeded {
			t.Fatalf("Action lineage mismatch call=%+v action=%+v err=%v", call, action, err)
		}
		attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: call.ActionID})
		if err != nil || len(attempts) != 1 || attempts[0].AttemptID != call.AttemptID || attempts[0].Status != domainaction.AttemptStatusSucceeded {
			t.Fatalf("Action attempt mismatch call=%+v attempts=%+v err=%v", call, attempts, err)
		}
		requests, err := transports.ListRequests(ctx, domaintransport.RequestFilter{ActionID: call.ActionID, AttemptID: call.AttemptID})
		if err != nil || len(requests) != 1 || requests[0].RequestID != call.RequestID || requests[0].TaskID != call.TaskID || requests[0].RunID != call.RunID || requests[0].ActionID != call.ActionID || requests[0].AttemptID != call.AttemptID || requests[0].Status != domaintransport.RequestStatusResponded {
			t.Fatalf("Transport request lineage mismatch call=%+v requests=%+v err=%v", call, requests, err)
		}
		responses, err := transports.ListResponses(ctx, domaintransport.ResponseFilter{RequestID: call.RequestID})
		if err != nil || len(responses) != 1 || responses[0].TaskID != call.TaskID || responses[0].RunID != call.RunID || responses[0].ActionID != call.ActionID || responses[0].AttemptID != call.AttemptID {
			t.Fatalf("Transport response lineage mismatch call=%+v responses=%+v err=%v", call, responses, err)
		}
	}
}

type runtimeTaskMeetingFakeProvider struct {
	calls          int
	reportCalls    int
	answerCalls    int
	failNext       bool
	statusActor    string
	statusTaskRef  modulecore.TaskID
	foreignTaskRef modulecore.TaskID
	privateTaskRef modulecore.TaskID
	lineage        []runtimeTaskMeetingProviderLineage
}

type runtimeTaskMeetingProviderLineage struct {
	TaskID    modulecore.TaskID
	RunID     modulecore.RunID
	ActionID  modulecore.ActionID
	AttemptID modulecore.AttemptID
	RequestID modulecore.RequestID
}

func (p *runtimeTaskMeetingFakeProvider) Name() string { return "taskmeeting-runtime-fixture" }
func (p *runtimeTaskMeetingFakeProvider) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	if request.RequestAdmission == nil {
		return llm.GenerateResponse{}, errors.New("native LLM admission is absent")
	}
	if err := request.RequestAdmission.Admit(); err != nil {
		return llm.GenerateResponse{}, err
	}
	identity, err := execution.IdentityFromContext(ctx)
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	actionID, attemptID, bound := execution.BoundActionAttemptFromContext(ctx)
	if !bound || actionID.Validate() != nil || attemptID.Validate() != nil {
		return llm.GenerateResponse{}, errors.New("native Action/Attempt lineage is absent")
	}
	receiptOwner, ok := domaintransport.ReceiptOwnerFromContext(ctx)
	if !ok {
		return llm.GenerateResponse{}, errors.New("native Transport receipt owner is absent")
	}
	transportRequest, err := receiptOwner.BeginRequest(ctx, "task_meeting.generate")
	if err != nil {
		return llm.GenerateResponse{}, err
	}
	lineage := runtimeTaskMeetingProviderLineage{TaskID: identity.TaskID, RunID: identity.RunID, ActionID: actionID, AttemptID: attemptID, RequestID: transportRequest.RequestID}
	p.calls++
	if p.failNext {
		p.failNext = false
		p.lineage = append(p.lineage, lineage)
		callErr := errors.New("fixture provider returned an uncertain outcome")
		if err := receiptOwner.FailWithoutResponse(ctx, transportRequest.RequestID, "fixture provider call failed"); err != nil {
			return llm.GenerateResponse{}, errors.Join(callErr, err)
		}
		return llm.GenerateResponse{}, callErr
	}
	if _, err := receiptOwner.CompleteResponse(ctx, transportRequest.RequestID, "fixture-llm-response"); err != nil {
		return llm.GenerateResponse{}, err
	}
	p.lineage = append(p.lineage, lineage)
	if strings.Contains(request.SystemPrompt, "Report schema:") {
		p.reportCalls++
		if len(request.Messages) == 0 {
			return llm.GenerateResponse{}, errors.New("meeting report prompt has no frozen input")
		}
		var prompt struct {
			ActorID           string                                   `json:"actor_id"`
			ShareableStatuses []domainagent.TaskMeetingShareableStatus `json:"shareable_statuses"`
		}
		if err := json.Unmarshal([]byte(request.Messages[0].Content), &prompt); err != nil {
			return llm.GenerateResponse{}, errors.New("meeting report prompt is not valid frozen JSON")
		}
		if !strings.Contains(request.SystemPrompt, "partial view of public tasks") || !strings.Contains(request.SystemPrompt, "Never infer that no work exists") {
			return llm.GenerateResponse{}, errors.New("meeting report prompt does not explain the partial public status view")
		}
		if prompt.ActorID == p.statusActor {
			if len(prompt.ShareableStatuses) != 1 || prompt.ShareableStatuses[0].ActorID != p.statusActor || prompt.ShareableStatuses[0].TaskRef != p.statusTaskRef || prompt.ShareableStatuses[0].Status != domaintask.StatusRunning {
				return llm.GenerateResponse{}, errors.New("self report prompt omitted or widened the public status projection")
			}
		} else if len(prompt.ShareableStatuses) != 0 {
			return llm.GenerateResponse{}, errors.New("report prompt included another actor's task status")
		}
		for _, forbidden := range []modulecore.TaskID{p.foreignTaskRef, p.privateTaskRef} {
			for _, status := range prompt.ShareableStatuses {
				if status.TaskRef == forbidden {
					return llm.GenerateResponse{}, errors.New("report prompt included a foreign or private task reference")
				}
			}
		}
		for _, forbidden := range []string{"fixture-public-task-title-must-not-leak", "fixture-foreign-task-title-must-not-leak", "fixture-private-task-title-must-not-leak"} {
			if strings.Contains(request.Messages[0].Content, forbidden) {
				return llm.GenerateResponse{}, errors.New("report prompt included a task title")
			}
		}
		if prompt.ActorID == p.statusActor {
			return llm.GenerateResponse{Content: `{"body":"native owner report","current":{"state":"in_progress","text":"Working on a public task.","task_ref":"` + string(p.statusTaskRef) + `"},"next":{"state":"undecided"},"concern_declaration":"listed","concerns":[{"description":"Need review timeline before release."}]}`}, nil
		}
		return llm.GenerateResponse{Content: `{"body":"native owner report","current":{"state":"undecided"},"next":{"state":"undecided"},"concern_declaration":"none","concerns":[]}`}, nil
	}
	p.answerCalls++
	return llm.GenerateResponse{Content: `{"body":"The timeline should be reviewed with the owner before release.","kind":"concrete_support"}`}, nil
}

type runtimeTaskMeetingFakeWorld struct {
	declaration domainmeeting.TaskMeetingDeclaration
}

type runtimeTaskMeetingFakeGatherClient struct{ world runtimeTaskMeetingFakeWorld }

func (c runtimeTaskMeetingFakeGatherClient) ReadScope() worldread.ReadScope {
	return c.world.ReadScope()
}
func (c runtimeTaskMeetingFakeGatherClient) Snapshot(ctx context.Context) (worldread.Snapshot, error) {
	return c.world.Snapshot(ctx)
}
func (runtimeTaskMeetingFakeGatherClient) Events(context.Context, uint64) (worldread.EventBatch, error) {
	return worldread.EventBatch{}, nil
}
func (runtimeTaskMeetingFakeGatherClient) Command(context.Context, string, string, worldread.HomeActionPayload) (worldread.CommandResult, error) {
	return worldread.CommandResult{}, errors.New("unexpected WORLD life command in all actors already gathered fixture")
}

func setRuntimeTaskMeetingFakeGather(executor *runtimeTaskMeetingOccurrenceExecutor, declaration domainmeeting.TaskMeetingDeclaration) {
	executor.worldGatherClientFactory = func() (worldmeeting.TaskMeetingWorldGatherClient, error) {
		return runtimeTaskMeetingFakeGatherClient{world: runtimeTaskMeetingFakeWorld{declaration: declaration}}, nil
	}
}

func (w runtimeTaskMeetingFakeWorld) ReadScope() worldread.ReadScope {
	ids := make([]string, 0, len(w.declaration.WorldActorBindings))
	for _, binding := range w.declaration.WorldActorBindings {
		ids = append(ids, binding.WorldActorID)
	}
	sort.Strings(ids)
	return worldread.ReadScope{BaseURL: w.declaration.WorldBaseURL, WorldID: w.declaration.WorldID, TimelineID: w.declaration.TimelineID, TimelineEpoch: w.declaration.TimelineEpoch, ActorIDs: ids}
}

func (w runtimeTaskMeetingFakeWorld) Snapshot(context.Context) (worldread.Snapshot, error) {
	actors := make([]worldread.Actor, 0, len(w.declaration.WorldActorBindings))
	for _, binding := range w.declaration.WorldActorBindings {
		actors = append(actors, worldread.Actor{ActorID: binding.WorldActorID, Present: true, RoomID: w.declaration.Rooms.Living})
	}
	state := sha256.Sum256([]byte("stable local test snapshot"))
	return worldread.Snapshot{SchemaVersion: 1, WorldID: w.declaration.WorldID, TimelineID: w.declaration.TimelineID, TimelineEpoch: w.declaration.TimelineEpoch, StateSeq: 1, StateHash: hex.EncodeToString(state[:]), Actors: actors}, nil
}

var _ llm.LLMProvider = (*runtimeTaskMeetingFakeProvider)(nil)
var _ worldmeeting.TaskMeetingWorldSnapshotReader = runtimeTaskMeetingFakeWorld{}
