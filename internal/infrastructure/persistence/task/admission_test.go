package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestAdmittedCreateAndStartRunIsVisibleBeforeTaskJSONLFlush(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		options OpenOptions
	}{
		{name: "with index", options: OpenOptions{Index: true, Persist: true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewJSONLStoreWithOptions(root, testCase.options)
			if err != nil {
				t.Fatal(err)
			}
			storeClosed := false
			t.Cleanup(func() {
				if !storeClosed {
					if err := store.Close(); err != nil {
						t.Errorf("close task store: %v", err)
					}
				}
			})

			queue, err := persistencequeue.NewPersistenceQueue(8, 4<<20)
			if err != nil {
				t.Fatal(err)
			}
			unblock := make(chan struct{})
			workerStarted := make(chan struct{})
			var unblockOnce sync.Once
			releaseWorker := func() { unblockOnce.Do(func() { close(unblock) }) }
			t.Cleanup(func() {
				releaseWorker()
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				if err := queue.Shutdown(shutdownCtx); err != nil {
					t.Errorf("shutdown persistence queue: %v", err)
				}
			})

			blocker, err := persistencequeue.Admit(queue, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = blocker.Release() })
			if _, err := blocker.Enqueue(persistencequeue.PersistenceCommand{
				Owner: persistencequeue.OwnerTask, Kind: "test.block", SizeBytes: 1,
				Apply: func(context.Context) error {
					close(workerStarted)
					<-unblock
					return nil
				},
			}); err != nil {
				t.Fatalf("enqueue worker blocker: %v", err)
			}
			if err := blocker.Release(); err != nil {
				t.Fatalf("release blocker admission: %v", err)
			}
			select {
			case <-workerStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("persistence worker did not reach the blocker")
			}
			commitStarted := make(chan struct{})
			commitRelease := make(chan struct{})
			var commitReleaseOnce sync.Once
			releaseCommitHook := func() { commitReleaseOnce.Do(func() { close(commitRelease) }) }
			t.Cleanup(releaseCommitHook)
			originalHook := store.taskPersistencePostCommitHook(store.idx.cur)
			store.batch.SetPostCommitHook(func(ctx context.Context, appends []jsonlbatch.CommittedAppend) {
				if _, acceptedTaskWrite := ctx.Value(taskAdmissionWriteContextKey{}).(*taskAdmissionWrite); acceptedTaskWrite {
					close(commitStarted)
					<-commitRelease
				}
				originalHook(ctx, appends)
			})

			admission, err := persistencequeue.Admit(queue, 1, 2<<20)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = admission.Release() })
			ctx := persistencequeue.WithAdmission(context.Background(), admission)
			manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
			draft := domaintask.Task{
				TaskID: modulecore.NewTaskID(), Title: "admitted root task",
				Route: domaintask.RouteGeneral, Assignee: "Mio",
			}
			type createResult struct {
				task domaintask.Task
				run  domaintask.Run
				err  error
			}
			created := make(chan createResult, 1)
			go func() {
				task, run, err := manager.CreateAndStartRun(ctx, draft, domaintask.SharedRoleContext{})
				created <- createResult{task: task, run: run, err: err}
			}()

			var result createResult
			select {
			case result = <-created:
				if result.err != nil {
					t.Fatalf("create and start while queue worker is blocked: %v", result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("admitted Task creation waited for the persistence worker")
			}

			for _, filename := range []string{stateFilename, runFilename, contextFilename} {
				data, err := os.ReadFile(filepath.Join(root, filename))
				if err == nil && len(data) != 0 {
					t.Fatalf("%s was written before the shared persistence worker drained", filename)
				}
				if err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("read %s before queue drain: %v", filename, err)
				}
			}

			if result.task.Status != domaintask.StatusRunning || result.run.Status != domaintask.RunStatusRunning || result.run.TaskID != result.task.TaskID {
				t.Fatalf("created Task/Run = %+v / %+v, want one running root Task and its running first Run", result.task, result.run)
			}
			readTask, err := manager.Get(ctx, result.task.TaskID)
			if err != nil || readTask.Status != domaintask.StatusRunning {
				t.Fatalf("admitted Task readback = %+v, err=%v", readTask, err)
			}
			readRuns, err := manager.ListRuns(ctx, domaintask.RunFilter{TaskID: result.task.TaskID})
			if err != nil || len(readRuns) != 1 || readRuns[0].RunID != result.run.RunID || readRuns[0].Status != domaintask.RunStatusRunning {
				t.Fatalf("admitted Run readback = %+v, err=%v", readRuns, err)
			}
			readContext, err := manager.Context(ctx, result.task.TaskID)
			if err != nil || readContext.TaskID != result.task.TaskID {
				t.Fatalf("admitted context readback = %+v, err=%v", readContext, err)
			}
			if _, err := manager.Get(context.Background(), result.task.TaskID); err == nil {
				t.Fatal("a non-admitted Task read observed a RAM-only accepted write")
			}
			releaseWorker()
			select {
			case <-commitStarted:
			case <-time.After(2 * time.Second):
				t.Fatal("Task writer did not enter the held post-commit hook")
			}
			// The Task JSONL bytes are already committed and the worker still owns
			// jsonlbatch's exclusive lock. Indexed admitted transactions must be
			// served from the committed index plus RAM overlay without waiting for
			// that lock or the post-commit hook.
			readDuringCommit := make(chan error, 1)
			go func() {
				readDuringCommit <- store.TaskTransaction(ctx, result.task.TaskID, func(tx domaintask.Store) error {
					transactionTask, err := tx.GetTask(ctx, result.task.TaskID)
					if err != nil {
						return err
					}
					transactionRuns, err := tx.ListRuns(ctx, domaintask.RunFilter{TaskID: result.task.TaskID})
					if err != nil {
						return err
					}
					if transactionTask.TaskID != result.task.TaskID || len(transactionRuns) != 1 || transactionRuns[0].RunID != result.run.RunID {
						return errors.New("admitted Task transaction did not see the accepted Task and Run")
					}
					return nil
				})
			}()
			select {
			case err := <-readDuringCommit:
				if err != nil {
					t.Fatalf("admitted Task transaction during disk commit: %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("admitted Task transaction waited for the jsonlbatch disk lock")
			}
			readOnlyDuringCommit := make(chan error, 1)
			go func() {
				readTask, err := manager.Get(ctx, result.task.TaskID)
				if err == nil && readTask.Status != domaintask.StatusRunning {
					err = errors.New("admitted Task read did not see the accepted state")
				}
				readOnlyDuringCommit <- err
			}()
			select {
			case err := <-readOnlyDuringCommit:
				if err != nil {
					t.Fatalf("admitted Task read during disk commit: %v", err)
				}
			case <-time.After(250 * time.Millisecond):
				t.Fatal("admitted Task read waited for the jsonlbatch disk lock")
			}
			effectStarted := make(chan struct{})
			effectDone := make(chan error, 1)
			go func() {
				effectDone <- store.WithTaskExecutionFence(context.Background(), result.task.TaskID, func() error {
					close(effectStarted)
					return nil
				})
			}()
			select {
			case <-effectStarted:
				t.Fatal("a synchronous external effect crossed an accepted Task write")
			case <-time.After(50 * time.Millisecond):
			}
			releaseCommitHook()
			barrierCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := admission.Barrier(barrierCtx); err != nil {
				t.Fatalf("wait for admitted Task JSONL flush: %v", err)
			}
			if err := admission.Release(); err != nil {
				t.Fatalf("release Task admission: %v", err)
			}
			select {
			case err := <-effectDone:
				if err != nil {
					t.Fatalf("run synchronous external effect after Task persistence barrier: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("synchronous external effect did not resume after Task persistence")
			}

			for _, filename := range []string{stateFilename, runFilename, contextFilename} {
				data, err := os.ReadFile(filepath.Join(root, filename))
				if err != nil || len(data) == 0 {
					t.Fatalf("%s after queue drain has %d bytes, err=%v", filename, len(data), err)
				}
			}

			if err := store.Close(); err != nil {
				t.Fatalf("close task writer: %v", err)
			}
			storeClosed = true
			reader, err := NewJSONLReader(root)
			if err != nil {
				t.Fatalf("reopen task JSONL: %v", err)
			}
			defer reader.Close()
			durableTask, err := reader.GetTask(context.Background(), result.task.TaskID)
			if err != nil || durableTask.Status != domaintask.StatusRunning {
				t.Fatalf("durable Task readback = %+v, err=%v", durableTask, err)
			}
			durableRun, err := reader.GetRun(context.Background(), result.run.RunID)
			if err != nil || durableRun.Status != domaintask.RunStatusRunning || durableRun.TaskID != result.task.TaskID {
				t.Fatalf("durable Run readback = %+v, err=%v", durableRun, err)
			}
		})
	}
}

func TestAdmittedTaskOverlayFailsClosedAfterQueueFailure(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			if err := store.closeStore(false); err != nil {
				t.Errorf("close task store after queue failure: %v", err)
			}
		}
	})

	queue, err := persistencequeue.NewPersistenceQueue(8, 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	workerStarted := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseWorker := func() { releaseOnce.Do(func() { close(release) }) }
	failureExpected := false
	t.Cleanup(func() {
		releaseWorker()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Shutdown(shutdownCtx); err != nil && !failureExpected {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})

	blocker, err := persistencequeue.Admit(queue, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Release() })
	commandFailure := errors.New("injected shared persistence failure")
	if _, err := blocker.Enqueue(persistencequeue.PersistenceCommand{
		Owner: persistencequeue.OwnerTask, Kind: "test.fail", SizeBytes: 1,
		Apply: func(context.Context) error {
			close(workerStarted)
			<-release
			return commandFailure
		},
	}); err != nil {
		t.Fatalf("enqueue failing worker blocker: %v", err)
	}
	if err := blocker.Release(); err != nil {
		t.Fatalf("release blocker admission: %v", err)
	}
	select {
	case <-workerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("persistence worker did not reach the blocker")
	}

	admission, err := persistencequeue.Admit(queue, 1, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Release() })
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	task, _, err := manager.CreateAndStartRun(ctx, domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "accepted but not durable", Route: domaintask.RouteGeneral, Assignee: "Mio",
	}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatalf("accept Task write while worker is blocked: %v", err)
	}

	releaseWorker()
	barrierCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := admission.Barrier(barrierCtx); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("failed queue barrier = %v, want persistence queue failure", err)
	}
	failureExpected = true
	if _, err := manager.Get(ctx, task.TaskID); !errors.Is(err, persistencequeue.ErrFailed) {
		t.Fatalf("read after accepted Task command failed = %v, want fail-closed queue error", err)
	}
	if err := store.closeStore(false); err != nil {
		t.Fatalf("close store after failed Task command: %v", err)
	}
	storeClosed = true
}

