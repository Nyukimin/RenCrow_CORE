package txobserve

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/jsonlbatch"
)

// BenchmarkObserverObserve measures the observer's own work for a fast
// transaction (ring insert and threshold checks), the cost added to every tx.
func BenchmarkObserverObserve(b *testing.B) {
	observer := New(Options{Prefix: testPrefix, Logf: func(string, ...any) {}})
	tx := jsonlbatch.TxObservation{Kind: jsonlbatch.TxKindWrite, Acquired: true, LockWait: time.Millisecond, Exec: 20 * time.Millisecond, Commit: 5 * time.Millisecond, Files: []string{"task_state.jsonl"}}
	ctx := WithLabel(context.Background(), Label{Op: "TaskTransaction", ScopeKey: "task_id", ScopeValue: "tsk_bench"})
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		observer.Observe(ctx, tx)
	}
}

// BenchmarkSummarize measures the once-per-interval aggregation over a full ring.
func BenchmarkSummarize(b *testing.B) {
	r := newRing(ringCapacity)
	now := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	for i := 0; i < ringCapacity; i++ {
		r.add(sample{At: now.Add(time.Duration(i) * time.Second), Write: i%3 != 0, Acquired: true, Appended: i%3 != 0,
			LockWait: time.Duration(i) * time.Millisecond, Exec: time.Duration(i*7) * time.Millisecond, Commit: time.Duration(i) * time.Millisecond})
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sum := summarize(r.samples(), now.Add(time.Hour))
		if sum.Write.N == 0 {
			b.Fatal("empty summary")
		}
	}
}
