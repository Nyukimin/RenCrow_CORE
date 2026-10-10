package jsonlbatch

import (
	"context"
	"testing"
)

// BenchmarkWriteObserver compares a minimal Write (one small append: WAL prepare,
// data fsync, WAL commit) with no observer, a no-op observer, and a cleared one.
func BenchmarkWriteObserver(b *testing.B) {
	payload := map[string][]byte{"state.jsonl": []byte(`{"id":"task-1","state":"running"}` + "\n")}
	callback := func() (map[string][]byte, error) { return payload, nil }
	for _, mode := range []string{"no_observer", "observer", "observer_then_cleared"} {
		b.Run(mode, func(b *testing.B) {
			store, err := New(b.TempDir(), []string{"state.jsonl"})
			if err != nil {
				b.Fatal(err)
			}
			switch mode {
			case "observer":
				store.SetTxObserver(func(context.Context, TxObservation) {})
			case "observer_then_cleared":
				store.SetTxObserver(func(context.Context, TxObservation) {})
				store.SetTxObserver(nil)
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.Write(ctx, callback); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkReadObserver is the same comparison for Read, whose callback does
// nothing, so any observer overhead is visible against the lock and journal check.
func BenchmarkReadObserver(b *testing.B) {
	for _, mode := range []string{"no_observer", "observer"} {
		b.Run(mode, func(b *testing.B) {
			store, err := New(b.TempDir(), []string{"state.jsonl"})
			if err != nil {
				b.Fatal(err)
			}
			if mode == "observer" {
				store.SetTxObserver(func(context.Context, TxObservation) {})
			}
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if err := store.Read(ctx, func() error { return nil }); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
