package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// recoverOrphanTaskRunsAfterRestart closes active canonical Runs that no Action
// can explain. Action-backed Runs belong to recoverActionRunsAfterRestart, which
// derives each Run outcome from its terminal Action. A Run written by this
// process keeps running because its writer generation matches the current store
// writer lease, so only Runs whose writer predates the restart are recovered.
//
// Without this boundary a Task that started a Run and died before opening its
// first Action keeps status running forever, and that single stale Task consumes
// one parallel slot on every later start (Atlas lifecycle startup recovery and
// heartbeat collections are denied with "parallel limit exceeded").
func recoverOrphanTaskRunsAfterRestart(ctx context.Context, actions *actionmanager.Manager, tasks *taskmanager.Manager) (int, error) {
	if actions == nil || tasks == nil {
		return 0, errors.New("Action and Task owners are required for orphan Run restart recovery")
	}
	storedActions, err := actions.ListActions(ctx, domainaction.Filter{})
	if err != nil {
		return 0, err
	}
	actionOwned := make(map[modulecore.RunID]struct{}, len(storedActions))
	for _, action := range storedActions {
		actionOwned[action.RunID] = struct{}{}
	}
	allRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		return 0, fmt.Errorf("list canonical runs for orphan Run restart recovery: %w", err)
	}
	recovered := 0
	for _, run := range allRuns {
		if run.Status != domaintask.RunStatusRunning {
			continue
		}
		if _, owned := actionOwned[run.RunID]; owned {
			continue
		}
		task, err := tasks.Get(ctx, run.TaskID)
		if err != nil {
			if errors.Is(err, domaintask.ErrNotFound) {
				continue
			}
			return recovered, fmt.Errorf("load Task %s for orphan Run %s: %w", run.TaskID, run.RunID, err)
		}
		if task.Status != domaintask.StatusRunning {
			continue
		}
		actorID := strings.TrimSpace(run.Assignee)
		if actorID == "" {
			actorID = strings.TrimSpace(task.Assignee)
		}
		if actorID == "" || task.Assignee != actorID {
			log.Printf("SKIP orphan run %s task %s: Task, Run, and actor ownership do not match", run.RunID, run.TaskID)
			continue
		}
		_, didRecover, err := tasks.RecoverRunAfterProcessRestart(ctx, run.TaskID, run.RunID, actorID, domaintask.StatusFailed, "process restarted before run completion", "")
		if err != nil {
			if errors.Is(err, taskmanager.ErrRunConflict) {
				continue
			}
			return recovered, fmt.Errorf("recover orphan run %s: %w", run.RunID, err)
		}
		if didRecover {
			recovered++
		}
	}
	return recovered, nil
}
