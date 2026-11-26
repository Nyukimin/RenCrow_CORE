package taskcenter

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	tool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	persistworkstream "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestSubmitSharedTaskRequiresAnEligibleAgent(t *testing.T) {
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "task-center.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	policy := taskCenterTestPolicy("mio")
	revision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(store, policy, []string{"mio", "other"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true, IntrinsicAllowedAgents: []string{"other"}}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	scope := taskCenterServiceTestScope(t, "no-eligible-agent")
	input := SubmitInput{Title: "shared work", Description: "research", KindID: "research", SuccessCriteria: []string{"answer"}, Verification: []string{"verify"}, Assignee: domainworkstream.TaskPlanAssigneeUnassigned, IdempotencyKey: "no-eligible-agent", ExpectedPolicyRevision: revision}
	if _, err := service.Submit(tool.WithToolExecutionScope(context.Background(), scope), input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("shared task without an eligible agent error = %v, want invalid", err)
	}
}

func TestSubmitReceiptReplaySurvivesPolicyChange(t *testing.T) {
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "task-center.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	policyBefore := taskexecution.Policy{
		SchemaVersion: taskexecution.SchemaVersionV1,
		TaskKinds: map[string]taskexecution.KindPolicy{
			"research": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsExplicit, ActorIDs: []string{"mio"}}},
		},
	}
	policyAfter := taskexecution.Policy{
		SchemaVersion: taskexecution.SchemaVersionV1,
		TaskKinds: map[string]taskexecution.KindPolicy{
			"research": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsExplicit, ActorIDs: []string{"other"}}},
		},
	}
	actors := []string{"mio", "other"}
	kinds := []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}
	oldRevision, err := policyBefore.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash(before) error = %v", err)
	}
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	serviceBefore, err := NewService(store, policyBefore, actors, kinds, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService(before) error = %v", err)
	}
	scopeBefore := taskCenterServiceTestScope(t, "request-before")
	input := SubmitInput{
		DataScope: domainworkstream.TaskPlanDataScopePublic, KindID: "research", Description: "A deterministic task description.",
		Assignee: "mio", SuccessCriteria: []string{"The task is complete."}, Verification: []string{"Check the result."},
		IdempotencyKey: "policy-change-replay", ExpectedPolicyRevision: oldRevision,
	}
	created, err := serviceBefore.Submit(tool.WithToolExecutionScope(context.Background(), scopeBefore), input)
	if err != nil {
		t.Fatalf("Submit(before) error = %v", err)
	}
	if created.Replayed || created.Item.Title == "" || created.Item.TimingStatus != "known" || created.Item.TimingSource != "workstream.goal.task_plan" {
		t.Fatalf("Submit(before) item metadata = %#v", created.Item)
	}

	now = now.Add(2 * time.Hour)
	serviceAfter, err := NewService(store, policyAfter, actors, kinds, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService(after) error = %v", err)
	}
	scopeAfter := taskCenterServiceTestScope(t, "request-after-restart")
	replayed, err := serviceAfter.Submit(tool.WithToolExecutionScope(context.Background(), scopeAfter), input)
	if err != nil || !replayed.Replayed || replayed.Item.GoalID != created.Item.GoalID || replayed.Item.Title != created.Item.Title {
		t.Fatalf("Submit(after) replay = %#v, error = %v", replayed, err)
	}
	receipt, found, err := serviceAfter.Receipt(tool.WithToolExecutionScope(context.Background(), scopeAfter), "submit", input.IdempotencyKey)
	if err != nil || !found || receipt.Item.GoalID != created.Item.GoalID ||
		!reflect.DeepEqual(receipt.Item.SuccessCriteria, input.SuccessCriteria) || !reflect.DeepEqual(receipt.Item.Verification, input.Verification) {
		t.Fatalf("Receipt(after) = %#v, found %v, error %v", receipt, found, err)
	}

	changed := input
	changed.Title = "different body"
	if _, err := serviceAfter.Submit(tool.WithToolExecutionScope(context.Background(), scopeAfter), changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key with changed body error = %v, want conflict", err)
	}
	if len(replayed.Item.AllowedAgents) != 1 || replayed.Item.AllowedAgents[0] != "other" {
		t.Fatalf("current policy candidate projection = %#v, want only other", replayed.Item.AllowedAgents)
	}
}

