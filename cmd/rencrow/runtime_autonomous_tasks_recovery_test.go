package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var errInjectedAutonomousCompleteFailure = errors.New("injected autonomous completion persistence failure")

type autonomousCompleteFaultState struct {
	sync.Mutex
	armed             bool
	failuresRemaining int
	armOnOutput       bool
}

type autonomousCompleteFaultStore struct {
	domaintask.Store
	state *autonomousCompleteFaultState
}

func (s *autonomousCompleteFaultStore) TaskTransaction(ctx context.Context, taskID modulecore.TaskID, fn func(domaintask.Store) error) error {
	s.state.Lock()
	if s.state.armed && s.state.failuresRemaining > 0 {
		s.state.failuresRemaining--
		s.state.Unlock()
		return errInjectedAutonomousCompleteFailure
	}
	s.state.Unlock()
	return s.Store.TaskTransaction(ctx, taskID, func(tx domaintask.Store) error {
		return fn(&autonomousCompleteFaultView{Store: tx, state: s.state})
	})
}

type autonomousCompleteFaultView struct {
	domaintask.Store
	state *autonomousCompleteFaultState
}

func (s *autonomousCompleteFaultView) SaveContext(ctx context.Context, shared domaintask.SharedRoleContext) error {
	if err := s.Store.SaveContext(ctx, shared); err != nil {
		return err
	}
	s.state.Lock()
	if shared.AutonomousDecision != nil || (s.state.armOnOutput && shared.PrivateText != nil && shared.PrivateText.Output != "") {
		s.state.armed = true
	}
	s.state.Unlock()
	return nil
}

