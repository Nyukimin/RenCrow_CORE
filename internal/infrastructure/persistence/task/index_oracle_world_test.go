package task

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// The oracle compares the indexed store with the store that folds every record
// on every transaction (the behavior this index must reproduce). Both receive
// the same operations; every observable result and every error must match.

type obsItem struct {
	What string
	Val  any
	Err  string
}

type obsLog struct {
	root  string
	items []obsItem
}

func (l *obsLog) add(what string, val any, err error) {
	item := obsItem{What: what, Val: val}
	if err != nil {
		item.Err = strings.ReplaceAll(err.Error(), l.root, "<root>")
	}
	l.items = append(l.items, item)
}

// oraclePair holds the legacy (full fold) and indexed stores over separate roots.
type oraclePair struct {
	t          *testing.T
	rootLegacy string
	rootIndex  string
	legacy     *JSONLStore
	indexed    *JSONLStore
	// outcomes counts operations by label and whether they succeeded, so a test
	// can show the sequence exercised both the success and the error paths.
	outcomes map[string][2]int
}

func newOraclePair(t *testing.T) *oraclePair {
	t.Helper()
	p := &oraclePair{t: t, rootLegacy: t.TempDir(), rootIndex: t.TempDir(), outcomes: map[string][2]int{}}
	p.open()
	t.Cleanup(p.closeBoth)
	return p
}

func (p *oraclePair) open() {
	p.t.Helper()
	var err error
	if p.legacy, err = NewJSONLStoreWithOptions(p.rootLegacy, OpenOptions{}); err != nil {
		p.t.Fatalf("open legacy store: %v", err)
	}
	if p.indexed, err = NewJSONLStoreWithOptions(p.rootIndex, OpenOptions{Index: true}); err != nil {
		p.t.Fatalf("open indexed store: %v", err)
	}
	if _, ok := p.indexed.IndexStats(); !ok {
		p.t.Fatal("indexed store reports no index")
	}
	if _, ok := p.legacy.IndexStats(); ok {
		p.t.Fatal("legacy store unexpectedly has an index")
	}
}

func (p *oraclePair) closeBoth() {
	if p.legacy != nil {
		_ = p.legacy.Close()
	}
	if p.indexed != nil {
		_ = p.indexed.Close()
	}
}

func (p *oraclePair) reopen() {
	p.t.Helper()
	if err := p.legacy.Close(); err != nil {
		p.t.Fatalf("close legacy: %v", err)
	}
	if err := p.indexed.Close(); err != nil {
		p.t.Fatalf("close indexed: %v", err)
	}
	p.open()
}

// run executes fn against both stores and requires identical results.
func (p *oraclePair) run(label string, fn func(s *JSONLStore, log *obsLog) error) (error, error) {
	p.t.Helper()
	legacyLog := &obsLog{root: p.rootLegacy}
	indexedLog := &obsLog{root: p.rootIndex}
	legacyErr := fn(p.legacy, legacyLog)
	indexedErr := fn(p.indexed, indexedLog)
	counts := p.outcomes[label]
	if legacyErr == nil {
		counts[0]++
	} else {
		counts[1]++
	}
	p.outcomes[label] = counts
	if (legacyErr == nil) != (indexedErr == nil) {
		p.t.Fatalf("%s: error mismatch: legacy=%v indexed=%v", label, legacyErr, indexedErr)
	}
	if legacyErr != nil {
		l := strings.ReplaceAll(legacyErr.Error(), p.rootLegacy, "<root>")
		i := strings.ReplaceAll(indexedErr.Error(), p.rootIndex, "<root>")
		if l != i {
			p.t.Fatalf("%s: error text mismatch:\n legacy : %s\n indexed: %s", label, l, i)
		}
		for _, sentinel := range oracleSentinels {
			if errors.Is(legacyErr, sentinel) != errors.Is(indexedErr, sentinel) {
				p.t.Fatalf("%s: errors.Is(%v) differs: legacy=%v indexed=%v", label, sentinel, legacyErr, indexedErr)
			}
		}
	}
	compareObs(p.t, label, legacyLog.items, indexedLog.items)
	return legacyErr, indexedErr
}

