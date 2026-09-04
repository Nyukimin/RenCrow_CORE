package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestL1SQLiteStore_BackgroundLifecycleAuditFailureRollsBackMutation(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		eventType string
		prepare   func(*testing.T, *L1SQLiteStore, time.Time) func(*testing.T, *L1SQLiteStore)
		opts      func(time.Time) MemoryLifecycleOptions
	}{
		{
			name:      "candidate",
			eventType: "memory.candidate_review_queued",
			prepare: func(t *testing.T, store *L1SQLiteStore, now time.Time) func(*testing.T, *L1SQLiteStore) {
				t.Helper()
				memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
					UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "candidate",
					State: MemoryStateCandidate, Sensitivity: "normal", Scope: "all_personas",
				})
				if err != nil {
					t.Fatalf("create candidate: %v", err)
				}
				backdateMemory(t, store, memory.ID, now.Add(-30*24*time.Hour))
				return func(t *testing.T, store *L1SQLiteStore) {
					event, err := store.memoryByID(ctx, memory.ID)
					if err != nil {
						t.Fatalf("read candidate: %v", err)
					}
					if got := metaStringValue(event.Meta, "review_status"); got != "" {
						t.Fatalf("review_status=%q after audit failure, want empty", got)
					}
				}
			},
			opts: func(now time.Time) MemoryLifecycleOptions {
				return MemoryLifecycleOptions{Now: now, CandidateReviewAfter: 7 * 24 * time.Hour, VectorCleanupLimit: 1}
			},
		},
		{
			name:      "monthly_highlight",
			eventType: "memory.monthly_highlight_built",
			prepare: func(t *testing.T, store *L1SQLiteStore, _ time.Time) func(*testing.T, *L1SQLiteStore) {
				t.Helper()
				insertDailyDigest(t, store, "digest:atomic:2026-05-01", "2026-05-01", "ai", "day", "atomic monthly digest")
				return func(t *testing.T, store *L1SQLiteStore) {
					var count int
					if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_monthly_highlight WHERE month = ? AND category = ?`, "2026-05", "ai").Scan(&count); err != nil {
						t.Fatalf("count monthly highlights: %v", err)
					}
					if count != 0 {
						t.Fatalf("monthly highlights=%d after audit failure, want 0", count)
					}
				}
			},
			opts: func(now time.Time) MemoryLifecycleOptions {
				return MemoryLifecycleOptions{Now: now, MonthlyHighlightAfter: 7 * 24 * time.Hour, VectorCleanupLimit: 1}
			},
		},
		{
			name:      "thread_seed",
			eventType: "memory.thread_summary_monthly_seed_queued",
			prepare: func(t *testing.T, store *L1SQLiteStore, now time.Time) func(*testing.T, *L1SQLiteStore) {
				t.Helper()
				insertThreadSummary(t, store, "summary:atomic", "conv:atomic", "atomic-thread", "atomic thread summary", now.Add(-20*24*time.Hour))
				return func(t *testing.T, store *L1SQLiteStore) {
					event, err := store.memoryByID(ctx, "summary:atomic")
					if err != nil {
						t.Fatalf("read thread summary: %v", err)
					}
					if got := metaStringValue(event.Meta, "monthly_highlight_seed_status"); got != "" {
						t.Fatalf("thread seed status=%q after audit failure, want empty", got)
					}
				}
			},
			opts: func(now time.Time) MemoryLifecycleOptions {
				return MemoryLifecycleOptions{Now: now, ThreadSummarySeedAfter: 7 * 24 * time.Hour, VectorCleanupLimit: 1}
			},
		},
		{
			name:      "decay",
			eventType: "memory.decayed",
			prepare: func(t *testing.T, store *L1SQLiteStore, now time.Time) func(*testing.T, *L1SQLiteStore) {
				t.Helper()
				memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
					UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "decay",
					State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-decay"},
					Sensitivity: "normal", Scope: "all_personas", Source: "user_explicit",
				})
				if err != nil {
					t.Fatalf("create confirmed memory: %v", err)
				}
				backdateMemory(t, store, memory.ID, now.Add(-120*24*time.Hour))
				return func(t *testing.T, store *L1SQLiteStore) {
					event, err := store.memoryByID(ctx, memory.ID)
					if err != nil {
						t.Fatalf("read decay memory: %v", err)
					}
					if got := metaStringValue(event.Meta, "lifecycle_status"); got != "" {
						t.Fatalf("lifecycle_status=%q after audit failure, want empty", got)
					}
				}
			},
			opts: func(now time.Time) MemoryLifecycleOptions {
				return MemoryLifecycleOptions{Now: now, DecayAfter: 90 * 24 * time.Hour, VectorCleanupLimit: 1}
			},
		},
		{
			name:      "vector_queue",
			eventType: "memory.vector_cleanup_queued",
			prepare: func(t *testing.T, store *L1SQLiteStore, _ time.Time) func(*testing.T, *L1SQLiteStore) {
				t.Helper()
				memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
					UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "vector",
					State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-vector"},
					Sensitivity: "normal", Scope: "all_personas", Source: "user_explicit",
				})
				if err != nil {
					t.Fatalf("create vector memory: %v", err)
				}
				if _, err := store.ForgetUserMemory(ctx, memory.ID, "atomic test"); err != nil {
					t.Fatalf("forget vector memory: %v", err)
				}
				return func(t *testing.T, store *L1SQLiteStore) {
					event, err := store.memoryByID(ctx, memory.ID)
					if err != nil {
						t.Fatalf("read vector memory: %v", err)
					}
					if got := metaStringValue(event.Meta, "vector_cleanup_status"); got != "" {
						t.Fatalf("vector_cleanup_status=%q after audit failure, want empty", got)
					}
				}
			},
			opts: func(now time.Time) MemoryLifecycleOptions {
				return MemoryLifecycleOptions{Now: now, VectorCleanupLimit: 1}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
			if err != nil {
				t.Fatalf("NewL1SQLiteStore: %v", err)
			}
			defer store.Close()
			assertUnchanged := test.prepare(t, store, now)
			installLifecycleAuditAbortTrigger(t, store, test.eventType)
			if _, err := store.RunMemoryLifecycleMaintenance(ctx, test.opts(now)); err == nil {
				t.Fatalf("lifecycle succeeded despite %s audit abort", test.eventType)
			}
			assertUnchanged(t, store)
			var auditCount int
			if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type = ?`, test.eventType).Scan(&auditCount); err != nil {
				t.Fatalf("count %s audit: %v", test.eventType, err)
			}
			if auditCount != 0 {
				t.Fatalf("%s audit rows=%d after rollback, want 0", test.eventType, auditCount)
			}
		})
	}
}