func TestGameProjectRegistrationIsAtomicReplayableAndDoesNotRollbackProgress(t *testing.T) {
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "game-project.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	service, err := NewService(store, taskCenterTestPolicy("mio"), []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, time.Now)
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	submission := domainworkstream.GameProjectSubmission{Name: "New game", Description: "Initial game brief", IdempotencyKey: "new-game-plan"}
	for _, key := range domainworkstream.GameProjectStageKeys() {
		submission.Stages = append(submission.Stages, domainworkstream.GameProjectStage{
			Key: key, Title: key, Description: "stage output", SuccessCriteria: []string{"expected result exists"}, Verification: []string{"inspect expected result"},
		})
	}
	userContext := tool.WithToolExecutionScope(context.Background(), taskCenterServiceTestScope(t, "game-project-register"))
	created, err := service.SubmitGameProjectPlan(userContext, submission)
	if err != nil || created.Replayed || created.Registration.WorkstreamID == "" || len(created.Registration.GoalIDs) != len(submission.Stages) {
		t.Fatalf("registration=%+v err=%v", created, err)
	}
	workstreams, err := store.ListWorkstreams(context.Background(), 100)
	if err != nil || len(workstreams) != 1 || workstreams[0].ProjectDomain == nil || *workstreams[0].ProjectDomain != domainworkstream.ProjectDomainGame || workstreams[0].TaskCenterScope != nil {
		t.Fatalf("public Workstreams=%+v err=%v", workstreams, err)
	}
	goals, err := store.ListGoals(context.Background(), 100)
	if err != nil || len(goals) != len(submission.Stages) {
		t.Fatalf("public Goals=%+v err=%v", goals, err)
	}
	goalsByID := make(map[string]domainworkstream.Goal, len(goals))
	for _, goal := range goals {
		goalsByID[goal.GoalID] = goal
	}
	for index, goalID := range created.Registration.GoalIDs {
		goal, ok := goalsByID[goalID]
		if !ok || goal.WorkstreamID != created.Registration.WorkstreamID || goal.Status != domainworkstream.StatusDraft || goal.TaskPlan != nil || goal.Title != submission.Stages[index].Title {
			t.Fatalf("Goal %q=%+v found=%v; expected public draft Goal for receipt stage %q", goalID, goal, ok, submission.Stages[index].Key)
		}
		if !goal.CreatedAt.Equal(created.Registration.RegisteredAt) {
			t.Fatalf("Goal %q CreatedAt=%s, want actual registration time %s", goalID, goal.CreatedAt, created.Registration.RegisteredAt)
		}
	}
	progressed := goalsByID[created.Registration.GoalIDs[0]]
	progressed.Status = domainworkstream.StatusActive
	if err := store.SaveGoal(context.Background(), progressed); err != nil {
		t.Fatalf("advance first stage through current Workstream owner: %v", err)
	}
	replayed, err := service.SubmitGameProjectPlan(userContext, submission)
	if err != nil || !replayed.Replayed || replayed.Registration.WorkstreamID != created.Registration.WorkstreamID || !reflect.DeepEqual(replayed.Registration.GoalIDs, created.Registration.GoalIDs) {
		t.Fatalf("same-request replay=%+v err=%v", replayed, err)
	}
	current, found, err := store.FindGoalByID(context.Background(), progressed.GoalID)
	if err != nil || !found || current.Status != domainworkstream.StatusActive {
		t.Fatalf("current Goal=%+v found=%v err=%v; replay must not restore initial draft", current, found, err)
	}
	recovered, found, err := service.GameProjectPlanReceipt(userContext, submission.IdempotencyKey)
	if err != nil || !found || recovered.Registration.WorkstreamID != created.Registration.WorkstreamID {
		t.Fatalf("recovery=%+v found=%v err=%v", recovered, found, err)
	}
	changed := submission
	changed.Name = "Different request"
	if _, err := service.SubmitGameProjectPlan(userContext, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same key with changed payload error=%v, want conflict", err)
	}
	otherUserContext := tool.WithToolExecutionScope(context.Background(), taskCenterServiceTestScope(t, "other-user-game-project"))
	otherScope, _ := tool.ToolExecutionScopeFromContext(otherUserContext)
	otherScope.ActorID = "user-b"
	otherScope.AuthenticatedUserID = "user-b"
	otherUserContext = tool.WithToolExecutionScope(context.Background(), otherScope)
	if _, found, err := service.GameProjectPlanReceipt(otherUserContext, submission.IdempotencyKey); err != nil || found {
		t.Fatalf("other user receipt found=%v err=%v, want scoped 404", found, err)
	}
}

