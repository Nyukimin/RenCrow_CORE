package superagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/persistencequeue"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db               *sql.DB
	maxContextTokens int
	writeQueue       *persistencequeue.Queue[persistencequeue.PersistenceCommand]
}

func NewSQLiteStore(path string, maxContextTokens int) (*SQLiteStore, error) {
	return newSQLiteStore(path, maxContextTokens, nil)
}

// NewSQLiteStoreWithWriteQueue configures the store to enqueue writes admitted
// by the same process-local FIFO used by the rest of the runtime.
func NewSQLiteStoreWithWriteQueue(path string, maxContextTokens int, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) (*SQLiteStore, error) {
	if queue == nil {
		return nil, persistencequeue.ErrNilPersistenceQueue
	}
	return newSQLiteStore(path, maxContextTokens, queue)
}

func newSQLiteStore(path string, maxContextTokens int, queue *persistencequeue.Queue[persistencequeue.PersistenceCommand]) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/superagent_harness.sqlite"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	store := &SQLiteStore{db: db, maxContextTokens: maxContextTokens, writeQueue: queue}
	if err := store.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// contextPackTableDDL is the canonical context_pack schema. Legacy DBs created
// before the canonical save path declared run_id NOT NULL while saves only
// write artifact_id, created_at and payload, so migrate() rebuilds them.
const contextPackTableDDL = `CREATE TABLE IF NOT EXISTS context_pack (
			artifact_id TEXT PRIMARY KEY,
			run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`

func (s *SQLiteStore) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS agent_run (
			run_id TEXT PRIMARY KEY,
			started_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS subagent_task (
			task_id TEXT PRIMARY KEY,
			run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		contextPackTableDDL,
		`CREATE TABLE IF NOT EXISTS message_channel (
			channel_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS run_queue (
			queue_item_id TEXT PRIMARY KEY,
			task_id TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	// Best-effort rename for local DBs created before queue_item_id was canonical.
	_, _ = s.db.Exec(`ALTER TABLE run_queue RENAME COLUMN queue_id TO queue_item_id`)
	if err := s.migrateContextPackRunID(); err != nil {
		return err
	}
	return s.migrateRunQueueTaskIndex()
}

// migrateContextPackRunID rebuilds context_pack tables that still declare
// run_id NOT NULL. The canonical save path writes only artifact_id, created_at
// and payload, so the legacy constraint fails every context pack insert.
func (s *SQLiteStore) migrateContextPackRunID() error {
	var notNull int
	if err := s.db.QueryRow(`SELECT count(*) FROM pragma_table_info('context_pack') WHERE name='run_id' AND "notnull"=1`).Scan(&notNull); err != nil {
		return err
	}
	if notNull == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, stmt := range []string{
		`DROP TABLE IF EXISTS context_pack_legacy_notnull`,
		`ALTER TABLE context_pack RENAME TO context_pack_legacy_notnull`,
		strings.Replace(contextPackTableDDL, "IF NOT EXISTS ", "", 1),
		`INSERT INTO context_pack (artifact_id, run_id, created_at, payload) SELECT artifact_id, run_id, created_at, payload FROM context_pack_legacy_notnull`,
		`DROP TABLE context_pack_legacy_notnull`,
	} {
		if _, err := tx.Exec(stmt); err != nil {
			return fmt.Errorf("migrate legacy context_pack schema: %w", err)
		}
	}
	return tx.Commit()
}

func (s *SQLiteStore) SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error {
	if err := domainsuperagent.ValidateAgentRun(item); err != nil {
		return err
	}
	return s.saveAdmitted(ctx, "agent_run", "run_id", string(item.RunID), "started_at", item.StartedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAgentRuns(ctx context.Context, limit int) ([]domainsuperagent.AgentRun, error) {
	return listSQLiteItems[domainsuperagent.AgentRun](ctx, s, "agent_run", limit)
}

// FindAgentRunByID returns the record stored under the exact agent_run primary key.
func (s *SQLiteStore) FindAgentRunByID(ctx context.Context, runID string) (domainsuperagent.AgentRun, bool, error) {
	if s == nil || s.db == nil {
		return domainsuperagent.AgentRun{}, false, fmt.Errorf("superagent sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM agent_run WHERE run_id = ?`, runID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domainsuperagent.AgentRun{}, false, nil
	}
	if err != nil {
		return domainsuperagent.AgentRun{}, false, err
	}
	var item domainsuperagent.AgentRun
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainsuperagent.AgentRun{}, false, err
	}
	if err := domainsuperagent.ValidateAgentRun(item); err != nil {
		return domainsuperagent.AgentRun{}, false, err
	}
	if string(item.RunID) != runID {
		return domainsuperagent.AgentRun{}, false, fmt.Errorf("stored agent run ID %q does not match primary key %q", item.RunID, runID)
	}
	return item, true, nil
}

