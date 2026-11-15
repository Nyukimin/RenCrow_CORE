package main

import (
	"context"
	"fmt"
	"sort"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/viewer"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/agentheartbeat"
	appsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/application/superagent"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const boardAgentWorkTaskLimit = 5000

type boardAgentWorkTaskOwner interface {
	List(context.Context, domaintask.Filter) ([]domaintask.Task, error)
	Get(context.Context, modulecore.TaskID) (domaintask.Task, error)
}

type boardAgentWorkQueueCandidateStore interface {
	ListRunQueueCandidatesForTasks(context.Context, []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error)
}

type boardAgentWorkHeartbeatOwner interface {
	OwnerLivingSnapshot(context.Context, func(context.Context, modulecore.TaskID) (string, error)) ([]agentheartbeat.LivingActivity, bool)
}

type boardAgentWorkRuntimeReader struct {
	tasks     boardAgentWorkTaskOwner
	queue     boardAgentWorkQueueCandidateStore
	heartbeat boardAgentWorkHeartbeatOwner
}

func newRuntimeBoardAgentWorkReader(deps *Dependencies) viewer.BoardAgentWorkReader {
	if deps == nil {
		return nil
	}
	var queue boardAgentWorkQueueCandidateStore
	if deps.superAgentStore != nil {
		queue, _ = deps.superAgentStore.(boardAgentWorkQueueCandidateStore)
	}
	return &boardAgentWorkRuntimeReader{tasks: deps.taskManager, queue: queue, heartbeat: deps.agentHeartbeat}
}

func (r *boardAgentWorkRuntimeReader) ReadBoardAgentWork(ctx context.Context) (viewer.BoardAgentWorkSnapshot, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return viewer.BoardAgentWorkSnapshot{}, err
	}
	actors := domainsuperagent.PrivateChatQueueActorIDs()
	result := viewer.BoardAgentWorkSnapshot{Actors: make([]viewer.BoardAgentWorkActor, 0, len(actors))}
	heartbeats := make(map[string]agentheartbeat.LivingActivity, len(actors))
	heartbeatReady := false
	if r != nil && r.heartbeat != nil {
		var titleLookup func(context.Context, modulecore.TaskID) (string, error)
		if r.tasks != nil {
			titleLookup = func(ctx context.Context, taskID modulecore.TaskID) (string, error) {
				task, err := r.tasks.Get(ctx, taskID)
				if err != nil {
					return "", err
				}
				return task.Title, nil
			}
		}
		activities, ready := r.heartbeat.OwnerLivingSnapshot(ctx, titleLookup)
		heartbeatReady = ready
		for _, activity := range activities {
			heartbeats[activity.ActorID] = activity
		}
	}

	for _, actorID := range actors {
		actor := viewer.BoardAgentWorkActor{
			ActorID:        actorID,
			Availability:   viewer.BoardAgentWorkAvailability{Heartbeat: "unavailable", Tasks: "unavailable", RunQueue: "unavailable"},
			HeartbeatState: "unavailable",
		}
		if activity, ok := heartbeats[actorID]; heartbeatReady && ok {
			actor.Availability.Heartbeat = "available"
			actor.HeartbeatState = activity.State
			actor.ActivityKind = activity.ActivityKind
			actor.ActivityTitle = activity.Title
		}
		if r == nil || r.tasks == nil {
			result.Actors = append(result.Actors, actor)
			continue
		}

		tasks, err := listBoardAgentWorkTasks(ctx, r.tasks, actorID)
		if err != nil {
			if ctx.Err() != nil {
				return viewer.BoardAgentWorkSnapshot{}, ctx.Err()
			}
			result.Actors = append(result.Actors, actor)
			continue
		}
		actor.Availability.Tasks = "available"

		queueItems := map[modulecore.TaskID]domainsuperagent.RunQueueItem{}
		queueAvailable := r.queue != nil
		if queueAvailable {
			taskIDs := make([]modulecore.TaskID, 0, len(tasks))
			for _, task := range tasks {
				taskIDs = append(taskIDs, task.TaskID)
			}
			items, queueErr := r.queue.ListRunQueueCandidatesForTasks(ctx, taskIDs)
			if queueErr != nil || len(items) >= boardAgentWorkTaskLimit {
				queueAvailable = false
			} else {
				for _, item := range items {
					if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
						queueAvailable = false
						queueItems = nil
						break
					}
					if previous, exists := queueItems[item.TaskID]; !exists || runQueueItemCreatedBefore(item, previous) {
						queueItems[item.TaskID] = item
					}
				}
			}
		}
		if queueAvailable {
			actor.Availability.RunQueue = "available"
		}

		current := make([]boardAgentWorkTaskWithQueue, 0)
		waiting := make([]boardAgentWorkTaskWithQueue, 0)
		for _, task := range tasks {
			item, hasQueueItem := queueItems[task.TaskID]
			row := boardAgentWorkTaskRow(task, item, hasQueueItem, queueAvailable)
			entry := boardAgentWorkTaskWithQueue{task: task, item: item, row: row}
			switch task.Status {
			case domaintask.StatusRunning:
				current = append(current, entry)
			case domaintask.StatusQueued, domaintask.StatusWaiting, domaintask.StatusBlocked:
				waiting = append(waiting, entry)
			}
		}
		sortBoardAgentWorkTasks(current)
		sortBoardAgentWorkTasks(waiting)
		actor.CurrentTasks = make([]viewer.BoardAgentWorkTask, 0, len(current))
		for _, task := range current {
			actor.CurrentTasks = append(actor.CurrentTasks, task.row)
		}
		actor.WaitingQueue = make([]viewer.BoardAgentWorkTask, 0, len(waiting))
		for _, task := range waiting {
			actor.WaitingQueue = append(actor.WaitingQueue, task.row)
		}
		result.Actors = append(result.Actors, actor)
	}
	return result, nil
}

