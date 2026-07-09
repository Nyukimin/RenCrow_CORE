package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func (s *L1SQLiteStore) SaveWebGatherFetchCache(ctx context.Context, rawURL string, fetchProvider string, extractor string, status string, responseJSON string, ttl time.Duration) (*L1WebGatherFetchCacheEntry, error) {
	normalizedURL := normalizeWebGatherCacheURL(rawURL)
	if normalizedURL == "" {
		return nil, errors.New("web gather fetch cache url is required")
	}
	fetchProvider = webGatherCachePart(fetchProvider, "http")
	extractor = webGatherCachePart(extractor, "html_basic")
	status = strings.TrimSpace(status)
	if status == "" {
		status = "ok"
	}
	if responseJSON == "" {
		responseJSON = "{}"
	}
	if !json.Valid([]byte(responseJSON)) {
		return nil, errors.New("web gather fetch cache response_json must be valid JSON")
	}
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	now := time.Now().UTC()
	entry := &L1WebGatherFetchCacheEntry{
		CacheKey:      webGatherFetchCacheKey(normalizedURL, fetchProvider, extractor),
		URL:           normalizedURL,
		FetchProvider: fetchProvider,
		Extractor:     extractor,
		Status:        status,
		ResponseJSON:  responseJSON,
		ErrorCode:     webGatherFetchCacheErrorCode(responseJSON),
		RetrievedAt:   now,
		ExpiresAt:     now.Add(ttl),
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO l1_web_gather_fetch_cache (
	cache_key, url, fetch_provider, extractor, status, response_json, error_code,
	retrieved_at, expires_at, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(cache_key) DO UPDATE SET
	status = excluded.status,
	response_json = excluded.response_json,
	error_code = excluded.error_code,
	retrieved_at = excluded.retrieved_at,
	expires_at = excluded.expires_at,
	updated_at = excluded.updated_at
`, entry.CacheKey, entry.URL, entry.FetchProvider, entry.Extractor, entry.Status, entry.ResponseJSON, entry.ErrorCode,
		entry.RetrievedAt, entry.ExpiresAt, entry.CreatedAt, entry.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save web gather fetch cache: %w", err)
	}
	if _, err := s.AppendEvent(ctx, "web_gather.fetch_cache_saved", "kb:web", "", 0, map[string]interface{}{
		"cache_key":      entry.CacheKey,
		"url":            entry.URL,
		"fetch_provider": entry.FetchProvider,
		"extractor":      entry.Extractor,
		"status":         entry.Status,
		"error_code":     entry.ErrorCode,
		"expires_at":     entry.ExpiresAt.Format(time.RFC3339),
	}, "web_gather_cache"); err != nil {
		return nil, fmt.Errorf("failed to append web gather fetch cache event: %w", err)
	}
	return entry, nil
}

func (s *L1SQLiteStore) GetFreshWebGatherFetchCache(ctx context.Context, rawURL string, fetchProvider string, extractor string, now time.Time) (*L1WebGatherFetchCacheEntry, error) {
	normalizedURL := normalizeWebGatherCacheURL(rawURL)
	if normalizedURL == "" {
		return nil, errors.New("web gather fetch cache url is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	row := s.db.QueryRowContext(ctx, `
SELECT cache_key, url, fetch_provider, extractor, status, response_json, error_code,
       retrieved_at, expires_at, created_at, updated_at
FROM l1_web_gather_fetch_cache
WHERE cache_key = ? AND expires_at > ?
`, webGatherFetchCacheKey(normalizedURL, webGatherCachePart(fetchProvider, "http"), webGatherCachePart(extractor, "html_basic")), now.UTC())
	entry, err := scanL1WebGatherFetchCacheEntry(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return entry, nil
}

func (s *L1SQLiteStore) SaveWebGatherRateState(ctx context.Context, domain string, at time.Time) (*L1WebGatherRateState, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, errors.New("web gather rate state domain is required")
	}
	if at.IsZero() {
		at = time.Now().UTC()
	}
	now := time.Now().UTC()
	state := &L1WebGatherRateState{Domain: domain, LastFetchAt: at.UTC(), CreatedAt: now, UpdatedAt: now}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO l1_web_gather_rate_state (domain, last_fetch_at, created_at, updated_at)
VALUES (?, ?, ?, ?)
ON CONFLICT(domain) DO UPDATE SET
	last_fetch_at = excluded.last_fetch_at,
	updated_at = excluded.updated_at
`, state.Domain, state.LastFetchAt, state.CreatedAt, state.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save web gather rate state: %w", err)
	}
	return state, nil
}

func (s *L1SQLiteStore) GetWebGatherRateState(ctx context.Context, domain string) (*L1WebGatherRateState, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, errors.New("web gather rate state domain is required")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT domain, last_fetch_at, created_at, updated_at
FROM l1_web_gather_rate_state
WHERE domain = ?
`, domain)
	var state L1WebGatherRateState
	if err := row.Scan(&state.Domain, &state.LastFetchAt, &state.CreatedAt, &state.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan web gather rate state: %w", err)
	}
	return &state, nil
}

type l1WebGatherFetchCacheRow interface {
	Scan(dest ...interface{}) error
}

func scanL1WebGatherFetchCacheEntry(row l1WebGatherFetchCacheRow) (*L1WebGatherFetchCacheEntry, error) {
	var entry L1WebGatherFetchCacheEntry
	if err := row.Scan(
		&entry.CacheKey,
		&entry.URL,
		&entry.FetchProvider,
		&entry.Extractor,
		&entry.Status,
		&entry.ResponseJSON,
		&entry.ErrorCode,
		&entry.RetrievedAt,
		&entry.ExpiresAt,
		&entry.CreatedAt,
		&entry.UpdatedAt,
	); err != nil {
		return nil, fmt.Errorf("failed to scan web gather fetch cache: %w", err)
	}
	return &entry, nil
}

func normalizeWebGatherCacheURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return strings.TrimSpace(rawURL)
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String()
}

func webGatherCachePart(value string, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return fallback
	}
	return value
}

func webGatherFetchCacheKey(normalizedURL string, fetchProvider string, extractor string) string {
	sum := sha256.Sum256([]byte(normalizedURL + "\x00" + fetchProvider + "\x00" + extractor))
	return hex.EncodeToString(sum[:])
}

func webGatherFetchCacheErrorCode(responseJSON string) string {
	var payload struct {
		ErrorCode string `json:"error_code"`
	}
	_ = json.Unmarshal([]byte(responseJSON), &payload)
	return strings.TrimSpace(payload.ErrorCode)
}
