package task

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

var indexTestNow = time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)

func TestStoreWithoutIndexOptionHasNoIndex(t *testing.T) {
	store, err := NewJSONLStoreWithOptions(t.TempDir(), OpenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, ok := store.IndexStats(); ok {
		t.Fatal("a store opened without the Index option reports an index")
	}
}

func TestIndexedStoreAndReaderSeeTheSameLog(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	for n := 0; n < 5; n++ {
		task := indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
		task.Status = domaintask.StatusRunning
		task.UpdatedAt = task.UpdatedAt.Add(time.Minute)
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	requireIndexCoversLog(t, store, root)

	reader, err := NewJSONLReader(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	fromReader, err := reader.ListTasks(ctx, domaintask.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	fromIndex, err := store.ListTasks(ctx, domaintask.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(fromReader)
	b, _ := json.Marshal(fromIndex)
	if string(a) != string(b) || len(fromIndex) != 5 {
		t.Fatalf("reader and index disagree:\nreader: %s\nindex : %s", a, b)
	}
}

// A second writer path (here a bare jsonlbatch.Store without the hook) can leave
// committed bytes the index was never told about. The next transaction absorbs
// them before it reads anything, so no ID in the log is ever missing from the
// index when a write decides what exists.
func TestNextTransactionAbsorbsCommittedBytesTheIndexWasNotToldAbout(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	first := indexedTestTask(1, indexTestNow)
	if err := store.SaveTask(ctx, first); err != nil {
		t.Fatal(err)
	}

	other, err := jsonlbatch.New(root, taskBatchFilenames())
	if err != nil {
		t.Fatal(err)
	}
	second := indexedTestTask(2, indexTestNow.Add(time.Second))
	if err := other.Write(ctx, func() (map[string][]byte, error) {
		return map[string][]byte{stateFilename: marshalLine(t, second)}, nil
	}); err != nil {
		t.Fatal(err)
	}

	third := indexedTestTask(3, indexTestNow.Add(2*time.Second))
	if err := store.SaveTask(ctx, third); err != nil {
		t.Fatalf("SaveTask after an unannounced commit: %v", err)
	}
	if _, err := store.GetTask(ctx, second.TaskID); err != nil {
		t.Fatalf("task committed behind the index's back is still unknown: %v", err)
	}
	requireIndexCoversLog(t, store, root)
}

func TestDirtyIndexResynchronizesBeforeAReadAnswers(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	if err := store.SaveTask(ctx, indexedTestTask(1, indexTestNow)); err != nil {
		t.Fatal(err)
	}
	other, err := jsonlbatch.New(root, taskBatchFilenames())
	if err != nil {
		t.Fatal(err)
	}
	second := indexedTestTask(2, indexTestNow.Add(time.Second))
	if err := other.Write(ctx, func() (map[string][]byte, error) {
		return map[string][]byte{stateFilename: marshalLine(t, second)}, nil
	}); err != nil {
		t.Fatal(err)
	}
	store.idx.dirty.Store(true)
	got, err := store.GetTask(ctx, second.TaskID)
	if err != nil || got.TaskID != second.TaskID {
		t.Fatalf("read from a dirty index = %v, %v; want the committed task", got.TaskID, err)
	}
	if stats, _ := store.IndexStats(); stats.Dirty {
		t.Fatal("index stayed dirty after the resync")
	}
	requireIndexCoversLog(t, store, root)
}

func TestHookThatCannotAbsorbACommitMarksTheIndexDirty(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	if err := store.SaveTask(ctx, indexedTestTask(1, indexTestNow)); err != nil {
		t.Fatal(err)
	}
	// An append that does not continue the indexed prefix cannot be applied.
	store.idx.onCommit(ctx, []jsonlbatch.CommittedAppend{{Name: stateFilename, Offset: 1, Payload: []byte("{}\n")}})
	if stats, _ := store.IndexStats(); !stats.Dirty {
		t.Fatal("index is not dirty after a commit it could not absorb")
	}
	// The files did not change, so the next resync finds nothing to replay.
	if err := store.SaveTask(ctx, indexedTestTask(2, indexTestNow.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if stats, _ := store.IndexStats(); stats.Dirty {
		t.Fatal("index stayed dirty after a successful transaction")
	}
	requireIndexCoversLog(t, store, root)
}

// ErrCommitUncertain leaves one of two shapes behind. Neither lets the index move
// on its own: it is marked dirty and the next lock holder decides from the WAL.
func TestCommitUncertainShapesAreResolvedFromTheLog(t *testing.T) {
	uncertain := errors.Join(jsonlbatch.ErrCommitUncertain, jsonlbatch.ErrRecoveryRequired)
	ctx := context.Background()

	t.Run("commit record never written is rolled back by the next writer", func(t *testing.T) {
		root := t.TempDir()
		store := openIndexed(t, root)
		kept := indexedTestTask(1, indexTestNow)
		if err := store.SaveTask(ctx, kept); err != nil {
			t.Fatal(err)
		}
		lost := indexedTestTask(2, indexTestNow.Add(time.Second))
		craftWAL(t, root, map[string][]byte{stateFilename: marshalLine(t, lost)}, false)
		store.idx.noteWriteResult(uncertain)

		if _, err := store.GetTask(ctx, kept.TaskID); !errors.Is(err, jsonlbatch.ErrRecoveryRequired) {
			t.Fatalf("read while the WAL is pending = %v, want ErrRecoveryRequired (fail closed, like the original path)", err)
		}
		next := indexedTestTask(3, indexTestNow.Add(2*time.Second))
		if err := store.SaveTask(ctx, next); err != nil {
			t.Fatalf("SaveTask after recovery: %v", err)
		}
		if _, err := store.GetTask(ctx, lost.TaskID); !errors.Is(err, domaintask.ErrNotFound) {
			t.Fatalf("rolled-back task = %v, want ErrNotFound", err)
		}
		if _, err := store.GetTask(ctx, next.TaskID); err != nil {
			t.Fatal(err)
		}
		if stats, _ := store.IndexStats(); stats.Dirty {
			t.Fatal("index still dirty after the WAL was settled")
		}
		requireIndexCoversLog(t, store, root)
	})

	t.Run("commit record written is replayed by the next read", func(t *testing.T) {
		root := t.TempDir()
		store := openIndexed(t, root)
		if err := store.SaveTask(ctx, indexedTestTask(1, indexTestNow)); err != nil {
			t.Fatal(err)
		}
		committed := indexedTestTask(2, indexTestNow.Add(time.Second))
		craftWAL(t, root, map[string][]byte{stateFilename: marshalLine(t, committed)}, true)
		store.idx.noteWriteResult(uncertain)
		got, err := store.GetTask(ctx, committed.TaskID)
		if err != nil || got.TaskID != committed.TaskID {
			t.Fatalf("committed-but-unannounced task = %v, %v", got.TaskID, err)
		}
		requireIndexCoversLog(t, store, root)
	})

	t.Run("commit record written is replayed by the next write", func(t *testing.T) {
		root := t.TempDir()
		store := openIndexed(t, root)
		if err := store.SaveTask(ctx, indexedTestTask(1, indexTestNow)); err != nil {
			t.Fatal(err)
		}
		committed := indexedTestTask(2, indexTestNow.Add(time.Second))
		craftWAL(t, root, map[string][]byte{stateFilename: marshalLine(t, committed)}, true)
		store.idx.noteWriteResult(uncertain)
		// Re-creating the uncertain Task must see it; this is the double-create
		// the index must never allow.
		err := store.TaskTransaction(ctx, committed.TaskID, func(tx domaintask.Store) error {
			if _, err := tx.GetTask(ctx, committed.TaskID); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			t.Fatalf("transaction after an uncertain commit does not see the committed task: %v", err)
		}
		requireIndexCoversLog(t, store, root)
	})
}

func TestCorruptLineIsReportedAsCorruptNeverAsMissing(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	victim := indexedTestTask(1, indexTestNow)
	other := indexedTestTask(2, indexTestNow.Add(time.Second))
	for _, task := range []domaintask.Task{victim, other} {
		if err := store.SaveTask(ctx, task); err != nil {
			t.Fatal(err)
		}
	}
	victim.Status = domaintask.StatusRunning
	victim.UpdatedAt = victim.UpdatedAt.Add(time.Minute)
	if err := store.SaveTask(ctx, victim); err != nil {
		t.Fatal(err)
	}

	key, err := parseCanonicalKey(string(victim.TaskID), taskKeyPrefix)
	if err != nil {
		t.Fatal(err)
	}
	pos, ok := store.idx.taskTailLocked(key)
	if !ok {
		t.Fatal("victim has no indexed line")
	}
	statePath := filepath.Join(root, stateFilename)
	data, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	line := data[pos.off : pos.off+uint64(pos.length)]
	at := strings.Index(string(line), "indexed task")
	if at < 0 {
		t.Fatalf("title not found in %s", line)
	}
	// Flip one character inside a string: still valid JSON, still the right
	// length, only the CRC32C taken at write time can tell.
	data[pos.off+uint64(at)] ^= 0x01
	if err := os.WriteFile(statePath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	_, err = store.GetTask(ctx, victim.TaskID)
	if !errors.Is(err, ErrRecordCorrupt) || errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("GetTask(corrupt) = %v, want ErrRecordCorrupt and never ErrNotFound", err)
	}
	if got, err := store.GetTask(ctx, other.TaskID); err != nil || got.TaskID != other.TaskID {
		t.Fatalf("an unrelated Task became unreadable: %v", err)
	}
	if _, err := store.ListTasks(ctx, domaintask.Filter{}); !errors.Is(err, ErrRecordCorrupt) {
		t.Fatalf("ListTasks over a corrupt line = %v, want ErrRecordCorrupt", err)
	}
	if stats, _ := store.IndexStats(); stats.CorruptReads == 0 {
		t.Fatal("corrupt reads are not counted for health reporting")
	}
}

func TestReadBeyondTheEndOfTheFileIsCorruptNotMissing(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	task := indexedTestTask(1, indexTestNow)
	if err := store.SaveTask(ctx, task); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(filepath.Join(root, stateFilename), 10); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetTask(ctx, task.TaskID); !errors.Is(err, ErrRecordCorrupt) || errors.Is(err, domaintask.ErrNotFound) {
		t.Fatalf("GetTask after truncation = %v, want ErrRecordCorrupt", err)
	}
}

func TestWriteFailsClosedWhenAFileLostCommittedBytes(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	for n := 1; n <= 3; n++ {
		if err := store.SaveTask(ctx, indexedTestTask(n, indexTestNow.Add(time.Duration(n)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
	statePath := filepath.Join(root, stateFilename)
	info, err := os.Stat(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(statePath, info.Size()/2); err != nil {
		t.Fatal(err)
	}
	err = store.SaveTask(ctx, indexedTestTask(4, indexTestNow.Add(time.Hour)))
	if !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("SaveTask over a shrunken log = %v, want ErrIndexInconsistent", err)
	}
	after, err := os.Stat(statePath)
	if err != nil || after.Size() != info.Size()/2 {
		t.Fatalf("the refused write changed the log: %v %v", after, err)
	}
}

func TestOpenFailsClosedOnLogsTheIndexCannotAbsorb(t *testing.T) {
	task := indexedTestTask(1, indexTestNow)
	run := indexedTestRun(task, 1, 1, indexTestNow)
	secondRun := indexedTestRun(task, 2, 1, indexTestNow.Add(time.Second))
	orphan := indexedTestTask(9, indexTestNow)
	upperID := task
	upperID.TaskID = modulecore.TaskID("tsk_" + strings.ToUpper(strings.TrimPrefix(string(task.TaskID), "tsk_")))
	receipt := TaskOperationReceipt{OperationID: "op:dup", RequestHash: taskOperationHash("a"), Scope: TaskOperationScopeTask, TaskID: task.TaskID, Result: json.RawMessage(`{"v":1}`), WriterGeneration: 1}
	receiptOther := receipt
	receiptOther.Result = json.RawMessage(`{"v":2}`)

	cases := []struct {
		name  string
		files map[string][]byte
		want  []error
	}{
		{name: "unterminated final line", files: map[string][]byte{stateFilename: append(marshalLine(t, task), []byte(`{"task_id"`)...)}, want: []error{ErrIndexInconsistent}},
		{name: "invalid task", files: map[string][]byte{stateFilename: []byte(`{"task_id":"tsk_x"}` + "\n")}, want: []error{ErrIndexInconsistent}},
		{name: "unknown field", files: map[string][]byte{stateFilename: []byte(`{"job_id":"legacy"}` + "\n")}, want: []error{ErrIndexInconsistent}},
		{name: "non-canonical task id", files: map[string][]byte{stateFilename: marshalLine(t, upperID)}, want: []error{ErrIndexInconsistent, errNonCanonicalID}},
		{name: "run without task", files: map[string][]byte{runFilename: marshalLine(t, run)}, want: []error{ErrIndexInconsistent}},
		{name: "context without task", files: map[string][]byte{contextFilename: marshalLine(t, domaintask.SharedRoleContext{TaskID: orphan.TaskID, UpdatedAt: indexTestNow})}, want: []error{ErrIndexInconsistent}},
		{name: "two active runs", files: map[string][]byte{stateFilename: marshalLine(t, task), runFilename: append(marshalLine(t, run), marshalLine(t, secondRun)...)}, want: []error{ErrIndexInconsistent}},
		{name: "conflicting receipts", files: map[string][]byte{receiptsName: append(marshalLine(t, receipt), marshalLine(t, receiptOther)...)}, want: []error{ErrIndexInconsistent, ErrTaskOperationReceiptCorrupt}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for name, data := range tc.files {
				if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			store, err := NewJSONLStoreWithOptions(root, OpenOptions{Index: true})
			if err == nil {
				_ = store.Close()
				t.Fatal("store opened over a log the index cannot absorb")
			}
			for _, want := range tc.want {
				if !errors.Is(err, want) {
					t.Errorf("open error = %v, want it to wrap %v", err, want)
				}
			}
		})
	}
}

const receiptsName = taskOperationReceiptFilename

func TestNonCanonicalIDsAreRefusedBeforeTheyReachTheLog(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	task := indexedTestTask(1, indexTestNow)
	task.TaskID = modulecore.TaskID("tsk_" + strings.ToUpper(strings.TrimPrefix(string(task.TaskID), "tsk_")))
	if err := task.Validate(); err != nil {
		t.Fatalf("test premise: the upper-case spelling must pass Validate, got %v", err)
	}
	if err := store.SaveTask(ctx, task); !errors.Is(err, errNonCanonicalID) {
		t.Fatalf("SaveTask(upper-case ID) = %v, want errNonCanonicalID", err)
	}
	if info, err := os.Stat(filepath.Join(root, stateFilename)); err != nil || info.Size() != 0 {
		t.Fatalf("refused Task reached the log: %v %v", info, err)
	}
}

// A record the index would refuse must never be committed: it would make every
// later replay and start fail closed.
func TestDryRunRefusesWhatTheIndexCannotAbsorbBeforeCommit(t *testing.T) {
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	if err := store.SaveTask(ctx, indexedTestTask(1, indexTestNow)); err != nil {
		t.Fatal(err)
	}
	statsBefore, _ := store.IndexStats()
	sizesBefore := scanLog(t, root).bytes

	stranger := indexedTestTask(99, indexTestNow)
	orphanRun := indexedTestRun(stranger, 99, 1, indexTestNow)
	err := store.writeBatch(ctx, func() (map[string][]byte, error) {
		return map[string][]byte{runFilename: marshalLine(t, orphanRun)}, nil
	})
	if !errors.Is(err, ErrIndexInconsistent) {
		t.Fatalf("writeBatch with a run for an unknown task = %v, want ErrIndexInconsistent", err)
	}
	sizesAfter := scanLog(t, root).bytes
	for kind, size := range sizesBefore {
		if sizesAfter[kind] != size {
			t.Fatalf("%s grew from %d to %d although the write was refused", kindFilename[kind], size, sizesAfter[kind])
		}
	}
	statsAfter, _ := store.IndexStats()
	if statsAfter.Tasks != statsBefore.Tasks || statsAfter.Runs != statsBefore.Runs || statsAfter.Dirty {
		t.Fatalf("refused write changed the index: before %+v after %+v", statsBefore, statsAfter)
	}
	// The store keeps working.
	if err := store.SaveTask(ctx, indexedTestTask(2, indexTestNow.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
}

func TestStateIsAppliedBeforeRunsEvenThoughFilesAppendInNameOrder(t *testing.T) {
	// task_context.jsonl sorts before task_state.jsonl, so a transaction that
	// creates a Task and its context appends the context first.
	root := t.TempDir()
	store := openIndexed(t, root)
	ctx := context.Background()
	task := indexedTestTask(1, indexTestNow)
	err := store.Transaction(ctx, func(tx domaintask.Store) error {
		if err := tx.SaveTask(ctx, task); err != nil {
			return err
		}
		generation, err := tx.WriterGeneration()
		if err != nil {
			return err
		}
		if err := tx.SaveRun(ctx, indexedTestRun(task, 1, generation, indexTestNow)); err != nil {
			return err
		}
		return tx.SaveContext(ctx, domaintask.SharedRoleContext{TaskID: task.TaskID, CurrentPlan: "plan", UpdatedAt: indexTestNow})
	})
	if err != nil {
		t.Fatal(err)
	}
	requireIndexCoversLog(t, store, root)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened := openIndexed(t, root)
	requireIndexCoversLog(t, reopened, root)
}
