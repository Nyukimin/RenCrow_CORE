package action

import (
	"context"
	"fmt"
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	domainaction "github.com/Nyukimin/RenCrow_CORE/internal/domain/action"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The oracle test: the store that reads the logs in full is what the logs mean.
// A store with the snapshot is given the same transactions, valid and not, in the
// same order, and must (1) accept and refuse exactly the same ones, (2) answer
// every question with the same value in every field, (3) write byte-identical
// logs, and (4) never need to resynchronize from the files, because the commits
// it followed were enough.

type oracleStep struct {
	action  *domainaction.Action
	attempt *domainaction.Attempt
}

type oracleRun struct {
	t     *testing.T
	rng   *rand.Rand
	tasks []modulecore.TaskID
	runs  []modulecore.RunID
	clock int
	ctx   context.Context
	full  *JSONLStore
}

func (o *oracleRun) now() time.Time {
	o.clock++
	return supportBase.Add(time.Duration(o.clock) * time.Second)
}

func (o *oracleRun) state() (map[modulecore.ActionID]domainaction.Action, map[modulecore.AttemptID]domainaction.Attempt, []domainaction.Action, []domainaction.Attempt) {
	actions, err := o.full.ListActions(o.ctx, domainaction.Filter{})
	if err != nil {
		o.t.Fatal(err)
	}
	attempts, err := o.full.ListAttempts(o.ctx, domainaction.AttemptFilter{})
	if err != nil {
		o.t.Fatal(err)
	}
	am := map[modulecore.ActionID]domainaction.Action{}
	for _, a := range actions {
		am[a.ActionID] = a
	}
	tm := map[modulecore.AttemptID]domainaction.Attempt{}
	for _, a := range attempts {
		tm[a.AttemptID] = a
	}
	return am, tm, actions, attempts
}

func (o *oracleRun) pickTaskRun() (modulecore.TaskID, modulecore.RunID) {
	return o.tasks[o.rng.Intn(len(o.tasks))], o.runs[o.rng.Intn(len(o.runs))]
}

func (o *oracleRun) pick(actions []domainaction.Action, keep func(domainaction.Action) bool) (domainaction.Action, bool) {
	var matches []domainaction.Action
	for _, a := range actions {
		if keep == nil || keep(a) {
			matches = append(matches, a)
		}
	}
	if len(matches) == 0 {
		return domainaction.Action{}, false
	}
	return matches[o.rng.Intn(len(matches))], true
}

func currentAttempt(a domainaction.Action, attempts map[modulecore.AttemptID]domainaction.Attempt) (domainaction.Attempt, bool) {
	v, ok := attempts[a.CurrentAttemptID]
	return v, ok
}

var oracleKinds = []domainaction.Kind{domainaction.KindTool, domainaction.KindLLM, domainaction.KindMemoryPromotion, domainaction.KindPlayback, domainaction.KindDelegation}
var oracleNames = []string{"", "a", "b", "playback_receipt:x:1"}

func (o *oracleRun) newStep() ([]oracleStep, string) {
	actionsByID, attemptsByID, actions, attempts := o.state()
	rng := o.rng
	choice := rng.Intn(100)
	switch {
	case choice < 24: // create
		task, run := o.pickTaskRun()
		kind := oracleKinds[rng.Intn(len(oracleKinds))]
		name := oracleNames[rng.Intn(len(oracleNames))]
		a, at := newOpenPair(task, run, kind, name, o.now())
		return []oracleStep{{action: &a}, {attempt: &at}}, "create"
	case choice < 38: // finish
		a, ok := o.pick(actions, func(a domainaction.Action) bool {
			at, has := currentAttempt(a, attemptsByID)
			return a.IsOpen() && has && at.IsActive() && at.NativeDelegation == nil
		})
		if !ok {
			return nil, "finish(none)"
		}
		at, _ := currentAttempt(a, attemptsByID)
		statuses := []domainaction.AttemptStatus{domainaction.AttemptStatusSucceeded, domainaction.AttemptStatusFailed, domainaction.AttemptStatusCancelled, domainaction.AttemptStatusTimedOut}
		actionStatuses := []domainaction.Status{domainaction.StatusSucceeded, domainaction.StatusFailed, domainaction.StatusCancelled, domainaction.StatusSuperseded}
		when := o.now()
		closedAttempt, err := at.Close(statuses[rng.Intn(len(statuses))], when, "end")
		if err != nil {
			o.t.Fatal(err)
		}
		closedAction, err := a.Close(actionStatuses[rng.Intn(len(actionStatuses))], when, "end")
		if err != nil {
			o.t.Fatal(err)
		}
		if rng.Intn(2) == 0 {
			return []oracleStep{{attempt: &closedAttempt}, {action: &closedAction}}, "finish"
		}
		return []oracleStep{{action: &closedAction}, {attempt: &closedAttempt}}, "finish(action first)"
	case choice < 46: // the attempt ends, the Action stays open
		a, ok := o.pick(actions, func(a domainaction.Action) bool {
			at, has := currentAttempt(a, attemptsByID)
			return a.IsOpen() && has && at.IsActive() && at.NativeDelegation == nil
		})
		if !ok {
			return nil, "end-attempt(none)"
		}
		at, _ := currentAttempt(a, attemptsByID)
		closedAttempt, err := at.Close(domainaction.AttemptStatusFailed, o.now(), "end")
		if err != nil {
			o.t.Fatal(err)
		}
		return []oracleStep{{attempt: &closedAttempt}}, "end-attempt"
	case choice < 54: // retry
		a, ok := o.pick(actions, func(a domainaction.Action) bool {
			at, has := currentAttempt(a, attemptsByID)
			return a.IsOpen() && has && !at.IsActive() && at.NativeDelegation == nil
		})
		if !ok {
			return nil, "retry(none)"
		}
		when := o.now()
		next := domainaction.Attempt{
			AttemptID: modulecore.NewAttemptID(), ActionID: a.ActionID, StartReason: domainaction.AttemptStartReasonRetry,
			Status: domainaction.AttemptStatusRunning, StartedAt: when,
		}
		a.CurrentAttemptID, a.UpdatedAt = next.AttemptID, when
		return []oracleStep{{attempt: &next}, {action: &a}}, "retry"
	case choice < 62: // native delegation, first form
		task, run := o.pickTaskRun()
		a, _ := newOpenPair(task, run, domainaction.KindDelegation, domainaction.NativeDelegationActionName, o.now())
		at := nativeAttempt(a, a.CreatedAt, nil)
		return []oracleStep{{action: &a}, {attempt: &at}}, "native-create"
	case choice < 68: // native delegation advances toward a definite rejection
		a, ok := o.pick(actions, func(a domainaction.Action) bool { return a.IsOpen() && isNativeDelegationAction(a) })
		if !ok {
			return nil, "native-advance(none)"
		}
		at, has := currentAttempt(a, attemptsByID)
		if !has {
			return nil, "native-advance(no attempt)"
		}
		next := at.Clone()
		switch {
		case next.NativeDelegation.Open == nil:
			next.NativeDelegation.Open = nativeTestMutation("k", []byte(`{"o":1}`), domainaction.NativeMutationStatusPrepared, nil, "")
			return []oracleStep{{attempt: &next}}, "native-prepare"
		case next.NativeDelegation.Open.Status == domainaction.NativeMutationStatusPrepared:
			next.NativeDelegation.Open.Status = domainaction.NativeMutationStatusDeliveryUnknown
			return []oracleStep{{attempt: &next}}, "native-unknown"
		default:
			when := o.now()
			next.NativeDelegation.Open.Status = domainaction.NativeMutationStatusRejected
			next.NativeDelegation.Open.ErrorCode = "denied"
			next.Status, next.CompletedAt = domainaction.AttemptStatusFailed, &when
			a.Status, a.CompletedAt, a.UpdatedAt = domainaction.StatusFailed, &when, when
			return []oracleStep{{attempt: &next}, {action: &a}}, "native-reject"
		}
	case choice < 78: // arbitrary damage to an Action
		a, ok := o.pick(actions, nil)
		if !ok {
			return nil, "mutate-action(none)"
		}
		return []oracleStep{{action: o.mutateAction(a, attempts)}}, "mutate-action"
	case choice < 88: // arbitrary damage to an Attempt
		if len(attempts) == 0 {
			return nil, "mutate-attempt(none)"
		}
		at := attempts[rng.Intn(len(attempts))]
		return []oracleStep{{attempt: o.mutateAttempt(at, actions)}}, "mutate-attempt"
	case choice < 92: // an Attempt of an Action that is not there
		at := domainaction.Attempt{
			AttemptID: modulecore.NewAttemptID(), ActionID: modulecore.NewActionID(), StartReason: domainaction.AttemptStartReasonFirst,
			Status: domainaction.AttemptStatusRunning, StartedAt: o.now(),
		}
		return []oracleStep{{attempt: &at}}, "orphan-attempt"
	case choice < 96: // the same Action saved twice in one transaction
		a, ok := o.pick(actions, func(a domainaction.Action) bool { return !isNativeDelegationAction(a) })
		if !ok {
			return nil, "double(none)"
		}
		first, second := a, a
		first.Summary, second.Summary = "first", "second"
		first.UpdatedAt = a.UpdatedAt.Add(time.Second)
		second.UpdatedAt = a.UpdatedAt.Add(2 * time.Second)
		return []oracleStep{{action: &first}, {action: &second}}, "double-save"
	default:
		_ = actionsByID
		return nil, "empty"
	}
}

func (o *oracleRun) mutateAction(a domainaction.Action, attempts []domainaction.Attempt) *domainaction.Action {
	rng := o.rng
	switch rng.Intn(10) {
	case 0:
		a.RunID = o.runs[rng.Intn(len(o.runs))]
	case 1:
		a.TaskID = o.tasks[rng.Intn(len(o.tasks))]
	case 2:
		a.Kind = oracleKinds[rng.Intn(len(oracleKinds))]
	case 3:
		a.Name = oracleNames[rng.Intn(len(oracleNames))]
	case 4:
		a.Name = domainaction.NativeDelegationActionName
		a.Kind = domainaction.KindDelegation
	case 5:
		a.CurrentAttemptID = modulecore.NewAttemptID()
	case 6:
		a.CurrentAttemptID = ""
	case 7:
		if len(attempts) > 0 {
			a.CurrentAttemptID = attempts[rng.Intn(len(attempts))].AttemptID
		}
	case 8:
		done := a.CreatedAt.Add(time.Duration(1+rng.Intn(5)) * time.Second)
		a.Status, a.CompletedAt, a.UpdatedAt = domainaction.StatusCancelled, &done, done
	case 9:
		a.Status, a.CompletedAt = domainaction.StatusOpen, nil
	}
	return &a
}

func (o *oracleRun) mutateAttempt(at domainaction.Attempt, actions []domainaction.Action) *domainaction.Attempt {
	rng := o.rng
	at = at.Clone()
	switch rng.Intn(8) {
	case 0:
		if len(actions) > 0 {
			at.ActionID = actions[rng.Intn(len(actions))].ActionID
		}
	case 1:
		at.ActionID = modulecore.NewActionID()
	case 2:
		at.Status, at.CompletedAt = domainaction.AttemptStatusRunning, nil
	case 3:
		done := at.StartedAt.Add(time.Second)
		at.Status, at.CompletedAt = domainaction.AttemptStatusSucceeded, &done
	case 4:
		at.StartReason = domainaction.AttemptStartReasonFallback
	case 5:
		at.NativeDelegation = &domainaction.NativeDelegation{Mode: domainaction.NativeDelegationModeOpenStart, ExpectedCriteriaRevision: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
	case 6:
		at.NativeDelegation = nil
	case 7:
		at.Summary = fmt.Sprintf("s%d", rng.Intn(5))
	}
	return &at
}

func (o *oracleRun) apply(store *JSONLStore, steps []oracleStep, failAfter bool) error {
	return store.Transaction(o.ctx, func(tx domainaction.Store) error {
		for _, step := range steps {
			if step.action != nil {
				if err := tx.SaveAction(o.ctx, *step.action); err != nil {
					return err
				}
			}
			if step.attempt != nil {
				if err := tx.SaveAttempt(o.ctx, step.attempt.Clone()); err != nil {
					return err
				}
			}
		}
		if failAfter {
			return fmt.Errorf("callback failed after saving")
		}
		return nil
	})
}

func TestSnapshotFollowsTheFullReadOnRandomTransactions(t *testing.T) {
	seeds := 12
	steps := 80
	if testing.Short() {
		seeds, steps = 3, 40
	}
	for seed := 1; seed <= seeds; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed%d", seed), func(t *testing.T) {
			t.Parallel()
			fullRoot := filepath.Join(t.TempDir(), "full")
			snapRoot := filepath.Join(t.TempDir(), "snap")
			full, snap := openFull(t, fullRoot), openIndexed(t, snapRoot)
			ix := requireIndexed(t, snap)
			o := &oracleRun{t: t, rng: rand.New(rand.NewSource(int64(seed))), ctx: context.Background(), full: full}
			for i := 0; i < 3; i++ {
				o.tasks = append(o.tasks, modulecore.NewTaskID())
				o.runs = append(o.runs, modulecore.NewRunID())
			}
			accepted, refused := 0, 0
			outcomes := map[string]int{}
			for step := 0; step < steps; step++ {
				ops, label := o.newStep()
				failAfter := o.rng.Intn(25) == 0
				wantErr := o.apply(full, ops, failAfter)
				gotErr := o.apply(snap, ops, failAfter)
				if (wantErr == nil) != (gotErr == nil) {
					t.Fatalf("seed %d step %d (%s): the full read answered %v, the snapshot answered %v", seed, step, label, wantErr, gotErr)
				}
				if wantErr == nil && len(ops) > 0 {
					accepted++
					outcomes[label+": accepted"]++
				} else if wantErr != nil {
					refused++
					outcomes[label+": refused"]++
				}
				requireSameAnswers(t, full, snap)
				requireSameFiles(t, full, snap)
			}
			if accepted < steps/4 || refused < 2 {
				t.Fatalf("seed %d: %d accepted and %d refused transactions; the sequence exercises too little", seed, accepted, refused)
			}
			t.Logf("seed %d outcomes: %v", seed, outcomes)
			requireSnapshotEqualsLog(t, snap)
			if stats := ix.stats(); stats.Rebuilds != 0 {
				t.Fatalf("seed %d: the snapshot rebuilt itself %d times although it followed every commit", seed, stats.Rebuilds)
			}
			reopened := openIndexed(t, snapRoot)
			requireSnapshotEqualsLog(t, reopened)
			requireSameAnswers(t, full, reopened)
		})
	}
}
