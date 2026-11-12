package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPrivateTaskRunQueueRunnerPersistsMidoriTextOnlyToCanonicalTaskAndRun(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			testPrivateTaskRunQueueRunnerPersistsMidoriTextOnlyToCanonicalTaskAndRun(t, backend)
		})
	}
}

func testPrivateTaskRunQueueRunnerPersistsMidoriTextOnlyToCanonicalTaskAndRun(t *testing.T, backend string) {
	t.Helper()
	ctx := context.Background()
	title := "write a short story about a blue lantern"
	owner, jsonQueue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "midori", Title: title,
	}, domaintask.SharedRoleContext{})
	events, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	var state runtimeSuperAgentStateStore = jsonQueue
	if backend == "sqlite" {
		sqliteQueue, err := superagentpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.sqlite"), 3000)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := sqliteQueue.Close(); err != nil {
				t.Errorf("close SuperAgent SQLite store: %v", err)
			}
		})
		state = sqliteQueue
	}
	runtimeStore := composeRuntimeSuperAgentStore(state, events)
	response := serveTaskEnqueueRequest(&taskEnqueueHandler{
		ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner,
		queue: runtimeStore.(taskEnqueueRunQueueStore), agentSchedulerEnabled: true,
	}, "POST", "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if response.Code != 200 {
		t.Fatalf("enqueue status=%d body=%s", response.Code, response.Body.String())
	}

	provider := &privateTaskQueueLLMProvider{response: "A blue lantern glowed beside the river."}
	wild := agent.NewWildAgent(provider, "Midori: write requested text only.")
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild)
	legacyProcessor := &captureSuperAgentRunQueueProcessor{}
	scheduler := appsuperagent.NewRunQueueScheduler(runtimeStore,
		newSuperAgentRunQueueProcessor(legacyProcessor, backgroundJobFailureReporter{}, true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{
			LeaseDuration: time.Minute,
			Now:           func() time.Time { return time.Now().UTC() },
			LeaseToken:    func() (string, error) { return "private-task-runner-test-lease", nil },
			ActorTaskLister: func(ctx context.Context, actorID string, limit int) ([]domaintask.Task, error) {
				return owner.List(ctx, domaintask.Filter{Assignee: actorID, Status: domaintask.StatusQueued, Limit: limit})
			},
		},
	)
	planned, plannedTask, err := scheduler.PlanForActor(ctx, "midori")
	if err != nil || planned == nil || planned.TaskID != task.TaskID || plannedTask.TaskID != task.TaskID {
		t.Fatalf("PlanForActor()=(%+v,%+v,%v), want exact queued Task", planned, plannedTask, err)
	}
	ran, err := scheduler.RunPlanned(ctx, planned.QueueItemID, "midori")
	if err != nil || !ran {
		t.Fatalf("RunPlanned()=(%v,%v), want successful exact private run", ran, err)
	}
	if legacyProcessor.called {
		t.Fatal("private first-chat intent went through generic ProcessMessage")
	}
	if provider.calls != 1 || provider.userMessage != title {
		t.Fatalf("provider calls=%d user_message=%q, want one exact Task title", provider.calls, provider.userMessage)
	}
	identity, err := execution.IdentityFromContext(provider.ctx)
	if err != nil || identity.TaskID != task.TaskID || identity.RunID == "" || string(identity.TraceID) != provider.observation.TraceID {
		t.Fatalf("provider execution identity=%+v err=%v observation=%+v", identity, err, provider.observation)
	}
	if provider.observation.TaskID != task.TaskID || provider.observation.Initiator != "midori" || provider.observation.TraceID == "" {
		t.Fatalf("LLM execution observation=%+v, want exact Task/trace and Midori initiator", provider.observation)
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(provider.ctx)
	if !ok || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != "midori" || scope.AuthenticatedUserID != "" ||
		len(scope.AllowedDataScopes) != 1 || scope.AllowedDataScopes[0] != domaintool.DataScopePublic || scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator {
		t.Fatalf("provider ToolExecutionScope=%+v found=%t, want public-only orchestrated Midori without User identity", scope, ok)
	}

	completedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || completedTask.Status != domaintask.StatusSucceeded || completedTask.Summary != provider.response {
		t.Fatalf("canonical Task=%+v err=%v, want succeeded with actual generated text", completedTask, err)
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded || runs[0].Summary != provider.response {
		t.Fatalf("canonical Runs=%+v err=%v, want one succeeded Run with actual generated text", runs, err)
	}
	items, err := runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != "completed" || items[0].Reason != privateTaskQueueProjectionSummary ||
		strings.Contains(items[0].Reason, provider.response) || strings.Contains(items[0].Goal, title) {
		t.Fatalf("queue projection=%+v err=%v, want generic summary and redacted goal", items, err)
	}
	agentRuns, err := runtimeStore.ListAgentRuns(ctx, 10)
	if err != nil || len(agentRuns) != 0 {
		t.Fatalf("AgentRun projections=%+v err=%v, want none fabricated for private text work", agentRuns, err)
	}
}

func TestPrivateTaskRunQueueRunnerTerminalizesProviderCancellationWithoutLeakingTaskText(t *testing.T) {
	ctx := context.Background()
	title := "private authored request that must not enter diagnostics"
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "midori", Title: title,
	}, domaintask.SharedRoleContext{})
	events, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	runtimeStore := composeRuntimeSuperAgentStore(queue, events)
	response := serveTaskEnqueueRequest(&taskEnqueueHandler{
		ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner,
		queue: runtimeStore.(taskEnqueueRunQueueStore), agentSchedulerEnabled: true,
	}, "POST", "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if response.Code != 200 {
		t.Fatalf("enqueue status=%d body=%s", response.Code, response.Body.String())
	}
	provider := &privateTaskQueueLLMProvider{failure: context.Canceled}
	wild := agent.NewWildAgent(provider, "Midori: write requested text only.")
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild)
	legacyProcessor := &captureSuperAgentRunQueueProcessor{}
	listener := &captureBackgroundJobEventListener{}
	scheduler := appsuperagent.NewRunQueueScheduler(runtimeStore,
		newSuperAgentRunQueueProcessor(legacyProcessor, newBackgroundJobFailureReporter(listener), true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{
			LeaseDuration: time.Minute,
			Now:           func() time.Time { return time.Now().UTC() },
			LeaseToken:    func() (string, error) { return "private-task-cancel-test-lease", nil },
			ActorTaskLister: func(ctx context.Context, actorID string, limit int) ([]domaintask.Task, error) {
				return owner.List(ctx, domaintask.Filter{Assignee: actorID, Status: domaintask.StatusQueued, Limit: limit})
			},
		},
	)
	planned, _, err := scheduler.PlanForActor(ctx, "midori")
	if err != nil || planned == nil {
		t.Fatalf("PlanForActor()=(%+v,%v), want queued private Task", planned, err)
	}
	if ran, runErr := scheduler.RunPlanned(ctx, planned.QueueItemID, "midori"); runErr == nil || !ran {
		t.Fatalf("RunPlanned()=(%v,%v), want a failed canonical execution", ran, runErr)
	}
	if provider.calls != 1 || legacyProcessor.called {
		t.Fatalf("provider calls=%d legacy called=%t, want one private attempt and no generic route", provider.calls, legacyProcessor.called)
	}
	failedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || failedTask.Status != domaintask.StatusFailed || failedTask.Summary != errPrivateTaskExecutionFailed.Error() {
		t.Fatalf("canonical Task=%+v err=%v, want sanitized failed Task", failedTask, err)
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusFailed || runs[0].Summary != errPrivateTaskExecutionFailed.Error() {
		t.Fatalf("canonical Runs=%+v err=%v, want exact failed Run with sanitized summary", runs, err)
	}
	items, err := runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != "failed" || items[0].Reason != errPrivateTaskExecutionFailed.Error() {
		t.Fatalf("queue projection=%+v err=%v, want terminal failed item", items, err)
	}
	for _, event := range listener.Events() {
		if strings.Contains(event.Content, title) {
			t.Fatalf("background failure event leaked private task text: %q", event.Content)
		}
	}
}

func TestPrivateTaskRunQueueRunnerRejectsWrongOwnerBeforeProviderAndTerminalizesQueue(t *testing.T) {
	ctx := context.Background()
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "another-owner", Route: domaintask.RouteCHAT, Assignee: "midori", Title: "private wrong-owner title",
	}, domaintask.SharedRoleContext{})
	item := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: task.TaskID, RunStartReason: domaintask.RunStartReasonFirst,
		WorkstreamID: string(task.WorkstreamID), Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
		IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat, CreatedAt: task.CreatedAt,
	}
	if _, _, err := queue.InsertRunQueueItemIfAbsent(ctx, item); err != nil {
		t.Fatal(err)
	}
	events, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	runtimeStore := composeRuntimeSuperAgentStore(queue, events)
	provider := &privateTaskQueueLLMProvider{response: "must never execute"}
	wild := agent.NewWildAgent(provider, "Midori private task test.")
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild)
	legacyProcessor := &captureSuperAgentRunQueueProcessor{}
	scheduler := appsuperagent.NewRunQueueScheduler(runtimeStore,
		newSuperAgentRunQueueProcessor(legacyProcessor, backgroundJobFailureReporter{}, true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{
			LeaseDuration: time.Minute,
			Now:           func() time.Time { return time.Now().UTC() },
			LeaseToken:    func() (string, error) { return "private-task-wrong-owner-lease", nil },
			ActorTaskLister: func(ctx context.Context, actorID string, limit int) ([]domaintask.Task, error) {
				return owner.List(ctx, domaintask.Filter{Assignee: actorID, Status: domaintask.StatusQueued, Limit: limit})
			},
		},
	)
	planned, _, err := scheduler.PlanForActor(ctx, "midori")
	if err != nil || planned == nil || planned.QueueItemID != item.QueueItemID {
		t.Fatalf("PlanForActor()=(%+v,%v), want exact malicious queue item for defensive revalidation", planned, err)
	}
	if ran, runErr := scheduler.RunPlanned(ctx, item.QueueItemID, "midori"); runErr == nil || !ran {
		t.Fatalf("RunPlanned()=(%v,%v), want wrong-owner refusal", ran, runErr)
	}
	if provider.calls != 0 || legacyProcessor.called {
		t.Fatalf("provider calls=%d legacy called=%t, want both execution paths denied", provider.calls, legacyProcessor.called)
	}
	failedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || failedTask.Status != domaintask.StatusFailed {
		t.Fatalf("canonical Task=%+v err=%v, want terminal failed Task", failedTask, err)
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusFailed {
		t.Fatalf("canonical Runs=%+v err=%v, want exact failed Run", runs, err)
	}
	items, err := runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 || items[0].Status != "failed" || strings.Contains(items[0].Reason, task.Title) {
		t.Fatalf("private queue=%+v err=%v, want terminal non-leaking failure", items, err)
	}
}

func TestPrivateTaskRunQueueRunnerRejectsPrivateIntentWhenNoActorIsConfigured(t *testing.T) {
	ctx := context.Background()
	owner, _, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "midori", Title: "private task",
	}, domaintask.SharedRoleContext{})
	run, err := owner.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonFirst)
	if err != nil {
		t.Fatal(err)
	}
	item := domainsuperagent.RunQueueItem{
		TaskID: task.TaskID, RunID: run.RunID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat,
		WorkstreamID: string(task.WorkstreamID), AttemptCount: 3,
	}
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, nil)
	if _, err := runner.process(ctx, item, modulecore.NewTraceID()); err == nil || !strings.Contains(err.Error(), "actor is unavailable") {
		t.Fatalf("private runner error=%v, want a closed actor configuration refusal", err)
	}
	failedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || failedTask.Status != domaintask.StatusRunning {
		t.Fatalf("direct runner changed Task on actor configuration refusal: Task=%+v err=%v", failedTask, err)
	}
}

type privateTaskQueueLLMProvider struct {
	response    string
	ctx         context.Context
	observation llm.ExecutionObservation
	userMessage string
	calls       int
	failure     error
}

func (p *privateTaskQueueLLMProvider) Name() string { return "private-task-queue-test-provider" }

func (p *privateTaskQueueLLMProvider) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.ctx = ctx
	p.observation, _ = llm.ExecutionObservationFromContext(ctx)
	for _, message := range request.Messages {
		if message.Role == "user" {
			p.userMessage = message.Content
		}
	}
	if p.failure != nil {
		return llm.GenerateResponse{}, p.failure
	}
	return llm.GenerateResponse{Content: p.response}, nil
}
