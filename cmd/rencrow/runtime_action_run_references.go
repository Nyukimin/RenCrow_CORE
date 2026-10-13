package main

import (
	"context"
	"errors"
	"fmt"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// verifyActionRunReferences checks that the Run of every Action in the store is
// owned by a Task. It reads the whole Action history, so it runs only when asked
// for: at start by action_store.verify_run_references_at_start, and by the
// operator through "rencrow actions verify --check-runs".
func verifyActionRunReferences(ctx context.Context, actions *actionmanager.Manager, tasks *taskmanager.Manager) error {
	if actions == nil || tasks == nil {
		return errors.New("Action and Task owners are required for the Run reference check")
	}
	all, err := actions.ListActions(ctx, domainaction.Filter{})
	if err != nil {
		return err
	}
	resolveRun, err := tasks.RunResolver(ctx)
	if err != nil {
		return fmt.Errorf("prepare canonical run lookup for the Run reference check: %w", err)
	}
	checked := make(map[modulecore.RunID]struct{}, len(all))
	missing := 0
	var firstRun modulecore.RunID
	var firstAction modulecore.ActionID
	for _, action := range all {
		if _, done := checked[action.RunID]; done {
			continue
		}
		checked[action.RunID] = struct{}{}
		if _, err := resolveRun(action.RunID); err != nil {
			if !errors.Is(err, domaintask.ErrNotFound) {
				return fmt.Errorf("load run %s for Action %s: %w", action.RunID, action.ActionID, err)
			}
			if missing == 0 {
				firstRun, firstAction = action.RunID, action.ActionID
			}
			missing++
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d Run reference(s) of Actions name a Run that no Task owns (first: Run %s of Action %s)", missing, firstRun, firstAction)
	}
	return nil
}
