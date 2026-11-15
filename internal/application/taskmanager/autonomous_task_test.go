package taskmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const autonomousTestDate = "2026-11-15"

func autonomousPlanDraft(t *testing.T, date string) domaintask.Task {
	t.Helper()
	taskID, err := domaintask.AutonomousPlanTaskID("ren", "mio", date, "")
	if err != nil {
		t.Fatalf("AutonomousPlanTaskID: %v", err)
	}
	moduleID, err := domaintask.AutonomousModuleID("mio")
	if err != nil {
		t.Fatal(err)
	}
	return domaintask.Task{
		TaskID: taskID, Title: "Review the actor's recent results", ModuleID: moduleID,
		OwnerID: "ren", Assignee: "mio", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow,
		InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{
			PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan,
			Date: date, RootTaskID: taskID,
		},
	}
}

func admitAutonomousTestPlan(t *testing.T, manager *Manager) domaintask.Task {
	t.Helper()
	return admitAutonomousCandidate(t, manager, autonomousPlanDraft(t, autonomousTestDate), domaintask.SharedRoleContext{
		UserIntent:  "Evaluate prior results and make a bounded plan.",
		CurrentPlan: "Review completed work and decide whether one useful step remains.",
	})
}

func admitAutonomousCandidate(t *testing.T, manager *Manager, draft domaintask.Task, shared domaintask.SharedRoleContext) domaintask.Task {
	t.Helper()
	got, err := manager.AdmitAutonomousTask(context.Background(), draft, shared)
	if err != nil {
		t.Fatalf("AdmitAutonomousTask: %v", err)
	}
	return got
}

func TestAutonomousAdmissionIsDeterministicAndGenericCreateCannotSpoof(t *testing.T) {
	manager := newExistsTestManager(t)
	ctx := context.Background()
	draft := autonomousPlanDraft(t, autonomousTestDate)
	shared := domaintask.SharedRoleContext{UserIntent: "Review prior work", CurrentPlan: "Decide next step"}
	withModuleRoot := shared
	withModuleRoot.ModuleRoot = "/tmp/forbidden-autonomous-root"
	if _, err := manager.AdmitAutonomousTask(ctx, draft, withModuleRoot); err == nil {
		t.Fatal("autonomous admission accepted a SharedRoleContext module root")
	}
	if _, err := manager.Create(ctx, draft, shared); err == nil {
		t.Fatal("generic Create accepted autonomous Task metadata")
	}
	created := admitAutonomousCandidate(t, manager, draft, shared)
	if created.TaskID != draft.TaskID || created.Status != domaintask.StatusQueued || !created.ReadOnly || created.Priority != domaintask.PriorityLow || created.Route != domaintask.RouteCHAT {
		t.Fatalf("admitted autonomous Task has wrong canonical shape: %+v", created)
	}
	replayed := admitAutonomousCandidate(t, manager, draft, shared)
	if replayed.TaskID != created.TaskID {
		t.Fatalf("idempotent admission returned %s, want %s", replayed.TaskID, created.TaskID)
	}
	conflict := draft
	conflict.Title = "Different content with the same deterministic ID"
	if _, err := manager.AdmitAutonomousTask(ctx, conflict, shared); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("changed candidate error = %v, want ErrRunConflict", err)
	}
	if _, _, err := manager.CreateAndStartRun(ctx, draft, shared); err == nil {
		t.Fatal("generic CreateAndStartRun accepted autonomous Task metadata")
	}
}

