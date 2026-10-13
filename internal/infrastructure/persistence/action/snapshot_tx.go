package action

import (
	"fmt"
	"sort"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The methods below are what a transaction needs beyond a plain map lookup. Each
// has one body for both shapes of transactionStore (see its comment), so the two
// cannot answer differently. All of them expect dataMu to be held.

// selectActions returns the Actions a filter selects, as this transaction sees
// them, unsorted and cloned.
func (tx *transactionStore) selectActions(filter domainaction.Filter) []domainaction.Action {
	if tx.base == nil {
		result := make([]domainaction.Action, 0, len(tx.actions))
		for _, item := range tx.actions {
			if matchesActionFilter(item, filter) {
				result = append(result, cloneAction(item))
			}
		}
		return result
	}
	// Never nil, like the full read's: an empty answer is [] to every caller.
	result := make([]domainaction.Action, 0)
	take := func(item domainaction.Action) {
		if matchesActionFilter(item, filter) {
			result = append(result, cloneAction(item))
		}
	}
	candidates, narrowed := tx.base.actionCandidates(filter)
	if narrowed {
		considered := make(map[modulecore.ActionID]struct{}, len(candidates))
		for _, id := range candidates {
			considered[id] = struct{}{}
			if item, ok := tx.lookupAction(id); ok {
				take(item)
			}
		}
		for id, item := range tx.actions {
			if _, done := considered[id]; !done {
				take(item)
			}
		}
		return result
	}
	for id, item := range tx.base.actions.latest {
		if written, ok := tx.actions[id]; ok {
			item = written
		}
		take(item)
	}
	for id, item := range tx.actions {
		if _, inBase := tx.base.actions.latest[id]; !inBase {
			take(item)
		}
	}
	return result
}

// attemptsOfAction lists the Attempts of an Action as this transaction sees them.
func (tx *transactionStore) attemptsOfAction(actionID modulecore.ActionID) []domainaction.Attempt {
	var out []domainaction.Attempt
	if tx.base != nil {
		for _, id := range tx.base.attemptsOf[actionID] {
			if _, rewritten := tx.attempts[id]; rewritten {
				continue
			}
			if item, ok := tx.base.attempt(id); ok {
				out = append(out, item)
			}
		}
	}
	for _, item := range tx.attempts {
		if item.ActionID == actionID {
			out = append(out, item)
		}
	}
	return out
}

// selectAttempts returns the Attempts a filter selects, as this transaction sees
// them, unsorted and cloned.
func (tx *transactionStore) selectAttempts(filter domainaction.AttemptFilter) []domainaction.Attempt {
	if tx.base == nil {
		result := make([]domainaction.Attempt, 0, len(tx.attempts))
		for _, item := range tx.attempts {
			if matchesAttemptFilter(item, filter) {
				result = append(result, cloneAttempt(item))
			}
		}
		return result
	}
	result := make([]domainaction.Attempt, 0)
	take := func(item domainaction.Attempt) {
		if matchesAttemptFilter(item, filter) {
			result = append(result, cloneAttempt(item))
		}
	}
	if filter.ActionID != "" {
		for _, item := range tx.attemptsOfAction(filter.ActionID) {
			take(item)
		}
		return result
	}
	for id, item := range tx.base.attempts.latest {
		if written, ok := tx.attempts[id]; ok {
			item = written
		}
		take(item)
	}
	for id, item := range tx.attempts {
		if _, inBase := tx.base.attempts.latest[id]; !inBase {
			take(item)
		}
	}
	return result
}

// validateTouchedLocked holds a transaction on the snapshot to the store's rule
// by checking what it changed: the group of every Action it saved and of every
// Action an Attempt it saved belongs to, and the Task Run of each native
// delegation among them. The rest of the store satisfied the rule before the
// transaction began and no part of it changed, so it satisfies it still.
// (TestGroupRuleEqualsTheWholeStoreRule states why a group is the right unit.)
//
// This runs once, at finalize, and not as each record is saved: CreateAction saves
// an Action that names its first Attempt before it saves the Attempt, so the rule
// holds only for the whole of what a transaction saved.
func (tx *transactionStore) validateTouchedLocked() error {
	touched := make(map[modulecore.ActionID]struct{}, len(tx.pendingActions)+len(tx.pendingAttempts))
	for _, item := range tx.pendingActions {
		touched[item.ActionID] = struct{}{}
	}
	for _, item := range tx.pendingAttempts {
		touched[item.ActionID] = struct{}{}
	}
	ids := make([]modulecore.ActionID, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	for _, id := range ids {
		action, ok := tx.lookupAction(id)
		if !ok {
			return fmt.Errorf("attempt refers to unavailable action %s", id)
		}
		if err := validateActionGroup(action, tx.attemptsOfAction(id), tx.lookupAttempt); err != nil {
			return err
		}
		if isNativeDelegationAction(action) {
			if err := tx.checkNativeOwnerLocked(action); err != nil {
				return err
			}
		}
	}
	return nil
}

// checkNativeOwnerLocked refuses a second native delegation for one Task Run,
// among the snapshot's and this transaction's.
func (tx *transactionStore) checkNativeOwnerLocked(action domainaction.Action) error {
	key := [2]string{string(action.TaskID), string(action.RunID)}
	if owner, ok := tx.base.actions.nativeByTaskRun[key]; ok && owner != action.ActionID {
		return fmt.Errorf("Task Run %s/%s has multiple native delegation actions", action.TaskID, action.RunID)
	}
	for _, other := range tx.actions {
		if other.ActionID != action.ActionID && isNativeDelegationAction(other) && other.TaskID == action.TaskID && other.RunID == action.RunID {
			return fmt.Errorf("Task Run %s/%s has multiple native delegation actions", action.TaskID, action.RunID)
		}
	}
	return nil
}