func (s *SQLiteStore) SaveSubagentTask(ctx context.Context, item domainsuperagent.SubagentTask) error {
	if err := domainsuperagent.ValidateSubagentTask(item); err != nil {
		return err
	}
	return s.save(ctx, "subagent_task", "task_id", string(item.TaskID), "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSubagentTasks(ctx context.Context, limit int) ([]domainsuperagent.SubagentTask, error) {
	return listSQLiteItems[domainsuperagent.SubagentTask](ctx, s, "subagent_task", limit)
}

func (s *SQLiteStore) SaveContextPack(ctx context.Context, item domainsuperagent.ContextPack) error {
	if err := domainsuperagent.ValidateContextPack(item, s.maxContextTokens); err != nil {
		return err
	}
	return s.saveAdmitted(ctx, "context_pack", "artifact_id", string(item.ArtifactID), "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListContextPacks(ctx context.Context, limit int) ([]domainsuperagent.ContextPack, error) {
	return listSQLiteItems[domainsuperagent.ContextPack](ctx, s, "context_pack", limit)
}

func (s *SQLiteStore) SaveMessageChannel(ctx context.Context, item domainsuperagent.MessageChannel) error {
	if err := domainsuperagent.ValidateMessageChannel(item); err != nil {
		return err
	}
	return s.save(ctx, "message_channel", "channel_id", item.ChannelID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListMessageChannels(ctx context.Context, limit int) ([]domainsuperagent.MessageChannel, error) {
	return listSQLiteItems[domainsuperagent.MessageChannel](ctx, s, "message_channel", limit)
}

func (s *SQLiteStore) SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error {
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return err
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO run_queue (queue_item_id, task_id, status, created_at, payload) VALUES (?, ?, ?, ?, ?)`, string(item.QueueItemID), string(item.TaskID), item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), string(payload))
	return err
}

// FindRunQueueItemByTaskIntent returns the first durable receipt for one
// canonical Task and idempotency key, including its latest saved lifecycle
// state. It deliberately does not use a recent-items limit: a retry after
// claim or completion must still resolve to the original receipt.
func (s *SQLiteStore) FindRunQueueItemByTaskIntent(ctx context.Context, taskID modulecore.TaskID, intent string) (domainsuperagent.RunQueueItem, bool, error) {
	if s == nil || s.db == nil {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("superagent sqlite store is closed")
	}
	if err := taskID.Validate(); err != nil {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("task_id is invalid: %w", err)
	}
	if strings.TrimSpace(intent) == "" || strings.TrimSpace(intent) != intent {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("run queue intent is required")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM run_queue WHERE task_id=? AND json_extract(payload, '$.idempotency_key')=? ORDER BY rowid ASC LIMIT 1`, string(taskID), intent).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domainsuperagent.RunQueueItem{}, false, nil
	}
	if err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	var item domainsuperagent.RunQueueItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	return item, true, nil
}

// InsertRunQueueItemIfAbsent atomically inserts one first-run intent for a
// canonical Task. A retry returns the existing receipt without replacing its
// QueueItemID, status, lease, RunID, or history.
func (s *SQLiteStore) InsertRunQueueItemIfAbsent(ctx context.Context, item domainsuperagent.RunQueueItem) (domainsuperagent.RunQueueItem, bool, error) {
	if s == nil || s.db == nil {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("superagent sqlite store is closed")
	}
	if strings.TrimSpace(item.IdempotencyKey) == "" || strings.TrimSpace(item.IdempotencyKey) != item.IdempotencyKey {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("run queue intent is required")
	}
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	result, err := s.db.ExecContext(ctx, `INSERT INTO run_queue (queue_item_id, task_id, status, created_at, payload)
		SELECT ?, ?, ?, ?, ? WHERE NOT EXISTS (
			SELECT 1 FROM run_queue WHERE task_id=? AND json_extract(payload, '$.idempotency_key')=?
		)`, string(item.QueueItemID), string(item.TaskID), item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), string(payload), string(item.TaskID), item.IdempotencyKey)
	if err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	insertedRows, err := result.RowsAffected()
	if err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	if insertedRows == 1 {
		return item, true, nil
	}
	existing, found, err := s.FindRunQueueItemByTaskIntent(ctx, item.TaskID, item.IdempotencyKey)
	if err != nil {
		return domainsuperagent.RunQueueItem{}, false, err
	}
	if !found {
		return domainsuperagent.RunQueueItem{}, false, fmt.Errorf("run queue intent insert was skipped without an existing receipt")
	}
	return existing, false, nil
}

func (s *SQLiteStore) ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	return listSQLiteItems[domainsuperagent.RunQueueItem](ctx, s, "run_queue", limit)
}

