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
	if len(noWork.Actors) != len(actors) || noWork.Actors[0].Availability.Tasks != "available" || noWork.Actors[0].Availability.RunQueue != "available" ||
		noWork.Actors[0].CurrentTasks == nil || noWork.Actors[0].WaitingQueue == nil || len(noWork.Actors[0].WaitingQueue) != 0 ||
		noWork.Actors[0].Availability.BoardInbox != "not_configured" || noWork.Actors[0].BoardInboxQueue != nil || noWork.Actors[0].QueueCompleteness != "incomplete" {
		t.Fatalf("no-work snapshot = %+v, want task-store empty arrays and explicit unconfigured BOARD source", noWork.Actors[0])
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

func TestBoardAgentWorkRuntimeReaderProjectsBOARDAndForegroundQueuesSeparately(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	resume := now.Add(time.Hour)
	reader := &boardAgentWorkRuntimeReader{
		tasks: boardAgentWorkTaskOwnerStub{}, queue: boardAgentWorkQueueStoreStub{},
		heartbeat: boardAgentWorkHeartbeatStub{
			ready: true,
			waiters: []agentheartbeat.ForegroundWaiterQueue{{ActorID: "mio", Count: 2, Items: []agentheartbeat.ForegroundWaiterPriority{
				{Position: 1, Priority: "high"}, {Position: 2, Priority: "normal"},
			}}},
		},
		board: map[string]boardAgentWorkBoardInboxOwner{
			"mio": boardAgentWorkBoardInboxStub{items: []scheduledBoardInboxQueueItem{{
				WorkID: "board-inbox:item-browser", Title: "bounded instruction title", Priority: domaintask.PriorityHigh, CreatedAt: now, NotBefore: &resume,
			}}},
		},
	}

	snapshot, err := reader.ReadBoardAgentWork(context.Background())
	if err != nil {
		t.Fatalf("ReadBoardAgentWork() error = %v", err)
	}
	mio := snapshot.Actors[0]
	if mio.Availability.BoardInbox != "available" || mio.Availability.ForegroundWaiters != "available" || mio.QueueCompleteness != "complete" {
		t.Fatalf("mio queue source status = %+v completeness=%q, want complete read", mio.Availability, mio.QueueCompleteness)
	}
	if len(mio.BoardInboxQueue) != 1 || mio.BoardInboxQueue[0].WorkID != "board-inbox:item-browser" ||
		mio.BoardInboxQueue[0].Title != "bounded instruction title" || mio.BoardInboxQueue[0].Priority != "high" || mio.BoardInboxQueue[0].NotBefore == nil {
		t.Fatalf("BOARD queue = %+v, want generic metadata-only row", mio.BoardInboxQueue)
	}
	if mio.ForegroundWaiters == nil || mio.ForegroundWaiters.Count != 2 || len(mio.ForegroundWaiters.Items) != 2 || mio.ForegroundWaiters.Items[0].Priority != "high" {
		t.Fatalf("foreground waiters = %+v, want ordered count and priorities", mio.ForegroundWaiters)
	}
}

func TestBoardAgentWorkRuntimeReaderMarksUnavailableBOARDQueueIncomplete(t *testing.T) {
	reader := &boardAgentWorkRuntimeReader{
		tasks: boardAgentWorkTaskOwnerStub{}, queue: boardAgentWorkQueueStoreStub{},
		heartbeat: boardAgentWorkHeartbeatStub{ready: true},
		board:     map[string]boardAgentWorkBoardInboxOwner{"mio": boardAgentWorkBoardInboxStub{err: errors.New("inbox unavailable")}},
	}
	snapshot, err := reader.ReadBoardAgentWork(context.Background())
	if err != nil {
		t.Fatalf("ReadBoardAgentWork() error = %v", err)
	}
	mio := snapshot.Actors[0]
	if mio.Availability.BoardInbox != "unavailable" || mio.BoardInboxQueue != nil || mio.QueueCompleteness != "incomplete" {
		t.Fatalf("unavailable BOARD source = %+v, want null queue and incomplete status", mio)
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
	waiters    []agentheartbeat.ForegroundWaiterQueue
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

func (s boardAgentWorkHeartbeatStub) OwnerForegroundWaiterSnapshot() ([]agentheartbeat.ForegroundWaiterQueue, bool) {
	queues := append([]agentheartbeat.ForegroundWaiterQueue(nil), s.waiters...)
	return queues, s.ready || len(s.activities) > 0
}

type boardAgentWorkBoardInboxStub struct {
	items []scheduledBoardInboxQueueItem
	err   error
}

func (s boardAgentWorkBoardInboxStub) ReadScheduledBoardInboxQueue(context.Context, string) ([]scheduledBoardInboxQueueItem, error) {
	if s.err != nil {
		return nil, s.err
	}
	return append([]scheduledBoardInboxQueueItem(nil), s.items...), nil
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
