package main

import (
	"context"
	"errors"
	"fmt"
	"sort"

	nativedelegation "github.com/Nyukimin/RenCrow_CORE/internal/adapter/nativeharnessclient/delegation"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// recoverActionRunsAfterRestart settles what a process that stopped left behind:
// it cancels the Actions that were still open, and closes the Run of an Action
// that finished while its Run was still running.
//
// It acts on a bounded set, read from the store by what makes an Action matter
// here, and not on the history: the Actions still open, the native delegations
// (which keep their own reconciliation), and the Actions of a Run that is still
// running. An Action outside that set (finished, not a native delegation, its Run
// not running) has nothing to settle. Each Action it does act on has its Run
// resolved and a Run that no Task owns is an error, as it always was. The same
// reference check over the whole history is verifyActionRunReferences, which a
// start runs only when action_store.verify_run_references_at_start asks for it.
func recoverActionRunsAfterRestart(ctx context.Context, actions *actionmanager.Manager, tasks *taskmanager.Manager) (int, error) {
	if actions == nil || tasks == nil {
		return 0, errors.New("Action and Task owners are required for restart recovery")
	}
	runningRuns, err := tasks.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning})
	if err != nil {
		return 0, fmt.Errorf("list running canonical runs for Action restart recovery: %w", err)
	}
	storedActions, err := actionsInRecoveryScope(ctx, actions, runningRuns)
	if err != nil {
		return 0, err
	}
	// Runs are resolved by ID, never held in a map of the whole history here:
	// the Task owner decides how (one read per Run with an ID-indexed store, one
	// shared pass without one). A reference to a Run that no Task owns is still
	// an error.
	resolveRun, err := tasks.RunResolver(ctx)
	if err != nil {
		return 0, fmt.Errorf("prepare canonical run lookup for Action restart recovery: %w", err)
	}
	recovered := 0
	for _, action := range storedActions {
		actionStatus := action.Status
		summary := action.Summary
		isNativeDelegation := action.Kind == domainaction.KindDelegation && action.Name == domainaction.NativeDelegationActionName
		var current *domainaction.Attempt
		if actionStatus == domainaction.StatusOpen || isNativeDelegation {
			attempts, err := actions.ListAttempts(ctx, domainaction.AttemptFilter{ActionID: action.ActionID})
			if err != nil {
				return recovered, fmt.Errorf("load attempts for stale Action %s: %w", action.ActionID, err)
			}
			for index := range attempts {
				if attempts[index].AttemptID == action.CurrentAttemptID {
					current = &attempts[index]
					break
				}
			}
			if current == nil {
				return recovered, fmt.Errorf("stale Action %s has no active current Attempt", action.ActionID)
			}
			if actionStatus == domainaction.StatusOpen {
				if current.Status != domainaction.AttemptStatusRunning {
					return recovered, fmt.Errorf("stale Action %s has no active current Attempt", action.ActionID)
				}
				if isNativeDelegation {
					// A native Harness mutation may have reached the owner before this
					// process stopped. Its typed owner forbids generic completion; keep
					// the original Run and frozen idempotency state for later owner-led
					// reconciliation instead of manufacturing a terminal outcome.
					continue
				}
				summary = "process restarted before Action completion"
				if _, _, err := actions.CompleteAttempt(ctx, action.ActionID, current.AttemptID, domainaction.AttemptStatusCancelled, domainaction.StatusCancelled, summary); err != nil {
					return recovered, fmt.Errorf("cancel stale Action %s: %w", action.ActionID, err)
				}
				actionStatus = domainaction.StatusCancelled
			}
		}
		run, err := resolveRun(action.RunID)
		if err != nil {
			return recovered, fmt.Errorf("load run %s for terminal Action %s: %w", action.RunID, action.ActionID, err)
		}
		if run.Status != domaintask.RunStatusRunning {
			continue
		}
		status := domaintask.StatusFailed
		switch actionStatus {
		case domainaction.StatusSucceeded:
			if !isNativeDelegation {
				status = domaintask.StatusSucceeded
				break
			}
			expectedCriteriaRevision := ""
			if task, taskErr := tasks.Get(ctx, action.TaskID); taskErr == nil {
				expectedCriteriaRevision = task.ExpectedCriteriaRevision
			}
			if nativedelegation.HasAcceptedStoredRunResult(*current, expectedCriteriaRevision) {
				status = domaintask.StatusSucceeded
			}
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

// actionsInRecoveryScope reads, in one transaction, the Actions a restart
// recovery acts on, oldest first (the order the whole history was walked in).
func actionsInRecoveryScope(ctx context.Context, actions *actionmanager.Manager, runningRuns []domaintask.Run) ([]domainaction.Action, error) {
	byID := map[modulecore.ActionID]domainaction.Action{}
	err := actions.ReadTransaction(ctx, func(tx domainaction.Store) error {
		add := func(found []domainaction.Action, err error) error {
			if err != nil {
				return err
			}
			for _, action := range found {
				byID[action.ActionID] = action
			}
			return nil
		}
		if err := add(tx.ListActions(ctx, domainaction.Filter{Status: domainaction.StatusOpen})); err != nil {
			return err
		}
		if err := add(tx.ListActions(ctx, domainaction.Filter{Kind: domainaction.KindDelegation, Name: domainaction.NativeDelegationActionName})); err != nil {
			return err
		}
		for _, run := range runningRuns {
			if err := add(tx.ListActions(ctx, domainaction.Filter{RunID: run.RunID})); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list the Actions that Action restart recovery acts on: %w", err)
	}
	scoped := make([]domainaction.Action, 0, len(byID))
	for _, action := range byID {
		scoped = append(scoped, action)
	}
	sort.Slice(scoped, func(i, j int) bool {
		if scoped[i].CreatedAt.Equal(scoped[j].CreatedAt) {
			return scoped[i].ActionID < scoped[j].ActionID
		}
		return scoped[i].CreatedAt.Before(scoped[j].CreatedAt)
	})
	return scoped, nil
}
