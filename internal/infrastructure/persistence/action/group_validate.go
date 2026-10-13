package action

import (
	"fmt"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// validateActionGroup is the part of the whole-store rule (validateSnapshot) that
// concerns one Action and the Attempts that belong to it. A transaction on the
// snapshot runs it on every Action it touched, and on the Action of every Attempt
// it touched, instead of re-validating the store: the rule is local to that group
// except for one thing, that no two native delegations share a Task Run, which
// the caller checks against the index of native delegations.
//
// attempts are all the Attempts of action, in any order. attemptByID finds any
// Attempt of the store, because an Action's current Attempt may name an Attempt
// of another Action, which is a violation to report and not one to repair.
//
// It is a second statement of rules validateSnapshot already states, kept because
// validateSnapshot is the full-read path the flag falls back to and the oracle the
// tests hold this one to: for every state, validateSnapshot accepts exactly when
// every group is valid, no Attempt lacks its Action, and no Task Run has two
// native delegations (TestGroupRuleEqualsTheWholeStoreRule). Delete it together
// with the full-read path, or fold validateSnapshot into it, never change one
// alone.
func validateActionGroup(action domainaction.Action, attempts []domainaction.Attempt, attemptByID func(modulecore.AttemptID) (domainaction.Attempt, bool)) error {
	if err := action.Validate(); err != nil {
		return fmt.Errorf("action %s is invalid: %w", action.ActionID, err)
	}
	native := isNativeDelegationAction(action)
	var activeID modulecore.AttemptID
	hasActive := false
	typedNative := false
	for _, item := range attempts {
		if err := item.Validate(); err != nil {
			return fmt.Errorf("attempt %s is invalid: %w", item.AttemptID, err)
		}
		if item.ActionID != action.ActionID {
			return fmt.Errorf("attempt %s belongs to action %s, not %s", item.AttemptID, item.ActionID, action.ActionID)
		}
		if item.NativeDelegation != nil {
			if !native {
				return fmt.Errorf("typed native delegation state belongs to non-native action %s", item.ActionID)
			}
			typedNative = true
		}
		if item.Status == domainaction.AttemptStatusRunning {
			if hasActive && activeID != item.AttemptID {
				return fmt.Errorf("action %s has multiple active attempts", item.ActionID)
			}
			activeID, hasActive = item.AttemptID, true
		}
	}
	if action.CurrentAttemptID == "" {
		if typedNative {
			return fmt.Errorf("typed native delegation %s has no current attempt", action.ActionID)
		}
	} else {
		attempt, ok := attemptByID(action.CurrentAttemptID)
		if !ok {
			return fmt.Errorf("action %s refers to unavailable current attempt %s", action.ActionID, action.CurrentAttemptID)
		}
		if attempt.ActionID != action.ActionID {
			return fmt.Errorf("action %s current attempt %s belongs to action %s", action.ActionID, action.CurrentAttemptID, attempt.ActionID)
		}
		if attempt.NativeDelegation != nil {
			wantReason := domainaction.AttemptStartReasonFirst
			if attempt.NativeDelegation.EffectiveMode() == domainaction.NativeDelegationModeResume {
				wantReason = domainaction.AttemptStartReasonExplicitResume
			}
			if !native || attempt.StartReason != wantReason || len(attempts) != 1 {
				return fmt.Errorf("typed native delegation %s must own exactly one initial attempt for its mode", action.ActionID)
			}
			if len(attempt.NativeDelegation.RunResult) == 0 {
				stillUnknown := action.IsOpen() && attempt.IsActive() && !nativeHasDefiniteRejection(attempt.NativeDelegation)
				rejected := action.Status == domainaction.StatusFailed && attempt.Status == domainaction.AttemptStatusFailed && nativeHasDefiniteRejection(attempt.NativeDelegation)
				if !stillUnknown && !rejected {
					return fmt.Errorf("native delegation without a run result must remain open/running or be terminally rejected")
				}
			} else if action.IsOpen() || attempt.IsActive() {
				return fmt.Errorf("native delegation result and terminal state must be committed together")
			} else if attempt.Status == domainaction.AttemptStatusSucceeded && attempt.NativeDelegation.Proof == nil {
				return fmt.Errorf("successful native delegation requires a durable criteria proof")
			} else if attempt.NativeDelegation.Proof != nil && attempt.Status != domainaction.AttemptStatusSucceeded {
				return fmt.Errorf("native criteria proof requires a successful Action and Attempt")
			}
		}
		if !action.IsOpen() && attempt.IsActive() {
			return fmt.Errorf("terminal action %s current attempt %s is still running", action.ActionID, action.CurrentAttemptID)
		}
	}
	if hasActive && (!action.IsOpen() || action.CurrentAttemptID != activeID) {
		return fmt.Errorf("active attempt %s is not current for action %s", activeID, action.ActionID)
	}
	return nil
}
