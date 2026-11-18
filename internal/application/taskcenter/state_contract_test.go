package taskcenter

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

func TestReleaseRejectsTerminalGoalsAndPreservesReceiptReplay(t *testing.T) {
	policy := taskCenterTestPolicy("mio")
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash() error = %v", err)
	}
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)

	for _, status := range []string{domainworkstream.StatusCompleted, domainworkstream.StatusArchived} {
		t.Run(status, func(t *testing.T) {
			goal := taskCenterStateTestGoal("goal-"+status, status, domainworkstream.TaskPlanAssigneeUnassigned, "user-a", now, policyRevision)
			store := &stateContractStore{
				goals: map[string]domainworkstream.Goal{goal.GoalID: goal},
				receipts: map[string]domainworkstream.TaskPlanMutationResult{
					stateContractReceiptKey("release", "old-release"): {
						Goal:       taskCenterStateTestGoal(goal.GoalID, domainworkstream.StatusWaiting, domainworkstream.TaskPlanAssigneeUnassigned, "user-a", now, policyRevision),
						ReceiptRef: "art_old_release",
					},
				},
			}
			service, err := NewService(store, policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, func() time.Time { return now })
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}

			userScope := taskCenterServiceTestScope(t, "release-replay")
			ctx := tool.WithToolExecutionScope(context.Background(), userScope)
			replayed, err := service.Release(ctx, ReleaseInput{
				GoalID: goal.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: policyRevision, IdempotencyKey: "old-release",
			})
			if err != nil || !replayed.Replayed || replayed.Item.Status != domainworkstream.StatusWaiting {
				t.Fatalf("Release(old receipt) = %#v, error = %v, want historical replay", replayed, err)
			}
			if store.authorizationCalls != 0 {
				t.Fatalf("historical receipt ran authorization callback %d times", store.authorizationCalls)
			}

			before := cloneGoalForStateContract(store.goals[goal.GoalID])
			_, err = service.Release(ctx, ReleaseInput{
				GoalID: goal.GoalID, ExpectedRevision: goal.TaskPlan.Revision,
				ExpectedPolicyRevision: policyRevision, IdempotencyKey: "new-terminal-release",
			})
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Release(terminal) error = %v, want conflict", err)
			}
			if after := store.goals[goal.GoalID]; !reflect.DeepEqual(after, before) {
				t.Fatalf("terminal Goal changed after release rejection:\nbefore=%#v\nafter=%#v", before, after)
			}
			if _, found := store.receipts[stateContractReceiptKey("release", "new-terminal-release")]; found {
				t.Fatal("terminal release wrote an operation receipt")
			}

			agentScope := taskCenterServiceAgentScope(t, "claimable-terminal", "mio")
			claimable, err := service.List(tool.WithToolExecutionScope(context.Background(), agentScope), ListQuery{View: ViewClaimable})
			if err != nil || len(claimable.Items) != 0 {
				t.Fatalf("List(claimable) = %#v, error = %v, want no terminal Goal", claimable.Items, err)
			}
		})
	}
}

