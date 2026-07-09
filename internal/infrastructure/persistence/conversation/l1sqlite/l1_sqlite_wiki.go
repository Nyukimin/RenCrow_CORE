package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

const (
	WikiPageStatusDraft      = "draft"
	WikiPageStatusActive     = "active"
	WikiPageStatusArchived   = "archived"
	WikiPageStatusDeprecated = "deprecated"
)

func (s *L1SQLiteStore) SaveWikiPageIndex(ctx context.Context, item WikiPageIndexItem) (*WikiPageIndexItem, error) {
	normalized, err := normalizeWikiPageIndexItem(item)
	if err != nil {
		return nil, err
	}
	sourceJSON, err := json.Marshal(normalized.SourcePaths)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal wiki page source paths: %w", err)
	}
	relatedJSON, err := json.Marshal(normalized.Related)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal wiki page related paths: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO wiki_page_index (
	page_id, path, title, type, status, owner, canonical_source,
	source_paths_json, related_json, summary, content_hash, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(page_id) DO UPDATE SET
	path = excluded.path,
	title = excluded.title,
	type = excluded.type,
	status = excluded.status,
	owner = excluded.owner,
	canonical_source = excluded.canonical_source,
	source_paths_json = excluded.source_paths_json,
	related_json = excluded.related_json,
	summary = excluded.summary,
	content_hash = excluded.content_hash,
	updated_at = excluded.updated_at
`, normalized.PageID, normalized.Path, normalized.Title, normalized.Type, normalized.Status, normalized.Owner,
		normalized.CanonicalSource, string(sourceJSON), string(relatedJSON), normalized.Summary,
		normalized.ContentHash, normalized.CreatedAt, normalized.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save wiki page index: %w", err)
	}
	if err := s.upsertWikiPageIndexFTS(ctx, normalized); err != nil {
		return nil, err
	}
	return normalized, nil
}

func (s *L1SQLiteStore) SearchWikiPageIndex(ctx context.Context, query string, limit int) ([]WikiPageIndexItem, error) {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("wiki page index query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT w.page_id, w.path, w.title, w.type, w.status, w.owner, w.canonical_source,
       w.source_paths_json, w.related_json, w.summary, w.content_hash, w.created_at, w.updated_at
FROM wiki_page_index_fts f
JOIN wiki_page_index w ON w.page_id = f.page_id
WHERE (
	f.title LIKE ?
	OR f.path LIKE ?
	OR f.canonical_source LIKE ?
	OR f.summary LIKE ?
	OR f.source_text LIKE ?
	OR f.related_text LIKE ?
)
  AND w.status NOT IN (?, ?)
ORDER BY w.updated_at DESC
LIMIT ?
`, LikeQuery(query), LikeQuery(query), LikeQuery(query), LikeQuery(query), LikeQuery(query), LikeQuery(query),
		WikiPageStatusArchived, WikiPageStatusDeprecated, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search wiki page index: %w", err)
	}
	defer rows.Close()
	items, err := scanWikiPageIndexRows(rows)
	if err != nil || len(items) > 0 {
		return items, err
	}
	return s.searchWikiPageIndexByTerms(ctx, query, limit)
}

func (s *L1SQLiteStore) searchWikiPageIndexByTerms(ctx context.Context, query string, limit int) ([]WikiPageIndexItem, error) {
	terms := knowledgeSearchTerms(query)
	if len(terms) == 0 {
		return []WikiPageIndexItem{}, nil
	}
	clauses := make([]string, 0, len(terms))
	args := make([]interface{}, 0, len(terms)*6+3)
	for _, term := range terms {
		clauses = append(clauses, `(f.title LIKE ? OR f.path LIKE ? OR f.canonical_source LIKE ? OR f.summary LIKE ? OR f.source_text LIKE ? OR f.related_text LIKE ?)`)
		like := LikeQuery(term)
		args = append(args, like, like, like, like, like, like)
	}
	args = append(args, WikiPageStatusArchived, WikiPageStatusDeprecated, limit)
	rows, err := s.db.QueryContext(ctx, `
SELECT w.page_id, w.path, w.title, w.type, w.status, w.owner, w.canonical_source,
       w.source_paths_json, w.related_json, w.summary, w.content_hash, w.created_at, w.updated_at
FROM wiki_page_index_fts f
JOIN wiki_page_index w ON w.page_id = f.page_id
WHERE (`+strings.Join(clauses, " OR ")+`)
  AND w.status NOT IN (?, ?)
ORDER BY w.updated_at DESC
LIMIT ?
`, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search wiki page index by terms: %w", err)
	}
	defer rows.Close()
	return scanWikiPageIndexRows(rows)
}

