package main

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestPrivateTaskRunQueueRunnerPersistsMidoriTextOnlyToCanonicalTaskAndRun(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			testPrivateTaskRunQueueRunnerPersistsMidoriTextOnlyToCanonicalTaskAndRun(t, backend)
		})
	}
}

func TestTaskEnqueueWakeRunsDurablePrivateTasksInOneSchedulerSeries(t *testing.T) {
	provider := &privateTaskQueueLLMProvider{response: "saved generated result"}
	worker := newPrivateTextWorkers(map[string]llm.LLMProvider{"aka": provider}, map[string]string{
		"aka": "Aka: complete the requested text task only.",
	})["aka"]
	runTaskEnqueueWakeContinuation(t, "aka", worker, 5*time.Second)
	if provider.calls != 3 || !reflect.DeepEqual(provider.userMessages, []string{
		"Write one concise sentence for continuation stage A.",
		"Write one concise sentence for continuation stage B.",
		"Write one concise sentence for continuation stage C.",
	}) {
		t.Fatalf("provider calls=%d messages=%q; want sequential A/B/C execution", provider.calls, provider.userMessages)
	}
}

func TestTaskEnqueueWakeRunsConfiguredGatewayTasksInOneSchedulerSeries(t *testing.T) {
	gatewayURL := strings.TrimSpace(os.Getenv("TEST_RENCROW_GATEWAY_URL"))
	if gatewayURL == "" {
		t.Skip("set TEST_RENCROW_GATEWAY_URL to explicitly enable the local production Gateway integration")
	}
	parsedURL, err := url.Parse(gatewayURL)
	if err != nil || parsedURL.Scheme != "http" || net.ParseIP(parsedURL.Hostname()) == nil || !net.ParseIP(parsedURL.Hostname()).IsLoopback() {
		t.Fatalf("TEST_RENCROW_GATEWAY_URL must be an explicit loopback HTTP URL, got %q", gatewayURL)
	}
	cfg := &config.Config{
		LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: gatewayURL, TimeoutSec: 45},
	}
	primary := buildPrimaryLLMProviders(cfg, nil)
	workers := newPrivateTextWorkers(primary.PrivateTextProviders, map[string]string{
		"aka": "You are Aka completing a concise private text task. Reply with one short, useful sentence.",
	})
	worker := workers["aka"]
	if worker == nil {
		t.Fatal("production Gateway provider did not create the exact Aka private text worker")
	}
	runTaskEnqueueWakeContinuation(t, "aka", worker, 3*time.Minute)
}

