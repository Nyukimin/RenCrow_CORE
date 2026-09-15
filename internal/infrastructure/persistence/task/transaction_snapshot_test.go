package task

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func snapshotTestTask(now time.Time) domaintask.Task {
	started := now.Add(time.Second)
	task := domaintask.Task{
		TaskID:            modulecore.NewTaskID(),
		Title:             "transaction snapshot task",
		Route:             domaintask.RouteGeneral,
		Assignee:          "Mio",
		Status:            domaintask.StatusRunning,
		Priority:          domaintask.PriorityNormal,
		InterruptPolicy:   domaintask.InterruptNotifyDoneOrBlocked,
		CoderRoles:        []string{"reviewer"},
		DependencyTaskIDs: []modulecore.TaskID{modulecore.NewTaskID()},
		NextActions:       []string{"inspect"},
		Evidence:          []string{"evidence"},
		Artifacts:         []string{"artifact"},
		CreatedAt:         now,
		UpdatedAt:         now.Add(2 * time.Second),
		StartedAt:         &started,
	}
	return task
}

func snapshotTestRun(task domaintask.Task, generation uint64, now time.Time, status domaintask.RunStatus) domaintask.Run {
	run := domaintask.Run{
		WriterGeneration: generation,
		RunID:            modulecore.NewRunID(),
		TaskID:           task.TaskID,
		StartReason:      domaintask.RunStartReasonFirst,
		Assignee:         task.Assignee,
		Status:           status,
		StartedAt:        now,
	}
	if status != domaintask.RunStatusRunning {
		completed := now.Add(time.Second)
		run.CompletedAt = &completed
	}
	return run
}

