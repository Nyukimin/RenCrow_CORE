//go:build linux && amd64

package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	_ "github.com/marcboeker/go-duckdb"
)

func (d *DuckDBStore) ArchiveL1MemoryEvents(ctx context.Context, items []L1MemoryEvent) error {
	for _, item := range items {
		metaJSON, err := json.Marshal(item.Meta)
		if err != nil {
			return fmt.Errorf("failed to marshal l1 memory archive meta: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `DELETE FROM l1_memory_event_archive WHERE id = ?`, item.ID); err != nil {
			return fmt.Errorf("failed to replace l1 memory archive row: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO l1_memory_event_archive (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ID, item.Namespace, item.SessionID, item.ThreadID, string(item.Speaker), item.Message, string(metaJSON),
			item.MemoryState, item.Layer, item.Source, item.CreatedAt, item.UpdatedAt); err != nil {
			return fmt.Errorf("failed to archive l1 memory event: %w", err)
		}
	}
	return nil
}

func (d *DuckDBStore) ArchiveL1NewsItems(ctx context.Context, items []L1NewsItem) error {
	for _, item := range items {
		keywordsJSON, metaJSON, err := marshalArchiveJSON(item.Keywords, item.Meta)
		if err != nil {
			return err
		}
		var publishedAt interface{}
		if !item.PublishedAt.IsZero() {
			publishedAt = item.PublishedAt
		}
		if _, err := d.db.ExecContext(ctx, `DELETE FROM l1_news_item_archive WHERE id = ?`, item.ID); err != nil {
			return fmt.Errorf("failed to replace l1 news archive row: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO l1_news_item_archive (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ID, item.StagingID, item.Category, item.SourceID, item.SourceURL, publishedAt, item.FetchedAt,
			item.RawText, item.RawHash, item.SummaryDraft, keywordsJSON, item.LicenseNote, metaJSON,
			item.CreatedAt, item.UpdatedAt); err != nil {
			return fmt.Errorf("failed to archive l1 news item: %w", err)
		}
	}
	return nil
}

func (d *DuckDBStore) ArchiveL1KnowledgeItems(ctx context.Context, items []L1KnowledgeItem) error {
	for _, item := range items {
		keywordsJSON, metaJSON, err := marshalArchiveJSON(item.Keywords, item.Meta)
		if err != nil {
			return err
		}
		if _, err := d.db.ExecContext(ctx, `DELETE FROM l1_knowledge_item_archive WHERE id = ?`, item.ID); err != nil {
			return fmt.Errorf("failed to replace l1 knowledge archive row: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `DELETE FROM l1_knowledge_item_fts_archive WHERE id = ?`, item.ID); err != nil {
			return fmt.Errorf("failed to replace l1 knowledge fts archive row: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO l1_knowledge_item_archive (
	id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash,
	summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ID, item.StagingID, item.Domain, item.Title, item.SourceID, item.SourceURL, item.RawText, item.RawHash,
			item.SummaryDraft, keywordsJSON, item.LicenseNote, metaJSON, item.CreatedAt, item.UpdatedAt); err != nil {
			return fmt.Errorf("failed to archive l1 knowledge item: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO l1_knowledge_item_fts_archive (id, domain, title, raw_text, summary_draft, keywords_text)
VALUES (?, ?, ?, ?, ?, ?)
`, item.ID, item.Domain, item.Title, item.RawText, item.SummaryDraft, strings.Join(item.Keywords, " ")); err != nil {
			return fmt.Errorf("failed to archive l1 knowledge fts item: %w", err)
		}
	}
	return nil
}

func (d *DuckDBStore) SearchKnowledgeArchiveFTS(ctx context.Context, domain string, query string, limit int) ([]L1KnowledgeItem, error) {
	if err := validateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	domain = normalizeNewsCategory(domain)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("duckdb knowledge fts query is required")
	}
	if limit <= 0 {
		limit = 20
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT k.id, k.staging_id, k.domain, k.title, k.source_id, k.source_url, k.raw_text, k.raw_hash,
       k.summary_draft, k.keywords_json, k.license_note, k.meta_json, k.created_at, k.updated_at
FROM l1_knowledge_item_fts_archive f
JOIN l1_knowledge_item_archive k ON k.id = f.id
WHERE (
	f.title LIKE ?
	OR f.raw_text LIKE ?
	OR f.summary_draft LIKE ?
	OR f.keywords_text LIKE ?
)
  AND f.domain = ?
ORDER BY k.updated_at DESC
LIMIT ?
`, likeQuery(query), likeQuery(query), likeQuery(query), likeQuery(query), domain, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search duckdb knowledge fts archive: %w", err)
	}
	defer rows.Close()
	return scanL1KnowledgeItems(rows)
}

func (d *DuckDBStore) ArchiveL1StagingItems(ctx context.Context, items []L1StagingItem) error {
	for _, item := range items {
		keywordsJSON, metaJSON, err := marshalArchiveJSON(item.Keywords, item.Meta)
		if err != nil {
			return err
		}
		var publishedAt interface{}
		if !item.PublishedAt.IsZero() {
			publishedAt = item.PublishedAt
		}
		if _, err := d.db.ExecContext(ctx, `DELETE FROM l1_staging_item_archive WHERE id = ?`, item.ID); err != nil {
			return fmt.Errorf("failed to replace l1 staging archive row: %w", err)
		}
		if _, err := d.db.ExecContext(ctx, `
INSERT INTO l1_staging_item_archive (
	id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note,
	validation_status, meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ID, item.Kind, item.Namespace, item.EventID, item.SourceID, item.SourceURL, item.FetchedAt, publishedAt,
			item.RawText, item.RawHash, item.SummaryDraft, keywordsJSON, item.LicenseNote,
			item.ValidationStatus, metaJSON, item.CreatedAt, item.UpdatedAt); err != nil {
			return fmt.Errorf("failed to archive l1 staging item: %w", err)
		}
	}
	return nil
}

func marshalArchiveJSON(keywords []string, meta map[string]interface{}) (string, string, error) {
	keywordsJSON, err := json.Marshal(keywords)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal l1 archive keywords: %w", err)
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal l1 archive meta: %w", err)
	}
	return string(keywordsJSON), string(metaJSON), nil
}