var oracleSentinels = []error{
	domaintask.ErrNotFound,
	jsonlbatch.ErrRecoveryRequired,
	jsonlbatch.ErrCommitUncertain,
	ErrTaskOperationConflict,
	ErrTaskOperationNotFound,
	ErrTaskOperationReceiptCorrupt,
	ErrTaskExecutionFenceActive,
	ErrTaskExecutionFenceMismatch,
	ErrTaskExecutionFenceNotFound,
	errTaskExecutionFenceScope,
	errReadOnlyTransaction,
	ErrRecordCorrupt,
	ErrExpectedCriteriaRevisionImmutable,
	ErrNativeOPSResumeClaimImmutable,
}

func compareObs(t *testing.T, label string, legacy, indexed []obsItem) {
	t.Helper()
	if len(legacy) != len(indexed) {
		t.Fatalf("%s: %d legacy observations vs %d indexed", label, len(legacy), len(indexed))
	}
	for i := range legacy {
		if legacy[i].What != indexed[i].What || legacy[i].Err != indexed[i].Err || !reflect.DeepEqual(legacy[i].Val, indexed[i].Val) {
			lj, _ := json.Marshal(legacy[i].Val)
			ij, _ := json.Marshal(indexed[i].Val)
			t.Fatalf("%s: observation %d (%s) differs:\n legacy : err=%q val=%s\n indexed: err=%q val=%s", label, i, legacy[i].What, legacy[i].Err, lj, indexed[i].Err, ij)
		}
	}
}

// oracleWorld generates reproducible operation sequences and tracks which IDs
// exist so most operations are valid while a share are deliberately invalid.
type oracleWorld struct {
	t     *testing.T
	pair  *oraclePair
	rng   *rand.Rand
	seed  int64
	seq   uint64
	clock time.Time

	tasks   []domaintask.Task
	runs    []domaintask.Run
	opIDs   []string
	opTask  map[string]modulecore.TaskID
	opHash  map[string]string
	fenced  []modulecore.TaskID
	history []string

	lastNotification *domaintask.Notification
}

var oracleNamespace = uuid.MustParse("5e1f6c4a-0b3d-5a4e-8f77-0a1b2c3d4e5f")

func newOracleWorld(t *testing.T, seed int64) *oracleWorld {
	t.Helper()
	return &oracleWorld{
		t: t, pair: newOraclePair(t), rng: rand.New(rand.NewSource(seed)), seed: seed,
		clock:  time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),
		opTask: map[string]modulecore.TaskID{}, opHash: map[string]string{},
	}
}

func (w *oracleWorld) newUUID() string {
	w.seq++
	return uuid.NewSHA1(oracleNamespace, []byte(fmt.Sprintf("%d/%d", w.seed, w.seq))).String()
}
func (w *oracleWorld) newTaskID() modulecore.TaskID { return modulecore.TaskID("tsk_" + w.newUUID()) }
func (w *oracleWorld) newRunID() modulecore.RunID   { return modulecore.RunID("run_" + w.newUUID()) }

func pick[T any](w *oracleWorld, values []T) T { return values[w.rng.Intn(len(values))] }

var (
	oracleRoutes     = []domaintask.Route{domaintask.RouteCode, domaintask.RouteResearch, domaintask.RouteOperations, domaintask.RouteGeneral, domaintask.RouteCHAT, domaintask.RouteCODE2}
	oracleStatuses   = []domaintask.Status{domaintask.StatusQueued, domaintask.StatusRunning, domaintask.StatusWaiting, domaintask.StatusBlocked, domaintask.StatusFailed, domaintask.StatusSucceeded, domaintask.StatusCancelled}
	oraclePriorities = []domaintask.Priority{domaintask.PriorityLow, domaintask.PriorityNormal, domaintask.PriorityHigh, domaintask.PriorityCritical}
	oracleAssignees  = []string{"", "Shiro", "shiro", "SHIRO", "Kuro", "Mio", "Midori"}
	oracleModules    = []string{"", "core", "tools", "viewer"}
	oracleTitles     = []string{"Memory Promotion", "Atlas backlog", "Heartbeat sweep", "user request", "idle chat"}
)

func (w *oracleWorld) tick() time.Time {
	// Often repeat the same instant so ordering ties are exercised.
	if w.rng.Intn(3) != 0 {
		w.clock = w.clock.Add(time.Duration(w.rng.Intn(5_000_000_000)))
	}
	if w.rng.Intn(7) == 0 {
		return w.clock.In(time.FixedZone("JST", 9*3600)).Add(time.Duration(w.rng.Intn(1000)))
	}
	return w.clock
}

