package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskcenter"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type taskCenterToolServiceStub struct {
	listQuery   taskcenter.ListQuery
	listCtx     context.Context
	submit      taskcenter.SubmitInput
	submitCtx   context.Context
	claim       taskcenter.ClaimInput
	claimCtx    context.Context
	getGoalID   string
	getCtx      context.Context
	listCalls   int
	submitCalls int
	claimCalls  int
	getCalls    int
}

type taskCenterExecutionServiceStub struct {
	*taskCenterToolServiceStub
	input taskcenter.ExecutionInput
	ctx   context.Context
	calls int
}

func (s *taskCenterExecutionServiceStub) ExecuteTaskCenterExecution(ctx context.Context, input taskcenter.ExecutionInput) (taskcenter.ExecutionResult, error) {
	s.input, s.ctx = input, ctx
	s.calls++
	return taskcenter.ExecutionResult{GoalID: input.GoalID, Verification: "verification_pending"}, nil
}

func (s *taskCenterToolServiceStub) List(ctx context.Context, query taskcenter.ListQuery) (taskcenter.ListResult, error) {
	s.listCtx, s.listQuery = ctx, query
	s.listCalls++
	return taskcenter.ListResult{Items: []taskcenter.Item{}}, nil
}

func TestTaskCenterExecuteToolRequiresTrustedAgentInvocation(t *testing.T) {
	service := &taskCenterExecutionServiceStub{taskCenterToolServiceStub: &taskCenterToolServiceStub{}}
	chat := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, false)
	worker := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, true)
	chatTools, err := chat.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, metadata := range chatTools {
		if metadata.ToolID == taskCenterExecuteTool {
			t.Fatal("read-only Chat runner exposed tasks.execute")
		}
	}
	workerTools, err := worker.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	metadata := taskCenterMetadataByID(workerTools)[taskCenterExecuteTool]
	if metadata.ToolID == "" || metadata.Category != "mutation" || metadata.Parameters["additionalProperties"] != false {
		t.Fatalf("tasks.execute metadata = %+v", metadata)
	}
	args := map[string]any{
		"goal_id": "goal-native", "expected_revision": 7, "expected_assignment_epoch": 0,
		"expected_policy_revision": strings.Repeat("a", 64), "idempotency_key": "execute-1",
	}
	response, err := worker.ExecuteV2(context.Background(), taskCenterExecuteTool, args)
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrToolExecutionScopeMissing || service.calls != 0 {
		t.Fatalf("missing-scope execute response=%+v err=%v calls=%d", response, err, service.calls)
	}
	userCtx := taskCenterUserContextForTest(t)
	response, err = worker.ExecuteV2(userCtx, taskCenterExecuteTool, args)
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrPermissionDenied || service.calls != 0 {
		t.Fatalf("user execute response=%+v err=%v calls=%d", response, err, service.calls)
	}
	ctx := taskCenterAgentContext(t, "midori")
	response, err = worker.ExecuteV2(ctx, taskCenterExecuteTool, args)
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrPermissionDenied || service.calls != 0 {
		t.Fatalf("unbound Agent invocation response=%+v err=%v calls=%d", response, err, service.calls)
	}
	identityCtx, err := domainexecution.WithIdentity(ctx, modulecore.NewTaskID(), modulecore.NewRunID(), "")
	if err != nil {
		t.Fatal(err)
	}
	response, err = worker.ExecuteV2(identityCtx, taskCenterExecuteTool, args)
	if err != nil || response == nil || response.Error != nil || service.calls != 1 || service.input.GoalID != "goal-native" ||
		service.input.CallerTaskID == "" || service.input.CallerRunID == "" || service.input.ExpectedRevision != 7 || service.input.ExpectedAssignmentEpoch != 0 {
		t.Fatalf("trusted execute response=%+v err=%v calls=%d input=%+v", response, err, service.calls, service.input)
	}
	if service.input.CallerTaskID == modulecore.TaskID("attacker") {
		t.Fatalf("caller-supplied TaskID was accepted: %+v", service.input)
	}
}
func (s *taskCenterToolServiceStub) Capabilities(context.Context) (taskcenter.Capabilities, error) {
	return taskcenter.Capabilities{Kinds: []taskcenter.KindCapability{{KindID: "movie.catalog.lookup"}}, PolicyRevision: strings.Repeat("a", 64)}, nil
}
func (s *taskCenterToolServiceStub) Get(ctx context.Context, goalID string) (taskcenter.Item, error) {
	s.getCtx, s.getGoalID = ctx, goalID
	s.getCalls++
	return taskcenter.Item{GoalID: goalID, Title: "full detail"}, nil
}
func (s *taskCenterToolServiceStub) Submit(ctx context.Context, input taskcenter.SubmitInput) (taskcenter.MutationResult, error) {
	s.submitCtx, s.submit = ctx, input
	s.submitCalls++
	return taskcenter.MutationResult{}, nil
}
func (s *taskCenterToolServiceStub) Claim(ctx context.Context, input taskcenter.ClaimInput) (taskcenter.MutationResult, error) {
	s.claimCtx, s.claim = ctx, input
	s.claimCalls++
	return taskcenter.MutationResult{}, nil
}