func (s *L1SQLiteStore) upsertWikiPageIndexFTS(ctx context.Context, item *WikiPageIndexItem) error {
	if item == nil {
		return errors.New("wiki page index fts item is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM wiki_page_index_fts WHERE page_id = ?`, item.PageID); err != nil {
		return fmt.Errorf("failed to delete wiki page index fts row: %w", err)
	}
	if _, err := s.db.ExecContext(ctx, `
INSERT INTO wiki_page_index_fts (
	page_id, title, path, type, status, owner, canonical_source, summary, source_text, related_text
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.PageID, item.Title, item.Path, item.Type, item.Status, item.Owner, item.CanonicalSource,
		item.Summary, strings.Join(item.SourcePaths, " "), strings.Join(item.Related, " ")); err != nil {
		return fmt.Errorf("failed to upsert wiki page index fts row: %w", err)
	}
	return nil
}

func normalizeWikiPageIndexItem(item WikiPageIndexItem) (*WikiPageIndexItem, error) {
	item.PageID = strings.TrimSpace(item.PageID)
	item.Path = strings.TrimSpace(item.Path)
	item.Title = strings.TrimSpace(item.Title)
	item.Type = strings.TrimSpace(item.Type)
	item.Status = strings.TrimSpace(item.Status)
	item.Owner = strings.TrimSpace(item.Owner)
	item.CanonicalSource = strings.TrimSpace(item.CanonicalSource)
	item.Summary = strings.TrimSpace(item.Summary)
	item.ContentHash = strings.TrimSpace(item.ContentHash)
	item.SourcePaths = cleanStringSlice(item.SourcePaths)
	item.Related = cleanStringSlice(item.Related)
	if item.PageID == "" {
		return nil, errors.New("wiki page index page_id is required")
	}
	if strings.ContainsAny(item.PageID, " \t\r\n") {
		return nil, fmt.Errorf("invalid wiki page index page_id: %s", item.PageID)
	}
	cleanPath, err := validateWikiMarkdownPath(item.Path)
	if err != nil {
		return nil, err
	}
	item.Path = cleanPath
	if item.Title == "" {
		return nil, errors.New("wiki page index title is required")
	}
	if err := validateWikiPageType(item.Type); err != nil {
		return nil, err
	}
	if item.Status == "" {
		item.Status = WikiPageStatusActive
	}
	if err := validateWikiPageStatus(item.Status); err != nil {
		return nil, err
	}
	if item.CanonicalSource == "" {
		return nil, errors.New("wiki page index canonical_source is required")
	}
	if len(item.SourcePaths) == 0 {
		return nil, errors.New("wiki page index source paths are required")
	}
	if item.Summary == "" {
		item.Summary = item.Title
	}
	now := time.Now().UTC()
	if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = now
	}
	return &item, nil
}

func validateWikiMarkdownPath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", errors.New("wiki page index path is required")
	}
	if strings.HasPrefix(value, "/") || strings.Contains(value, "\\") {
		return "", fmt.Errorf("invalid wiki page index path: %s", value)
	}
	cleaned := path.Clean(value)
	if cleaned == "." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("invalid wiki page index path: %s", value)
	}
	if !strings.HasSuffix(cleaned, ".md") {
		return "", fmt.Errorf("wiki page index path must be markdown: %s", value)
	}
	return cleaned, nil
}

func validateWikiPageType(value string) error {
	switch value {
	case "index", "log", "concept", "module", "spec", "runbook":
		return nil
	default:
		return fmt.Errorf("invalid wiki page index type: %s", value)
	}
}

func validateWikiPageStatus(value string) error {
	switch value {
	case WikiPageStatusDraft, WikiPageStatusActive, WikiPageStatusArchived, WikiPageStatusDeprecated:
		return nil
	default:
		return fmt.Errorf("invalid wiki page index status: %s", value)
	}
}

func cleanStringSlice(values []string) []string {
	cleaned := make([]string, 0, len(values))
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		cleaned = append(cleaned, value)
	}
	return cleaned
}

func scanWikiPageIndexRows(rows *sql.Rows) ([]WikiPageIndexItem, error) {
	items := []WikiPageIndexItem{}
	for rows.Next() {
		var item WikiPageIndexItem
		var sourceJSON string
		var relatedJSON string
		if err := rows.Scan(&item.PageID, &item.Path, &item.Title, &item.Type, &item.Status, &item.Owner,
			&item.CanonicalSource, &sourceJSON, &relatedJSON, &item.Summary, &item.ContentHash,
			&item.CreatedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan wiki page index row: %w", err)
		}
		if err := json.Unmarshal([]byte(sourceJSON), &item.SourcePaths); err != nil {
			return nil, fmt.Errorf("failed to unmarshal wiki page source paths: %w", err)
		}
		if err := json.Unmarshal([]byte(relatedJSON), &item.Related); err != nil {
			return nil, fmt.Errorf("failed to unmarshal wiki page related paths: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("failed to iterate wiki page index rows: %w", err)
	}
	return items, nil
}
