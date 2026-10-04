package task

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func taskOperationHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func TestJSONLStoreIdempotentTaskOperationCommitsAndReplaysReceipt(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	task := newExecutionFenceTestTask("operation task")
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}

	requestHash := taskOperationHash("bounded request")
	operationID := "op:test-operation-1"
	wantResult := json.RawMessage(`{"status":"saved","count":1}`)
	calls := 0
	gotResult, err := store.ExecuteIdempotentTaskOperation(ctx, task.TaskID, operationID, requestHash, func(tx domaintask.Store) (json.RawMessage, error) {
		calls++
		current, err := tx.GetTask(ctx, task.TaskID)
		if err != nil {
			return nil, err
		}
		current.Title = "committed by operation"
		current.UpdatedAt = current.UpdatedAt.Add(time.Minute)
		if err := tx.SaveTask(ctx, current); err != nil {
			return nil, err
		}
		return append(json.RawMessage(nil), wantResult...), nil
	})
	if err != nil {
		t.Fatalf("execute task operation: %v", err)
	}
	if string(gotResult) != string(wantResult) || calls != 1 {
		t.Fatalf("first result=%s callback calls=%d, want %s and 1", gotResult, calls, wantResult)
	}

	receipt, err := store.LookupTaskOperation(ctx, operationID)
	if err != nil {
		t.Fatalf("lookup committed receipt: %v", err)
	}
	if receipt.OperationID != operationID || receipt.TaskID != task.TaskID || receipt.RequestHash != requestHash || receipt.WriterGeneration == 0 || string(receipt.Result) != string(wantResult) {
		t.Fatalf("stored receipt = %#v", receipt)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	gotResult, err = store.ExecuteIdempotentTaskOperation(ctx, task.TaskID, operationID, requestHash, func(domaintask.Store) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"status":"duplicated"}`), nil
	})
	if err != nil {
		t.Fatalf("replay task operation after reopen: %v", err)
	}
	if string(gotResult) != string(wantResult) || calls != 1 {
		t.Fatalf("replay result=%s callback calls=%d, want stored %s and no callback", gotResult, calls, wantResult)
	}
	updated, err := store.GetTask(ctx, task.TaskID)
	if err != nil || updated.Title != "committed by operation" {
		t.Fatalf("operation task state after reopen = %#v err=%v", updated, err)
	}

	wal, err := os.ReadFile(filepath.Join(root, ".jsonlbatch.wal"))
	if err != nil {
		t.Fatal(err)
	}
	var preparedFiles []string
	scanner := bufio.NewScanner(strings.NewReader(string(wal)))
	for scanner.Scan() {
		var record struct {
			Kind  string `json:"kind"`
			Files []struct {
				Name string `json:"name"`
			} `json:"files"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
			t.Fatalf("decode batch WAL: %v", err)
		}
		if record.Kind == "prepare" {
			preparedFiles = preparedFiles[:0]
			for _, file := range record.Files {
				preparedFiles = append(preparedFiles, file.Name)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !containsString(preparedFiles, stateFilename) || !containsString(preparedFiles, taskOperationReceiptFilename) {
		t.Fatalf("latest batch prepare files = %v, want task state and receipt together", preparedFiles)
	}
}

func TestJSONLStoreTaskOperationCommitCrashBoundaryReconcilesByReceipt(t *testing.T) {
	if root := os.Getenv("RENCROW_TASK_OPERATION_CRASH_ROOT"); root != "" {
		store, err := NewJSONLStore(root)
		if err != nil {
			_, _ = os.Stderr.WriteString("open task operation crash child: " + err.Error())
			os.Exit(2)
		}
		taskID := modulecore.TaskID(os.Getenv("RENCROW_TASK_OPERATION_CRASH_TASK"))
		task := newExecutionFenceTestTask("before operation")
		task.TaskID = taskID
		ctx := context.Background()
		if err := store.SaveTask(ctx, task); err != nil {
			_, _ = os.Stderr.WriteString("save task in crash child: " + err.Error())
			os.Exit(3)
		}
		_, err = store.ExecuteIdempotentTaskOperation(ctx, taskID, "op:crash-boundary", taskOperationHash("crash-boundary"), func(tx domaintask.Store) (json.RawMessage, error) {
			current, err := tx.GetTask(ctx, taskID)
			if err != nil {
				return nil, err
			}
			current.Title = "committed before lost host response"
			if err := tx.SaveTask(ctx, current); err != nil {
				return nil, err
			}
			return json.RawMessage(`{"committed":true}`), nil
		})
		if err != nil {
			_, _ = os.Stderr.WriteString("commit task operation in crash child: " + err.Error())
			os.Exit(4)
		}
		// Exit without returning the result to the parent or closing the store.
		os.Exit(0)
	}

	root := t.TempDir()
	taskID := modulecore.NewTaskID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJSONLStoreTaskOperationCommitCrashBoundaryReconcilesByReceipt$")
	command.Env = append(os.Environ(), "RENCROW_TASK_OPERATION_CRASH_ROOT="+root, "RENCROW_TASK_OPERATION_CRASH_TASK="+string(taskID))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash-boundary child error=%v output=%s", err, output)
	}

	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	receipt, err := store.LookupTaskOperation(context.Background(), "op:crash-boundary")
	if err != nil {
		t.Fatalf("lookup receipt after owner crash: %v", err)
	}
	if receipt.TaskID != taskID || receipt.WriterGeneration == 0 || string(receipt.Result) != `{"committed":true}` {
		t.Fatalf("reconciled receipt = %#v", receipt)
	}
	callbackCalled := false
	result, err := store.ExecuteIdempotentTaskOperation(context.Background(), taskID, "op:crash-boundary", taskOperationHash("crash-boundary"), func(domaintask.Store) (json.RawMessage, error) {
		callbackCalled = true
		return json.RawMessage(`null`), nil
	})
	if err != nil || callbackCalled || string(result) != `{"committed":true}` {
		t.Fatalf("post-crash replay result=%s callback_called=%t err=%v", result, callbackCalled, err)
	}
	updated, err := store.GetTask(context.Background(), taskID)
	if err != nil || updated.Title != "committed before lost host response" {
		t.Fatalf("task state after owner crash = %#v err=%v", updated, err)
	}
}

