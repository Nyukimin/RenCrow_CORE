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
	// Only running Runs can be orphans. Reading the whole Run history here would
	// decode every Run ever written on each start; the store narrows the read to
	// the running ones, and the status check below keeps the result identical.
	activeRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning})
	if err != nil {
		return 0, fmt.Errorf("list running canonical runs for orphan Run restart recovery: %w", err)
	}
	// Whether an Action belongs to a Run is asked of each running Run, in one read
	// of the Action store: listing every Action to collect their Runs read the
	// whole Action history at every start.
	actionOwned, err := runsWithActions(ctx, actions, activeRuns)
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, run := range activeRuns {
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

// runsWithActions returns the Runs among runs that have at least one Action. One
// read transaction answers all of them.
func runsWithActions(ctx context.Context, actions *actionmanager.Manager, runs []domaintask.Run) (map[modulecore.RunID]struct{}, error) {
	owned := make(map[modulecore.RunID]struct{}, len(runs))
	err := actions.ReadTransaction(ctx, func(tx domainaction.Store) error {
		for _, run := range runs {
			found, err := tx.ListActions(ctx, domainaction.Filter{RunID: run.RunID, Limit: 1})
			if err != nil {
				return err
			}
			if len(found) > 0 {
				owned[run.RunID] = struct{}{}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("look up the Actions of running Runs for orphan Run restart recovery: %w", err)
	}
	return owned, nil
}
