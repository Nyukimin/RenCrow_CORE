package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestTaskCenterSQLiteClaimFlowsThroughSecuredAgentRun(t *testing.T) {
	workspace := t.TempDir()
	store, err := workstreampersistence.NewSQLiteStoreWithVault(filepath.Join(workspace, "workstream.db"), filepath.Join(workspace, "vault"))
	if err != nil {
		t.Fatalf("open SQLite Workstream owner: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close SQLite Workstream owner: %v", err)
		}
	})

	actors := taskCenterRegisteredActorIDs()
	policy := taskexecution.Policy{
		SchemaVersion: taskexecution.SchemaVersionV1,
		TaskKinds: map[string]taskexecution.KindPolicy{
			"movie.catalog.lookup": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsAll}},
		},
	}
	policyRevision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatalf("hash task policy: %v", err)
	}
	service, err := taskcenter.NewService(
		store,
		policy,
		actors,
		[]taskcenter.Kind{{ID: "movie.catalog.lookup", Label: "Movie catalog lookup", ExecutorAvailable: true}},
		func() time.Time { return time.Date(2026, time.November, 17, 0, 0, 0, 0, time.UTC) },
	)
	if err != nil {
		t.Fatalf("construct TaskCenter service: %v", err)
	}

	publicScope, err := domaintool.NewToolExecutionScope(
		"task-center-native-public-submit",
		domaintool.ActorKindUser,
		"operator-a",
		"operator-a",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("construct public submit scope: %v", err)
	}
	public, err := service.Submit(domaintool.WithToolExecutionScope(context.Background(), publicScope), taskcenter.SubmitInput{
		DataScope:              domainworkstream.TaskPlanDataScopePublic,
		KindID:                 "movie.catalog.lookup",
		Title:                  "Public claimable TaskCenter fixture",
		Description:            "A public Goal claimed by a secured Agent Worker.",
		SuccessCriteria:        []string{"The Goal is assigned to the requesting Agent."},
		Verification:           []string{"The Agent remaining list contains the same Goal."},
		Assignee:               domainworkstream.TaskPlanAssigneeUnassigned,
		Timing:                 modulecore.TaskTiming{Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{}},
		IdempotencyKey:         "task-center-native-public-submit-1",
		ExpectedPolicyRevision: policyRevision,
	})
	if err != nil {
		t.Fatalf("submit public Goal: %v", err)
	}
	if public.Item.DataScope != domainworkstream.TaskPlanDataScopePublic || public.Item.Assignee != domainworkstream.TaskPlanAssigneeUnassigned || public.Item.Revision != 1 {
		t.Fatalf("unexpected initial public Goal: %+v", public.Item)
	}

	foreignScope, err := domaintool.NewToolExecutionScope(
		"task-center-native-foreign-submit",
		domaintool.ActorKindUser,
		"foreign-user",
		"foreign-user",
		[]string{domaintool.DataScopePublic, domaintool.DataScopeUser},
		domaintool.AuthenticationSourceHTTP,
	)
	if err != nil {
		t.Fatalf("construct foreign private submit scope: %v", err)
	}
	foreign, err := service.Submit(domaintool.WithToolExecutionScope(context.Background(), foreignScope), taskcenter.SubmitInput{
		DataScope:              domainworkstream.TaskPlanDataScopeUser,
		KindID:                 "movie.catalog.lookup",
		Title:                  "Foreign private TaskCenter fixture",
		Description:            "This Goal must stay private to its authenticated owner.",
		SuccessCriteria:        []string{"It is not visible to another scope."},
		Verification:           []string{"The Agent cannot find its Goal ID or sentinel title."},
		Assignee:               domainworkstream.TaskPlanAssigneeUnassigned,
		Timing:                 modulecore.TaskTiming{Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{}},
		IdempotencyKey:         "task-center-native-foreign-submit-1",
		ExpectedPolicyRevision: policyRevision,
	})
	if err != nil {
		t.Fatalf("submit foreign private Goal: %v", err)
	}

	taskWorkspace, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve Task owner fixture directory: %v", err)
	}
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, taskWorkspace); err != nil {
		t.Fatalf("initialize Task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close Task owner: %v", err)
		}
	})
	task, err := deps.taskManager.Create(context.Background(), domaintask.Task{
		Title:    "TaskCenter secured Worker fixture",
		Route:    domaintask.RouteGeneral,
		Assignee: "shiro",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("create canonical Task: %v", err)
	}
	run, err := deps.taskManager.StartRunWithReason(context.Background(), task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatalf("start canonical Run: %v", err)
	}
	base := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, true)
	workerRuntime := toolsinfra.NewContextBudgetRunner(base, toolsinfra.ContextBudgetRunnerConfig{Agent: "shiro"})
	securedWorker := &taskExecutionRunner{owner: deps.taskManager, inner: workerRuntime}
	ctx := newTaskExecutionRunnerContext(t, task, run, domaintool.ActorKindAgent, "shiro", true, true)

	metadata, err := securedWorker.ListTools(context.Background())
	if err != nil {
		t.Fatalf("list secured Worker tools: %v", err)
	}
	seen := map[string]bool{}
	for _, item := range metadata {
		seen[item.ToolID] = true
	}
	for _, id := range []string{"tasks.capabilities", "tasks.list_claimable", "tasks.add", "tasks.claim", "tasks.list_remaining"} {
		if !seen[id] {
			t.Fatalf("TaskCenter Worker tool %q is missing", id)
		}
	}

	capabilitiesResponse, err := securedWorker.ExecuteV2(ctx, "tasks.capabilities", map[string]any{})
	if err != nil || capabilitiesResponse == nil || capabilitiesResponse.Error != nil {
		t.Fatalf("read Agent TaskCenter capabilities response=%+v err=%v", capabilitiesResponse, err)
	}
	capabilities, ok := capabilitiesResponse.Result.(taskcenter.Capabilities)
	if !ok || capabilities.PolicyRevision != policyRevision || !capabilities.CanClaim {
		t.Fatalf("Agent TaskCenter capabilities are incomplete: %#v", capabilitiesResponse.Result)
	}
	if len(capabilities.Agents) != len(actors) {
		t.Fatalf("capabilities Agent registry mismatch: got %v want %v", capabilities.Agents, actors)
	}

	claimableResponse, err := securedWorker.ExecuteV2(ctx, "tasks.list_claimable", map[string]any{})
	if err != nil || claimableResponse == nil || claimableResponse.Error != nil {
		t.Fatalf("list claimable response=%+v err=%v", claimableResponse, err)
	}
	claimable, ok := claimableResponse.Result.(taskcenter.ListResult)
	if !ok {
		t.Fatalf("claimable tool result has type %T", claimableResponse.Result)
	}
	if len(claimable.Items) != 1 || claimable.Items[0].GoalID != public.Item.GoalID {
		t.Fatalf("claimable list must expose only the public Goal: %+v", claimable.Items)
	}
	if claimable.Items[0].GoalID == foreign.Item.GoalID || claimable.Items[0].Title == foreign.Item.Title {
		t.Fatalf("foreign private Goal leaked into claimable list: %+v", claimable.Items)
	}

	claimArgs := map[string]any{
		"goal_id":                  public.Item.GoalID,
		"expected_revision":        public.Item.Revision,
		"expected_policy_revision": policyRevision,
		"idempotency_key":          "task-center-native-claim-shiro-1",
	}
	spoofedArgs := map[string]any{
		"goal_id":                  public.Item.GoalID,
		"expected_revision":        public.Item.Revision,
		"expected_policy_revision": policyRevision,
		"idempotency_key":          "task-center-native-claim-shiro-1",
		"actor_id":                 "aka",
	}
	spoofedResponse, err := securedWorker.ExecuteV2(ctx, "tasks.claim", spoofedArgs)
	if err != nil || spoofedResponse == nil || spoofedResponse.Error == nil || spoofedResponse.Error.Code != domaintool.ErrValidationFailed {
		t.Fatalf("actor-spoofed claim response=%+v err=%v", spoofedResponse, err)
	}

	claimResponse, err := securedWorker.ExecuteV2(ctx, "tasks.claim", claimArgs)
	if err != nil || claimResponse == nil || claimResponse.Error != nil {
		t.Fatalf("secured claim response=%+v err=%v", claimResponse, err)
	}
	claimed, ok := claimResponse.Result.(taskcenter.MutationResult)
	if !ok {
		t.Fatalf("claim tool result has type %T", claimResponse.Result)
	}
	if claimed.Replayed || claimed.Item.GoalID != public.Item.GoalID || claimed.Item.Assignee != "shiro" || claimed.Item.Revision <= public.Item.Revision {
		t.Fatalf("claim did not assign the public Goal to the trusted Agent: %+v", claimed)
	}

	replayResponse, err := securedWorker.ExecuteV2(ctx, "tasks.claim", claimArgs)
	if err != nil || replayResponse == nil || replayResponse.Error != nil {
		t.Fatalf("same-key claim replay response=%+v err=%v", replayResponse, err)
	}
	replayed, ok := replayResponse.Result.(taskcenter.MutationResult)
	if !ok {
		t.Fatalf("claim replay tool result has type %T", replayResponse.Result)
	}
	if !replayed.Replayed || replayed.Item.GoalID != claimed.Item.GoalID || replayed.Item.Assignee != "shiro" {
		t.Fatalf("same-key claim replay changed the canonical result: %+v", replayed)
	}

	addArgs := map[string]any{
		"kind_id":          "movie.catalog.lookup",
		"title":            "Agent-created TaskCenter fixture",
		"description":      "An Agent creates and owns a public Goal through its trusted Worker Run.",
		"success_criteria": []any{"The Goal is public and assigned to the trusted Agent."},
		"verification":     []any{"The Agent remaining list contains one canonical Goal after replay."},
		"timing": map[string]any{
			"timezone":                   "UTC",
			"scheduled_start_at":         nil,
			"not_before":                 nil,
			"allowed_windows":            []any{},
			"deadline_at":                nil,
			"recurrence":                 nil,
			"estimated_duration_seconds": nil,
		},
		"idempotency_key":          "task-center-native-agent-add-shiro-1",
		"expected_policy_revision": policyRevision,
	}
	addResponse, err := securedWorker.ExecuteV2(ctx, "tasks.add", addArgs)
	if err != nil || addResponse == nil || addResponse.Error != nil {
		t.Fatalf("secured Agent add response=%+v err=%v", addResponse, err)
	}
	added, ok := addResponse.Result.(taskcenter.MutationResult)
	if !ok || added.Replayed || added.Item.DataScope != domainworkstream.TaskPlanDataScopePublic || added.Item.Assignee != "shiro" {
		t.Fatalf("Agent add must create a new public Goal assigned to self: %#v", addResponse.Result)
	}
	addReplayResponse, err := securedWorker.ExecuteV2(ctx, "tasks.add", addArgs)
	if err != nil || addReplayResponse == nil || addReplayResponse.Error != nil {
		t.Fatalf("same-key Agent add replay response=%+v err=%v", addReplayResponse, err)
	}
	addReplay, ok := addReplayResponse.Result.(taskcenter.MutationResult)
	if !ok || !addReplay.Replayed || addReplay.Item.GoalID != added.Item.GoalID || addReplay.Item.Assignee != "shiro" {
		t.Fatalf("same-key Agent add replay changed the canonical Goal: %#v", addReplayResponse.Result)
	}

	remainingResponse, err := securedWorker.ExecuteV2(ctx, "tasks.list_remaining", map[string]any{})
	if err != nil || remainingResponse == nil || remainingResponse.Error != nil {
		t.Fatalf("list remaining response=%+v err=%v", remainingResponse, err)
	}
	remaining, ok := remainingResponse.Result.(taskcenter.ListResult)
	if !ok {
		t.Fatalf("remaining tool result has type %T", remainingResponse.Result)
	}
	foundClaimed := false
	foundAdded := false
	for _, item := range remaining.Items {
		if item.GoalID == public.Item.GoalID {
			foundClaimed = item.Assignee == "shiro"
		}
		if item.GoalID == added.Item.GoalID {
			if foundAdded {
				t.Fatalf("same-key Agent add replay created a duplicate Goal: %+v", remaining.Items)
			}
			foundAdded = item.Assignee == "shiro" && item.DataScope == domainworkstream.TaskPlanDataScopePublic
		}
		if item.GoalID == foreign.Item.GoalID || item.Title == foreign.Item.Title {
			t.Fatalf("foreign private Goal leaked into remaining list: %+v", item)
		}
	}
	if !foundClaimed {
		t.Fatalf("claimed public Goal is missing from the trusted Agent remaining list: %+v", remaining.Items)
	}
	if !foundAdded {
		t.Fatalf("Agent-created public Goal is missing from the trusted Agent remaining list: %+v", remaining.Items)
	}
}