func (w *oracleWorld) randomTask() domaintask.Task {
	now := w.tick()
	task := domaintask.Task{
		TaskID: w.newTaskID(), Title: pick(w, oracleTitles), ModuleID: pick(w, oracleModules), Route: pick(w, oracleRoutes),
		Assignee: pick(w, oracleAssignees), Status: pick(w, oracleStatuses), Priority: pick(w, oraclePriorities),
		InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, ReadOnly: w.rng.Intn(4) == 0, CreatedAt: now, UpdatedAt: now,
	}
	if w.rng.Intn(5) == 0 {
		task.InterruptPolicy = domaintask.InterruptSilent
	}
	if task.Status == domaintask.StatusWaiting {
		task.WaitingReason = "waiting for input"
	}
	if w.rng.Intn(3) == 0 {
		started := now.Add(time.Second)
		task.StartedAt = &started
	}
	if w.rng.Intn(4) == 0 {
		finished := now.Add(time.Minute)
		task.FinishedAt = &finished
	}
	if len(w.tasks) > 0 && w.rng.Intn(5) == 0 {
		task.ParentTaskID = pick(w, w.tasks).TaskID
	}
	if len(w.tasks) > 1 && w.rng.Intn(6) == 0 {
		task.DependencyTaskIDs = []modulecore.TaskID{pick(w, w.tasks).TaskID}
	}
	if len(w.tasks) > 0 && w.rng.Intn(9) == 0 {
		task.SupersedesTaskID = pick(w, w.tasks).TaskID
	}
	if w.rng.Intn(4) == 0 {
		task.CoderRoles = []string{"coder1"}
	}
	if w.rng.Intn(3) == 0 {
		task.NextActions = []string{"check", "report"}
		task.Evidence = []string{}
		task.Summary = "summary " + w.newUUID()[:8]
	}
	return task
}

func (w *oracleWorld) note(format string, args ...any) {
	w.history = append(w.history, fmt.Sprintf(format, args...))
	if len(w.history) > 40 {
		w.history = w.history[len(w.history)-40:]
	}
}

func (w *oracleWorld) fatalf(format string, args ...any) {
	w.t.Helper()
	w.t.Fatalf("seed=%d recent ops:\n  %s\n"+format, append([]any{w.seed, strings.Join(w.history, "\n  ")}, args...)...)
}

// step performs one random operation on both stores.
func (w *oracleWorld) step() {
	w.t.Helper()
	ctx := context.Background()
	switch n := w.rng.Intn(100); {
	case n < 14 || len(w.tasks) == 0:
		task := w.randomTask()
		w.note("SaveTask new %s", task.TaskID)
		legacyErr, _ := w.pair.run("SaveTask(new)", func(s *JSONLStore, _ *obsLog) error { return s.SaveTask(ctx, task) })
		if legacyErr == nil {
			w.tasks = append(w.tasks, task)
		}
	case n < 28:
		idx := w.rng.Intn(len(w.tasks))
		task := w.tasks[idx]
		w.tasks[idx] = w.mutate(task)
		w.note("SaveTask update %s", task.TaskID)
		legacyErr, _ := w.pair.run("SaveTask(update)", func(s *JSONLStore, _ *obsLog) error { return s.SaveTask(ctx, w.tasks[idx]) })
		if legacyErr != nil {
			w.tasks[idx] = task
		}
	case n < 40:
		w.opStartRun()
	case n < 50:
		w.opCloseRun()
	case n < 54:
		w.opBadRun()
	case n < 62:
		w.opContext()
	case n < 70:
		w.opNotification()
	case n < 80:
		w.opTransaction()
	case n < 86:
		w.opIdempotent()
	case n < 90:
		w.opFence()
	case n < 92:
		w.note("reopen")
		w.pair.reopen()
	default:
		w.compareSample(6)
	}
}

