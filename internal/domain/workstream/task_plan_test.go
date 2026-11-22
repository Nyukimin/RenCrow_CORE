package workstream

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func validTaskPlan() TaskPlan {
	return TaskPlan{
		DataScope:      TaskPlanDataScopeUser,
		UserID:         "ren",
		CreatedBy:      "ren",
		Assignee:       TaskPlanAssigneeUnassigned,
		KindID:         "research",
		Revision:       1,
		PolicyRevision: strings.Repeat("a", 64),
		HandoffState:   TaskPlanHandoffNone,
		Timing:         modulecore.TaskTiming{},
		UpdatedAt:      time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC),
	}
}

func validGameProjectSubmission() GameProjectSubmission {
	stages := make([]GameProjectStage, 0, len(GameProjectStageKeys()))
	for _, key := range GameProjectStageKeys() {
		stages = append(stages, GameProjectStage{Key: key, Title: key, SuccessCriteria: []string{"result recorded"}, Verification: []string{"check result"}})
	}
	return GameProjectSubmission{Name: "Game project", Stages: stages, IdempotencyKey: "project-key"}
}

func TestGameProjectSubmissionRequiresCompleteOrderedDraftPlan(t *testing.T) {
	valid := validGameProjectSubmission()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid game project submission: %v", err)
	}
	wrongOrder := valid
	wrongOrder.Stages = append([]GameProjectStage(nil), valid.Stages...)
	wrongOrder.Stages[0], wrongOrder.Stages[1] = wrongOrder.Stages[1], wrongOrder.Stages[0]
	missingStage := valid
	missingStage.Stages = append([]GameProjectStage(nil), valid.Stages[:len(valid.Stages)-1]...)
	missingVerification := valid
	missingVerification.Stages = append([]GameProjectStage(nil), valid.Stages...)
	missingVerification.Stages[2].Verification = nil
	for name, candidate := range map[string]GameProjectSubmission{"wrong order": wrongOrder, "missing stage": missingStage, "missing verification": missingVerification} {
		t.Run(name, func(t *testing.T) {
			if err := candidate.Validate(); err == nil {
				t.Fatal("Validate() succeeded for invalid game project plan")
			}
		})
	}
}

func TestTaskPlanValidateScopeAndNativeReferences(t *testing.T) {
	valid := validTaskPlan()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid task plan: %v", err)
	}
	uppercaseUser := valid
	uppercaseUser.UserID = "Ren"
	uppercaseUser.CreatedBy = "Ren"
	if err := uppercaseUser.Validate(); err != nil {
		t.Fatalf("valid case-sensitive user identity: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*TaskPlan)
	}{
		{name: "private scope without owner", mutate: func(plan *TaskPlan) { plan.UserID = "" }},
		{name: "public scope with private owner", mutate: func(plan *TaskPlan) { plan.DataScope, plan.UserID = TaskPlanDataScopePublic, "ren" }},
		{name: "invalid policy revision", mutate: func(plan *TaskPlan) { plan.PolicyRevision = "changed" }},
		{name: "invalid task reference", mutate: func(plan *TaskPlan) { plan.TaskID = "task-1" }},
		{name: "execution reference without handoff", mutate: func(plan *TaskPlan) { plan.TaskID = modulecore.NewTaskID() }},
		{name: "zero revision", mutate: func(plan *TaskPlan) { plan.Revision = 0 }},
		{name: "invalid timing", mutate: func(plan *TaskPlan) { plan.Timing.Timezone = "not a timezone" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			plan := validTaskPlan()
			test.mutate(&plan)
			if err := plan.Validate(); err == nil {
				t.Fatal("Validate() succeeded for invalid task plan")
			}
		})
	}
}

