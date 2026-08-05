package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

const (
	L1NewsArticleFetchStatusFetching    = "fetching"
	L1NewsArticleFetchStatusReady       = "ready"
	L1NewsArticleFetchStatusUnavailable = "unavailable"
)

const defaultNewsArticleFetchLease = 2 * time.Minute

// ClaimNewsArticleFetch atomically reserves a URL for one linked-article HTTP
// request. A ready URL is permanent and is returned as a cache hit. Failed URLs
// may be claimed again only after next_attempt_at.
func (s *L1SQLiteStore) ClaimNewsArticleFetch(ctx context.Context, rawURL string, now time.Time) (*L1NewsArticleFetch, bool, error) {
	normalizedURL := normalizeNewsArticleURL(rawURL)
	if normalizedURL == "" {
		return nil, false, errors.New("news article fetch url is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	leaseExpiresAt := now.Add(defaultNewsArticleFetchLease)
	result, err := s.db.ExecContext(ctx, `
INSERT INTO l1_news_article_fetch (
	normalized_url, status, attempt_count, lease_expires_at,
	created_at, updated_at
) VALUES (?, ?, 1, ?, ?, ?)
ON CONFLICT(normalized_url) DO NOTHING
`, normalizedURL, L1NewsArticleFetchStatusFetching, leaseExpiresAt, now, now)
	if err != nil {
		return nil, false, fmt.Errorf("failed to claim news article fetch: %w", err)
	}
	if affected, _ := result.RowsAffected(); affected == 1 {
		entry, err := s.NewsArticleFetch(ctx, normalizedURL)
		return entry, true, err
	}

	entry, err := s.NewsArticleFetch(ctx, normalizedURL)
	if err != nil {
		return nil, false, err
	}
	if entry.Status == L1NewsArticleFetchStatusReady {
		if err := s.syncReadyNewsArticleFetch(ctx, entry); err != nil {
			return nil, false, err
		}
		return entry, false, nil
	}
	if entry.Status == L1NewsArticleFetchStatusFetching && entry.LeaseExpiresAt.After(now) {
		return entry, false, nil
	}
	if entry.Status == L1NewsArticleFetchStatusUnavailable && entry.NextAttemptAt.After(now) {
		return entry, false, nil
	}
	result, err = s.db.ExecContext(ctx, `
UPDATE l1_news_article_fetch
SET status = ?, error_code = '', attempt_count = attempt_count + 1,
	lease_expires_at = ?, next_attempt_at = NULL, updated_at = ?
WHERE normalized_url = ?
  AND status <> ?
  AND (
	(status = ? AND (lease_expires_at IS NULL OR lease_expires_at <= ?)) OR
	(status = ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?))
  )
`, L1NewsArticleFetchStatusFetching, leaseExpiresAt, now, normalizedURL,
		L1NewsArticleFetchStatusReady,
		L1NewsArticleFetchStatusFetching, now,
		L1NewsArticleFetchStatusUnavailable, now)
	if err != nil {
		return nil, false, fmt.Errorf("failed to reclaim news article fetch: %w", err)
	}
	claimed, _ := result.RowsAffected()
	entry, err = s.NewsArticleFetch(ctx, normalizedURL)
	return entry, claimed == 1, err
}

func (s *L1SQLiteStore) syncReadyNewsArticleFetch(ctx context.Context, entry *L1NewsArticleFetch) error {
	if entry == nil || entry.Status != L1NewsArticleFetchStatusReady || strings.TrimSpace(entry.ArticleText) == "" {
		return nil
	}
	now := entry.CompletedAt
	if now.IsZero() {
		now = time.Now().UTC()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin ready news article sync: %w", err)
	}
	defer tx.Rollback()
	completion := L1NewsArticleFetchCompletion{
		FinalURL: entry.FinalURL, FetchURL: entry.FetchURL, ContentType: entry.ContentType, FetchProvider: entry.FetchProvider,
		Extractor: entry.Extractor, RawBytes: entry.RawBytes, ArticleText: entry.ArticleText, ContentSHA256: entry.ContentSHA256,
	}
	if err := syncCompletedArticleToNewsItems(ctx, tx, entry.NormalizedURL, completion, now.UTC()); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit ready news article sync: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) CompleteNewsArticleFetch(ctx context.Context, rawURL string, completion L1NewsArticleFetchCompletion, now time.Time) error {
	normalizedURL := normalizeNewsArticleURL(rawURL)
	if normalizedURL == "" {
		return errors.New("news article fetch url is required")
	}
	completion.ArticleText = strings.TrimSpace(completion.ArticleText)
	if completion.ArticleText == "" {
		return errors.New("news article fetch article text is required")
	}
	if strings.TrimSpace(completion.FetchURL) == "" {
		completion.FetchURL = firstNonEmptyL1(completion.FinalURL, normalizedURL)
	}
	if strings.TrimSpace(completion.ContentSHA256) == "" {
		completion.ContentSHA256 = rawTextHash(completion.ArticleText)
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin news article completion: %w", err)
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `
UPDATE l1_news_article_fetch
SET status = ?, final_url = ?, fetch_url = ?, content_type = ?, fetch_provider = ?,
	extractor = ?, raw_bytes = ?, article_text = ?, content_sha256 = ?, error_code = '',
	lease_expires_at = NULL, next_attempt_at = NULL, completed_at = ?, updated_at = ?
WHERE normalized_url = ? AND status = ?
`, L1NewsArticleFetchStatusReady, strings.TrimSpace(completion.FinalURL), strings.TrimSpace(completion.FetchURL), strings.TrimSpace(completion.ContentType),
		strings.TrimSpace(completion.FetchProvider), strings.TrimSpace(completion.Extractor), completion.RawBytes,
		completion.ArticleText, strings.TrimSpace(completion.ContentSHA256), now, now, normalizedURL, L1NewsArticleFetchStatusFetching)
	if err != nil {
		return fmt.Errorf("failed to complete news article fetch: %w", err)
	}
	affected, _ := result.RowsAffected()
	if affected != 1 {
		return errors.New("news article fetch claim is no longer active")
	}
	if err := syncCompletedArticleToNewsItems(ctx, tx, normalizedURL, completion, now); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit news article completion: %w", err)
	}
	return nil
}

func syncCompletedArticleToNewsItems(ctx context.Context, tx *sql.Tx, normalizedURL string, completion L1NewsArticleFetchCompletion, now time.Time) error {
	rows, err := tx.QueryContext(ctx, `
SELECT id, raw_text, meta_json
FROM l1_news_item
WHERE source_url = ?
`, normalizedURL)
	if err != nil {
		return fmt.Errorf("failed to query news items for completed article: %w", err)
	}
	type newsArticleUpdate struct {
		id       string
		rawText  string
		metaJSON string
	}
	updates := []newsArticleUpdate{}
	for rows.Next() {
		var id, existingRawText, metaJSON string
		if err := rows.Scan(&id, &existingRawText, &metaJSON); err != nil {
			rows.Close()
			return fmt.Errorf("failed to scan news item for completed article: %w", err)
		}
		meta := map[string]interface{}{}
		if err := json.Unmarshal([]byte(metaJSON), &meta); err != nil {
			rows.Close()
			return fmt.Errorf("failed to unmarshal news item meta for completed article: %w", err)
		}
		rawText := completion.ArticleText
		if title := stringMeta(meta, "feed_item_title"); title != "" {
			rawText = title + "\n" + completion.ArticleText
		}
		wasReady := stringMeta(meta, "article_fetch_status") == L1NewsArticleFetchStatusReady
		meta["article_fetch_status"] = L1NewsArticleFetchStatusReady
		meta["article_original_url"] = normalizedURL
		meta["article_final_url"] = strings.TrimSpace(completion.FinalURL)
		meta["article_fetch_url"] = strings.TrimSpace(completion.FetchURL)
		meta["article_content_type"] = strings.TrimSpace(completion.ContentType)
		meta["article_fetch_provider"] = strings.TrimSpace(completion.FetchProvider)
		meta["article_extractor"] = strings.TrimSpace(completion.Extractor)
		meta["article_raw_bytes"] = completion.RawBytes
		meta["article_extracted_chars"] = len([]rune(completion.ArticleText))
		meta["article_content_sha256"] = strings.TrimSpace(completion.ContentSHA256)
		meta["article_fetched_at"] = now.UTC().Format(time.RFC3339Nano)
		if existingRawText == rawText && wasReady {
			continue
		}
		encodedMeta, err := json.Marshal(meta)
		if err != nil {
			rows.Close()
			return fmt.Errorf("failed to marshal news item meta for completed article: %w", err)
		}
		updates = append(updates, newsArticleUpdate{id: id, rawText: rawText, metaJSON: string(encodedMeta)})
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close news item rows for completed article: %w", err)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("failed to iterate news items for completed article: %w", err)
	}
	for _, update := range updates {
		if _, err := tx.ExecContext(ctx, `
UPDATE l1_news_item
SET raw_text = ?, raw_hash = ?, meta_json = ?, updated_at = ?
WHERE id = ?
`, update.rawText, rawTextHash(update.rawText), update.metaJSON, now, update.id); err != nil {
			return fmt.Errorf("failed to sync completed article to news item: %w", err)
		}
	}
	return nil
}

func (s *L1SQLiteStore) FailNewsArticleFetch(ctx context.Context, rawURL string, errorCode string, now time.Time, retryAfter time.Duration) error {
	normalizedURL := normalizeNewsArticleURL(rawURL)
	if normalizedURL == "" {
		return errors.New("news article fetch url is required")
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if retryAfter <= 0 {
		retryAfter = 30 * time.Minute
	}
	now = now.UTC()
	_, err := s.db.ExecContext(ctx, `
UPDATE l1_news_article_fetch
SET status = ?, error_code = ?, lease_expires_at = NULL,
	next_attempt_at = ?, updated_at = ?
WHERE normalized_url = ? AND status = ?
`, L1NewsArticleFetchStatusUnavailable, strings.TrimSpace(errorCode), now.Add(retryAfter), now,
		normalizedURL, L1NewsArticleFetchStatusFetching)
	if err != nil {
		return fmt.Errorf("failed to mark news article fetch unavailable: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) ReopenIncompleteNewsArticleFetch(ctx context.Context, rawURL string, minimumChars int, now time.Time) error {
	normalizedURL := normalizeNewsArticleURL(rawURL)
	if normalizedURL == "" {
		return errors.New("news article fetch url is required")
	}
	if minimumChars <= 0 {
		return nil
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	_, err := s.db.ExecContext(ctx, `
UPDATE l1_news_article_fetch
SET status = ?, error_code = 'incomplete_article_text', completed_at = NULL,
	next_attempt_at = ?, updated_at = ?
WHERE normalized_url = ? AND status = ? AND length(article_text) < ?
  AND extractor <> 'nhk_news_article'
`, L1NewsArticleFetchStatusUnavailable, now, now, normalizedURL, L1NewsArticleFetchStatusReady, minimumChars)
	if err != nil {
		return fmt.Errorf("failed to reopen incomplete news article fetch: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) NewsArticleFetch(ctx context.Context, rawURL string) (*L1NewsArticleFetch, error) {
	normalizedURL := normalizeNewsArticleURL(rawURL)
	if normalizedURL == "" {
		return nil, errors.New("news article fetch url is required")
	}
	row := s.db.QueryRowContext(ctx, `
SELECT normalized_url, status, final_url, fetch_url, content_type, fetch_provider,
	extractor, raw_bytes, article_text, content_sha256, error_code, attempt_count,
	lease_expires_at, next_attempt_at, completed_at, created_at, updated_at
FROM l1_news_article_fetch
WHERE normalized_url = ?
`, normalizedURL)
	var entry L1NewsArticleFetch
	var leaseExpiresAt, nextAttemptAt, completedAt sql.NullTime
	if err := row.Scan(&entry.NormalizedURL, &entry.Status, &entry.FinalURL, &entry.FetchURL, &entry.ContentType,
		&entry.FetchProvider, &entry.Extractor, &entry.RawBytes, &entry.ArticleText,
		&entry.ContentSHA256, &entry.ErrorCode, &entry.AttemptCount, &leaseExpiresAt, &nextAttemptAt,
		&completedAt, &entry.CreatedAt, &entry.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to scan news article fetch: %w", err)
	}
	if leaseExpiresAt.Valid {
		entry.LeaseExpiresAt = leaseExpiresAt.Time
	}
	if nextAttemptAt.Valid {
		entry.NextAttemptAt = nextAttemptAt.Time
	}
	if completedAt.Valid {
		entry.CompletedAt = completedAt.Time
	}
	return &entry, nil
}

func firstNonEmptyL1(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func normalizeNewsArticleURL(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	u.Scheme = strings.ToLower(u.Scheme)
	u.Host = strings.ToLower(u.Host)
	u.Fragment = ""
	return u.String()
}