// GetRunQueueItem reads the exact durable queue receipt named by a Task cursor.
func (s *SQLiteStore) GetRunQueueItem(ctx context.Context, queueID modulecore.QueueItemID) (domainsuperagent.RunQueueItem, bool, error) {
	items, err := s.GetRunQueueItems(ctx, []modulecore.QueueItemID{queueID})
	item, found := items[queueID]
	return item, found, err
}

// GetRunQueueItems resolves a bounded set of exact receipts through their
// primary keys, independent of queue history size.
func (s *SQLiteStore) GetRunQueueItems(ctx context.Context, queueIDs []modulecore.QueueItemID) (map[modulecore.QueueItemID]domainsuperagent.RunQueueItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("superagent sqlite store is closed")
	}
	items := make(map[modulecore.QueueItemID]domainsuperagent.RunQueueItem, len(queueIDs))
	unique := make([]modulecore.QueueItemID, 0, len(queueIDs))
	seen := make(map[modulecore.QueueItemID]struct{}, len(queueIDs))
	for _, queueID := range queueIDs {
		if err := queueID.Validate(); err != nil {
			return nil, fmt.Errorf("queue_item_id is invalid: %w", err)
		}
		if _, duplicate := seen[queueID]; duplicate {
			continue
		}
		seen[queueID] = struct{}{}
		unique = append(unique, queueID)
	}
	const maxQueueIDsPerQuery = 500
	for start := 0; start < len(unique); start += maxQueueIDsPerQuery {
		end := min(start+maxQueueIDsPerQuery, len(unique))
		chunk := unique[start:end]
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, len(chunk))
		for _, queueID := range chunk {
			args = append(args, string(queueID))
		}
		rows, err := s.db.QueryContext(ctx, `SELECT queue_item_id, payload FROM run_queue WHERE queue_item_id IN (`+placeholders+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var storedID, payload string
			if err := rows.Scan(&storedID, &payload); err != nil {
				rows.Close()
				return nil, err
			}
			var item domainsuperagent.RunQueueItem
			if err := json.Unmarshal([]byte(payload), &item); err != nil {
				rows.Close()
				return nil, err
			}
			if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
				rows.Close()
				return nil, err
			}
			if string(item.QueueItemID) != storedID {
				rows.Close()
				return nil, fmt.Errorf("stored queue_item_id %q does not match primary key %q", item.QueueItemID, storedID)
			}
			items[item.QueueItemID] = item
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// ListActiveRunQueueItems returns only unfinished reserved/claimed receipts so
// startup repair does not scan the lifetime queue history through this API.
func (s *SQLiteStore) ListActiveRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("superagent sqlite store is closed")
	}
	if limit <= 0 {
		return nil, fmt.Errorf("active run queue limit must be positive")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT status, payload FROM run_queue WHERE status IN ('reserved','claimed') ORDER BY created_at ASC, queue_item_id ASC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domainsuperagent.RunQueueItem, 0, limit)
	for rows.Next() {
		var status, payload string
		if err := rows.Scan(&status, &payload); err != nil {
			return nil, err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
			return nil, err
		}
		if item.Status != status {
			return nil, fmt.Errorf("run queue status column %q does not match payload status %q", status, item.Status)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func (s *SQLiteStore) ClaimNextRunQueueItem(ctx context.Context, now, leaseUntil time.Time, leaseToken string) (*domainsuperagent.RunQueueItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("superagent sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT payload FROM run_queue ORDER BY rowid ASC`)
	if err != nil {
		return nil, err
	}
	type candidate struct {
		item    domainsuperagent.RunQueueItem
		payload string
	}
	var selected *candidate
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			rows.Close()
			return nil, err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			rows.Close()
			return nil, err
		}
		if !runQueueItemClaimable(item, now) {
			continue
		}
		if selected == nil || runQueueItemBefore(item, selected.item) {
			selected = &candidate{item: item, payload: payload}
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if selected == nil {
		return nil, tx.Commit()
	}
	item := selected.item
	// Storage owns only the lease reservation. The canonical Task owner issues
	// the execution Run afterwards, so a claim must never carry forward or
	// invent a RunID.
	item.Status, item.ClaimedAt, item.LeaseToken, item.LeaseUntil = "reserved", now, leaseToken, leaseUntil
	item.RunID = ""
	item.AttemptCount++
	item.CompletedAt = time.Time{}
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status='reserved', payload=? WHERE queue_item_id=? AND payload=?`, string(encoded), string(item.QueueItemID), selected.payload)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, fmt.Errorf("run queue claim conflict")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

// AttachRunQueueRun atomically binds a canonical Task-owned Run to the current
// reserved lease. A stale or already-attached token returns false without a
// write.
func (s *SQLiteStore) AttachRunQueueRun(ctx context.Context, queueItemID, leaseToken string, canonicalRunID modulecore.RunID) (bool, error) {
	if err := canonicalRunID.Validate(); err != nil {
		return false, fmt.Errorf("canonical run_id is invalid: %w", err)
	}
	if strings.TrimSpace(leaseToken) == "" {
		return false, nil
	}
	if s == nil || s.db == nil {
		return false, fmt.Errorf("superagent sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM run_queue WHERE queue_item_id=?`, queueItemID).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	} else if err != nil {
		return false, err
	}
	var item domainsuperagent.RunQueueItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return false, err
	}
	if item.Status != "reserved" || item.RunID != "" || item.LeaseToken != leaseToken {
		return false, tx.Commit()
	}
	item.Status = "claimed"
	item.RunID = canonicalRunID
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return false, err
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status='claimed', payload=? WHERE queue_item_id=? AND payload=?`, string(encoded), queueItemID, payload)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func (s *SQLiteStore) RenewRunQueueLease(ctx context.Context, queueItemID, leaseToken string, leaseUntil time.Time) (bool, error) {
	return s.updateRunQueueLease(ctx, queueItemID, leaseToken, false, false, func(item *domainsuperagent.RunQueueItem) {
		item.LeaseUntil = leaseUntil
	})
}

func (s *SQLiteStore) CompleteRunQueueItem(ctx context.Context, queueItemID, leaseToken, status, reason string, completedAt time.Time) (bool, error) {
	return s.updateRunQueueLease(ctx, queueItemID, leaseToken, true, status == "blocked", func(item *domainsuperagent.RunQueueItem) {
		item.Status, item.Reason, item.CompletedAt = status, reason, completedAt
		item.LeaseToken, item.LeaseUntil = "", time.Time{}
	})
}

func (s *SQLiteStore) updateRunQueueLease(ctx context.Context, queueItemID, leaseToken string, claimedOnly, allowReservedBlock bool, mutate func(*domainsuperagent.RunQueueItem)) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("superagent sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM run_queue WHERE queue_item_id=?`, queueItemID).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	} else if err != nil {
		return false, err
	}
	var item domainsuperagent.RunQueueItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return false, err
	}
	if (claimedOnly && item.Status != "claimed" && !(allowReservedBlock && item.Status == "reserved")) || (!claimedOnly && item.Status != "reserved" && item.Status != "claimed") || item.LeaseToken != leaseToken {
		return false, tx.Commit()
	}
	mutate(&item)
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return false, err
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status=?, payload=? WHERE queue_item_id=? AND payload=?`, item.Status, string(encoded), queueItemID, payload)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return changed == 1, nil
}

func runQueueItemClaimable(item domainsuperagent.RunQueueItem, now time.Time) bool {
	if item.IdempotencyKey == domainsuperagent.RunQueueIntentPrivateFirstChat {
		return false
	}
	if !item.NotBefore.IsZero() && item.NotBefore.After(now) {
		return false
	}
	return item.Status == "queued" || (item.Status == "reserved" && !item.LeaseUntil.After(now)) || (item.Status == "claimed" && !item.LeaseUntil.After(now))
}

func runQueueItemBefore(left, right domainsuperagent.RunQueueItem) bool {
	return left.Priority > right.Priority || (left.Priority == right.Priority && left.CreatedAt.Before(right.CreatedAt))
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, timeColumn string, timestamp string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("superagent sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, payload) VALUES (?, ?, ?)`, table, idColumn, timeColumn)
	_, err = s.db.ExecContext(ctx, query, id, timestamp, string(payload))
	return err
}

