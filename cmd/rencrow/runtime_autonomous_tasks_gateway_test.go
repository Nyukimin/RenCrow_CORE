package main

import (
	"context"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// This opt-in test uses the actual Gateway recipient and private runner, while
// keeping every Task, Run, queue and routine record in isolated temporary stores.
func TestAutonomousTasksConfiguredGatewayCreatesExecutesAndEvaluates(t *testing.T) {
	gatewayURL := strings.TrimSpace(os.Getenv("TEST_RENCROW_GATEWAY_URL"))
	if gatewayURL == "" {
		t.Skip("set TEST_RENCROW_GATEWAY_URL to enable the local Gateway acceptance")
	}
	u, err := url.Parse(gatewayURL)
	if err != nil || u.Scheme != "http" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() {
		t.Fatal("TEST_RENCROW_GATEWAY_URL must be an explicit loopback HTTP URL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	owner, queue, _ := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Assignee: "outside-fixture", Route: domaintask.RouteCHAT, Title: "isolated acceptance setup",
	}, domaintask.SharedRoleContext{})
	events, err := eventstore.NewSQLiteStore(filepath.Join(t.TempDir(), "events.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.Close() })
	store := composeRuntimeSuperAgentStore(queue, events)
	cfg := &config.Config{
		WorkspaceDir:  t.TempDir(),
		LLMGateway:    config.LLMGatewayConfig{Enabled: true, BaseURL: gatewayURL, TimeoutSec: 45},
		LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, UserID: "ren"},
		Heartbeat:     config.HeartbeatConfig{AgentSchedulerEnabled: true},
	}
	cfg.Heartbeat.Routine.Enabled = true
	primary := buildPrimaryLLMProviders(cfg, nil)
	workers := newPrivateTextWorkers(primary.PrivateTextProviders, map[string]string{
		"aka": "You are Aka, an architecture thinker. Your current interest is comparing error-handling strategies. When asked to choose the initial autonomous task, choose continue and invent one useful, small, specific text task yourself. When evaluating a saved result, stop once its completion condition has been met. Follow the requested JSON contract exactly for decisions. For ordinary text work, produce a concise useful result. Write reason and task content in Japanese.",
	})
	if workers["aka"] == nil {
		t.Fatal("configured Gateway did not supply the actual Aka private worker")
	}
	workers["aka"] = autonomousObservedGatewayWorker{inner: workers["aka"], t: t}
	runner := newPrivateTaskRunQueueRunner(owner, "ren", nil, nil, nil, workers)
	scheduler := appsuperagent.NewRunQueueScheduler(store,
		newSuperAgentRunQueueProcessor(&captureSuperAgentRunQueueProcessor{}, backgroundJobFailureReporter{}, true, runner), owner,
		appsuperagent.RunQueueSchedulerOptions{
			ActorTaskLister: func(ctx context.Context, actor string, limit int) ([]domaintask.Task, error) {
				return owner.List(ctx, domaintask.Filter{Assignee: actor, Status: domaintask.StatusQueued, Limit: limit})
			},
		},
	)
	deps := &Dependencies{taskManager: owner, superAgentStore: store, superAgentStateStore: queue, agentRunQueue: scheduler}
	configureAgentHeartbeatScheduler(cfg, deps, workers, nil, runner)
	if runner.autonomous == nil || deps.agentRoutine == nil {
		t.Fatal("production constructor did not connect autonomous tasks to the private runner and routine")
	}
	t.Cleanup(func() { _ = deps.agentRoutine.Close() })
	daytime := time.Date(2026, time.November, 15, 14, 0, 0, 0, time.FixedZone("Asia/Tokyo", 9*3600))
	runner.autonomous.now = func() time.Time { return daytime }
	deps.agentRoutine.now = func() time.Time { return daytime }
	source := deps.agentRoutine.idleSource(runner.autonomous.idleSource(nil))
	seed, found, err := source(ctx, "aka")
	if err != nil || !found || seed.Run == nil {
		t.Fatalf("daytime seed found=%v work=%+v err=%v", found, seed, err)
	}
	if err := seed.Run(ctx, make(chan struct{})); err != nil {
		t.Fatal(err)
	}
	rootID, err := domaintask.AutonomousPlanTaskID("ren", "aka", "2026-11-15", "")
	if err != nil {
		t.Fatal(err)
	}
	workCount, planCount := 0, 0
	for step := 0; step < 2*domaintask.AutonomousMaxDepth; step++ {
		item, task, err := scheduler.PlanForActor(ctx, "aka")
		if err != nil || item == nil {
			t.Fatalf("step %d immediate successor absent: item=%+v err=%v", step, item, err)
		}
		ran, err := scheduler.RunPlannedWithStop(ctx, item.QueueItemID, "aka", make(chan struct{}))
		if err != nil || !ran {
			t.Fatalf("step %d private execution ran=%v err=%v", step, ran, err)
		}
		saved, err := owner.Get(ctx, task.TaskID)
		if err != nil || saved.Status != domaintask.StatusSucceeded {
			t.Fatalf("step %d canonical result=%+v err=%v", step, saved, err)
		}
		checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
		if err != nil || !found || strings.TrimSpace(checkpoint.Output) == "" {
			t.Fatalf("step %d persisted output found=%v err=%v", step, found, err)
		}
		if saved.Autonomous.Kind == domaintask.AutonomousTaskKindWork {
			workCount++
			t.Logf("work task=%s depth=%d title=%q result=%q", saved.TaskID, saved.Autonomous.Depth, saved.Title, truncateUTF8(checkpoint.Output, 600))
		} else {
			planCount++
			shared, err := owner.Context(ctx, task.TaskID)
			if err != nil || shared.AutonomousDecision == nil {
				t.Fatalf("step %d canonical decision absent: %+v %v", step, shared, err)
			}
			t.Logf("plan task=%s depth=%d decision=%+v", saved.TaskID, saved.Autonomous.Depth, *shared.AutonomousDecision)
		}
		settled, err := runner.autonomous.rootSettled(ctx, rootID)
		if err != nil {
			t.Fatal(err)
		}
		if settled {
			break
		}
	}
	if workCount < 1 || workCount > domaintask.AutonomousMaxDepth || planCount < 2 {
		t.Fatalf("actual Gateway sequence works=%d plans=%d; want generation, execution and saved-result evaluation", workCount, planCount)
	}
	if err := runner.autonomous.reconcileRoot(ctx, "aka", rootID); err != nil {
		t.Fatalf("completed chain replay: %v", err)
	}
	if _, found, err := runner.autonomous.idleSource(nil)(ctx, "aka"); err != nil || found {
		t.Fatalf("same-day duplicate seed found=%v err=%v", found, err)
	}
	items, err := queue.ListRunQueueItems(ctx, 20)
	if err != nil || len(items) != workCount+planCount {
		t.Fatalf("canonical queue count=%d works=%d plans=%d err=%v", len(items), workCount, planCount, err)
	}
	for _, item := range items {
		if item.Status != "completed" || item.RunID == modulecore.RunID("") {
			t.Fatalf("nonterminal actual Gateway queue item: %+v", item)
		}
	}
}

type autonomousObservedGatewayWorker struct {
	inner privateTaskWorker
	t     *testing.T
}

func (w autonomousObservedGatewayWorker) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, capabilities []string) (string, error) {
	output, err := w.inner.ExecutePrivateWork(ctx, input, capabilities)
	w.t.Logf("actual Gateway task=%s output=%q error=%v", input.RootTaskID(), truncateUTF8(output, 16<<10), err)
	return output, err
}