func TestTaskCenterToolsRegisterReadonlyAndWorkerMutationMetadata(t *testing.T) {
	service := &taskCenterToolServiceStub{}
	chat := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, false)
	worker := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, true)
	chatTools, err := chat.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	workerTools, err := worker.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	chatByID := taskCenterMetadataByID(chatTools)
	workerByID := taskCenterMetadataByID(workerTools)
	if len(chatByID) != len(chatTools) || len(workerByID) != len(workerTools) {
		t.Fatalf("duplicate Chat/Worker tool metadata IDs: chat=%d/%d worker=%d/%d", len(chatByID), len(chatTools), len(workerByID), len(workerTools))
	}
	for _, id := range []string{taskCenterListRemainingTool, taskCenterListClaimableTool, taskCenterGetTool, taskCenterCapabilitiesTool} {
		if _, ok := chatByID[id]; !ok {
			t.Fatalf("chat runner lacks read-only tool %q", id)
		}
	}
	if metadata := workerByID[taskCenterGetTool]; metadata.Category != "query" || metadata.Parameters["additionalProperties"] != false {
		t.Fatalf("Worker tasks.get metadata = %+v, want closed query schema", metadata)
	}
	for _, id := range []string{taskCenterAddTool, taskCenterClaimTool} {
		if _, ok := chatByID[id]; ok {
			t.Fatalf("chat runner unexpectedly exposes mutation tool %q", id)
		}
		metadata, ok := workerByID[id]
		if !ok || metadata.Category != "mutation" || metadata.Parameters["additionalProperties"] != false {
			t.Fatalf("worker mutation metadata %q = %+v, present=%v", id, metadata, ok)
		}
	}
	addProperties, ok := workerByID[taskCenterAddTool].Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tasks.add properties = %#v", workerByID[taskCenterAddTool].Parameters["properties"])
	}
	kindInput, ok := addProperties["kind_input"].(map[string]any)
	description, hasDescription := kindInput["description"].(string)
	if !ok || !hasDescription || kindInput["type"] != "object" || !strings.Contains(description, "tasks.capabilities") || kindInput["additionalProperties"] != true {
		t.Fatalf("tasks.add kind_input metadata = %#v", addProperties["kind_input"])
	}
}

