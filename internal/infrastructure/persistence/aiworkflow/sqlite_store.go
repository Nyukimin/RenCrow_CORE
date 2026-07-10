package aiworkflow

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/ai_workflow.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db}
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
		`CREATE TABLE IF NOT EXISTS ai_workflow_event (
			event_id TEXT PRIMARY KEY,
			parent_event_id TEXT,
			run_id TEXT,
			workstream_id TEXT,
			event_type TEXT,
			agent TEXT,
			repo TEXT,
			worktree_id TEXT,
			command_name TEXT,
			skill_name TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS project_memory_index (
			id TEXT PRIMARY KEY,
			repo TEXT,
			file_path TEXT,
			memory_type TEXT,
			updated_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS worktree_registry (
			worktree_id TEXT PRIMARY KEY,
			repo TEXT,
			path TEXT,
			branch TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS command_registry (
			command_name TEXT PRIMARY KEY,
			file_path TEXT,
			default_agent TEXT,
			required_skill TEXT,
			updated_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS ai_context_usage (
			event_id TEXT PRIMARY KEY,
			session_id TEXT,
			run_id TEXT,
			workstream_id TEXT,
			job_id TEXT,
			agent TEXT,
			model TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
	}
	for _, stmt := range stmts {
		if _, err := s.db.Exec(stmt); err != nil {
			return err
		}
	}
	if err := addColumnIfMissing(s.db, "ai_workflow_event", "run_id", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(s.db, "ai_workflow_event", "workstream_id", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(s.db, "ai_context_usage", "run_id", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(s.db, "ai_context_usage", "workstream_id", "TEXT"); err != nil {
		return err
	}
	if err := addColumnIfMissing(s.db, "ai_context_usage", "job_id", "TEXT"); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error {
	if err := domainai.ValidateWorkflowEvent(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO ai_workflow_event (
		event_id, parent_event_id, run_id, workstream_id, event_type, agent, repo, worktree_id, command_name, skill_name, status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.EventID, item.ParentEventID, item.RunID, item.WorkstreamID, item.EventType, item.Agent, item.Repo, item.WorktreeID, item.CommandName, item.SkillName, item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListWorkflowEvents(ctx context.Context, limit int) ([]domainai.WorkflowEvent, error) {
	return listSQLiteItems[domainai.WorkflowEvent](ctx, s, "ai_workflow_event", limit)
}

func (s *SQLiteStore) SaveProjectMemoryIndex(ctx context.Context, item domainai.ProjectMemoryIndex) error {
	if err := domainai.ValidateProjectMemoryIndex(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO project_memory_index (
		id, repo, file_path, memory_type, updated_at, payload
	) VALUES (?, ?, ?, ?, ?, ?)`,
		item.ID, item.Repo, item.FilePath, item.MemoryType, item.UpdatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListProjectMemoryIndexes(ctx context.Context, limit int) ([]domainai.ProjectMemoryIndex, error) {
	return listSQLiteItems[domainai.ProjectMemoryIndex](ctx, s, "project_memory_index", limit)
}

func (s *SQLiteStore) SaveWorktreeRegistry(ctx context.Context, item domainai.WorktreeRegistry) error {
	if err := domainai.ValidateWorktreeRegistry(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO worktree_registry (
		worktree_id, repo, path, branch, status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		item.WorktreeID, item.Repo, item.Path, item.Branch, item.Status, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListWorktreeRegistries(ctx context.Context, limit int) ([]domainai.WorktreeRegistry, error) {
	return listSQLiteItems[domainai.WorktreeRegistry](ctx, s, "worktree_registry", limit)
}

func (s *SQLiteStore) SaveCommandRegistry(ctx context.Context, item domainai.CommandRegistry) error {
	if err := domainai.ValidateCommandRegistry(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO command_registry (
		command_name, file_path, default_agent, required_skill, updated_at, payload
	) VALUES (?, ?, ?, ?, ?, ?)`,
		item.CommandName, item.FilePath, item.DefaultAgent, item.RequiredSkill, item.UpdatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListCommandRegistries(ctx context.Context, limit int) ([]domainai.CommandRegistry, error) {
	return listSQLiteItems[domainai.CommandRegistry](ctx, s, "command_registry", limit)
}

func (s *SQLiteStore) SaveContextUsage(ctx context.Context, item domainai.ContextUsage) error {
	if err := domainai.ValidateContextUsage(item); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO ai_context_usage (
		event_id, session_id, run_id, workstream_id, job_id, agent, model, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.EventID, item.SessionID, item.RunID, item.WorkstreamID, item.JobID, item.Agent, item.Model, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListContextUsages(ctx context.Context, limit int) ([]domainai.ContextUsage, error) {
	return listSQLiteItems[domainai.ContextUsage](ctx, s, "ai_context_usage", limit)
}

func (s *SQLiteStore) save(ctx context.Context, query string, args ...any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("ai workflow sqlite store is closed")
	}
	if len(args) == 0 {
		return fmt.Errorf("ai workflow sqlite store save requires payload")
	}
	payload, err := json.Marshal(args[len(args)-1])
	if err != nil {
		return err
	}
	args[len(args)-1] = string(payload)
	_, err = s.db.ExecContext(ctx, query, args...)
	return err
}

func addColumnIfMissing(db *sql.DB, table string, column string, columnType string) error {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, columnType))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("ai workflow sqlite store is closed")
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