func TestAutonomousDecisionAndSourceAdmissionUseSucceededCanonicalRuns(t *testing.T) {
	ctx := context.Background()
	manager := newExistsTestManager(t)
	plan := admitAutonomousTestPlan(t, manager)
	workID, err := domaintask.AutonomousWorkTaskID(plan.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	decision := domaintask.AutonomousDecision{
		Decision: domaintask.AutonomousDecisionContinue, Reason: "A bounded review step can improve the next plan.",
		Title: "Review the result", Objective: "Review the completed result and extract useful findings.",
		Completion: "Write a concise result summary.", CandidateTaskID: workID,
	}
	queueID := modulecore.NewQueueItemID()
	planRun, err := manager.StartPrivateTextRun(ctx, plan.TaskID, queueID, plan.Assignee, "", "")
	if err != nil {
		t.Fatalf("start plan Run: %v", err)
	}
	planOutput := "The plan's reviewed source output."
	if err := manager.SavePrivateTextOutput(ctx, plan.TaskID, planRun.RunID, plan.Assignee, planOutput); err != nil {
		t.Fatalf("save plan output: %v", err)
	}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, "shiro", decision); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("wrong decision actor error = %v, want ErrRunConflict", err)
	}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, plan.Assignee, decision); err != nil {
		t.Fatalf("SaveAutonomousDecision: %v", err)
	}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, plan.Assignee, decision); err != nil {
		t.Fatalf("idempotent SaveAutonomousDecision: %v", err)
	}
	conflictingDecision := decision
	conflictingDecision.Reason = "Changed after admission"
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, plan.Assignee, conflictingDecision); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("changed decision error = %v, want ErrRunConflict", err)
	}
	if err := manager.UpdateContext(ctx, domaintask.SharedRoleContext{
		TaskID: plan.TaskID, UserIntent: "attempt to rewrite the prompt",
		AutonomousDecision: &conflictingDecision,
	}); err != nil {
		t.Fatalf("generic UpdateContext: %v", err)
	}
	storedContext, err := manager.Context(ctx, plan.TaskID)
	if err != nil || storedContext.UserIntent != "Evaluate prior results and make a bounded plan." || storedContext.AutonomousDecision == nil || *storedContext.AutonomousDecision != decision {
		t.Fatalf("generic context update changed autonomous contract: context=%+v err=%v", storedContext, err)
	}

	work := domaintask.Task{
		TaskID: workID, Title: decision.Title, ModuleID: plan.ModuleID, OwnerID: plan.OwnerID, Assignee: plan.Assignee,
		Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{
			PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindWork,
			Date: plan.Autonomous.Date, RootTaskID: plan.Autonomous.RootTaskID, SourceTaskID: plan.TaskID,
			SourceRunID: planRun.RunID, Depth: 1,
		},
	}
	workContext := domaintask.SharedRoleContext{UserIntent: decision.Objective, CurrentPlan: decision.Completion}
	if _, err := manager.AdmitAutonomousTask(ctx, work, workContext); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("candidate from active plan error = %v, want ErrRunConflict", err)
	}
	if err := manager.CompletePrivateTextRun(ctx, plan.TaskID, planRun.RunID, plan.Assignee, false); err != nil {
		t.Fatalf("complete plan Run: %v", err)
	}
	createdWork := admitAutonomousCandidate(t, manager, work, workContext)
	if createdWork.TaskID != workID {
		t.Fatalf("admitted work Task = %s, want deterministic %s", createdWork.TaskID, workID)
	}
	if err := manager.UpdateContext(ctx, domaintask.SharedRoleContext{
		TaskID: workID, UserIntent: "rewrite the Work objective", CurrentPlan: "rewrite completion",
	}); err != nil {
		t.Fatalf("generic Work context update: %v", err)
	}
	storedWorkContext, err := manager.Context(ctx, workID)
	if err != nil || storedWorkContext.UserIntent != decision.Objective || storedWorkContext.CurrentPlan != decision.Completion {
		t.Fatalf("generic context update changed admitted Work content: context=%+v err=%v", storedWorkContext, err)
	}
	if _, err := manager.AdmitAutonomousTask(ctx, work, domaintask.SharedRoleContext{
		UserIntent: decision.Objective, CurrentPlan: "modified completion condition",
	}); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("changed Work context error = %v, want ErrRunConflict", err)
	}

	workRun, err := manager.StartPrivateTextRun(ctx, workID, modulecore.NewQueueItemID(), plan.Assignee, "", "")
	if err != nil {
		t.Fatalf("start work Run: %v", err)
	}
	workOutput := "A concise result summary."
	if err := manager.SavePrivateTextOutput(ctx, workID, workRun.RunID, plan.Assignee, workOutput); err != nil {
		t.Fatalf("save work output: %v", err)
	}
	if err := manager.CompletePrivateTextRun(ctx, workID, workRun.RunID, plan.Assignee, false); err != nil {
		t.Fatalf("complete work Run: %v", err)
	}

	digest := sha256.Sum256([]byte(workOutput))
	evalTaskID, err := domaintask.AutonomousPlanTaskID(plan.OwnerID, plan.Assignee, plan.Autonomous.Date, workID)
	if err != nil {
		t.Fatal(err)
	}
	evalPlan := domaintask.Task{
		TaskID: evalTaskID, Title: "Evaluate the completed review", ModuleID: plan.ModuleID, OwnerID: plan.OwnerID, Assignee: plan.Assignee,
		Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, InterruptPolicy: domaintask.InterruptSilent, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{
			PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan,
			Date: plan.Autonomous.Date, RootTaskID: plan.TaskID, SourceTaskID: workID, SourceRunID: workRun.RunID,
			SourceOutputSHA256: hex.EncodeToString(digest[:]), Depth: 1,
		},
	}
	evalContext := domaintask.SharedRoleContext{UserIntent: "Evaluate the completed result", CurrentPlan: "Decide whether one bounded next step remains."}
	if created := admitAutonomousCandidate(t, manager, evalPlan, evalContext); created.TaskID != evalTaskID {
		t.Fatalf("admitted evaluation plan = %s, want %s", created.TaskID, evalTaskID)
	}
	wrongDate := evalPlan
	wrongDateID, err := domaintask.AutonomousPlanTaskID(plan.OwnerID, plan.Assignee, "2026-11-16", workID)
	if err != nil {
		t.Fatal(err)
	}
	wrongDate.TaskID = wrongDateID
	wrongMetadata := *evalPlan.Autonomous
	wrongMetadata.Date = "2026-11-16"
	wrongDate.Autonomous = &wrongMetadata
	if _, err := manager.AdmitAutonomousTask(ctx, wrongDate, evalContext); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("altered successor date error = %v, want ErrRunConflict", err)
	}
}

