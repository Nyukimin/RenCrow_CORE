package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type autonomousAcceptanceWorker struct {
	mu            sync.Mutex
	responses     []string
	inputs        []string
	afterResponse func()
}

func (w *autonomousAcceptanceWorker) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, capabilities []string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if len(capabilities) != 0 {
		return "", errors.New("autonomous test received execution capabilities")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.inputs = append(w.inputs, input.MessageText())
	if len(w.inputs) > len(w.responses) {
		return "", errors.New("unexpected extra provider call")
	}
	if w.afterResponse != nil {
		w.afterResponse()
	}
	return w.responses[len(w.inputs)-1], nil
}

type autonomousAcceptanceFixture struct {
	owner     *taskmanager.Manager
	queue     runtimeSuperAgentStateStore
	service   *autonomousTaskRuntime
	deps      *Dependencies
	scheduler *appsuperagent.RunQueueScheduler
	worker    *autonomousAcceptanceWorker
	clock     atomic.Int64
}

func newAutonomousTaskOwner(t *testing.T, root string) *taskmanager.Manager {
	t.Helper()
	taskStore, err := taskpersistence.NewJSONLStore(filepath.Join(root, "tasks"))
	if err != nil {
		t.Fatal(err)
	}
	owner := taskmanager.New(taskStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close autonomous Task owner: %v", err)
		}
	})
	return owner
}

func installEmptyAgentGameSessionsForSchedulerFixture(deps *Dependencies, owner *taskmanager.Manager) {
	deps.gameTaskAdmission = newGameTaskRuntimeAdmission(currentActivityGameReader{}, owner)
}

func newAutonomousAcceptanceFixture(t *testing.T, responses ...string) *autonomousAcceptanceFixture {
	return newAutonomousAcceptanceFixtureBackend(t, "jsonl", responses...)
}

func newAutonomousAcceptanceFixtureBackend(t *testing.T, backend string, responses ...string) *autonomousAcceptanceFixture {
	t.Helper()
	root := t.TempDir()
	owner := newAutonomousTaskOwner(t, root)
	if _, err := owner.Create(context.Background(), domaintask.Task{
		OwnerID: "ren", Assignee: "outside-fixture", Route: domaintask.RouteCHAT, Title: "isolated acceptance setup",
	}, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("create isolated acceptance setup Task: %v", err)
	}
	var queue runtimeSuperAgentStateStore
	if backend == "sqlite" {
		sqliteQueue, err := superagentpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "queue.sqlite"), 3000)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = sqliteQueue.Close() })
		queue = sqliteQueue
	} else if backend == "jsonl" {
		jsonlQueue := superagentpersistence.NewJSONLStore(filepath.Join(root, "superagent"), 3000)
		queue = jsonlQueue
	} else if backend != "jsonl" {
		t.Fatalf("unknown fixture queue backend %q", backend)
	}
	events, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.Close() })
	store := composeRuntimeSuperAgentStore(queue, events)
	worker := &autonomousAcceptanceWorker{responses: responses}
	workers := map[string]privateTaskWorker{"aka": worker}
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, nil, workers)
	scheduler := appsuperagent.NewRunQueueScheduler(store,
		newSuperAgentRunQueueProcessor(&captureSuperAgentRunQueueProcessor{}, backgroundJobFailureReporter{}, true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{ActorTaskLister: func(ctx context.Context, actor string, limit int) ([]domaintask.Task, error) {
			queued, err := owner.List(ctx, domaintask.Filter{Assignee: actor, Status: domaintask.StatusQueued, Limit: limit})
			if err != nil {
				return nil, err
			}
			waiting, err := owner.List(ctx, domaintask.Filter{Assignee: actor, Status: domaintask.StatusWaiting, Limit: limit})
			return append(queued, waiting...), err
		}},
	)
	deps := &Dependencies{taskManager: owner, superAgentStore: store, superAgentStateStore: queue, agentRunQueue: scheduler}
	installEmptyAgentGameSessionsForSchedulerFixture(deps, owner)
	cfg := &config.Config{WorkspaceDir: t.TempDir(), LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "ren"}, Heartbeat: config.HeartbeatConfig{AgentSchedulerEnabled: true}}
	cfg.Heartbeat.Routine.Enabled = true
	configureAgentHeartbeatScheduler(cfg, deps, workers, nil, runner)
	if runner.autonomous == nil || deps.agentRoutine == nil {
		t.Fatal("production runtime did not connect autonomous private work")
	}
	t.Cleanup(func() { _ = deps.agentRoutine.Close() })
	f := &autonomousAcceptanceFixture{owner: owner, queue: queue, service: runner.autonomous, deps: deps, scheduler: scheduler, worker: worker}
	f.setNow(time.Date(2026, 11, 15, 14, 0, 0, 0, time.FixedZone("Asia/Tokyo", 9*3600)))
	clock := func() time.Time { return time.Unix(0, f.clock.Load()).In(f.service.location()) }
	f.service.now, deps.agentRoutine.now = clock, clock
	return f
}