func (w *oracleWorld) mutate(task domaintask.Task) domaintask.Task {
	task.UpdatedAt = w.tick()
	if task.UpdatedAt.Before(task.CreatedAt) && w.rng.Intn(3) != 0 {
		task.UpdatedAt = task.CreatedAt.Add(time.Second)
	}
	if w.rng.Intn(2) == 0 {
		task.Status = pick(w, oracleStatuses)
	}
	if w.rng.Intn(3) == 0 {
		task.Assignee = pick(w, oracleAssignees)
	}
	if w.rng.Intn(4) == 0 {
		task.ModuleID = pick(w, oracleModules)
	}
	if w.rng.Intn(5) == 0 {
		task.Route = pick(w, oracleRoutes)
	}
	if task.Status == domaintask.StatusWaiting && task.WaitingReason == "" {
		task.WaitingReason = "waiting"
	}
	if w.rng.Intn(3) == 0 {
		finished := task.UpdatedAt
		task.FinishedAt = &finished
	}
	return task
}

func (w *oracleWorld) newRun(task domaintask.Task, generation uint64) domaintask.Run {
	return domaintask.Run{
		WriterGeneration: generation, RunID: w.newRunID(), TaskID: task.TaskID,
		StartReason: pick(w, []domaintask.RunStartReason{domaintask.RunStartReasonFirst, domaintask.RunStartReasonExplicitRerun, domaintask.RunStartReasonLeaseReacquire}),
		Assignee:    pick(w, oracleAssignees), Status: domaintask.RunStatusRunning, StartedAt: w.tick(),
	}
}

func (w *oracleWorld) opStartRun() {
	ctx := context.Background()
	task := pick(w, w.tasks)
	run := w.newRun(task, 0)
	w.note("SaveRun start task=%s run=%s", task.TaskID, run.RunID)
	var saved domaintask.Run
	legacyErr, _ := w.pair.run("SaveRun(start)", func(s *JSONLStore, _ *obsLog) error {
		generation, err := s.WriterGeneration()
		if err != nil {
			return err
		}
		candidate := run
		candidate.WriterGeneration = generation
		saved = candidate
		return s.SaveRun(ctx, candidate)
	})
	if legacyErr == nil {
		w.runs = append(w.runs, saved)
	}
}

func (w *oracleWorld) opCloseRun() {
	ctx := context.Background()
	if len(w.runs) == 0 {
		return
	}
	idx := w.rng.Intn(len(w.runs))
	run := w.runs[idx]
	closed := run
	closed.Status = pick(w, []domaintask.RunStatus{domaintask.RunStatusSucceeded, domaintask.RunStatusFailed, domaintask.RunStatusWaiting, domaintask.RunStatusInterrupted, domaintask.RunStatusRunning})
	if closed.Status != domaintask.RunStatusRunning {
		completed := run.StartedAt.Add(time.Duration(1+w.rng.Intn(1000)) * time.Millisecond)
		closed.CompletedAt = &completed
		closed.Summary = "done " + w.newUUID()[:6]
	}
	w.note("SaveRun update run=%s -> %s", run.RunID, closed.Status)
	legacyErr, _ := w.pair.run("SaveRun(update)", func(s *JSONLStore, _ *obsLog) error { return s.SaveRun(ctx, closed) })
	if legacyErr == nil {
		w.runs[idx] = closed
	}
}

func (w *oracleWorld) opBadRun() {
	ctx := context.Background()
	task := pick(w, w.tasks)
	switch w.rng.Intn(4) {
	case 0: // run for a task that does not exist
		missing := domaintask.Task{TaskID: modulecore.TaskID("tsk_" + uuid.NewSHA1(oracleNamespace, []byte("missing")).String())}
		run := w.newRun(missing, 0)
		w.note("SaveRun for unknown task")
		w.pair.run("SaveRun(unknown task)", func(s *JSONLStore, _ *obsLog) error {
			candidate := run
			candidate.WriterGeneration, _ = s.WriterGeneration()
			return s.SaveRun(ctx, candidate)
		})
	case 1: // stale writer generation
		run := w.newRun(task, 0)
		w.note("SaveRun with wrong generation")
		w.pair.run("SaveRun(generation)", func(s *JSONLStore, _ *obsLog) error {
			candidate := run
			generation, _ := s.WriterGeneration()
			candidate.WriterGeneration = generation + 7
			return s.SaveRun(ctx, candidate)
		})
	case 2: // changing an immutable field of an existing run
		if len(w.runs) == 0 {
			return
		}
		run := pick(w, w.runs)
		run.Assignee = "someone-else"
		w.note("SaveRun immutable change run=%s", run.RunID)
		w.pair.run("SaveRun(immutable)", func(s *JSONLStore, _ *obsLog) error { return s.SaveRun(ctx, run) })
	default: // invalid task content
		bad := task
		bad.UpdatedAt = bad.CreatedAt.Add(-time.Hour)
		w.note("SaveTask invalid updated_at %s", bad.TaskID)
		w.pair.run("SaveTask(invalid)", func(s *JSONLStore, _ *obsLog) error { return s.SaveTask(ctx, bad) })
	}
}

