package task

import (
	"context"
	"errors"
	"fmt"
	"sort"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

func isActiveExternalGameActivity(task domaintask.Task) bool {
	return task.Status == domaintask.StatusRunning && task.Route == domaintask.RouteGeneral &&
		task.PrivateAccess == nil && task.Activity != nil && task.Activity.Passive &&
		task.Activity.Kind == domaintask.ActivityKindGame && task.Activity.Game != nil &&
		task.Activity.Game.SessionID != ""
}

func (ix *taskIndex) currentActivityTasks(ctx context.Context, actorIDs []string) (map[string]domaintask.Task, error) {
	if err := ix.ensureCurrentActivityIndex(ctx); err != nil {
		return nil, err
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	return ix.currentActivityTasksLocked(ctx, actorIDs)
}

func (ix *taskIndex) currentActivityTasksLocked(ctx context.Context, actorIDs []string) (map[string]domaintask.Task, error) {
	result := make(map[string]domaintask.Task, len(actorIDs))
	for _, actorID := range actorIDs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key, found := ix.currentActivityByActor[actorID]
		if !found {
			continue
		}
		pos, found := ix.taskTailLocked(key)
		if !found {
			return nil, fmt.Errorf("current activity actor %q points to a missing Task: %w", actorID, ErrIndexInconsistent)
		}
		task, err := ix.readTaskAt(pos, key)
		if err != nil {
			return nil, err
		}
		result[actorID] = task
	}
	return result, nil
}

func (ix *taskIndex) activeExternalGameTasks(ctx context.Context) ([]domaintask.Task, error) {
	if err := ix.ensureCurrentActivityIndex(ctx); err != nil {
		return nil, err
	}
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	keys := make([]key16, 0, len(ix.activeExternalGameTaskByID))
	for key := range ix.activeExternalGameTaskByID {
		if slot, found := ix.taskMap[key]; found && ix.tasks[slot].runHead == 0 {
			keys = append(keys, key)
		}
	}
	sort.Slice(keys, func(i, j int) bool { return string(keys[i][:]) < string(keys[j][:]) })
	if len(keys) > domaintask.MaxActiveExternalGameActivities {
		return nil, domaintask.ErrActiveExternalGameActivitiesTooMany
	}
	result := make([]domaintask.Task, 0, len(keys))
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		slot := ix.taskMap[key]
		pos, found := ix.taskTailLocked(key)
		if !found {
			return nil, fmt.Errorf("active external Game Task has no indexed state: %w", ErrIndexInconsistent)
		}
		task, err := ix.readTaskAt(pos, key)
		if err != nil {
			return nil, err
		}
		if ix.tasks[slot].runHead == 0 && isActiveExternalGameActivity(task) {
			result = append(result, task)
		}
	}
	return result, nil
}

// ensureCurrentActivityIndex reconstructs the derived actor lookup once from
// each Task's latest indexed state. The canonical JSONL remains the authority.
func (ix *taskIndex) ensureCurrentActivityIndex(ctx context.Context) error {
	if ix.currentActivityReady.Load() {
		return nil
	}
	ix.mu.Lock()
	defer ix.mu.Unlock()
	if ix.currentActivityReady.Load() {
		return nil
	}
	byActor := make(map[string]key16)
	byTask := make(map[key16][]string)
	activeGames := make(map[key16]struct{})
	for _, rec := range ix.tasks {
		if err := ctx.Err(); err != nil {
			return err
		}
		if rec.stateTail == 0 {
			continue
		}
		pos := ix.lt.pos(rec.stateTail)
		task, err := ix.readTaskAt(pos, rec.key)
		if err != nil {
			return err
		}
		if task.Activity == nil {
			continue
		}
		if err := task.Activity.Validate(task.Status); err != nil {
			return fmt.Errorf("Task %s has invalid current activity metadata: %w", task.TaskID, err)
		}
		if rec.runHead == 0 && isActiveExternalGameActivity(task) {
			activeGames[rec.key] = struct{}{}
		}
		for _, participant := range task.Activity.Participants {
			if !participant.IsCurrent {
				continue
			}
			if existing, found := byActor[participant.ActorID]; found && existing != rec.key {
				return fmt.Errorf("actor %q has multiple current activity Tasks: %w", participant.ActorID, ErrIndexInconsistent)
			}
			byActor[participant.ActorID] = rec.key
			byTask[rec.key] = append(byTask[rec.key], participant.ActorID)
		}
	}
	ix.currentActivityByActor = byActor
	ix.currentActivityActorsByTask = byTask
	ix.activeExternalGameTaskByID = activeGames
	ix.currentActivityReady.Store(true)
	return nil
}

func validateActivityActorIDs(actorIDs []string) error {
	if len(actorIDs) == 0 {
		return errors.New("current activity actor IDs are required")
	}
	seen := make(map[string]struct{}, len(actorIDs))
	for _, actorID := range actorIDs {
		if actorID == "" {
			return errors.New("current activity actor ID is empty")
		}
		if _, exists := seen[actorID]; exists {
			return fmt.Errorf("current activity actor %q is duplicated", actorID)
		}
		seen[actorID] = struct{}{}
	}
	return nil
}
