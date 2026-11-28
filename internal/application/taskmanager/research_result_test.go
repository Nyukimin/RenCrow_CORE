package taskmanager

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func researchProjection(task domaintask.Task, run domaintask.Run, status string) domaintask.ResearchResultProjection {
	return domaintask.ResearchResultProjection{
		SchemaVersion: domaintask.ResearchResultSchema, TaskID: task.TaskID, ExecutionRunID: run.RunID,
		ResearchRunID: "research-run-1", ActorID: run.Assignee, DataScope: "public",
		ResultDigest: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		ArtifactRef:  "research-result:run-1", Status: status,
		Counts: domaintask.ResearchResultCounts{BoardPostsCreated: 0}, ReceiptState: "none", RequiredReceiptsComplete: true,
	}
}

func TestRecordResearchResultStoresOnlyVerifiedFactsForActiveRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	task, run, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "research task", Route: domaintask.RouteResearch, Assignee: "mio", Summary: "BOARD登録済み",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("CreateAndStartRun: %v", err)
	}
	projection := researchProjection(task, run, "succeeded")
	projection.Counts.BoardPostsCreated = 0
	verifyCalls := 0
	verify := ResearchResultVerificationCheck(func(_ context.Context, got domaintask.ResearchResultProjection) error {
		verifyCalls++
		if got.ResultDigest != projection.ResultDigest || got.ExecutionRunID != run.RunID {
			t.Fatalf("verification got wrong result binding: %+v", got)
		}
		return nil
	})
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "mio", projection, verify, nil, nil); err != nil {
		t.Fatalf("RecordResearchResult: %v", err)
	}
	stored, err := store.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if verifyCalls != 1 || stored.ResearchResult == nil || stored.ResearchResult.Counts.BoardPostsCreated != 0 || stored.Summary != "BOARD登録済み" {
		t.Fatalf("stored Task facts=%+v summary=%q verifyCalls=%d", stored.ResearchResult, stored.Summary, verifyCalls)
	}
	if err := manager.CloseStartedRun(ctx, task.TaskID, run.RunID, "mio", RunOutcomeOf(nil, "BOARD登録済み"), nil); err != nil {
		t.Fatalf("CloseStartedRun: %v", err)
	}
	completed, err := manager.Get(ctx, task.TaskID)
	if err != nil || completed.ResearchResult == nil || completed.Summary != projection.TaskSummary() || strings.Contains(completed.Summary, "BOARD登録済み") {
		t.Fatalf("completed Task summary=%q err=%v, want deterministic Research facts", completed.Summary, err)
	}
}

func TestCloseLocalResearchTaskSummaryPreservesPartialEngineState(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	owner := "local-research-owner"
	task, run, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "Research request", Route: domaintask.RouteResearch, OwnerID: owner, Assignee: owner,
		ResearchBinding: &domaintask.ResearchExecutionBinding{SchemaVersion: domaintask.ResearchExecutionBindingSchema, DataScope: "user", OwnerID: owner, OwnerRoute: "core.research.local"},
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	projection := researchProjection(task, run, "partial")
	projection.DataScope, projection.OwnerID = "user", owner
	projection.Counts.DocumentsFetched, projection.Counts.DocumentsAnalyzed = 2, 1
	projection.Counts.RecordsSaved, projection.Counts.BoardPostsCreated = 1, 0
	projection.ReceiptState, projection.ReceiptCount = "unavailable", 1
	projection.RequiredReceiptsComplete = false
	projection.Missing = []string{"save:knowledge_staging"}
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, owner, projection, func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, nil); err != nil {
		t.Fatalf("RecordResearchResult for trusted local Research Task: %v", err)
	}
	outcome := RunOutcome{Status: domaintask.StatusFailed, Summary: "Research completed successfully and BOARD registered"}
	if err := manager.CloseStartedRun(ctx, task.TaskID, run.RunID, owner, outcome, nil); err != nil {
		t.Fatalf("CloseStartedRun for local Research Task: %v", err)
	}
	completed, err := manager.Get(ctx, task.TaskID)
	if err != nil || completed.ResearchResult == nil || completed.Status != domaintask.StatusFailed || completed.Summary != projection.TaskSummary() || !strings.Contains(completed.Summary, "Research status=partial") || strings.Contains(completed.Summary, "successfully") || strings.Contains(completed.Summary, "BOARD registered") {
		t.Fatalf("local Research Task completion promoted the narrative: status=%s summary=%q err=%v", completed.Status, completed.Summary, err)
	}
}