func TestTransactionSnapshotDefensiveCopiesTaskAndRun(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 10, 20, 0, 0, time.UTC)
	task := snapshotTestTask(now)
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	run := snapshotTestRun(task, generation, now.Add(3*time.Second), domaintask.RunStatusSucceeded)

	err = store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, task); err != nil {
			return err
		}
		got, err := tx.GetTask(ctx, task.TaskID)
		if err != nil {
			return err
		}
		*got.StartedAt = got.StartedAt.Add(10 * time.Minute)
		got.CoderRoles[0] = "mutated"
		got.DependencyTaskIDs[0] = modulecore.NewTaskID()
		got.NextActions[0] = "mutated"
		got.Evidence[0] = "mutated"
		got.Artifacts[0] = "mutated"

		again, err := tx.GetTask(ctx, task.TaskID)
		if err != nil {
			return err
		}
		if again.StartedAt == nil || !again.StartedAt.Equal(*task.StartedAt) || again.CoderRoles[0] != "reviewer" || again.DependencyTaskIDs[0] != task.DependencyTaskIDs[0] || again.NextActions[0] != "inspect" || again.Evidence[0] != "evidence" || again.Artifacts[0] != "artifact" {
			return errors.New("mutating a returned Task changed the transaction snapshot")
		}

		if err := tx.SaveRun(ctx, run); err != nil {
			return err
		}
		gotRun, err := tx.GetRun(ctx, run.RunID)
		if err != nil {
			return err
		}
		*gotRun.CompletedAt = gotRun.CompletedAt.Add(10 * time.Minute)
		againRun, err := tx.GetRun(ctx, run.RunID)
		if err != nil {
			return err
		}
		if againRun.CompletedAt == nil || !againRun.CompletedAt.Equal(*run.CompletedAt) {
			return errors.New("mutating a returned Run changed the transaction snapshot")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTransactionSnapshotPendingOverlayRefreshesAfterEachAppend(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 10, 30, 0, 0, time.UTC)
	task := snapshotTestTask(now)
	task.Status = domaintask.StatusQueued
	task.StartedAt = nil
	task.UpdatedAt = now
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	err = store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, task); err != nil {
			return err
		}
		queued, err := tx.GetTask(ctx, task.TaskID)
		if err != nil || queued.Status != domaintask.StatusQueued {
			return errors.New("initial pending Task was not visible")
		}

		started := now.Add(time.Second)
		task.Status = domaintask.StatusRunning
		task.StartedAt = &started
		task.UpdatedAt = now.Add(2 * time.Second)
		if err := tx.SaveTask(ctx, task); err != nil {
			return err
		}
		current, err := tx.GetTask(ctx, task.TaskID)
		if err != nil || current.Status != domaintask.StatusRunning || current.StartedAt == nil {
			return errors.New("pending Task overlay was stale after SaveTask")
		}

		run := snapshotTestRun(task, generation, now.Add(3*time.Second), domaintask.RunStatusRunning)
		if err := tx.SaveRun(ctx, run); err != nil {
			return err
		}
		active, err := tx.GetRun(ctx, run.RunID)
		if err != nil || active.Status != domaintask.RunStatusRunning || active.CompletedAt != nil {
			return errors.New("pending active Run was not visible")
		}

		closed := run
		closed.Status = domaintask.RunStatusSucceeded
		completed := now.Add(4 * time.Second)
		closed.CompletedAt = &completed
		if err := tx.SaveRun(ctx, closed); err != nil {
			return err
		}
		terminal, err := tx.GetRun(ctx, run.RunID)
		if err != nil || terminal.Status != domaintask.RunStatusSucceeded || terminal.CompletedAt == nil {
			return errors.New("pending terminal Run overlay was stale after SaveRun")
		}
		activeRuns, err := tx.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID, Status: domaintask.RunStatusRunning})
		if err != nil {
			return err
		}
		if len(activeRuns) != 0 {
			return errors.New("terminal pending Run remained active")
		}
		allRuns, err := tx.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID})
		if err != nil || len(allRuns) != 1 || allRuns[0].RunID != run.RunID {
			return errors.New("pending Run history was not coherent")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTransactionSnapshotRollbackStartsFreshView(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	task := snapshotTestTask(time.Date(2026, 9, 14, 10, 40, 0, 0, time.UTC))
	injected := errors.New("rollback snapshot test")
	err = store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, task); err != nil {
			return err
		}
		if _, err := tx.GetTask(ctx, task.TaskID); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("transaction error = %v, want injected error", err)
	}
	err = store.ReadTransaction(ctx, func(tx domaintask.Store) error {
		if _, err := tx.GetTask(ctx, task.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
			return errors.New("rolled-back Task remained visible to the next transaction")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestTransactionSnapshotRejectsMalformedHistoricalRunTransition(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 10, 50, 0, 0, time.UTC)
	task := snapshotTestTask(now)
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	run := snapshotTestRun(task, generation, now.Add(time.Second), domaintask.RunStatusRunning)
	closed := run
	closed.Status = domaintask.RunStatusSucceeded
	completed := now.Add(2 * time.Second)
	closed.CompletedAt = &completed
	reopened := run
	if err := os.WriteFile(filepath.Join(root, runFilename), []byte(mustJSONLRun(t, run)+mustJSONLRun(t, closed)+mustJSONLRun(t, reopened)), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRuns(ctx, domaintask.RunFilter{TaskID: task.TaskID}); err == nil {
		t.Fatal("malformed historical Run transition was accepted")
	}
}

func BenchmarkTransactionSnapshotReadReuse(b *testing.B) {
	store, err := NewJSONLStore(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 14, 11, 0, 0, 0, time.UTC)
	generation, err := store.WriterGeneration()
	if err != nil {
		b.Fatal(err)
	}
	tasks := make([]domaintask.Task, 64)
	runs := make([]domaintask.Run, 64)
	err = store.Transaction(ctx, func(tx domaintask.Store) error {
		for index := range tasks {
			task := snapshotTestTask(now.Add(time.Duration(index) * time.Second))
			task.Title = "benchmark task"
			tasks[index] = task
			if err := tx.SaveTask(ctx, task); err != nil {
				return err
			}
			run := snapshotTestRun(task, generation, now.Add(time.Duration(index+1)*time.Second), domaintask.RunStatusSucceeded)
			runs[index] = run
			if err := tx.SaveRun(ctx, run); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := store.ReadTransaction(ctx, func(tx domaintask.Store) error {
			for index := 0; index < 16; index++ {
				if _, err := tx.GetTask(ctx, tasks[index].TaskID); err != nil {
					return err
				}
				if _, err := tx.ListRuns(ctx, domaintask.RunFilter{TaskID: tasks[index].TaskID}); err != nil {
					return err
				}
			}
			return nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}
}
