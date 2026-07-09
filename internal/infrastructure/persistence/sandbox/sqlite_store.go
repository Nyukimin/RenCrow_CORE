package sandbox

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domainsandbox "github.com/Nyukimin/RenCrow_CORE/internal/domain/sandbox"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/sandbox.db"
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", path)
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
		`CREATE TABLE IF NOT EXISTS sandbox_registry (
			sandbox_id TEXT PRIMARY KEY,
			workstream_id TEXT,
			goal_id TEXT,
			sandbox_type TEXT,
			path TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sandbox_artifact (
			artifact_id TEXT PRIMARY KEY,
			sandbox_id TEXT,
			artifact_type TEXT,
			file_path TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS sandbox_promotion_request (
			promotion_id TEXT PRIMARY KEY,
			sandbox_id TEXT,
			workstream_id TEXT,
			goal_id TEXT,
			target_path TEXT,
			status TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS promotion_gate_log (
			event_id TEXT PRIMARY KEY,
			promotion_id TEXT,
			gate_status TEXT,
			human_approval_status TEXT,
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

func (s *SQLiteStore) SaveSandbox(ctx context.Context, record domainsandbox.SandboxRecord) error {
	if err := domainsandbox.ValidateSandboxRecord(record); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO sandbox_registry (
		sandbox_id, workstream_id, goal_id, sandbox_type, path, status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		record.SandboxID, record.WorkstreamID, record.GoalID, record.Type, record.Path, record.Status, record.CreatedAt.Format(timeFormatRFC3339Nano), record)
}

func (s *SQLiteStore) ListSandboxes(ctx context.Context, limit int) ([]domainsandbox.SandboxRecord, error) {
	return listSQLiteItems[domainsandbox.SandboxRecord](ctx, s, "sandbox_registry", limit)
}

func (s *SQLiteStore) SaveSandboxArtifact(ctx context.Context, artifact domainsandbox.SandboxArtifact) error {
	if err := domainsandbox.ValidateSandboxArtifact(artifact); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO sandbox_artifact (
		artifact_id, sandbox_id, artifact_type, file_path, status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		artifact.ArtifactID, artifact.SandboxID, artifact.Type, artifact.FilePath, artifact.Status, artifact.CreatedAt.Format(timeFormatRFC3339Nano), artifact)
}

func (s *SQLiteStore) ListSandboxArtifacts(ctx context.Context, limit int) ([]domainsandbox.SandboxArtifact, error) {
	return listSQLiteItems[domainsandbox.SandboxArtifact](ctx, s, "sandbox_artifact", limit)
}

func (s *SQLiteStore) SavePromotionRequest(ctx context.Context, req domainsandbox.PromotionRequest) error {
	if err := domainsandbox.ValidatePromotionRequest(req); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO sandbox_promotion_request (
		promotion_id, sandbox_id, workstream_id, goal_id, target_path, status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		req.PromotionID, req.SandboxID, req.WorkstreamID, req.GoalID, req.TargetPath, req.HumanApprovalStatus, req.CreatedAt.Format(timeFormatRFC3339Nano), req)
}

func (s *SQLiteStore) ListPromotionRequests(ctx context.Context, limit int) ([]domainsandbox.PromotionRequest, error) {
	return listSQLiteItems[domainsandbox.PromotionRequest](ctx, s, "sandbox_promotion_request", limit)
}

func (s *SQLiteStore) SavePromotionGateLog(ctx context.Context, log domainsandbox.PromotionGateLog) error {
	if err := domainsandbox.ValidatePromotionGateLog(log); err != nil {
		return err
	}
	return s.save(ctx, `INSERT OR REPLACE INTO promotion_gate_log (
		event_id, promotion_id, gate_status, human_approval_status, created_at, payload
	) VALUES (?, ?, ?, ?, ?, ?)`,
		log.EventID, log.PromotionID, log.GateStatus, log.HumanApprovalStatus, log.CreatedAt.Format(timeFormatRFC3339Nano), log)
}

func (s *SQLiteStore) ListPromotionGateLogs(ctx context.Context, limit int) ([]domainsandbox.PromotionGateLog, error) {
	return listSQLiteItems[domainsandbox.PromotionGateLog](ctx, s, "promotion_gate_log", limit)
}

func (s *SQLiteStore) save(ctx context.Context, query string, args ...any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("sandbox sqlite store is closed")
	}
	if len(args) == 0 {
		return fmt.Errorf("sandbox sqlite store save requires payload")
	}
	payload, err := json.Marshal(args[len(args)-1])
	if err != nil {
		return err
	}
	args[len(args)-1] = string(payload)
	_, err = s.db.ExecContext(ctx, query, args...)
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("sandbox sqlite store is closed")
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
