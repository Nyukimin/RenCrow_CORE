package superagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type actorPlannedRunQueuePersistence interface {
	ListRunQueueCandidatesForTasks(context.Context, []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error)
	ClaimRunQueueItem(context.Context, modulecore.QueueItemID, time.Time, time.Time, string) (*domainsuperagent.RunQueueItem, error)
	ReleaseRunQueueItem(context.Context, modulecore.QueueItemID, string) (bool, error)
}

func TestActorPlannedRunQueuePersistenceFiltersByTasksAndClaimsExactItem(t *testing.T) {
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	taskA, taskB := modulecore.NewTaskID(), modulecore.NewTaskID()
	queueA, queueB, queueOther, queueReserved, queueExpired := modulecore.NewQueueItemID(), modulecore.NewQueueItemID(), modulecore.NewQueueItemID(), modulecore.NewQueueItemID(), modulecore.NewQueueItemID()

	tests := []struct {
		name string
		new  func(*testing.T) (interface {
			SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
		}, func())
	}{
		{
			name: "sqlite",
			new: func(t *testing.T) (interface {
				SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
			}, func()) {
				store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.sqlite"), 0)
				if err != nil {
					t.Fatalf("NewSQLiteStore() error = %v", err)
				}
				return store, func() { _ = store.Close() }
			},
		},
		{
			name: "jsonl",
			new: func(t *testing.T) (interface {
				SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
			}, func()) {
				return NewJSONLStore(filepath.Join(t.TempDir(), "superagent"), 0), func() {}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, closeStore := test.new(t)
			defer closeStore()
			planned, ok := any(store).(actorPlannedRunQueuePersistence)
			if !ok {
				t.Fatal("run queue store does not expose actor candidate reads and exact lease reservation")
			}
			items := []domainsuperagent.RunQueueItem{
				actorQueueItem(queueA, taskA, "queued", now),
				actorQueueItem(queueB, taskA, "queued", now.Add(time.Second)),
				actorQueueItem(queueOther, taskB, "queued", now.Add(2*time.Second)),
				actorQueueItem(queueReserved, taskA, "reserved", now.Add(3*time.Second)),
			}
			items[3].ClaimedAt = now
			items[3].LeaseToken = "active-owner"
			items[3].AttemptCount = 1
			items[3].LeaseUntil = now.Add(time.Minute)
			expired := actorQueueItem(queueExpired, taskA, "reserved", now.Add(4*time.Second))
			expired.AttemptCount = 4
			expired.ClaimedAt = now.Add(-2 * time.Minute)
			expired.LeaseToken = "expired-owner"
			expired.LeaseUntil = now.Add(-time.Second)
			items = append(items, expired)
			for _, item := range items {
				if err := store.SaveRunQueueItem(context.Background(), item); err != nil {
					t.Fatalf("SaveRunQueueItem(%s) error = %v", item.QueueItemID, err)
				}
			}

			candidates, err := planned.ListRunQueueCandidatesForTasks(context.Background(), []modulecore.TaskID{taskA})
			if err != nil {
				t.Fatalf("ListRunQueueCandidatesForTasks() error = %v", err)
			}
			got := make(map[modulecore.QueueItemID]bool, len(candidates))
			for _, item := range candidates {
				got[item.QueueItemID] = true
			}
			if len(got) != 4 || !got[queueA] || !got[queueB] || !got[queueReserved] || !got[queueExpired] {
				t.Fatalf("actor task candidates = %#v, want queued and reserved items for task %s", candidates, taskA)
			}
			if active, err := planned.ClaimRunQueueItem(context.Background(), queueReserved, now, now.Add(time.Minute), "other-owner"); err != nil || active != nil {
				t.Fatalf("active reservation claim = %#v, %v; want no claim", active, err)
			}
			recovered, err := planned.ClaimRunQueueItem(context.Background(), queueExpired, now, now.Add(time.Minute), "recovery-owner")
			if err != nil {
				t.Fatalf("ClaimRunQueueItem(expired reservation) error = %v", err)
			}
			if recovered == nil || recovered.Status != "reserved" || recovered.AttemptCount != 1 || recovered.LeaseToken != "recovery-owner" {
				t.Fatalf("expired reservation recovery = %#v; want a fresh pre-Run lease", recovered)
			}
			if released, err := planned.ReleaseRunQueueItem(context.Background(), queueExpired, "recovery-owner"); err != nil || !released {
				t.Fatalf("expired reservation release = %v, %v; want true", released, err)
			}

			claimed, err := planned.ClaimRunQueueItem(context.Background(), queueB, now, now.Add(time.Minute), "planned-owner")
			if err != nil {
				t.Fatalf("ClaimRunQueueItem(exact) error = %v", err)
			}
			if claimed == nil || claimed.QueueItemID != queueB || claimed.Status != "reserved" || claimed.RunID != "" || claimed.AttemptCount != 1 {
				t.Fatalf("exact claim = %#v", claimed)
			}
			if duplicate, err := planned.ClaimRunQueueItem(context.Background(), queueB, now, now.Add(time.Minute), "other-owner"); err != nil || duplicate != nil {
				t.Fatalf("duplicate exact claim = %#v, %v; want no claim", duplicate, err)
			}
			if released, err := planned.ReleaseRunQueueItem(context.Background(), queueB, "wrong-owner"); err != nil || released {
				t.Fatalf("wrong-token release = %v, %v; want false", released, err)
			}
			if released, err := planned.ReleaseRunQueueItem(context.Background(), queueB, "planned-owner"); err != nil || !released {
				t.Fatalf("exact release = %v, %v; want true", released, err)
			}
			after, err := planned.ListRunQueueCandidatesForTasks(context.Background(), []modulecore.TaskID{taskA})
			if err != nil {
				t.Fatalf("ListRunQueueCandidatesForTasks() after release error = %v", err)
			}
			if len(after) != 4 {
				t.Fatalf("candidates after release = %#v, want queued and reserved items", after)
			}
			for _, item := range after {
				if item.QueueItemID == queueB && (item.AttemptCount != 0 || item.LeaseToken != "" || !item.LeaseUntil.IsZero() || !item.ClaimedAt.IsZero()) {
					t.Fatalf("released queue item retained lease state: %#v", item)
				}
			}
		})
	}
}

