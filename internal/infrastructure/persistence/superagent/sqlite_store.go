package superagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
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
			subagent_id TEXT PRIMARY KEY,
			parent_run_id TEXT,
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
		`CREATE TABLE IF NOT EXISTS trace_event (
			event_id TEXT PRIMARY KEY,
			run_id TEXT,
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
	return s.save(ctx, "agent_run", "run_id", item.RunID, "started_at", item.StartedAt.Format(timeFormatRFC3339Nano), item)
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
	if item.RunID != runID {
		return domainsuperagent.AgentRun{}, false, fmt.Errorf("stored agent run ID %q does not match primary key %q", item.RunID, runID)
	}
	return item, true, nil
}

func (s *SQLiteStore) SaveSubagentTask(ctx context.Context, item domainsuperagent.SubagentTask) error {
	if err := domainsuperagent.ValidateSubagentTask(item); err != nil {
		return err
	}
	return s.save(ctx, "subagent_task", "subagent_id", item.SubagentID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
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

func (s *SQLiteStore) SaveTraceEvent(ctx context.Context, item domainsuperagent.TraceEvent) error {
	if err := domainsuperagent.ValidateTraceEvent(item); err != nil {
		return err
	}
	return s.save(ctx, "trace_event", "event_id", item.EventID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListTraceEvents(ctx context.Context, limit int) ([]domainsuperagent.TraceEvent, error) {
	return listSQLiteItems[domainsuperagent.TraceEvent](ctx, s, "trace_event", limit)
}

// FindTraceEventByID returns the record stored under the exact trace_event primary key.
func (s *SQLiteStore) FindTraceEventByID(ctx context.Context, eventID string) (domainsuperagent.TraceEvent, bool, error) {
	if s == nil || s.db == nil {
		return domainsuperagent.TraceEvent{}, false, fmt.Errorf("superagent sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM trace_event WHERE event_id = ?`, eventID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domainsuperagent.TraceEvent{}, false, nil
	}
	if err != nil {
		return domainsuperagent.TraceEvent{}, false, err
	}
	var item domainsuperagent.TraceEvent
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainsuperagent.TraceEvent{}, false, err
	}
	if err := domainsuperagent.ValidateTraceEvent(item); err != nil {
		return domainsuperagent.TraceEvent{}, false, err
	}
	if item.EventID != eventID {
		return domainsuperagent.TraceEvent{}, false, fmt.Errorf("stored trace event ID %q does not match primary key %q", item.EventID, eventID)
	}
	return item, true, nil
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
	item.Status, item.ClaimedAt, item.LeaseToken, item.LeaseUntil = "claimed", now, leaseToken, leaseUntil
	item.AttemptCount++
	item.CompletedAt = time.Time{}
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE run_queue SET status='claimed', payload=? WHERE queue_id=? AND payload=?`, string(encoded), item.QueueID, selected.payload)
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

func (s *SQLiteStore) RenewRunQueueLease(ctx context.Context, queueID, leaseToken string, leaseUntil time.Time) (bool, error) {
	return s.updateRunQueueLease(ctx, queueID, leaseToken, func(item *domainsuperagent.RunQueueItem) {
		item.LeaseUntil = leaseUntil
	})
}

func (s *SQLiteStore) CompleteRunQueueItem(ctx context.Context, queueID, leaseToken, status, reason string, completedAt time.Time) (bool, error) {
	return s.updateRunQueueLease(ctx, queueID, leaseToken, func(item *domainsuperagent.RunQueueItem) {
		item.Status, item.Reason, item.CompletedAt = status, reason, completedAt
		item.LeaseToken, item.LeaseUntil = "", time.Time{}
	})
}

func (s *SQLiteStore) updateRunQueueLease(ctx context.Context, queueID, leaseToken string, mutate func(*domainsuperagent.RunQueueItem)) (bool, error) {
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
	if item.Status != "claimed" || item.LeaseToken != leaseToken {
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
	return item.Status == "queued" || (item.Status == "claimed" && !item.LeaseUntil.After(now))
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
