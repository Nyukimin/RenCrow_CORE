package browsertrace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domaintrace "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/browsertrace"
	_ "github.com/mattn/go-sqlite3"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/browser_trace_to_api.sqlite"
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
		`CREATE TABLE IF NOT EXISTS browser_trace_run (
			trace_run_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_candidate (
			candidate_id TEXT PRIMARY KEY,
			trace_run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_candidate_schema (
			schema_id TEXT PRIMARY KEY,
			candidate_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_candidate_validation (
			validation_id TEXT PRIMARY KEY,
			candidate_id TEXT,
			trace_run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_coverage_report (
			report_id TEXT PRIMARY KEY,
			trace_run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_artifact (
			artifact_id TEXT PRIMARY KEY,
			trace_run_id TEXT,
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

func (s *SQLiteStore) SaveTraceRun(ctx context.Context, item domaintrace.TraceRun) error {
	if err := domaintrace.ValidateTraceRun(item); err != nil {
		return err
	}
	return s.save(ctx, "browser_trace_run", "trace_run_id", item.TraceRunID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListTraceRuns(ctx context.Context, limit int) ([]domaintrace.TraceRun, error) {
	return listSQLiteItems[domaintrace.TraceRun](ctx, s, "browser_trace_run", limit)
}

func (s *SQLiteStore) SaveAPICandidate(ctx context.Context, item domaintrace.APICandidate) error {
	if err := domaintrace.ValidateAPICandidate(item); err != nil {
		return err
	}
	return s.save(ctx, "api_candidate", "candidate_id", item.CandidateID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICandidates(ctx context.Context, limit int) ([]domaintrace.APICandidate, error) {
	return listSQLiteItems[domaintrace.APICandidate](ctx, s, "api_candidate", limit)
}

func (s *SQLiteStore) SaveAPICandidateSchema(ctx context.Context, item domaintrace.APICandidateSchema) error {
	if err := domaintrace.ValidateAPICandidateSchema(item); err != nil {
		return err
	}
	return s.save(ctx, "api_candidate_schema", "schema_id", item.SchemaID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICandidateSchemas(ctx context.Context, limit int) ([]domaintrace.APICandidateSchema, error) {
	return listSQLiteItems[domaintrace.APICandidateSchema](ctx, s, "api_candidate_schema", limit)
}

func (s *SQLiteStore) SaveAPICandidateValidationResult(ctx context.Context, item domaintrace.APICandidateValidationResult) error {
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		return err
	}
	return s.save(ctx, "api_candidate_validation", "validation_id", item.ValidationID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICandidateValidationResults(ctx context.Context, limit int) ([]domaintrace.APICandidateValidationResult, error) {
	return listSQLiteItems[domaintrace.APICandidateValidationResult](ctx, s, "api_candidate_validation", limit)
}

func (s *SQLiteStore) SaveAPICoverageReport(ctx context.Context, item domaintrace.APICoverageReport) error {
	if err := domaintrace.ValidateAPICoverageReport(item); err != nil {
		return err
	}
	return s.save(ctx, "api_coverage_report", "report_id", item.ReportID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICoverageReports(ctx context.Context, limit int) ([]domaintrace.APICoverageReport, error) {
	return listSQLiteItems[domaintrace.APICoverageReport](ctx, s, "api_coverage_report", limit)
}

func (s *SQLiteStore) SaveAPIArtifact(ctx context.Context, item domaintrace.APIArtifact) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	return s.save(ctx, "api_artifact", "artifact_id", item.ArtifactID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPIArtifacts(ctx context.Context, limit int) ([]domaintrace.APIArtifact, error) {
	return listSQLiteItems[domaintrace.APIArtifact](ctx, s, "api_artifact", limit)
}

func (s *SQLiteStore) save(ctx context.Context, table string, idColumn string, id string, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("browser trace sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
	_, err = s.db.ExecContext(ctx, query, id, createdAt, string(payload))
	return err
}

func listSQLiteItems[T any](ctx context.Context, s *SQLiteStore, table string, limit int) ([]T, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("browser trace sqlite store is closed")
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
