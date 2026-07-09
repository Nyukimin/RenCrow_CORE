package superagent

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	_ "github.com/mattn/go-sqlite3"
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
	db, err := sql.Open("sqlite3", path)
	if err != nil {
		return nil, err
	}
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

func (s *SQLiteStore) SaveRunQueueItem(ctx context.Context, item domainsuperagent.RunQueueItem) error {
	if err := domainsuperagent.ValidateRunQueueItem(item); err != nil {
		return err
	}
	return s.save(ctx, "run_queue", "queue_id", item.QueueID, "created_at", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListRunQueueItems(ctx context.Context, limit int) ([]domainsuperagent.RunQueueItem, error) {
	return listSQLiteItems[domainsuperagent.RunQueueItem](ctx, s, "run_queue", limit)
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
