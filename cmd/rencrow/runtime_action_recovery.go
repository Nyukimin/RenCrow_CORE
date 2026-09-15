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

func recoverActionRunsAfterRestart(ctx context.Context, actions *actionmanager.Manager, tasks *taskmanager.Manager) (int, error) {
	if actions == nil || tasks == nil {
		return 0, errors.New("Action and Task owners are required for restart recovery")
	}
	storedActions, err := actions.ListActions(ctx, domainaction.Filter{})
	if err != nil {
		return 0, err
	}
	allRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{})
	if err != nil {
		return 0, fmt.Errorf("list canonical runs for Action restart recovery: %w", err)
	}
	runByID := make(map[modulecore.RunID]domaintask.Run, len(allRuns))
	for _, run := range allRuns {
		runByID[run.RunID] = run
	}
	recovered := 0
	for _, action := range storedActions {
		actionStatus := action.Status
		summary := action.Summary
		if actionStatus == domainaction.StatusOpen {
			attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
			if err != nil {
				return recovered, fmt.Errorf("load attempts for stale Action %s: %w", action.ActionID, err)
			}
			var current *domainaction.Attempt
			for index := range attempts {
				if attempts[index].AttemptID == action.CurrentAttemptID {
					current = &attempts[index]
					break
				}
			}
			if current == nil || current.Status != domainaction.AttemptStatusRunning {
				return recovered, fmt.Errorf("stale Action %s has no active current Attempt", action.ActionID)
			}
			summary = "process restarted before Action completion"
			if _, _, err := actions.CompleteAttempt(ctx, action.ActionID, current.AttemptID, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, summary); err != nil {
				return recovered, fmt.Errorf("cancel stale Action %s: %w", action.ActionID, err)
			}
			actionStatus = domainaction.StatusCancelled
		}
		run, ok := runByID[action.RunID]
		if !ok {
			return recovered, fmt.Errorf("load run %s for terminal Action %s: %w", action.RunID, action.ActionID, domaintask.ErrNotFound)
		}
		if run.Status != domaintask.RunStatusRunning {
			continue
		}
		status := domaintask.StatusFailed
		switch actionStatus {
		case domainaction.StatusSucceeded:
			status = domaintask.StatusSucceeded
		case domainaction.StatusCancelled, domainaction.StatusSuperseded:
			status = domaintask.StatusCancelled
		}
		if summary == "" {
			summary = "recovered from terminal Action after process restart"
		}
		_, didRecover, err := tasks.RecoverRunAfterProcessRestart(ctx, action.TaskID, action.RunID, run.Assignee, status, summary, "")
		if err != nil {
			return recovered, fmt.Errorf("recover run %s for terminal Action %s: %w", action.RunID, action.ActionID, err)
		}
		if didRecover {
			recovered++
		}
	}
	return recovered, nil
}
