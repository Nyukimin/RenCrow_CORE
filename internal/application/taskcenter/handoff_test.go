package taskcenter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	persistworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskCenterTypedHandoffRecoversAfterPolicyRevocation(t *testing.T) {
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "typed-handoff.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	policyBefore := typedHandoffPolicy("mio", "other")
	policyAfter := typedHandoffPolicy("other")
	policyRevision, err := policyBefore.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash(before) error = %v", err)
	}
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	actors := []string{"mio", "other"}
	kinds := []Kind{{ID: domainworkstream.TaskPlanKindMovieCatalogLookup, Label: "Movie catalog lookup", IntrinsicAllowedAgents: []string{"mio", "other"}, ExecutorAvailable: true}}
	service, err := NewService(store, policyBefore, actors, kinds, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService(before) error = %v", err)
	}
	userScope := taskCenterServiceTestScope(t, "typed-submit")
	created, err := service.Submit(tool.WithToolExecutionScope(context.Background(), userScope), SubmitInput{
		DataScope: domainworkstream.TaskPlanDataScopePublic,
		KindID:    domainworkstream.TaskPlanKindMovieCatalogLookup,
		KindInput: json.RawMessage(`{"kind":"MOVIE","name":" Arrival ","limit":0}`),
		Title:     "Arrival lookup", Description: "This freeform Goal description must not enter the lookup Worker context.",
		SuccessCriteria: []string{"The exact catalog match is reported."}, Verification: []string{"Verify the typed lookup result."},
		Assignee: domainworkstream.TaskPlanAssigneeUnassigned, IdempotencyKey: "typed-submit", ExpectedPolicyRevision: policyRevision,
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	wantInput := json.RawMessage(`{"kind":"movie","name":"Arrival","limit":10}`)
	if string(created.Item.KindInput) != string(wantInput) || created.Item.KindInputVersion != 1 {
		t.Fatalf("canonical typed input = %s v%d, want %s v1", created.Item.KindInput, created.Item.KindInputVersion, wantInput)
	}
	agentScope := taskCenterServiceAgentScope(t, "typed-claim", "mio")
	claimed, err := service.Claim(tool.WithToolExecutionScope(context.Background(), agentScope), ClaimInput{
		GoalID: created.Item.GoalID, ExpectedRevision: created.Item.Revision,
		ExpectedPolicyRevision: policyRevision, IdempotencyKey: "typed-claim",
	})
	if err != nil {
		t.Fatalf("Claim() error = %v", err)
	}
	taskID := modulecore.NewTaskID()
	draft, err := json.Marshal(task.Task{
		Title: claimed.Item.Title, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
		WorkstreamID: modulecore.WorkstreamID(claimed.Item.WorkstreamID), GoalID: modulecore.GoalID(claimed.Item.GoalID),
	})
	if err != nil {
		t.Fatalf("marshal Task draft: %v", err)
	}
	shared, err := json.Marshal(task.SharedRoleContext{})
	if err != nil {
		t.Fatalf("marshal shared context: %v", err)
	}
	handoffKey := taskexecution.HandoffKey(modulecore.NewRequestID())
	prepared, err := service.PrepareHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), HandoffPrepareInput{
		GoalID: claimed.Item.GoalID, ExpectedRevision: claimed.Item.Revision,
		ExpectedAssignmentEpoch: claimed.Item.AssignmentEpoch, ExpectedPolicyRevision: policyRevision,
		IdempotencyKey: "typed-prepare", HandoffKey: handoffKey, TaskDraft: draft, SharedContext: shared,
	})
	if err != nil {
		t.Fatalf("PrepareHandoff() error = %v", err)
	}
	if prepared.Item.Revision != claimed.Item.Revision+1 || prepared.Item.TaskID != "" {
		t.Fatalf("prepared Goal projection = %#v", prepared.Item)
	}
	preparedGoal, err := store.GetTaskPlan(tool.WithToolExecutionScope(context.Background(), agentScope), agentScope, prepared.Item.GoalID)
	if err != nil || preparedGoal.TaskPlan.HandoffInput == nil {
		t.Fatalf("GetTaskPlan(prepared) error = %v, input = %#v", err, preparedGoal.TaskPlan)
	}
	var frozenDraft task.Task
	if err := decodeSingleStrictJSON(preparedGoal.TaskPlan.HandoffInput.TaskDraft, &frozenDraft); err != nil {
		t.Fatalf("decode persisted frozen Task: %v", err)
	}
	if frozenDraft.Timing == nil || frozenDraft.TaskCenterExecution == nil || frozenDraft.TaskCenterExecution.SourceGoalID != modulecore.GoalID(prepared.Item.GoalID) || frozenDraft.TaskCenterExecution.PreparedRevision != prepared.Item.Revision || frozenDraft.TaskCenterExecution.AssignmentEpoch != prepared.Item.AssignmentEpoch {
		t.Fatalf("frozen Task lacks exact Goal provenance: %#v", frozenDraft)
	}
	inputDigest, err := task.TaskCenterKindInputSHA256(wantInput)
	if err != nil || frozenDraft.TaskCenterExecution.KindInputSHA256 != inputDigest {
		t.Fatalf("frozen typed input digest = %q, want %q (error %v)", frozenDraft.TaskCenterExecution.KindInputSHA256, inputDigest, err)
	}
	response, err := json.Marshal(prepared.Item)
	if err != nil || containsJSONKey(response, "task_draft") || containsJSONKey(response, "shared_context") {
		t.Fatalf("Goal projection exposed frozen private context: %s (error %v)", response, err)
	}

	expectedRunID := modulecore.NewRunID()
	verificationCalls := 0
	verifier := handoffVerifierFunc(func(ctx context.Context, source domainworkstream.Goal, gotTaskID modulecore.TaskID, gotRunID modulecore.RunID, authorize HandoffReceiptAuthorizationCheck) error {
		verificationCalls++
		if source.TaskPlan == nil || source.TaskPlan.HandoffInput == nil || gotTaskID != taskID || gotRunID != expectedRunID {
			return ErrForbidden
		}
		return authorize(ctx)
	})
	serviceAfter, err := NewServiceWithHandoffReceiptVerifier(store, policyAfter, actors, kinds, func() time.Time { return now.Add(24 * time.Hour) }, verifier)
	if err != nil {
		t.Fatalf("NewService(after revocation) error = %v", err)
	}
	unknown, err := serviceAfter.MarkHandoffOutcomeUnknown(tool.WithToolExecutionScope(context.Background(), agentScope), HandoffTransitionInput{
		GoalID: prepared.Item.GoalID, ExpectedRevision: prepared.Item.Revision,
		ExpectedAssignmentEpoch: prepared.Item.AssignmentEpoch, ExpectedPolicyRevision: policyRevision,
		IdempotencyKey: "typed-unknown", HandoffKey: handoffKey,
	})
	if err != nil || unknown.Item.Revision != prepared.Item.Revision+1 {
		t.Fatalf("MarkHandoffOutcomeUnknown() = %#v, error = %v", unknown.Item, err)
	}
	linkInput := HandoffTransitionInput{
		GoalID: unknown.Item.GoalID, ExpectedRevision: unknown.Item.Revision,
		ExpectedAssignmentEpoch: unknown.Item.AssignmentEpoch, ExpectedPolicyRevision: policyRevision,
		IdempotencyKey: "typed-link", HandoffKey: handoffKey, TaskID: taskID, RunID: expectedRunID,
	}
	serviceWithoutVerifier, err := NewService(store, policyAfter, actors, kinds, func() time.Time { return now.Add(24 * time.Hour) })
	if err != nil {
		t.Fatalf("NewService(without receipt verifier) error = %v", err)
	}
	if _, err := serviceWithoutVerifier.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), linkInput); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("LinkHandoff() without receipt verifier error = %v, want unsupported", err)
	}
	wrongTask := linkInput
	wrongTask.TaskID = modulecore.NewTaskID()
	if _, err := serviceAfter.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), wrongTask); !errors.Is(err, ErrForbidden) {
		t.Fatalf("LinkHandoff() with wrong Task ID error = %v, want forbidden", err)
	}
	wrongRun := linkInput
	wrongRun.RunID = modulecore.NewRunID()
	if _, err := serviceAfter.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), wrongRun); !errors.Is(err, ErrForbidden) {
		t.Fatalf("LinkHandoff() with unverified Run ID error = %v, want forbidden", err)
	}
	linked, err := serviceAfter.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), linkInput)
	if err != nil || linked.Item.TaskID != string(taskID) || linked.Item.RunID == "" || linked.Item.Revision != unknown.Item.Revision+1 {
		t.Fatalf("LinkHandoff() = %#v, error = %v", linked.Item, err)
	}
	if linked.Item.KindInputVersion != 1 || string(linked.Item.KindInput) != string(wantInput) {
		t.Fatalf("linked typed operation input changed: %#v", linked.Item)
	}
	linkedGoal, err := store.GetTaskPlan(tool.WithToolExecutionScope(context.Background(), agentScope), agentScope, linked.Item.GoalID)
	if err != nil || !taskPlanLinkReceiptMatches(linkInput, linkedGoal) {
		t.Fatal("linked receipt result does not match the complete Link request")
	}
	for name, mutate := range map[string]func(*HandoffTransitionInput){
		"GoalID":                  func(input *HandoffTransitionInput) { input.GoalID = string(modulecore.NewGoalID()) },
		"ExpectedRevision":        func(input *HandoffTransitionInput) { input.ExpectedRevision++ },
		"ExpectedAssignmentEpoch": func(input *HandoffTransitionInput) { input.ExpectedAssignmentEpoch++ },
		"ExpectedPolicyRevision":  func(input *HandoffTransitionInput) { input.ExpectedPolicyRevision = "different-policy" },
		"HandoffKey": func(input *HandoffTransitionInput) {
			input.HandoffKey = taskexecution.HandoffKey(modulecore.NewRequestID())
		},
		"TaskID": func(input *HandoffTransitionInput) { input.TaskID = modulecore.NewTaskID() },
		"RunID":  func(input *HandoffTransitionInput) { input.RunID = modulecore.NewRunID() },
	} {
		t.Run("receipt mismatch/"+name, func(t *testing.T) {
			candidate := linkInput
			mutate(&candidate)
			if taskPlanLinkReceiptMatches(candidate, linkedGoal) {
				t.Fatal("changed Link request field matched persisted receipt result")
			}
		})
	}
	callsAfterLink := verificationCalls
	replayedLink, err := serviceAfter.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), linkInput)
	if err != nil || !replayedLink.Replayed || replayedLink.Item.TaskID != linked.Item.TaskID || verificationCalls != callsAfterLink {
		t.Fatalf("lost-response LinkHandoff() replay = %#v, error = %v, verifier calls %d -> %d", replayedLink.Item, err, callsAfterLink, verificationCalls)
	}
	changedReceipt := linkInput
	changedReceipt.RunID = modulecore.NewRunID()
	if _, err := serviceAfter.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), changedReceipt); !errors.Is(err, ErrConflict) || verificationCalls != callsAfterLink {
		t.Fatalf("same-key changed receipt LinkHandoff() error = %v, verifier calls %d -> %d", err, callsAfterLink, verificationCalls)
	}
	changedTaskReceipt := linkInput
	changedTaskReceipt.TaskID = modulecore.NewTaskID()
	if _, err := serviceAfter.LinkHandoff(tool.WithToolExecutionScope(context.Background(), agentScope), changedTaskReceipt); !errors.Is(err, ErrConflict) || verificationCalls != callsAfterLink {
		t.Fatalf("same-key changed Task LinkHandoff() error = %v, verifier calls %d -> %d", err, callsAfterLink, verificationCalls)
	}
}

