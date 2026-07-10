package dci

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	domaindci "github.com/Nyukimin/RenCrow_CORE/internal/domain/dci"

	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
}

func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		return nil, fmt.Errorf("failed to open dci sqlite: %w", err)
	}
	store := &SQLiteStore{db: db}
	if err := store.ensureSchema(); err != nil {
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

func (s *SQLiteStore) ensureSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS dci_search_trace (
  event_id TEXT PRIMARY KEY,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  actor TEXT NOT NULL,
  mode TEXT NOT NULL,
  user_query TEXT,
  corpus_scope TEXT,
  status TEXT NOT NULL,
  final_evidence_count INTEGER DEFAULT 0,
  error_message TEXT
);

CREATE TABLE IF NOT EXISTS dci_search_step (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  step_no INTEGER NOT NULL,
  tool TEXT NOT NULL,
  command_text TEXT,
  file_path TEXT,
  result_count INTEGER,
  status TEXT NOT NULL,
  error_message TEXT,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS dci_evidence (
  evidence_id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL,
  source_id TEXT,
  file_path TEXT NOT NULL,
  heading TEXT,
  line_start INTEGER,
  line_end INTEGER,
  snippet TEXT NOT NULL,
  reason TEXT,
  confidence REAL DEFAULT 0.0,
  created_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS dci_query_terms (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  event_id TEXT NOT NULL,
  term TEXT NOT NULL,
  term_type TEXT,
  parent_term TEXT,
  created_at TEXT NOT NULL
);
`
	if _, err := s.db.Exec(schema); err != nil {
		return fmt.Errorf("failed to initialize dci sqlite schema: %w", err)
	}
	return nil
}

func (s *SQLiteStore) SaveSearchTrace(ctx context.Context, trace domaindci.SearchTrace) error {
	return s.SaveSearchResult(ctx, domaindci.SearchResult{Trace: trace})
}

func (s *SQLiteStore) SaveSearchResult(ctx context.Context, result domaindci.SearchResult) error {
	trace := result.Trace
	if err := domaindci.ValidateSearchTrace(trace); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	scopeJSON, err := marshalStringSlice(trace.CorpusScope)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT OR REPLACE INTO dci_search_trace (
  event_id, started_at, ended_at, actor, mode, user_query, corpus_scope, status,
  final_evidence_count, error_message
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		trace.EventID,
		formatTime(trace.StartedAt),
		formatTime(trace.EndedAt),
		trace.Actor,
		trace.Mode,
		trace.UserQuery,
		scopeJSON,
		trace.Status,
		trace.FinalEvidenceCount,
		trace.ErrorMessage,
	)
	if err != nil {
		return fmt.Errorf("failed to save dci search trace: %w", err)
	}
	for _, table := range []string{"dci_search_step", "dci_evidence", "dci_query_terms"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE event_id = ?", trace.EventID); err != nil {
			return err
		}
	}
	for _, step := range trace.Steps {
		if _, err = tx.ExecContext(ctx, `
INSERT INTO dci_search_step (
  event_id, step_no, tool, command_text, file_path, result_count, status, error_message, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			trace.EventID,
			step.StepNo,
			step.Tool,
			step.CommandText,
			step.FilePath,
			step.ResultCount,
			step.Status,
			step.ErrorMessage,
			formatTime(step.CreatedAt),
		); err != nil {
			return fmt.Errorf("failed to save dci search step: %w", err)
		}
	}
	createdAt := trace.EndedAt
	if createdAt.IsZero() {
		createdAt = trace.StartedAt
	}
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	for _, evidence := range result.Pack.Evidence {
		if evidence.EvidenceID == "" {
			return fmt.Errorf("dci evidence_id is required")
		}
		if _, err = tx.ExecContext(ctx, `
INSERT OR REPLACE INTO dci_evidence (
  evidence_id, event_id, source_id, file_path, heading, line_start, line_end,
  snippet, reason, confidence, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			evidence.EvidenceID,
			trace.EventID,
			evidence.SourceID,
			evidence.FilePath,
			evidence.Heading,
			evidence.LineStart,
			evidence.LineEnd,
			evidence.Snippet,
			evidence.Reason,
			evidence.Confidence,
			formatTime(createdAt),
		); err != nil {
			return fmt.Errorf("failed to save dci evidence: %w", err)
		}
	}
	for _, term := range result.Pack.DerivedTerms {
		if term == "" {
			continue
		}
		if _, err = tx.ExecContext(ctx, `
INSERT INTO dci_query_terms (event_id, term, term_type, parent_term, created_at)
VALUES (?, ?, ?, ?, ?)`,
			trace.EventID,
			term,
			"derived",
			result.Pack.Query,
			formatTime(createdAt),
		); err != nil {
			return fmt.Errorf("failed to save dci query term: %w", err)
		}
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *SQLiteStore) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.Query(`
SELECT event_id, started_at, ended_at, actor, mode, user_query, corpus_scope, status,
       final_evidence_count, error_message
FROM dci_search_trace
ORDER BY started_at DESC, event_id DESC
LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var traces []domaindci.SearchTrace
	for rows.Next() {
		var trace domaindci.SearchTrace
		var startedAt, endedAt, scopeJSON string
		if err := rows.Scan(
			&trace.EventID,
			&startedAt,
			&endedAt,
			&trace.Actor,
			&trace.Mode,
			&trace.UserQuery,
			&scopeJSON,
			&trace.Status,
			&trace.FinalEvidenceCount,
			&trace.ErrorMessage,
		); err != nil {
			return nil, err
		}
		trace.StartedAt = parseTime(startedAt)
		trace.EndedAt = parseTime(endedAt)
		trace.CorpusScope = unmarshalStringSlice(scopeJSON)
		steps, err := s.listSteps(trace.EventID)
		if err != nil {
			return nil, err
		}
		trace.Steps = steps
		traces = append(traces, trace)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return traces, nil
}

func (s *SQLiteStore) listSteps(eventID string) ([]domaindci.SearchStep, error) {
	rows, err := s.db.Query(`
SELECT step_no, tool, command_text, file_path, result_count, status, error_message, created_at
FROM dci_search_step
WHERE event_id = ?
ORDER BY step_no ASC, id ASC`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var steps []domaindci.SearchStep
	for rows.Next() {
		var step domaindci.SearchStep
		var createdAt string
		if err := rows.Scan(
			&step.StepNo,
			&step.Tool,
			&step.CommandText,
			&step.FilePath,
			&step.ResultCount,
			&step.Status,
			&step.ErrorMessage,
			&createdAt,
		); err != nil {
			return nil, err
		}
		step.CreatedAt = parseTime(createdAt)
		steps = append(steps, step)
	}
	return steps, rows.Err()
}

func marshalStringSlice(items []string) (string, error) {
	if items == nil {
		items = []string{}
	}
	b, err := json.Marshal(items)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func unmarshalStringSlice(raw string) []string {
	var items []string
	if raw == "" || json.Unmarshal([]byte(raw), &items) != nil {
		return []string{}
	}
	return items
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}
	}
	return t
}
