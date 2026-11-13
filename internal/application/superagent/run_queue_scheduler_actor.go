package superagent

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const actorPlanningTaskLimit = 5000

type RunQueueActorTaskLister func(context.Context, string, int) ([]domaintask.Task, error)

type RunQueueActorCandidateStore interface {
	ListRunQueueCandidatesForTasks(context.Context, []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error)
}

type RunQueueTaskAdmissionChecker interface {
	CanStart(context.Context, domaintask.Task) (bool, string, error)
}

type RunQueueExactLeaseStore interface {
	ClaimRunQueueItem(context.Context, modulecore.QueueItemID, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	ReleaseRunQueueItem(context.Context, modulecore.QueueItemID, string) (bool, error)
}

type actorRunQueueCandidate struct {
	item domainsuperagent.RunQueueItem
	task domaintask.Task
}

// PlanForActor selects a due queue item for one canonical Task assignee. It
// reads the Task owner and queue only; it does not reserve work or issue a Run.
func (s *RunQueueScheduler) PlanForActor(ctx context.Context, actor string) (*domainsuperagent.RunQueueItem, domaintask.Task, error) {
	if s == nil || s.store == nil || s.taskOwner == nil {
		return nil, domaintask.Task{}, fmt.Errorf("run queue scheduler is not configured")
	}
	if err := domainsuperagent.ValidateActorID(actor); err != nil {
		return nil, domaintask.Task{}, fmt.Errorf("plan run queue for actor: %w", err)
	}
	if s.options.ActorTaskLister == nil {
		return nil, domaintask.Task{}, fmt.Errorf("run queue scheduler has no actor task lister")
	}
	tasks, err := s.options.ActorTaskLister(ctx, actor, actorPlanningTaskLimit+1)
	if err != nil {
		return nil, domaintask.Task{}, fmt.Errorf("list queued tasks for actor %s: %w", actor, err)
	}
	if len(tasks) > actorPlanningTaskLimit {
		return nil, domaintask.Task{}, fmt.Errorf("queued task candidate limit exceeded for actor %s", actor)
	}

	tasksByID := make(map[modulecore.TaskID]domaintask.Task, len(tasks))
	taskIDs := make([]modulecore.TaskID, 0, len(tasks))
	for _, task := range tasks {
		if task.Assignee != actor || (task.Status != domaintask.StatusQueued && !isPrivateTextWaitingTask(task)) {
			continue
		}
		if err := task.Validate(); err != nil {
			return nil, domaintask.Task{}, fmt.Errorf("validate queued task %s for actor %s: %w", task.TaskID, actor, err)
		}
		if _, exists := tasksByID[task.TaskID]; exists {
			return nil, domaintask.Task{}, fmt.Errorf("canonical task owner returned duplicate task %s", task.TaskID)
		}
		tasksByID[task.TaskID] = task
		taskIDs = append(taskIDs, task.TaskID)
	}
	if len(taskIDs) == 0 {
		return nil, domaintask.Task{}, nil
	}

	queueStore, ok := s.store.(RunQueueActorCandidateStore)
	if !ok {
		return nil, domaintask.Task{}, fmt.Errorf("run queue store does not support actor candidate reads")
	}
	items, err := queueStore.ListRunQueueCandidatesForTasks(ctx, taskIDs)
	if err != nil {
		return nil, domaintask.Task{}, fmt.Errorf("list queued run queue items for actor %s: %w", actor, err)
	}
	if len(items) > actorPlanningTaskLimit {
		return nil, domaintask.Task{}, fmt.Errorf("run queue candidate limit exceeded for actor %s", actor)
	}

	now := s.options.Now().UTC()
	candidates := make([]actorRunQueueCandidate, 0, len(items))
	for _, item := range items {
		task, ok := tasksByID[item.TaskID]
		if !ok || !actorPlannableRunQueueCandidate(item, now) || !actorPlannableTaskForQueueItem(task, item, item.TaskID, actor) {
			continue
		}
		if task.Status == domaintask.StatusWaiting {
			owner, ok := s.taskOwner.(RunQueuePrivateTextTaskOwner)
			if !ok {
				return nil, domaintask.Task{}, fmt.Errorf("canonical Task owner does not support private text checkpoint reads")
			}
			checkpoint, found, err := owner.GetPrivateTextCheckpoint(ctx, task.TaskID)
			if err != nil {
				return nil, domaintask.Task{}, fmt.Errorf("read private text checkpoint while planning task %s: %w", task.TaskID, err)
			}
			if !found || !privateTextResumeCheckpointMatches(task, item, checkpoint) {
				continue
			}
		}
		candidates = append(candidates, actorRunQueueCandidate{item: item, task: task})
	}
	if len(candidates) == 0 {
		return nil, domaintask.Task{}, nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		left, right := candidates[i], candidates[j]
		if left.task.Priority != right.task.Priority {
			return taskPriorityRank(left.task.Priority) > taskPriorityRank(right.task.Priority)
		}
		if !left.task.CreatedAt.Equal(right.task.CreatedAt) {
			return left.task.CreatedAt.Before(right.task.CreatedAt)
		}
		if left.task.TaskID != right.task.TaskID {
			return string(left.task.TaskID) < string(right.task.TaskID)
		}
		if !left.item.CreatedAt.Equal(right.item.CreatedAt) {
			return left.item.CreatedAt.Before(right.item.CreatedAt)
		}
		return string(left.item.QueueItemID) < string(right.item.QueueItemID)
	})
	admission, hasAdmission := s.taskOwner.(RunQueueTaskAdmissionChecker)
	visitedTasks := make(map[modulecore.TaskID]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, visited := visitedTasks[candidate.task.TaskID]; visited {
			continue
		}
		visitedTasks[candidate.task.TaskID] = struct{}{}
		if hasAdmission {
			allowed, _, err := admission.CanStart(ctx, candidate.task)
			if err != nil {
				return nil, domaintask.Task{}, fmt.Errorf("check canonical task admission while planning: %w", err)
			}
			if !allowed {
				continue
			}
		}
		item := candidate.item
		return &item, candidate.task, nil
	}
	return nil, domaintask.Task{}, nil
}

