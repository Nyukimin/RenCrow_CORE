package task

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// benchObserverTaskCount is how many Task/Run pairs the store holds before the
// timed loop, so each transaction decodes a realistic amount of history.
const benchObserverTaskCount = 2000

func newObserverBenchStore(b *testing.B, observed bool) (*JSONLStore, domaintask.Task) {
	b.Helper()
	root := b.TempDir()
	// Open and close once so the next open is writer generation 2, then lay down
	// the history as JSONL directly: preloading through transactions is quadratic.
	first, err := NewJSONLStore(root)
	if err != nil {
		b.Fatal(err)
	}
	generation, err := first.WriterGeneration()
	if err != nil {
		b.Fatal(err)
	}
	if err := first.Close(); err != nil {
		b.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	var states, runs bytes.Buffer
	var last domaintask.Task
	for i := 0; i < benchObserverTaskCount; i++ {
		last = snapshotTestTask(now.Add(time.Duration(i) * time.Second))
		run := snapshotTestRun(last, generation, now.Add(time.Duration(i+1)*time.Second), domaintask.RunStatusSucceeded)
		for _, item := range []struct {
			buf   *bytes.Buffer
			value any
		}{{&states, last}, {&runs, run}} {
			encoded, err := json.Marshal(item.value)
			if err != nil {
				b.Fatal(err)
			}
			item.buf.Write(encoded)
			item.buf.WriteByte('\n')
		}
	}
	if err := os.WriteFile(filepath.Join(root, stateFilename), states.Bytes(), 0o644); err != nil {
		b.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, runFilename), runs.Bytes(), 0o644); err != nil {
		b.Fatal(err)
	}
	store, err := NewJSONLStore(root)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = store.Close() })
	if !observed {
		store.batch.SetTxObserver(nil)
	}
	return store, last
}

// BenchmarkTaskStoreTransactionObserver compares one real task-store transaction
// (decode of the whole history, append, SHA-256, fsync) with and without the
// timing observer installed.
func BenchmarkTaskStoreTransactionObserver(b *testing.B) {
	for _, observed := range []bool{false, true} {
		name := "no_observer"
		if observed {
			name = "observer"
		}
		b.Run("write_"+name, func(b *testing.B) {
			store, task := newObserverBenchStore(b, observed)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				task.UpdatedAt = task.UpdatedAt.Add(time.Second)
				if err := store.SaveTask(ctx, task); err != nil {
					b.Fatal(err)
				}
			}
		})
		b.Run("read_"+name, func(b *testing.B) {
			store, task := newObserverBenchStore(b, observed)
			ctx := context.Background()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := store.GetTask(ctx, task.TaskID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkTxObserverObserve measures the observer's own work for a fast
// transaction (ring insert and threshold checks), the cost added to every tx.
func BenchmarkTxObserverObserve(b *testing.B) {
	observer := newTxObserver("task_state.jsonl", "task_run.jsonl")
	observer.logf = func(string, ...any) {}
	tx := jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true, LockWait: time.Millisecond, Exec: 20 * time.Millisecond, Commit: 5 * time.Millisecond, Files: []string{"task_state.jsonl"}}
	ctx := withTxLabel(context.Background(), "TaskTransaction", "tsk_bench")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		observer.observe(ctx, tx)
	}
}

// BenchmarkSummarizeTx measures the once-per-interval aggregation over a full ring.
func BenchmarkSummarizeTx(b *testing.B) {
	ring := newTxRing(txRingCapacity)
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < txRingCapacity; i++ {
		ring.add(txSample{At: now.Add(time.Duration(i) * time.Second), Write: i%3 != 0, Acquired: true, Appended: i%3 != 0,
			LockWait: time.Duration(i) * time.Millisecond, Exec: time.Duration(i*7) * time.Millisecond, Commit: time.Duration(i) * time.Millisecond})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sum := summarizeTx(ring.samples(), now.Add(time.Hour))
		if sum.Write.N == 0 {
			b.Fatal("empty summary")
		}
	}
}
