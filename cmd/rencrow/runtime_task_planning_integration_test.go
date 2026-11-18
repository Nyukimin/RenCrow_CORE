package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domaincapability "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/taskexecution"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	sessionpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/session"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	workstreampersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestNativePlanningReachesFourAgentAndFourConfiguredCoderAdapters(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() { _ = tasks.Close() })
	workstream, err := workstreampersistence.NewSQLiteStore(filepath.Join(root, "workstream.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workstream.Close() })
	actors := taskCenterRegisteredActorIDs()
	policy := taskexecution.Policy{SchemaVersion: taskexecution.SchemaVersionV1, TaskKinds: map[string]taskexecution.KindPolicy{
		"research": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsAll}},
	}}
	revision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	service, err := taskcenter.NewService(workstream, policy, actors, []taskcenter.Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	deps := &Dependencies{taskCenter: service, taskManager: tasks}
	agents := agentRuntime{
		Mio: domainagent.NewMioAgent(nil, nil, nil, nil, nil, nil), ShiroChat: domainagent.NewMioAgent(nil, nil, nil, nil, nil, nil),
		Shiro: domainagent.NewShiroAgent(nil, nil, nil, "", nil),
		Heavy: domainagent.NewHeavyAgent(nil, ""), Wild: domainagent.NewWildAgent(nil, ""),
	}
	cfg := &config.Config{Coder1: config.CoderConfig{Enabled: true, Name: "aka"}, Coder2: config.CoderConfig{Enabled: true, Name: "ao"}, Coder3: config.CoderConfig{Enabled: true, Name: "kin"}, Coder4: config.CoderConfig{Enabled: true, Name: "gin"}}
	attachTaskCenterPlanningPorts(cfg, deps, agents)
	owner, err := domaintool.NewToolExecutionScope("native-plan-owner", domaintool.ActorKindUser, "operator", "operator", []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	goals := make(map[string]taskcenter.Item, len(actors))
	for _, actor := range actors {
		created, submitErr := service.Submit(domaintool.WithToolExecutionScope(context.Background(), owner), taskcenter.SubmitInput{
			DataScope: domainworkstream.TaskPlanDataScopePublic, KindID: "research", Title: "claim for " + actor,
			SuccessCriteria: []string{"The Goal is assigned to the actor."}, Verification: []string{"Read the claimed Item."}, Assignee: domainworkstream.TaskPlanAssigneeUnassigned,
			Timing: modulecore.TaskTiming{Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{}}, IdempotencyKey: "native-plan-submit-" + actor, ExpectedPolicyRevision: revision,
		})
		if submitErr != nil {
			t.Fatalf("submit %s Goal: %v", actor, submitErr)
		}
		goals[actor] = created.Item
	}
	callers := []struct {
		actor string
		call  func(context.Context, domainconversation.TurnInput) (string, error)
	}{
		{"mio", agents.Mio.Chat}, {"shiro", agents.Shiro.Execute}, {"kuro", agents.Heavy.Generate}, {"midori", agents.Wild.Generate},
	}
	for _, caller := range callers {
		parent, run, ctx := nativePlanningParentFixture(t, tasks, caller.actor, "")
		goal := goals[caller.actor]
		message := fmt.Sprintf(`/tasks {"operation":"tasks.claim","input":{"goal_id":%q,"expected_revision":%d,"expected_policy_revision":%q,"idempotency_key":%q}}`, goal.GoalID, goal.Revision, revision, "native-plan-claim-"+caller.actor)
		turn := nativePlanningTurnFixture(t, parent.TaskID, message)
		response, callErr := caller.call(ctx, turn)
		if callErr != nil {
			t.Fatalf("%s planning invocation: %v", caller.actor, callErr)
		}
		var result taskcenter.MutationResult
		if err := json.Unmarshal([]byte(response), &result); err != nil || result.Item.Assignee != caller.actor {
			t.Fatalf("%s self-claim result=%s err=%v", caller.actor, response, err)
		}
		if _, err := tasks.CompleteRun(ctx, parent.TaskID, run.RunID, caller.actor, domaintask.StatusSucceeded, "done", ""); err != nil {
			t.Fatal(err)
		}
	}

	coders := []*coderAdapter{{domainCoder: domainagent.NewCoderAgent(nil, nil, nil, "")}, {domainCoder: domainagent.NewCoderAgent(nil, nil, nil, "")}, {domainCoder: domainagent.NewCoderAgent(nil, nil, nil, "")}, {domainCoder: domainagent.NewCoderAgent(nil, nil, nil, "")}}
	attachCoderTaskPlanningPorts(cfg, deps, coders...)
	capabilityText := renderNativeTaskPlanningCapability(cfg, deps, agents, coders...)
	for _, actor := range actors {
		if !strings.Contains(capabilityText, actor) {
			t.Fatalf("connected native planning actor %q missing from capability context %q", actor, capabilityText)
		}
	}
	executor := orchestrator.NewDefaultCodeExecutor(coders[0], coders[1], coders[2], coders[3], nil, nil, nil).WithCapabilities([]domaincapability.CoderCapability{
		{Name: "coder1", Available: true}, {Name: "coder2", Available: true}, {Name: "coder3", Available: true}, {Name: "coder4", Available: true},
	})
	shiro, run, _ := nativePlanningParentFixture(t, tasks, "shiro", "")
	for _, slot := range []struct {
		actor string
		route domainrouting.Route
	}{{"aka", domainrouting.RouteCODE1}, {"ao", domainrouting.RouteCODE2}, {"kin", domainrouting.RouteCODE3}, {"gin", domainrouting.RouteCODE4}} {
		ctx, requestID := nativePlanningActorFixture(t, shiro, run, "shiro")
		command := `/tasks {"operation":"tasks.list_remaining","input":{"limit":10}}`
		response, callErr := executor.ExecuteCode(ctx, orchestrator.CodeExecutionRequest{Input: nativePlanningTurnFixture(t, shiro.TaskID, command).WithRoute(slot.route), TaskID: shiro.TaskID})
		if callErr != nil {
			t.Fatalf("%s actual CODE route dispatch: %v", slot.actor, callErr)
		}
		var listed taskcenter.ListResult
		if err := json.Unmarshal([]byte(response.Response), &listed); err != nil || response.Handled {
			t.Fatalf("%s proposal boundary/list result response=%+v err=%v", slot.actor, response, err)
		}
		envelope := []byte(`{"operation":"tasks.list_remaining","input":{"limit":10}}`)
		child, _, _, created, err := tasks.EnsureNativePlanningInvocation(ctx, shiro.TaskID, run.RunID, "shiro", slot.actor, requestID, envelope)
		if err != nil || created || child.Assignee != slot.actor || !child.ReadOnly || child.NativePlanningInvocation.ParentActorID != "shiro" {
			t.Fatalf("%s child proof=%+v created=%v err=%v", slot.actor, child, created, err)
		}
	}
	if _, err := tasks.CompleteRun(context.Background(), shiro.TaskID, run.RunID, "shiro", domaintask.StatusSucceeded, "done", ""); err != nil {
		t.Fatal(err)
	}
	privateOwner, err := domaintool.NewToolExecutionScope("native-private-owner", domaintool.ActorKindUser, "user-a", "user-a", []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	privateGoal, err := service.Submit(domaintool.WithToolExecutionScope(context.Background(), privateOwner), taskcenter.SubmitInput{
		DataScope: domainworkstream.TaskPlanDataScopeUser, KindID: "research", Title: "Private planning fixture", Description: "private-user-a-sentinel",
		SuccessCriteria: []string{"Visible to the owner."}, Verification: []string{"Foreign user is denied."}, Assignee: "mio",
		Timing: modulecore.TaskTiming{Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{}}, IdempotencyKey: "native-private-submit", ExpectedPolicyRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}
	privateParent, privateRun, privateCtx := nativePlanningParentFixture(t, tasks, "mio", "user-a")
	get := fmt.Sprintf(`/tasks {"operation":"tasks.get","input":{"goal_id":%q}}`, privateGoal.Item.GoalID)
	privateResponse, err := agents.Mio.Chat(privateCtx, nativePlanningTurnFixture(t, privateParent.TaskID, get))
	if err != nil || !strings.Contains(privateResponse, "private-user-a-sentinel") {
		t.Fatalf("authorized private Goal result=%q err=%v", privateResponse, err)
	}
	trustedScope, _ := domaintool.ToolExecutionScopeFromContext(privateCtx)
	foreignScope, err := domaintool.NewToolExecutionScope(trustedScope.RequestID, domaintool.ActorKindAgent, "mio", "user-b", []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	foreignResponse, err := agents.Mio.Chat(domaintool.WithToolExecutionScope(privateCtx, foreignScope), nativePlanningTurnFixture(t, privateParent.TaskID, get))
	if err == nil || strings.Contains(foreignResponse, "private-user-a-sentinel") {
		t.Fatalf("foreign user replay response=%q err=%v", foreignResponse, err)
	}
	foreignActorScope, err := domaintool.NewToolExecutionScope(trustedScope.RequestID, domaintool.ActorKindAgent, "shiro", "", []string{domaintool.DataScopePublic}, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	foreignActorCtx := domaintool.WithToolExecutionScope(privateCtx, foreignActorScope)
	if _, err := agents.Mio.Chat(foreignActorCtx, nativePlanningTurnFixture(t, privateParent.TaskID, `/tasks {"operation":"tasks.capabilities","input":{}}`)); err == nil {
		t.Fatal("Mio adapter accepted another actor's native scope")
	}
	if _, err := tasks.CompleteRun(privateCtx, privateParent.TaskID, privateRun.RunID, "mio", domaintask.StatusSucceeded, "done", ""); err != nil {
		t.Fatal(err)
	}
}

func TestNativePlanningOversizedMutationPersistsExactReceiptReadback(t *testing.T) {
	result := taskcenter.MutationResult{
		ReceiptRef: "receipt-123",
		Item:       taskcenter.Item{GoalID: "goal-123", Description: strings.Repeat("large-existing-goal", 4000), Revision: 7, Status: domainworkstream.StatusWaiting},
	}
	encoded, err := encodeNativeTaskPlanningResult(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 48<<10 {
		t.Fatalf("compact mutation acknowledgement is %d bytes", len(encoded))
	}
	var acknowledgement struct {
		ReceiptRef string `json:"receipt_ref"`
		GoalID     string `json:"goal_id"`
		Revision   uint64 `json:"revision"`
		Status     string `json:"status"`
		Readback   string `json:"readback_operation"`
	}
	if err := json.Unmarshal(encoded, &acknowledgement); err != nil {
		t.Fatal(err)
	}
	if acknowledgement.ReceiptRef != result.ReceiptRef || acknowledgement.GoalID != result.Item.GoalID || acknowledgement.Revision != result.Item.Revision || acknowledgement.Status != result.Item.Status || acknowledgement.Readback != "tasks.get" {
		t.Fatalf("oversized mutation lost its exact owner receipt/readback: %+v", acknowledgement)
	}
	if _, err := encodeNativeTaskPlanningResult(taskcenter.Item{GoalID: "goal-123", Description: strings.Repeat("large-read", 6000)}); err == nil {
		t.Fatal("oversized read result was silently truncated or accepted")
	}
}

func nativePlanningParentFixture(t *testing.T, tasks *taskmanager.Manager, actor, user string) (domaintask.Task, domaintask.Run, context.Context) {
	t.Helper()
	parent, run, err := tasks.CreateAndStartRun(context.Background(), domaintask.Task{Title: "native planning parent " + actor, Route: domaintask.RouteGeneral, Assignee: actor}, domaintask.SharedRoleContext{CurrentPlan: "frozen"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, requestID := nativePlanningActorFixture(t, parent, run, actor, user)
	_ = requestID
	return parent, run, ctx
}

func nativePlanningActorFixture(t *testing.T, parent domaintask.Task, run domaintask.Run, actor string, user ...string) (context.Context, modulecore.RequestID) {
	t.Helper()
	requestID := modulecore.NewRequestID()
	ctx, err := domainexecution.WithIdentity(context.Background(), parent.TaskID, run.RunID, run.TraceID)
	if err != nil {
		t.Fatal(err)
	}
	uid := ""
	if len(user) > 0 {
		uid = user[0]
	}
	scopes := []string{domaintool.DataScopePublic}
	if uid != "" {
		scopes = append(scopes, domaintool.DataScopeUser)
	}
	scope, err := domaintool.NewToolExecutionScope(string(requestID), domaintool.ActorKindAgent, actor, uid, scopes, domaintool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return domaintool.WithToolExecutionScope(ctx, scope), requestID
}

func nativePlanningTurnFixture(t *testing.T, taskID modulecore.TaskID, message string) domainconversation.TurnInput {
	t.Helper()
	address, err := domainconversation.NewChannelAddress("viewer", "native-planning-fixture")
	if err != nil {
		t.Fatal(err)
	}
	input, err := domainconversation.NewTurnInput(taskID, message, address)
	if err != nil {
		t.Fatal(err)
	}
	return input
}

type nativePlanningMustNotCallProvider struct{ calls int }

func (p *nativePlanningMustNotCallProvider) Generate(context.Context, domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.calls++
	return domainllm.GenerateResponse{}, fmt.Errorf("provider must not be called for reserved /tasks command")
}

func (*nativePlanningMustNotCallProvider) Name() string { return "native-planning-must-not-call" }

type nativePlanningMustNotClassify struct{ calls int }

func (c *nativePlanningMustNotClassify) Classify(context.Context, domainconversation.TurnInput) (domainrouting.Decision, error) {
	c.calls++
	return domainrouting.Decision{}, fmt.Errorf("classifier must not be called for reserved /tasks command")
}

func TestNativePlanningMessageOrchestratorBindsRealTaskAndSkipsModel(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	tasks := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() { _ = tasks.Close() })
	workstream, err := workstreampersistence.NewSQLiteStore(filepath.Join(root, "workstream.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = workstream.Close() })
	actors := taskCenterRegisteredActorIDs()
	policy := taskexecution.Policy{SchemaVersion: taskexecution.SchemaVersionV1, TaskKinds: map[string]taskexecution.KindPolicy{
		"research": {AllowedAgents: taskexecution.AllowedAgents{Mode: taskexecution.AllowedAgentsAll}},
	}}
	revision, err := policy.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	service, err := taskcenter.NewService(workstream, policy, actors, []taskcenter.Kind{{ID: "research", Label: "Research", ExecutorAvailable: true}}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := domaintool.NewToolExecutionScope("message-native-plan-owner", domaintool.ActorKindUser, "operator", "operator", []string{domaintool.DataScopePublic, domaintool.DataScopeUser}, domaintool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Submit(domaintool.WithToolExecutionScope(context.Background(), owner), taskcenter.SubmitInput{
		DataScope: domainworkstream.TaskPlanDataScopePublic, KindID: "research", Title: "Message-path self claim",
		SuccessCriteria: []string{"Mio claims this Goal."}, Verification: []string{"Read the claimed Goal."}, Assignee: domainworkstream.TaskPlanAssigneeUnassigned,
		Timing: modulecore.TaskTiming{Timezone: "UTC", AllowedWindows: []modulecore.TaskTimeWindow{}}, IdempotencyKey: "message-native-plan-submit", ExpectedPolicyRevision: revision,
	})
	if err != nil {
		t.Fatal(err)
	}

	provider, classifier := &nativePlanningMustNotCallProvider{}, &nativePlanningMustNotClassify{}
	mio := domainagent.NewMioAgent(provider, classifier, nil, nil, nil, nil)
	deps := &Dependencies{taskCenter: service, taskManager: tasks}
	agents := agentRuntime{Mio: mio}
	attachTaskCenterPlanningPorts(&config.Config{}, deps, agents)
	sessionRoot := filepath.Join(root, "sessions")
	if err := os.MkdirAll(sessionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	repo := sessionpersistence.NewJSONSessionRepository(sessionRoot)
	sessionID := saveRuntimeTaskSession(t, context.Background(), repo, modulecore.NewSessionID(), "viewer", "native-planning", "")
	message := fmt.Sprintf(`/tasks {"operation":"tasks.claim","input":{"goal_id":%q,"expected_revision":%d,"expected_policy_revision":%q,"idempotency_key":"message-native-plan-claim"}}`, created.Item.GoalID, created.Item.Revision, revision)
	orch := orchestrator.NewMessageOrchestrator(repo, mio, nil, nil, nil, nil, nil, nil)
	orch.SetTaskLifecycleManager(tasks)
	response, err := orch.ProcessMessage(context.Background(), orchestrator.ProcessMessageRequest{
		SessionID: string(sessionID), Channel: "viewer", ChatID: "native-planning", UserMessage: message,
	})
	if err != nil {
		t.Fatalf("MessageOrchestrator.ProcessMessage: %v", err)
	}
	if provider.calls != 0 || classifier.calls != 0 {
		t.Fatalf("reserved command invoked model path: provider=%d classifier=%d", provider.calls, classifier.calls)
	}
	var result taskcenter.MutationResult
	if err := json.Unmarshal([]byte(response.Response), &result); err != nil || result.Item.Assignee != "mio" {
		t.Fatalf("native Message-path self-claim response=%s err=%v", response.Response, err)
	}
	if response.TaskID == "" || response.Route != domainrouting.RouteCHAT {
		t.Fatalf("Message-path result lacks real root Task or CHAT route: %+v", response)
	}
}
