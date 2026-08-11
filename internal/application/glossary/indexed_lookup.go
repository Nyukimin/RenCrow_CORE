package glossary

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

type Item struct {
	ID          string `json:"id"`
	Term        string `json:"term"`
	Explanation string `json:"explanation"`
	Source      string `json:"source"`
	Category    string `json:"category"`
	UpdatedAt   string `json:"updated_at"`
}

type LookupRequest struct {
	Operation, Term, Category string
	Limit                     int
}
type LookupResult struct {
	Operation string `json:"operation"`
	Items     []Item `json:"items"`
}

func ValidateIndexedSchema(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("glossary database is nil")
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='glossary_items'`).Scan(&tableCount); err != nil || tableCount != 1 {
		return fmt.Errorf("glossary_items table is unavailable")
	}
	for _, index := range []string{"idx_term", "idx_category"} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name=? AND tbl_name='glossary_items'`, index).Scan(&count); err != nil || count != 1 {
			return fmt.Errorf("required glossary index %s is unavailable", index)
		}
	}
	return nil
}

func Lookup(ctx context.Context, db *sql.DB, req LookupRequest) (LookupResult, error) {
	if err := ValidateIndexedSchema(ctx, db); err != nil {
		return LookupResult{}, err
	}
	limit := req.Limit
	if limit == 0 {
		limit = 10
	}
	if limit < 1 || limit > 20 {
		return LookupResult{}, fmt.Errorf("limit must be between 1 and 20")
	}
	op := strings.TrimSpace(req.Operation)
	var rows *sql.Rows
	var err error
	switch op {
	case "define_term":
		term := strings.TrimSpace(req.Term)
		if term == "" || strings.TrimSpace(req.Category) != "" {
			return LookupResult{}, fmt.Errorf("define_term requires only term")
		}
		rows, err = db.QueryContext(ctx, `SELECT id,term,explanation,source,category,CAST(updated_at AS TEXT) FROM glossary_items INDEXED BY idx_term WHERE term=? ORDER BY id LIMIT ?`, term, limit)
	case "list_category":
		category := strings.TrimSpace(req.Category)
		if category == "" || strings.TrimSpace(req.Term) != "" {
			return LookupResult{}, fmt.Errorf("list_category requires only category")
		}
		rows, err = db.QueryContext(ctx, `SELECT id,term,explanation,source,category,CAST(updated_at AS TEXT) FROM glossary_items INDEXED BY idx_category WHERE category=? ORDER BY id LIMIT ?`, category, limit)
	default:
		return LookupResult{}, fmt.Errorf("unsupported glossary operation")
	}
	if err != nil {
		return LookupResult{}, err
	}
	defer rows.Close()
	result := LookupResult{Operation: op, Items: []Item{}}
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Term, &item.Explanation, &item.Source, &item.Category, &item.UpdatedAt); err != nil {
			return LookupResult{}, err
		}
		result.Items = append(result.Items, item)
	}
	return result, rows.Err()
}
