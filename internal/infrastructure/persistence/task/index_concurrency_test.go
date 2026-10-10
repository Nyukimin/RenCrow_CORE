package task

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// TestConcurrentReadsWritesAndResyncAreSafe runs writers, readers and forced
// resynchronizations together (run it with -race). Each write commits a Task and
// its context with the same version marker in one transaction; a reader's
// ReadTransaction must therefore never see the two disagree.
func TestConcurrentReadsWritesAndResyncAreSafe(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()

	const writers, perWriter, readers = 4, 25, 6
	tasks := make([]domaintask.Task, writers)
	for i := range tasks {
		tasks[i] = indexedTestTask(100+i, indexTestNow)
		if err := store.SaveTask(ctx, tasks[i]); err != nil {
			t.Fatal(err)
		}
		if err := store.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: tasks[i].TaskID, CurrentPlan: "v0", UpdatedAt: indexTestNow}); err != nil {
			t.Fatal(err)
		}
	}

	var failures atomic.Int64
	fail := func(format string, args ...any) {
		failures.Add(1)
		t.Errorf(format, args...)
	}

	var writersDone sync.WaitGroup
	for w := 0; w < writers; w++ {
		w := w
		writersDone.Add(1)
		go func() {
			defer writersDone.Done()
			task := tasks[w]
			for n := 1; n <= perWriter; n++ {
				version := fmt.Sprintf("v%d", n)
				err := store.TaskTransaction(ctx, task.TaskID, func(tx domaintask.Store) error {
					current, err := tx.GetTask(ctx, task.TaskID)
					if err != nil {
						return err
					}
					current.Summary = version
					current.UpdatedAt = indexTestNow.Add(time.Duration(n) * time.Second)
					if err := tx.SaveTask(ctx, current); err != nil {
						return err
					}
					if err := tx.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: version, UpdatedAt: current.UpdatedAt}); err != nil {
						return err
					}
					generation, err := tx.WriterGeneration()
					if err != nil {
						return err
					}
					run := indexedTestRun(task, w*1000+n, generation, current.UpdatedAt)
					if err := tx.SaveRun(ctx, run); err != nil {
						return err
					}
					completed := run.StartedAt.Add(time.Millisecond)
					run.Status, run.CompletedAt = domaintask.RunStatusSucceeded, &completed
					if err := tx.SaveRun(ctx, run); err != nil {
						return err
					}
					return tx.SaveNotification(ctx, domaintask.NewNotification(current, current.UpdatedAt))
				})
				if err != nil {
					fail("writer %d tx %d: %v", w, n, err)
					return
				}
			}
		}()
	}

	stop := make(chan struct{})
	var background sync.WaitGroup
	for r := 0; r < readers; r++ {
		r := r
		background.Add(1)
		go func() {
			defer background.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				task := tasks[(r+i)%writers]
				switch i % 5 {
				case 0:
					if _, err := store.GetTask(ctx, task.TaskID); err != nil {
						fail("reader GetTask: %v", err)
					}
				case 1:
					if _, err := store.ListTasks(ctx, domaintask.Filter{Limit: 3}); err != nil {
						fail("reader ListTasks: %v", err)
					}
				case 2:
					if _, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID, Limit: 4}); err != nil {
						fail("reader ListRuns: %v", err)
					}
				case 3:
					if _, err := store.ListNotifications(ctx, 5, false); err != nil {
						fail("reader ListNotifications: %v", err)
					}
				default:
					err := store.ReadTransaction(ctx, func(tx domaintask.Store) error {
						got, err := tx.GetTask(ctx, task.TaskID)
						if err != nil {
							return err
						}
						plan, err := tx.GetContext(ctx, task.TaskID)
						if err != nil {
							return err
						}
						want := got.Summary
						if want == "" {
							want = "v0"
						}
						if plan.CurrentPlan != want {
							return fmt.Errorf("torn snapshot: task summary %q but context %q", got.Summary, plan.CurrentPlan)
						}
						return nil
					})
					if err != nil {
						fail("reader snapshot: %v", err)
					}
				}
			}
		}()
	}
	// Force reads onto the resynchronize-through-the-lock path while writes run.
	background.Add(1)
	go func() {
		defer background.Done()
		for {
			select {
			case <-stop:
				return
			case <-time.After(3 * time.Millisecond):
				store.idx.dirty.Store(true)
			}
		}
	}()

	writersDone.Wait()
	close(stop)
	background.Wait()
	if failures.Load() > 0 {
		t.FailNow()
	}
	if err := store.GetTaskThroughRoundTrip(ctx, tasks[0].TaskID); err != nil {
		t.Fatal(err)
	}
	requireIndexCoversLog(t, store, root)
	for _, task := range tasks {
		got, err := store.GetTask(ctx, task.TaskID)
		if err != nil || got.Summary != fmt.Sprintf("v%d", perWriter) {
			t.Fatalf("final state of %s = %q, %v", task.TaskID, got.Summary, err)
		}
	}
}

// GetTaskThroughRoundTrip is a test-only helper: the final Task must read back
// identically through the index and through a fresh reader of the same files.
func (s *JSONLStore) GetTaskThroughRoundTrip(ctx context.Context, taskID modulecore.TaskID) error {
	indexed, err := s.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	reader, err := NewJSONLReader(s.root)
	if err != nil {
		return err
	}
	defer reader.Close()
	folded, err := reader.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	if indexed.Summary != folded.Summary || !indexed.UpdatedAt.Equal(folded.UpdatedAt) {
		return errors.New("index and a full fold read different Tasks")
	}
	return nil
}

func TestClosedStoreRefusesIndexedReads(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	task := indexedTestTask(1, indexTestNow)
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() = %v, want an idempotent no-op", err)
	}
	if _, err := store.GetTask(ctx, task.TaskID); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("GetTask after Close = %v, want a closed-store error", err)
	}
	if _, err := store.ListTasks(ctx, domaintask.Filter{}); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("ListTasks after Close = %v, want a closed-store error", err)
	}
	if err := store.SaveTask(ctx, task); err == nil {
		t.Fatal("SaveTask after Close succeeded")
	}
}