func TestAutonomousDecisionRequiresActiveOwnedPlanRun(t *testing.T) {
	ctx := context.Background()
	manager := newExistsTestManager(t)
	plan := admitAutonomousTestPlan(t, manager)
	workID, _ := domaintask.AutonomousWorkTaskID(plan.TaskID)
	decision := domaintask.AutonomousDecision{Decision: domaintask.AutonomousDecisionContinue, Reason: "continue", Title: "Next", Objective: "Do next", Completion: "Finish next", CandidateTaskID: workID}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, modulecore.NewRunID(), plan.Assignee, decision); err == nil {
		t.Fatal("decision for an unissued Run was accepted")
	}
	planRun, err := manager.StartPrivateTextRun(ctx, plan.TaskID, modulecore.NewQueueItemID(), plan.Assignee, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, plan.Assignee, decision); err == nil {
		t.Fatal("decision was accepted before its private output checkpoint was saved")
	}
	if err := manager.SavePrivateTextOutput(ctx, plan.TaskID, planRun.RunID, plan.Assignee, "result"); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, plan.Assignee, decision); err != nil {
		t.Fatal(err)
	}
	if err := manager.CompletePrivateTextRun(ctx, plan.TaskID, planRun.RunID, plan.Assignee, false); err != nil {
		t.Fatal(err)
	}
	if err := manager.SaveAutonomousDecision(ctx, plan.TaskID, planRun.RunID, plan.Assignee, decision); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("decision for completed Run error = %v, want ErrRunConflict", err)
	}
}
