package knowledgememory

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/knowledge_memory.db"
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
		`CREATE TABLE IF NOT EXISTS personal_archive (
			entry_id TEXT PRIMARY KEY,
			user_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS creative_knowledge (
			item_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS news_knowledge (
			item_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS daily_intake_rule (
			rule_id TEXT PRIMARY KEY,
			user_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS temporal_memory_marker (
			marker_id TEXT PRIMARY KEY,
			user_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS dream_consolidation_run (
			run_id TEXT PRIMARY KEY,
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

func (s *SQLiteStore) SavePersonalArchiveEntry(ctx context.Context, item domainkm.PersonalArchiveEntry) error {
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return err
	}
	return s.save(ctx, "personal_archive", "entry_id", item.EntryID, "user_id", item.UserID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListPersonalArchiveEntries(ctx context.Context, limit int) ([]domainkm.PersonalArchiveEntry, error) {
	return listSQLiteItems[domainkm.PersonalArchiveEntry](ctx, s, "personal_archive", limit)
}

func (s *SQLiteStore) SaveCreativeKnowledgeItem(ctx context.Context, item domainkm.CreativeKnowledgeItem) error {
	if err := domainkm.ValidateCreativeKnowledgeItem(item); err != nil {
		return err
	}
	return s.save(ctx, "creative_knowledge", "item_id", item.ItemID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListCreativeKnowledgeItems(ctx context.Context, limit int) ([]domainkm.CreativeKnowledgeItem, error) {
	return listSQLiteItems[domainkm.CreativeKnowledgeItem](ctx, s, "creative_knowledge", limit)
}

func (s *SQLiteStore) SaveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error {
	if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
		return err
	}
	return s.save(ctx, "news_knowledge", "item_id", item.ItemID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListNewsKnowledgeItems(ctx context.Context, limit int) ([]domainkm.NewsKnowledgeItem, error) {
	return listSQLiteItems[domainkm.NewsKnowledgeItem](ctx, s, "news_knowledge", limit)
}

func (s *SQLiteStore) SaveDailyIntakeRule(ctx context.Context, item domainkm.DailyIntakeRule) error {
	if err := domainkm.ValidateDailyIntakeRule(item); err != nil {
		return err
	}
	return s.save(ctx, "daily_intake_rule", "rule_id", item.RuleID, "user_id", item.UserID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListDailyIntakeRules(ctx context.Context, limit int) ([]domainkm.DailyIntakeRule, error) {
	return listSQLiteItems[domainkm.DailyIntakeRule](ctx, s, "daily_intake_rule", limit)
}

func (s *SQLiteStore) SaveTemporalMemoryMarker(ctx context.Context, item domainkm.TemporalMemoryMarker) error {
	if err := domainkm.ValidateTemporalMemoryMarker(item); err != nil {
		return err
	}
	return s.save(ctx, "temporal_memory_marker", "marker_id", item.MarkerID, "user_id", item.UserID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListTemporalMemoryMarkers(ctx context.Context, limit int) ([]domainkm.TemporalMemoryMarker, error) {
	return listSQLiteItems[domainkm.TemporalMemoryMarker](ctx, s, "temporal_memory_marker", limit)
}

func (s *SQLiteStore) SaveDreamConsolidationRun(ctx context.Context, item domainkm.DreamConsolidationRun) error {
	if err := domainkm.ValidateDreamConsolidationRun(item); err != nil {
		return err
	}
	return s.save(ctx, "dream_consolidation_run", "run_id", item.RunID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListDreamConsolidationRuns(ctx context.Context, limit int) ([]domainkm.DreamConsolidationRun, error) {
	return listSQLiteItems[domainkm.DreamConsolidationRun](ctx, s, "dream_consolidation_run", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, secondaryColumn string, secondaryValue string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge memory sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	if secondaryColumn == "" {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
		_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, created_at, payload) VALUES (?, ?, ?, ?)`, table, idColumn, secondaryColumn)
	_, err = s.db.ExecContext(ctx, query, id, secondaryValue, createdAt, string(payload))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("knowledge memory sqlite store is closed")
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
