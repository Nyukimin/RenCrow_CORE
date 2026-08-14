package persistence

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/glossary/domain/entity"
	_ "modernc.org/sqlite"
)

type SQLiteGlossaryRepository struct {
	db *sql.DB
}

func NewSQLiteGlossaryRepository(dbPath string) (*SQLiteGlossaryRepository, error) {
	db, err := sql.Open("sqlite", dbPath+"?_time_format=sqlite")
	if err != nil {
		return nil, err
	}

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
	`
	_, err := db.Exec(query)
	return err
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