func installLifecycleAuditAbortTrigger(t *testing.T, store *L1SQLiteStore, eventType string) {
	t.Helper()
	eventType = strings.ReplaceAll(eventType, "'", "''")
	if _, err := store.db.ExecContext(context.Background(), fmt.Sprintf(`
CREATE TRIGGER abort_background_lifecycle_audit
BEFORE INSERT ON l1_event_log
WHEN NEW.event_type = '%s'
BEGIN
  SELECT RAISE(ABORT, 'intentional background lifecycle audit failure');
END`, eventType)); err != nil {
		t.Fatalf("install audit abort trigger: %v", err)
	}
}

func TestL1SQLiteStore_BackgroundLifecycleDecayAuditUsesPersistedScore(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "exact decay score",
		State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-score"}, Sensitivity: "normal",
		Scope: "all_personas", Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	backdateMemory(t, store, memory.ID, now.Add(-45*24*time.Hour))
	if _, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{Now: now, DecayAfter: 30 * 24 * time.Hour, VectorCleanupLimit: 1}); err != nil {
		t.Fatalf("RunMemoryLifecycleMaintenance: %v", err)
	}
	event, err := store.memoryByID(ctx, memory.ID)
	if err != nil {
		t.Fatalf("read decayed memory: %v", err)
	}
	persisted := metaFloatValue(event.Meta, "decay_score")
	var payloadJSON string
	if err := store.db.QueryRowContext(ctx, `SELECT payload_json FROM l1_event_log WHERE event_type = ? AND namespace = ?`, "memory.decayed", event.Namespace).Scan(&payloadJSON); err != nil {
		t.Fatalf("read decay audit: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(payloadJSON), &payload); err != nil {
		t.Fatalf("decode decay audit: %v", err)
	}
	audited, ok := payload["decay_score"].(float64)
	if !ok || audited != persisted || audited == 0.5 {
		t.Fatalf("decay score metadata=%v audit=%v, want same non-0.5 score", persisted, payload["decay_score"])
	}
}

type atomicLifecycleVectorSink struct {
	mu           sync.Mutex
	err          error
	calls        int
	items        []L1VectorCleanupItem
	beforeReturn func()
}

