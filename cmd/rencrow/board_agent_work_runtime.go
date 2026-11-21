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
	OwnerForegroundWaiterSnapshot() ([]agentheartbeat.ForegroundWaiterQueue, bool)
}

type boardAgentWorkBoardInboxOwner interface {
	ReadScheduledBoardInboxQueue(context.Context, string) ([]scheduledBoardInboxQueueItem, error)
}

type boardAgentWorkRuntimeReader struct {
	tasks     boardAgentWorkTaskOwner
	queue     boardAgentWorkQueueCandidateStore
	heartbeat boardAgentWorkHeartbeatOwner
	board     map[string]boardAgentWorkBoardInboxOwner
}

func newRuntimeBoardAgentWorkReader(deps *Dependencies) viewer.BoardAgentWorkReader {
	if deps == nil {
		return nil
	}
	var queue boardAgentWorkQueueCandidateStore
	if deps.superAgentStore != nil {
		queue, _ = deps.superAgentStore.(boardAgentWorkQueueCandidateStore)
	}
	board := make(map[string]boardAgentWorkBoardInboxOwner, len(deps.boardCommunicationActorBoots))
	for actorID, boot := range deps.boardCommunicationActorBoots {
		if boot != nil {
			board[actorID] = boot
		}
	}
	return &boardAgentWorkRuntimeReader{tasks: deps.taskManager, queue: queue, heartbeat: deps.agentHeartbeat, board: board}
}

