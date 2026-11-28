package taskmanager

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestStartResearchContinuationKeepsAuthenticatedOwnerAndTaskActor(t *testing.T) {
	for _, tc := range []struct {
		name   string
		origin bool
		actor  string
	}{
		{name: "local research binding", actor: "ren"},
		{name: "private origin mio task", origin: true, actor: "mio"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store, err := taskpersistence.NewJSONLStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = store.Close() })
			manager := New(store, DefaultParallelLimits())
			taskID := modulecore.NewTaskID()
			draft := domaintask.Task{TaskID: taskID, Title: "Research request", Route: domaintask.RouteResearch, OwnerID: "ren", Assignee: tc.actor}
			createCtx := ctx
			if tc.origin {
				draft.Route = domaintask.RouteCHAT
				draft.OwnerID = "mio"
				draft.OriginSessionID = modulecore.NewSessionID()
				address, err := conversation.NewChannelAddress("viewer", "cmd:ren")
				if err != nil {
					t.Fatal(err)
				}
				binding, err := conversation.NewSessionBindingWithPublicationIncarnation(
					draft.OriginSessionID, address, conversation.SessionAudiencePrivate, "ren", 1, "0123456789abcdef0123456789abcdef",
				)
				if err != nil {
					t.Fatal(err)
				}
				createCtx = conversation.WithSessionBinding(ctx, binding)
			} else {
				draft.ResearchBinding = &domaintask.ResearchExecutionBinding{
					SchemaVersion: domaintask.ResearchExecutionBindingSchema, DataScope: "user", OwnerID: "ren", OwnerRoute: "core.research.local",
				}
			}
			created, prior, err := manager.CreateAndStartRun(createCtx, draft, domaintask.SharedRoleContext{TaskID: taskID})
			if err != nil {
				t.Fatal(err)
			}
			projection := researchProjection(created, prior, "waiting_input")
			projection.DataScope, projection.OwnerID = "user", "ren"
			if err := manager.RecordResearchResult(ctx, taskID, prior.RunID, tc.actor, projection, func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, nil); err != nil {
				t.Fatalf("record source receipt: %v", err)
			}
			if _, err := manager.CompleteRun(ctx, taskID, prior.RunID, tc.actor, domaintask.StatusWaiting, "manual seed required", "manual seed required"); err != nil {
				t.Fatal(err)
			}
			next, err := manager.StartResearchContinuation(ctx, taskID, prior.RunID, "ren", tc.actor)
			if err != nil {
				t.Fatalf("start authorized continuation: %v", err)
			}
			if next.RunID == prior.RunID || next.Assignee != tc.actor || next.StartReason != domaintask.RunStartReasonCheckpointResume {
				t.Fatalf("continuation Run=%+v prior=%+v", next, prior)
			}
			stored, err := manager.Get(ctx, taskID)
			if err != nil || stored.OwnerID != draft.OwnerID || stored.Assignee != tc.actor || stored.ResearchResult == nil || stored.ResearchResult.OwnerID != "ren" {
				t.Fatalf("Task identity/scope changed: task=%+v err=%v", stored, err)
			}
		})
	}
}

