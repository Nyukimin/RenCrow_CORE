package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	eventpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/eventstore"
	superagentpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestRuntimeSuperAgentStoreComposesActorQueueCapabilities(t *testing.T) {
	for _, backend := range []string{"jsonl", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			testRuntimeSuperAgentStoreComposesActorQueueCapabilities(t, backend)
		})
	}
}

func testRuntimeSuperAgentStoreComposesActorQueueCapabilities(t *testing.T, backend string) {
	ctx := context.Background()
	owner, queue, task := newTaskEnqueueFixture(t, domaintask.Task{
		OwnerID: "ren", Route: domaintask.RouteCHAT, Assignee: "mio", Priority: domaintask.PriorityHigh,
		Title: "composed private queue task",
	}, domaintask.SharedRoleContext{})
	events, err := eventpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "events.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := events.Close(); err != nil {
			t.Errorf("close event store: %v", err)
		}
	})
	var state runtimeSuperAgentStateStore = queue
	if backend == "sqlite" {
		sqliteStore, err := superagentpersistence.NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.db"), 3000)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := sqliteStore.Close(); err != nil {
				t.Errorf("close superagent store: %v", err)
			}
		})
		state = sqliteStore
	}
	runtimeStore := composeRuntimeSuperAgentStore(state, events)
	for _, capability := range []struct {
		name string
		ok   bool
	}{
		{name: "actor candidate reads", ok: hasRuntimeSuperAgentCapability[runtimeActorCandidateStore](runtimeStore)},
		{name: "exact queue claims", ok: hasRuntimeSuperAgentCapability[runtimeExactQueueLeaseStore](runtimeStore)},
		{name: "legacy queue leases", ok: hasRuntimeSuperAgentCapability[runtimeLegacyQueueLeaseStore](runtimeStore)},
		{name: "Task intent lookup", ok: hasRuntimeSuperAgentCapability[taskEnqueueRunQueueStore](runtimeStore)},
	} {
		if !capability.ok {
			t.Errorf("composed runtime SuperAgent store is missing %s", capability.name)
		}
	}
	queueStore, ok := runtimeStore.(taskEnqueueRunQueueStore)
	if !ok {
		t.Fatal("composed runtime SuperAgent store is missing Task intent lookup")
	}

	handler := &taskEnqueueHandler{
		ownerID: "ren", token: []byte(taskEnqueueTestToken), tasks: owner,
		queue: queueStore, agentSchedulerEnabled: true,
	}
	response := serveTaskEnqueueRequest(handler, "POST", "127.0.0.1:1234", taskEnqueueTestToken, `{"task_id":"`+string(task.TaskID)+`"}`)
	if response.Code != 200 {
		t.Fatalf("enqueue status=%d body=%s", response.Code, response.Body.String())
	}

	var processed modulecore.QueueItemID
	scheduler := appsuperagent.NewRunQueueScheduler(runtimeStore, appsuperagent.RunQueueProcessorFunc(func(ctx context.Context, item domainsuperagent.RunQueueItem, _ modulecore.TraceID) (string, error) {
		processed = item.QueueItemID
		time.Sleep(150 * time.Millisecond)
		if _, err := owner.Succeed(ctx, item.TaskID, "integration complete"); err != nil {
			return "", err
		}
		return "integration complete", nil
	}), owner, appsuperagent.RunQueueSchedulerOptions{
		LeaseDuration: 300 * time.Millisecond,
		Now:           func() time.Time { return time.Now().UTC() },
		LeaseToken:    func() (string, error) { return "composed-runtime-test-lease", nil },
		ActorTaskLister: func(ctx context.Context, actor string, limit int) ([]domaintask.Task, error) {
			return owner.List(ctx, domaintask.Filter{Assignee: actor, Status: domaintask.StatusQueued, Limit: limit})
		},
	})
	planned, canonicalTask, err := scheduler.PlanForActor(ctx, "mio")
	if err != nil || planned == nil || planned.TaskID != task.TaskID || canonicalTask.TaskID != task.TaskID {
		t.Fatalf("PlanForActor()=(%+v,%+v,%v), want enqueued canonical Task", planned, canonicalTask, err)
	}
	ran, err := scheduler.RunPlanned(ctx, planned.QueueItemID, "mio")
	if err != nil || !ran {
		t.Fatalf("RunPlanned(%s)=(%v,%v), want exact queued item run", planned.QueueItemID, ran, err)
	}
	if processed != planned.QueueItemID {
		t.Fatalf("processor received queue item %s, planned %s", processed, planned.QueueItemID)
	}
	items, err := runtimeStore.ListRunQueueItems(ctx, 10)
	if err != nil || len(items) != 1 || items[0].QueueItemID != planned.QueueItemID || items[0].Status != "completed" {
		t.Fatalf("queue after exact dispatch=%#v err=%v, want planned item completed", items, err)
	}
	updatedTask, err := owner.Get(ctx, task.TaskID)
	if err != nil || updatedTask.Status != domaintask.StatusSucceeded {
		t.Fatalf("canonical Task after dispatch=%+v err=%v, want succeeded", updatedTask, err)
	}
	runs, err := owner.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 1 || runs[0].Status != domaintask.RunStatusSucceeded {
		t.Fatalf("canonical Runs after dispatch=%#v err=%v, want one succeeded Run", runs, err)
	}
}

type runtimeActorCandidateStore interface {
	ListRunQueueCandidatesForTasks(context.Context, []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error)
}

type runtimeExactQueueLeaseStore interface {
	ClaimRunQueueItem(context.Context, modulecore.QueueItemID, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	ReleaseRunQueueItem(context.Context, modulecore.QueueItemID, string) (bool, error)
}

type runtimeLegacyQueueLeaseStore interface {
	ClaimNextRunQueueItem(context.Context, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	AttachRunQueueRun(context.Context, string, string, modulecore.RunID) (bool, error)
	RenewRunQueueLease(context.Context, string, string, time.Time) (bool, error)
	CompleteRunQueueItem(context.Context, string, string, string, string, time.Time) (bool, error)
}

func hasRuntimeSuperAgentCapability[T any](value any) bool {
	_, ok := value.(T)
	return ok
}