func TestTaskCenterReadToolsRequireTrustedScopeAndUseCanonicalViews(t *testing.T) {
	service := &taskCenterToolServiceStub{}
	runner := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, true)
	response, err := runner.ExecuteV2(context.Background(), taskCenterListRemainingTool, map[string]any{})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrToolExecutionScopeMissing || service.listCalls != 0 {
		t.Fatalf("missing scope response=%+v err=%v list_calls=%d", response, err, service.listCalls)
	}
	ctx := taskCenterAgentContext(t, "midori")
	response, err = runner.ExecuteV2(ctx, taskCenterListRemainingTool, map[string]any{})
	if err != nil || response == nil || response.Error != nil || service.listQuery.View != taskcenter.ViewRemaining || service.listQuery.Assignee != "" {
		t.Fatalf("remaining response=%+v err=%v query=%+v", response, err, service.listQuery)
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.listCtx)
	if !found || scope.ActorKind != tool.ActorKindAgent || scope.ActorID != "midori" {
		t.Fatalf("service received scope=%+v found=%v", scope, found)
	}
	response, err = runner.ExecuteV2(ctx, taskCenterListClaimableTool, map[string]any{})
	if err != nil || response == nil || response.Error != nil || service.listQuery.View != taskcenter.ViewClaimable || service.listQuery.Assignee != "unassigned" {
		t.Fatalf("claimable response=%+v err=%v query=%+v", response, err, service.listQuery)
	}
	response, err = runner.ExecuteV2(ctx, taskCenterListRemainingTool, map[string]any{"limit": 125, "cursor": "next-page"})
	if err != nil || response == nil || response.Error != nil || service.listQuery.Limit != 125 || service.listQuery.Cursor != "next-page" {
		t.Fatalf("paged remaining response=%+v err=%v query=%+v", response, err, service.listQuery)
	}
	response, err = runner.ExecuteV2(ctx, taskCenterListRemainingTool, map[string]any{"assignee": "aka"})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("caller-selected assignee response=%+v err=%v", response, err)
	}
	response, err = runner.ExecuteV2(ctx, taskCenterCapabilitiesTool, map[string]any{})
	if err != nil || response == nil || response.Error != nil {
		t.Fatalf("capabilities response=%+v err=%v", response, err)
	}
	response, err = runner.ExecuteV2(context.Background(), taskCenterGetTool, map[string]any{"goal_id": "goal-private"})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrToolExecutionScopeMissing || service.getCalls != 0 {
		t.Fatalf("get without trusted scope response=%+v err=%v calls=%d", response, err, service.getCalls)
	}
	response, err = runner.ExecuteV2(ctx, taskCenterGetTool, map[string]any{"goal_id": "goal-visible"})
	if err != nil || response == nil || response.Error != nil || service.getCalls != 1 || service.getGoalID != "goal-visible" {
		t.Fatalf("Worker tasks.get response=%+v err=%v calls=%d GoalID=%q", response, err, service.getCalls, service.getGoalID)
	}
	scope, found = tool.ToolExecutionScopeFromContext(service.getCtx)
	if !found || scope.ActorKind != tool.ActorKindAgent || scope.ActorID != "midori" {
		t.Fatalf("tasks.get received scope=%+v found=%v", scope, found)
	}
	response, err = runner.ExecuteV2(ctx, taskCenterGetTool, map[string]any{"goal_id": "goal-visible", "actor_id": "aka"})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed || service.getCalls != 1 {
		t.Fatalf("spoofed tasks.get response=%+v err=%v calls=%d", response, err, service.getCalls)
	}
	chat := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, false)
	userCtx := taskCenterUserContextForTest(t)
	response, err = chat.ExecuteV2(userCtx, taskCenterGetTool, map[string]any{"goal_id": "goal-visible-to-user"})
	if err != nil || response == nil || response.Error != nil || service.getCalls != 2 || service.getGoalID != "goal-visible-to-user" {
		t.Fatalf("Chat read-only tasks.get response=%+v err=%v calls=%d GoalID=%q", response, err, service.getCalls, service.getGoalID)
	}
	scope, found = tool.ToolExecutionScopeFromContext(service.getCtx)
	if !found || scope.ActorKind != tool.ActorKindUser || scope.AuthenticatedUserID != "local-user" {
		t.Fatalf("Chat tasks.get received scope=%+v found=%v", scope, found)
	}
}

