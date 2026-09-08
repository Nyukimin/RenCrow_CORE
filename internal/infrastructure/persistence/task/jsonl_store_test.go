package task

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreKeepsLatestCanonicalTaskState(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 0, 0, 0, 0, time.UTC)
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "test", Route: domaintask.RouteCode, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	value.Status = domaintask.StatusRunning
	value.UpdatedAt = now.Add(time.Minute)
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetTask(context.Background(), value.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != domaintask.StatusRunning {
		t.Fatalf("status = %s, want running", got.Status)
	}
	if _, err := os.Stat(filepath.Join(root, "task_state.jsonl")); err != nil {
		t.Fatal(err)
	}
	for _, filename := range []string{"task_context.jsonl", "task_notifications.jsonl"} {
		if _, err := os.Stat(filepath.Join(root, filename)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJSONLStoreRejectsLegacyFilesAndUnknownAliases(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "job_state.jsonl"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewJSONLStore(root); err == nil || !strings.Contains(err.Error(), "legacy") {
		t.Fatalf("legacy store result = %v", err)
	}

	root = t.TempDir()
	store, err := NewJSONLStore(root)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.statePath, []byte(`{"job_id":"legacy"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListTasks(context.Background(), domaintask.Filter{}); err == nil {
		t.Fatal("legacy JSON alias was accepted")
	}
}

func TestJSONLStoreContextAndNotificationUseTaskID(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "test", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), value); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveContext(context.Background(), domaintask.SharedRoleContext{TaskID: value.TaskID, CurrentPlan: "plan", UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	contextValue, err := store.GetContext(context.Background(), value.TaskID)
	if err != nil || contextValue.CurrentPlan != "plan" {
		t.Fatalf("context = %#v err=%v", contextValue, err)
	}
	notification := domaintask.NewNotification(value, now)
	if err := store.SaveNotification(context.Background(), notification); err != nil {
		t.Fatal(err)
	}
	items, err := store.ListNotifications(context.Background(), 10, false)
	if err != nil || len(items) != 1 || items[0].TaskID != value.TaskID {
		t.Fatalf("notifications = %#v err=%v", items, err)
	}
}

func TestJSONLStoreTransactionOverlayCommitsAsOneBatch(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 9, 8, 0, 0, 0, 0, time.UTC)
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "transaction overlay", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	contextValue := domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "same transaction", UpdatedAt: now}
	err = store.Transaction(context.Background(), func(tx domaintask.Store) error {
		if err := tx.SaveTask(context.Background(), task); err != nil {
			return err
		}
		visible, err := tx.GetTask(context.Background(), task.TaskID)
		if err != nil || visible.TaskID != task.TaskID {
			return fmt.Errorf("pending Task was not visible: %#v %v", visible, err)
		}
		if err := tx.SaveContext(context.Background(), contextValue); err != nil {
			return err
		}
		visibleContext, err := tx.GetContext(context.Background(), task.TaskID)
		if err != nil || visibleContext.CurrentPlan != contextValue.CurrentPlan {
			return fmt.Errorf("pending Context was not visible: %#v %v", visibleContext, err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTask(context.Background(), task.TaskID); err != nil {
		t.Fatal(err)
	}
	if got, err := store.GetContext(context.Background(), task.TaskID); err != nil || got.CurrentPlan != contextValue.CurrentPlan {
		t.Fatalf("committed Context = %#v err=%v", got, err)
	}
}

func TestJSONLStoreTransactionCallbackFailureDiscardsPendingAppends(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "transaction rollback", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	injected := errors.New("callback rejected")
	err = store.Transaction(context.Background(), func(tx domaintask.Store) error {
		if err := tx.SaveTask(context.Background(), task); err != nil {
			return err
		}
		return injected
	})
	if !errors.Is(err, injected) {
		t.Fatalf("Transaction error = %v, want injected error", err)
	}
	if _, err := store.GetTask(context.Background(), task.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("Task survived callback failure: %v", err)
	}
}

func TestJSONLStoreNestedTransactionsAndLifetimeBoundaries(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	var outer, nested domaintask.Store
	err = store.Transaction(ctx, func(tx domaintask.Store) error {
		outer = tx
		return tx.Transaction(ctx, func(inner domaintask.Store) error {
			nested = inner
			return tx.ReadTransaction(ctx, func(readOnly domaintask.Store) error {
				if err := readOnly.SaveTask(ctx, domaintask.Task{}); !errors.Is(err, errReadOnlyTransaction) {
					return fmt.Errorf("nested read transaction Save error = %v", err)
				}
				return nil
			})
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if outer == nil || nested == nil || outer != nested {
		t.Fatalf("nested Transaction did not reuse transaction instance: outer=%T nested=%T", outer, nested)
	}
	if _, err := outer.GetTask(ctx, modulecore.NewTaskID()); !errors.Is(err, errTaskTransactionExpired) {
		t.Fatalf("transaction view remained usable after callback: %v", err)
	}
}

func TestJSONLStoreReaderRejectsPendingWALWithoutRecovery(t *testing.T) {
	root := t.TempDir()
	writer, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	walPath := filepath.Join(root, ".jsonlbatch.wal")
	if err := os.WriteFile(walPath, []byte(`{"version":1,"kind":"prepare","tx_id":"pending-test","files":[]}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	if _, err := reader.ListTasks(context.Background(), domaintask.Filter{}); !errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
		t.Fatalf("reader pending WAL error = %v, want ErrRecoveryRequired", err)
	}
}

func TestJSONLStoreRunHistoryReloadsByRunIDAndTaskID(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 6, 0, 0, 0, time.UTC)
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "run persistence", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	first := domaintask.Run{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
	if err := store.SaveRun(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	completedAt := now.Add(time.Minute)
	first.Status = domaintask.RunStatusSucceeded
	first.CompletedAt = &completedAt
	first.Summary = "first complete"
	if err := store.SaveRun(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	second := domaintask.Run{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonExplicitRerun, Status: domaintask.RunStatusRunning, StartedAt: now.Add(2 * time.Minute), Assignee: "Mio"}
	if err := store.SaveRun(context.Background(), second); err != nil {
		t.Fatal(err)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewJSONLStore(root)
	t.Cleanup(func() {
		if reloaded != nil {
			_ = reloaded.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	runs, err := reloaded.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID})
	if err != nil || len(runs) != 2 {
		t.Fatalf("reloaded runs = %#v err=%v", runs, err)
	}
	if runs[0].RunID != first.RunID || runs[0].Status != domaintask.RunStatusSucceeded || runs[1].RunID != second.RunID {
		t.Fatalf("reloaded chronology = %#v", runs)
	}
	if _, err := os.Stat(filepath.Join(root, "task_run.jsonl")); err != nil {
		t.Fatalf("task_run.jsonl missing: %v", err)
	}
}

func TestJSONLStoreRejectsInvalidRunOwnershipAndUnknownFields(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 7, 0, 0, 0, time.UTC)
	missingTask := domaintask.Run{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: modulecore.NewTaskID(), StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
	if err := store.SaveRun(context.Background(), missingTask); err == nil {
		t.Fatal("run for missing task was accepted")
	}
	missingID := missingTask
	missingID.TaskID = ""
	if err := store.SaveRun(context.Background(), missingID); err == nil {
		t.Fatal("run without task ID was accepted")
	}
	if err := os.WriteFile(store.runPath, []byte(`{"run_id":"run_00000000-0000-7000-8000-000000000000","task_id":"tsk_00000000-0000-7000-8000-000000000000","start_reason":"first","status":"running","started_at":"2026-09-05T07:00:00Z","parent_run_id":"run_00000000-0000-7000-8000-000000000001"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
		t.Fatal("unknown run field was accepted")
	}
}

func TestJSONLStoreRejectsRunHistoryCorruption(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(domaintask.Run, modulecore.TaskID, time.Time) domaintask.Run
	}{
		{name: "task_id", mutate: func(run domaintask.Run, otherTaskID modulecore.TaskID, _ time.Time) domaintask.Run {
			run.TaskID = otherTaskID
			return run
		}},
		{name: "start_reason", mutate: func(run domaintask.Run, _ modulecore.TaskID, _ time.Time) domaintask.Run {
			run.StartReason = domaintask.RunStartReasonExplicitRerun
			return run
		}},
		{name: "assignee", mutate: func(run domaintask.Run, _ modulecore.TaskID, _ time.Time) domaintask.Run {
			run.Assignee = "Shiro"
			return run
		}},
		{name: "started_at", mutate: func(run domaintask.Run, _ modulecore.TaskID, now time.Time) domaintask.Run {
			run.StartedAt = now.Add(time.Minute)
			return run
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewJSONLStore(t.TempDir())
			t.Cleanup(func() {
				if store != nil {
					_ = store.Close()
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 9, 5, 11, 0, 0, 0, time.UTC)
			task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "history", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
			if err := store.SaveTask(context.Background(), task); err != nil {
				t.Fatal(err)
			}
			otherTaskID := modulecore.NewTaskID()
			base := domaintask.Run{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now, Assignee: "Mio"}
			mutated := test.mutate(base, otherTaskID, now)
			if err := os.WriteFile(store.runPath, []byte(mustJSONLRun(t, base)+mustJSONLRun(t, mutated)), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
				t.Fatal("immutable run history rewrite was accepted")
			}
		})
	}

	t.Run("closed_terminal_rewrite", func(t *testing.T) {
		store, err := NewJSONLStore(t.TempDir())
		t.Cleanup(func() {
			if store != nil {
				_ = store.Close()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 5, 11, 30, 0, 0, time.UTC)
		task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "terminal history", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
		if err := store.SaveTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		base := domaintask.Run{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
		completedAt := now.Add(time.Minute)
		closed := base
		closed.Status = domaintask.RunStatusSucceeded
		closed.CompletedAt = &completedAt
		closed.Summary = "first"
		rewrite := closed
		rewrite.Summary = "rewritten"
		if err := os.WriteFile(store.runPath, []byte(mustJSONLRun(t, base)+mustJSONLRun(t, closed)+mustJSONLRun(t, rewrite)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
			t.Fatal("closed terminal rewrite was accepted")
		}
	})

	t.Run("two_active_runs", func(t *testing.T) {
		store, err := NewJSONLStore(t.TempDir())
		t.Cleanup(func() {
			if store != nil {
				_ = store.Close()
			}
		})
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
		task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "active history", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
		if err := store.SaveTask(context.Background(), task); err != nil {
			t.Fatal(err)
		}
		first := domaintask.Run{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
		second := first
		second.RunID = modulecore.NewRunID()
		if err := os.WriteFile(store.runPath, []byte(mustJSONLRun(t, first)+mustJSONLRun(t, second)), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := store.ListRuns(context.Background(), domaintask.RunFilter{}); err == nil {
			t.Fatal("multiple active runs were accepted")
		}
	})
}

func TestJSONLStoreConcurrentFirstActiveRunHasSingleWinner(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	t.Cleanup(func() {
		if store != nil {
			_ = store.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 5, 13, 0, 0, 0, time.UTC)
	task := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "concurrent", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	runs := []domaintask.Run{
		{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now},
		{WriterGeneration: 1, RunID: modulecore.NewRunID(), TaskID: task.TaskID, StartReason: domaintask.RunStartReasonExplicitRerun, Status: domaintask.RunStatusRunning, StartedAt: now.Add(time.Second)},
	}
	start := make(chan struct{})
	results := make(chan error, len(runs))
	var group sync.WaitGroup
	for _, run := range runs {
		group.Add(1)
		go func(value domaintask.Run) {
			defer group.Done()
			<-start
			results <- store.SaveRun(context.Background(), value)
		}(run)
	}
	close(start)
	group.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted concurrent active runs = %d, want 1", accepted)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := NewJSONLStore(root)
	t.Cleanup(func() {
		if reloaded != nil {
			_ = reloaded.Close()
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	items, err := reloaded.ListRuns(context.Background(), domaintask.RunFilter{TaskID: task.TaskID, Status: domaintask.RunStatusRunning})
	if err != nil || len(items) != 1 {
		t.Fatalf("reloaded active runs = %#v err=%v", items, err)
	}
}

func mustJSONLRun(t *testing.T, value domaintask.Run) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded) + "\n"
}

func TestJSONLStoreRejectsConcurrentWriter(t *testing.T) {
	root := t.TempDir()
	first, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if closer, ok := any(first).(interface{ Close() error }); ok {
		t.Cleanup(func() { _ = closer.Close() })
	}
	second, err := NewJSONLStore(root)
	if err == nil {
		if closer, ok := any(second).(interface{ Close() error }); ok {
			_ = closer.Close()
		}
		t.Fatal("second canonical Task writer accepted")
	}
}

func TestJSONLStoreWriterCloseAndReadOnly(t *testing.T) {
	root := t.TempDir()
	writer, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	now := time.Now().UTC()
	value := domaintask.Task{TaskID: modulecore.NewTaskID(), Title: "read-only", Route: domaintask.RouteGeneral, Status: domaintask.StatusQueued, Priority: domaintask.PriorityNormal, InterruptPolicy: domaintask.InterruptNotifyDoneOrBlocked, CreatedAt: now, UpdatedAt: now}
	if err := reader.SaveTask(context.Background(), value); err == nil {
		t.Fatal("reader accepted write")
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.SaveTask(context.Background(), value); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("closed writer error=%v", err)
	}
	reopened, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if _, err := os.Stat(filepath.Join(root, ".writer.lock")); err != nil {
		t.Fatal("lock inode must persist", err)
	}
}
func TestJSONLStoreWriterCrashReleasesOwnership(t *testing.T) {
	if root := os.Getenv("RENCROW_TASK_LOCK_CHILD_ROOT"); root != "" {
		store, err := NewJSONLStore(root)
		if err != nil {
			fmt.Fprintln(os.Stdout, "error:", err)
			os.Exit(2)
		}
		_ = store
		fmt.Fprintln(os.Stdout, "ready")
		var b [1]byte
		_, _ = os.Stdin.Read(b[:])
		runtime.KeepAlive(store)
		os.Exit(0)
	}
	root := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJSONLStoreWriterCrashReleasesOwnership$")
	cmd.Env = append(os.Environ(), "RENCROW_TASK_LOCK_CHILD_ROOT="+root)
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	scanner := bufio.NewScanner(output)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("child readiness: %q %v", scanner.Text(), scanner.Err())
	}
	second, err := NewJSONLStore(root)
	if second != nil {
		_ = second.Close()
	}
	if !errors.Is(err, ErrTaskWriterBusy) {
		t.Fatalf("live child writer accepted: %v", err)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	recovered, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal("dead process retained lock", err)
	}
	defer recovered.Close()
}

func TestTaskWriterGenerationRejectsCorruptCounters(t *testing.T) {
	for _, counter := range []string{"broken\n", "12", "1\n3\n", "18446744073709551615\n", strings.Repeat("1", 129) + "\n"} {
		t.Run(counter[:min(len(counter), 15)], func(t *testing.T) {
			root := t.TempDir()
			lockPath := filepath.Join(root, ".writer.lock")
			if err := os.WriteFile(lockPath, []byte(counter), 0600); err != nil {
				t.Fatal(err)
			}
			store, err := NewJSONLStore(root)
			if store != nil {
				_ = store.Close()
			}
			if err == nil {
				t.Fatal("corrupt counter accepted")
			}
			data, err := os.ReadFile(lockPath)
			if err != nil || string(data) != counter {
				t.Fatal("corrupt counter rewritten")
			}
		})
	}
}
func TestRunWriterGenerationCannotBeChangedOrReused(t *testing.T) {
	root := t.TempDir()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Now().UTC()
	task := domaintask.Task{Title: "owner", Assignee: "shiro", Route: domaintask.RouteOperations}
	task.ApplyDefaults(now)
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	generation, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	run := domaintask.Run{RunID: modulecore.NewRunID(), TaskID: task.TaskID, WriterGeneration: generation, StartReason: domaintask.RunStartReasonFirst, Status: domaintask.RunStatusRunning, StartedAt: now}
	missing := run
	missing.WriterGeneration = 0
	if err := store.SaveRun(context.Background(), missing); err == nil {
		t.Fatal("new Run missing writer generation accepted")
	}
	foreign := run
	foreign.WriterGeneration++
	if err := store.SaveRun(context.Background(), foreign); err == nil {
		t.Fatal("new Run with foreign writer generation accepted")
	}
	if err := store.SaveRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	changed := run
	changed.WriterGeneration++
	if err := store.SaveRun(context.Background(), changed); err == nil {
		t.Fatal("existing generation rewritten")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".writer.lock"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	reopened, err := NewJSONLStore(root)
	if reopened != nil {
		_ = reopened.Close()
	}
	if err == nil {
		t.Fatal("counter reset reused existing Run generation")
	}
}