// RunPlanned reserves and executes exactly the selected queue item. A false,
// nil result means that the exact item was no longer eligible for this actor.
func (s *RunQueueScheduler) RunPlanned(ctx context.Context, itemID modulecore.QueueItemID, actor string) (bool, error) {
	return s.runPlanned(ctx, itemID, actor, nil)
}

// RunPlannedWithStop passes an actor-heartbeat stop signal to private text
// provider work without cancelling the context used for durable Task writes.
func (s *RunQueueScheduler) RunPlannedWithStop(ctx context.Context, itemID modulecore.QueueItemID, actor string, stop <-chan struct{}) (bool, error) {
	return s.runPlanned(ctx, itemID, actor, stop)
}

func (s *RunQueueScheduler) runPlanned(ctx context.Context, itemID modulecore.QueueItemID, actor string, stop <-chan struct{}) (bool, error) {
	if stop != nil {
		ctx = withRunQueueStopSignal(ctx, stop)
	}
	if s == nil || s.store == nil || s.processor == nil || s.taskOwner == nil {
		return false, fmt.Errorf("run queue scheduler is not configured")
	}
	if err := domainsuperagent.ValidateActorID(actor); err != nil {
		return false, fmt.Errorf("run planned queue item: %w", err)
	}
	if err := itemID.Validate(); err != nil {
		return false, fmt.Errorf("run planned queue item: invalid queue_item_id: %w", err)
	}
	store, ok := s.store.(RunQueueExactLeaseStore)
	if !ok {
		return false, fmt.Errorf("run queue store does not support exact lease reservation")
	}
	now := s.options.Now().UTC()
	token, err := s.options.LeaseToken()
	if err != nil {
		return false, err
	}
	item, err := store.ClaimRunQueueItem(ctx, itemID, now, now.Add(s.options.LeaseDuration), token)
	if err != nil {
		return false, fmt.Errorf("reserve planned run queue item %s: %w", itemID, err)
	}
	if item == nil {
		return false, nil
	}
	release := func(cause error) (bool, error) {
		released, releaseErr := store.ReleaseRunQueueItem(ctx, itemID, item.LeaseToken)
		if releaseErr != nil {
			return false, errors.Join(cause, fmt.Errorf("release ineligible run queue reservation: %w", releaseErr))
		}
		if !released {
			return false, errors.Join(cause, fmt.Errorf("run queue lease lost before ineligible reservation release"))
		}
		return false, cause
	}
	if !actorPlannableRunQueueItem(*item, "reserved", 1, now) || !item.LeaseUntil.After(now) || strings.TrimSpace(item.LeaseToken) == "" {
		return release(nil)
	}
	task, err := s.taskOwner.Get(ctx, item.TaskID)
	if err != nil {
		if errors.Is(err, domaintask.ErrNotFound) {
			return release(nil)
		}
		return release(fmt.Errorf("recheck canonical task %s before run: %w", item.TaskID, err))
	}
	if !actorPlannableTaskForQueueItem(task, *item, item.TaskID, actor) {
		return release(nil)
	}
	if admission, ok := s.taskOwner.(RunQueueTaskAdmissionChecker); ok {
		allowed, _, err := admission.CanStart(ctx, task)
		if err != nil {
			return release(fmt.Errorf("check canonical task admission before run: %w", err))
		}
		if !allowed {
			return release(nil)
		}
	}
	deferred := false
	deferStart := func(cause error) (bool, error) {
		deferred = true
		_, releaseErr := release(cause)
		return true, releaseErr
	}
	err = s.runReservedItemWithStartFailure(ctx, item, func(startErr error) (bool, error) {
		current, getErr := s.taskOwner.Get(ctx, item.TaskID)
		if errors.Is(getErr, domaintask.ErrNotFound) {
			return deferStart(nil)
		}
		if getErr != nil {
			return deferStart(errors.Join(startErr, fmt.Errorf("recheck canonical task after run start refusal: %w", getErr)))
		}
		if !actorPlannableTaskForQueueItem(current, *item, item.TaskID, actor) {
			return deferStart(nil)
		}
		if admission, ok := s.taskOwner.(RunQueueTaskAdmissionChecker); ok {
			allowed, _, checkErr := admission.CanStart(ctx, current)
			if checkErr != nil {
				return deferStart(errors.Join(startErr, fmt.Errorf("recheck canonical task admission after run start refusal: %w", checkErr)))
			}
			if !allowed {
				return deferStart(nil)
			}
		}
		return false, nil
	})
	if err != nil {
		return !deferred, err
	}
	return !deferred, nil
}