func (w *oracleWorld) opContext() {
	ctx := context.Background()
	taskID := pick(w, w.tasks).TaskID
	if w.rng.Intn(8) == 0 {
		taskID = modulecore.TaskID("tsk_" + uuid.NewSHA1(oracleNamespace, []byte("no-such-task")).String())
	}
	value := domaintask.SharedRoleContext{TaskID: taskID, CurrentPlan: "plan " + w.newUUID()[:6], UpdatedAt: w.tick()}
	if w.rng.Intn(2) == 0 {
		value.RelevantFiles = []string{"a.go", "b.go"}
		value.Decisions = []string{}
	}
	w.note("SaveContext %s", taskID)
	w.pair.run("SaveContext", func(s *JSONLStore, _ *obsLog) error { return s.SaveContext(ctx, value) })
}

func (w *oracleWorld) opNotification() {
	ctx := context.Background()
	task := pick(w, w.tasks)
	value := domaintask.NewNotification(task, w.tick())
	value.Interrupt = w.rng.Intn(2) == 0
	if w.rng.Intn(10) == 0 {
		value.TaskID = modulecore.TaskID("tsk_" + uuid.NewSHA1(oracleNamespace, []byte(fmt.Sprintf("orphan-notification-%d", w.rng.Intn(3)))).String())
	}
	if w.rng.Intn(15) == 0 {
		value.CreatedAt = time.Time{}
	}
	// Repeat the previous notification's Task and time so equal sort keys occur
	// and the order between them (log order) is exercised.
	if w.rng.Intn(3) == 0 && w.lastNotification != nil {
		value.TaskID = w.lastNotification.TaskID
		value.CreatedAt = w.lastNotification.CreatedAt
		value.Summary = "tie " + w.newUUID()[:6]
	}
	saved := value
	w.lastNotification = &saved
	w.note("SaveNotification %s", value.TaskID)
	w.pair.run("SaveNotification", func(s *JSONLStore, _ *obsLog) error { return s.SaveNotification(ctx, value) })
}

// readsInside records the reads a callback can make so the transaction view is
// compared with the legacy fold view, and returns them for a post-commit check.
func (w *oracleWorld) readsInside(ctx context.Context, tx domaintask.Store, log *obsLog, tag string, task domaintask.Task, runID modulecore.RunID) {
	got, err := tx.GetTask(ctx, task.TaskID)
	log.add(tag+" GetTask", got, err)
	runs, err := tx.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	log.add(tag+" ListRuns(task)", runs, err)
	running, err := tx.ListRuns(ctx, domaintask.RunFilter{Status: domaintask.RunStatusRunning, Limit: 4})
	log.add(tag+" ListRuns(running)", running, err)
	if runID != "" {
		run, err := tx.GetRun(ctx, runID)
		log.add(tag+" GetRun", run, err)
	}
	value, err := tx.GetContext(ctx, task.TaskID)
	log.add(tag+" GetContext", value, err)
	list, err := tx.ListTasks(ctx, domaintask.Filter{Assignee: "shiro", Limit: 5})
	log.add(tag+" ListTasks(shiro)", list, err)
	notes, err := tx.ListNotifications(ctx, 4, false)
	log.add(tag+" ListNotifications", notes, err)
	_ = tx.ReadTransaction(ctx, func(view domaintask.Store) error {
		again, err := view.ListTasks(ctx, domaintask.Filter{Limit: 3})
		log.add(tag+" nested ListTasks", again, err)
		return nil
	})
}