func TestTaskPlanHandoffFrozenInputAndLegacyReadCompatibility(t *testing.T) {
	plan := validTaskPlan()
	plan.KindID = TaskPlanKindMovieCatalogLookup
	plan.KindInput = json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`)
	plan.KindInputVersion = 1
	plan.Assignee = "mio"
	plan.AssignmentEpoch = 1
	plan.Revision = 5
	plan.HandoffState = TaskPlanHandoffOutcomeUnknown
	plan.HandoffKey = "req_018c4f36-5f2f-7abc-8def-0123456789ab"
	draft := []byte(`{"task_id":"tsk_018c4f36-5f2f-7abc-8def-0123456789ab"}`)
	shared := []byte(`{"task_id":"tsk_018c4f36-5f2f-7abc-8def-0123456789ab"}`)
	plan.HandoffInput = &TaskPlanHandoffInput{
		Version: 1, Revision: 3, AssignmentEpoch: 1, KindID: plan.KindID, KindInputVersion: 1,
		TaskDraft: draft, SharedContext: shared, KindInput: append(json.RawMessage(nil), plan.KindInput...),
	}
	plan.HandoffInput.InputHash = ComputeTaskPlanHandoffInputHash(draft, shared, plan.KindInput)
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid frozen recovery state rejected: %v", err)
	}
	mutated := plan
	copyInput := *plan.HandoffInput
	copyInput.TaskDraft = append([]byte(nil), draft...)
	copyInput.TaskDraft[2] = 'X'
	mutated.HandoffInput = &copyInput
	if err := mutated.Validate(); err == nil {
		t.Fatal("changed frozen request bytes passed the stored digest")
	}

	legacy := validTaskPlan()
	legacy.HandoffState = TaskPlanHandoffLinked
	legacy.HandoffKey = "legacy-key"
	legacy.TaskID = modulecore.NewTaskID()
	if err := legacy.Validate(); err != nil {
		t.Fatalf("legacy linked Goal without a snapshot must remain readable: %v", err)
	}
}

func TestGoalStatusContracts(t *testing.T) {
	tests := []struct {
		status     string
		open       bool
		claimReady bool
		terminal   bool
	}{
		{status: StatusDraft, open: true},
		{status: StatusActive, open: true},
		{status: StatusPaused, open: true},
		{status: StatusWaiting, open: true, claimReady: true},
		{status: StatusCompleted, terminal: true},
		{status: StatusArchived, terminal: true},
		{status: "unknown"},
	}
	for _, test := range tests {
		t.Run(test.status, func(t *testing.T) {
			if got := GoalStatusOpen(test.status); got != test.open {
				t.Fatalf("GoalStatusOpen(%q) = %v, want %v", test.status, got, test.open)
			}
			if got := GoalStatusClaimReady(test.status); got != test.claimReady {
				t.Fatalf("GoalStatusClaimReady(%q) = %v, want %v", test.status, got, test.claimReady)
			}
			if got := GoalStatusTerminal(test.status); got != test.terminal {
				t.Fatalf("GoalStatusTerminal(%q) = %v, want %v", test.status, got, test.terminal)
			}
		})
	}
}

func TestTaskPlanExecutionResultCreatorAcceptanceScope(t *testing.T) {
	makeGoal := func(dataScope, creatorKind, createdBy, userID string, accepted bool) Goal {
		taskID, runID := modulecore.NewTaskID(), modulecore.NewRunID()
		goal := Goal{
			GoalID: string(modulecore.NewGoalID()), WorkstreamID: string(modulecore.NewWorkstreamID()), Title: "Typed result",
			SuccessCriteria: []string{"Review the result"}, Verification: []string{"Creator reviewed it"}, Status: StatusWaiting,
			CreatedAt: time.Now().UTC(),
		}
		kindInput := json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`)
		kindDigest, _ := domaintask.TaskCenterKindInputSHA256(kindInput)
		criteriaDigest, _ := TaskPlanCriteriaSHA256(goal.SuccessCriteria, goal.Verification)
		artifactID, _ := domaintask.TaskCenterResultArtifactID(taskID, runID)
		state, acceptedBy := TaskPlanResultVerificationPending, ""
		if accepted {
			state, acceptedBy = TaskPlanResultVerificationAccepted, createdBy
			goal.Status, goal.CompletedAt = StatusCompleted, time.Now().UTC()
		}
		plan := validTaskPlan()
		plan.DataScope, plan.CreatorKind, plan.CreatedBy, plan.UserID = dataScope, creatorKind, createdBy, userID
		plan.Assignee, plan.KindID, plan.KindInput, plan.KindInputVersion = "mio", TaskPlanKindMovieCatalogLookup, kindInput, 1
		plan.Revision, plan.AssignmentEpoch, plan.HandoffState, plan.HandoffKey = 3, 1, TaskPlanHandoffLinked, string(modulecore.NewRequestID())
		plan.TaskID, plan.RunID = taskID, runID
		plan.HandoffInput = &TaskPlanHandoffInput{Version: 1, Revision: 2, AssignmentEpoch: 1, KindID: plan.KindID, KindInputVersion: 1, TaskDraft: []byte(`{}`), SharedContext: []byte(`{}`), KindInput: kindInput}
		plan.HandoffInput.InputHash = ComputeTaskPlanHandoffInputHash(plan.HandoffInput.TaskDraft, plan.HandoffInput.SharedContext, kindInput)
		plan.ExecutionResult = &TaskPlanExecutionResult{ArtifactID: artifactID, KindInputSHA256: kindDigest, ResultSHA256: strings.Repeat("b", 64), CriteriaSHA256: criteriaDigest, Verification: state, AcceptedBy: acceptedBy}
		if accepted {
			plan.ExecutionResult.AcceptedAt = goal.CompletedAt
		}
		goal.TaskPlan = &plan
		return goal
	}
	for name, goal := range map[string]Goal{
		"public user creator":   makeGoal(TaskPlanDataScopePublic, "user", "ren", "", true),
		"private exact creator": makeGoal(TaskPlanDataScopeUser, "user", "ren", "ren", true),
	} {
		if err := ValidateGoal(goal); err != nil {
			t.Errorf("%s acceptance rejected: %v", name, err)
		}
	}
	for name, goal := range map[string]Goal{
		"agent creator":        makeGoal(TaskPlanDataScopePublic, "agent", "mio", "", true),
		"private UID mismatch": makeGoal(TaskPlanDataScopeUser, "user", "ren", "other", true),
	} {
		if err := ValidateGoal(goal); err == nil {
			t.Errorf("%s acceptance proof was accepted", name)
		}
	}
}

func TestTaskCenterWorkstreamScopeValidate(t *testing.T) {
	for _, scope := range []TaskCenterWorkstreamScope{
		{DataScope: TaskPlanDataScopePublic},
		{DataScope: TaskPlanDataScopeUser, UserID: "ren"},
	} {
		if err := scope.Validate(); err != nil {
			t.Errorf("valid scope %+v: %v", scope, err)
		}
	}
	if err := (TaskCenterWorkstreamScope{DataScope: TaskPlanDataScopeUser}).Validate(); err == nil {
		t.Fatal("private scope without user ID was accepted")
	}
	if err := (TaskCenterWorkstreamScope{DataScope: TaskPlanDataScopePublic, UserID: "ren"}).Validate(); err == nil {
		t.Fatal("public scope with a user ID was accepted")
	}
}
