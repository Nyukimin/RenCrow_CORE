package taskmanager

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const checkpointTestDigest = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newCheckpointFixture(t *testing.T, status domaintask.Status) (*Manager, *taskpersistence.JSONLStore, domaintask.Task, domaintask.Run) {
	t.Helper()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, predecessor := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.CompleteRun(context.Background(), task.TaskID, predecessor.RunID, "Mio", status, "checkpoint", "checkpoint persisted"); err != nil {
		t.Fatalf("complete predecessor: %v", err)
	}
	return manager, store, task, predecessor
}

func TestManagerStartRunFromCheckpointPersistsExactDigest(t *testing.T) {
	ctx := context.Background()
	manager, _, task, predecessor := newCheckpointFixture(t, domaintask.StatusWaiting)
	run, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest)
	if err != nil {
		t.Fatalf("StartRunFromCheckpoint: %v", err)
	}
	if run.TaskID != task.TaskID || run.RunID == predecessor.RunID || run.Assignee != "Mio" || run.StartReason != domaintask.RunStartReasonCheckpointResume || run.Status != domaintask.RunStatusRunning || run.StartCheckpointSHA256 != checkpointTestDigest {
		t.Fatalf("issued Run = %#v", run)
	}
	persisted, err := manager.GetRun(ctx, run.RunID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(persisted, run) {
		t.Fatalf("returned/persisted Run mismatch: returned=%#v persisted=%#v", run, persisted)
	}
}

func TestManagerStartRunFromCheckpointSupportsExplicitRerunOnlyForTerminalPredecessor(t *testing.T) {
	ctx := context.Background()
	manager, _, task, predecessor := newCheckpointFixture(t, domaintask.StatusSucceeded)
	run, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonExplicitRerun, checkpointTestDigest)
	if err != nil {
		t.Fatalf("StartRunFromCheckpoint explicit rerun: %v", err)
	}
	if run.StartReason != domaintask.RunStartReasonExplicitRerun || run.StartCheckpointSHA256 != checkpointTestDigest {
		t.Fatalf("explicit rerun = %#v", run)
	}
}

func TestManagerStartRunFromCheckpointRejectsActorDigestReasonAndStaleCASWithoutWrites(t *testing.T) {
	ctx := context.Background()
	manager, _, task, predecessor := newCheckpointFixture(t, domaintask.StatusWaiting)
	beforeTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		actor  string
		reason domaintask.RunStartReason
		digest string
	}{
		{name: "wrong_actor", actor: "Shiro", reason: domaintask.RunStartReasonCheckpointResume, digest: checkpointTestDigest},
		{name: "wrong_reason", actor: "Mio", reason: domaintask.RunStartReasonExplicitRerun, digest: checkpointTestDigest},
		{name: "empty_digest", actor: "Mio", reason: domaintask.RunStartReasonCheckpointResume, digest: ""},
		{name: "uppercase_digest", actor: "Mio", reason: domaintask.RunStartReasonCheckpointResume, digest: "0123456789ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef"},
		{name: "short_digest", actor: "Mio", reason: domaintask.RunStartReasonCheckpointResume, digest: "0123456789abcdef"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, test.actor, test.reason, test.digest); err == nil {
				t.Fatal("invalid checkpoint start was accepted")
			} else if !errors.Is(err, ErrRunConflict) && test.name != "empty_digest" && test.name != "uppercase_digest" && test.name != "short_digest" {
				t.Fatalf("error = %v, want ErrRunConflict", err)
			}
			afterTask, getErr := manager.Get(ctx, task.TaskID)
			if getErr != nil {
				t.Fatal(getErr)
			}
			afterRuns, listErr := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if listErr != nil {
				t.Fatal(listErr)
			}
			if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
				t.Fatalf("rejected start changed records: before task=%#v runs=%#v after task=%#v runs=%#v", beforeTask, beforeRuns, afterTask, afterRuns)
			}
		})
	}

	issued, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest)
	if err != nil {
		t.Fatalf("first CAS start: %v", err)
	}
	beforeTask, err = manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err = manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("stale predecessor error = %v, want ErrRunConflict", err)
	}
	afterTask, err := manager.Get(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) || issued.RunID == predecessor.RunID {
		t.Fatalf("stale CAS changed records or issued wrong Run: before=%#v/%#v after=%#v/%#v issued=%#v", beforeTask, beforeRuns, afterTask, afterRuns, issued)
	}
}

func TestManagerStartRunFromCheckpointRejectsPreviousAssigneeMismatch(t *testing.T) {
	ctx := context.Background()
	manager, _, task, predecessor := newCheckpointFixture(t, domaintask.StatusWaiting)
	if _, err := manager.RecordAssignment(ctx, task.TaskID, "Shiro", modulecore.NewEventID()); err != nil {
		t.Fatalf("RecordAssignment: %v", err)
	}
	if _, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Shiro", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("previous-assignee mismatch error = %v, want ErrRunConflict", err)
	}
}

func TestManagerStartRunFromCheckpointRejectsTiedLatestRuns(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	fixed := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return fixed }
	task, first := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.CompleteRun(ctx, task.TaskID, first.RunID, "Mio", domaintask.StatusSucceeded, "first", ""); err != nil {
		t.Fatal(err)
	}
	second, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonExplicitRerun)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.CompleteRun(ctx, task.TaskID, second.RunID, "Mio", domaintask.StatusSucceeded, "second", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, second.RunID, "Mio", domaintask.RunStartReasonExplicitRerun, checkpointTestDigest); !errors.Is(err, ErrRunConflict) {
		t.Fatalf("tied latest error = %v, want ErrRunConflict", err)
	}
}

