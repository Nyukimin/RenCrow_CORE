package categoryrecall

import (
	"context"
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
	return []string{"hobby", "book", "game", "movie", "drama", "person"}
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
	return result, rows.Err()
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
	return hints, rows.Err()
}