func TestClaimabilityUsesCurrentPolicyTimingAndAgentAssignee(t *testing.T) {
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "claimability.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	actors := []string{"mio", "other"}
	kinds := []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}
	policyBefore := taskCenterTestPolicy("mio", "other")
	policyCurrent := taskCenterTestPolicy("mio")
	oldRevision, err := policyBefore.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash(before) error = %v", err)
	}
	currentRevision, err := policyCurrent.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash(current) error = %v", err)
	}
	serviceBefore, err := NewService(store, policyBefore, actors, kinds, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService(before) error = %v", err)
	}
	userScope := taskCenterServiceTestScope(t, "submit-claimability")
	userCtx := tool.WithToolExecutionScope(context.Background(), userScope)
	submit := func(key, assignee string, subset []string, timing modulecore.TaskTiming) MutationResult {
		t.Helper()
		result, err := serviceBefore.Submit(userCtx, SubmitInput{
			DataScope: domainworkstream.TaskPlanDataScopePublic, KindID: "research", Title: key,
			Description: "Claimability task", SuccessCriteria: []string{"Complete the task."},
			Verification: []string{"Verify completion."}, Assignee: assignee, AllowedAgents: subset,
			Timing: timing, IdempotencyKey: key, ExpectedPolicyRevision: oldRevision,
		})
		if err != nil {
			t.Fatalf("Submit(%q) error = %v", key, err)
		}
		return result
	}
	assigned := submit("assigned-to-mio", "mio", nil, modulecore.TaskTiming{})
	unassigned := submit("claimable", domainworkstream.TaskPlanAssigneeUnassigned, nil, modulecore.TaskTiming{})
	revokedSubset := submit("revoked-subset", domainworkstream.TaskPlanAssigneeUnassigned, []string{"other"}, modulecore.TaskTiming{})
	future := now.Add(time.Hour)
	scheduled := submit("scheduled", domainworkstream.TaskPlanAssigneeUnassigned, nil, modulecore.TaskTiming{
		Timezone: "UTC", NotBefore: &future,
	})

	serviceCurrent, err := NewService(store, policyCurrent, actors, kinds, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService(current) error = %v", err)
	}
	mioScope := taskCenterServiceAgentScope(t, "mio-list", "mio")
	mioCtx := tool.WithToolExecutionScope(context.Background(), mioScope)
	claimable, err := serviceCurrent.List(mioCtx, ListQuery{View: ViewClaimable})
	if err != nil {
		t.Fatalf("List(claimable,mio) error = %v", err)
	}
	if len(claimable.Items) != 1 || claimable.Items[0].GoalID != unassigned.Item.GoalID {
		t.Fatalf("List(claimable,mio) = %#v, want only unassigned task", claimable.Items)
	}
	remaining, err := serviceCurrent.List(mioCtx, ListQuery{View: ViewRemaining})
	if err != nil {
		t.Fatalf("List(remaining,mio) error = %v", err)
	}
	if len(remaining.Items) != 1 || remaining.Items[0].GoalID != assigned.Item.GoalID {
		t.Fatalf("List(remaining,mio) = %#v, want only assigned task", remaining.Items)
	}

	otherScope := taskCenterServiceAgentScope(t, "other-list", "other")
	otherCtx := tool.WithToolExecutionScope(context.Background(), otherScope)
	otherClaimable, err := serviceCurrent.List(otherCtx, ListQuery{View: ViewClaimable})
	if err != nil || len(otherClaimable.Items) != 0 {
		t.Fatalf("List(claimable,other) = %#v, error = %v, want no tasks", otherClaimable.Items, err)
	}
	if _, err := serviceCurrent.Claim(otherCtx, ClaimInput{
		GoalID: unassigned.Item.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: currentRevision, IdempotencyKey: "wrong-agent-claim",
	}); !errors.Is(err, ErrForbidden) {
		t.Fatalf("Claim(other) error = %v, want forbidden", err)
	}
	if _, err := serviceCurrent.Claim(mioCtx, ClaimInput{
		GoalID: scheduled.Item.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: currentRevision, IdempotencyKey: "early-claim",
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Claim(before not_before) error = %v, want conflict", err)
	}
	claimed, err := serviceCurrent.Claim(mioCtx, ClaimInput{
		GoalID: unassigned.Item.GoalID, ExpectedRevision: 1, ExpectedPolicyRevision: currentRevision, IdempotencyKey: "mio-claim",
	})
	if err != nil || claimed.Item.Assignee != "mio" || claimed.Item.Revision != 2 || claimed.Item.AssignmentEpoch != 1 {
		t.Fatalf("Claim(mio) = %#v, error = %v", claimed, err)
	}
	for _, item := range claimable.Items {
		if item.GoalID == revokedSubset.Item.GoalID || item.GoalID == scheduled.Item.GoalID {
			t.Fatalf("claimable list included restricted task %q", item.GoalID)
		}
	}
}