func TestTaskCenterMutationToolsRejectActorSpoofingAndPreserveTrustedActor(t *testing.T) {
	service := &taskCenterToolServiceStub{}
	runner := NewToolRunner(ToolRunnerConfig{DisableToolHarness: true}).WithTaskCenterService(service, true)
	ctx := taskCenterAgentContext(t, "midori")
	spoofed := map[string]any{"kind_id": "writing.novel", "title": "title", "actor_id": "aka"}
	response, err := runner.ExecuteV2(ctx, taskCenterAddTool, spoofed)
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrValidationFailed || service.submitCalls != 0 {
		t.Fatalf("spoofed add response=%+v err=%v submits=%d", response, err, service.submitCalls)
	}
	valid := map[string]any{
		"kind_id": "writing.novel", "title": "draft outline", "success_criteria": []any{"outline exists"},
		"verification": []any{"review outline"}, "idempotency_key": "task-center-add-1", "expected_policy_revision": strings.Repeat("a", 64),
		"timing": map[string]any{"timezone": "", "scheduled_start_at": nil, "not_before": nil, "allowed_windows": []any{}, "deadline_at": nil, "recurrence": nil, "estimated_duration_seconds": nil},
	}
	response, err = runner.ExecuteV2(ctx, taskCenterAddTool, valid)
	if err != nil || response == nil || response.Error != nil || service.submitCalls != 1 || service.submit.KindID != "writing.novel" {
		t.Fatalf("valid add response=%+v err=%v submits=%d input=%+v", response, err, service.submitCalls, service.submit)
	}
	scope, found := tool.ToolExecutionScopeFromContext(service.submitCtx)
	if !found || scope.ActorID != "midori" {
		t.Fatalf("submit scope=%+v found=%v", scope, found)
	}
	_, err = runner.ExecuteV2(ctx, taskCenterClaimTool, map[string]any{"goal_id": "goal-1", "expected_revision": 1, "expected_policy_revision": strings.Repeat("a", 64), "idempotency_key": "claim-1"})
	if err != nil || service.claimCalls != 1 || service.claim.GoalID != "goal-1" {
		t.Fatalf("claim err=%v claims=%d input=%+v", err, service.claimCalls, service.claim)
	}
	userCtx := taskCenterUserContextForTest(t)
	response, err = runner.ExecuteV2(userCtx, taskCenterClaimTool, map[string]any{"goal_id": "goal-1", "expected_revision": 1, "expected_policy_revision": strings.Repeat("a", 64), "idempotency_key": "claim-2"})
	if err != nil || response == nil || response.Error == nil || response.Error.Code != tool.ErrPermissionDenied || service.claimCalls != 1 {
		t.Fatalf("user claim response=%+v err=%v claims=%d", response, err, service.claimCalls)
	}
}

func taskCenterMetadataByID(values []tool.ToolMetadata) map[string]tool.ToolMetadata {
	out := make(map[string]tool.ToolMetadata, len(values))
	for _, value := range values {
		out[value.ToolID] = value
	}
	return out
}

func taskCenterAgentContext(t *testing.T, actor string) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope("task-center-test", tool.ActorKindAgent, actor, "", []string{tool.DataScopePublic}, tool.AuthenticationSourceAgentOrchestrator)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}

func taskCenterUserContextForTest(t *testing.T) context.Context {
	t.Helper()
	scope, err := tool.NewToolExecutionScope("task-center-test-user", tool.ActorKindUser, "local-user", "local-user", []string{tool.DataScopePublic, tool.DataScopeUser}, tool.AuthenticationSourceHTTP)
	if err != nil {
		t.Fatal(err)
	}
	return tool.WithToolExecutionScope(context.Background(), scope)
}