func (f *autonomousAcceptanceFixture) setNow(now time.Time) { f.clock.Store(now.UnixNano()) }

func (f *autonomousAcceptanceFixture) runNext(t *testing.T) domaintask.Task {
	t.Helper()
	ctx := context.Background()
	item, task, err := f.scheduler.PlanForActor(ctx, "aka")
	if err != nil || item == nil {
		t.Fatalf("immediate private successor missing: item=%+v err=%v", item, err)
	}
	if ran, err := f.scheduler.RunPlannedWithStop(ctx, item.QueueItemID, "aka", make(chan struct{})); err != nil || !ran {
		t.Fatalf("private execution ran=%v err=%v", ran, err)
	}
	saved, err := f.owner.Get(ctx, task.TaskID)
	if err != nil || saved.Status != domaintask.StatusSucceeded {
		t.Fatalf("saved Task=%+v err=%v", saved, err)
	}
	return saved
}

func autonomousContinueJSON(stage int) string {
	return fmt.Sprintf(`{"decision":"continue","reason":"The saved result leaves a distinct useful step.","title":"Architecture note %d","objective":"Explain distinct concern %d","completion":"Write one concrete paragraph for concern %d"}`, stage, stage, stage)
}

func TestAutonomousTasksCreateThreeWorksFromSavedResultsWithoutTimer(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite"} {
		t.Run(backend, func(t *testing.T) { testAutonomousTasksCreateThreeWorksFromSavedResultsWithoutTimer(t, backend) })
	}
}