func TestAutonomousPlanStopAfterSavedOutputSurvivesPauseWriteFailure(t *testing.T) {
	f := newAutonomousCompleteFaultFixture(t, 1, autonomousContinueJSON(1))
	f.state.Lock()
	f.state.armOnOutput = true
	f.state.Unlock()
	stop := make(chan struct{})
	f.worker.afterResponse = func() { close(stop) }
	ctx := context.Background()
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed found=%v err=%v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	item, task, err := f.scheduler.PlanForActor(ctx, "aka")
	if err != nil || item == nil {
		t.Fatalf("plan item=%+v err=%v", item, err)
	}
	if ran, err := f.scheduler.RunPlannedWithStop(ctx, item.QueueItemID, "aka", stop); !ran || err == nil {
		t.Fatalf("stop after output with injected pause failure ran=%v err=%v", ran, err)
	}
	running, err := f.owner.Get(ctx, task.TaskID)
	if err != nil || running.Status != domaintask.StatusRunning {
		t.Fatalf("pause persistence failure changed canonical Task: %+v err=%v", running, err)
	}
	checkpoint, found, err := f.owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
	if err != nil || !found || checkpoint.Output == "" {
		t.Fatalf("pause persistence failure lost saved provider output: checkpoint=%+v found=%v err=%v", checkpoint, found, err)
	}
	shared, err := f.owner.Context(ctx, task.TaskID)
	if err != nil || shared.AutonomousDecision != nil {
		t.Fatalf("stop-after-output persisted a new plan decision: %+v err=%v", shared.AutonomousDecision, err)
	}
	items, err := f.queue.ListRunQueueItems(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, queued := range items {
		if queued.TaskID == task.TaskID && queued.Status == "failed" {
			t.Fatalf("pause persistence failure terminalized the queue item: %+v", queued)
		}
	}
	f.worker.mu.Lock()
	defer f.worker.mu.Unlock()
	if len(f.worker.inputs) != 1 {
		t.Fatalf("stop-after-output caused unexpected provider calls: %q", f.worker.inputs)
	}
}

func TestAutonomousPlanCompletionFailurePausesAndResumesSavedDecision(t *testing.T) {
	f := newAutonomousCompleteFaultFixture(t, 1, autonomousContinueJSON(1))
	ctx := context.Background()
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed found=%v err=%v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}

	first, firstTask, err := f.scheduler.PlanForActor(ctx, "aka")
	if err != nil || first == nil {
		t.Fatalf("first plan item=%+v err=%v", first, err)
	}
	if ran, err := f.scheduler.RunPlannedWithStop(ctx, first.QueueItemID, "aka", make(chan struct{})); err != nil || !ran {
		t.Fatalf("first plan run ran=%v err=%v", ran, err)
	}
	waiting, err := f.owner.Get(ctx, firstTask.TaskID)
	if err != nil || waiting.Status != domaintask.StatusWaiting {
		t.Fatalf("completion write failure did not preserve a resumable plan: Task=%+v err=%v", waiting, err)
	}
	shared, err := f.owner.Context(ctx, firstTask.TaskID)
	checkpoint, found, checkpointErr := f.owner.GetPrivateTextCheckpoint(ctx, firstTask.TaskID)
	if err != nil || shared.AutonomousDecision == nil || checkpointErr != nil || !found || checkpoint.Output == "" {
		t.Fatalf("saved output/decision lost after completion failure: context=%+v checkpoint=%+v found=%v errs=(%v,%v)", shared, checkpoint, found, err, checkpointErr)
	}

	resume, resumeTask, err := f.scheduler.PlanForActor(ctx, "aka")
	if err != nil || resume == nil || resumeTask.TaskID != firstTask.TaskID {
		t.Fatalf("checkpoint resume item=%+v task=%+v err=%v", resume, resumeTask, err)
	}
	if ran, err := f.scheduler.RunPlannedWithStop(ctx, resume.QueueItemID, "aka", make(chan struct{})); err != nil || !ran {
		t.Fatalf("checkpoint resume ran=%v err=%v", ran, err)
	}
	succeeded, err := f.owner.Get(ctx, firstTask.TaskID)
	if err != nil || succeeded.Status != domaintask.StatusSucceeded {
		t.Fatalf("resumed plan Task=%+v err=%v", succeeded, err)
	}
	childID, err := domaintask.AutonomousWorkTaskID(firstTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.owner.Get(ctx, childID)
	if err != nil || child.Status != domaintask.StatusQueued {
		t.Fatalf("saved decision did not admit one work candidate: %+v err=%v", child, err)
	}
	f.worker.mu.Lock()
	defer f.worker.mu.Unlock()
	if len(f.worker.inputs) != 1 {
		t.Fatalf("checkpoint resume invoked provider again: %q", f.worker.inputs)
	}
}

func TestAutonomousPlanDoubleCompletionFailureRecoversAfterTaskStoreReopen(t *testing.T) {
	f := newAutonomousCompleteFaultFixture(t, 2, autonomousContinueJSON(1))
	ctx := context.Background()
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed found=%v err=%v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	first, firstTask, err := f.scheduler.PlanForActor(ctx, "aka")
	if err != nil || first == nil {
		t.Fatalf("first plan item=%+v err=%v", first, err)
	}
	if ran, err := f.scheduler.RunPlannedWithStop(ctx, first.QueueItemID, "aka", make(chan struct{})); !ran || err == nil {
		t.Fatalf("double completion failure ran=%v err=%v", ran, err)
	}
	running, err := f.owner.Get(ctx, firstTask.TaskID)
	if err != nil || running.Status != domaintask.StatusRunning {
		t.Fatalf("double failure terminalized the Task: %+v err=%v", running, err)
	}
	shared, err := f.owner.Context(ctx, firstTask.TaskID)
	checkpoint, checkpointFound, checkpointErr := f.owner.GetPrivateTextCheckpoint(ctx, firstTask.TaskID)
	if err != nil || shared.AutonomousDecision == nil || checkpointErr != nil || !checkpointFound || checkpoint.Output == "" {
		t.Fatalf("double failure lost saved output or decision: context=%+v checkpoint=%+v found=%v errs=(%v,%v)", shared, checkpoint, checkpointFound, err, checkpointErr)
	}
	queueItems, err := f.queue.ListRunQueueItems(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range queueItems {
		if item.TaskID == firstTask.TaskID && item.Status == "failed" {
			t.Fatalf("double failure terminalized queue receipt: %+v", item)
		}
	}

	if err := f.closeInitial(); err != nil {
		t.Fatalf("close old Task Manager/store generation: %v", err)
	}
	base, err := taskpersistence.NewJSONLStore(f.taskPath)
	if err != nil {
		t.Fatalf("reopen Task store: %v", err)
	}
	owner := taskmanager.New(base, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close reopened Task Manager: %v", err)
		}
		if err := base.Close(); err != nil {
			t.Errorf("close reopened Task store: %v", err)
		}
	})
	f.owner = owner
	f.service.tasks = owner
	f.service.setWaker(autonomousNoopWaker{})
	recovery, err := appsuperagent.RecoverPrivateTextRunQueueReceipts(ctx, f.queue, owner, time.Now().Add(10*time.Minute))
	if err != nil || recovery.RecoveredRuns == 0 || recovery.QueuedResumes == 0 {
		t.Fatalf("startup private checkpoint recovery=%+v err=%v", recovery, err)
	}
	queueItems, err = f.queue.ListRunQueueItems(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	var resume *domainsuperagent.RunQueueItem
	for i := range queueItems {
		if domainsuperagent.IsPrivateTextResumeQueueItem(queueItems[i]) && queueItems[i].Status == "queued" {
			item := queueItems[i]
			resume = &item
			break
		}
	}
	if resume == nil {
		t.Fatalf("startup recovery did not create a queued checkpoint resume: %+v", queueItems)
	}
	run, err := owner.StartPrivateTextRun(ctx, resume.TaskID, resume.QueueItemID, "aka", resume.PredecessorRunID, resume.CheckpointDigest)
	if err != nil {
		t.Fatalf("start recovered private checkpoint Run: %v", err)
	}
	resume.RunID, resume.AttemptCount = run.RunID, 1
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, nil, map[string]privateTaskWorker{"aka": f.worker})
	runner.autonomous = f.service
	if _, err := runner.process(ctx, *resume, modulecore.NewTraceID()); err != nil {
		t.Fatalf("execute recovered saved-output checkpoint: %v", err)
	}
	succeeded, err := owner.Get(ctx, firstTask.TaskID)
	if err != nil || succeeded.Status != domaintask.StatusSucceeded {
		t.Fatalf("reopened canonical plan Task=%+v err=%v", succeeded, err)
	}
	childID, err := domaintask.AutonomousWorkTaskID(firstTask.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := owner.Get(ctx, childID)
	if err != nil || child.Status != domaintask.StatusQueued {
		t.Fatalf("reopened plan did not register one work candidate: %+v err=%v", child, err)
	}
	f.worker.mu.Lock()
	defer f.worker.mu.Unlock()
	if len(f.worker.inputs) != 1 {
		t.Fatalf("startup checkpoint recovery called provider again: %q", f.worker.inputs)
	}
}

type autonomousNoopWaker struct{}

func (autonomousNoopWaker) Wake(string) error { return nil }

type autonomousFaultAcceptanceFixture struct {
	*autonomousAcceptanceFixture
	base         *taskpersistence.JSONLStore
	taskPath     string
	closeInitial func() error
	state        *autonomousCompleteFaultState
}

func newAutonomousCompleteFaultFixture(t *testing.T, failureCount int, responses ...string) *autonomousFaultAcceptanceFixture {
	t.Helper()
	root := t.TempDir()
	taskPath := filepath.Join(root, "tasks")
	base, err := taskpersistence.NewJSONLStore(taskPath)
	if err != nil {
		t.Fatal(err)
	}
	state := &autonomousCompleteFaultState{failuresRemaining: failureCount}
	owner := taskmanager.New(&autonomousCompleteFaultStore{Store: base, state: state}, taskmanager.DefaultParallelLimits())
	initialClosed := false
	closeInitial := func() error {
		if initialClosed {
			return nil
		}
		initialClosed = true
		return errors.Join(owner.Close(), base.Close())
	}
	t.Cleanup(func() {
		if err := closeInitial(); err != nil {
			t.Errorf("close fault-injected Task Manager/store: %v", err)
		}
	})
	queue := superagentpersistence.NewJSONLStore(filepath.Join(root, "superagent"), 3000)
	events, err := eventstore.NewSQLiteStore(filepath.Join(root, "events.sqlite"))
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
	return &autonomousFaultAcceptanceFixture{autonomousAcceptanceFixture: f, base: base, taskPath: taskPath, closeInitial: closeInitial, state: state}
}

func TestAutonomousPlanReplaysSavedCheckpointWithoutProviderCall(t *testing.T) {
	decision := autonomousContinueJSON(1)
	f := newAutonomousAcceptanceFixture(t)
	ctx := context.Background()
	seed, found, err := f.service.idleSource(nil)(ctx, "aka")
	if err != nil || !found {
		t.Fatalf("seed found=%v err=%v", found, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}

	rootID, err := domaintask.AutonomousPlanTaskID("ren", "aka", "2026-11-15", "")
	if err != nil {
		t.Fatal(err)
	}
	items, err := f.queue.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 || items[0].TaskID != rootID {
		t.Fatalf("root queue item=%+v err=%v", items, err)
	}
	item := items[0]
	run, err := f.owner.StartPrivateTextRun(ctx, rootID, item.QueueItemID, "aka", "", "")
	if err != nil {
		t.Fatalf("StartPrivateTextRun: %v", err)
	}
	if err := f.owner.SavePrivateTextOutput(ctx, rootID, run.RunID, "aka", decision); err != nil {
		t.Fatalf("persist provider output before simulated restart: %v", err)
	}

	item.RunID = run.RunID
	item.AttemptCount = 1
	runner := newPrivateTaskRunQueueRunner(f.owner, "ren", nil, nil, nil, map[string]privateTaskWorker{"aka": f.worker})
	runner.autonomous = f.service
	if _, err := runner.process(ctx, item, modulecore.NewTraceID()); err != nil {
		t.Fatalf("resume from exact saved-output checkpoint: %v", err)
	}

	saved, err := f.owner.Get(ctx, rootID)
	if err != nil || saved.Status != domaintask.StatusSucceeded {
		t.Fatalf("replayed plan Task=%+v err=%v", saved, err)
	}
	shared, err := f.owner.Context(ctx, rootID)
	if err != nil || shared.AutonomousDecision == nil || shared.AutonomousDecision.Decision != domaintask.AutonomousDecisionContinue {
		t.Fatalf("saved plan decision=%+v err=%v", shared.AutonomousDecision, err)
	}
	childID, err := domaintask.AutonomousWorkTaskID(rootID)
	if err != nil {
		t.Fatal(err)
	}
	child, err := f.owner.Get(ctx, childID)
	if err != nil || child.Autonomous == nil || child.Status != domaintask.StatusQueued {
		t.Fatalf("recovered concrete work Task=%+v err=%v", child, err)
	}
	f.worker.mu.Lock()
	defer f.worker.mu.Unlock()
	if len(f.worker.inputs) != 0 {
		t.Fatalf("saved checkpoint replay invoked provider: %q", f.worker.inputs)
	}
}
