package persistence

import (
	"context"
	"database/sql"

	_ "github.com/mattn/go-sqlite3"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
)

type SQLiteGlossaryRepository struct {
	db *sql.DB
}

func NewSQLiteGlossaryRepository(dbPath string) (*SQLiteGlossaryRepository, error) {
	db, err := sql.Open("sqlite3", dbPath)
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
	`
	_, err := db.Exec(query)
	return err
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
	          FROM glossary_items ORDER BY created_at DESC LIMIT ?`
	rows, err := r.db.QueryContext(ctx, query, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return scanGlossaryItems(rows)
}

func (r *SQLiteGlossaryRepository) FindByCategory(ctx context.Context, category string, limit int) ([]*entity.GlossaryItem, error) {
	query := `SELECT id, term, explanation, source, category, created_at, updated_at 
	          FROM glossary_items WHERE category = ? ORDER BY created_at DESC LIMIT ?`
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