func TestTaskCenterReceiptVerifierErrorCategoriesStaySafe(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want error
	}{
		{name: "canceled", err: context.Canceled, want: context.Canceled},
		{name: "deadline", err: context.DeadlineExceeded, want: context.DeadlineExceeded},
		{name: "conflict", err: ErrConflict, want: ErrConflict},
		{name: "receipt conflict", err: task.ErrTaskCenterExecutionReceiptConflict, want: ErrConflict},
		{name: "forbidden", err: task.ErrTaskCenterExecutionReceiptForbidden, want: ErrForbidden},
		{name: "unsupported", err: task.ErrTaskCenterExecutionReceiptUnsupported, want: ErrUnsupported},
		{name: "corrupt receipt", err: task.ErrTaskCenterExecutionReceiptCorrupt, want: ErrUnavailable},
		{name: "missing receipt", err: task.ErrNotFound, want: ErrUnavailable},
		{name: "unknown owner failure", err: errors.New("sensitive owner detail"), want: ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := mapHandoffReceiptError(test.err)
			if !errors.Is(got, test.want) {
				t.Fatalf("mapHandoffReceiptError() = %v, want category %v", got, test.want)
			}
			if test.name == "unknown owner failure" && got.Error() == test.err.Error() {
				t.Fatal("raw owner details were exposed")
			}
		})
	}
}