func (w *oracleWorld) opTransaction() {
	ctx := context.Background()
	task := pick(w, w.tasks)
	var closeExisting *domaintask.Run
	if w.rng.Intn(2) == 0 {
		for _, run := range w.runs {
			if run.Status == domaintask.RunStatusRunning {
				for _, candidate := range w.tasks {
					if candidate.TaskID == run.TaskID {
						task = candidate
						done := run
						completed := run.StartedAt.Add(2 * time.Second)
						done.Status = domaintask.RunStatusFailed
						done.CompletedAt = &completed
						done.Summary = "closed in transaction"
						closeExisting = &done
					}
				}
				break
			}
		}
	}
	fresh := w.randomTask()
	freshRun := "run_" + w.newUUID()
	scoped := w.rng.Intn(2) == 0
	failAtEnd := w.rng.Intn(5) == 0
	orderSwap := w.rng.Intn(4) == 0
	updated := w.mutate(task)
	w.note("Transaction scoped=%t task=%s fail=%t swap=%t", scoped, task.TaskID, failAtEnd, orderSwap)
	callbackFor := func(s *JSONLStore, log *obsLog, target domaintask.Task) func(domaintask.Store) error {
		return func(tx domaintask.Store) error {
			generation, err := tx.WriterGeneration()
			log.add("tx WriterGeneration", generation, err)
			w.readsInside(ctx, tx, log, "before", target, "")
			if closeExisting != nil {
				log.add("tx SaveRun(close committed)", nil, tx.SaveRun(ctx, *closeExisting))
				w.readsInside(ctx, tx, log, "closed-existing", target, closeExisting.RunID)
			}
			log.add("tx SaveTask(update)", nil, tx.SaveTask(ctx, updated))
			note := domaintask.NewNotification(updated, updated.UpdatedAt)
			log.add("tx SaveNotification", nil, tx.SaveNotification(ctx, note))
			run := domaintask.Run{WriterGeneration: generation, RunID: modulecore.RunID(freshRun), TaskID: target.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: updated.UpdatedAt}
			if orderSwap {
				log.add("tx SaveContext", nil, tx.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: target.TaskID, CurrentPlan: "in tx", UpdatedAt: updated.UpdatedAt}))
				log.add("tx SaveRun", nil, tx.SaveRun(ctx, run))
			} else {
				log.add("tx SaveRun", nil, tx.SaveRun(ctx, run))
				log.add("tx SaveContext", nil, tx.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: target.TaskID, CurrentPlan: "in tx", UpdatedAt: updated.UpdatedAt}))
			}
			if !scoped {
				log.add("tx SaveTask(new)", nil, tx.SaveTask(ctx, fresh))
				log.add("tx SaveContext(new)", nil, tx.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: fresh.TaskID, CurrentPlan: "new", UpdatedAt: fresh.UpdatedAt}))
			}
			w.readsInside(ctx, tx, log, "after", updated, run.RunID)
			if !scoped {
				w.readsInside(ctx, tx, log, "after-new", fresh, "")
			}
			closed := run
			completed := run.StartedAt.Add(time.Second)
			closed.Status = domaintask.RunStatusSucceeded
			closed.CompletedAt = &completed
			log.add("tx SaveRun(close)", nil, tx.SaveRun(ctx, closed))
			w.readsInside(ctx, tx, log, "closed", updated, run.RunID)
			if failAtEnd {
				return errors.New("callback failed after writes")
			}
			return nil
		}
	}
	legacyErr, _ := w.pair.run("Transaction", func(s *JSONLStore, log *obsLog) error {
		if scoped {
			return s.TaskTransaction(ctx, task.TaskID, callbackFor(s, log, task))
		}
		return s.Transaction(ctx, callbackFor(s, log, task))
	})
	if legacyErr == nil {
		// Refresh the model from the legacy store (the source of truth).
		for i := range w.tasks {
			if got, err := w.pair.legacy.GetTask(ctx, w.tasks[i].TaskID); err == nil {
				w.tasks[i] = got
			}
		}
		if !scoped {
			if got, err := w.pair.legacy.GetTask(ctx, fresh.TaskID); err == nil {
				w.tasks = append(w.tasks, got)
			}
		}
		if runs, err := w.pair.legacy.ListRuns(ctx, domaintask.RunFilter{}); err == nil {
			w.runs = runs
		}
	}
}

