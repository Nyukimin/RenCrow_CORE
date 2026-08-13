package categoryrecall

import (
	"context"
	"database/sql"
	"strings"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "modernc.org/sqlite"
)

type HobbyGraphSource struct {
	path string
}

func NewHobbyGraphSource(path string) *HobbyGraphSource {
	return &HobbyGraphSource{path: strings.TrimSpace(path)}
}

func (s *HobbyGraphSource) ID() string { return "hobby_graph" }

func (s *HobbyGraphSource) Categories() []string {
	return []string{"hobby", "book", "game", "movie", "drama", "person", "music", "anime", "novel", "manga", "award"}
}

func (s *HobbyGraphSource) Search(ctx context.Context, query domconv.CategoryRecallQuery) (domconv.CategoryRecallResult, error) {
	db, err := openReadOnlySQLite(s.path)
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	defer db.Close()
	exists, err := tableExists(db, "hobby_items")
	if err != nil {
		return domconv.CategoryRecallResult{}, err
	}
	if !exists {
		return domconv.CategoryRecallResult{}, errUnavailable("hobby_items table is missing")
	}
	predicate, args := lexicalPredicate(query.Message, "title", "category", "item_type")
	args = append(args, boundedLimit(query.Limit))
	rows, err := db.QueryContext(ctx, `SELECT item_id, category, item_type, title, COALESCE(canonical_source, ''), COALESCE(canonical_url, ''), COALESCE(metadata_json, '{}'), COALESCE(updated_at, '') FROM hobby_items WHERE `+predicate+` ORDER BY updated_at DESC, title LIMIT ?`, args...)
	if err != nil {
		return domconv.CategoryRecallResult{}, errUnavailable("hobby_items query failed: " + err.Error())
	}
	defer rows.Close()
	result := domconv.CategoryRecallResult{}
	for rows.Next() {
		var id, category, itemType, title, canonicalSource, canonicalURL, metadataJSON string
		var updatedAt string
		if err := rows.Scan(&id, &category, &itemType, &title, &canonicalSource, &canonicalURL, &metadataJSON, &updatedAt); err != nil {
			return result, err
		}
		category = normalizeCategory(category)
		requestedCategory := normalizeCategory(query.Category)
		if requestedCategory != "" && requestedCategory != "hobby" && category != requestedCategory {
			continue
		}
		if !queryMatches(query.Message, title, category, itemType) {
			continue
		}
		meta := decodeJSONMap(metadataJSON)
		state := metadataString(meta, "validation_status")
		if state == "" {
			state = domconv.CategoryRecordStateValidated
		}
		scope := metadataString(meta, "scope")
		if scope == "" {
			scope = "public"
		}
		sensitivity := metadataString(meta, "sensitivity")
		if sensitivity == "" {
			sensitivity = "normal"
		}
		freshUntil := metadataTime(meta, "fresh_until")
		retrievedAt := parseSourceTime(updatedAt)
		provenance := nonEmptyStrings(canonicalURL)
		if len(provenance) == 0 && strings.HasPrefix(strings.ToLower(strings.TrimSpace(canonicalSource)), "http") {
			provenance = nonEmptyStrings(canonicalSource)
		}
		result.Records = append(result.Records, domconv.CategoryRecallRecord{
			Category: category, SourceID: s.ID(), RecordID: id, Title: strings.TrimSpace(title),
			Summary: strings.TrimSpace(itemType + " / " + category), ProvenanceURLs: provenance,
			RetrievedAt: retrievedAt, ValidatedAt: retrievedAt, FreshUntil: freshUntil,
			State: state, Sensitivity: sensitivity, Scope: scope,
			Roles: []string{"chat", "worker", "heavy", "creative"}, Score: 1,
		})
		if len(result.Records) >= boundedLimit(query.Limit) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return result, err
	}
	if len(result.Records) >= boundedLimit(query.Limit) {
		return result, nil
	}
	related, err := s.searchValidatedRelatedItems(ctx, db, query, boundedLimit(query.Limit)-len(result.Records))
	if err != nil {
		return result, err
	}
	result.Records = append(result.Records, related...)
	return result, nil
}