func TestJSONLStoreIdempotentTaskOperationRejectsRequestConflicts(t *testing.T) {
	store, err := NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	firstTask := newExecutionFenceTestTask("operation conflict task")
	secondTask := newExecutionFenceTestTask("other operation task")
	if err := store.SaveTask(ctx, firstTask); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(ctx, secondTask); err != nil {
		t.Fatal(err)
	}
	operationID := "op:conflict"
	requestHash := taskOperationHash("request A")
	calls := 0
	_, err = store.ExecuteIdempotentTaskOperation(ctx, firstTask.TaskID, operationID, requestHash, func(domaintask.Store) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"ok":true}`), nil
	})
	if err != nil {
		t.Fatalf("initial operation: %v", err)
	}
	for _, tc := range []struct {
		name        string
		taskID      modulecore.TaskID
		requestHash string
	}{
		{name: "different hash", taskID: firstTask.TaskID, requestHash: taskOperationHash("request B")},
		{name: "different task", taskID: secondTask.TaskID, requestHash: requestHash},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := store.ExecuteIdempotentTaskOperation(ctx, tc.taskID, operationID, tc.requestHash, func(domaintask.Store) (json.RawMessage, error) {
				calls++
				return json.RawMessage(`null`), nil
			})
			if !errors.Is(err, ErrTaskOperationConflict) {
				t.Fatalf("conflicting operation error = %v, want ErrTaskOperationConflict", err)
			}
		})
	}
	if calls != 1 {
		t.Fatalf("operation callback calls=%d, want 1", calls)
	}
}

func TestJSONLStoreTaskOperationReceiptCorruptionFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name string
		data func(t *testing.T) []byte
	}{
		{name: "malformed JSON", data: func(*testing.T) []byte { return []byte(`{"operation_id":` + "\n") }},
		{name: "conflicting duplicate", data: func(t *testing.T) []byte {
			taskID := modulecore.NewTaskID()
			first := TaskOperationReceipt{OperationID: "op:duplicate", RequestHash: taskOperationHash("one"), TaskID: taskID, Result: json.RawMessage(`{"value":1}`), WriterGeneration: 1}
			second := first
			second.Result = json.RawMessage(`{"value":2}`)
			one, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			two, err := json.Marshal(second)
			if err != nil {
				t.Fatal(err)
			}
			return append(append(append(one, '\n'), two...), '\n')
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store, err := NewJSONLStore(root)
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, taskOperationReceiptFilename), tc.data(t), 0o600); err != nil {
				t.Fatal(err)
			}
			store, err = NewJSONLStore(root)
			if store != nil {
				_ = store.Close()
			}
			if !errors.Is(err, ErrTaskOperationReceiptCorrupt) {
				t.Fatalf("store open error = %v, want corrupt-receipt error", err)
			}
		})
	}
}

func TestJSONLStoreOpensExistingFourFileTaskStore(t *testing.T) {
	root := t.TempDir()
	for _, filename := range []string{stateFilename, runFilename, contextFilename, notificationsFilename} {
		if err := os.WriteFile(filepath.Join(root, filename), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("open existing four-file task store: %v", err)
	}
	for _, filename := range []string{taskOperationReceiptFilename, taskExecutionFenceFilename} {
		if _, err := os.Stat(filepath.Join(root, filename)); err != nil {
			t.Fatalf("new owner file %q was not initialized: %v", filename, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatalf("read upgraded four-file task store: %v", err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLStoreExecutionFencePersistsAcrossReopenAndExactRelease(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	taskA := newExecutionFenceTestTask("durable fenced task")
	taskB := newExecutionFenceTestTask("unrelated task")
	if err := store.SaveTask(ctx, taskA); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveTask(ctx, taskB); err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireTaskExecutionFence(ctx, taskA.TaskID, "capability:stable-1"); err != nil {
		t.Fatalf("acquire fence: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	updated := taskA
	updated.UpdatedAt = updated.UpdatedAt.Add(time.Minute)
	if err := store.TaskTransaction(ctx, taskA.TaskID, func(tx domaintask.Store) error { return tx.SaveTask(ctx, updated) }); !errors.Is(err, ErrTaskExecutionFenceActive) {
		t.Fatalf("same-task transaction error = %v, want active fence", err)
	}
	if err := store.AcquireTaskExecutionFence(ctx, taskA.TaskID, "capability:other"); !errors.Is(err, ErrTaskExecutionFenceActive) {
		t.Fatalf("another capability acquire error = %v, want active fence", err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskA.TaskID, "capability:wrong"); !errors.Is(err, ErrTaskExecutionFenceMismatch) {
		t.Fatalf("mismatched release error = %v, want fence mismatch", err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskB.TaskID, "capability:stable-1"); !errors.Is(err, ErrTaskExecutionFenceMismatch) {
		t.Fatalf("wrong-task release error = %v, want fence mismatch", err)
	}
	status, err := store.GetTaskExecutionFence(ctx, taskA.TaskID)
	if err != nil {
		t.Fatalf("query active fence status: %v", err)
	}
	currentGeneration, err := store.WriterGeneration()
	if err != nil {
		t.Fatal(err)
	}
	if status.TaskID != taskA.TaskID || status.FenceID != "capability:stable-1" || status.State != TaskExecutionFenceStateActive || status.AcquireWriterGeneration >= currentGeneration || status.ReleaseWriterGeneration != 0 {
		t.Fatalf("reopened fence status=%#v current_generation=%d, want old-generation active capability", status, currentGeneration)
	}

	updatedB := taskB
	updatedB.Title = "unrelated task progressed"
	if err := store.TaskTransaction(ctx, taskB.TaskID, func(tx domaintask.Store) error { return tx.SaveTask(ctx, updatedB) }); err != nil {
		t.Fatalf("unrelated task transaction: %v", err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskA.TaskID, "capability:stable-1"); err != nil {
		t.Fatalf("exact fence release: %v", err)
	}
	status, err = store.GetTaskExecutionFence(ctx, taskA.TaskID)
	if err != nil || status.State != TaskExecutionFenceStateReleased || status.FenceID != "capability:stable-1" || status.ReleaseWriterGeneration < status.AcquireWriterGeneration {
		t.Fatalf("released fence status=%#v err=%v", status, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.TaskTransaction(ctx, taskA.TaskID, func(tx domaintask.Store) error { return tx.SaveTask(ctx, updated) }); err != nil {
		t.Fatalf("same-task transaction after durable release: %v", err)
	}
}

func TestJSONLStoreExecutionFenceAcquireReplayAndLocalCallbackErrorRelease(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	taskID := modulecore.NewTaskID()
	if err := store.AcquireTaskExecutionFence(ctx, taskID, "capability:retry"); err != nil {
		t.Fatalf("initial acquire: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AcquireTaskExecutionFence(ctx, taskID, "capability:retry"); err != nil {
		t.Fatalf("idempotent acquire retry after reopen: %v", err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskID, "capability:retry"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskID, "capability:retry"); err != nil {
		t.Fatalf("idempotent release retry: %v", err)
	}
	fenceLog, err := os.ReadFile(filepath.Join(root, taskExecutionFenceFilename))
	if err != nil {
		t.Fatal(err)
	}
	if got := len(strings.Split(strings.TrimSpace(string(fenceLog)), "\n")); got != 2 {
		t.Fatalf("fence log records after acquire/release retries=%d, want one acquire and one release", got)
	}
	if err := store.AcquireTaskExecutionFence(ctx, taskID, "capability:retry"); !errors.Is(err, ErrTaskExecutionFenceMismatch) {
		t.Fatalf("released capability reuse error = %v, want mismatch", err)
	}

	callbackErr := errors.New("external callback failed")
	if err := store.WithTaskExecutionFence(ctx, taskID, func() error { return callbackErr }); !errors.Is(err, callbackErr) {
		t.Fatalf("local fenced callback error = %v, want original callback error", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.TaskTransaction(ctx, taskID, func(domaintask.Store) error { return nil }); err != nil {
		t.Fatalf("local callback error left an active fence after reopen: %v", err)
	}
}

func TestJSONLStoreExecutionFenceReleasePersistenceFailureStaysBlocked(t *testing.T) {
	root := t.TempDir()
	ctx := context.Background()
	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	taskID := modulecore.NewTaskID()
	fencePath := filepath.Join(root, taskExecutionFenceFilename)
	backupPath := filepath.Join(root, "task_execution_fences.saved")
	restored := false
	t.Cleanup(func() {
		if !restored {
			_ = os.Remove(fencePath)
			_ = os.Rename(backupPath, fencePath)
		}
		if store != nil {
			_ = store.Close()
		}
	})
	err = store.WithTaskExecutionFence(ctx, taskID, func() error {
		if err := os.Rename(fencePath, backupPath); err != nil {
			return err
		}
		if err := os.Mkdir(fencePath, 0o700); err != nil {
			_ = os.Rename(backupPath, fencePath)
			return err
		}
		return nil
	})
	if err == nil {
		t.Fatal("fenced callback succeeded despite an unpersistable release")
	}
	if err := os.Remove(fencePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupPath, fencePath); err != nil {
		t.Fatal(err)
	}
	restored = true
	if err := store.TaskTransaction(ctx, taskID, func(domaintask.Store) error { return nil }); !errors.Is(err, ErrTaskExecutionFenceActive) {
		t.Fatalf("same-task transaction after failed release = %v, want active fence", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TaskTransaction(ctx, taskID, func(domaintask.Store) error { return nil }); !errors.Is(err, ErrTaskExecutionFenceActive) {
		t.Fatalf("same-task transaction after reopen = %v, want active fence", err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskID, "local-invalid"); !errors.Is(err, ErrTaskExecutionFenceMismatch) {
		t.Fatalf("wrong capability release after reopen = %v, want mismatch", err)
	}
	status, err := store.GetTaskExecutionFence(ctx, taskID)
	if err != nil || status.State != TaskExecutionFenceStateActive || status.FenceID == "" {
		t.Fatalf("query capability after release persistence failure: status=%#v err=%v", status, err)
	}
	if err := store.ReleaseTaskExecutionFence(ctx, taskID, status.FenceID); err != nil {
		t.Fatalf("release recovered capability: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.TaskTransaction(ctx, taskID, func(domaintask.Store) error { return nil }); err != nil {
		t.Fatalf("same-task transaction after durable release: %v", err)
	}
}

func TestJSONLStoreExecutionFenceCrashLeavesDurableAdmission(t *testing.T) {
	if root := os.Getenv("RENCROW_TASK_FENCE_CRASH_ROOT"); root != "" {
		store, err := NewJSONLStore(root)
		if err != nil {
			os.Exit(2)
		}
		taskID := modulecore.TaskID(os.Getenv("RENCROW_TASK_FENCE_CRASH_TASK"))
		if err := store.AcquireTaskExecutionFence(context.Background(), taskID, "capability:crash"); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}

	root := t.TempDir()
	taskID := modulecore.NewTaskID()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestJSONLStoreExecutionFenceCrashLeavesDurableAdmission$")
	command.Env = append(os.Environ(), "RENCROW_TASK_FENCE_CRASH_ROOT="+root, "RENCROW_TASK_FENCE_CRASH_TASK="+string(taskID))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash-boundary child error=%v output=%s", err, output)
	}

	store, err := NewJSONLStore(root)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	callbackCalled := false
	err = store.TaskTransaction(context.Background(), taskID, func(domaintask.Store) error {
		callbackCalled = true
		return nil
	})
	if !errors.Is(err, ErrTaskExecutionFenceActive) || callbackCalled {
		t.Fatalf("post-crash task transaction error=%v callback_called=%t, want blocked before callback", err, callbackCalled)
	}
	if err := store.ReleaseTaskExecutionFence(context.Background(), taskID, "capability:crash"); err != nil {
		t.Fatalf("explicit post-crash release: %v", err)
	}
}

func TestJSONLStoreWriterAdmissionRejectsFenceGenerationAfterCounterLoss(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset func(t *testing.T, path string)
	}{
		{name: "lost counter", reset: func(t *testing.T, path string) {
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "truncated counter", reset: func(t *testing.T, path string) {
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "reset lower counter", reset: func(t *testing.T, path string) {
			if err := os.WriteFile(path, []byte("1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			store := openTaskStoreAtGeneration(t, root, 3)
			taskID := modulecore.NewTaskID()
			if err := store.AcquireTaskExecutionFence(context.Background(), taskID, "capability:generation-floor"); err != nil {
				t.Fatalf("acquire generation-3 fence: %v", err)
			}
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			tc.reset(t, filepath.Join(root, ".writer.lock"))

			reopened, err := NewJSONLStore(root)
			if reopened != nil {
				_ = reopened.Close()
			}
			if err == nil {
				t.Fatal("writer admission accepted a generation not greater than the persisted active fence")
			}
		})
	}
}

func TestJSONLStoreWriterAdmissionRejectsReleasedFenceGenerationFloor(t *testing.T) {
	root := t.TempDir()
	store := openTaskStoreAtGeneration(t, root, 2)
	taskID := modulecore.NewTaskID()
	if err := store.AcquireTaskExecutionFence(context.Background(), taskID, "capability:released-floor"); err != nil {
		t.Fatalf("acquire generation-2 fence: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopenedAtThree, err := NewJSONLStore(root)
	if err != nil {
		t.Fatalf("reopen store at generation 3: %v", err)
	}
	store = reopenedAtThree
	if generation, err := store.WriterGeneration(); err != nil || generation != 3 {
		_ = store.Close()
		t.Fatalf("writer generation=%d err=%v, want 3", generation, err)
	}
	if err := store.ReleaseTaskExecutionFence(context.Background(), taskID, "capability:released-floor"); err != nil {
		t.Fatalf("release generation-2 fence at generation 3: %v", err)
	}
	status, err := store.GetTaskExecutionFence(context.Background(), taskID)
	if err != nil || status.AcquireWriterGeneration != 2 || status.ReleaseWriterGeneration != 3 {
		t.Fatalf("released fence status=%#v err=%v, want acquire=2 release=3", status, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".writer.lock"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewJSONLStore(root)
	if reopened != nil {
		_ = reopened.Close()
	}
	if err == nil {
		t.Fatal("writer admission accepted generation 3, equal to the persisted release generation")
	}
}

func TestJSONLStoreWriterAdmissionRejectsReceiptGenerationFloor(t *testing.T) {
	root := t.TempDir()
	store := openTaskStoreAtGeneration(t, root, 3)
	task := newExecutionFenceTestTask("receipt generation floor")
	if err := store.SaveTask(context.Background(), task); err != nil {
		t.Fatalf("save receipt task: %v", err)
	}
	_, err := store.ExecuteIdempotentTaskOperation(context.Background(), task.TaskID, "op:generation-floor", taskOperationHash("generation-floor"), func(domaintask.Store) (json.RawMessage, error) {
		return json.RawMessage(`{"committed":true}`), nil
	})
	if err != nil {
		t.Fatalf("commit generation-3 receipt: %v", err)
	}
	receipt, err := store.LookupTaskOperation(context.Background(), "op:generation-floor")
	if err != nil || receipt.WriterGeneration != 3 {
		t.Fatalf("receipt=%#v err=%v, want generation 3", receipt, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".writer.lock"), []byte("2\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewJSONLStore(root)
	if reopened != nil {
		_ = reopened.Close()
	}
	if err == nil {
		t.Fatal("writer admission accepted generation 3, equal to the persisted receipt generation")
	}
}

func openTaskStoreAtGeneration(t *testing.T, root string, generation uint64) *JSONLStore {
	t.Helper()
	var store *JSONLStore
	for current := uint64(1); current <= generation; current++ {
		var err error
		store, err = NewJSONLStore(root)
		if err != nil {
			t.Fatalf("open store at generation %d: %v", current, err)
		}
		got, err := store.WriterGeneration()
		if err != nil || got != current {
			_ = store.Close()
			t.Fatalf("writer generation=%d err=%v, want %d", got, err, current)
		}
		if current != generation {
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
		}
	}
	return store
}

func containsString(items []string, wanted string) bool {
	for _, item := range items {
		if item == wanted {
			return true
		}
	}
	return false
}