func TestClaimRequiresWaitingGoalStatus(t *testing.T) {
	statuses := []string{
		domainworkstream.StatusWaiting,
		domainworkstream.StatusDraft,
		domainworkstream.StatusActive,
		domainworkstream.StatusPaused,
		domainworkstream.StatusCompleted,
		domainworkstream.StatusArchived,
		"unknown",
	}
	policy := taskCenterTestPolicy("mio")
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash() error = %v", err)
	}
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	allGoals := make([]domainworkstream.Goal, 0, len(statuses))
	for index, status := range statuses {
		allGoals = append(allGoals, taskCenterStateTestGoal(fmt.Sprintf("goal-%d-%s", index, status), status, domainworkstream.TaskPlanAssigneeUnassigned, "user-a", now, policyRevision))
	}
	listStore := newStateContractStore(allGoals...)
	listService, err := NewService(listStore, policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService(list) error = %v", err)
	}
	agentScope := taskCenterServiceAgentScope(t, "claimable-statuses", "mio")
	claimable, err := listService.List(tool.WithToolExecutionScope(context.Background(), agentScope), ListQuery{View: ViewClaimable})
	if err != nil {
		t.Fatalf("List(claimable) error = %v", err)
	}
	if len(claimable.Items) != 1 || claimable.Items[0].Status != domainworkstream.StatusWaiting {
		t.Fatalf("List(claimable) statuses = %#v, want only waiting", claimable.Items)
	}

	for _, status := range statuses {
		t.Run(status, func(t *testing.T) {
			goal := taskCenterStateTestGoal("claim-"+status, status, domainworkstream.TaskPlanAssigneeUnassigned, "user-a", now, policyRevision)
			store := newStateContractStore(goal)
			service, err := NewService(store, policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, func() time.Time { return now })
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			before := cloneGoalForStateContract(store.goals[goal.GoalID])
			if status == domainworkstream.StatusWaiting {
				goal.CompletedAt = now.Add(-time.Minute)
				store.goals[goal.GoalID] = cloneGoalForStateContract(goal)
				before = cloneGoalForStateContract(goal)
			}
			result, err := service.Claim(tool.WithToolExecutionScope(context.Background(), agentScope), ClaimInput{
				GoalID: goal.GoalID, ExpectedRevision: goal.TaskPlan.Revision,
				ExpectedPolicyRevision: policyRevision, IdempotencyKey: "claim-" + status,
			})
			if status == domainworkstream.StatusWaiting {
				if err != nil || result.Item.Assignee != "mio" {
					t.Fatalf("Claim(waiting) = %#v, error = %v", result, err)
				}
				after := store.goals[goal.GoalID]
				if after.Status != before.Status || !after.CompletedAt.Equal(before.CompletedAt) {
					t.Fatalf("Claim(waiting) changed lifecycle fields: before=%#v after=%#v", before, after)
				}
				return
			}
			if !errors.Is(err, ErrConflict) {
				t.Fatalf("Claim(%q) error = %v, want conflict", status, err)
			}
			if after := store.goals[goal.GoalID]; !reflect.DeepEqual(after, before) {
				t.Fatalf("Claim(%q) changed Goal:\nbefore=%#v\nafter=%#v", status, before, after)
			}
			if _, found := store.receipts[stateContractReceiptKey("claim", "claim-"+status)]; found {
				t.Fatalf("Claim(%q) wrote a receipt", status)
			}
		})
	}
}

func taskCenterStateTestGoal(goalID, status, assignee, createdBy string, now time.Time, policyRevision string) domainworkstream.Goal {
	completedAt := time.Time{}
	if status == domainworkstream.StatusCompleted || status == domainworkstream.StatusArchived {
		completedAt = now.Add(-time.Minute)
	}
	return domainworkstream.Goal{
		GoalID: goalID, WorkstreamID: "ws_state_contract", Title: "State contract task",
		SuccessCriteria: []string{"The task is complete."}, Verification: []string{"Check the result."},
		Status: status, CreatedAt: now.Add(-time.Hour), CompletedAt: completedAt,
		TaskPlan: &domainworkstream.TaskPlan{
			DataScope: domainworkstream.TaskPlanDataScopePublic, CreatedBy: createdBy, Assignee: assignee,
			KindID: "research", Revision: 1, PolicyRevision: policyRevision,
			HandoffState: domainworkstream.TaskPlanHandoffNone, UpdatedAt: now.Add(-time.Hour),
		},
	}
}

type stateContractStore struct {
	goals              map[string]domainworkstream.Goal
	receipts           map[string]domainworkstream.TaskPlanMutationResult
	authorizationCalls int
}

func newStateContractStore(goals ...domainworkstream.Goal) *stateContractStore {
	store := &stateContractStore{goals: make(map[string]domainworkstream.Goal, len(goals)), receipts: make(map[string]domainworkstream.TaskPlanMutationResult)}
	for _, goal := range goals {
		store.goals[goal.GoalID] = cloneGoalForStateContract(goal)
	}
	return store
}

func (s *stateContractStore) SubmitTaskPlan(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanSubmission, TaskPlanAuthorizer) (domainworkstream.TaskPlanMutationResult, error) {
	return domainworkstream.TaskPlanMutationResult{}, errors.New("unexpected SubmitTaskPlan call")
}

