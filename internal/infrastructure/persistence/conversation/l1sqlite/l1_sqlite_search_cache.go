package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func (s *L1SQLiteStore) SaveSearchCache(ctx context.Context, provider string, rawQuery string, resultsJSON string, sourceURLs []string, ttl time.Duration) (*L1SearchCacheEntry, error) {
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return nil, errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	if resultsJSON == "" {
		resultsJSON = "[]"
	}
	if !json.Valid([]byte(resultsJSON)) {
		return nil, errors.New("search cache results_json must be valid JSON")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now().UTC()
	entry := &L1SearchCacheEntry{
		QueryHash:       searchQueryHash(provider, normalizedQuery),
		NormalizedQuery: normalizedQuery,
		Provider:        provider,
		RawQuery:        rawQuery,
		ResultsJSON:     resultsJSON,
		SourceURLs:      sourceURLs,
		RetrievedAt:     now,
		ExpiresAt:       now.Add(ttl),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	sourceURLsJSON, err := json.Marshal(sourceURLs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal search cache source urls: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_search_cache (
	query_hash, normalized_query, provider, raw_query, results_json, source_urls_json,
	retrieved_at, expires_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(query_hash) DO UPDATE SET
	raw_query = excluded.raw_query,
	results_json = excluded.results_json,
	source_urls_json = excluded.source_urls_json,
	retrieved_at = excluded.retrieved_at,
	expires_at = excluded.expires_at,
	updated_at = excluded.updated_at
`, entry.QueryHash, entry.NormalizedQuery, entry.Provider, entry.RawQuery, entry.ResultsJSON, string(sourceURLsJSON),
		entry.RetrievedAt, entry.ExpiresAt, entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save l1 search cache: %w", err)
	}
	searchNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, provider)
	if err != nil {
		return nil, err
	}
	if _, err := s.AppendEvent(ctx, "search.cache_saved", searchNamespace, "", 0, map[string]interface{}{
		"query_hash":       entry.QueryHash,
		"normalized_query": entry.NormalizedQuery,
		"raw_query":        entry.RawQuery,
		"provider":         entry.Provider,
		"expires_at":       entry.ExpiresAt.Format(time.RFC3339),
		"source_urls":      entry.SourceURLs,
	}, "search_cache"); err != nil {
		return nil, fmt.Errorf("failed to append l1 search cache event log: %w", err)
	}
	return entry, nil
}

func (s *L1SQLiteStore) GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*L1SearchCacheEntry, error) {
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return nil, errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, `
SELECT query_hash, normalized_query, provider, raw_query, results_json, source_urls_json,
       retrieved_at, expires_at, created_at, updated_at
FROM l1_search_cache
WHERE query_hash = ? AND expires_at > ?
`, searchQueryHash(provider, normalizedQuery), now.UTC())
	entry, err := scanL1SearchCacheEntry(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return entry, nil
}

func (s *L1SQLiteStore) GetSimilarFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time, threshold float64) (*L1SearchCacheEntry, error) {
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return nil, errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	if threshold <= 0 {
		threshold = 0.75
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT query_hash, normalized_query, provider, raw_query, results_json, source_urls_json,
       retrieved_at, expires_at, created_at, updated_at
FROM l1_search_cache
WHERE provider = ? AND expires_at > ?
ORDER BY retrieved_at DESC, updated_at DESC
LIMIT 50
`, provider, now.UTC())
	if err != nil {
		return nil, fmt.Errorf("failed to query similar l1 search cache: %w", err)
	}
	defer rows.Close()
	var best *L1SearchCacheEntry
	bestScore := 0.0
	for rows.Next() {
		entry, err := scanL1SearchCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		score := searchQuerySimilarity(normalizedQuery, entry.NormalizedQuery)
		if score >= threshold && score > bestScore {
			best = entry
			bestScore = score
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("similar l1 search cache rows error: %w", err)
	}
	return best, nil
}

func (s *L1SQLiteStore) InvalidateSearchCache(ctx context.Context, provider string, rawQuery string) (int64, error) {
	normalizedQuery := normalizeSearchQuery(rawQuery)
	if normalizedQuery == "" {
		return 0, errors.New("search cache query is required")
	}
	if provider == "" {
		provider = "default"
	}
	hash := searchQueryHash(provider, normalizedQuery)
	result, err := s.db.ExecContext(ctx, `DELETE FROM l1_search_cache WHERE query_hash = ?`, hash)
	if err != nil {
		return 0, fmt.Errorf("failed to invalidate l1 search cache: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("failed to inspect l1 search cache invalidation: %w", err)
	}
	searchNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, provider)
	if err != nil {
		return 0, err
	}
	if _, err := s.AppendEvent(ctx, "search.cache_invalidated", searchNamespace, "", 0, map[string]interface{}{
		"query_hash":       hash,
		"normalized_query": normalizedQuery,
		"raw_query":        rawQuery,
		"provider":         provider,
		"affected":         affected,
	}, "search_cache"); err != nil {
		return 0, fmt.Errorf("failed to append l1 search cache invalidated event log: %w", err)
	}
	return affected, nil
}

func (s *L1SQLiteStore) RecentSearchCache(ctx context.Context, limit int) ([]L1SearchCacheEntry, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT query_hash, normalized_query, provider, raw_query, results_json, source_urls_json,
       retrieved_at, expires_at, created_at, updated_at
FROM l1_search_cache
ORDER BY retrieved_at DESC, updated_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 search cache: %w", err)
	}
	defer rows.Close()

	var entries []L1SearchCacheEntry
	for rows.Next() {
		entry, err := scanL1SearchCacheEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, *entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("l1 search cache rows error: %w", err)
	}
	return entries, nil
}

type l1SearchCacheRow interface {
	Scan(dest ...interface{}) error
}

func scanL1SearchCacheEntry(row l1SearchCacheRow) (*L1SearchCacheEntry, error) {
	var entry L1SearchCacheEntry
	var sourceURLsJSON string
	if err := row.Scan(
		&entry.QueryHash,
		&entry.NormalizedQuery,
		&entry.Provider,
		&entry.RawQuery,
		&entry.ResultsJSON,
		&sourceURLsJSON,
		&entry.RetrievedAt,
		&entry.ExpiresAt,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("failed to scan l1 search cache: %w", err)
	}
	if sourceURLsJSON == "" {
		sourceURLsJSON = "[]"
	}
	if err := json.Unmarshal([]byte(sourceURLsJSON), &entry.SourceURLs); err != nil {
		return nil, fmt.Errorf("failed to unmarshal search cache source urls: %w", err)
	}
	return &entry, nil
}

func normalizeSearchQuery(query string) string {
	return strings.Join(strings.Fields(strings.ToLower(query)), " ")
}

func searchQueryHash(provider string, normalizedQuery string) string {
	sum := sha256.Sum256([]byte(provider + "\x00" + normalizedQuery))
	return hex.EncodeToString(sum[:])
}

func searchQuerySimilarity(a string, b string) float64 {
	aSet := tokenSet(a)
	bSet := tokenSet(b)
	if len(aSet) == 0 || len(bSet) == 0 {
		return 0
	}
	intersection := 0
	for token := range aSet {
		if bSet[token] {
			intersection++
		}
	}
	union := len(aSet) + len(bSet) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func tokenSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, token := range strings.Fields(normalizeSearchQuery(text)) {
		out[token] = true
	}
	return out
}
