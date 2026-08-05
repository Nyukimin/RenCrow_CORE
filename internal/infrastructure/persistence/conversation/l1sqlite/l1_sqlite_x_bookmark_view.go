package l1sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	domainworkflow "github.com/Nyukimin/RenCrow_CORE/internal/domain/xbookmarkworkflow"
)

const (
	L1XBookmarkReviewAll         = ""
	L1XBookmarkReviewNeedsReview = "needs_review"
	L1XBookmarkReviewClassified  = "classified"
)

type L1XBookmarkViewQuery struct {
	Major  string
	Minor  string
	Review string
	Search string
	Limit  int
	Offset int
}

type L1XBookmarkViewSummary struct {
	Total       int
	NeedsReview int
	MajorCounts map[string]int
	MinorCounts map[string]int
}

type L1XBookmarkViewPage struct {
	Items   []L1StagingItem
	Total   int
	Limit   int
	Offset  int
	Summary L1XBookmarkViewSummary
}

// XBookmarkWorkflowSource returns one immutable staging projection for an
// explicitly requested utilization workflow. It never validates or promotes it.
func (s *L1SQLiteStore) XBookmarkWorkflowSource(ctx context.Context, id string) (domainworkflow.SourceRecord, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return domainworkflow.SourceRecord{}, domainworkflow.ErrSourceNotFound
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note,
       validation_status, meta_json, created_at, updated_at
FROM l1_staging_item
WHERE id = ? AND json_extract(meta_json, '$.collection') = 'x_bookmark'
LIMIT 1
`, id)
	if err != nil {
		return domainworkflow.SourceRecord{}, fmt.Errorf("query x bookmark workflow source: %w", err)
	}
	items, scanErr := scanL1StagingItems(rows)
	rows.Close()
	if scanErr != nil {
		return domainworkflow.SourceRecord{}, scanErr
	}
	if len(items) == 0 {
		return domainworkflow.SourceRecord{}, domainworkflow.ErrSourceNotFound
	}
	item := items[0]
	title := workflowMetaString(item.Meta, "title")
	if title == "" {
		title = firstNonEmptyLine(item.SummaryDraft, item.RawText)
	}
	author, _ := item.Meta["author"].(map[string]interface{})
	return domainworkflow.SourceRecord{
		ID: item.ID, Title: title, SourceURL: item.SourceURL, RawText: item.RawText,
		AuthorName: workflowMapString(author, "name"), AuthorUsername: workflowMapString(author, "username"),
		Media: workflowMedia(item.Meta["media"]), References: workflowReferences(item.Meta["references"]),
	}, nil
}

func workflowMedia(raw interface{}) []domainworkflow.Media {
	values, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	result := make([]domainworkflow.Media, 0, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(map[string]interface{})
		if !ok {
			continue
		}
		media := domainworkflow.Media{
			Type: workflowMapString(value, "type"), URL: workflowMapString(value, "url"),
			Alt: workflowMapString(value, "alt"), Poster: workflowMapString(value, "poster"),
		}
		if media.URL != "" {
			result = append(result, media)
		}
	}
	return result
}

func workflowReferences(raw interface{}) []map[string]string {
	values, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	keys := []string{"kind", "url", "resolved_url", "status_url", "capture_status", "display_text", "preview_text", "page_title", "page_description", "body_text", "text"}
	result := make([]map[string]string, 0, len(values))
	for _, rawValue := range values {
		value, ok := rawValue.(map[string]interface{})
		if !ok {
			continue
		}
		projected := map[string]string{}
		for _, key := range keys {
			if text := workflowMapString(value, key); text != "" {
				projected[key] = text
			}
		}
		if len(projected) > 0 {
			result = append(result, projected)
		}
	}
	return result
}

func workflowMetaString(meta map[string]interface{}, key string) string {
	return workflowMapString(meta, key)
}

func workflowMapString(values map[string]interface{}, key string) string {
	if values == nil {
		return ""
	}
	value, _ := values[key].(string)
	return strings.TrimSpace(value)
}

func firstNonEmptyLine(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if index := strings.IndexByte(value, '\n'); index >= 0 {
			value = value[:index]
		}
		return strings.TrimSpace(strings.TrimLeft(value, "# "))
	}
	return ""
}

// XBookmarkStagingPage projects imported X Bookmark staging records for the
// read-only Viewer. It never validates or promotes staging items.
func (s *L1SQLiteStore) XBookmarkStagingPage(ctx context.Context, query L1XBookmarkViewQuery) (*L1XBookmarkViewPage, error) {
	query.Major = strings.TrimSpace(query.Major)
	query.Minor = strings.TrimSpace(query.Minor)
	query.Review = strings.TrimSpace(query.Review)
	query.Search = strings.TrimSpace(query.Search)
	if query.Limit == 0 {
		query.Limit = 12
	}
	if query.Limit < 1 || query.Limit > 50 {
		return nil, errors.New("x bookmark view limit must be between 1 and 50")
	}
	if query.Offset < 0 {
		return nil, errors.New("x bookmark view offset must not be negative")
	}
	if len(query.Search) > 200 {
		return nil, errors.New("x bookmark view search must not exceed 200 characters")
	}
	if query.Review != L1XBookmarkReviewAll && query.Review != L1XBookmarkReviewNeedsReview && query.Review != L1XBookmarkReviewClassified {
		return nil, errors.New("invalid x bookmark view review filter")
	}

	where, args := xBookmarkViewWhere(query)
	var total int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM l1_staging_item WHERE "+where, args...).Scan(&total); err != nil {
		return nil, fmt.Errorf("failed to count x bookmark staging items: %w", err)
	}
	listArgs := append(append([]interface{}{}, args...), query.Limit, query.Offset)
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note,
       validation_status, meta_json, created_at, updated_at
FROM l1_staging_item
WHERE `+where+`
ORDER BY updated_at DESC, rowid DESC
LIMIT ? OFFSET ?
`, listArgs...)
	if err != nil {
		return nil, fmt.Errorf("failed to query x bookmark staging items: %w", err)
	}
	items, err := scanL1StagingItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	summary, err := s.xBookmarkViewSummary(ctx)
	if err != nil {
		return nil, err
	}
	return &L1XBookmarkViewPage{
		Items: items, Total: total, Limit: query.Limit, Offset: query.Offset, Summary: summary,
	}, nil
}