func (s *atomicLifecycleVectorSink) CleanupMemoryVectors(_ context.Context, items []L1VectorCleanupItem) (*L1VectorCleanupResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls++
	s.items = append(s.items, items...)
	if s.beforeReturn != nil {
		s.beforeReturn()
	}
	if s.err != nil {
		return nil, s.err
	}
	return &L1VectorCleanupResult{Deleted: len(items)}, nil
}

func TestL1SQLiteStore_BackgroundLifecycleRequeuesStaleRunningVectorCleanup(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "stale running vector cleanup",
		State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-vector-stale"}, Sensitivity: "normal",
		Scope: "all_personas", Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, memory.ID, "stale running test"); err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	event, err := store.memoryByID(ctx, memory.ID)
	if err != nil {
		t.Fatalf("read memory: %v", err)
	}
	meta := cloneMeta(event.Meta)
	meta["vector_cleanup_status"] = "running"
	meta["vector_cleanup_started_at"] = now.Add(-time.Hour).Format(time.RFC3339Nano)
	meta["vector_cleanup_claim_id"] = "crashed-claim"
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal stale metadata: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ?, updated_at = ? WHERE id = ?`, string(metaJSON), now.Add(-time.Hour), memory.ID); err != nil {
		t.Fatalf("install stale running state: %v", err)
	}
	sink := &atomicLifecycleVectorSink{}
	store.WithVectorCleanupSink(sink)
	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{Now: now, VectorCleanupLimit: 1})
	if err != nil {
		t.Fatalf("replay stale running cleanup: %v", err)
	}
	if result.VectorCleanupQueued != 1 || result.VectorCleanupExecuted != 1 {
		t.Fatalf("stale running result=%+v, want queued/executed 1/1", result)
	}
	event, err = store.memoryByID(ctx, memory.ID)
	if err != nil {
		t.Fatalf("read completed memory: %v", err)
	}
	if got := metaStringValue(event.Meta, "vector_cleanup_status"); got != "done" {
		t.Fatalf("stale running final status=%q, want done", got)
	}
}

func TestL1SQLiteStore_BackgroundLifecycleSkipsStaleVectorCompletionAfterOwnerDrift(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "owner drift vector cleanup",
		State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-vector-drift"}, Sensitivity: "normal",
		Scope: "all_personas", Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, memory.ID, "owner drift test"); err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	sink := &atomicLifecycleVectorSink{}
	sink.beforeReturn = func() {
		event, err := store.memoryByID(ctx, memory.ID)
		if err != nil {
			t.Fatalf("read claimed memory in sink: %v", err)
		}
		meta := cloneMeta(event.Meta)
		meta["owner_note"] = "changed concurrently"
		metaJSON, err := json.Marshal(meta)
		if err != nil {
			t.Fatalf("marshal owner drift metadata: %v", err)
		}
		if _, err := store.db.ExecContext(ctx, `UPDATE l1_memory_event SET meta_json = ?, updated_at = ? WHERE id = ?`, string(metaJSON), now.Add(time.Minute), memory.ID); err != nil {
			t.Fatalf("apply owner drift: %v", err)
		}
	}
	store.WithVectorCleanupSink(sink)
	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{Now: now, VectorCleanupLimit: 1})
	if err != nil {
		t.Fatalf("owner drift cleanup: %v", err)
	}
	if result.VectorCleanupExecuted != 0 {
		t.Fatalf("owner drift cleanup executed=%d, want 0", result.VectorCleanupExecuted)
	}
	event, err := store.memoryByID(ctx, memory.ID)
	if err != nil {
		t.Fatalf("read drifted memory: %v", err)
	}
	if got := metaStringValue(event.Meta, "owner_note"); got != "changed concurrently" {
		t.Fatalf("owner note=%q, want concurrent value", got)
	}
	if got := metaStringValue(event.Meta, "vector_cleanup_status"); got != "running" {
		t.Fatalf("drifted cleanup status=%q, want running for replay", got)
	}
}

func TestL1SQLiteStore_BackgroundLifecycleVectorCleanupFailureIsDurableAndRetryable(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "retry vector cleanup",
		State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-vector-retry"}, Sensitivity: "normal",
		Scope: "all_personas", Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, memory.ID, "retry test"); err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	sinkErr := errors.New("vector backend unavailable")
	sink := &atomicLifecycleVectorSink{err: sinkErr}
	store.WithVectorCleanupSink(sink)
	if _, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{Now: now, VectorCleanupLimit: 1}); !errors.Is(err, sinkErr) {
		t.Fatalf("first run error=%v, want sink error", err)
	}
	event, err := store.memoryByID(ctx, memory.ID)
	if err != nil {
		t.Fatalf("read failed cleanup memory: %v", err)
	}
	if got := metaStringValue(event.Meta, "vector_cleanup_status"); got != "error" {
		t.Fatalf("failed cleanup status=%q, want error", got)
	}
	for _, eventType := range []string{"memory.vector_cleanup_started", "memory.vector_cleanup_failed"} {
		var count int
		if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type = ? AND namespace = ?`, eventType, event.Namespace).Scan(&count); err != nil {
			t.Fatalf("count %s audit: %v", eventType, err)
		}
		if count != 1 {
			t.Fatalf("%s audit count=%d, want 1", eventType, count)
		}
	}
	sink.mu.Lock()
	sink.err = nil
	sink.mu.Unlock()
	result, err := store.RunMemoryLifecycleMaintenance(ctx, MemoryLifecycleOptions{Now: now.Add(time.Minute), VectorCleanupLimit: 1})
	if err != nil {
		t.Fatalf("retry run: %v", err)
	}
	if result.VectorCleanupQueued != 1 || result.VectorCleanupExecuted != 1 {
		t.Fatalf("retry result=%+v, want queued/executed 1/1", result)
	}
	event, err = store.memoryByID(ctx, memory.ID)
	if err != nil {
		t.Fatalf("read completed cleanup memory: %v", err)
	}
	if got := metaStringValue(event.Meta, "vector_cleanup_status"); got != "done" {
		t.Fatalf("completed cleanup status=%q, want done", got)
	}
	var completedCount int
	if err := store.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_event_log WHERE event_type = ? AND namespace = ?`, "memory.vector_cleanup_completed", event.Namespace).Scan(&completedCount); err != nil {
		t.Fatalf("count completed audit: %v", err)
	}
	if completedCount != 1 {
		t.Fatalf("completed audit count=%d, want 1", completedCount)
	}
}

type blockingLifecycleVectorSink struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
	calls   atomic.Int32
}

func (s *blockingLifecycleVectorSink) CleanupMemoryVectors(_ context.Context, items []L1VectorCleanupItem) (*L1VectorCleanupResult, error) {
	s.calls.Add(1)
	s.once.Do(func() { close(s.started) })
	<-s.release
	return &L1VectorCleanupResult{Deleted: len(items)}, nil
}

func TestL1SQLiteStore_BackgroundLifecycleConcurrentRunsDoNotDoubleCallVectorSink(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	memory, err := store.CreateUserMemory(ctx, domainmemory.CreateUserMemoryInput{
		UserID: "ren", Type: domainmemory.UserMemoryTypePreference, Statement: "single vector sink call",
		State: MemoryStateConfirmed, EvidenceEventIDs: []string{"e-vector-concurrent"}, Sensitivity: "normal",
		Scope: "all_personas", Source: "user_explicit",
	})
	if err != nil {
		t.Fatalf("create memory: %v", err)
	}
	if _, err := store.ForgetUserMemory(ctx, memory.ID, "concurrent test"); err != nil {
		t.Fatalf("forget memory: %v", err)
	}
	sink := &blockingLifecycleVectorSink{started: make(chan struct{}), release: make(chan struct{})}
	store.WithVectorCleanupSink(sink)
	opts := MemoryLifecycleOptions{Now: now, VectorCleanupLimit: 1}
	type outcome struct {
		result *MemoryLifecycleResult
		err    error
	}
	first := make(chan outcome, 1)
	go func() {
		result, err := store.RunMemoryLifecycleMaintenance(ctx, opts)
		first <- outcome{result: result, err: err}
	}()
	select {
	case <-sink.started:
	case <-time.After(5 * time.Second):
		t.Fatal("vector sink was not called")
	}
	second := make(chan outcome, 1)
	go func() {
		result, err := store.RunMemoryLifecycleMaintenance(ctx, opts)
		second <- outcome{result: result, err: err}
	}()
	close(sink.release)
	select {
	case result := <-first:
		if result.err != nil {
			t.Fatalf("first run error: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first lifecycle run did not finish")
	}
	select {
	case result := <-second:
		if result.err != nil {
			t.Fatalf("second run error: %v", result.err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second lifecycle run did not finish")
	}
	if calls := sink.calls.Load(); calls != 1 {
		t.Fatalf("vector sink calls=%d, want 1", calls)
	}
}
