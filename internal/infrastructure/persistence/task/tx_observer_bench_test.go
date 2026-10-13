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
