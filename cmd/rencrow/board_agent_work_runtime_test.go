package main

import (
	"context"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBoardAgentWorkRuntimeReaderProjectsAllAssignedWorkInCanonicalActorOrder(t *testing.T) {
	now := time.Date(2026, 11, 15, 4, 0, 0, 0, time.UTC)
	queued := boardAgentWorkTestTask("mio queued high", "mio", domaintask.StatusQueued, domaintask.PriorityHigh, now.Add(-3*time.Hour))
	queued.Summary = "secret summary must not cross projection"
	blocked := boardAgentWorkTestTask("mio blocked", "mio", domaintask.StatusBlocked, domaintask.PriorityNormal, now.Add(-2*time.Hour))
	running := boardAgentWorkTestTask("mio running", "mio", domaintask.StatusRunning, domaintask.PriorityNormal, now.Add(-time.Hour))
	running.StartedAt = ptrTime(now.Add(-20 * time.Minute))
	ownerOnly := boardAgentWorkTestTask("owner only", "", domaintask.StatusQueued, domaintask.PriorityCritical, now.Add(-4*time.Hour))
	ownerOnly.OwnerID = "mio"
	otherActor := boardAgentWorkTestTask("shiro queued", "shiro", domaintask.StatusQueued, domaintask.PriorityLow, now.Add(-time.Hour))
	waiting := boardAgentWorkTestTask("mio waiting", "mio", domaintask.StatusWaiting, domaintask.PriorityLow, now.Add(-30*time.Minute))
	waiting.WaitingReason = "private wait reason must not cross projection"
	queueItem := domainsuperagent.RunQueueItem{
		QueueItemID: modulecore.NewQueueItemID(), TaskID: queued.TaskID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: "private queue goal must not cross projection", Action: "chat", Status: "queued", NotBefore: now.Add(time.Hour), CreatedAt: now,
	}
	reader := &boardAgentWorkRuntimeReader{
		tasks: boardAgentWorkTaskOwnerStub{tasks: []domaintask.Task{ownerOnly, otherActor, blocked, running, waiting, queued}},
		queue: boardAgentWorkQueueStoreStub{items: []domainsuperagent.RunQueueItem{queueItem}},
		heartbeat: boardAgentWorkHeartbeatStub{activities: []agentheartbeat.LivingActivity{{
			ActorID: "mio", State: "running", ActivityKind: string(agentheartbeat.ActivityKindBoard), Title: "current BOARD title",
		}}},
	}

	snapshot, err := reader.ReadBoardAgentWork(context.Background())
	if err != nil {
		t.Fatalf("ReadBoardAgentWork() error = %v", err)
	}
	wantActors := []string{"mio", "shiro", "kuro", "midori", "aka", "ao", "kin", "gin"}
	if len(snapshot.Actors) != len(wantActors) {
		t.Fatalf("actor count = %d, want %d: %#v", len(snapshot.Actors), len(wantActors), snapshot.Actors)
	}
	for index, actorID := range wantActors {
		if snapshot.Actors[index].ActorID != actorID {
			t.Fatalf("actor[%d] = %q, want %q", index, snapshot.Actors[index].ActorID, actorID)
		}
	}
	mio := snapshot.Actors[0]
	if mio.Availability.Heartbeat != "available" || mio.HeartbeatState != "running" || mio.ActivityKind != "board" || mio.ActivityTitle != "current BOARD title" {
		t.Fatalf("mio scheduler activity = %+v", mio)
	}
	if len(mio.CurrentTasks) != 1 || mio.CurrentTasks[0].TaskID != string(running.TaskID) || mio.CurrentTasks[0].Title != running.Title {
		t.Fatalf("mio current tasks = %+v, want running Task only", mio.CurrentTasks)
	}
	if len(mio.WaitingQueue) != 3 {
		t.Fatalf("mio waiting queue = %+v, want queued/waiting/blocked rows only", mio.WaitingQueue)
	}
	if mio.WaitingQueue[0].TaskID != string(queued.TaskID) || mio.WaitingQueue[0].QueueClass != "scheduled" || mio.WaitingQueue[0].RunQueueStatus != "queued" || mio.WaitingQueue[0].NotBefore == nil {
		t.Fatalf("first queue row = %+v, want high priority scheduled non-due task", mio.WaitingQueue[0])
	}
	if mio.WaitingQueue[1].TaskID != string(blocked.TaskID) || mio.WaitingQueue[1].QueueClass != "task_only" || mio.WaitingQueue[1].Status != string(domaintask.StatusBlocked) {
		t.Fatalf("second queue row = %+v, want blocked task-only row", mio.WaitingQueue[1])
	}
	if mio.WaitingQueue[2].TaskID != string(waiting.TaskID) || mio.WaitingQueue[2].Status != string(domaintask.StatusWaiting) {
		t.Fatalf("third queue row = %+v, want waiting row", mio.WaitingQueue[2])
	}
	if len(snapshot.Actors[1].WaitingQueue) != 1 || snapshot.Actors[1].WaitingQueue[0].TaskID != string(otherActor.TaskID) {
		t.Fatalf("shiro waiting queue = %+v, want only explicitly assigned Task", snapshot.Actors[1].WaitingQueue)
	}
	for _, actor := range snapshot.Actors {
		for _, row := range append(append([]viewer.BoardAgentWorkTask{}, actor.CurrentTasks...), actor.WaitingQueue...) {
			if row.TaskID == string(ownerOnly.TaskID) || row.Title == ownerOnly.Title {
				t.Fatalf("owner-only Task was attributed to an actor: %+v", row)
			}
		}
	}
}

func TestBoardAgentWorkRuntimeReaderDistinguishesNoWorkFromUnavailableStores(t *testing.T) {
	actors := domainsuperagent.PrivateChatQueueActorIDs()
	noWorkReader := &boardAgentWorkRuntimeReader{
		tasks: boardAgentWorkTaskOwnerStub{}, queue: boardAgentWorkQueueStoreStub{}, heartbeat: boardAgentWorkHeartbeatStub{ready: true},
	}
	noWork, err := noWorkReader.ReadBoardAgentWork(context.Background())
	if err != nil {
		t.Fatalf("no-work ReadBoardAgentWork() error = %v", err)
	}
	if len(noWork.Actors) != len(actors) || noWork.Actors[0].Availability.Tasks != "available" || noWork.Actors[0].Availability.RunQueue != "available" || noWork.Actors[0].CurrentTasks == nil || noWork.Actors[0].WaitingQueue == nil || len(noWork.Actors[0].WaitingQueue) != 0 {
		t.Fatalf("no-work snapshot = %+v, want available empty slices", noWork.Actors[0])
	}

	unavailableReader := &boardAgentWorkRuntimeReader{
		tasks: boardAgentWorkTaskOwnerStub{listErr: errors.New("store unavailable")},
		queue: boardAgentWorkQueueStoreStub{}, heartbeat: boardAgentWorkHeartbeatStub{ready: true},
	}
	unavailable, err := unavailableReader.ReadBoardAgentWork(context.Background())
	if err != nil {
		t.Fatalf("unavailable ReadBoardAgentWork() error = %v", err)
	}
	if unavailable.Actors[0].Availability.Tasks != "unavailable" || unavailable.Actors[0].CurrentTasks != nil || unavailable.Actors[0].WaitingQueue != nil {
		t.Fatalf("unavailable snapshot = %+v, want explicit unavailable/null arrays", unavailable.Actors[0])
	}
}

func TestBoardAgentWorkRuntimeReaderKeepsTaskRowsWhenRunQueueIsUnavailable(t *testing.T) {
	task := boardAgentWorkTestTask("mio queued", "mio", domaintask.StatusQueued, domaintask.PriorityNormal, time.Now().UTC())
	reader := &boardAgentWorkRuntimeReader{tasks: boardAgentWorkTaskOwnerStub{tasks: []domaintask.Task{task}}, queue: boardAgentWorkQueueStoreStub{err: errors.New("queue unavailable")}}
	snapshot, err := reader.ReadBoardAgentWork(context.Background())
	if err != nil {
		t.Fatalf("ReadBoardAgentWork() error = %v", err)
	}
	actor := snapshot.Actors[0]
	if actor.Availability.Tasks != "available" || actor.Availability.RunQueue != "unavailable" || len(actor.WaitingQueue) != 1 || actor.WaitingQueue[0].QueueClass != "unknown" {
		t.Fatalf("partial snapshot = %+v, want visible Task and unknown scheduler queue", actor)
	}
}

type boardAgentWorkTaskOwnerStub struct {
	tasks   []domaintask.Task
	listErr error
}

func (s boardAgentWorkTaskOwnerStub) List(_ context.Context, filter domaintask.Filter) ([]domaintask.Task, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	items := make([]domaintask.Task, 0)
	for _, task := range s.tasks {
		if task.Assignee != filter.Assignee || task.Status != filter.Status {
			continue
		}
		if filter.TaskIDAfter != nil && string(task.TaskID) <= string(*filter.TaskIDAfter) {
			continue
		}
		items = append(items, task)
	}
	sort.Slice(items, func(i, j int) bool { return string(items[i].TaskID) < string(items[j].TaskID) })
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

func (s boardAgentWorkTaskOwnerStub) Get(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	for _, task := range s.tasks {
		if task.TaskID == taskID {
			return task, nil
		}
	}
	return domaintask.Task{}, domaintask.ErrNotFound
}

type boardAgentWorkQueueStoreStub struct {
	items []domainsuperagent.RunQueueItem
	err   error
}

func (s boardAgentWorkQueueStoreStub) ListRunQueueCandidatesForTasks(_ context.Context, taskIDs []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error) {
	if s.err != nil {
		return nil, s.err
	}
	ids := make(map[modulecore.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		ids[taskID] = struct{}{}
	}
	items := make([]domainsuperagent.RunQueueItem, 0)
	for _, item := range s.items {
		if _, ok := ids[item.TaskID]; ok {
			items = append(items, item)
		}
	}
	return items, nil
}

type boardAgentWorkHeartbeatStub struct {
	activities []agentheartbeat.LivingActivity
	ready      bool
}

func (s boardAgentWorkHeartbeatStub) OwnerLivingSnapshot(_ context.Context, lookup func(context.Context, modulecore.TaskID) (string, error)) ([]agentheartbeat.LivingActivity, bool) {
	activities := append([]agentheartbeat.LivingActivity(nil), s.activities...)
	if lookup != nil {
		for index := range activities {
			if activities[index].Title == "" {
				activities[index].Title = "activity title"
			}
		}
	}
	return activities, s.ready || len(activities) > 0
}

func boardAgentWorkTestTask(title, assignee string, status domaintask.Status, priority domaintask.Priority, createdAt time.Time) domaintask.Task {
	task := domaintask.Task{Title: title, Assignee: assignee, Status: status, Priority: priority, Route: domaintask.RouteGeneral, CreatedAt: createdAt, UpdatedAt: createdAt}
	task.ApplyDefaults(createdAt)
	task.Status, task.Priority, task.Assignee = status, priority, assignee
	if status == domaintask.StatusWaiting {
		task.WaitingReason = "waiting"
	}
	return task
}

func ptrTime(value time.Time) *time.Time { return &value }
