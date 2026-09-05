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

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db               *sql.DB
	maxContextTokens int
}

func NewSQLiteStore(path string, maxContextTokens int) (*SQLiteStore, error) {
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
	store := &SQLiteStore{db: db, maxContextTokens: maxContextTokens}
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
		`CREATE TABLE IF NOT EXISTS context_pack (
			context_pack_id TEXT PRIMARY KEY,
			run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS message_channel (
			channel_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS run_queue (
			queue_id TEXT PRIMARY KEY,
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
	return nil
}

func (s *SQLiteStore) SaveAgentRun(ctx context.Context, item domainsuperagent.AgentRun) error {
	if err := domainsuperagent.ValidateAgentRun(item); err != nil {
		return err
	}
	return s.save(ctx, "agent_run", "run_id", string(item.RunID), "started_at", item.StartedAt.Format(timeFormatRFC3339Nano), item)
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
	return s.save(ctx, "context_pack", "context_pack_id", item.ContextPackID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
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
	_, err = s.db.ExecContext(ctx, `INSERT OR REPLACE INTO run_queue (queue_id, status, created_at, payload) VALUES (?, ?, ?, ?)`, item.QueueID, item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), string(payload))
	return err
}

func (s *SQLiteStore) ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	return listSQLiteItems[domainsuperagent.RunQueueItem](ctx, s, "run_queue", limit)
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
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status='reserved', payload=? WHERE queue_id=? AND payload=?`, string(encoded), item.QueueID, selected.payload)
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
func (s *SQLiteStore) AttachRunQueueRun(ctx context.Context, queueID, leaseToken string, canonicalRunID modulecore.RunID) (bool, error) {
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
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM run_queue WHERE queue_id=?`, queueID).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
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
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status='claimed', payload=? WHERE queue_id=? AND payload=?`, string(encoded), queueID, payload)
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

func (s *SQLiteStore) RenewRunQueueLease(ctx context.Context, queueID, leaseToken string, leaseUntil time.Time) (bool, error) {
	return s.updateRunQueueLease(ctx, queueID, leaseToken, false, false, func(item *domainsuperagent.RunQueueItem) {
		item.LeaseUntil = leaseUntil
	})
}

func (s *SQLiteStore) CompleteRunQueueItem(ctx context.Context, queueID, leaseToken, status, reason string, completedAt time.Time) (bool, error) {
	return s.updateRunQueueLease(ctx, queueID, leaseToken, true, status == "blocked", func(item *domainsuperagent.RunQueueItem) {
		item.Status, item.Reason, item.CompletedAt = status, reason, completedAt
		item.LeaseToken, item.LeaseUntil = "", time.Time{}
	})
}

func (s *SQLiteStore) updateRunQueueLease(ctx context.Context, queueID, leaseToken string, claimedOnly, allowReservedBlock bool, mutate func(*domainsuperagent.RunQueueItem)) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("superagent sqlite store is closed")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	var payload string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM run_queue WHERE queue_id=?`, queueID).Scan(&payload); errors.Is(err, sql.ErrNoRows) {
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
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status=?, payload=? WHERE queue_id=? AND payload=?`, item.Status, string(encoded), queueID, payload)
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