func (b *boardCommunicationBoot) ReadScheduledBoardInboxQueue(ctx context.Context, actorID string) ([]scheduledBoardInboxQueueItem, error) {
	if b == nil {
		return nil, ErrBoardCommunicationCompositionUnavailable
	}
	composition := b.currentComposition()
	if composition == nil {
		return nil, ErrBoardCommunicationCompositionUnavailable
	}
	return composition.ReadScheduledBoardInboxQueue(ctx, actorID)
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
	if r == nil || r.tasks == nil {
		return viewer.BoardAgentWorkSnapshot{}, fmt.Errorf("canonical Task owner is unavailable")
	}
	activityReader, ok := r.tasks.(interface {
		SnapshotCurrentActivities(context.Context, []string) (map[string]domaintask.CurrentActivitySnapshot, error)
	})
	if !ok {
		return viewer.BoardAgentWorkSnapshot{}, fmt.Errorf("canonical current activity snapshot is unavailable")
	}
	currentActivities, err := activityReader.SnapshotCurrentActivities(ctx, actors)
	if err != nil {
		return viewer.BoardAgentWorkSnapshot{}, fmt.Errorf("read canonical current activities: %w", err)
	}
	heartbeats := make(map[string]agentheartbeat.LivingActivity, len(actors))
	heartbeatReady := false
	waiterQueues := make(map[string]agentheartbeat.ForegroundWaiterQueue, len(actors))
	waitersReady := false
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
		queues, ready := r.heartbeat.OwnerForegroundWaiterSnapshot()
		waitersReady = ready
		for _, queue := range queues {
			waiterQueues[queue.ActorID] = queue
		}
	}

	for _, actorID := range actors {
		actor := viewer.BoardAgentWorkActor{
			ActorID:           actorID,
			Availability:      viewer.BoardAgentWorkAvailability{Heartbeat: "unavailable", Tasks: "unavailable", RunQueue: "unavailable", BoardInbox: "not_configured", ForegroundWaiters: "not_configured"},
			HeartbeatState:    "unavailable",
			QueueCompleteness: "incomplete",
		}
		if activity, ok := heartbeats[actorID]; heartbeatReady && ok {
			actor.Availability.Heartbeat = "available"
			actor.HeartbeatState = activity.State
			actor.ActivityKind = activity.ActivityKind
			actor.ActivityTitle = activity.Title
		}
		if r != nil && r.heartbeat != nil {
			if !waitersReady {
				actor.Availability.ForegroundWaiters = "unavailable"
			} else if queue, ok := waiterQueues[actorID]; ok {
				actor.Availability.ForegroundWaiters = "available"
				waiters := &viewer.BoardAgentWorkForegroundWaiters{Count: queue.Count, Items: make([]viewer.BoardAgentWorkForegroundWaiter, 0, len(queue.Items))}
				for _, item := range queue.Items {
					waiters.Items = append(waiters.Items, viewer.BoardAgentWorkForegroundWaiter{Position: item.Position, Priority: item.Priority})
				}
				actor.ForegroundWaiters = waiters
			} else {
				actor.Availability.ForegroundWaiters = "unavailable"
			}
		}
		if r != nil {
			if source, configured := r.board[actorID]; configured {
				items, boardErr := source.ReadScheduledBoardInboxQueue(ctx, actorID)
				if boardErr == nil {
					actor.Availability.BoardInbox = "available"
					actor.BoardInboxQueue = make([]viewer.BoardAgentWorkBoardItem, 0, len(items))
					for _, item := range items {
						row := viewer.BoardAgentWorkBoardItem{
							WorkID: item.WorkID, Title: item.Title, Priority: string(item.Priority), CreatedAt: item.CreatedAt,
						}
						if item.NotBefore != nil {
							notBefore := item.NotBefore.UTC()
							row.NotBefore = &notBefore
						}
						actor.BoardInboxQueue = append(actor.BoardInboxQueue, row)
					}
				} else {
					if ctx.Err() != nil {
						return viewer.BoardAgentWorkSnapshot{}, ctx.Err()
					}
					actor.Availability.BoardInbox = "unavailable"
				}
			}
		}
		if r == nil || r.tasks == nil {
			actor.QueueCompleteness = boardAgentWorkQueueCompleteness(actor.Availability)
			result.Actors = append(result.Actors, actor)
			continue
		}
		currentSnapshot, hasCurrent := currentActivities[actorID]
		if !hasCurrent || currentSnapshot.Task.Activity == nil {
			return viewer.BoardAgentWorkSnapshot{}, fmt.Errorf("canonical current activity is missing for %s", actorID)
		}
		kind, label, phase, revision, activityValid := publicCurrentActivity(currentSnapshot)
		if !activityValid {
			return viewer.BoardAgentWorkSnapshot{}, fmt.Errorf("canonical current activity is invalid for %s", actorID)
		}
		currentActivityDTO := &viewer.BoardAgentWorkCurrentActivity{Kind: kind, Label: label, Phase: phase, Revision: revision}
		actor.CurrentActivity = currentActivityDTO
		actor.ActivityKind, actor.ActivityTitle = kind, label
		currentRow := boardAgentWorkTaskRow(currentSnapshot.Task, domainsuperagent.RunQueueItem{}, false, false)
		currentRow.Title = currentActivityDTO.Label
		currentRow.CurrentActivity = currentActivityDTO
		actor.CurrentTasks = []viewer.BoardAgentWorkTask{currentRow}

		taskProjection, err := readActorTaskProjection(ctx, r.tasks, actorID)
		if err != nil {
			if ctx.Err() != nil {
				return viewer.BoardAgentWorkSnapshot{}, ctx.Err()
			}
			actor.QueueCompleteness = boardAgentWorkQueueCompleteness(actor.Availability)
			result.Actors = append(result.Actors, actor)
			continue
		}
		actor.Availability.Tasks = "available"

		queueItems := map[modulecore.TaskID]domainsuperagent.RunQueueItem{}
		queueAvailable := r.queue != nil
		if queueAvailable {
			taskIDs := make([]modulecore.TaskID, 0, len(taskProjection.All))
			for _, task := range taskProjection.All {
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

		waiting := make([]boardAgentWorkTaskWithQueue, 0, len(taskProjection.Waiting))
		for _, task := range taskProjection.Waiting {
			if task.TaskID == currentSnapshot.Task.TaskID {
				continue
			}
			item, hasQueueItem := queueItems[task.TaskID]
			row := boardAgentWorkTaskRow(task, item, hasQueueItem, queueAvailable)
			waiting = append(waiting, boardAgentWorkTaskWithQueue{task: task, item: item, row: row})
		}
		sortBoardAgentWorkTasks(waiting)
		item, hasQueueItem := queueItems[currentSnapshot.Task.TaskID]
		currentRow = boardAgentWorkTaskRow(currentSnapshot.Task, item, hasQueueItem, queueAvailable)
		currentRow.Title = currentActivityDTO.Label
		currentRow.CurrentActivity = currentActivityDTO
		actor.CurrentTasks = []viewer.BoardAgentWorkTask{currentRow}
		actor.WaitingQueue = make([]viewer.BoardAgentWorkTask, 0, len(waiting))
		for _, task := range waiting {
			actor.WaitingQueue = append(actor.WaitingQueue, task.row)
		}
		actor.QueueCompleteness = boardAgentWorkQueueCompleteness(actor.Availability)
		result.Actors = append(result.Actors, actor)
	}
	return result, nil
}

func boardAgentWorkQueueCompleteness(availability viewer.BoardAgentWorkAvailability) string {
	if availability.Tasks == "available" && availability.RunQueue == "available" &&
		availability.BoardInbox == "available" && availability.ForegroundWaiters == "available" {
		return "complete"
	}
	return "incomplete"
}

type boardAgentWorkTaskWithQueue struct {
	task domaintask.Task
	item domainsuperagent.RunQueueItem
	row  viewer.BoardAgentWorkTask
}

type actorTaskProjection struct {
	All     []domaintask.Task
	Current []domaintask.Task
	Waiting []domaintask.Task
}

func readActorTaskProjection(ctx context.Context, owner boardAgentWorkTaskOwner, actorID string, statuses ...domaintask.Status) (actorTaskProjection, error) {
	tasks, err := listBoardAgentWorkTasks(ctx, owner, actorID, statuses...)
	if err != nil {
		return actorTaskProjection{}, err
	}
	projection := actorTaskProjection{
		All:     tasks,
		Current: make([]domaintask.Task, 0),
		Waiting: make([]domaintask.Task, 0),
	}
	for _, task := range tasks {
		switch task.Status {
		case domaintask.StatusRunning:
			projection.Current = append(projection.Current, task)
		case domaintask.StatusQueued, domaintask.StatusWaiting, domaintask.StatusBlocked:
			projection.Waiting = append(projection.Waiting, task)
		}
	}
	order := func(tasks []domaintask.Task) {
		sort.Slice(tasks, func(i, j int) bool {
			return appsuperagent.ActorQueueTaskLess(tasks[i], domainsuperagent.RunQueueItem{}, tasks[j], domainsuperagent.RunQueueItem{})
		})
	}
	order(projection.Current)
	order(projection.Waiting)
	return projection, nil
}

func listBoardAgentWorkTasks(ctx context.Context, owner boardAgentWorkTaskOwner, actorID string, statuses ...domaintask.Status) ([]domaintask.Task, error) {
	if len(statuses) == 0 {
		statuses = []domaintask.Status{domaintask.StatusRunning, domaintask.StatusQueued, domaintask.StatusWaiting, domaintask.StatusBlocked}
	}
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
