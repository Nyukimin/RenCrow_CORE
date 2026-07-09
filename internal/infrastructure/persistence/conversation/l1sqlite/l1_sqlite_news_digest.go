package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func (s *L1SQLiteStore) RecentNewsItems(ctx context.Context, category string, limit int) ([]L1NewsItem, error) {
	category = NormalizeNewsCategory(category)
	if limit <= 0 {
		limit = 20
	}
	query := `
SELECT id, staging_id, category, source_id, source_url, published_at, fetched_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
       created_at, updated_at
FROM l1_news_item
`
	var args []interface{}
	if category != "" {
		query += "WHERE category = ?\n"
		args = append(args, category)
	}
	query += "ORDER BY COALESCE(published_at, fetched_at) DESC, created_at DESC\nLIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 news items: %w", err)
	}
	defer rows.Close()
	return scanL1NewsItems(rows)
}

func (s *L1SQLiteStore) BuildDailyDigest(ctx context.Context, digestDate time.Time, category string, limit int) (*L1DailyDigest, error) {
	return s.BuildDailyDigestForSlot(ctx, digestDate, category, L1DailyDigestSlotDay, limit)
}

func (s *L1SQLiteStore) BuildDailyDigestForSlot(ctx context.Context, digestDate time.Time, category string, digestSlot string, limit int) (*L1DailyDigest, error) {
	category = NormalizeNewsCategory(category)
	if category == "" {
		return nil, errors.New("l1 daily digest category is required")
	}
	digestSlot = normalizeDailyDigestSlot(digestSlot)
	if digestSlot == "" {
		return nil, errors.New("l1 daily digest slot is required")
	}
	if digestDate.IsZero() {
		digestDate = time.Now().UTC()
	}
	dateText := digestDate.UTC().Format("2006-01-02")
	if limit <= 0 {
		limit = 20
	}
	query := `
SELECT id, staging_id, category, source_id, source_url, published_at, fetched_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
       created_at, updated_at
FROM l1_news_item
WHERE category = ?
  AND date(COALESCE(published_at, fetched_at)) = date(?)
`
	args := []interface{}{category, dateText}
	if digestSlot != L1DailyDigestSlotDay {
		startHour, endHour := dailyDigestSlotHourRange(digestSlot)
		query += `  AND CAST(strftime('%H', COALESCE(published_at, fetched_at)) AS INTEGER) >= ?
  AND CAST(strftime('%H', COALESCE(published_at, fetched_at)) AS INTEGER) < ?
`
		args = append(args, startHour, endHour)
	}
	query += `
ORDER BY COALESCE(published_at, fetched_at) DESC, created_at DESC
LIMIT ?
`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 news items for digest: %w", err)
	}
	news, err := scanL1NewsItems(rows)
	rows.Close()
	if err != nil {
		return nil, err
	}
	if len(news) == 0 {
		return nil, errors.New("l1 daily digest requires at least one news item")
	}
	newsIDs := make([]string, 0, len(news))
	lines := make([]string, 0, len(news))
	for _, item := range news {
		newsIDs = append(newsIDs, item.ID)
		text := strings.TrimSpace(item.SummaryDraft)
		if text == "" {
			text = strings.TrimSpace(item.RawText)
		}
		lines = append(lines, "- "+text)
	}
	digestText := strings.Join(lines, "\n")
	if s.dailyDigestSummarizer != nil {
		if summarized, err := s.dailyDigestSummarizer.SummarizeDailyDigest(ctx, digestDate, category, digestSlot, news); err == nil && strings.TrimSpace(summarized) != "" {
			digestText = strings.TrimSpace(summarized)
		}
	}
	newsIDsJSON, err := json.Marshal(newsIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 daily digest news ids: %w", err)
	}
	now := time.Now().UTC()
	digest := &L1DailyDigest{
		ID:         fmt.Sprintf("digest:%s:%s", dateText, category),
		DigestDate: dateText,
		Category:   category,
		DigestSlot: digestSlot,
		NewsIDs:    newsIDs,
		DigestText: digestText,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if digestSlot != L1DailyDigestSlotDay {
		digest.ID = fmt.Sprintf("digest:%s:%s:%s", dateText, category, digestSlot)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_daily_digest (
	id, digest_date, category, digest_slot, news_ids_json, digest_text, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(digest_date, category, digest_slot) DO UPDATE SET
	news_ids_json = excluded.news_ids_json,
	digest_text = excluded.digest_text,
	updated_at = excluded.updated_at
`, digest.ID, digest.DigestDate, digest.Category, digest.DigestSlot, string(newsIDsJSON), digest.DigestText, digest.CreatedAt, digest.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save l1 daily digest: %w", err)
	}
	newsNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, "news")
	if err != nil {
		return nil, err
	}
	if _, err := s.AppendEvent(ctx, "news.daily_digest_built", newsNamespace, "", 0, map[string]interface{}{
		"digest_id":   digest.ID,
		"digest_date": digest.DigestDate,
		"category":    digest.Category,
		"digest_slot": digest.DigestSlot,
		"news_ids":    digest.NewsIDs,
	}, "daily_digest"); err != nil {
		return nil, fmt.Errorf("failed to append l1 daily digest event log: %w", err)
	}
	return digest, nil
}

func (s *L1SQLiteStore) RecentDailyDigests(ctx context.Context, category string, limit int) ([]L1DailyDigest, error) {
	category = NormalizeNewsCategory(category)
	if limit <= 0 {
		limit = 20
	}
	query := `
SELECT id, digest_date, category, digest_slot, news_ids_json, digest_text, created_at, updated_at
FROM l1_daily_digest
`
	var args []interface{}
	if category != "" {
		query += "WHERE category = ?\n"
		args = append(args, category)
	}
	query += "ORDER BY digest_date DESC, created_at DESC\nLIMIT ?"
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 daily digests: %w", err)
	}
	defer rows.Close()
	return scanL1DailyDigests(rows)
}

func NormalizeNewsCategory(category string) string {
	return strings.Join(strings.Fields(strings.ToLower(category)), "-")
}

func normalizeDailyDigestSlot(slot string) string {
	slot = strings.ToLower(strings.TrimSpace(slot))
	if slot == "" {
		return L1DailyDigestSlotDay
	}
	switch slot {
	case L1DailyDigestSlotDay, L1DailyDigestSlotMorning, L1DailyDigestSlotNoon, L1DailyDigestSlotEvening:
		return slot
	default:
		return ""
	}
}

func dailyDigestSlotHourRange(slot string) (startHour, endHour int) {
	switch slot {
	case L1DailyDigestSlotMorning:
		return 0, 12
	case L1DailyDigestSlotNoon:
		return 12, 18
	case L1DailyDigestSlotEvening:
		return 18, 24
	default:
		return 0, 24
	}
}
