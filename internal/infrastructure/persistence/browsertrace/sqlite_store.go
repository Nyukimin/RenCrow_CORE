package browsertrace

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

type SQLiteStore struct {
	db *sql.DB
	// rollbackArtifact ends the transaction this store's Artifact operations open.
	// NewSQLiteStore always installs the real ROLLBACK; only tests in this package
	// replace it, on their own store instance, to reach a rollback that fails.
	rollbackArtifact artifactRollbackFunc
}

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	if path == "" {
		path = "workspace/logs/browser_trace_to_api.sqlite"
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
	store := &SQLiteStore{db: db, rollbackArtifact: realArtifactRollback}
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
			run_id TEXT PRIMARY KEY,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_candidate (
			candidate_id TEXT PRIMARY KEY,
			run_id TEXT,
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
			run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_coverage_report (
			artifact_id TEXT PRIMARY KEY,
			run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS api_artifact (
			artifact_id TEXT PRIMARY KEY,
			run_id TEXT,
			created_at TEXT,
			payload TEXT NOT NULL
		)`,
		// One owner-local publication intent row per created artifact, in the same
		// database and the same transaction as the artifact write. The payload holds
		// the typed creation envelope; artifact_id and event_id are validated indexes
		// of that envelope, not separately editable truth. The UNIQUE event_id is what
		// makes one event id claimable by only one creation.
		`CREATE TABLE IF NOT EXISTS ` + publicationIntentTable + ` (
			artifact_id TEXT PRIMARY KEY,
			event_id TEXT NOT NULL UNIQUE,
			payload TEXT NOT NULL
		)`,
		// One owner-local supersession fact row per established edge, in the same
		// database and the same transaction as the edge UPDATE. The payload holds the
		// typed supersession envelope; artifact_id is the predecessor the fact is about
		// and event_id is a validated index of that envelope, not separately editable
		// truth. The UNIQUE event_id stops one event id from claiming two supersessions.
		`CREATE TABLE IF NOT EXISTS ` + supersessionIntentTable + ` (
			artifact_id TEXT PRIMARY KEY,
			event_id TEXT NOT NULL UNIQUE,
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
	return s.save(ctx, "browser_trace_run", "run_id", string(item.RunID), item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListTraceRuns(ctx context.Context, limit int) ([]domaintrace.TraceRun, error) {
	return listSQLiteItems[domaintrace.TraceRun](ctx, s, "browser_trace_run", limit)
}

func (s *SQLiteStore) SaveAPICandidate(ctx context.Context, item domaintrace.APICandidate) error {
	if err := domaintrace.ValidateAPICandidate(item); err != nil {
		return err
	}
	return s.saveOwned(ctx, "api_candidate", "candidate_id", item.CandidateID, item.RunID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICandidates(ctx context.Context, limit int) ([]domaintrace.APICandidate, error) {
	return listSQLiteItems[domaintrace.APICandidate](ctx, s, "api_candidate", limit)
}

// FindAPICandidateByID returns the row with the exact primary ID without scanning a list.
func (s *SQLiteStore) FindAPICandidateByID(ctx context.Context, candidateID string) (domaintrace.APICandidate, bool, error) {
	if s == nil || s.db == nil {
		return domaintrace.APICandidate{}, false, fmt.Errorf("browser trace sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM api_candidate WHERE candidate_id = ?`, candidateID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return domaintrace.APICandidate{}, false, nil
		}
		return domaintrace.APICandidate{}, false, err
	}
	var item domaintrace.APICandidate
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domaintrace.APICandidate{}, false, err
	}
	return item, true, nil
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
	return s.saveOwned(ctx, "api_candidate_validation", "validation_id", item.ValidationID, item.RunID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICandidateValidationResults(ctx context.Context, limit int) ([]domaintrace.APICandidateValidationResult, error) {
	return listSQLiteItems[domaintrace.APICandidateValidationResult](ctx, s, "api_candidate_validation", limit)
}

// FindAPICandidateValidationResultByID returns the row with the exact primary ID without scanning a list.
func (s *SQLiteStore) FindAPICandidateValidationResultByID(ctx context.Context, validationID string) (domaintrace.APICandidateValidationResult, bool, error) {
	if s == nil || s.db == nil {
		return domaintrace.APICandidateValidationResult{}, false, fmt.Errorf("browser trace sqlite store is closed")
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM api_candidate_validation WHERE validation_id = ?`, validationID).Scan(&payload)
	if err != nil {
		if err == sql.ErrNoRows {
			return domaintrace.APICandidateValidationResult{}, false, nil
		}
		return domaintrace.APICandidateValidationResult{}, false, err
	}
	var item domaintrace.APICandidateValidationResult
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domaintrace.APICandidateValidationResult{}, false, err
	}
	return item, true, nil
}

func (s *SQLiteStore) SaveAPICoverageReport(ctx context.Context, item domaintrace.APICoverageReport) error {
	if err := domaintrace.ValidateAPICoverageReport(item); err != nil {
		return err
	}
	return s.saveOwned(ctx, "api_coverage_report", "artifact_id", string(item.ArtifactID), item.RunID, item.CreatedAt.Format(timeFormatRFC3339Nano), item)
}

func (s *SQLiteStore) ListAPICoverageReports(ctx context.Context, limit int) ([]domaintrace.APICoverageReport, error) {
	return listSQLiteItems[domaintrace.APICoverageReport](ctx, s, "api_coverage_report", limit)
}

// SaveAPIArtifact writes one APIArtifact row. Basic validation is kept, and the
// write then runs on one reserved connection inside one BEGIN IMMEDIATE
// transaction, serialized with SupersedeAPIArtifact, so the row it reads is the
// row it overwrites: a Save that read the row before a supersede cannot commit
// afterwards and erase that edge.
//
// The supersession edge belongs to SupersedeAPIArtifact, so an ordinary Save may
// only carry the edge exactly as stored. A brand new row may not carry one at all,
// because the successor may not exist and nothing has been checked, and a stored
// row may not have its edge added, moved or cleared here. An existing artifact_id
// may not be moved into another task, run, actor, workstream, content role or
// kind either, because that would invalidate a chain that was already checked.
// Everything else stays the owner's normal in-place update: title, status, body
// and their digest are written as given, and a row that cannot be read back fails
// closed instead of being silently replaced. There is no global immutability rule.
func (s *SQLiteStore) SaveAPIArtifact(ctx context.Context, item domaintrace.APIArtifact) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	if s == nil || s.db == nil {
		return fmt.Errorf("browser trace sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	return s.withArtifactTransaction(ctx, "artifact save", func(ctx context.Context, conn *sql.Conn) error {
		stored, found, err := findAPIArtifactOnConn(ctx, conn, item.ArtifactID)
		if err != nil {
			return err
		}
		if !found {
			if item.SupersededBy != "" {
				return fmt.Errorf("artifact %s is new with superseded_by %s: only SupersedeAPIArtifact may establish an edge", item.ArtifactID, item.SupersededBy)
			}
		} else if err := domaintrace.ValidateAPIArtifactScope(stored, item); err != nil {
			return fmt.Errorf("save of artifact %s: %w", item.ArtifactID, err)
		} else if item.SupersededBy != stored.SupersededBy {
			return fmt.Errorf("artifact %s superseded_by %q does not match the stored edge %q: only SupersedeAPIArtifact may add, move or clear that edge", item.ArtifactID, item.SupersededBy, stored.SupersededBy)
		}
		if _, err := conn.ExecContext(ctx,
			`INSERT OR REPLACE INTO api_artifact (artifact_id, run_id, created_at, payload) VALUES (?, ?, ?, ?)`,
			string(item.ArtifactID), string(item.RunID), item.CreatedAt.Format(timeFormatRFC3339Nano), string(payload)); err != nil {
			return fmt.Errorf("write artifact %s: %w", item.ArtifactID, err)
		}
		return nil
	})
}

func (s *SQLiteStore) ListAPIArtifacts(ctx context.Context, limit int) ([]domaintrace.APIArtifact, error) {
	return listSQLiteItems[domaintrace.APIArtifact](ctx, s, "api_artifact", limit)
}

// SupersedeAPIArtifact establishes the edge that says an existing APIArtifact was
// replaced by an existing successor. It is the only browsertrace SQLite operation
// allowed to create that edge, because only here can both rows be read and checked
// before the write. The reservation, every read, the checks and the single UPDATE
// all run on one reserved connection inside one transaction: the connection is
// never returned to the pool between BEGIN and COMMIT, so a second supersede
// cannot interleave and observe a half-applied edge, and any error path rolls back
// and releases it. Neither the predecessor's identity, content role, body, digest
// and created_at nor the successor row are rewritten.
func (s *SQLiteStore) SupersedeAPIArtifact(ctx context.Context, predecessorID, successorID modulecore.ArtifactID) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("browser trace sqlite store is closed")
	}
	if err := modulecore.ValidateArtifactSupersession(predecessorID, successorID); err != nil {
		return err
	}
	return s.withArtifactTransaction(ctx, "supersede", func(ctx context.Context, conn *sql.Conn) error {
		predecessor, err := loadAPIArtifactOnConn(ctx, conn, predecessorID)
		if err != nil {
			return err
		}
		successor, err := loadAPIArtifactOnConn(ctx, conn, successorID)
		if err != nil {
			return err
		}
		// The predecessor and the successor are two distinct artifacts that have to
		// agree on task, run, actor, workstream, content role and stored kind, and
		// every row below the successor has to stay verifiable and in that same scope,
		// because the new edge joins that chain. Nothing is written before all of it
		// has been read on this one reserved connection.
		if err := domaintrace.ValidateAPIArtifactSupersessionPair(predecessor, successor); err != nil {
			return err
		}
		if err := verifyAPIArtifactSuccessorChain(ctx, func(ctx context.Context, id modulecore.ArtifactID) (domaintrace.APIArtifact, error) {
			return loadAPIArtifactOnConn(ctx, conn, id)
		}, predecessorID, successor); err != nil {
			return err
		}

		switch {
		case predecessor.SupersededBy == successorID:
			// The requested edge is already there, so repeating the call is idempotent.
			return nil
		case predecessor.SupersededBy != "":
			return fmt.Errorf("artifact %s is already superseded by %s, not %s", predecessorID, predecessor.SupersededBy, successorID)
		}

		updated := predecessor
		updated.SupersededBy = successorID
		if err := domaintrace.ValidateAPIArtifact(updated); err != nil {
			return fmt.Errorf("supersede of artifact %s leaves an invalid row: %w", predecessorID, err)
		}
		payload, err := json.Marshal(updated)
		if err != nil {
			return fmt.Errorf("encode superseded payload for artifact %s: %w", predecessorID, err)
		}
		if _, err := conn.ExecContext(ctx, `UPDATE api_artifact SET payload = ? WHERE artifact_id = ?`, string(payload), string(predecessorID)); err != nil {
			return fmt.Errorf("write supersede edge for artifact %s: %w", predecessorID, err)
		}
		return nil
	})
}

// loadAPIArtifactOnConn reads one artifact row on the reserved connection. A
// missing row, a payload that belongs to another artifact and a payload that will
// not decode are all errors: a row that cannot be verified cannot be superseded.
// loadAPIArtifactOnConn reads one artifact row on the reserved connection and
// treats a missing row as an error.
func loadAPIArtifactOnConn(ctx context.Context, conn *sql.Conn, id modulecore.ArtifactID) (domaintrace.APIArtifact, error) {
	item, found, err := findAPIArtifactOnConn(ctx, conn, id)
	if err != nil {
		return domaintrace.APIArtifact{}, err
	}
	if !found {
		return domaintrace.APIArtifact{}, fmt.Errorf("artifact %s is not in api_artifact", id)
	}
	return item, nil
}

// findAPIArtifactOnConn reads one artifact row on the reserved connection and
// reports whether it exists. A row that is there but cannot be verified is an
// error, never a miss: the caller has to fail closed on it rather than treat it
// as a free artifact_id.
func findAPIArtifactOnConn(ctx context.Context, conn *sql.Conn, id modulecore.ArtifactID) (domaintrace.APIArtifact, bool, error) {
	var payload, runIDColumn string
	if err := conn.QueryRowContext(ctx, `SELECT payload, run_id FROM api_artifact WHERE artifact_id = ?`, string(id)).Scan(&payload, &runIDColumn); err != nil {
		if err == sql.ErrNoRows {
			return domaintrace.APIArtifact{}, false, nil
		}
		return domaintrace.APIArtifact{}, false, fmt.Errorf("read artifact %s: %w", id, err)
	}
	var item domaintrace.APIArtifact
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domaintrace.APIArtifact{}, false, fmt.Errorf("artifact %s payload is corrupt: %w", id, err)
	}
	if item.ArtifactID != id {
		return domaintrace.APIArtifact{}, false, fmt.Errorf("artifact %s payload holds artifact_id %s", id, item.ArtifactID)
	}
	// The indexed run_id column and the run inside the payload must name the same
	// run, otherwise a chain check that reads one would disagree with a reader of
	// the other, so the row fails closed rather than being quietly accepted.
	if runIDColumn != string(item.RunID) {
		return domaintrace.APIArtifact{}, false, fmt.Errorf("artifact %s run_id column %s disagrees with payload run_id %s", id, runIDColumn, item.RunID)
	}
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return domaintrace.APIArtifact{}, false, fmt.Errorf("artifact %s row is invalid: %w", id, err)
	}
	return item, true, nil
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

func (s *SQLiteStore) saveOwned(ctx context.Context, table, idColumn, id string, runID modulecore.RunID, createdAt string, item any) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("browser trace sqlite store is closed")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return err
	}
	query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, run_id, created_at, payload) VALUES (?, ?, ?, ?)`, table, idColumn)
	_, err = s.db.ExecContext(ctx, query, id, string(runID), createdAt, string(payload))
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