func TestExplicitAdmissionFailsFastWhileSynchronousTaskWriterWaitsForJSONLLock(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	storeClosed := false
	t.Cleanup(func() {
		if !storeClosed {
			if err := store.Close(); err != nil {
				t.Errorf("close task store: %v", err)
			}
		}
	})

	queue, err := persistencequeue.NewPersistenceQueue(4, 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})
	admission, err := persistencequeue.Admit(queue, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Release() })
	ctx := persistencequeue.WithAdmission(context.Background(), admission)

	// A second jsonlbatch handle holds the real on-disk lock. The Task store's
	// synchronous write below enters its normal writer gate, then waits for that
	// lock. An explicitly admitted operation must report busy instead of joining
	// that disk wait.
	lockHolder, err := jsonlbatch.New(root, taskBatchFilenames())
	if err != nil {
		t.Fatal(err)
	}
	lockHeld := make(chan struct{})
	releaseLock := make(chan struct{})
	var releaseLockOnce sync.Once
	releaseOSLock := func() { releaseLockOnce.Do(func() { close(releaseLock) }) }
	t.Cleanup(releaseOSLock)
	lockDone := make(chan error, 1)
	go func() {
		lockDone <- lockHolder.Write(context.Background(), func() (map[string][]byte, error) {
			close(lockHeld)
			<-releaseLock
			return nil, nil
		})
	}()
	select {
	case <-lockHeld:
	case <-time.After(2 * time.Second):
		t.Fatal("test writer did not acquire the JSONL lock")
	}

	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	type writeResult struct {
		task domaintask.Task
		err  error
	}
	synchronousWrite := make(chan writeResult, 1)
	go func() {
		task, _, err := manager.CreateAndStartRun(context.Background(), domaintask.Task{
			TaskID: modulecore.NewTaskID(), Title: "blocked synchronous writer", Route: domaintask.RouteGeneral, Assignee: "Mio",
		}, domaintask.SharedRoleContext{})
		synchronousWrite <- writeResult{task: task, err: err}
	}()
	if !waitForTaskSynchronousWriter(t, store) {
		releaseOSLock()
		if err := <-lockDone; err != nil {
			t.Errorf("release test JSONL lock: %v", err)
		}
		result := <-synchronousWrite
		t.Fatalf("Task writer did not enter its synchronous writer gate; result=%+v", result)
	}

	readDone := make(chan error, 1)
	go func() { readDone <- store.ReadTransaction(ctx, func(domaintask.Store) error { return nil }) }()
	writeDone := make(chan error, 1)
	go func() {
		writeDone <- store.TaskTransaction(ctx, modulecore.NewTaskID(), func(domaintask.Store) error { return nil })
	}()

	readErr, readReturned := receiveAdmissionBusy(readDone)
	writeErr, writeReturned := receiveAdmissionBusy(writeDone)
	if !readReturned || !writeReturned || !errors.Is(readErr, ErrTaskAdmissionBusy) || !errors.Is(writeErr, ErrTaskAdmissionBusy) {
		releaseOSLock()
		if err := <-lockDone; err != nil {
			t.Errorf("release test JSONL lock: %v", err)
		}
		<-synchronousWrite
		if !readReturned {
			<-readDone
		}
		if !writeReturned {
			<-writeDone
		}
		t.Fatalf("admitted read/write while synchronous JSONL writer is blocked: read=%v (returned=%v), write=%v (returned=%v); want ErrTaskAdmissionBusy immediately", readErr, readReturned, writeErr, writeReturned)
	}

	releaseOSLock()
	if err := <-lockDone; err != nil {
		t.Fatalf("release test JSONL lock: %v", err)
	}
	if result := <-synchronousWrite; result.err != nil {
		t.Fatalf("synchronous Task write after JSONL lock release: %v", result.err)
	}
	if status := queue.Status(); status.Pending != 0 {
		t.Fatalf("busy admitted operations enqueued %d persistence commands", status.Pending)
	}
	if err := admission.Release(); err != nil {
		t.Fatalf("release admission: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close Task store: %v", err)
	}
	storeClosed = true
}