func TestStartResearchContinuationRejectsWrongOwnerAndStalePriorRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	taskID := modulecore.NewTaskID()
	task, prior, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		TaskID: taskID, Title: "Research request", Route: domaintask.RouteResearch, OwnerID: "ren", Assignee: "ren",
		ResearchBinding: &domaintask.ResearchExecutionBinding{SchemaVersion: domaintask.ResearchExecutionBindingSchema, DataScope: "user", OwnerID: "ren", OwnerRoute: "core.research.local"},
	}, domaintask.SharedRoleContext{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	projection := researchProjection(task, prior, "waiting_input")
	projection.DataScope, projection.OwnerID = "user", "ren"
	if err := manager.RecordResearchResult(ctx, taskID, prior.RunID, "ren", projection, func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, taskID, prior.RunID, "ren", domaintask.StatusWaiting, "manual seed required", "manual seed required"); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartResearchContinuation(ctx, taskID, prior.RunID, "another-owner", "ren"); err == nil {
		t.Fatal("different authenticated owner was admitted")
	}
	newer, err := manager.StartRunWithReason(ctx, taskID, domaintask.RunStartReasonCheckpointResume)
	if err != nil {
		t.Fatal(err)
	}
	cause := errors.New("injected resume failure")
	if err := manager.CloseStartedRun(ctx, taskID, newer.RunID, "ren", RunOutcome{Status: domaintask.StatusFailed, Summary: "resume attempt failed"}, cause); err != nil && !errors.Is(err, cause) {
		t.Fatal(err)
	}
	before, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartResearchContinuation(ctx, taskID, prior.RunID, "ren", "ren"); !errors.Is(err, ErrResearchContinuationConflict) {
		t.Fatalf("stale Engine source Run authorized a new Task Run: %v", err)
	}
	after, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(before) != len(after) {
		t.Fatalf("recovery conflict created another Run: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestStartResearchContinuationRejectsPublicScopeBeforeIssuingRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	taskID := modulecore.NewTaskID()
	task, prior, err := manager.CreateAndStartRun(ctx, domaintask.Task{TaskID: taskID, Title: "Public Research", Route: domaintask.RouteCHAT, Assignee: "mio"}, domaintask.SharedRoleContext{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	projection := researchProjection(task, prior, "waiting_input")
	if err := manager.RecordResearchResult(ctx, taskID, prior.RunID, "mio", projection, func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, taskID, prior.RunID, "mio", domaintask.StatusWaiting, "manual seed required", "manual seed required"); err != nil {
		t.Fatal(err)
	}
	before, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartResearchContinuation(ctx, taskID, prior.RunID, "ren", "mio"); !errors.Is(err, ErrResearchContinuationConflict) {
		t.Fatalf("public Task was accepted for authenticated owner continuation: %v", err)
	}
	after, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: taskID})
	if err != nil || len(before) != len(after) {
		t.Fatalf("public scope denial issued a Run: before=%d after=%d err=%v", len(before), len(after), err)
	}
}

func TestStartResearchContinuationRejectsTypedOwnerWithoutIssuingRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	timing := modulecore.TaskTiming{AllowedWindows: []modulecore.TaskTimeWindow{}}
	timingDigest, err := domaintask.TaskTimingSHA256(timing)
	if err != nil {
		t.Fatal(err)
	}
	kindInput := json.RawMessage(`{"information":"","kind":"movie","limit":1,"name":"The Film"}`)
	inputDigest, err := domaintask.TaskCenterKindInputSHA256(kindInput)
	if err != nil {
		t.Fatal(err)
	}
	goalID, workstreamID := modulecore.NewGoalID(), modulecore.NewWorkstreamID()
	draft := domaintask.Task{
		Title: "typed private task", Route: domaintask.RouteGeneral, Assignee: "mio", Priority: domaintask.PriorityNormal,
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, ReadOnly: true, GoalID: goalID, WorkstreamID: workstreamID,
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "ren"}, Timing: &timing,
		TaskCenterExecution: &domaintask.TaskCenterExecution{
			KindID: TaskCenterMovieCatalogLookupKind, KindInputVersion: 1, KindInputSHA256: inputDigest,
			SourceGoalID: goalID, PreparedRevision: 1, AssignmentEpoch: 0, TimingSHA256: timingDigest,
		},
	}
	typed, run, err := manager.EnsureTaskCenterExecution(
		ctx, modulecore.NewRequestID(), 1, draft, domaintask.SharedRoleContext{}, kindInput,
		TaskCenterExecutionAdmissionCheck(func(context.Context, domaintask.Task, domaintask.SharedRoleContext, json.RawMessage) error {
			return nil
		}),
		domaintask.TaskCenterExecutionReceiptReadAuthorizationCheck(func(context.Context, domaintask.TaskCenterExecutionReceiptReadInput) error { return nil }),
	)
	if err != nil {
		t.Fatal(err)
	}
	stateReader, ok := any(store).(domaintask.TaskCenterExecutionStateReader)
	if !ok {
		t.Fatal("TaskCenter owner state reader is unavailable")
	}
	authorize := domaintask.TaskCenterExecutionAuthorizationCheck(func(context.Context, domaintask.Task, domaintask.Run, domaintask.SharedRoleContext) error { return nil })
	beforeTask, beforeRun, err := stateReader.GetTaskCenterExecutionState(ctx, typed.TaskID, run.RunID, authorize)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartResearchContinuation(ctx, typed.TaskID, run.RunID, "ren", "mio"); err == nil {
		t.Fatal("typed Task passed generic Research continuation")
	}
	afterTask, afterRun, err := stateReader.GetTaskCenterExecutionState(ctx, typed.TaskID, run.RunID, authorize)
	if err != nil || beforeTask.Status != afterTask.Status || beforeRun.RunID != afterRun.RunID || beforeRun.Status != afterRun.Status {
		t.Fatalf("typed Task denial changed owner state: before=%+v/%+v after=%+v/%+v err=%v", beforeTask, beforeRun, afterTask, afterRun, err)
	}
}

func TestStartResearchContinuationRejectsPrivateGoalWithoutOwnerAuthorization(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	now := time.Now().UTC()
	task := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "private Goal Research", Route: domaintask.RouteResearch, Assignee: "mio",
		GoalID: modulecore.NewGoalID(), WorkstreamID: modulecore.NewWorkstreamID(),
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "ren"},
		Status:        domaintask.StatusRunning, StartedAt: &now,
	}
	task.ApplyDefaults(now)
	read := domaintask.TaskReadAuthorizationCheck(func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
		if input.AuthenticatedUserID != "ren" {
			t.Fatalf("owner read crossed principals: %+v", input)
		}
		return nil
	})
	write := domaintask.TaskWriteAuthorizationCheck(func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
		if input.AuthenticatedUserID != "ren" {
			t.Fatalf("owner write crossed principals: %+v", input)
		}
		return nil
	})
	if err := store.SaveTaskAuthorized(ctx, task, write); err != nil {
		t.Fatal(err)
	}
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	run := domaintask.Run{WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Assignee: "mio", Status: domaintask.RunStatusRunning, StartedAt: now}
	if err := store.SaveRunAuthorized(ctx, run, write); err != nil {
		t.Fatal(err)
	}
	beforeTask, err := store.GetTaskAuthorized(ctx, task.TaskID, read)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := store.ListRunsAuthorized(ctx, domaintask.RunFilter{TaskID: task.TaskID}, read)
	if err != nil || len(beforeRuns) != 1 {
		t.Fatalf("private Goal initial Run read=%+v err=%v", beforeRuns, err)
	}
	if _, err := manager.StartResearchContinuation(ctx, task.TaskID, run.RunID, "ren", "mio"); err == nil {
		t.Fatal("private Goal continued without its owner authorization callback")
	}
	afterTask, err := store.GetTaskAuthorized(ctx, task.TaskID, read)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := store.ListRunsAuthorized(ctx, domaintask.RunFilter{TaskID: task.TaskID}, read)
	if err != nil || len(afterRuns) != 1 || afterRuns[0].RunID != run.RunID || afterTask.Status != beforeTask.Status {
		t.Fatalf("private Goal denial changed Task/Run state: Task=%+v Runs=%+v err=%v", afterTask, afterRuns, err)
	}
}
