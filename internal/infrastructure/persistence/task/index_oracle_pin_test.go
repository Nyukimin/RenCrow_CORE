package task

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

// The operations below put pinned Tasks (first-save criteria revision) and
// accepted OPS Tasks with native Resume claims into the oracle sequences, with
// the writes the pin rules refuse. The full-fold store and the indexed store must
// accept and refuse exactly the same ones, with the same error.

func (w *oracleWorld) uid(label string) string { return w.newUUID() + "-" + label }

// pinUID adapts newUUID to the fixture builders, which need canonical UUIDs.
func (w *oracleWorld) pinUID() uidFunc { return func(string) string { return w.newUUID() } }

func (w *oracleWorld) revision() string {
	return strings.ReplaceAll(w.newUUID()+w.newUUID(), "-", "")[:64]
}

// pinnedTasks are the model's Tasks that carry a criteria revision.
func (w *oracleWorld) pinnedTasks(wantOPS bool) []int {
	var out []int
	for i, task := range w.tasks {
		if task.ExpectedCriteriaRevision == "" {
			continue
		}
		if wantOPS && task.AcceptedOPSClaim == nil {
			continue
		}
		out = append(out, i)
	}
	return out
}

func (w *oracleWorld) saveModelTask(label string, task domaintask.Task, want error) error {
	ctx := context.Background()
	legacyErr, indexedErr := w.pair.run(label, func(s *JSONLStore, _ *obsLog) error { return s.SaveTask(ctx, task) })
	if want != nil && !errors.Is(legacyErr, want) {
		w.fatalf("%s: legacy error = %v, want %v (indexed %v)", label, legacyErr, want, indexedErr)
	}
	if want == nil && legacyErr != nil {
		w.fatalf("%s: legacy store refused a Task the sequence expected to be valid: %v", label, legacyErr)
	}
	return legacyErr
}

func (w *oracleWorld) opPin() {
	ctx := context.Background()
	switch w.rng.Intn(9) {
	case 0: // a new Task pinned by its first save
		task := w.randomTask()
		task.ExpectedCriteriaRevision = w.revision()
		w.note("SaveTask new pinned %s", task.TaskID)
		if w.saveModelTask("SaveTask(pin new)", task, nil) == nil {
			w.tasks = append(w.tasks, task)
		}
	case 1: // a new accepted OPS Task
		task := acceptedOPSTestTask(w.randomTask(), w.pinUID(), w.revision())
		w.note("SaveTask new accepted OPS %s", task.TaskID)
		if w.saveModelTask("SaveTask(ops new)", task, nil) == nil {
			w.tasks = append(w.tasks, task)
		}
	case 2: // change, clear or backfill the criteria revision
		idx := w.rng.Intn(len(w.tasks))
		task := w.tasks[idx]
		next := bumped(task)
		switch {
		case task.ExpectedCriteriaRevision == "":
			next.ExpectedCriteriaRevision = w.revision()
		case len(task.NativeResumeClaims) == 0 && w.rng.Intn(2) == 0: // claims require a revision, so a Task with claims is only re-pinned
			next.ExpectedCriteriaRevision = ""
		default:
			next.ExpectedCriteriaRevision = w.revision()
		}
		w.note("SaveTask pin violation %s", task.TaskID)
		w.saveModelTask("SaveTask(pin violation)", next, ErrExpectedCriteriaRevisionImmutable)
	case 3: // append a Resume claim
		ops := w.pinnedTasks(true)
		if len(ops) == 0 {
			return
		}
		idx := ops[w.rng.Intn(len(ops))]
		task := w.tasks[idx]
		next := bumped(task)
		next.NativeResumeClaims = append(append([]domaintask.NativeOPSResumeClaim(nil), task.NativeResumeClaims...), resumeTestClaim(task, w.pinUID(), w.tick()))
		w.note("SaveTask claim append %s", task.TaskID)
		if w.saveModelTask("SaveTask(claim append)", next, nil) == nil {
			w.tasks[idx] = next
		}
	case 4: // rewrite, drop or reorder the claims already stored
		var withClaims []int
		for _, i := range w.pinnedTasks(true) {
			if len(w.tasks[i].NativeResumeClaims) > 0 {
				withClaims = append(withClaims, i)
			}
		}
		if len(withClaims) == 0 {
			return
		}
		task := w.tasks[withClaims[w.rng.Intn(len(withClaims))]]
		next := bumped(task)
		next.NativeResumeClaims = append([]domaintask.NativeOPSResumeClaim(nil), task.NativeResumeClaims...)
		switch w.rng.Intn(3) {
		case 0:
			next.NativeResumeClaims[0].PayloadSHA256 = strings.Repeat("9", 64)
		case 1:
			next.NativeResumeClaims = nil
		default:
			next.NativeResumeClaims = append(next.NativeResumeClaims, resumeTestClaim(task, w.pinUID(), w.tick()))
			last := len(next.NativeResumeClaims) - 1
			next.NativeResumeClaims[0], next.NativeResumeClaims[last] = next.NativeResumeClaims[last], next.NativeResumeClaims[0]
		}
		w.note("SaveTask claim rewrite %s", task.TaskID)
		w.saveModelTask("SaveTask(claim rewrite)", next, ErrNativeOPSResumeClaimImmutable)
	case 5: // a Task cannot be first saved with Resume claims
		task := acceptedOPSTestTask(w.randomTask(), w.pinUID(), w.revision())
		task = withClaims(task, resumeTestClaim(task, w.pinUID(), w.tick()))
		w.note("SaveTask new with claims %s", task.TaskID)
		w.saveModelTask("SaveTask(claim on first save)", task, ErrNativeOPSResumeClaimImmutable)
	case 6: // one transaction: first pin wins, claims append and cannot be rewritten
		task := acceptedOPSTestTask(w.randomTask(), w.pinUID(), w.revision())
		other := bumped(task)
		other.ExpectedCriteriaRevision = w.revision()
		claim := resumeTestClaim(task, w.pinUID(), w.tick())
		appended := bumped(bumped(withClaims(task, claim)))
		rewritten := claim
		rewritten.RequestID = "rewritten-" + w.uid("r")[:8]
		rewrittenTask := bumped(bumped(bumped(withClaims(task, rewritten))))
		newWithClaims := acceptedOPSTestTask(w.randomTask(), w.pinUID(), w.revision())
		newWithClaims = withClaims(newWithClaims, resumeTestClaim(newWithClaims, w.pinUID(), w.tick()))
		w.note("Transaction pins %s", task.TaskID)
		legacyErr, _ := w.pair.run("Transaction(pins)", func(s *JSONLStore, log *obsLog) error {
			return s.Transaction(ctx, func(tx domaintask.Store) error {
				log.add("tx SaveTask(first pin)", nil, tx.SaveTask(ctx, task))
				log.add("tx SaveTask(other pin)", nil, tx.SaveTask(ctx, other))
				log.add("tx SaveTask(claim)", nil, tx.SaveTask(ctx, appended))
				log.add("tx SaveTask(rewrite)", nil, tx.SaveTask(ctx, rewrittenTask))
				log.add("tx SaveTask(new with claims)", nil, tx.SaveTask(ctx, newWithClaims))
				got, err := tx.GetTask(ctx, task.TaskID)
				log.add("tx GetTask", got, err)
				list, err := tx.ListTasks(ctx, domaintask.Filter{Assignee: "shiro", Limit: 4})
				log.add("tx ListTasks", list, err)
				return nil
			})
		})
		if legacyErr == nil {
			if got, err := w.pair.legacy.GetTask(ctx, task.TaskID); err == nil {
				w.tasks = append(w.tasks, got)
			}
		}
	case 7: // the same rules through a Task-scoped transaction
		ops := w.pinnedTasks(true)
		if len(ops) == 0 {
			return
		}
		idx := ops[w.rng.Intn(len(ops))]
		task := w.tasks[idx]
		next := bumped(task)
		next.NativeResumeClaims = append(append([]domaintask.NativeOPSResumeClaim(nil), task.NativeResumeClaims...), resumeTestClaim(task, w.pinUID(), w.tick()))
		violating := bumped(bumped(task))
		violating.ExpectedCriteriaRevision = w.revision()
		w.note("TaskTransaction claim %s", task.TaskID)
		legacyErr, _ := w.pair.run("TaskTransaction(claim)", func(s *JSONLStore, log *obsLog) error {
			return s.TaskTransaction(ctx, task.TaskID, func(tx domaintask.Store) error {
				current, err := tx.GetTask(ctx, task.TaskID)
				log.add("tx GetTask", current, err)
				log.add("tx SaveTask(violating)", nil, tx.SaveTask(ctx, violating))
				return tx.SaveTask(ctx, next)
			})
		})
		if legacyErr == nil {
			w.tasks[idx] = next
		}
	default: // an update that keeps every pinned field leaves pins and claims alone
		pinned := w.pinnedTasks(false)
		if len(pinned) == 0 {
			return
		}
		idx := pinned[w.rng.Intn(len(pinned))]
		task := w.tasks[idx]
		next := bumped(task)
		next.Summary = "pinned update " + w.uid("u")[:8]
		w.note("SaveTask pinned update %s", task.TaskID)
		if w.saveModelTask("SaveTask(pinned update)", next, nil) == nil {
			w.tasks[idx] = next
		}
	}
}