func TestRecordResearchResultRejectsUnverifiedAndMismatchedRun(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	task, firstRun, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "research task", Assignee: "mio"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	projection := researchProjection(task, firstRun, "partial")
	verifyErr := errors.New("exact Engine readback mismatch")
	if err := manager.RecordResearchResult(ctx, task.TaskID, firstRun.RunID, "mio", projection, func(context.Context, domaintask.ResearchResultProjection) error { return verifyErr }, nil, nil); !errors.Is(err, verifyErr) {
		t.Fatalf("unverified result error=%v", err)
	}
	if _, err := manager.Succeed(ctx, task.TaskID, "finished"); err != nil {
		t.Fatalf("Succeed first Run: %v", err)
	}
	_, err = manager.Start(ctx, task.TaskID)
	if err != nil {
		t.Fatalf("start next Run: %v", err)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("ListRuns after rerun=%+v err=%v", runs, err)
	}
	if runs[1].RunID == firstRun.RunID {
		t.Fatalf("rerun reused Run ID %s", runs[1].RunID)
	}
	if err := manager.RecordResearchResult(ctx, task.TaskID, firstRun.RunID, "mio", projection, func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, nil); err == nil {
		t.Fatal("stale Run accepted")
	}
	stored, err := store.GetTask(ctx, task.TaskID)
	if err != nil || stored.ResearchResult != nil {
		t.Fatalf("stale/unverified projection persisted: result=%+v err=%v", stored.ResearchResult, err)
	}
}

func TestRecordResearchResultPersistsUnknownEngineStateAndRejectsBindingMismatches(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	task, run, err := manager.CreateAndStartRun(ctx, domaintask.Task{Title: "research task", Assignee: "researcher"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	projection := researchProjection(task, run, "unknown")
	wrongActor := projection
	wrongActor.ActorID = "another-agent"
	verifyCalls := 0
	verify := ResearchResultVerificationCheck(func(context.Context, domaintask.ResearchResultProjection) error {
		verifyCalls++
		return nil
	})
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "researcher", wrongActor, verify, nil, nil); !errors.Is(err, ErrResearchResultConflict) {
		t.Fatalf("mismatched ActorID error=%v", err)
	}
	if verifyCalls != 0 {
		t.Fatalf("Engine verification ran for mismatched ActorID: calls=%d", verifyCalls)
	}
	wrongDigest := projection
	wrongDigest.ResultDigest = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"
	digestErr := errors.New("Engine digest mismatch")
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "researcher", wrongDigest,
		func(_ context.Context, got domaintask.ResearchResultProjection) error {
			if got.ResultDigest != wrongDigest.ResultDigest {
				t.Fatalf("verification got wrong digest: %+v", got)
			}
			return digestErr
		}, nil, nil); !errors.Is(err, digestErr) {
		t.Fatalf("mismatched Engine digest error=%v", err)
	}
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "researcher", projection, verify, nil, nil); err != nil {
		t.Fatalf("RecordResearchResult unknown status: %v", err)
	}
	stored, err := store.GetTask(ctx, task.TaskID)
	if err != nil || stored.ResearchResult == nil || stored.ResearchResult.Status != "unknown" {
		t.Fatalf("unknown Engine state was lost: result=%+v err=%v", stored.ResearchResult, err)
	}
}