func testAutonomousTasksCreateThreeWorksFromSavedResultsWithoutTimer(t *testing.T, backend string) {
	f := newAutonomousAcceptanceFixtureBackend(t, backend, autonomousContinueJSON(1), "saved finding one", autonomousContinueJSON(2), "saved finding two", autonomousContinueJSON(3), "saved finding three")
	ctx := context.Background()
	source := f.deps.agentRoutine.idleSource(f.service.idleSource(nil))
	seed, found, err := source(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed found=%v err=%v", found, err)
	}
	moduleID, _ := domaintask.AutonomousModuleID("aka")
	before, err := f.owner.List(ctx, domaintask.Filter{ModuleID: moduleID, Limit: 10})
	if err != nil || len(before) != 0 {
		t.Fatalf("read-only discovery wrote Tasks: %+v %v", before, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatalf("duplicate seed admission: %v", err)
	}
	for step := 0; step < 6; step++ {
		saved := f.runNext(t)
		if step%2 == 1 && (saved.Autonomous.Kind != domaintask.AutonomousTaskKindWork || saved.Autonomous.Depth != (step+1)/2) {
			t.Fatalf("work has wrong lineage: %+v", saved)
		}
	}
	items, err := f.queue.ListRunQueueItems(ctx, 20)
	if err != nil || len(items) != 6 {
		t.Fatalf("durable queue=%+v err=%v", items, err)
	}
	for _, item := range items {
		if item.Status != "completed" {
			t.Fatalf("unfinished queue item: %+v", item)
		}
	}
	if item, _, err := f.scheduler.PlanForActor(ctx, "aka"); err != nil || item != nil {
		t.Fatalf("chain exceeded depth cap: %+v %v", item, err)
	}
	if _, found, err := f.service.idleSource(nil)(ctx, "aka"); err != nil || found {
		t.Fatalf("same-day seed repeated: found=%v err=%v", found, err)
	}
	f.worker.mu.Lock()
	defer f.worker.mu.Unlock()
	if len(f.worker.inputs) != 6 || !strings.Contains(f.worker.inputs[2], "saved finding one") || !strings.Contains(f.worker.inputs[4], "saved finding two") {
		t.Fatalf("evaluation did not use saved results: %q", f.worker.inputs)
	}
	if !strings.Contains(f.worker.inputs[1], "Explain distinct concern 1") || !strings.Contains(f.worker.inputs[1], "Write one concrete paragraph for concern 1") {
		t.Fatalf("concrete work lost purpose/completion: %q", f.worker.inputs[1])
	}
}

type autonomousFailQueueOnce struct {
	taskEnqueueRunQueueStore
	failed bool
}

func (s *autonomousFailQueueOnce) InsertRunQueueItemIfAbsent(ctx context.Context, item domainsuperagent.RunQueueItem) (domainsuperagent.RunQueueItem, bool, error) {
	if !s.failed {
		s.failed = true
		return domainsuperagent.RunQueueItem{}, false, errors.New("injected queue insert outage")
	}
	return s.taskEnqueueRunQueueStore.InsertRunQueueItemIfAbsent(ctx, item)
}

func TestAutonomousTasksRecoverAdmittedPlanAfterQueueInsertFailure(t *testing.T) {
	f := newAutonomousAcceptanceFixture(t, `{"decision":"stop","reason":"No useful additional work."}`)
	ctx := context.Background()
	f.service.queue = &autonomousFailQueueOnce{taskEnqueueRunQueueStore: f.queue}
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed=%v %v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err == nil {
		t.Fatal("queue failure was hidden")
	}
	rootID, _ := domaintask.AutonomousPlanTaskID("ren", "aka", "2026-11-15", "")
	root, err := f.owner.Get(ctx, rootID)
	if err != nil || root.Status != domaintask.StatusQueued {
		t.Fatalf("admitted root not preserved: %+v %v", root, err)
	}
	repair, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found || !strings.Contains(repair.ID, "reconcile") {
		t.Fatalf("missing registration not recovered: work=%+v found=%v err=%v", repair, found, err)
	}
	if err := repair.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	f.runNext(t)
	items, err := f.queue.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 || items[0].TaskID != rootID || items[0].Status != "completed" {
		t.Fatalf("recovery duplicated Task/intent: %+v %v", items, err)
	}
}

func TestAutonomousTasksNightGateAllowsOrdinaryTaskBehindGeneratedTask(t *testing.T) {
	f := newAutonomousAcceptanceFixture(t, "ordinary task result")
	ctx := context.Background()
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed=%v %v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	ordinary, err := f.owner.Create(ctx, domaintask.Task{Title: "ordinary user task", OwnerID: "ren", Assignee: "aka", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = f.queue.InsertRunQueueItemIfAbsent(ctx, domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: ordinary.TaskID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: domainsuperagent.RunQueueGoalPrivateFirstChat, Action: "chat", Status: "queued", Priority: 1,
		IdempotencyKey: domainsuperagent.RunQueueIntentPrivateFirstChat, CreatedAt: ordinary.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.setNow(time.Date(2026, 11, 15, 23, 0, 0, 0, f.service.location()))
	lifetime, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- f.deps.agentHeartbeat.Start(lifetime) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("heartbeat did not stop after cancellation")
		}
		waitCtx, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		if err := f.deps.agentHeartbeat.Wait(waitCtx); err != nil {
			t.Error(err)
		}
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		saved, err := f.owner.Get(ctx, ordinary.TaskID)
		if err != nil {
			t.Fatal(err)
		}
		if saved.Status == domaintask.StatusSucceeded {
			rootID, _ := domaintask.AutonomousPlanTaskID("ren", "aka", "2026-11-15", "")
			root, err := f.owner.Get(ctx, rootID)
			if err != nil || root.Status != domaintask.StatusQueued {
				t.Fatalf("night started generated work: %+v %v", root, err)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("night-gated generated item blocked an ordinary user Task")
}

func TestAutonomousTasksRecoverActiveRootOlderThanRecentHistory(t *testing.T) {
	f := newAutonomousAcceptanceFixture(t)
	ctx := context.Background()
	f.service.queue = &autonomousFailQueueOnce{taskEnqueueRunQueueStore: f.queue}
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed=%v %v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err == nil {
		t.Fatal("queue outage was hidden")
	}
	oldID, _ := domaintask.AutonomousPlanTaskID("ren", "aka", "2026-11-15", "")
	moduleID, _ := domaintask.AutonomousModuleID("aka")
	start := time.Date(2026, 11, 16, 14, 0, 0, 0, f.service.location())
	for n := 0; n < autonomousTaskListLimit+1; n++ {
		date := start.AddDate(0, 0, n).Format("2006-01-02")
		id, err := domaintask.AutonomousPlanTaskID("ren", "aka", date, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = f.owner.AdmitAutonomousTask(ctx, domaintask.Task{
			TaskID: id, Title: "closed historical plan", ModuleID: moduleID, OwnerID: "ren", Assignee: "aka",
			Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, ReadOnly: true,
			Autonomous: &domaintask.AutonomousTask{PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan, Date: date, RootTaskID: id},
		}, domaintask.SharedRoleContext{UserIntent: "fixture planning instruction", CurrentPlan: "fixture decision"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.owner.Cancel(ctx, id, "historical fixture closed"); err != nil {
			t.Fatal(err)
		}
	}
	work, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found || !strings.Contains(work.ID, string(oldID)) {
		t.Fatalf("old active root was lost behind recent history: work=%+v found=%v err=%v", work, found, err)
	}
	if err := work.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	items, err := f.queue.ListRunQueueItems(ctx, 5)
	if err != nil || len(items) != 1 || items[0].TaskID != oldID {
		t.Fatalf("old pending registration was not repaired exactly once: %+v %v", items, err)
	}
}

func TestAutonomousTasksIsolateOtherOwnersHistoryAndActiveRoots(t *testing.T) {
	f := newAutonomousAcceptanceFixture(t)
	ctx := context.Background()
	moduleID, _ := domaintask.AutonomousModuleID("aka")
	otherID, _ := domaintask.AutonomousPlanTaskID("other-user", "aka", "2026-11-15", "")
	plan, err := f.owner.AdmitAutonomousTask(ctx, domaintask.Task{
		TaskID: otherID, Title: "other owner's plan", ModuleID: moduleID, OwnerID: "other-user", Assignee: "aka", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan, Date: "2026-11-15", RootTaskID: otherID},
	}, domaintask.SharedRoleContext{UserIntent: "Other owner's private prompt", CurrentPlan: "Create other private work"})
	if err != nil {
		t.Fatal(err)
	}
	workID, _ := domaintask.AutonomousWorkTaskID(otherID)
	decision := domaintask.AutonomousDecision{Decision: domaintask.AutonomousDecisionContinue, Reason: "Private planning", Title: "Other private work", Objective: "Other owner's objective", Completion: "Other owner's condition", CandidateTaskID: workID}
	run, err := f.owner.StartPrivateTextRun(ctx, plan.TaskID, modulecore.NewQueueItemID(), "aka", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.owner.SavePrivateTextOutput(ctx, plan.TaskID, run.RunID, "aka", autonomousContinueJSON(1)); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.SaveAutonomousDecision(ctx, plan.TaskID, run.RunID, "aka", decision); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.CompletePrivateTextRun(ctx, plan.TaskID, run.RunID, "aka", false); err != nil {
		t.Fatal(err)
	}
	work, err := f.owner.AdmitAutonomousTask(ctx, domaintask.Task{
		TaskID: workID, Title: decision.Title, ModuleID: moduleID, OwnerID: "other-user", Assignee: "aka", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindWork, Date: "2026-11-15", RootTaskID: otherID, SourceTaskID: otherID, SourceRunID: run.RunID, Depth: 1},
	}, domaintask.SharedRoleContext{UserIntent: decision.Objective, CurrentPlan: decision.Completion})
	if err != nil {
		t.Fatal(err)
	}
	workRun, err := f.owner.StartPrivateTextRun(ctx, work.TaskID, modulecore.NewQueueItemID(), "aka", "", "")
	if err != nil {
		t.Fatal(err)
	}
	const secret = "OTHER_OWNER_PRIVATE_RESULT_MUST_NOT_RECALL"
	if err := f.owner.SavePrivateTextOutput(ctx, work.TaskID, workRun.RunID, "aka", secret); err != nil {
		t.Fatal(err)
	}
	if err := f.owner.CompletePrivateTextRun(ctx, work.TaskID, workRun.RunID, "aka", false); err != nil {
		t.Fatal(err)
	}
	queuedID, _ := domaintask.AutonomousPlanTaskID("other-user", "aka", "2026-11-16", "")
	otherQueued, err := f.owner.AdmitAutonomousTask(ctx, domaintask.Task{
		TaskID: queuedID, Title: "other owner's active plan", ModuleID: moduleID, OwnerID: "other-user", Assignee: "aka", Route: domaintask.RouteCHAT, Priority: domaintask.PriorityLow, ReadOnly: true,
		Autonomous: &domaintask.AutonomousTask{PolicyVersion: domaintask.AutonomousPolicyVersion, Kind: domaintask.AutonomousTaskKindPlan, Date: "2026-11-16", RootTaskID: queuedID},
	}, domaintask.SharedRoleContext{UserIntent: "Other owner's active prompt", CurrentPlan: "Other decision"})
	if err != nil {
		t.Fatal(err)
	}
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found || !strings.HasPrefix(seed.ID, "autonomous-seed:") {
		t.Fatalf("another owner changed current owner's intake: work=%+v found=%v err=%v", seed, found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	currentID, _ := domaintask.AutonomousPlanTaskID("ren", "aka", "2026-11-15", "")
	shared, err := f.owner.Context(ctx, currentID)
	if err != nil || strings.Contains(shared.UserIntent, secret) || strings.Contains(shared.UserIntent, decision.Title) {
		t.Fatalf("another owner's private history entered prompt: %+v err=%v", shared, err)
	}
	if err := f.service.ensureQueued(ctx, otherQueued); err == nil {
		t.Fatal("current owner runtime accepted another owner's queue registration")
	}
}

func TestAutonomousTasksDoNotAdmitPlanWithoutActorWorker(t *testing.T) {
	f := newAutonomousAcceptanceFixture(t)
	if _, found, err := f.service.idleSource(nil)(context.Background(), "mio"); err != nil || found {
		t.Fatalf("unavailable actor worker produced a plan: found=%v err=%v", found, err)
	}
	moduleID, _ := domaintask.AutonomousModuleID("mio")
	tasks, err := f.owner.List(context.Background(), domaintask.Filter{ModuleID: moduleID, Limit: 10})
	if err != nil || len(tasks) != 0 {
		t.Fatalf("unavailable actor worker produced canonical Tasks: %+v %v", tasks, err)
	}
	fallback := func(context.Context, string) (agentheartbeat.Work, bool, error) {
		return agentheartbeat.Work{ID: "existing-daytime-game"}, true, nil
	}
	work, found, err := f.service.idleSource(fallback)(context.Background(), "mio")
	if err != nil || !found || work.ID != "existing-daytime-game" {
		t.Fatalf("unavailable autonomous worker blocked existing daytime fallback: %+v found=%v err=%v", work, found, err)
	}
}

func TestAutonomousTasksStopNormalizedAncestorCandidate(t *testing.T) {
	f := newAutonomousAcceptanceFixture(t, autonomousContinueJSON(1), "saved finding one", `{"decision":"continue","reason":"Repeat proposal","title":"architecture NOTE  1","objective":" EXPLAIN  distinct  concern 1 ","completion":"Another paragraph"}`)
	ctx := context.Background()
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed=%v %v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	f.runNext(t)
	f.runNext(t)
	evaluation := f.runNext(t)
	shared, err := f.owner.Context(ctx, evaluation.TaskID)
	if err != nil || shared.AutonomousDecision == nil || shared.AutonomousDecision.Decision != domaintask.AutonomousDecisionStop {
		t.Fatalf("repeated ancestor candidate did not stop: %+v %v", shared, err)
	}
	repeatedID, _ := domaintask.AutonomousWorkTaskID(evaluation.TaskID)
	if _, err := f.owner.Get(ctx, repeatedID); !errors.Is(err, taskmanager.ErrNotFound) {
		t.Fatalf("repeated candidate was admitted: %v", err)
	}
}