func (s *HobbyGraphSource) searchValidatedRelatedItems(ctx context.Context, db *sql.DB, query domconv.CategoryRecallQuery, limit int) ([]domconv.CategoryRecallRecord, error) {
	if limit <= 0 {
		return nil, nil
	}
	exists, err := tableExists(db, "hobby_related_items")
	if err != nil || !exists {
		return nil, err
	}
	predicate, args := lexicalPredicate(query.Message, "i.display_name", "i.name_original", "i.name_ja", "i.category", "i.item_type")
	args = append(args, limit)
	rows, err := db.QueryContext(ctx, `SELECT i.item_id,i.category,i.item_type,i.display_name,
COALESCE(NULLIF(s.description_ja,''),NULLIF(i.description_ja,''),NULLIF(s.description_original,''),NULLIF(i.description_original,''),i.item_type || ' / ' || i.category),
i.canonical_url,i.updated_at
FROM hobby_related_items i
LEFT JOIN hobby_item_summaries s ON s.category=i.category AND s.item_id=i.item_id
WHERE `+predicate+` AND EXISTS (
  SELECT 1 FROM hobby_person_relations r
  WHERE r.category=i.category AND r.target_item_id=i.item_id AND r.validation_state='validated'
)
ORDER BY i.updated_at DESC,i.display_name LIMIT ?`, args...)
	if err != nil {
		return nil, errUnavailable("related items query failed: " + err.Error())
	}
	defer rows.Close()
	records := make([]domconv.CategoryRecallRecord, 0, limit)
	for rows.Next() {
		var id, category, itemType, title, summary, sourceURL, updatedAt string
		if err := rows.Scan(&id, &category, &itemType, &title, &summary, &sourceURL, &updatedAt); err != nil {
			return records, err
		}
		category = normalizeCategory(category)
		requestedCategory := normalizeCategory(query.Category)
		if requestedCategory != "" && requestedCategory != "hobby" && category != requestedCategory {
			continue
		}
		if !queryMatches(query.Message, title, category, itemType) {
			continue
		}
		retrievedAt := parseSourceTime(updatedAt)
		records = append(records, domconv.CategoryRecallRecord{
			Category: category, SourceID: s.ID(), RecordID: "related:" + category + ":" + id,
			Title: strings.TrimSpace(title), Summary: strings.TrimSpace(summary), ProvenanceURLs: nonEmptyStrings(sourceURL),
			RetrievedAt: retrievedAt, ValidatedAt: retrievedAt, State: domconv.CategoryRecordStateValidated,
			Sensitivity: "normal", Scope: "public", Roles: []string{"chat", "worker", "heavy", "creative"}, Score: 1,
		})
	}
	return records, rows.Err()
}

func (s *HobbyGraphSource) StartupEntityHints(ctx context.Context) (map[string][]string, error) {
	db, err := openReadOnlySQLite(s.path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, `SELECT category, title FROM hobby_items WHERE TRIM(title) <> '' ORDER BY title`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	hints := map[string][]string{}
	for rows.Next() {
		var category, title string
		if err := rows.Scan(&category, &title); err != nil {
			return nil, err
		}
		hints[normalizeCategory(category)] = append(hints[normalizeCategory(category)], strings.TrimSpace(title))
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	relatedRows, err := db.QueryContext(ctx, `SELECT category,display_name FROM hobby_related_items WHERE TRIM(display_name)<>'' AND EXISTS (SELECT 1 FROM hobby_person_relations r WHERE r.category=hobby_related_items.category AND r.target_item_id=hobby_related_items.item_id AND r.validation_state='validated') ORDER BY display_name`)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such table") {
			return hints, nil
		}
		return nil, err
	}
	defer relatedRows.Close()
	for relatedRows.Next() {
		var category, title string
		if err := relatedRows.Scan(&category, &title); err != nil {
			return nil, err
		}
		hints[normalizeCategory(category)] = append(hints[normalizeCategory(category)], strings.TrimSpace(title))
	}
	return hints, relatedRows.Err()
}