func actorPlannableTask(task domaintask.Task, taskID modulecore.TaskID, actor string) bool {
	return task.TaskID == taskID &&
		task.Assignee == actor &&
		task.Status == domaintask.StatusQueued &&
		domaintask.ValidPriority(task.Priority) &&
		!task.CreatedAt.IsZero() &&
		task.Validate() == nil
}

func actorPlannableTaskForQueueItem(task domaintask.Task, item domainsuperagent.RunQueueItem, taskID modulecore.TaskID, actor string) bool {
	if task.Status == domaintask.StatusQueued {
		return !domainsuperagent.IsPrivateTextResumeQueueItem(item) && actorPlannableTask(task, taskID, actor)
	}
	return task.TaskID == taskID && task.Assignee == actor && isPrivateTextWaitingTask(task) &&
		domaintask.ValidPriority(task.Priority) && !task.CreatedAt.IsZero() && task.Validate() == nil &&
		domainsuperagent.IsPrivateTextResumeQueueItem(item)
}

func isPrivateTextWaitingTask(task domaintask.Task) bool {
	return task.Status == domaintask.StatusWaiting && task.WaitingReason == domaintask.PrivateTextWaitingReason
}

func actorPlannableRunQueueCandidate(item domainsuperagent.RunQueueItem, now time.Time) bool {
	if actorPlannableRunQueueItem(item, "queued", 0, now) {
		return true
	}
	return item.Status == "reserved" &&
		item.AttemptCount > 0 &&
		item.RunID == "" &&
		!item.LeaseUntil.IsZero() &&
		!item.LeaseUntil.After(now) &&
		actorPlannableRunQueueItem(item, "reserved", item.AttemptCount, now)
}

func actorPlannableRunQueueItem(item domainsuperagent.RunQueueItem, status string, attemptCount int, now time.Time) bool {
	if item.Status != status || item.AttemptCount != attemptCount || item.RunID != "" ||
		(!item.NotBefore.IsZero() && item.NotBefore.After(now)) ||
		domainsuperagent.ValidateRunQueueItem(item) != nil {
		return false
	}
	switch item.RunStartReason {
	case domaintask.RunStartReasonFirst, domaintask.RunStartReasonExplicitRerun:
		return true
	case domaintask.RunStartReasonCheckpointResume:
		if domainsuperagent.IsPrivateTextResumeQueueItem(item) {
			return true
		}
		// Preserve the explicit Viewer Resume contract. These descriptive fields
		// are validated intent metadata; they are not a processor execution
		// cursor and do not establish safe mid-work continuation by themselves.
		return item.Action == "resume" &&
			item.CheckpointRevision > 0 &&
			strings.TrimSpace(item.CheckpointSummary) != "" &&
			strings.TrimSpace(item.NextAction) != "" &&
			item.CheckpointID.Validate() == nil &&
			item.IdempotencyKey == string(item.CheckpointID)
	default:
		// In particular, process_restart_resume is not automatically replayed:
		// queue summaries do not prove a durable execution cursor.
		return false
	}
}

func taskPriorityRank(priority domaintask.Priority) int {
	switch priority {
	case domaintask.PriorityCritical:
		return 4
	case domaintask.PriorityHigh:
		return 3
	case domaintask.PriorityNormal:
		return 2
	case domaintask.PriorityLow:
		return 1
	default:
		return 0
	}
}