type privateTaskFirstRunGate struct {
	inner   privateTaskWorker
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *privateTaskFirstRunGate) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, allowedCapabilities []string) (string, error) {
	var waitErr error
	w.once.Do(func() {
		close(w.started)
		select {
		case <-w.release:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	})
	if waitErr != nil {
		return "", waitErr
	}
	return w.inner.ExecutePrivateWork(ctx, input, allowedCapabilities)
}

func runTaskEnqueueWakeContinuation(t *testing.T, actorID string, worker privateTaskWorker, waitTimeout time.Duration) {
	t.Helper()
	ctx := context.Background()
	owner, _, _ := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "continuation-fixture", Priority: domaintask.PriorityCritical, Title: "scheduler setup fixture outside configured actors",
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
	queue, err := superagentpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.sqlite"), 3000)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := queue.Close(); err != nil {
			t.Errorf("close SuperAgent SQLite store: %v", err)
		}
	})
	runtimeStore := composeRuntimeSuperAgentStore(queue, events)
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	var releaseFirstOnce sync.Once
	gateWorker := &privateTaskFirstRunGate{inner: worker, started: firstStarted, release: releaseFirst}
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, nil, map[string]privateTaskWorker{actorID: gateWorker})
	legacyProcessor := &captureSuperAgentRunQueueProcessor{}
	firstEmptyTaskList := make(chan struct{})
	var firstEmptyTaskListOnce sync.Once
	runQueue := appsuperagent.NewRunQueueScheduler(runtimeStore,
		newSuperAgentRunQueueProcessor(legacyProcessor, backgroundJobFailureReporter{}, true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{
			LeaseDuration: time.Minute,
			Now:           func() time.Time { return time.Now().UTC() },
			LeaseToken:    func() (string, error) { return "private-continuation-series-lease", nil },
			ActorTaskLister: func(ctx context.Context, listedActorID string, limit int) ([]domaintask.Task, error) {
				tasks, listErr := owner.List(ctx, domaintask.Filter{Assignee: listedActorID, Status: domaintask.StatusQueued, Limit: limit})
				if listedActorID == actorID && listErr == nil && len(tasks) == 0 {
					firstEmptyTaskListOnce.Do(func() { close(firstEmptyTaskList) })
				}
				return tasks, listErr
			},
		},
	)
	deps := &Dependencies{taskManager: owner, agentRunQueue: runQueue, superAgentStore: runtimeStore}
	tokenFile := filepath.Join(t.TempDir(), "agent-ops-token")
	if err := os.WriteFile(tokenFile, []byte(taskEnqueueTestToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{
		Heartbeat:     config.HeartbeatConfig{AgentSchedulerEnabled: true},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "ren", AuthTokenFile: tokenFile},
	}
	configureAgentHeartbeatScheduler(cfg, deps, nil, nil)
	heartbeat := deps.agentHeartbeat
	if heartbeat == nil {
		t.Fatal("production configureAgentHeartbeatScheduler did not configure scheduler")
	}
	handler, err := newConfiguredTaskEnqueueHandler(cfg, owner, runtimeStore, heartbeat)
	if err != nil || handler == nil {
		t.Fatalf("production newConfiguredTaskEnqueueHandler()=%v err=%v", handler, err)
	}
	lifetime, cancel := context.WithCancel(context.Background())
	startDone := make(chan error, 1)
	go func() { startDone <- heartbeat.Start(lifetime) }()
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirst) })
		cancel()
		select {
		case err := <-startDone:
			if err != nil {
				t.Errorf("heartbeat Start()=%v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("heartbeat scheduler did not stop after lifetime cancellation")
		}
		waitCtx, waitCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer waitCancel()
		if err := heartbeat.Wait(waitCtx); err != nil {
			t.Errorf("heartbeat Wait()=%v", err)
		}
	})
	select {
	case <-firstEmptyTaskList:
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not complete the initial empty private Task read for the target actor")
	}
	deadline := time.Now().Add(5 * time.Second)
	initialSweepFinished := false
	for time.Now().Before(deadline) {
		state, found := heartbeatActorState(ctx, heartbeat, actorID)
		if found && state != "checking" {
			initialSweepFinished = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !initialSweepFinished {
		t.Fatal("scheduler did not finish checking the target actor after its initial empty Task read")
	}
	firstTask, err := owner.Create(ctx, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: actorID, Priority: domaintask.PriorityCritical, Title: "Write one concise sentence for continuation stage A.",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	secondTask, err := owner.Create(ctx, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: actorID, Priority: domaintask.PriorityHigh, Title: "Write one concise sentence for continuation stage B.",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	thirdTask, err := owner.Create(ctx, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: actorID, Priority: domaintask.PriorityNormal, Title: "Write one concise sentence for continuation stage C.",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	response := serveTaskEnqueueRequest(handler, "POST", "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(firstTask.TaskID)+`"}`)
	if response.Code != 200 {
		t.Fatalf("enqueue Task %s status=%d body=%s", firstTask.TaskID, response.Code, response.Body.String())
	}
	var firstReceipt taskEnqueueReceipt
	if err := json.Unmarshal(response.Body.Bytes(), &firstReceipt); err != nil || firstReceipt.Status != "enqueued" || firstReceipt.QueueStatus != "queued" {
		t.Fatalf("enqueue Task %s receipt=%+v err=%v", firstTask.TaskID, firstReceipt, err)
	}
	select {
	case <-firstStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("durable enqueue Wake did not start Task A")
	}
	for _, task := range []domaintask.Task{secondTask, thirdTask} {
		item := domainsuperagent.RunQueueItem{
			QueueItemID: modulecore.NewQueueItemID(), TaskID: task.TaskID, RunStartReason: domaintask.RunStartReasonFirst,
			WorkstreamID: string(task.WorkstreamID), Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
			Priority: taskEnqueuePriority(task.Priority), IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat, CreatedAt: task.CreatedAt.UTC(),
		}
		if _, inserted, insertErr := queue.InsertRunQueueItemIfAbsent(ctx, item); insertErr != nil || !inserted {
			t.Fatalf("persist successor intent for %s: inserted=%t err=%v", task.TaskID, inserted, insertErr)
		}
	}
	releaseFirstOnce.Do(func() { close(releaseFirst) })
	deadline = time.Now().Add(waitTimeout)
	for time.Now().Before(deadline) {
		allFinished := true
		for _, task := range []domaintask.Task{firstTask, secondTask, thirdTask} {
			current, getErr := owner.Get(ctx, task.TaskID)
			if getErr != nil || current.Status != domaintask.StatusSucceeded {
				allFinished = false
				break
			}
		}
		items, listErr := runtimeStore.ListRunQueueItems(ctx, 10)
		if listErr != nil || len(items) != 3 {
			allFinished = false
		} else {
			for _, item := range items {
				if item.Status != "completed" || item.RunID == "" || item.CompletedAt.IsZero() {
					allFinished = false
					break
				}
			}
		}
		state, found := heartbeatActorState(ctx, heartbeat, actorID)
		if !found || state == "running" || state == "stopping" {
			allFinished = false
		}
		if allFinished {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	for _, task := range []domaintask.Task{firstTask, secondTask, thirdTask} {
		current, getErr := owner.Get(ctx, task.TaskID)
		if getErr != nil || current.Status != domaintask.StatusSucceeded || strings.TrimSpace(current.Summary) == "" {
			t.Fatalf("canonical Task=%+v err=%v; want succeeded with nonempty saved output", current, getErr)
		}
		runs, listErr := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
		if listErr != nil || len(runs) != 1 || runs[0].TaskID != task.TaskID || runs[0].Status != domaintask.RunStatusSucceeded || runs[0].Summary != current.Summary {
			t.Fatalf("canonical Runs for %s=%+v err=%v; want one succeeded Run bound to Task with exact output", task.TaskID, runs, listErr)
		}
		checkpoint, found, checkpointErr := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
		if checkpointErr != nil || !found || checkpoint.TaskID != task.TaskID || checkpoint.RunID != runs[0].RunID || checkpoint.QueueItemID == "" || checkpoint.ActorID != actorID || checkpoint.Output != current.Summary {
			t.Fatalf("canonical checkpoint for %s=%+v found=%t err=%v; want exact output bound to Task/Run/queue/actor", task.TaskID, checkpoint, found, checkpointErr)
		}
	}
	if legacyProcessor.called {
		t.Fatal("private continuation chain used generic ProcessMessage")
	}
	items, err := runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 3 {
		t.Fatalf("durable queue receipts=%+v err=%v; want three receipts", items, err)
	}
	for _, item := range items {
		if item.Status != "completed" || item.RunID == "" || item.CompletedAt.IsZero() {
			t.Fatalf("terminal queue receipt=%+v; want completed durable run receipt", item)
		}
		run, err := owner.GetRun(ctx, item.RunID)
		if err != nil || run.TaskID != item.TaskID || run.Status != domaintask.RunStatusSucceeded {
			t.Fatalf("queue RunID %s canonical Run=%+v err=%v does not match Task receipt", item.RunID, run, err)
		}
	}
	state, found := heartbeatActorState(ctx, heartbeat, actorID)
	if !found || state == "running" || state == "stopping" {
		t.Fatalf("scheduler actor %s state=%q found=%t; want actor slot released after A/B/C", actorID, state, found)
	}
}

func heartbeatActorState(ctx context.Context, heartbeat *agentheartbeat.Scheduler, actorID string) (string, bool) {
	activities, available := heartbeat.LivingSnapshot(ctx, nil)
	if !available {
		return "", false
	}
	for _, activity := range activities {
		if activity.ActorID == actorID {
			return activity.State, true
		}
	}
	return "", false
}

func TestPrivateTaskRunQueueRunnerPausesAndResumesSameTaskFromSavedOutput(t *testing.T) {
	ctx := context.Background()
	title := "private request that remains outside queue receipts"
	output := "private completed output saved before the stop signal"
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
	stop := make(chan struct{})
	provider := &privateTaskQueueLLMProvider{response: output, onGenerate: func() { close(stop) }}
	wild := agent.NewWildAgent(provider, "Midori: write requested text only.")
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild, nil)
	legacyProcessor := &captureSuperAgentRunQueueProcessor{}
	scheduler := appsuperagent.NewRunQueueScheduler(runtimeStore,
		newSuperAgentRunQueueProcessor(legacyProcessor, backgroundJobFailureReporter{}, true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{
			LeaseDuration: time.Minute,
			Now:           func() time.Time { return time.Now().UTC() },
			LeaseToken:    func() (string, error) { return "private-task-pause-resume-lease", nil },
			ActorTaskLister: func(ctx context.Context, actorID string, limit int) ([]domaintask.Task, error) {
				return owner.List(ctx, domaintask.Filter{Assignee: actorID, Limit: limit})
			},
		},
	)
	first, _, err := scheduler.PlanForActor(ctx, "midori")
	if err != nil || first == nil {
		t.Fatalf("PlanForActor(first)=(%+v,%v), want private first run", first, err)
	}
	if ran, err := scheduler.RunPlannedWithStop(ctx, first.QueueItemID, "midori", stop); err != nil || !ran {
		t.Fatalf("RunPlannedWithStop(first)=(%t,%v), want durable normal pause", ran, err)
	}
	if provider.calls != 1 || legacyProcessor.called {
		t.Fatalf("provider calls=%d legacy called=%t, want one provider call and no generic route", provider.calls, legacyProcessor.called)
	}
	pausedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || pausedTask.Status != domaintask.StatusWaiting || pausedTask.WaitingReason != domaintask.PrivateTextWaitingReason {
		t.Fatalf("paused Task=%+v err=%v, want the fixed private waiting reason", pausedTask, err)
	}
	firstRuns, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID, Limit: 2})
	if err != nil || len(firstRuns) != 1 || firstRuns[0].Status != domaintask.RunStatusWaiting {
		t.Fatalf("first Runs=%+v err=%v, want exact waiting predecessor", firstRuns, err)
	}
	checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !found || checkpoint.RunID != firstRuns[0].RunID || checkpoint.QueueItemID != first.QueueItemID || checkpoint.Output != output {
		t.Fatalf("paused checkpoint=%+v found=%t err=%v, want saved output bound to exact predecessor", checkpoint, found, err)
	}
	items, err := runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("queue items=%+v err=%v, want source and successor receipts", items, err)
	}
	var successor *domainsuperagent.RunQueueItem
	for i := range items {
		if items[i].QueueItemID == first.QueueItemID {
			if items[i].Status != "cancelled" || items[i].Reason != "private task paused" {
				t.Fatalf("source receipt=%+v, want sanitized pause completion", items[i])
			}
			continue
		}
		candidate := items[i]
		successor = &candidate
	}
	if successor == nil || successor.Status != "queued" || successor.RunStartReason != domaintask.RunStartReasonCheckpointResume ||
		successor.Action != "chat" || successor.PredecessorRunID != firstRuns[0].RunID || successor.CheckpointDigest != checkpoint.Digest() ||
		successor.IdempotencyKey != domainsuperagent.PrivateTextResumeQueueKey(firstRuns[0].RunID) {
		t.Fatalf("successor receipt=%+v, want insert-only private cursor resume", successor)
	}
	for _, item := range items {
		if strings.Contains(item.Goal, title) || strings.Contains(item.Reason, title) || strings.Contains(item.Goal, output) || strings.Contains(item.Reason, output) {
			t.Fatalf("private text leaked into queue receipt: %+v", item)
		}
	}

	resumed, resumedTask, err := scheduler.PlanForActor(ctx, "midori")
	if err != nil || resumed == nil || resumed.QueueItemID != successor.QueueItemID || resumedTask.Status != domaintask.StatusWaiting {
		t.Fatalf("PlanForActor(resume)=(%+v,%+v,%v), want the exact private successor", resumed, resumedTask, err)
	}
	if ran, err := scheduler.RunPlanned(ctx, resumed.QueueItemID, "midori"); err != nil || !ran {
		t.Fatalf("RunPlanned(resume)=(%t,%v), want successful saved-output completion", ran, err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls=%d after resume, want saved checkpoint output to skip remote compute", provider.calls)
	}
	completedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || completedTask.Status != domaintask.StatusSucceeded || completedTask.Summary != output {
		t.Fatalf("completed Task=%+v err=%v, want original CLI result summary", completedTask, err)
	}
	items, err = runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("queue items after resume=%+v err=%v, want two stable receipts", items, err)
	}
	for _, item := range items {
		if item.QueueItemID == successor.QueueItemID && (item.Status != "completed" || strings.Contains(item.Reason, output)) {
			t.Fatalf("resumed queue receipt=%+v, want completed redacted receipt", item)
		}
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
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild, nil)
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
	if err != nil || len(items) != 1 {
		t.Fatalf("run queue receipts=%+v err=%v, want one private first-run receipt", items, err)
	}
	checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !found || checkpoint.TaskID != task.TaskID || checkpoint.RunID != runs[0].RunID || checkpoint.QueueItemID != items[0].QueueItemID || checkpoint.Output != provider.response {
		t.Fatalf("private checkpoint=%+v found=%t err=%v, want exact durable output cursor", checkpoint, found, err)
	}
	if items[0].Status != "completed" || items[0].Reason != privateTaskQueueProjectionSummary ||
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
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild, nil)
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
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, wild, nil)
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
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, nil, nil)
	if _, err := runner.process(ctx, item, modulecore.NewTraceID()); err == nil || !strings.Contains(err.Error(), "actor is unavailable") {
		t.Fatalf("private runner error=%v, want a closed actor configuration refusal", err)
	}
	failedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || failedTask.Status != domaintask.StatusRunning {
		t.Fatalf("direct runner changed Task on actor configuration refusal: Task=%+v err=%v", failedTask, err)
	}
}

func TestPrivateTextStartupRecoveryRepairsIssuanceBeforeQueueAttachment(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	taskPath := filepath.Join(root, "tasks")
	queue := superagentpersistence.NewJSONLStore(filepath.Join(root, "superagent"), 3000)
	openOwner := func() *taskmanager.Manager {
		taskStore, err := taskpersistence.NewJSONLStore(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		return taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	}
	owner := openOwner()
	task, err := owner.Create(ctx, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "midori", Title: "crash window private task",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	intent := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: task.TaskID,
		RunStartReason: domaintask.RunStartReasonFirst, WorkstreamID: string(task.WorkstreamID),
		Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued",
		Priority: 2, IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat, CreatedAt: task.CreatedAt,
	}
	if _, inserted, err := queue.InsertRunQueueItemIfAbsent(ctx, intent); err != nil || !inserted {
		t.Fatalf("insert first private intent: inserted=%t err=%v", inserted, err)
	}
	claimed, err := queue.ClaimRunQueueItem(ctx, intent.QueueItemID, now, now.Add(time.Minute), "startup-recovery-lease")
	if err != nil || claimed == nil {
		t.Fatalf("claim first private intent: item=%+v err=%v", claimed, err)
	}
	issued, err := owner.StartPrivateTextRun(ctx, task.TaskID, intent.QueueItemID, "midori", "", "")
	if err != nil {
		t.Fatalf("issue private Run: %v", err)
	}
	if err := owner.Close(); err != nil {
		t.Fatal(err)
	}
	owner = openOwner()
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close recovered TaskManager: %v", err)
		}
	})

	result, err := appsuperagent.RecoverPrivateTextRunQueueReceipts(ctx, queue, owner, now.Add(time.Second))
	if err != nil {
		t.Fatalf("recover issuance-before-attachment window: %v", err)
	}
	if result.RecoveredRuns != 1 || result.QueuedResumes != 1 || result.ClosedReceipts != 1 {
		t.Fatalf("recovery result=%+v, want one old Run, successor, and source receipt repaired", result)
	}
	recoveredTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || recoveredTask.Status != domaintask.StatusWaiting || recoveredTask.WaitingReason != domaintask.PrivateTextWaitingReason {
		t.Fatalf("recovered Task=%+v err=%v, want fixed private waiting state", recoveredTask, err)
	}
	checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !found || checkpoint.RunID != issued.RunID || checkpoint.QueueItemID != intent.QueueItemID {
		t.Fatalf("recovered cursor=%+v found=%t err=%v, want exact issued Run and queue item", checkpoint, found, err)
	}
	items, err := queue.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 2 {
		t.Fatalf("recovered queue items=%+v err=%v, want source and one successor", items, err)
	}
	for _, item := range items {
		if item.QueueItemID == intent.QueueItemID && (item.Status != "cancelled" || item.RunID != issued.RunID) {
			t.Fatalf("recovered source receipt=%+v, want attached and cancelled exact old Run", item)
		}
		if domainsuperagent.IsPrivateTextResumeQueueItem(item) && (item.Status != "queued" || item.PredecessorRunID != issued.RunID || item.CheckpointDigest != checkpoint.Digest()) {
			t.Fatalf("recovered successor=%+v, want exact waiting cursor intent", item)
		}
	}
	second, err := appsuperagent.RecoverPrivateTextRunQueueReceipts(ctx, queue, owner, now.Add(2*time.Second))
	if err != nil || second.QueuedResumes != 0 || second.ClosedReceipts != 0 || second.DeferredReceipts != 0 {
		t.Fatalf("second recovery=%+v err=%v, want idempotent no-op", second, err)
	}
}

type privateTaskQueueLLMProvider struct {
	response     string
	ctx          context.Context
	observation  llm.ExecutionObservation
	userMessage  string
	userMessages []string
	calls        int
	failure      error
	onGenerate   func()
}

func (p *privateTaskQueueLLMProvider) Name() string { return "private-task-queue-test-provider" }

func (p *privateTaskQueueLLMProvider) Generate(ctx context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.ctx = ctx
	p.observation, _ = llm.ExecutionObservationFromContext(ctx)
	for _, message := range request.Messages {
		if message.Role == "user" {
			p.userMessage = message.Content
			p.userMessages = append(p.userMessages, message.Content)
		}
	}
	if p.onGenerate != nil {
		p.onGenerate()
	}
	if p.failure != nil {
		return llm.GenerateResponse{}, p.failure
	}
	return llm.GenerateResponse{Content: p.response}, nil
}
