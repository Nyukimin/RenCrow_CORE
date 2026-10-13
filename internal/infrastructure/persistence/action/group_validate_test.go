package action

import (
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// validateSnapshot is the rule for a whole store. A transaction on the snapshot
// does not run it: it runs validateActionGroup on the Actions it touched, and
// relies on the rest of the store having satisfied the rule before. That reliance
// is sound only if the whole-store rule is exactly "every Action and its Attempts
// satisfy the group rule, no Attempt lacks its Action, and no two native
// delegations share a Task Run". This test checks that statement on arbitrary
// states, valid and not, against the original validateSnapshot.

// groupVerdict applies the group rule to every Action of a state, the way a
// transaction that touched all of them would.
func groupVerdict(actions []domainaction.Action, attempts []domainaction.Attempt) error {
	actionByID := map[modulecore.ActionID]domainaction.Action{}
	for _, a := range actions {
		actionByID[a.ActionID] = a
	}
	attemptByID := map[modulecore.AttemptID]domainaction.Attempt{}
	byAction := map[modulecore.ActionID][]domainaction.Attempt{}
	for _, a := range attempts {
		attemptByID[a.AttemptID] = a
		byAction[a.ActionID] = append(byAction[a.ActionID], a)
	}
	for _, a := range attempts {
		if _, ok := actionByID[a.ActionID]; !ok {
			return fmt.Errorf("attempt %s refers to unavailable action", a.AttemptID)
		}
	}
	native := map[[2]string]modulecore.ActionID{}
	for _, a := range actions {
		if isNativeDelegationAction(a) {
			key := [2]string{string(a.TaskID), string(a.RunID)}
			if other, ok := native[key]; ok && other != a.ActionID {
				return fmt.Errorf("two native delegations share a Task Run")
			}
			native[key] = a.ActionID
		}
		if err := validateActionGroup(a, byAction[a.ActionID], func(id modulecore.AttemptID) (domainaction.Attempt, bool) {
			v, ok := attemptByID[id]
			return v, ok
		}); err != nil {
			return err
		}
	}
	return nil
}

type stateGen struct {
	rng      *rand.Rand
	tasks    []modulecore.TaskID
	runs     []modulecore.RunID
	actionIx int
}

func newStateGen(seed int64) *stateGen {
	g := &stateGen{rng: rand.New(rand.NewSource(seed))}
	for i := 0; i < 2; i++ {
		g.tasks = append(g.tasks, modulecore.NewTaskID())
		g.runs = append(g.runs, modulecore.NewRunID())
	}
	return g
}

func (g *stateGen) at(n int) time.Time { return supportBase.Add(time.Duration(n) * time.Second) }

func nativeAttempt(action domainaction.Action, at time.Time, mutate func(*domainaction.NativeDelegation)) domainaction.Attempt {
	state := &domainaction.NativeDelegation{Mode: domainaction.NativeDelegationModeOpenStart, ExpectedCriteriaRevision: strings.Repeat("a", 64)}
	if mutate != nil {
		mutate(state)
	}
	return domainaction.Attempt{
		AttemptID: action.CurrentAttemptID, ActionID: action.ActionID, StartReason: domainaction.AttemptStartReasonFirst,
		Status: domainaction.AttemptStatusRunning, StartedAt: at, NativeDelegation: state,
	}
}

// validState builds a store the original rule accepts, from the shapes the
// owners write: open and closed pairs, a retried Action, native delegations in
// their initial and definitely-rejected forms.
func (g *stateGen) validState() ([]domainaction.Action, []domainaction.Attempt) {
	var actions []domainaction.Action
	var attempts []domainaction.Attempt
	n := 1 + g.rng.Intn(5)
	for i := 0; i < n; i++ {
		task, run := g.tasks[g.rng.Intn(len(g.tasks))], g.runs[g.rng.Intn(len(g.runs))]
		g.actionIx++
		at := g.at(g.actionIx)
		switch g.rng.Intn(6) {
		case 0: // open, running
			a, at1 := newOpenPair(task, run, domainaction.KindTool, "o", at)
			actions, attempts = append(actions, a), append(attempts, at1)
		case 1: // closed
			a, at1 := newOpenPair(task, run, domainaction.KindLLM, "c", at)
			closedAttempt, _ := at1.Close(domainaction.AttemptStatusSucceeded, at.Add(time.Second), "x")
			closedAction, _ := a.Close(domainaction.StatusSucceeded, at.Add(time.Second), "x")
			actions, attempts = append(actions, closedAction), append(attempts, closedAttempt)
		case 2: // retried: first attempt failed, second running and current
			a, first := newOpenPair(task, run, domainaction.KindTool, "r", at)
			failed, _ := first.Close(domainaction.AttemptStatusFailed, at.Add(time.Second), "x")
			second := domainaction.Attempt{
				AttemptID: modulecore.NewAttemptID(), ActionID: a.ActionID, StartReason: domainaction.AttemptStartReasonRetry,
				Status: domainaction.AttemptStatusRunning, StartedAt: at.Add(2 * time.Second),
			}
			a.CurrentAttemptID = second.AttemptID
			actions, attempts = append(actions, a), append(attempts, failed, second)
		case 3: // open Action whose current attempt ended (waiting for a retry)
			a, at1 := newOpenPair(task, run, domainaction.KindTool, "w", at)
			ended, _ := at1.Close(domainaction.AttemptStatusTimedOut, at.Add(time.Second), "x")
			actions, attempts = append(actions, a), append(attempts, ended)
		case 4: // native delegation, initial form (only one per Task Run)
			a, _ := newOpenPair(task, run, domainaction.KindDelegation, domainaction.NativeDelegationActionName, at)
			if g.hasNative(actions, a) {
				continue
			}
			actions, attempts = append(actions, a), append(attempts, nativeAttempt(a, at, nil))
		case 5: // native delegation, definitely rejected
			a, _ := newOpenPair(task, run, domainaction.KindDelegation, domainaction.NativeDelegationActionName, at)
			if g.hasNative(actions, a) {
				continue
			}
			attempt := nativeAttempt(a, at, func(d *domainaction.NativeDelegation) {
				d.Open = nativeTestMutation("k", []byte(`{"o":1}`), domainaction.NativeMutationStatusRejected, nil, "denied")
			})
			done := at.Add(time.Second)
			attempt.Status, attempt.CompletedAt = domainaction.AttemptStatusFailed, &done
			a.Status, a.CompletedAt, a.UpdatedAt = domainaction.StatusFailed, &done, done
			actions, attempts = append(actions, a), append(attempts, attempt)
		}
	}
	return actions, attempts
}

func (g *stateGen) hasNative(actions []domainaction.Action, candidate domainaction.Action) bool {
	for _, a := range actions {
		if isNativeDelegationAction(a) && a.TaskID == candidate.TaskID && a.RunID == candidate.RunID {
			return true
		}
	}
	return false
}

// perturb damages a valid state in one of the ways a bug or a hand edit could.
func (g *stateGen) perturb(actions []domainaction.Action, attempts []domainaction.Attempt) ([]domainaction.Action, []domainaction.Attempt) {
	actions = append([]domainaction.Action(nil), actions...)
	attempts = append([]domainaction.Attempt(nil), attempts...)
	pickAction := func() int { return g.rng.Intn(len(actions)) }
	pickAttempt := func() int { return g.rng.Intn(len(attempts)) }
	switch g.rng.Intn(14) {
	case 0: // current attempt unknown
		i := pickAction()
		actions[i].CurrentAttemptID = modulecore.NewAttemptID()
	case 1: // current attempt cleared
		actions[pickAction()].CurrentAttemptID = ""
	case 2: // current attempt of another Action
		if len(attempts) > 0 && len(actions) > 1 {
			i := pickAction()
			j := pickAttempt()
			actions[i].CurrentAttemptID = attempts[j].AttemptID
		}
	case 3: // attempt moved to another Action
		if len(attempts) > 0 && len(actions) > 1 {
			attempts[pickAttempt()].ActionID = actions[pickAction()].ActionID
		}
	case 4: // attempt of an unknown Action
		if len(attempts) > 0 {
			attempts[pickAttempt()].ActionID = modulecore.NewActionID()
		}
	case 5: // attempt back to running
		if len(attempts) > 0 {
			i := pickAttempt()
			attempts[i].Status, attempts[i].CompletedAt = domainaction.AttemptStatusRunning, nil
		}
	case 6: // action closed without touching its attempts
		i := pickAction()
		done := actions[i].CreatedAt.Add(time.Minute)
		actions[i].Status, actions[i].CompletedAt, actions[i].UpdatedAt = domainaction.StatusCancelled, &done, done
	case 7: // action reopened
		i := pickAction()
		actions[i].Status, actions[i].CompletedAt = domainaction.StatusOpen, nil
	case 8: // a second running attempt for an Action
		if len(attempts) > 0 {
			i := pickAttempt()
			extra := attempts[i]
			extra.AttemptID = modulecore.NewAttemptID()
			extra.Status, extra.CompletedAt = domainaction.AttemptStatusRunning, nil
			extra.NativeDelegation = nil
			attempts = append(attempts, extra)
		}
	case 9: // typed native state on an ordinary Action
		if len(attempts) > 0 {
			i := pickAttempt()
			attempts[i].NativeDelegation = &domainaction.NativeDelegation{Mode: domainaction.NativeDelegationModeOpenStart, ExpectedCriteriaRevision: strings.Repeat("a", 64)}
		}
	case 10: // a second native delegation in one Task Run
		i := pickAction()
		dup := actions[i]
		dup.ActionID = modulecore.NewActionID()
		dup.Kind, dup.Name = domainaction.KindDelegation, domainaction.NativeDelegationActionName
		dup.CurrentAttemptID = ""
		actions = append(actions, dup)
	case 11: // an extra attempt on a native delegation
		for i := range actions {
			if isNativeDelegationAction(actions[i]) {
				extra := domainaction.Attempt{
					AttemptID: modulecore.NewAttemptID(), ActionID: actions[i].ActionID, StartReason: domainaction.AttemptStartReasonRetry,
					Status: domainaction.AttemptStatusFailed, StartedAt: actions[i].CreatedAt,
				}
				done := actions[i].CreatedAt.Add(time.Second)
				extra.CompletedAt = &done
				attempts = append(attempts, extra)
				break
			}
		}
	case 12: // an invalid record
		i := pickAction()
		actions[i].UpdatedAt = actions[i].CreatedAt.Add(-time.Second)
	case 13: // an Action whose attempts were all dropped
		if len(attempts) > 0 {
			attempts = attempts[:len(attempts)-1]
		}
	}
	return actions, attempts
}

func TestGroupRuleEqualsTheWholeStoreRule(t *testing.T) {
	g := newStateGen(20261013)
	var valid, invalid int
	const states = 40000
	for i := 0; i < states; i++ {
		actions, attempts := g.validState()
		for k := g.rng.Intn(3); k > 0; k-- {
			if len(actions) == 0 {
				break
			}
			actions, attempts = g.perturb(actions, attempts)
		}
		whole := validateSnapshot(actions, attempts)
		group := groupVerdict(actions, attempts)
		if (whole == nil) != (group == nil) {
			t.Fatalf("state %d: whole-store rule = %v, group rule = %v\nactions: %s\nattempts: %s", i, whole, group, summarizeActions(actions), summarizeAttempts(attempts))
		}
		if whole == nil {
			valid++
		} else {
			invalid++
		}
	}
	t.Logf("%d states: %d accepted, %d rejected by both rules", states, valid, invalid)
	if valid < states/5 || invalid < states/5 {
		t.Fatalf("generator is lopsided (%d accepted, %d rejected); the comparison would prove little", valid, invalid)
	}
}

func summarizeActions(actions []domainaction.Action) string {
	var parts []string
	for _, a := range actions {
		parts = append(parts, fmt.Sprintf("{%s %s/%s %s %s cur=%s}", short(string(a.ActionID)), a.Kind, a.Name, a.Status, ptrState(a.CompletedAt != nil), short(string(a.CurrentAttemptID))))
	}
	return strings.Join(parts, " ")
}

func summarizeAttempts(attempts []domainaction.Attempt) string {
	var parts []string
	for _, a := range attempts {
		parts = append(parts, fmt.Sprintf("{%s of %s %s native=%v}", short(string(a.AttemptID)), short(string(a.ActionID)), a.Status, a.NativeDelegation != nil))
	}
	return strings.Join(parts, " ")
}

func short(id string) string {
	if len(id) > 6 {
		return id[len(id)-6:]
	}
	return id
}

func ptrState(set bool) string {
	if set {
		return "done"
	}
	return "-"
}
