package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	moviecatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/moviecatalog"
	_ "modernc.org/sqlite"
)

type runtimeMovieCatalogLookup struct {
	dbPath string
}

// runtimeMoviePreferenceCandidate is the owner-owned projection shared by the
// candidate writer and the two private recall routes. The model never sees
// the database path or this storage shape directly.
type runtimeMoviePreferenceCandidate struct {
	ID          string
	RequestID   string
	UserID      string
	ActorID     string
	PayloadHash string
	TargetKind  string
	TargetID    string
	Familiarity string
	Sentiment   string
	Note        string
	State       string
	CreatedAt   string
}

// prepareRuntimeMovieCatalogLookup is the sole schema-migration boundary for
// the runtime Tool. Missing or unmigratable optional DBs return no capability.
func prepareRuntimeMovieCatalogLookup(ctx context.Context, configuredPath string) (*runtimeMovieCatalogLookup, error) {
	configuredPath = strings.TrimSpace(configuredPath)
	if configuredPath == "" {
		return nil, fmt.Errorf("movie catalog database path is not configured")
	}
	absPath, err := filepath.Abs(configuredPath)
	if err != nil {
		return nil, fmt.Errorf("resolve movie catalog database path: %w", err)
	}
	info, err := os.Stat(absPath)
	if err != nil {
		return nil, fmt.Errorf("stat movie catalog database: %w", err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("movie catalog database path is a directory")
	}
	db, err := sql.Open("sqlite", "file:"+absPath+"?_time_format=sqlite&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("open movie catalog database for indexed migration: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog database for indexed migration: %w", err)
	}
	if err := moviecatalogapp.EnsureIndexedLookupSchema(ctx, db); err != nil {
		return nil, fmt.Errorf("prepare movie catalog indexed lookup: %w", err)
	}
	return &runtimeMovieCatalogLookup{dbPath: absPath}, nil
}

func (l *runtimeMovieCatalogLookup) Lookup(ctx context.Context, kind string, name string, information string, limit int) (any, error) {
	if l == nil || strings.TrimSpace(l.dbPath) == "" {
		return nil, fmt.Errorf("movie catalog lookup is unavailable")
	}
	db, err := openRuntimeMovieCatalogReadOnly(l.dbPath)
	if err != nil {
		return nil, fmt.Errorf("open movie catalog read-only: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return nil, fmt.Errorf("connect movie catalog read-only: %w", err)
	}
	// The data.recall owner route requests the complete safe catalog projection
	// with the fixed semantic value "all". The application lookup contract uses
	// an empty information selector for that same projection.
	if strings.EqualFold(strings.TrimSpace(information), "all") {
		information = ""
	}
	return moviecatalogapp.Lookup(db, moviecatalogapp.LookupRequest{Kind: kind, Name: name, Information: information, Limit: limit})
}

func openRuntimeMovieCatalogReadOnly(dbPath string) (*sql.DB, error) {
	// The movie catalog runs journal_mode=delete, so a concurrent import
	// write-locks readers; wait briefly instead of surfacing SQLITE_BUSY.
	return sql.Open("sqlite", "file:"+dbPath+"?mode=ro&_time_format=sqlite&_pragma=busy_timeout(5000)")
}

// ensureRuntimeMoviePreferenceCandidateSchema is the owner initialization
// boundary for private movie preference proposals. It never touches canonical
// movie/person or selection tables.
func (l *runtimeMovieCatalogLookup) ensureRuntimeMoviePreferenceCandidateSchema(ctx context.Context) error {
	if l == nil || strings.TrimSpace(l.dbPath) == "" {
		return fmt.Errorf("movie catalog preference candidate is unavailable")
	}
	db, err := openRuntimePersonRelatedCatalogReadWrite(l.dbPath)
	if err != nil {
		return fmt.Errorf("open movie catalog for preference candidate schema: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return fmt.Errorf("connect movie catalog for preference candidate schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS movie_preference_candidate (
  id TEXT PRIMARY KEY,
  request_id TEXT NOT NULL UNIQUE,
  user_id TEXT NOT NULL,
  actor_id TEXT NOT NULL,
  payload_hash TEXT NOT NULL,
  target_kind TEXT NOT NULL CHECK(target_kind IN ('movie','person')),
  target_id TEXT NOT NULL,
  familiarity TEXT NOT NULL DEFAULT '',
  sentiment TEXT NOT NULL DEFAULT '',
  note TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL DEFAULT 'candidate' CHECK(state = 'candidate'),
  created_at TEXT NOT NULL
)`); err != nil {
		return fmt.Errorf("create movie preference candidate table: %w", err)
	}
	for _, statement := range []string{
		`CREATE INDEX IF NOT EXISTS idx_movie_preference_candidate_user_id ON movie_preference_candidate(user_id, id)`,
		`CREATE INDEX IF NOT EXISTS idx_movie_preference_candidate_request_id ON movie_preference_candidate(request_id)`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create movie preference candidate index: %w", err)
		}
	}
	return nil
}

// insertRuntimeMoviePreferenceCandidate performs the candidate insert and
// request lookup in one SQLite transaction. Existing rows are returned to the
// adapter for binding comparison; no update or replace is ever performed.
func (l *runtimeMovieCatalogLookup) insertRuntimeMoviePreferenceCandidate(ctx context.Context, candidate runtimeMoviePreferenceCandidate) (runtimeMoviePreferenceCandidate, bool, error) {
	if l == nil || strings.TrimSpace(l.dbPath) == "" {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("movie catalog preference candidate is unavailable")
	}
	db, err := openRuntimePersonRelatedCatalogReadWrite(l.dbPath)
	if err != nil {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("open movie catalog for preference candidate write: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("connect movie catalog for preference candidate write: %w", err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("begin movie preference candidate transaction: %w", err)
	}
	rollback := func(cause error) (runtimeMoviePreferenceCandidate, bool, error) {
		_ = tx.Rollback()
		return runtimeMoviePreferenceCandidate{}, false, cause
	}

	if existing, found, err := queryRuntimeMoviePreferenceCandidateTx(ctx, tx, "request_id", candidate.RequestID); err != nil {
		return rollback(err)
	} else if found {
		if err := tx.Rollback(); err != nil {
			return runtimeMoviePreferenceCandidate{}, false, err
		}
		return existing, true, nil
	}
	if existing, found, err := queryRuntimeMoviePreferenceCandidateTx(ctx, tx, "id", candidate.ID); err != nil {
		return rollback(err)
	} else if found {
		if err := tx.Rollback(); err != nil {
			return runtimeMoviePreferenceCandidate{}, false, err
		}
		return existing, true, nil
	}
	targetTable, targetColumn := "movies", "movie_id"
	if candidate.TargetKind == "person" {
		targetTable, targetColumn = "people", "person_id"
	}
	var targetCount int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+targetTable+" WHERE "+targetColumn+" = ?", candidate.TargetID).Scan(&targetCount); err != nil {
		return rollback(fmt.Errorf("verify movie catalog preference target: %w", err))
	}
	if targetCount != 1 {
		return rollback(fmt.Errorf("movie catalog preference target %q/%q is not found", candidate.TargetKind, candidate.TargetID))
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO movie_preference_candidate
  (id,request_id,user_id,actor_id,payload_hash,target_kind,target_id,familiarity,sentiment,note,state,created_at)
VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		candidate.ID, candidate.RequestID, candidate.UserID, candidate.ActorID, candidate.PayloadHash,
		candidate.TargetKind, candidate.TargetID, candidate.Familiarity, candidate.Sentiment,
		candidate.Note, candidate.State, candidate.CreatedAt); err != nil {
		return rollback(fmt.Errorf("insert movie preference candidate: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("commit movie preference candidate: %w", err)
	}
	return candidate, false, nil
}

func queryRuntimeMoviePreferenceCandidateTx(ctx context.Context, tx *sql.Tx, column, value string) (runtimeMoviePreferenceCandidate, bool, error) {
	if column != "id" && column != "request_id" {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("unsupported movie preference candidate lookup column")
	}
	row := tx.QueryRowContext(ctx, `
SELECT id,request_id,user_id,actor_id,payload_hash,target_kind,target_id,familiarity,sentiment,note,state,created_at
FROM movie_preference_candidate WHERE `+column+` = ?`, value)
	candidate, err := scanRuntimeMoviePreferenceCandidate(row)
	if err == sql.ErrNoRows {
		return runtimeMoviePreferenceCandidate{}, false, nil
	}
	if err != nil {
		return runtimeMoviePreferenceCandidate{}, false, err
	}
	return candidate, true, nil
}

func (l *runtimeMovieCatalogLookup) findRuntimeMoviePreferenceCandidateByID(ctx context.Context, userID, candidateID string) (runtimeMoviePreferenceCandidate, bool, error) {
	return l.findRuntimeMoviePreferenceCandidate(ctx, userID, "id", candidateID)
}

func (l *runtimeMovieCatalogLookup) findRuntimeMoviePreferenceCandidateByRequestID(ctx context.Context, userID, requestID string) (runtimeMoviePreferenceCandidate, bool, error) {
	return l.findRuntimeMoviePreferenceCandidate(ctx, userID, "request_id", requestID)
}

func (l *runtimeMovieCatalogLookup) findRuntimeMoviePreferenceCandidate(ctx context.Context, userID, column, value string) (runtimeMoviePreferenceCandidate, bool, error) {
	if l == nil || strings.TrimSpace(l.dbPath) == "" {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("movie catalog preference candidate is unavailable")
	}
	if column != "id" && column != "request_id" {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("unsupported movie preference candidate lookup column")
	}
	db, err := openRuntimeMovieCatalogReadOnly(l.dbPath)
	if err != nil {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("open movie catalog for preference candidate recall: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := db.PingContext(ctx); err != nil {
		return runtimeMoviePreferenceCandidate{}, false, fmt.Errorf("connect movie catalog for preference candidate recall: %w", err)
	}
	row := db.QueryRowContext(ctx, `
SELECT id,request_id,user_id,actor_id,payload_hash,target_kind,target_id,familiarity,sentiment,note,state,created_at
FROM movie_preference_candidate WHERE `+column+` = ? AND user_id = ?`, value, userID)
	candidate, err := scanRuntimeMoviePreferenceCandidate(row)
	if err == sql.ErrNoRows {
		return runtimeMoviePreferenceCandidate{}, false, nil
	}
	if err != nil {
		return runtimeMoviePreferenceCandidate{}, false, err
	}
	return candidate, true, nil
}

type runtimeMoviePreferenceCandidateRow interface {
	Scan(...any) error
}

func scanRuntimeMoviePreferenceCandidate(row runtimeMoviePreferenceCandidateRow) (runtimeMoviePreferenceCandidate, error) {
	var candidate runtimeMoviePreferenceCandidate
	err := row.Scan(
		&candidate.ID, &candidate.RequestID, &candidate.UserID, &candidate.ActorID, &candidate.PayloadHash,
		&candidate.TargetKind, &candidate.TargetID, &candidate.Familiarity, &candidate.Sentiment,
		&candidate.Note, &candidate.State, &candidate.CreatedAt,
	)
	return candidate, err
}