func xBookmarkViewWhere(query L1XBookmarkViewQuery) (string, []interface{}) {
	clauses := []string{"json_extract(meta_json, '$.collection') = 'x_bookmark'"}
	args := make([]interface{}, 0, 8)
	if query.Major != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(meta_json, '$.use_case_tags') AS tag WHERE json_extract(tag.value, '$.major') = ?)")
		args = append(args, query.Major)
	}
	if query.Minor != "" {
		clauses = append(clauses, "EXISTS (SELECT 1 FROM json_each(meta_json, '$.use_case_tags') AS tag WHERE json_extract(tag.value, '$.minor') = ?)")
		args = append(args, query.Minor)
	}
	switch query.Review {
	case L1XBookmarkReviewNeedsReview:
		clauses = append(clauses, "COALESCE(json_extract(meta_json, '$.classification.needs_review'), 0) = 1")
	case L1XBookmarkReviewClassified:
		clauses = append(clauses, "COALESCE(json_extract(meta_json, '$.classification.needs_review'), 0) = 0")
	}
	if query.Search != "" {
		pattern := "%" + escapeSQLiteLike(query.Search) + "%"
		clauses = append(clauses, `(raw_text LIKE ? ESCAPE '\' OR summary_draft LIKE ? ESCAPE '\' OR source_url LIKE ? ESCAPE '\' OR COALESCE(json_extract(meta_json, '$.title'), '') LIKE ? ESCAPE '\'
OR EXISTS (
  SELECT 1 FROM json_each(meta_json, '$.references') AS reference
  WHERE COALESCE(json_extract(reference.value, '$.url'), '') LIKE ? ESCAPE '\'
     OR COALESCE(json_extract(reference.value, '$.resolved_url'), '') LIKE ? ESCAPE '\'
     OR COALESCE(json_extract(reference.value, '$.page_title'), '') LIKE ? ESCAPE '\'
     OR COALESCE(json_extract(reference.value, '$.page_description'), '') LIKE ? ESCAPE '\'
     OR COALESCE(json_extract(reference.value, '$.body_text'), '') LIKE ? ESCAPE '\'
     OR COALESCE(json_extract(reference.value, '$.preview_text'), '') LIKE ? ESCAPE '\'
     OR COALESCE(json_extract(reference.value, '$.text'), '') LIKE ? ESCAPE '\'
))`)
		args = append(args, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern, pattern)
	}
	return strings.Join(clauses, " AND "), args
}

func escapeSQLiteLike(value string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(value)
}

func (s *L1SQLiteStore) xBookmarkViewSummary(ctx context.Context) (L1XBookmarkViewSummary, error) {
	summary := L1XBookmarkViewSummary{MajorCounts: map[string]int{}, MinorCounts: map[string]int{}}
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(SUM(CASE WHEN COALESCE(json_extract(meta_json, '$.classification.needs_review'), 0) = 1 THEN 1 ELSE 0 END), 0)
FROM l1_staging_item
WHERE json_extract(meta_json, '$.collection') = 'x_bookmark'
`).Scan(&summary.Total, &summary.NeedsReview)
	if err != nil {
		return summary, fmt.Errorf("failed to summarize x bookmark staging items: %w", err)
	}
	if err := s.readXBookmarkFacetCounts(ctx, "major", summary.MajorCounts); err != nil {
		return summary, err
	}
	if err := s.readXBookmarkFacetCounts(ctx, "minor", summary.MinorCounts); err != nil {
		return summary, err
	}
	return summary, nil
}

func (s *L1SQLiteStore) readXBookmarkFacetCounts(ctx context.Context, field string, destination map[string]int) error {
	if field != "major" && field != "minor" {
		return errors.New("invalid x bookmark facet field")
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT json_extract(tag.value, '$.`+field+`') AS facet, COUNT(*)
FROM l1_staging_item AS item
JOIN json_each(item.meta_json, '$.use_case_tags') AS tag
WHERE json_extract(item.meta_json, '$.collection') = 'x_bookmark'
  AND COALESCE(json_extract(tag.value, '$.`+field+`'), '') <> ''
GROUP BY facet
ORDER BY COUNT(*) DESC, facet
`)
	if err != nil {
		return fmt.Errorf("failed to query x bookmark %s facets: %w", field, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		var count int
		if err := rows.Scan(&name, &count); err != nil {
			return fmt.Errorf("failed to scan x bookmark %s facet: %w", field, err)
		}
		destination[name] = count
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("x bookmark %s facet rows error: %w", field, err)
	}
	return nil
}
