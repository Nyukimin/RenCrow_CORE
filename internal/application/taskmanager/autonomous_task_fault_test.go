package taskmanager

import (
	"context"
	"errors"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type failOnceContextSave struct {
	err    error
	failed bool
}

type failOnceContextStore struct {
	Store
	state *failOnceContextSave
}

func (s *failOnceContextStore) transactionView(store Store) Store {
	return &failOnceContextStore{Store: store, state: s.state}
}

func (s *failOnceContextStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(Store) error) error {
	return s.Store.TaskTransaction(ctx, taskID, func(store Store) error {
		return fn(s.transactionView(store))
	})
}

func (s *failOnceContextStore) ReadTransaction(ctx context.Context, fn func(Store) error) error {
	return s.Store.ReadTransaction(ctx, func(store Store) error {
		return fn(s.transactionView(store))
	})
}

func (s *failOnceContextStore) SaveContext(ctx context.Context, value domaintask.SharedRoleContext) error {
	if !s.state.failed {
		s.state.failed = true
		return s.state.err
	}
	return s.Store.SaveContext(ctx, value)
}

func TestAdmitAutonomousTaskRollsBackFailedContextSaveAndRetriesSameCandidate(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	registerTaskStoreCleanup(t, base)
	draft := autonomousPlanDraft(t, autonomousTestDate)
	shared := domaintask.SharedRoleContext{
		UserIntent: "Review recent work.", CurrentPlan: "Decide whether a bounded next step is useful.",
	}
	injected := errors.New("injected autonomous context append failure")
	faultStore := &failOnceContextStore{Store: base, state: &failOnceContextSave{err: injected}}
	manager := New(faultStore, DefaultParallelLimits())
	ctx := context.Background()

	if _, err := manager.AdmitAutonomousTask(ctx, draft, shared); !errors.Is(err, injected) {
		t.Fatalf("first AdmitAutonomousTask error = %v, want injected failure", err)
	}
	if _, err := base.GetTask(ctx, draft.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("orphan Task remained after failed context save: %v", err)
	}
	if _, err := base.GetContext(ctx, draft.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("Context remained after failed context save: %v", err)
	}
	runs, err := base.ListRuns(ctx, domaintask.RunFilter{TaskID: draft.TaskID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs after failed queued admission = %+v err=%v, want none", runs, err)
	}

	created, err := manager.AdmitAutonomousTask(ctx, draft, shared)
	if err != nil || created.TaskID != draft.TaskID {
		t.Fatalf("retry AdmitAutonomousTask = (%+v, %v), want deterministic candidate", created, err)
	}
	stored, err := base.GetTask(ctx, draft.TaskID)
	if err != nil || stored.TaskID != draft.TaskID {
		t.Fatalf("retried Task = (%+v, %v)", stored, err)
	}
	storedContext, err := base.GetContext(ctx, draft.TaskID)
	if err != nil || storedContext.TaskID != draft.TaskID || storedContext.UserIntent != shared.UserIntent {
		t.Fatalf("retried Context = (%+v, %v)", storedContext, err)
	}
	runs, err = base.ListRuns(ctx, domaintask.RunFilter{TaskID: draft.TaskID})
	if err != nil || len(runs) != 0 {
		t.Fatalf("Runs after successful queued retry = %+v err=%v, want none", runs, err)
	}
}

func TestSaveAutonomousDecisionFailurePreservesOutputThenRetriesImmutably(t *testing.T) {
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONLStore: %v", err)
	}
	registerTaskStoreCleanup(t, base)
	manager := New(base, DefaultParallelLimits())
	ctx := context.Background()
	plan := admitAutonomousTestPlan(t, manager)
	run, err := manager.StartPrivateTextRun(ctx, plan.TaskID, modulecore.NewQueueItemID(), plan.Assignee, "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun: %v", err)
	}
	output := "A durable plan output used by the autonomous decision."
	if err := manager.SavePrivateTextOutput(ctx, plan.TaskID, run.RunID, plan.Assignee, output); err != nil {
		t.Fatalf("SavePrivateTextOutput: %v", err)
	}
	workID, err := domaintask.AutonomousWorkTaskID(plan.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	decision := domaintask.AutonomousDecision{
		Decision: domaintask.AutonomousDecisionContinue,
		Reason:   "One useful bounded follow-up remains.",
		Title:    "Review the completed output", Objective: "Extract one useful finding.",
		Completion: "Save a concise finding summary.", CandidateTaskID: workID,
	}
	injected := errors.New("injected autonomous decision context failure")
	faultStore := &failOnceContextStore{Store: base, state: &failOnceContextSave{err: injected}}
	faultManager := New(faultStore, DefaultParallelLimits())

	if err := faultManager.SaveAutonomousDecision(ctx, plan.TaskID, run.RunID, plan.Assignee, decision); !errors.Is(err, injected) {
		t.Fatalf("first SaveAutonomousDecision error = %v, want injected failure", err)
	}
	storedContext, err := base.GetContext(ctx, plan.TaskID)
	if err != nil {
		t.Fatalf("GetContext after failed decision save: %v", err)
	}
	if storedContext.AutonomousDecision != nil || storedContext.PrivateText == nil || storedContext.PrivateText.Output != output || storedContext.PrivateText.RunID != run.RunID {
		t.Fatalf("failed decision save changed prior output or partially saved decision: context=%+v", storedContext)
	}
	if err := faultManager.ValidateRunExecution(ctx, plan.TaskID, run.RunID, plan.Assignee); err != nil {
		t.Fatalf("active Run was not preserved after decision save failure: %v", err)
	}

	if err := faultManager.SaveAutonomousDecision(ctx, plan.TaskID, run.RunID, plan.Assignee, decision); err != nil {
		t.Fatalf("retry SaveAutonomousDecision: %v", err)
	}
	storedContext, err = base.GetContext(ctx, plan.TaskID)
	if err != nil || storedContext.AutonomousDecision == nil || *storedContext.AutonomousDecision != decision ||
		storedContext.PrivateText == nil || storedContext.PrivateText.Output != output || storedContext.PrivateText.RunID != run.RunID {
		t.Fatalf("retried decision context = (%+v, %v), want exact decision and unchanged output", storedContext, err)
	}
	changed := decision
	changed.Reason = "Different decision under the same plan Run."
	if err := faultManager.SaveAutonomousDecision(ctx, plan.TaskID, run.RunID, plan.Assignee, changed); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("changed decision retry error = %v, want ErrRunConflict", err)
	}
	if err := faultManager.ValidateRunExecution(ctx, plan.TaskID, run.RunID, plan.Assignee); err != nil {
		t.Fatalf("active Run was not preserved after idempotent decision retry: %v", err)
	}
}
