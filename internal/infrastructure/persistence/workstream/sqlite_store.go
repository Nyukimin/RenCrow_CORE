package workstream

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db        *sql.DB
	vaultRoot string
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	return NewSQLiteStoreWithVault(path, "")
}

func NewSQLiteStoreWithVault(path, vaultRoot string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/workstream.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	store := &SQLiteStore{db: db, vaultRoot: vaultRoot}
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
		`CREATE TABLE IF NOT EXISTS workstream (
			workstream_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS workstream_goal (
			goal_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS artifact (
			artifact_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS artifact_annotation (
			annotation_id TEXT PRIMARY KEY,
			artifact_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS steering_queue (
			steering_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS heartbeat_schedule (
			heartbeat_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS vault_update_log (
			update_id TEXT PRIMARY KEY,
			workstream_id TEXT,
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

func (s *SQLiteStore) SaveWorkstream(ctx context.Context, item domainworkstream.Workstream) error {
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return err
	}
	if s.vaultRoot != "" {
		vaultPath, err := ensureVaultFiles(s.vaultRoot, item)
		if err != nil {
			return err
		}
		if item.VaultPath == "" {
			item.VaultPath = vaultPath
		}
	}
	return s.save(ctx, "workstream", "workstream_id", item.WorkstreamID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListWorkstreams(ctx context.Context, limit int) ([]domainworkstream.Workstream, error) {
	return listSQLiteItems[domainworkstream.Workstream](ctx, s, "workstream", limit)
}

func (s *SQLiteStore) SaveGoal(ctx context.Context, item domainworkstream.Goal) error {
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return err
	}
	return s.save(ctx, "workstream_goal", "goal_id", item.GoalID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListGoals(ctx context.Context, limit int) ([]domainworkstream.Goal, error) {
	return listSQLiteItems[domainworkstream.Goal](ctx, s, "workstream_goal", limit)
}

func (s *SQLiteStore) SaveArtifact(ctx context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	return s.save(ctx, "artifact", "artifact_id", item.ArtifactID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListArtifacts(ctx context.Context, limit int) ([]domainworkstream.Artifact, error) {
	return listSQLiteItems[domainworkstream.Artifact](ctx, s, "artifact", limit)
}

func (s *SQLiteStore) SaveArtifactAnnotation(ctx context.Context, item domainworkstream.ArtifactAnnotation) error {
	if err := domainworkstream.ValidateArtifactAnnotation(item); err != nil {
		return err
	}
	return s.save(ctx, "artifact_annotation", "annotation_id", item.AnnotationID, "artifact_id", item.ArtifactID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListArtifactAnnotations(ctx context.Context, limit int) ([]domainworkstream.ArtifactAnnotation, error) {
	return listSQLiteItems[domainworkstream.ArtifactAnnotation](ctx, s, "artifact_annotation", limit)
}

func (s *SQLiteStore) SaveSteeringItem(ctx context.Context, item domainworkstream.SteeringItem) error {
	if err := domainworkstream.ValidateSteeringItem(item); err != nil {
		return err
	}
	return s.save(ctx, "steering_queue", "steering_id", item.SteeringID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListSteeringItems(ctx context.Context, limit int) ([]domainworkstream.SteeringItem, error) {
	return listSQLiteItems[domainworkstream.SteeringItem](ctx, s, "steering_queue", limit)
}

func (s *SQLiteStore) SaveHeartbeatSchedule(ctx context.Context, item domainworkstream.HeartbeatSchedule) error {
	if err := domainworkstream.ValidateHeartbeatSchedule(item); err != nil {
		return err
	}
	return s.save(ctx, "heartbeat_schedule", "heartbeat_id", item.HeartbeatID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListHeartbeatSchedules(ctx context.Context, limit int) ([]domainworkstream.HeartbeatSchedule, error) {
	return listSQLiteItems[domainworkstream.HeartbeatSchedule](ctx, s, "heartbeat_schedule", limit)
}

func (s *SQLiteStore) SaveVaultUpdateLog(ctx context.Context, item domainworkstream.VaultUpdateLog) error {
	if err := domainworkstream.ValidateVaultUpdateLog(item); err != nil {
		return err
	}
	return s.save(ctx, "vault_update_log", "update_id", item.UpdateID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListVaultUpdateLogs(ctx context.Context, limit int) ([]domainworkstream.VaultUpdateLog, error) {
	return listSQLiteItems[domainworkstream.VaultUpdateLog](ctx, s, "vault_update_log", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, secondaryColumn string, secondaryValue string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("workstream sqlite store is closed")
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
		return nil, fmt.Errorf("workstream sqlite store is closed")
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