func (s *stateContractStore) ClaimTaskPlan(_ context.Context, scope tool.ToolExecutionScope, request domainworkstream.TaskPlanClaim, authorize TaskPlanAuthorizer) (domainworkstream.TaskPlanMutationResult, error) {
	if result, exists := s.receipts[stateContractReceiptKey("claim", request.IdempotencyKey)]; exists {
		result.Replayed = true
		return result, nil
	}
	goal, exists := s.goals[request.GoalID]
	if !exists {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	s.authorizationCalls++
	if err := authorize(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	plan := *goal.TaskPlan
	plan.Assignee = scope.ActorID
	plan.AssignmentEpoch++
	plan.Revision++
	plan.PolicyRevision = request.ExpectedPolicyRevision
	goal.TaskPlan = &plan
	s.goals[goal.GoalID] = goal
	result := domainworkstream.TaskPlanMutationResult{Goal: cloneGoalForStateContract(goal), ReceiptRef: "art_" + request.IdempotencyKey}
	s.receipts[stateContractReceiptKey("claim", request.IdempotencyKey)] = result
	return result, nil
}

func (s *stateContractStore) ReleaseTaskPlan(_ context.Context, _ tool.ToolExecutionScope, request domainworkstream.TaskPlanRelease, authorize TaskPlanAuthorizer) (domainworkstream.TaskPlanMutationResult, error) {
	if result, exists := s.receipts[stateContractReceiptKey("release", request.IdempotencyKey)]; exists {
		result.Replayed = true
		return result, nil
	}
	goal, exists := s.goals[request.GoalID]
	if !exists {
		return domainworkstream.TaskPlanMutationResult{}, domainworkstream.ErrTaskPlanNotFound
	}
	s.authorizationCalls++
	if err := authorize(goal); err != nil {
		return domainworkstream.TaskPlanMutationResult{}, err
	}
	plan := *goal.TaskPlan
	plan.Assignee = domainworkstream.TaskPlanAssigneeUnassigned
	plan.AssignmentEpoch++
	plan.Revision++
	plan.PolicyRevision = request.ExpectedPolicyRevision
	goal.TaskPlan = &plan
	s.goals[goal.GoalID] = goal
	result := domainworkstream.TaskPlanMutationResult{Goal: cloneGoalForStateContract(goal), ReceiptRef: "art_" + request.IdempotencyKey}
	s.receipts[stateContractReceiptKey("release", request.IdempotencyKey)] = result
	return result, nil
}

func (s *stateContractStore) ListTaskPlans(context.Context, tool.ToolExecutionScope, domainworkstream.TaskPlanQuery) ([]domainworkstream.Goal, string, bool, error) {
	goals := make([]domainworkstream.Goal, 0, len(s.goals))
	for _, goal := range s.goals {
		goals = append(goals, cloneGoalForStateContract(goal))
	}
	return goals, "", false, nil
}

func (s *stateContractStore) GetTaskPlan(_ context.Context, _ tool.ToolExecutionScope, goalID string) (domainworkstream.Goal, error) {
	goal, exists := s.goals[goalID]
	if !exists {
		return domainworkstream.Goal{}, domainworkstream.ErrTaskPlanNotFound
	}
	return cloneGoalForStateContract(goal), nil
}

func (s *stateContractStore) TaskPlanReceipt(_ context.Context, _ tool.ToolExecutionScope, operation, key string) (domainworkstream.TaskPlanMutationResult, bool, error) {
	result, exists := s.receipts[stateContractReceiptKey(operation, key)]
	return result, exists, nil
}

func stateContractReceiptKey(operation, key string) string { return operation + "\x00" + key }

func cloneGoalForStateContract(goal domainworkstream.Goal) domainworkstream.Goal {
	if goal.TaskPlan != nil {
		plan := *goal.TaskPlan
		goal.TaskPlan = &plan
	}
	goal.SuccessCriteria = append([]string(nil), goal.SuccessCriteria...)
	goal.Verification = append([]string(nil), goal.Verification...)
	return goal
}

var _ TaskPlanStore = (*stateContractStore)(nil)