func TestManagerStartRunFromCheckpointRejectsNonIncreasingSuccessorTimestamp(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name string
		now  func(domaintask.Run) time.Time
	}{
		{name: "equal_started_at", now: func(predecessor domaintask.Run) time.Time {
			return predecessor.StartedAt
		}},
		{name: "before_completed_at", now: func(predecessor domaintask.Run) time.Time {
			if predecessor.CompletedAt == nil {
				return predecessor.StartedAt
			}
			return predecessor.CompletedAt.Add(-time.Nanosecond)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manager, _, task, predecessor := newCheckpointFixture(t, domaintask.StatusWaiting)
			persisted, err := manager.GetRun(ctx, predecessor.RunID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.CompletedAt == nil {
				t.Fatal("checkpoint predecessor has no completed_at")
			}
			manager.now = func() time.Time { return test.now(persisted) }
			beforeTask, err := manager.Get(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			beforeRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil {
				t.Fatal(err)
			}

			if _, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest); !errors.Is(err, ErrRunConflict) {
				t.Fatalf("non-increasing successor timestamp error = %v, want ErrRunConflict", err)
			}
			afterTask, err := manager.Get(ctx, task.TaskID)
			if err != nil {
				t.Fatal(err)
			}
			afterRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
				t.Fatalf("rejected timestamp changed records: before task=%#v runs=%#v after task=%#v runs=%#v", beforeTask, beforeRuns, afterTask, afterRuns)
			}
		})
	}
}

func TestManagerConcurrentCheckpointCASHasOneWinner(t *testing.T) {
	ctx := context.Background()
	manager, _, task, predecessor := newCheckpointFixture(t, domaintask.StatusWaiting)
	start := make(chan struct{})
	results := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			_, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest)
			results <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		if err == nil {
			winners++
		} else if errors.Is(err, ErrRunConflict) {
			conflicts++
		} else {
			t.Fatalf("concurrent CAS error = %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent CAS outcomes = winners %d conflicts %d, want 1/1", winners, conflicts)
	}
	runs, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 {
		t.Fatalf("run history length = %d, want 2: %#v", len(runs), runs)
	}
}

func TestManagerStartRunFromCheckpointRollsBackOnRunSaveFailure(t *testing.T) {
	ctx := context.Background()
	base, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, base)
	setup := New(base, DefaultParallelLimits())
	task, predecessor := newRunningExecutionTaskRun(t, setup, "Mio")
	if _, err := setup.CompleteRun(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.StatusWaiting, "checkpoint", "wait"); err != nil {
		t.Fatal(err)
	}
	beforeTask, err := base.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	beforeRuns, err := base.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("checkpoint run append failed")
	manager := New(&faultTaskStore{Store: base, failOperation: "run", failErr: injected}, DefaultParallelLimits())
	if _, err := manager.StartRunFromCheckpoint(ctx, task.TaskID, predecessor.RunID, "Mio", domaintask.RunStartReasonCheckpointResume, checkpointTestDigest); !errors.Is(err, injected) {
		t.Fatalf("StartRunFromCheckpoint error = %v, want injected error", err)
	}
	afterTask, err := base.GetTask(ctx, task.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	afterRuns, err := base.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(beforeTask, afterTask) || !reflect.DeepEqual(beforeRuns, afterRuns) {
		t.Fatalf("failed checkpoint start changed records: before=%#v/%#v after=%#v/%#v", beforeTask, beforeRuns, afterTask, afterRuns)
	}
}

func TestManagerExecuteRunEffectRejectsStaleRunWithoutCallingEffect(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, active := newRunningExecutionTaskRun(t, manager, "Mio")
	if _, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonLeaseReacquire); err != nil {
		t.Fatal(err)
	}
	called := false
	err = manager.ExecuteRunEffect(ctx, task.TaskID, active.RunID, "Mio", func(context.Context) error {
		called = true
		return nil
	})
	if !errors.Is(err, ErrRunConflict) || called {
		t.Fatalf("stale effect result = %v called=%v, want conflict and no callback", err, called)
	}
}

func TestManagerExecuteRunEffectRejectsNilCallback(t *testing.T) {
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, run := newRunningExecutionTaskRun(t, manager, "Mio")
	if err := manager.ExecuteRunEffect(context.Background(), task.TaskID, run.RunID, "Mio", nil); err == nil {
		t.Fatal("nil effect callback accepted")
	}
}

func TestManagerExecuteRunEffectHoldsAdmissionFenceUntilCallbackReturnsReal(t *testing.T) {
	ctx := context.Background()
	store, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	registerTaskStoreCleanup(t, store)
	manager := New(store, DefaultParallelLimits())
	task, active := newRunningExecutionTaskRun(t, manager, "Mio")
	effectStarted := make(chan struct{})
	releaseEffect := make(chan struct{})
	effectDone := make(chan error, 1)
	go func() {
		effectDone <- manager.ExecuteRunEffect(ctx, task.TaskID, active.RunID, "Mio", func(context.Context) error {
			close(effectStarted)
			<-releaseEffect
			return nil
		})
	}()
	select {
	case <-effectStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("effect callback did not start")
	}
	supersedeDone := make(chan error, 1)
	go func() {
		_, err := manager.StartRunWithReason(ctx, task.TaskID, domaintask.RunStartReasonLeaseReacquire)
		supersedeDone <- err
	}()
	select {
	case err := <-supersedeDone:
		close(releaseEffect)
		t.Fatalf("supersession completed while effect was active: %v", err)
	case <-time.After(150 * time.Millisecond):
	}
	close(releaseEffect)
	if err := <-effectDone; err != nil {
		t.Fatalf("ExecuteRunEffect: %v", err)
	}
	if err := <-supersedeDone; err != nil {
		t.Fatalf("supersession after effect: %v", err)
	}
}