func (s *SQLiteStore) saveAdmitted(ctx context.Context, table string, idColumn string, id string, timeColumn string, timestamp string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("superagent sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, payload) VALUES (?, ?, ?)`, table, idColumn, timeColumn)
	handled, err := enqueueAdmittedSuperAgentWrite(ctx, s.writeQueue, "sqlite."+table+".insert_or_replace", string(payload), func(workerCtx context.Context) error {
		if s == nil || s.db == nil {
			return fmt.Errorf("superagent sqlite store is closed")
		}
		_, err := s.db.ExecContext(workerCtx, query, id, timestamp, string(payload))
		return err
	})
	if handled || err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, query, id, timestamp, string(payload))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("superagent sqlite store is closed")
	}
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT payload FROM %s ORDER BY rowid DESC LIMIT ?`, table), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []T{}
	for rows.Next() {
		var payload string
		if err := rows.Scan(&payload); err != nil {
			return nil, err
		}
		var item T
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"

func (s *SQLiteStore) migrateRunQueueTaskIndex() error {
	var hasTaskID int
	if err := s.db.QueryRow("SELECT count(*) FROM pragma_table_info('run_queue') WHERE name='task_id'").Scan(&hasTaskID); err != nil {
		return err
	}
	if hasTaskID == 0 {
		if _, err := s.db.Exec("ALTER TABLE run_queue ADD COLUMN task_id TEXT"); err != nil {
			return err
		}
	}
	rows, err := s.db.Query("SELECT queue_item_id, payload FROM run_queue WHERE task_id IS NULL OR task_id=''")
	if err != nil {
		return err
	}
	type legacyQueueRow struct {
		queueID string
		taskID  string
	}
	var backfill []legacyQueueRow
	for rows.Next() {
		var queueID, payload string
		if err := rows.Scan(&queueID, &payload); err != nil {
			rows.Close()
			return err
		}
		var item domainsuperagent.RunQueueItem
		if err := json.Unmarshal([]byte(payload), &item); err != nil {
			rows.Close()
			return err
		}
		if item.TaskID == "" {
			rows.Close()
			return fmt.Errorf("run queue item %s has no task_id for migration", queueID)
		}
		backfill = append(backfill, legacyQueueRow{queueID: queueID, taskID: string(item.TaskID)})
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, row := range backfill {
		if _, err := s.db.Exec("UPDATE run_queue SET task_id=? WHERE queue_item_id=? AND (task_id IS NULL OR task_id='')", row.taskID, row.queueID); err != nil {
			return err
		}
	}
	_, err = s.db.Exec("CREATE INDEX IF NOT EXISTS run_queue_task_status_created_idx ON run_queue(task_id, status, created_at)")
	return err
}

func (s *SQLiteStore) ListRunQueueCandidatesForTasks(ctx context.Context, taskIDs []modulecore.TaskID) ([]domainsuperagent.RunQueueItem, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("superagent sqlite store is closed")
	}
	if len(taskIDs) == 0 {
		return nil, nil
	}
	unique := make([]modulecore.TaskID, 0, len(taskIDs))
	seen := make(map[modulecore.TaskID]struct{}, len(taskIDs))
	for _, taskID := range taskIDs {
		if err := taskID.Validate(); err != nil {
			return nil, fmt.Errorf("list run queue items: invalid task_id: %w", err)
		}
		if _, ok := seen[taskID]; ok {
			continue
		}
		seen[taskID] = struct{}{}
		unique = append(unique, taskID)
	}
	result := make([]domainsuperagent.RunQueueItem, 0)
	for offset := 0; offset < len(unique); offset += 900 {
		end := min(offset+900, len(unique))
		chunk := unique[offset:end]
		placeholders := make([]string, len(chunk))
		args := make([]any, 0, len(chunk)+1)
		for index, taskID := range chunk {
			placeholders[index] = "?"
			args = append(args, string(taskID))
		}
		remaining := runQueueActorCandidateLimit - len(result)
		if remaining <= 0 {
			return result, nil
		}
		args = append(args, remaining)
		query := "SELECT payload FROM run_queue WHERE status IN ('queued','reserved') AND task_id IN (" + strings.Join(placeholders, ",") + ") ORDER BY created_at ASC, queue_item_id ASC LIMIT ?"
		rows, err := s.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var payload string
			if err := rows.Scan(&payload); err != nil {
				rows.Close()
				return nil, err
			}
			var item domainsuperagent.RunQueueItem
			if err := json.Unmarshal([]byte(payload), &item); err != nil {
				rows.Close()
				return nil, err
			}
			result = append(result, item)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		if len(result) >= runQueueActorCandidateLimit {
			return result, nil
		}
	}
	return result, nil
}

func (s *SQLiteStore) ClaimRunQueueItem(ctx context.Context, queueID modulecore.QueueItemID, now, leaseUntil time.Time, leaseToken string) (*domainsuperagent.RunQueueItem, error) {
	if !validExactRunQueueLease(queueID, leaseToken) {
		return nil, fmt.Errorf("run queue exact claim requires a valid queue_item_id and lease token")
	}
	if !leaseUntil.After(now) {
		return nil, fmt.Errorf("run queue exact claim lease must expire after now")
	}
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("superagent sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.QueryRowContext(ctx, "SELECT payload FROM run_queue WHERE queue_item_id=?", string(queueID)).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	} else if err != nil {
		return nil, err
	}
	var item domainsuperagent.RunQueueItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return nil, err
	}
	if item.QueueItemID != queueID || !runQueueItemExactActorClaimable(item, now) {
		return nil, tx.Commit()
	}
	currentStatus := item.Status
	reserveExactRunQueueItem(&item, now, leaseUntil, leaseToken)
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE run_queue SET status='reserved', payload=? WHERE queue_item_id=? AND status=? AND payload=?", string(encoded), string(queueID), currentStatus, payload)
	if err != nil {
		return nil, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if changed != 1 {
		return nil, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *SQLiteStore) ReleaseRunQueueItem(ctx context.Context, queueID modulecore.QueueItemID, leaseToken string) (bool, error) {
	if !validExactRunQueueLease(queueID, leaseToken) {
		return false, nil
	}
	if s == nil || s.db == nil {
		return false, fmt.Errorf("superagent sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.QueryRowContext(ctx, "SELECT payload FROM run_queue WHERE queue_item_id=?", string(queueID)).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
		return false, tx.Commit()
	} else if err != nil {
		return false, err
	}
	var item domainsuperagent.RunQueueItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return false, err
	}
	if item.QueueItemID != queueID || item.Status != "reserved" || item.LeaseToken != leaseToken || item.RunID != "" || item.AttemptCount <= 0 {
		return false, tx.Commit()
	}
	releaseExactRunQueueItem(&item)
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return false, err
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		return false, err
	}
	result, err := tx.ExecContext(ctx, "UPDATE run_queue SET status='queued', payload=? WHERE queue_item_id=? AND status='reserved' AND payload=?", string(encoded), string(queueID), payload)
	if err != nil {
		return false, err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if changed != 1 {
		return false, tx.Commit()
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}