// TestOracleExercisesPinsAndClaims shows the pin operations reach both outcomes
// (accepted and refused) and that the stores agree on every one.
func TestOracleExercisesPinsAndClaims(t *testing.T) {
	steps := 150
	if testing.Short() {
		steps = 70
	}
	for seed := int64(101); seed <= 102; seed++ {
		seed := seed
		t.Run(fmt.Sprintf("seed-%d", seed), func(t *testing.T) {
			world := newOracleWorld(t, seed)
			for i := 0; i < steps; i++ {
				if i%2 == 0 || len(world.tasks) == 0 {
					world.opPinOrSeed()
				} else {
					world.step()
				}
			}
			world.compare()
			world.pair.reopen()
			world.compare()
			for i := 0; i < steps/2; i++ {
				if i%2 == 0 {
					world.opPinOrSeed()
				} else {
					world.step()
				}
			}
			world.compare()
			world.pair.reopen()
			world.compare()
			for _, label := range []string{"SaveTask(pin new)", "SaveTask(ops new)", "SaveTask(claim append)", "SaveTask(pinned update)", "TaskTransaction(claim)", "Transaction(pins)"} {
				if world.pair.outcomes[label][0] == 0 {
					t.Errorf("%s never succeeded: %v", label, world.pair.outcomes)
				}
			}
			for _, label := range []string{"SaveTask(pin violation)", "SaveTask(claim rewrite)", "SaveTask(claim on first save)"} {
				if world.pair.outcomes[label][1] == 0 {
					t.Errorf("%s never refused: %v", label, world.pair.outcomes)
				}
			}
		})
	}
}

// opPinOrSeed runs a pin operation, creating the pinned Tasks it needs first.
func (w *oracleWorld) opPinOrSeed() {
	if len(w.pinnedTasks(true)) == 0 && w.rng.Intn(2) == 0 {
		task := acceptedOPSTestTask(w.randomTask(), w.pinUID(), w.revision())
		if w.saveModelTask("SaveTask(ops new)", task, nil) == nil {
			w.tasks = append(w.tasks, task)
		}
		return
	}
	w.opPin()
}