func TestLegacyTypedKindGoalRemainsReadableButCannotBeClaimedForExecution(t *testing.T) {
	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "typed-input-unavailable.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	policy := taskexecution.Policy{SchemaVersion: taskexecution.SchemaVersionV1, TaskKinds: map[string]taskexecution.KindPolicy{
		"movie.catalog.lookup": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsExplicit, ActorIDs: []string{"midori"}}},
	}}
	revision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	kinds := []Kind{{ID: "movie.catalog.lookup", Label: "Movie lookup", RequiresTypedInput: true, ExecutorAvailable: true}}
	service, err := NewService(store, policy, []string{"midori"}, kinds, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	userCtx := tool.WithToolExecutionScope(context.Background(), taskCenterServiceTestScope(t, "typed-input-submit"))
	created, err := service.Submit(userCtx, SubmitInput{
		DataScope: domainworkstream.TaskPlanDataScopePublic, KindID: "movie.catalog.lookup", Title: "Legacy form task",
		SuccessCriteria: []string{"A typed query is prepared."}, Verification: []string{"Review the typed query."},
		IdempotencyKey: "typed-input-submit", ExpectedPolicyRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	agentScope, err := tool.NewToolExecutionScope("typed-input-claim", tool.ActorKindAgent, "midori", "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	agentCtx := tool.WithToolExecutionScope(context.Background(), agentScope)
	if _, err := service.Claim(agentCtx, ClaimInput{GoalID: created.Item.GoalID, ExpectedRevision: created.Item.Revision, ExpectedPolicyRevision: revision, IdempotencyKey: "typed-input-claim"}); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("claim without frozen typed input error = %v, want unsupported", err)
	}
	remaining, err := service.Get(agentCtx, created.Item.GoalID)
	if err != nil || remaining.KindInputVersion != 0 || remaining.ReasonCode != "typed_input_unavailable" {
		t.Fatalf("readable legacy item = %#v, error = %v", remaining, err)
	}
	claimable, err := service.List(agentCtx, ListQuery{View: ViewClaimable, Limit: 10})
	if err != nil || len(claimable.Items) != 0 {
		t.Fatalf("claimable typed-input-missing list = %#v, error = %v", claimable, err)
	}
}

func TestTaskCenterListBoundsUnicodeDescriptionAndGetReturnsFullAuthorizedGoal(t *testing.T) {
	for _, test := range []struct {
		name         string
		input        string
		wantRunes    int
		wantEllipsis bool
	}{
		{name: "at limit", input: strings.Repeat("あ", taskCenterListDescriptionMaxRunes), wantRunes: taskCenterListDescriptionMaxRunes},
		{name: "over limit", input: strings.Repeat("🙂", taskCenterListDescriptionMaxRunes+1), wantRunes: taskCenterListDescriptionMaxRunes, wantEllipsis: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := taskCenterListDescriptionSummary(test.input)
			if gotRunes := len([]rune(got)); gotRunes != test.wantRunes {
				t.Fatalf("summary rune count = %d, want %d", gotRunes, test.wantRunes)
			}
			if gotEllipsis := strings.HasSuffix(got, "…"); gotEllipsis != test.wantEllipsis {
				t.Fatalf("summary ellipsis = %t, want %t", gotEllipsis, test.wantEllipsis)
			}
		})
	}

	store, err := persistworkstream.NewSQLiteStore(filepath.Join(t.TempDir(), "task-center-detail.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	policy := taskCenterTestPolicy("mio")
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatalf("CanonicalHash() error = %v", err)
	}
	now := time.Date(2026, time.November, 17, 12, 0, 0, 0, time.UTC)
	service, err := NewService(store, policy, []string{"mio"}, []Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	description := strings.Repeat("説明🙂\n  ", 150) + "最後の説明🙂"
	criteria := []string{"最終結果が所定の内容を含む", "元Goalの受入条件を保つ"}
	verification := []string{"成果物を確認する", "受入条件との一致を確認する"}
	scope := taskCenterServiceTestScope(t, "task-center-detail-owner")
	ctx := tool.WithToolExecutionScope(context.Background(), scope)
	created, err := service.Submit(ctx, SubmitInput{
		DataScope: domainworkstream.TaskPlanDataScopeUser, KindID: "research", Title: "Unicode detail",
		Description: description, SuccessCriteria: criteria, Verification: verification,
		Assignee: domainworkstream.TaskPlanAssigneeUnassigned, IdempotencyKey: "unicode-list-detail",
		ExpectedPolicyRevision: policyRevision,
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}

	listed, err := service.List(ctx, ListQuery{View: ViewRemaining, Limit: 50})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].GoalID != created.Item.GoalID {
		t.Fatalf("List() items = %#v, want submitted Goal %q", listed.Items, created.Item.GoalID)
	}
	listItem := listed.Items[0]
	if !utf8.ValidString(listItem.Description) || len([]rune(listItem.Description)) > taskCenterListDescriptionMaxRunes ||
		!strings.HasSuffix(listItem.Description, "…") || strings.Contains(listItem.Description, "\n") {
		runes := []rune(listItem.Description)
		tailStart := max(0, len(runes)-24)
		t.Fatalf("List() description is not a bounded normalized Unicode summary: rune_count=%d valid=%t description_tail=%q",
			len(runes), utf8.ValidString(listItem.Description), string(runes[tailStart:]))
	}
	if strings.HasPrefix(listItem.Description, "説明🙂") == false {
		t.Fatalf("List() summary lost the leading description: %q", listItem.Description)
	}
	if listItem.SuccessCriteria != nil || listItem.Verification != nil {
		t.Fatalf("List() exposed full detail fields: criteria=%#v verification=%#v", listItem.SuccessCriteria, listItem.Verification)
	}
	wire, err := json.Marshal(listItem)
	if err != nil {
		t.Fatalf("Marshal(list item): %v", err)
	}
	var listFields map[string]json.RawMessage
	if err := json.Unmarshal(wire, &listFields); err != nil {
		t.Fatalf("Unmarshal(list item): %v", err)
	}
	if _, present := listFields["success_criteria"]; present {
		t.Fatal("List() serialized omitted success_criteria as an empty field")
	}
	if _, present := listFields["verification"]; present {
		t.Fatal("List() serialized omitted verification as an empty field")
	}

	detail, err := service.Get(ctx, created.Item.GoalID)
	if err != nil {
		t.Fatalf("Get(owner) error = %v", err)
	}
	if detail.Description != description || !reflect.DeepEqual(detail.SuccessCriteria, criteria) || !reflect.DeepEqual(detail.Verification, verification) {
		t.Fatalf("Get(owner) detail lost native Goal fields: description_equal=%t criteria=%#v verification=%#v",
			detail.Description == description, detail.SuccessCriteria, detail.Verification)
	}

	otherScope, err := tool.NewToolExecutionScope(
		"task-center-detail-other", tool.ActorKindUser, "user-b", "user-b",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(other user): %v", err)
	}
	otherScope.Purpose = "task-center detail"
	otherCtx := tool.WithToolExecutionScope(context.Background(), otherScope)
	if _, err := service.Get(otherCtx, created.Item.GoalID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get(other user) error = %v, want not found", err)
	}
	otherList, err := service.List(otherCtx, ListQuery{View: ViewRemaining, Limit: 50})
	if err != nil || len(otherList.Items) != 0 {
		t.Fatalf("List(other user) = %#v, error = %v, want no private Goal", otherList.Items, err)
	}
}

func taskCenterTestPolicy(actorIDs ...string) taskexecution.Policy {
	return taskexecution.Policy{
		SchemaVersion: taskexecution.SchemaVersionV1,
		TaskKinds: map[string]taskexecution.KindPolicy{
			"research": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsExplicit, ActorIDs: append([]string(nil), actorIDs...)}},
		},
	}
}

func taskCenterServiceTestScope(t *testing.T, requestID string) tool.ToolExecutionScope {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(
		requestID, tool.ActorKindUser, "user-a", "user-a",
		[]string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope() error = %v", err)
	}
	scope.Purpose = "task-center submission"
	return scope
}

func taskCenterServiceAgentScope(t *testing.T, requestID, actorID string) tool.ToolExecutionScope {
	t.Helper()
	scope, err := tool.NewToolExecutionScope(
		requestID, tool.ActorKindAgent, actorID, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator,
	)
	if err != nil {
		t.Fatalf("NewToolExecutionScope(agent) error = %v", err)
	}
	return scope
}
