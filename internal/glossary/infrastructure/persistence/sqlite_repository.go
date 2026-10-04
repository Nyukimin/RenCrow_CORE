package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	_ "modernc.org/sqlite"
)

const MaxGlossaryViewerPageTotal int64 = 1<<31 - 1

type SQLiteGlossaryRepository struct {
	db *sql.DB
}

// GlossaryOperationIdentity binds one storage-host op_id to one exact owner
// mutation. The receipt is committed in the same SQLite transaction as the
// glossary row so a restarted host can distinguish commit from non-commit.
type GlossaryOperationIdentity struct {
	OpID        string
	Operation   string
	PayloadHash string
}

type GlossaryOperationReceipt struct {
	Identity   GlossaryOperationIdentity
	ResultJSON json.RawMessage
}

var (
	ErrGlossaryOperationConflict = errors.New("glossary operation identity conflict")
	glossaryOperationIDPattern   = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
	glossaryPayloadHashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

func NewSQLiteGlossaryRepository(dbPath string) (*SQLiteGlossaryRepository, error) {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout%3d5000&_time_format=sqlite")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := createTables(db); err != nil {
		return nil, err
	}

	return &SQLiteGlossaryRepository{db: db}, nil
}

func createTables(db *sql.DB) error {
	query := `
	CREATE TABLE IF NOT EXISTS glossary_items (
		id TEXT PRIMARY KEY,
		term TEXT NOT NULL,
		explanation TEXT NOT NULL,
		source TEXT NOT NULL,
		category TEXT NOT NULL,
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_term ON glossary_items(term);
	CREATE INDEX IF NOT EXISTS idx_category ON glossary_items(category);
	CREATE INDEX IF NOT EXISTS idx_created_at ON glossary_items(created_at);
	CREATE TABLE IF NOT EXISTS glossary_candidates (
		id TEXT PRIMARY KEY,
		term TEXT NOT NULL,
		explanation TEXT NOT NULL,
		source_url TEXT NOT NULL,
		category TEXT NOT NULL,
		proposed_by TEXT NOT NULL,
		state TEXT NOT NULL,
		created_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_glossary_candidates_category ON glossary_candidates(category);
	CREATE INDEX IF NOT EXISTS idx_glossary_candidates_created_at ON glossary_candidates(created_at);
	CREATE TABLE IF NOT EXISTS glossary_storage_host_receipts (
		op_id TEXT PRIMARY KEY,
		operation TEXT NOT NULL,
		payload_hash TEXT NOT NULL,
		result_json TEXT NOT NULL
	);
	`
	_, err := db.Exec(query)
	return err
}

func validateGlossaryOperationIdentity(identity GlossaryOperationIdentity) error {
	if !glossaryOperationIDPattern.MatchString(identity.OpID) || !glossaryPayloadHashPattern.MatchString(identity.PayloadHash) {
		return errors.New("glossary operation identity is invalid")
	}
	switch identity.Operation {
	case "save", "delete", "save_candidate":
		return nil
	default:
		return errors.New("glossary operation is unsupported")
	}
}

func (r *SQLiteGlossaryRepository) SaveForStorageHostOperation(ctx context.Context, identity GlossaryOperationIdentity, item *entity.GlossaryItem) error {
	if identity.Operation != "save" {
		return errors.New("glossary save identity operation mismatch")
	}
	if item == nil {
		return errors.New("glossary item is nil")
	}
	return r.applyStorageHostOperation(ctx, identity, func(tx *sql.Tx) (json.RawMessage, error) {
		_, err := tx.ExecContext(ctx, `
			INSERT OR REPLACE INTO glossary_items (id, term, explanation, source, category, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, item.ID, item.Term, item.Explanation, item.Source, item.Category, item.CreatedAt, item.UpdatedAt)
		return json.RawMessage("null"), err
	})
}

func (r *SQLiteGlossaryRepository) DeleteForStorageHostOperation(ctx context.Context, identity GlossaryOperationIdentity, id string) error {
	if identity.Operation != "delete" {
		return errors.New("glossary delete identity operation mismatch")
	}
	return r.applyStorageHostOperation(ctx, identity, func(tx *sql.Tx) (json.RawMessage, error) {
		_, err := tx.ExecContext(ctx, `DELETE FROM glossary_items WHERE id = ?`, id)
		return json.RawMessage("null"), err
	})
}

func (r *SQLiteGlossaryRepository) SaveCandidateForStorageHostOperation(ctx context.Context, identity GlossaryOperationIdentity, candidate entity.GlossaryCandidate) error {
	if identity.Operation != "save_candidate" {
		return errors.New("glossary candidate identity operation mismatch")
	}
	if err := entity.ValidateGlossaryCandidate(candidate); err != nil {
		return err
	}
	return r.applyStorageHostOperation(ctx, identity, func(tx *sql.Tx) (json.RawMessage, error) {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO glossary_candidates
				(id, term, explanation, source_url, category, proposed_by, state, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		`, candidate.ID, candidate.Term, candidate.Explanation, candidate.SourceURL, candidate.Category, candidate.ProposedBy, candidate.State, candidate.CreatedAt)
		return json.RawMessage("null"), err
	})
}