type handoffVerifierFunc func(context.Context, domainworkstream.Goal, modulecore.TaskID, modulecore.RunID, HandoffReceiptAuthorizationCheck) error

func (verify handoffVerifierFunc) VerifyTaskCenterHandoffReceipt(ctx context.Context, source domainworkstream.Goal, taskID modulecore.TaskID, runID modulecore.RunID, authorize HandoffReceiptAuthorizationCheck) error {
	return verify(ctx, source, taskID, runID, authorize)
}

func TestTaskCenterStrictMovieLookupInputAndPrivateDraftBinding(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"movie","name":"Arrival","unexpected":"ignored"}`,
		`{"kind":"movie","name":"Arrival","name":"Other"}`,
		`{"kind":"movie","name":"Arrival"} {}`,
	} {
		if _, err := normalizeTaskPlanMovieLookup(json.RawMessage(raw)); err == nil {
			t.Errorf("malformed typed lookup %s was accepted", raw)
		}
	}
	if _, err := normalizeTaskPlanMovieLookup(json.RawMessage(`{"kind":"movie","name":"Arrival","limit":21}`)); err == nil {
		t.Fatal("lookup over the owner limit was accepted")
	}

	goalID := modulecore.NewGoalID()
	workstreamID := modulecore.NewWorkstreamID()
	goal := domainworkstream.Goal{
		GoalID: string(goalID), WorkstreamID: string(workstreamID), Title: "Private lookup", Status: domainworkstream.StatusWaiting,
		TaskPlan: &domainworkstream.TaskPlan{DataScope: domainworkstream.TaskPlanDataScopeUser, UserID: "user-A", Assignee: "mio"},
	}
	draft := task.Task{
		Title: goal.Title, OwnerID: "mio", Assignee: "mio", ReadOnly: true,
		WorkstreamID: workstreamID, GoalID: goalID,
		PrivateAccess: &task.TaskPrivateAccess{Scope: task.TaskAccessScopeUser, AuthenticatedUserID: "user-A"},
	}
	shared := task.SharedRoleContext{}
	if err := validateFrozenMovieLookupDraft(goal, draft, shared); err != nil {
		t.Fatalf("matching private owner draft rejected: %v", err)
	}
	draft.TaskID = modulecore.NewTaskID()
	if err := validateFrozenMovieLookupDraft(goal, draft, shared); err == nil {
		t.Fatal("typed lookup accepted caller-selected TaskID")
	}
	draft.TaskID = ""
	draft.ModuleRoot = "/private/arbitrary-module"
	if err := validateFrozenMovieLookupDraft(goal, draft, shared); err == nil {
		t.Fatal("typed lookup accepted caller-selected execution module metadata")
	}
	draft.ModuleRoot = ""
	draft.PrivateAccess.AuthenticatedUserID = "user-B"
	if err := validateFrozenMovieLookupDraft(goal, draft, shared); !errors.Is(err, task.ErrPrivateTaskAccessInvalid) {
		t.Fatalf("foreign private owner draft error = %v, want invalid private access", err)
	}
	draft.PrivateAccess.AuthenticatedUserID = "user-A"
	shared.UserIntent = "freeform worker instructions"
	if err := validateFrozenMovieLookupDraft(goal, draft, shared); err == nil {
		t.Fatal("freeform lookup Worker instructions were accepted")
	}
}

func TestPublicCreatorReleaseSeparatesActorKindAndID(t *testing.T) {
	policy := taskCenterTestPolicy("mio")
	store := newStateContractStore()
	service, err := NewService(store, policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, nil)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	scope := taskCenterServiceTestScope(t, "public-release")
	goal := domainworkstream.Goal{
		GoalID: string(modulecore.NewGoalID()), WorkstreamID: string(modulecore.NewWorkstreamID()), Status: domainworkstream.StatusWaiting,
		TaskPlan: &domainworkstream.TaskPlan{DataScope: domainworkstream.TaskPlanDataScopePublic, CreatedBy: scope.ActorID, CreatorKind: string(tool.ActorKindAgent), Assignee: domainworkstream.TaskPlanAssigneeUnassigned, KindID: "research", HandoffState: domainworkstream.TaskPlanHandoffNone},
	}
	if err := service.authorizeRelease(scope, goal); !errors.Is(err, ErrForbidden) {
		t.Fatalf("same-ID Agent creator release error = %v, want forbidden", err)
	}
	goal.TaskPlan.CreatorKind = string(tool.ActorKindUser)
	if err := service.authorizeRelease(scope, goal); err != nil {
		t.Fatalf("same-ID user creator release error = %v", err)
	}
}

func typedHandoffPolicy(actorIDs ...string) taskexecution.Policy {
	return taskexecution.Policy{SchemaVersion: taskexecution.SchemaVersionV1, TaskKinds: map[string]taskexecution.KindPolicy{
		domainworkstream.TaskPlanKindMovieCatalogLookup: {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsExplicit, ActorIDs: append([]string(nil), actorIDs...)}},
	}}
}

func containsJSONKey(raw []byte, key string) bool {
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil {
		return false
	}
	_, exists := object[key]
	return exists
}