func TestRecordResearchResultRequiresExactCurrentWriterGeneration(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	baseManager := New(store, DefaultParallelLimits())
	task, run, err := baseManager.CreateAndStartRun(ctx, domaintask.Task{Title: "research generation fence", Assignee: "researcher"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	current, err := store.WriterGeneration()
	if err != nil || current == 0 || run.WriterGeneration != current {
		t.Fatalf("initial WriterGeneration=%d err=%v Run generation=%d", current, err, run.WriterGeneration)
	}
	zero := uint64(0)
	future := current + 1
	generationErr := errors.New("writer generation unavailable")
	cases := []struct {
		name           string
		current        uint64
		currentErr     error
		runGeneration  *uint64
		wantGeneration bool
	}{
		{name: "stale run", current: future, wantGeneration: true},
		{name: "future run", current: current, runGeneration: &future, wantGeneration: true},
		{name: "zero run generation", current: current, runGeneration: &zero, wantGeneration: true},
		{name: "zero current generation", current: 0, wantGeneration: true},
		{name: "generation read failure", current: current, currentErr: generationErr},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			manager := New(researchGenerationStore{
				Store: store, generation: tc.current, generationErr: tc.currentErr, runGeneration: tc.runGeneration,
			}, DefaultParallelLimits())
			projection := researchProjection(task, run, "succeeded")
			err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "researcher", projection,
				func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, nil)
			switch {
			case tc.currentErr != nil:
				if !errors.Is(err, tc.currentErr) {
					t.Fatalf("RecordResearchResult error=%v, want writer generation error", err)
				}
			case tc.wantGeneration:
				if !errors.Is(err, ErrRunConflict) {
					t.Fatalf("RecordResearchResult error=%v, want ErrRunConflict", err)
				}
			case err != nil:
				t.Fatalf("RecordResearchResult: %v", err)
			}
		})
	}
}

func TestRecordResearchResultStoresLocalUserFactsOnlyWithCreationBinding(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := New(store, DefaultParallelLimits())
	binding := &domaintask.ResearchExecutionBinding{
		SchemaVersion: domaintask.ResearchExecutionBindingSchema, DataScope: "user", OwnerID: "local-1", OwnerRoute: "core.research.local",
	}
	task, run, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		Title: "generic research task", Route: domaintask.RouteResearch, OwnerID: "local-1", Assignee: "local-1", ResearchBinding: binding,
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("CreateAndStartRun with trusted binding: %v", err)
	}
	projection := researchProjection(task, run, "partial")
	projection.DataScope, projection.OwnerID = "user", "local-1"
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "local-1", projection,
		func(_ context.Context, got domaintask.ResearchResultProjection) error {
			if got.DataScope != "user" || got.OwnerID != "local-1" {
				t.Fatalf("Engine verification did not receive user binding: %+v", got)
			}
			return nil
		}, nil, nil); err != nil {
		t.Fatalf("RecordResearchResult: %v task=%+v run=%+v projection=%+v", err, task, run, projection)
	}
	stored, err := store.GetTask(ctx, task.TaskID)
	if err != nil || stored.ResearchResult == nil || stored.ResearchResult.Status != "partial" || stored.ResearchResult.DataScope != "user" {
		t.Fatalf("user-scope facts=%+v err=%v", stored.ResearchResult, err)
	}
}

func TestRecordResearchResultUsesBothPrivateGoalCallbacks(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	now := time.Date(2026, 11, 28, 2, 0, 0, 0, time.UTC)
	task := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "private Research Task", Route: domaintask.RouteResearch, Assignee: "mio",
		GoalID: modulecore.NewGoalID(), WorkstreamID: modulecore.NewWorkstreamID(),
		PrivateAccess: &domaintask.TaskPrivateAccess{Scope: domaintask.TaskAccessScopeUser, AuthenticatedUserID: "goal-user"},
		Status:        domaintask.StatusRunning, StartedAt: &now,
	}
	task.ApplyDefaults(now)
	if err := store.SaveTaskAuthorized(ctx, task, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
		if input.AuthenticatedUserID != "goal-user" || input.Resource != domaintask.TaskWriteResourceTask {
			t.Fatalf("initial private Task authorization input=%+v", input)
		}
		return nil
	}); err != nil {
		t.Fatalf("save private Task: %v", err)
	}
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatalf("WriterGeneration: %v", err)
	}
	run := domaintask.Run{WriterGeneration: generation, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst,
		Assignee: "mio", Status: domaintask.RunStatusRunning, StartedAt: now}
	if err := store.SaveRunAuthorized(ctx, run, func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
		if input.AuthenticatedUserID != "goal-user" || input.Resource != domaintask.TaskWriteResourceRun || input.RunID != run.RunID {
			t.Fatalf("initial private Run authorization input=%+v", input)
		}
		return nil
	}); err != nil {
		t.Fatalf("save private Run: %v", err)
	}
	projection := researchProjection(task, run, "succeeded")
	projection.DataScope, projection.OwnerID = "user", "goal-user"
	manager := New(store, DefaultParallelLimits(), WithClock(func() time.Time { return now.Add(time.Second) }))
	readChecks, writeChecks := 0, 0
	read := domaintask.TaskReadAuthorizationCheck(func(_ context.Context, input domaintask.TaskReadAuthorizationInput) error {
		readChecks++
		if input.TaskID != task.TaskID || input.GoalID != task.GoalID || input.Scope != domaintask.TaskAccessScopeUser || input.AuthenticatedUserID != "goal-user" {
			t.Fatalf("private read authorization input=%+v", input)
		}
		return nil
	})
	write := domaintask.TaskWriteAuthorizationCheck(func(_ context.Context, input domaintask.TaskWriteAuthorizationInput) error {
		writeChecks++
		if input.TaskID != task.TaskID || input.GoalID != task.GoalID || input.Scope != domaintask.TaskAccessScopeUser || input.AuthenticatedUserID != "goal-user" {
			t.Fatalf("private write authorization input=%+v", input)
		}
		return nil
	})
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "mio", projection,
		func(context.Context, domaintask.ResearchResultProjection) error { return nil }, read, write); err != nil {
		t.Fatalf("RecordResearchResult private Goal Task: %v", err)
	}
	if readChecks != 3 || writeChecks != 1 {
		t.Fatalf("private authorization checks read=%d write=%d", readChecks, writeChecks)
	}
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "mio", projection,
		func(context.Context, domaintask.ResearchResultProjection) error { return nil }, nil, write); err == nil {
		t.Fatal("private Goal Task accepted without read authorization")
	}
	if err := manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "mio", projection,
		func(context.Context, domaintask.ResearchResultProjection) error { return nil },
		func(context.Context, domaintask.TaskReadAuthorizationInput) error {
			return domaintask.ErrPrivateTaskAccessForbidden
		}, write); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("revoked private Goal read authorization error=%v, want hidden not-found", err)
	}
}