func (w *oracleWorld) opIdempotent() {
	ctx := context.Background()
	task := pick(w, w.tasks)
	var operationID string
	if len(w.opIDs) > 0 && w.rng.Intn(2) == 0 {
		operationID = pick(w, w.opIDs)
	} else {
		operationID = "op:" + w.newUUID()[:12]
	}
	hash := taskOperationHash("request " + operationID)
	if known, ok := w.opHash[operationID]; ok && w.rng.Intn(3) != 0 {
		hash = known
	} else if ok {
		hash = taskOperationHash("different request")
	}
	opTask := task.TaskID
	if known, ok := w.opTask[operationID]; ok && w.rng.Intn(4) != 0 {
		opTask = known
	}
	global := w.rng.Intn(4) == 0
	w.note("Idempotent op=%s task=%s global=%t", operationID, opTask, global)
	legacyErr, _ := w.pair.run("Idempotent", func(s *JSONLStore, log *obsLog) error {
		callback := func(tx domaintask.Store) (json.RawMessage, error) {
			current, err := tx.GetTask(ctx, opTask)
			log.add("op GetTask", current, err)
			if err != nil {
				return nil, err
			}
			current.UpdatedAt = current.UpdatedAt.Add(time.Second)
			current.Summary = "operation " + operationID
			if err := tx.SaveTask(ctx, current); err != nil {
				return nil, err
			}
			log.add("op ListTasks", func() any { v, _ := tx.ListTasks(ctx, domaintask.Filter{Limit: 3}); return v }(), nil)
			return json.RawMessage(`{"ok":true}`), nil
		}
		var result json.RawMessage
		var err error
		if global {
			result, err = s.ExecuteIdempotentGlobalTaskOperation(ctx, operationID, hash, callback)
		} else {
			result, err = s.ExecuteIdempotentTaskOperation(ctx, opTask, operationID, hash, callback)
		}
		log.add("op result", string(result), nil)
		return err
	})
	if legacyErr == nil {
		if _, ok := w.opHash[operationID]; !ok {
			w.opIDs = append(w.opIDs, operationID)
			w.opHash[operationID] = hash
			if global {
				w.opTask[operationID] = ""
			} else {
				w.opTask[operationID] = opTask
			}
		}
		if got, err := w.pair.legacy.GetTask(ctx, opTask); err == nil {
			for i := range w.tasks {
				if w.tasks[i].TaskID == opTask {
					w.tasks[i] = got
				}
			}
		}
	}
}

func (w *oracleWorld) opFence() {
	ctx := context.Background()
	task := pick(w, w.tasks)
	fenceID := "fence:" + w.newUUID()[:10]
	w.note("Fence acquire/release task=%s", task.TaskID)
	w.pair.run("AcquireTaskExecutionFence", func(s *JSONLStore, log *obsLog) error {
		err := s.AcquireTaskExecutionFence(ctx, task.TaskID, fenceID)
		status, getErr := s.GetTaskExecutionFence(ctx, task.TaskID)
		log.add("fence status", status, getErr)
		return err
	})
	w.fenced = append(w.fenced, task.TaskID)
	// While the fence is active a Task write must be refused identically.
	w.pair.run("SaveTask(fenced)", func(s *JSONLStore, _ *obsLog) error {
		current, err := s.GetTask(ctx, task.TaskID)
		if err != nil {
			return err
		}
		current.UpdatedAt = current.UpdatedAt.Add(time.Second)
		return s.SaveTask(ctx, current)
	})
	w.pair.run("ReleaseTaskExecutionFence(wrong)", func(s *JSONLStore, _ *obsLog) error {
		return s.ReleaseTaskExecutionFence(ctx, task.TaskID, "fence:other")
	})
	w.pair.run("ReleaseTaskExecutionFence", func(s *JSONLStore, log *obsLog) error {
		err := s.ReleaseTaskExecutionFence(ctx, task.TaskID, fenceID)
		status, getErr := s.GetTaskExecutionFence(ctx, task.TaskID)
		log.add("fence status", status, getErr)
		return err
	})
}

// compare reads everything the stores expose and requires identical results.
func (w *oracleWorld) compare() { w.compareSample(0) }