func actorQueueItem(queueID modulecore.QueueItemID, taskID modulecore.TaskID, status string, createdAt time.Time) domainsuperagent.RunQueueItem {
	return domainsuperagent.RunQueueItem{
		QueueItemID: queueID, TaskID: taskID, RunStartReason: domaintask.RunStartReasonFirst,
		Goal: "queue task", Action: "run", Status: status, CreatedAt: createdAt,
	}
}

func TestActorPlannedRunQueuePersistenceSerializesCompetingExactClaims(t *testing.T) {
	now := time.Date(2026, 11, 12, 13, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		new  func(*testing.T) (interface {
			SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
		}, func())
	}{
		{
			name: "sqlite",
			new: func(t *testing.T) (interface {
				SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
			}, func()) {
				store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "superagent.sqlite"), 0)
				if err != nil {
					t.Fatalf("NewSQLiteStore() error = %v", err)
				}
				return store, func() { _ = store.Close() }
			},
		},
		{
			name: "jsonl",
			new: func(t *testing.T) (interface {
				SaveRunQueueItem(context.Context, domainsuperagent.RunQueueItem) error
			}, func()) {
				return NewJSONLStore(filepath.Join(t.TempDir(), "superagent"), 0), func() {}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			base, closeStore := test.new(t)
			defer closeStore()
			store, ok := any(base).(actorPlannedRunQueuePersistence)
			if !ok {
				t.Fatal("run queue store does not expose exact lease reservation")
			}
			item := actorQueueItem(modulecore.NewQueueItemID(), modulecore.NewTaskID(), "queued", now)
			if err := base.SaveRunQueueItem(context.Background(), item); err != nil {
				t.Fatalf("SaveRunQueueItem() error = %v", err)
			}

			type result struct {
				item  *domainsuperagent.RunQueueItem
				token string
				err   error
			}
			start := make(chan struct{})
			results := make(chan result, 2)
			for _, token := range []string{"owner-a", "owner-b"} {
				go func(token string) {
					<-start
					claimed, err := store.ClaimRunQueueItem(context.Background(), item.QueueItemID, now, now.Add(time.Minute), token)
					results <- result{item: claimed, token: token, err: err}
				}(token)
			}
			close(start)
			successes := 0
			for range 2 {
				got := <-results
				if got.err != nil {
					t.Fatalf("ClaimRunQueueItem(%s) error = %v", got.token, got.err)
				}
				if got.item != nil {
					successes++
					if got.item.LeaseToken != got.token {
						t.Fatalf("claim token = %s, want %s", got.item.LeaseToken, got.token)
					}
				}
			}
			if successes != 1 {
				t.Fatalf("successful exact claims = %d, want exactly 1", successes)
			}
		})
	}
}

func TestSQLiteActorQueueIndexBackfillsLegacyQueueRows(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := db.Exec("CREATE TABLE run_queue (queue_item_id TEXT PRIMARY KEY, status TEXT, created_at TEXT, payload TEXT NOT NULL)"); err != nil {
		t.Fatalf("create legacy run_queue schema: %v", err)
	}
	item := actorQueueItem(modulecore.NewQueueItemID(), modulecore.NewTaskID(), "queued", time.Now().UTC())
	payload, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if _, err := db.Exec("INSERT INTO run_queue (queue_item_id, status, created_at, payload) VALUES (?, ?, ?, ?)", string(item.QueueItemID), item.Status, item.CreatedAt.Format(time.RFC3339Nano), string(payload)); err != nil {
		t.Fatalf("insert legacy run_queue row: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	store, err := NewSQLiteStore(path, 0)
	if err != nil {
		t.Fatalf("NewSQLiteStore() migration error = %v", err)
	}
	defer store.Close()
	items, err := store.ListRunQueueCandidatesForTasks(context.Background(), []modulecore.TaskID{item.TaskID})
	if err != nil {
		t.Fatalf("ListRunQueueCandidatesForTasks() error = %v", err)
	}
	if len(items) != 1 || items[0].QueueItemID != item.QueueItemID {
		t.Fatalf("migrated candidates = %#v, want legacy queue item %s", items, item.QueueItemID)
	}
}