func (r *SQLiteGlossaryRepository) applyStorageHostOperation(ctx context.Context, identity GlossaryOperationIdentity, apply func(*sql.Tx) (json.RawMessage, error)) error {
	if r == nil || r.db == nil {
		return sql.ErrConnDone
	}
	if err := validateGlossaryOperationIdentity(identity); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin glossary owner operation: %w", err)
	}
	defer tx.Rollback()
	var priorOperation, priorHash, priorResult string
	err = tx.QueryRowContext(ctx, `SELECT operation, payload_hash, result_json FROM glossary_storage_host_receipts WHERE op_id = ?`, identity.OpID).Scan(&priorOperation, &priorHash, &priorResult)
	if err == nil {
		if priorOperation != identity.Operation || priorHash != identity.PayloadHash || !json.Valid([]byte(priorResult)) {
			return fmt.Errorf("%w: op_id %q", ErrGlossaryOperationConflict, identity.OpID)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("read glossary owner operation receipt: %w", err)
	}
	result, err := apply(tx)
	if err != nil {
		return glossaryRolledBackError{inner: err}
	}
	if len(result) == 0 || len(result) > 64<<10 || !json.Valid(result) {
		return glossaryRolledBackError{inner: errors.New("glossary owner operation result is invalid")}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO glossary_storage_host_receipts (op_id, operation, payload_hash, result_json) VALUES (?, ?, ?, ?)`, identity.OpID, identity.Operation, identity.PayloadHash, string(result)); err != nil {
		return glossaryRolledBackError{inner: fmt.Errorf("insert glossary owner operation receipt: %w", err)}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit glossary owner operation: %w", err)
	}
	return nil
}

type glossaryRolledBackError struct{ inner error }

func (e glossaryRolledBackError) Error() string    { return e.inner.Error() }
func (e glossaryRolledBackError) Unwrap() error    { return e.inner }
func (e glossaryRolledBackError) RolledBack() bool { return true }

func (r *SQLiteGlossaryRepository) LookupStorageHostOperationReceipt(ctx context.Context, identity GlossaryOperationIdentity) (GlossaryOperationReceipt, bool, error) {
	if r == nil || r.db == nil {
		return GlossaryOperationReceipt{}, false, sql.ErrConnDone
	}
	if !glossaryOperationIDPattern.MatchString(identity.OpID) {
		return GlossaryOperationReceipt{}, false, errors.New("glossary operation id is invalid")
	}
	var receipt GlossaryOperationReceipt
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT operation, payload_hash, result_json FROM glossary_storage_host_receipts WHERE op_id = ?`, identity.OpID).Scan(&receipt.Identity.Operation, &receipt.Identity.PayloadHash, &raw)
	if errors.Is(err, sql.ErrNoRows) {
		return GlossaryOperationReceipt{}, false, nil
	}
	if err != nil {
		return GlossaryOperationReceipt{}, false, err
	}
	receipt.Identity.OpID = identity.OpID
	if len(raw) == 0 || len(raw) > 64<<10 || !json.Valid([]byte(raw)) {
		return GlossaryOperationReceipt{}, false, errors.New("glossary operation receipt result is malformed")
	}
	receipt.ResultJSON = json.RawMessage(raw)
	return receipt, true, nil
}

func (r *SQLiteGlossaryRepository) LookupStorageHost(ctx context.Context, request glossaryapp.LookupRequest) (glossaryapp.LookupResult, error) {
	if r == nil || r.db == nil {
		return glossaryapp.LookupResult{}, sql.ErrConnDone
	}
	return glossaryapp.Lookup(ctx, r.db, request)
}

