package conversation

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func (s *L1SQLiteStore) SaveStagingItem(ctx context.Context, item L1StagingItem) (*L1StagingItem, error) {
	item.Kind = strings.TrimSpace(item.Kind)
	item.Namespace = strings.TrimSpace(item.Namespace)
	item.EventID = strings.TrimSpace(item.EventID)
	item.SourceID = strings.TrimSpace(item.SourceID)
	item.SourceURL = strings.TrimSpace(item.SourceURL)
	item.ValidationStatus = strings.TrimSpace(item.ValidationStatus)
	if item.ValidationStatus == "" {
		item.ValidationStatus = L1StagingStatusPending
	}
	if err := validateL1StagingKind(item.Kind); err != nil {
		return nil, err
	}
	if err := ValidateL1Namespace(item.Namespace); err != nil {
		return nil, err
	}
	if item.EventID == "" {
		return nil, errors.New("l1 staging event_id is required")
	}
	if item.SourceID == "" {
		return nil, errors.New("l1 staging source_id is required")
	}
	if err := validateOptionalSourceURL(item.SourceURL); err != nil {
		return nil, err
	}
	if strings.TrimSpace(item.RawText) == "" {
		return nil, errors.New("l1 staging raw_text is required")
	}
	if err := validateL1StagingStatus(item.ValidationStatus); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	if item.FetchedAt.IsZero() {
		item.FetchedAt = now
	}
	item.FetchedAt = item.FetchedAt.UTC()
	if !item.PublishedAt.IsZero() {
		item.PublishedAt = item.PublishedAt.UTC()
	}
	item.RawHash = rawTextHash(item.RawText)
	if item.ID == "" {
		item.ID = fmt.Sprintf("%s:%s:%s", item.Namespace, item.EventID, item.RawHash[:12])
	}
	if item.Meta == nil {
		item.Meta = map[string]interface{}{}
	}
	item.CreatedAt = now
	item.UpdatedAt = now
	keywordsJSON, err := json.Marshal(item.Keywords)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 staging keywords: %w", err)
	}
	metaJSON, err := json.Marshal(item.Meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 staging meta: %w", err)
	}
	var publishedAt interface{}
	if !item.PublishedAt.IsZero() {
		publishedAt = item.PublishedAt
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO l1_staging_item (
	id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note,
	validation_status, meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(namespace, event_id) DO UPDATE SET
	kind = excluded.kind,
	source_id = excluded.source_id,
	source_url = excluded.source_url,
	fetched_at = excluded.fetched_at,
	published_at = excluded.published_at,
	raw_text = excluded.raw_text,
	raw_hash = excluded.raw_hash,
	summary_draft = excluded.summary_draft,
	keywords_json = excluded.keywords_json,
	license_note = excluded.license_note,
	validation_status = excluded.validation_status,
	meta_json = excluded.meta_json,
	updated_at = excluded.updated_at
`, item.ID, item.Kind, item.Namespace, item.EventID, item.SourceID, item.SourceURL, item.FetchedAt, publishedAt,
		item.RawText, item.RawHash, item.SummaryDraft, string(keywordsJSON), item.LicenseNote,
		item.ValidationStatus, string(metaJSON), item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("failed to save l1 staging item: %w", err)
	}
	if _, err := s.AppendEvent(ctx, "staging.item_saved", item.Namespace, "", 0, map[string]interface{}{
		"staging_id":        item.ID,
		"kind":              item.Kind,
		"event_id":          item.EventID,
		"source_id":         item.SourceID,
		"source_url":        item.SourceURL,
		"raw_hash":          item.RawHash,
		"validation_status": item.ValidationStatus,
	}, "staging"); err != nil {
		return nil, fmt.Errorf("failed to append l1 staging event log: %w", err)
	}
	if err := s.archiveStagingItem(ctx, item); err != nil {
		return nil, err
	}
	return &item, nil
}

func (s *L1SQLiteStore) RecentStagingItems(ctx context.Context, validationStatus string, limit int) ([]L1StagingItem, error) {
	if err := validateL1StagingStatus(validationStatus); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note,
       validation_status, meta_json, created_at, updated_at
FROM l1_staging_item
WHERE validation_status = ?
ORDER BY created_at DESC
LIMIT ?
`, validationStatus, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 staging items: %w", err)
	}
	defer rows.Close()
	return scanL1StagingItems(rows)
}

func (s *L1SQLiteStore) ExportStagingItemsJSONL(ctx context.Context, validationStatus string, writer io.Writer) error {
	if writer == nil {
		return errors.New("l1 staging JSONL writer is required")
	}
	items, err := s.RecentStagingItems(ctx, validationStatus, 0)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(writer)
	for _, item := range items {
		if err := encoder.Encode(item); err != nil {
			return fmt.Errorf("failed to encode l1 staging JSONL item: %w", err)
		}
	}
	return nil
}

func (s *L1SQLiteStore) ImportStagingItemsJSONL(ctx context.Context, reader io.Reader) (int, error) {
	if reader == nil {
		return 0, errors.New("l1 staging JSONL reader is required")
	}
	scanner := bufio.NewScanner(reader)
	imported := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var item L1StagingItem
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			return imported, fmt.Errorf("failed to decode l1 staging JSONL item: %w", err)
		}
		if _, err := s.SaveStagingItem(ctx, item); err != nil {
			return imported, err
		}
		imported++
	}
	if err := scanner.Err(); err != nil {
		return imported, fmt.Errorf("failed to scan l1 staging JSONL: %w", err)
	}
	return imported, nil
}

func (s *L1SQLiteStore) stagingItemByID(ctx context.Context, id string) (*L1StagingItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note,
       validation_status, meta_json, created_at, updated_at
FROM l1_staging_item
WHERE id = ?
LIMIT 1
`, id)
	if err != nil {
		return nil, fmt.Errorf("failed to query l1 staging item by id: %w", err)
	}
	defer rows.Close()
	items, err := scanL1StagingItems(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return &items[0], nil
}