type boardAgentWorkTaskWithQueue struct {
	task domaintask.Task
	item domainsuperagent.RunQueueItem
	row  viewer.BoardAgentWorkTask
}

func listBoardAgentWorkTasks(ctx context.Context, owner boardAgentWorkTaskOwner, actorID string) ([]domaintask.Task, error) {
	statuses := []domaintask.Status{domaintask.StatusRunning, domaintask.StatusQueued, domaintask.StatusWaiting, domaintask.StatusBlocked}
	result := make([]domaintask.Task, 0)
	seen := make(map[modulecore.TaskID]struct{})
	for _, status := range statuses {
		var cursor *modulecore.TaskID
		for {
			remaining := boardAgentWorkTaskLimit + 1 - len(result)
			if remaining <= 0 {
				return nil, fmt.Errorf("task snapshot limit exceeded for actor %s", actorID)
			}
			limit := min(domaintask.MaxTaskListPageSize, remaining)
			page, err := owner.List(ctx, domaintask.Filter{Assignee: actorID, Status: status, Limit: limit, TaskIDAfter: cursor})
			if err != nil {
				return nil, fmt.Errorf("list %s tasks for actor %s: %w", status, actorID, err)
			}
			if len(page) > limit {
				return nil, fmt.Errorf("task owner exceeded requested page for actor %s", actorID)
			}
			for _, task := range page {
				if task.Assignee != actorID || task.Status != status {
					continue
				}
				if err := task.Validate(); err != nil {
					return nil, fmt.Errorf("validate task %s for actor %s: %w", task.TaskID, actorID, err)
				}
				if _, duplicate := seen[task.TaskID]; duplicate {
					return nil, fmt.Errorf("duplicate task %s in actor snapshot", task.TaskID)
				}
				seen[task.TaskID] = struct{}{}
				result = append(result, task)
			}
			if len(page) < limit {
				break
			}
			lastID := page[len(page)-1].TaskID
			if lastID.Validate() != nil || cursor != nil && lastID == *cursor {
				return nil, fmt.Errorf("invalid task page cursor for actor %s", actorID)
			}
			cursor = &lastID
		}
	}
	if len(result) > boardAgentWorkTaskLimit {
		return nil, fmt.Errorf("task snapshot limit exceeded for actor %s", actorID)
	}
	return result, nil
}

func boardAgentWorkTaskRow(task domaintask.Task, item domainsuperagent.RunQueueItem, hasQueueItem, queueAvailable bool) viewer.BoardAgentWorkTask {
	queueClass := "task_only"
	if !queueAvailable {
		queueClass = "unknown"
	} else if hasQueueItem {
		queueClass = "scheduled"
	}
	row := viewer.BoardAgentWorkTask{
		TaskID: string(task.TaskID), Title: task.Title, Status: string(task.Status), Priority: string(task.Priority),
		CreatedAt: task.CreatedAt, UpdatedAt: task.UpdatedAt, QueueClass: queueClass,
	}
	if task.StartedAt != nil {
		startedAt := *task.StartedAt
		row.StartedAt = &startedAt
	}
	if hasQueueItem {
		row.RunQueueStatus = item.Status
		if !item.NotBefore.IsZero() {
			notBefore := item.NotBefore
			row.NotBefore = &notBefore
		}
	}
	return row
}

func runQueueItemCreatedBefore(left, right domainsuperagent.RunQueueItem) bool {
	if !left.CreatedAt.Equal(right.CreatedAt) {
		return left.CreatedAt.Before(right.CreatedAt)
	}
	return string(left.QueueItemID) < string(right.QueueItemID)
}

func sortBoardAgentWorkTasks(tasks []boardAgentWorkTaskWithQueue) {
	sort.Slice(tasks, func(i, j int) bool {
		return appsuperagent.ActorQueueTaskLess(tasks[i].task, tasks[i].item, tasks[j].task, tasks[j].item)
	})
}
