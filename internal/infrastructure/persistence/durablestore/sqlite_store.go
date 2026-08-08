package durablestore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	_ "modernc.org/sqlite"
)

const schemaVersion = 1

type SQLiteStore struct{ db *sql.DB }

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("durable store workflow sqlite path is required")
	}
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("durable store workflow sqlite path must be absolute")
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("durable store workflow parent directory: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("durable store workflow parent is not a directory")
	}
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	s := &SQLiteStore{db: db}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLiteStore) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *SQLiteStore) migrate() error {
	var version int
	if err := s.db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > schemaVersion {
		return fmt.Errorf("durable store workflow schema version %d is newer than supported %d", version, schemaVersion)
	}
	if version == schemaVersion {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`CREATE TABLE durable_store_workflow (
		requirement_id TEXT PRIMARY KEY,
		dedupe_key TEXT NOT NULL UNIQUE,
		status TEXT NOT NULL,
		lifecycle TEXT NOT NULL,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		payload TEXT NOT NULL
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", schemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) Save(ctx context.Context, result domain.WorkflowResult) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("durable store workflow sqlite store is closed")
	}
	if result.Requirement.RequirementID == "" || result.Requirement.DedupeKey == "" {
		return fmt.Errorf("requirement_id and dedupe_key are required")
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO durable_store_workflow (requirement_id, dedupe_key, status, lifecycle, created_at, updated_at, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dedupe_key) DO UPDATE SET status=excluded.status, lifecycle=excluded.lifecycle, updated_at=excluded.updated_at, payload=excluded.payload`,
		result.Requirement.RequirementID, result.Requirement.DedupeKey, result.Status, result.Lifecycle, result.CreatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), result.UpdatedAt.Format("2006-01-02T15:04:05.999999999Z07:00"), string(payload))
	return err
}

func (s *SQLiteStore) FindByDedupeKey(ctx context.Context, key string) (*domain.WorkflowResult, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("durable store workflow sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM durable_store_workflow WHERE dedupe_key = ?`, key).Scan(&payload)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var result domain.WorkflowResult
	if err := json.Unmarshal([]byte(payload), &result); err != nil {
		return nil, err
	}
	return &result, nil
}