func waitForTaskSynchronousWriter(t *testing.T, store *JSONLStore) bool {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		store.taskPersistence.mu.Lock()
		started := store.taskPersistence.synchronousWriter
		store.taskPersistence.mu.Unlock()
		if started {
			return true
		}
		select {
		case <-timer.C:
			return false
		case <-ticker.C:
		}
	}
}

func receiveAdmissionBusy(done <-chan error) (error, bool) {
	select {
	case err := <-done:
		return err, true
	case <-time.After(250 * time.Millisecond):
		return nil, false
	}
}

func TestExplicitAdmissionFailsFastWhenTaskLocksAreContended(t *testing.T) {
	store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{Index: true, Persist: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close task store: %v", err)
		}
	})
	queue, err := persistencequeue.NewPersistenceQueue(4, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})
	admission, err := persistencequeue.Admit(queue, 2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Release() })
	ctx := persistencequeue.WithAdmission(context.Background(), admission)

	tests := []struct {
		name      string
		lock      func() func()
		operation func() error
	}{
		{
			name: "Task store read lock",
			lock: func() func() {
				store.mu.Lock()
				return store.mu.Unlock
			},
			operation: func() error {
				return store.ReadTransaction(ctx, func(domaintask.Store) error { return nil })
			},
		},
		{
			name: "Task store write lock",
			lock: func() func() {
				store.mu.Lock()
				return store.mu.Unlock
			},
			operation: func() error {
				return store.TaskTransaction(ctx, modulecore.NewTaskID(), func(domaintask.Store) error { return nil })
			},
		},
		{
			name: "persistence queue state lock",
			lock: func() func() {
				store.taskPersistence.mu.Lock()
				return store.taskPersistence.mu.Unlock
			},
			operation: func() error {
				return store.ReadTransaction(ctx, func(domaintask.Store) error { return nil })
			},
		},
		{
			name: "index writer lock",
			lock: func() func() {
				store.idx.mu.Lock()
				return store.idx.mu.Unlock
			},
			operation: func() error {
				return store.ReadTransaction(ctx, func(domaintask.Store) error { return nil })
			},
		},
		{
			name: "index writer lock for transaction",
			lock: func() func() {
				store.idx.mu.Lock()
				return store.idx.mu.Unlock
			},
			operation: func() error {
				return store.TaskTransaction(ctx, modulecore.NewTaskID(), func(domaintask.Store) error { return nil })
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			release := test.lock()
			done := make(chan error, 1)
			go func() { done <- test.operation() }()
			err, returned := receiveAdmissionBusy(done)
			release()
			if !returned {
				err = <-done
				t.Fatalf("admitted operation waited for a contended %s; after release it returned %v", test.name, err)
			}
			if !errors.Is(err, ErrTaskAdmissionBusy) {
				t.Fatalf("admitted operation with contended %s = %v, want ErrTaskAdmissionBusy", test.name, err)
			}
		})
	}
}

