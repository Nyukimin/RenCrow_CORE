package l1sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	L1ResearchRecallMaxLimit    = 20
	L1ResearchRecallMaxScanRows = 2000
)

type L1ResearchStagingSearchResult struct {
	Items               []L1StagingItem
	ScanLimited         bool
	CandidatesTruncated bool
}

// SearchResearchStagingDocuments returns bounded candidate rows from the
// exact public research or user owner namespace. Callers must exact-read each
// candidate by namespace/event before treating its contents as evidence.
// Search uses one escaped literal substring against raw_text. Unreferenced
// searches scan at most the newest 2,000 staging row IDs. Explicit bounded
// references use the existing primary-key and namespace/event indexes instead.
// ScanLimited means older unreferenced rows may be outside the scan window.
func (s *L1SQLiteStore) SearchResearchStagingDocuments(
	ctx context.Context,
	scope string,
	ownerID string,
	query string,
	documentRefs []string,
	since time.Time,
	until time.Time,
	limit int,
) (L1ResearchStagingSearchResult, error) {
	if s == nil || ctx == nil {
		return L1ResearchStagingSearchResult{}, errors.New("l1 research recall store and context are required")
	}
	if limit < 1 || limit > L1ResearchRecallMaxLimit {
		return L1ResearchStagingSearchResult{}, errors.New("l1 research recall limit must be between 1 and 20")
	}
	query = strings.TrimSpace(query)
	if query == "" || len([]rune(query)) > 2000 {
		return L1ResearchStagingSearchResult{}, errors.New("l1 research recall query must be non-empty and bounded")
	}
	namespace, err := researchRecallNamespace(scope, ownerID)
	if err != nil {
		return L1ResearchStagingSearchResult{}, err
	}
	if since.IsZero() != until.IsZero() || !since.IsZero() && !until.After(since) {
		return L1ResearchStagingSearchResult{}, errors.New("l1 research recall time window must have a valid since and until")
	}
	refs := make([]string, 0, len(documentRefs))
	if len(documentRefs) > L1ResearchRecallMaxLimit {
		return L1ResearchStagingSearchResult{}, errors.New("l1 research recall document refs exceed the bounded limit")
	}
	seen := make(map[string]struct{}, len(documentRefs))
	for _, rawRef := range documentRefs {
		ref := strings.TrimSpace(rawRef)
		if ref == "" || ref != rawRef || len(ref) > 512 {
			return L1ResearchStagingSearchResult{}, errors.New("l1 research recall document refs must be non-empty and bounded")
		}
		if _, exists := seen[ref]; exists {
			continue
		}
		seen[ref] = struct{}{}
		refs = append(refs, ref)
	}
	queryDB := s.readDB
	if queryDB == nil {
		queryDB = s.db
	}
	if queryDB == nil {
		return L1ResearchStagingSearchResult{}, errors.New("l1 research recall store is closed")
	}
	conditions := []string{}
	args := []any{}
	scanLimited := false
	if len(refs) == 0 {
		var maxRowID int64
		if err := queryDB.QueryRowContext(ctx, `SELECT COALESCE(MAX(rowid), 0) FROM l1_staging_item`).Scan(&maxRowID); err != nil {
			return L1ResearchStagingSearchResult{}, fmt.Errorf("read l1 research staging scan boundary: %w", err)
		}
		minRowID := maxRowID - L1ResearchRecallMaxScanRows + 1
		scanLimited = minRowID > 1
		if minRowID < 1 {
			minRowID = 1
		}
		conditions = append(conditions, "rowid >= ?", "rowid <= ?")
		args = append(args, minRowID, maxRowID)
	}
	conditions = append(conditions,
		"kind = ?", "namespace = ?", "validation_status = ?",
		"json_valid(meta_json) = 1",
		"json_extract(meta_json, '$.research_scope') = ?",
		"json_extract(meta_json, '$.research_namespace') = ?",
		"json_extract(meta_json, '$.content_complete') = 1",
		"json_extract(meta_json, '$.retention_allowed') = 1",
		"json_extract(meta_json, '$.research_digest') = raw_hash",
		"raw_text LIKE ? ESCAPE '\\'",
	)
	args = append(args, L1StagingKindExternalFetch, namespace, L1StagingStatusPending, scope, namespace, LikeQuery(query))
	if !since.IsZero() {
		conditions = append(conditions, "datetime(COALESCE(published_at, fetched_at)) >= datetime(?)", "datetime(COALESCE(published_at, fetched_at)) < datetime(?)")
		args = append(args, since.UTC(), until.UTC())
	}
	if len(refs) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(refs)), ",")
		conditions = append(conditions, fmt.Sprintf(`rowid IN (
		SELECT rowid FROM l1_staging_item WHERE id IN (%s)
		UNION
		SELECT rowid FROM l1_staging_item WHERE namespace = ? AND event_id IN (%s)
		)`, placeholders, placeholders))
		for _, ref := range refs {
			args = append(args, ref)
		}
		args = append(args, namespace)
		for _, ref := range refs {
			args = append(args, ref)
		}
	}
	args = append(args, limit+1)
	rows, err := queryDB.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note,
       validation_status, meta_json, created_at, updated_at
FROM l1_staging_item
WHERE `+strings.Join(conditions, " AND ")+`
ORDER BY fetched_at DESC, event_id ASC
LIMIT ?`, args...)
	if err != nil {
		return L1ResearchStagingSearchResult{}, fmt.Errorf("query l1 research staging candidates: %w", err)
	}
	defer rows.Close()
	items, err := scanL1StagingItems(rows)
	if err != nil {
		return L1ResearchStagingSearchResult{}, err
	}
	candidatesTruncated := len(items) > limit
	if candidatesTruncated {
		items = items[:limit]
	}
	return L1ResearchStagingSearchResult{Items: items, ScanLimited: scanLimited, CandidatesTruncated: candidatesTruncated}, nil
}

func researchRecallNamespace(scope, ownerID string) (string, error) {
	switch scope {
	case "public":
		if ownerID != "" {
			return "", errors.New("public l1 research recall must not include an owner")
		}
		return BuildL1Namespace(NamespaceKindKnowledge, "research")
	case "user":
		if ownerID == "" || strings.TrimSpace(ownerID) != ownerID {
			return "", errors.New("user l1 research recall requires a canonical owner")
		}
		return BuildL1Namespace(NamespaceKindUser, ownerID)
	default:
		return "", errors.New("l1 research recall scope must be public or user")
	}
}