// SaveCandidate persists one model-proposed candidate without touching the
// canonical glossary_items table. INSERT-only semantics make accidental
// candidate replacement visible to the owner route.
func (r *SQLiteGlossaryRepository) SaveCandidate(ctx context.Context, candidate entity.GlossaryCandidate) error {
	if r == nil || r.db == nil {
		return sql.ErrConnDone
	}
	if err := entity.ValidateGlossaryCandidate(candidate); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `
		INSERT INTO glossary_candidates
			(id, term, explanation, source_url, category, proposed_by, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, candidate.ID, candidate.Term, candidate.Explanation, candidate.SourceURL, candidate.Category, candidate.ProposedBy, candidate.State, candidate.CreatedAt)
	return err
}

// FindCandidateByID performs an exact primary-key lookup and validates the
// complete stored row before exposing it to an owner route.
func (r *SQLiteGlossaryRepository) FindCandidateByID(ctx context.Context, id string) (entity.GlossaryCandidate, bool, error) {
	if r == nil || r.db == nil {
		return entity.GlossaryCandidate{}, false, sql.ErrConnDone
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return entity.GlossaryCandidate{}, false, fmt.Errorf("candidate id is required")
	}
	var candidate entity.GlossaryCandidate
	err := r.db.QueryRowContext(ctx, `
		SELECT id, term, explanation, source_url, category, proposed_by, state, created_at
		FROM glossary_candidates WHERE id = ?
	`, id).Scan(
		&candidate.ID, &candidate.Term, &candidate.Explanation, &candidate.SourceURL,
		&candidate.Category, &candidate.ProposedBy, &candidate.State, &candidate.CreatedAt,
	)
	if err == sql.ErrNoRows {
		return entity.GlossaryCandidate{}, false, nil
	}
	if err != nil {
		return entity.GlossaryCandidate{}, false, err
	}
	candidate.CreatedAt = candidate.CreatedAt.UTC()
	if candidate.ID != id {
		return entity.GlossaryCandidate{}, false, fmt.Errorf("candidate row id mismatch")
	}
	if err := entity.ValidateGlossaryCandidate(candidate); err != nil {
		return entity.GlossaryCandidate{}, false, fmt.Errorf("stored candidate is invalid: %w", err)
	}
	return candidate, true, nil
}

func (r *SQLiteGlossaryRepository) Save(ctx context.Context, item *entity.GlossaryItem) error {
	query := `
	INSERT OR REPLACE INTO glossary_items 
	(id, term, explanation, source, category, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	`
	_, err := r.db.ExecContext(ctx, query,
		item.ID,
		item.Term,
		item.Explanation,
		item.Source,
		item.Category,
		item.CreatedAt,
		item.UpdatedAt,
	)
	return err
}

func (r *SQLiteGlossaryRepository) FindByTerm(ctx context.Context, term string) (*entity.GlossaryItem, error) {
	query := `SELECT id, term, explanation, source, category, created_at, updated_at 
	          FROM glossary_items WHERE term = ? LIMIT 1`
	row := r.db.QueryRowContext(ctx, query, term)
	return scanGlossaryItem(row)
}

func (r *SQLiteGlossaryRepository) FindRecent(ctx context.Context, limit int) ([]*entity.GlossaryItem, error) {
	query := `SELECT id, term, explanation, source, category, created_at, updated_at 
	          FROM glossary_items ORDER BY created_at DESC, rowid DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanGlossaryItems(rows)
}

func (r *SQLiteGlossaryRepository) FindViewerPage(ctx context.Context, limit int) (int, []*entity.GlossaryItem, error) {
	if r == nil || r.db == nil {
		return 0, nil, errors.New("glossary database is unavailable")
	}
	if limit < 1 || limit > 200 {
		return 0, nil, errors.New("glossary Viewer page limit is invalid")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var total int64
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM glossary_items`).Scan(&total); err != nil {
		return 0, nil, err
	}
	if total < 0 || total > MaxGlossaryViewerPageTotal {
		return 0, nil, errors.New("glossary Viewer page total is out of bounds")
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, term, explanation, source, category, created_at, updated_at
		FROM glossary_items ORDER BY created_at DESC, rowid DESC LIMIT ?`, limit)
	if err != nil {
		return 0, nil, err
	}
	items, scanErr := scanGlossaryItems(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return 0, nil, scanErr
	}
	if closeErr != nil {
		return 0, nil, closeErr
	}
	if err := tx.Commit(); err != nil {
		return 0, nil, err
	}
	if items == nil {
		items = []*entity.GlossaryItem{}
	}
	return int(total), items, nil
}

func (r *SQLiteGlossaryRepository) FindByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	query := `SELECT id, term, explanation, source, category, created_at, updated_at 
	          FROM glossary_items WHERE category = ? ORDER BY created_at DESC, rowid DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, category, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanGlossaryItems(rows)
}

func (r *SQLiteGlossaryRepository) Delete(ctx context.Context, id string) error {
	query := `DELETE FROM glossary_items WHERE id = ?`
	_, err := r.db.ExecContext(ctx, query, id)
	return err
}

func (r *SQLiteGlossaryRepository) Close() error {
	if r == nil || r.db == nil {
		return nil
	}
	return r.db.Close()
}

func scanGlossaryItem(row *sql.Row) (*entity.GlossaryItem, error) {
	var item entity.GlossaryItem
	err := row.Scan(
		&item.ID,
		&item.Term,
		&item.Explanation,
		&item.Source,
		&item.Category,
		&item.CreatedAt,
		&item.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	return &item, nil
}

func scanGlossaryItems(rows *sql.Rows) ([]*entity.GlossaryItem, error) {
	var items []*entity.GlossaryItem
	for rows.Next() {
		var item entity.GlossaryItem
		err := rows.Scan(
			&item.ID,
			&item.Term,
			&item.Explanation,
			&item.Source,
			&item.Category,
			&item.CreatedAt,
			&item.UpdatedAt,
		)
		if err != nil {
			return nil, err
		}
		items = append(items, &item)
	}
	return items, nil
}
