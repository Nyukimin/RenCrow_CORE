package action

import (
	"fmt"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The two folds below are the one definition of how log lines combine into the
// latest version of each record, and of the rules each line is held to against
// the version before it. The full read (loadActions, loadAttempts) and the
// in-memory snapshot both run them, so the snapshot cannot accept a history the
// full read refuses, or the reverse. They check one line against the lines
// before it; the rules between an Action and its Attempts are validateSnapshot
// (whole store) and validateActionGroup (one Action and its Attempts).

// actionFold folds Action lines in log order.
type actionFold struct {
	latest          map[modulecore.ActionID]domainaction.Action
	nativeByTaskRun map[[2]string]modulecore.ActionID
}

func newActionFold(capacity int) *actionFold {
	return &actionFold{
		latest:          make(map[modulecore.ActionID]domainaction.Action, capacity),
		nativeByTaskRun: make(map[[2]string]modulecore.ActionID),
	}
}

// apply folds item, the index-th record, into the latest versions. The error
// leaves the fold unchanged.
func (f *actionFold) apply(index int, item domainaction.Action) error {
	if err := item.Validate(); err != nil {
		return fmt.Errorf("action record %d is invalid: %w", index, err)
	}
	if previous, ok := f.latest[item.ActionID]; ok {
		if previous.TaskID != item.TaskID || previous.RunID != item.RunID || previous.Kind != item.Kind || !previous.CreatedAt.Equal(item.CreatedAt) {
			return fmt.Errorf("action record %d changes immutable identity fields", index)
		}
		if isNativeDelegationAction(previous) {
			if !isNativeDelegationAction(item) || previous.Name != item.Name || previous.CurrentAttemptID != item.CurrentAttemptID {
				return fmt.Errorf("action record %d changes immutable native delegation identity", index)
			}
			if !previous.IsOpen() && item.Status != previous.Status {
				return fmt.Errorf("action record %d reopens or changes a terminal native delegation", index)
			}
		}
	}
	if isNativeDelegationAction(item) {
		key := [2]string{string(item.TaskID), string(item.RunID)}
		if previous, ok := f.nativeByTaskRun[key]; ok && previous != item.ActionID {
			return fmt.Errorf("action record %d duplicates native delegation for Task Run %s/%s", index, item.TaskID, item.RunID)
		}
		f.nativeByTaskRun[key] = item.ActionID
	}
	f.latest[item.ActionID] = item
	return nil
}

// attemptFold folds Attempt lines in log order.
type attemptFold struct {
	latest         map[modulecore.AttemptID]domainaction.Attempt
	activeByAction map[modulecore.ActionID]modulecore.AttemptID
}

func newAttemptFold(capacity int) *attemptFold {
	return &attemptFold{
		latest:         make(map[modulecore.AttemptID]domainaction.Attempt, capacity),
		activeByAction: make(map[modulecore.ActionID]modulecore.AttemptID),
	}
}

// apply folds item, the index-th record, into the latest versions. The error
// leaves the fold unchanged.
func (f *attemptFold) apply(index int, item domainaction.Attempt) error {
	if err := item.Validate(); err != nil {
		return fmt.Errorf("attempt record %d is invalid: %w", index, err)
	}
	previous, exists := f.latest[item.AttemptID]
	if !exists && item.NativeDelegation != nil {
		if err := validateInitialNativeAttemptShape(item); err != nil {
			return fmt.Errorf("attempt record %d has invalid initial native state: %w", index, err)
		}
	}
	if exists {
		if previous.ActionID != item.ActionID || previous.StartReason != item.StartReason || !previous.StartedAt.Equal(item.StartedAt) {
			return fmt.Errorf("attempt record %d changes immutable identity or start fields", index)
		}
		if err := validateAttemptRecordTransition(previous, item); err != nil {
			return fmt.Errorf("attempt record %d: %w", index, err)
		}
	}
	if item.Status == domainaction.AttemptStatusRunning {
		if activeID, ok := f.activeByAction[item.ActionID]; ok && activeID != item.AttemptID {
			return fmt.Errorf("action %s has multiple active attempts", item.ActionID)
		}
		f.activeByAction[item.ActionID] = item.AttemptID
	} else if activeID, ok := f.activeByAction[item.ActionID]; ok && activeID == item.AttemptID {
		delete(f.activeByAction, item.ActionID)
	}
	f.latest[item.AttemptID] = item
	return nil
}