// compareSample compares the lists and every sampled ID; sample 0 means every
// known ID. The legacy store folds the whole log per read, so a mid-sequence
// check samples a few IDs and the final check covers them all.
func (w *oracleWorld) compareSample(sample int) {
	w.t.Helper()
	ctx := context.Background()
	w.note("compare sample=%d", sample)
	tasks, runs := w.tasks, w.runs
	if sample > 0 {
		tasks = nil
		for i := 0; i < sample && len(w.tasks) > 0; i++ {
			tasks = append(tasks, w.tasks[w.rng.Intn(len(w.tasks))])
		}
		runs = nil
		for i := 0; i < sample && len(w.runs) > 0; i++ {
			runs = append(runs, w.runs[w.rng.Intn(len(w.runs))])
		}
	}
	w.pair.run("compare", func(s *JSONLStore, log *obsLog) error {
		for _, task := range tasks {
			got, err := s.GetTask(ctx, task.TaskID)
			log.add("GetTask "+string(task.TaskID), got, err)
			value, err := s.GetContext(ctx, task.TaskID)
			log.add("GetContext "+string(task.TaskID), value, err)
			runs, err := s.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			log.add("ListRuns(task) "+string(task.TaskID), runs, err)
		}
		for _, run := range runs {
			got, err := s.GetRun(ctx, run.RunID)
			log.add("GetRun "+string(run.RunID), got, err)
		}
		missingTask := modulecore.TaskID("tsk_" + uuid.NewSHA1(oracleNamespace, []byte("never-created")).String())
		_, err := s.GetTask(ctx, missingTask)
		log.add("GetTask(missing)", nil, err)
		_, err = s.GetRun(ctx, modulecore.RunID("run_"+uuid.NewSHA1(oracleNamespace, []byte("never-created-run")).String()))
		log.add("GetRun(missing)", nil, err)
		_, err = s.GetContext(ctx, missingTask)
		log.add("GetContext(missing)", nil, err)
		// A syntactically valid but non-canonical spelling is simply not found.
		upper := modulecore.TaskID(strings.ToUpper(string(missingTask)[:4]) + string(missingTask)[4:])
		_, err = s.GetTask(ctx, upper)
		log.add("GetTask(bad prefix)", nil, err)

		for _, filter := range []domaintask.Filter{
			{}, {Limit: 1}, {Limit: 7}, {Status: domaintask.StatusRunning}, {Status: domaintask.StatusSucceeded, Limit: 3},
			{ModuleID: "core"}, {ModuleID: "tools", Limit: 2}, {Assignee: "shiro"}, {Assignee: "SHIRO", Limit: 4}, {Assignee: "nobody"},
			{Route: domaintask.RouteCode}, {Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued},
			{Status: domaintask.StatusRunning, Assignee: "kuro", ModuleID: "viewer", Route: domaintask.RouteResearch},
			{Limit: 100000}, {Status: domaintask.Status("unknown")},
		} {
			list, err := s.ListTasks(ctx, filter)
			log.add(fmt.Sprintf("ListTasks %+v", filter), list, err)
		}
		runFilters := []domaintask.RunFilter{{}, {Limit: 2}, {Status: domaintask.RunStatusRunning}, {Status: domaintask.RunStatusSucceeded, Limit: 3}, {Status: domaintask.RunStatusFailed}, {Status: domaintask.RunStatus("bogus")}, {TaskID: modulecore.TaskID("bogus")}, {TaskID: missingTask}}
		if len(w.tasks) > 0 {
			runFilters = append(runFilters, domaintask.RunFilter{TaskID: w.tasks[0].TaskID, Status: domaintask.RunStatusRunning}, domaintask.RunFilter{TaskID: w.tasks[len(w.tasks)-1].TaskID, Limit: 1})
		}
		for _, filter := range runFilters {
			list, err := s.ListRuns(ctx, filter)
			log.add(fmt.Sprintf("ListRuns %+v", filter), list, err)
		}
		for _, limit := range []int{0, 1, 3, 100} {
			for _, interruptOnly := range []bool{false, true} {
				list, err := s.ListNotifications(ctx, limit, interruptOnly)
				log.add(fmt.Sprintf("ListNotifications %d %t", limit, interruptOnly), list, err)
			}
		}
		for _, operationID := range append(append([]string(nil), w.opIDs...), "op:unknown") {
			receipt, err := s.LookupTaskOperation(ctx, operationID)
			log.add("LookupTaskOperation "+operationID, receipt, err)
		}
		for _, taskID := range w.fenced {
			status, err := s.GetTaskExecutionFence(ctx, taskID)
			log.add("GetTaskExecutionFence "+string(taskID), status, err)
		}
		generation, err := s.WriterGeneration()
		log.add("WriterGeneration", generation, err)
		return nil
	})
}