func TestExplicitAdmissionRequiresIndexedTaskStore(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	queue, err := persistencequeue.NewPersistenceQueue(2, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := queue.Shutdown(ctx); err != nil {
			t.Errorf("shutdown persistence queue: %v", err)
		}
	})
	admission, err := persistencequeue.Admit(queue, 1, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admission.Release() })
	manager := taskmanager.New(store, taskmanager.DefaultParallelLimits())
	ctx := persistencequeue.WithAdmission(context.Background(), admission)
	_, _, err = manager.CreateAndStartRun(ctx, domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "requires indexed admission", Route: domaintask.RouteGeneral, Assignee: "Mio",
	}, domaintask.SharedRoleContext{})
	if !errors.Is(err, ErrTaskAdmissionRequiresIndex) {
		t.Fatalf("explicit Admission write error = %v, want ErrTaskAdmissionRequiresIndex", err)
	}
	if err := store.ReadTransaction(ctx, func(domaintask.Store) error { return nil }); !errors.Is(err, ErrTaskAdmissionRequiresIndex) {
		t.Fatalf("explicit Admission read error = %v, want ErrTaskAdmissionRequiresIndex", err)
	}
	if status := queue.Status(); status.Pending != 0 {
		t.Fatalf("non-indexed Task store enqueued %d persistence commands", status.Pending)
	}

	legacyTask := domaintask.Task{
		TaskID: modulecore.NewTaskID(), Title: "legacy synchronous write", Route: domaintask.RouteGeneral, Assignee: "Mio",
	}
	if _, _, err := manager.CreateAndStartRun(context.Background(), legacyTask, domaintask.SharedRoleContext{}); err != nil {
		t.Fatalf("non-admitted write on a non-indexed Task store: %v", err)
	}
	if got, err := manager.Get(context.Background(), legacyTask.TaskID); err != nil || got.TaskID != legacyTask.TaskID {
		t.Fatalf("non-admitted synchronous readback = %+v, err=%v", got, err)
	}
}