type ownerManagedResearchStore struct {
	domaintask.Store
	task domaintask.Task
}

type researchGenerationStore struct {
	domaintask.Store
	generation    uint64
	generationErr error
	runGeneration *uint64
}

func (s researchGenerationStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(tx domaintask.Store) error {
		return fn(researchGenerationTx{Store: tx, generation: s.generation, generationErr: s.generationErr, runGeneration: s.runGeneration})
	})
}

type researchGenerationTx struct {
	domaintask.Store
	generation    uint64
	generationErr error
	runGeneration *uint64
}

func (s researchGenerationTx) WriterGeneration() (uint64, error) {
	return s.generation, s.generationErr
}

func (s researchGenerationTx) ListRuns(ctx context.Context, filter domaintask.RunFilter) ([]domaintask.Run, error) {
	runs, err := s.Store.ListRuns(ctx, filter)
	if err != nil || s.runGeneration == nil {
		return runs, err
	}
	for index := range runs {
		runs[index].WriterGeneration = *s.runGeneration
	}
	return runs, nil
}

func (s ownerManagedResearchStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(tx domaintask.Store) error {
		return fn(ownerManagedResearchTx{Store: tx, task: s.task})
	})
}

type ownerManagedResearchTx struct {
	domaintask.Store
	task domaintask.Task
}

func (s ownerManagedResearchTx) GetTask(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	if taskID == s.task.TaskID {
		return s.task, nil
	}
	return s.Store.GetTask(context.Background(), taskID)
}

func TestRecordResearchResultLeavesOwnerManagedTaskToItsTypedOwner(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	baseManager := New(store, DefaultParallelLimits())
	task, run, err := baseManager.CreateAndStartRun(ctx, domaintask.Task{Title: "typed Task", Assignee: "researcher"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	task.NativePlanningInvocation = &domaintask.NativePlanningInvocation{}
	manager := New(ownerManagedResearchStore{Store: store, task: task}, DefaultParallelLimits())
	projection := researchProjection(task, run, "succeeded")
	verifyCalls := 0
	err = manager.RecordResearchResult(ctx, task.TaskID, run.RunID, "researcher", projection,
		func(context.Context, domaintask.ResearchResultProjection) error { verifyCalls++; return nil }, nil, nil)
	if !errors.Is(err, domaintask.ErrTypedTaskRequiresOwner) || verifyCalls != 1 {
		t.Fatalf("owner-managed Research result error=%v verifyCalls=%d", err, verifyCalls)
	}
}
