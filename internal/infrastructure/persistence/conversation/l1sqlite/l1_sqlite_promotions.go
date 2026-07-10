package l1sqlite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	_ "modernc.org/sqlite"
)

func (s *L1SQLiteStore) PromoteValidatedStagingItemToMemory(ctx context.Context, id string, targetNamespace string, promotedBy string) (*L1MemoryEvent, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("l1 staging item id is required")
	}
	if err := ValidateL1Namespace(targetNamespace); err != nil {
		return nil, err
	}
	item, err := s.stagingItemByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.ValidationStatus != L1StagingStatusValidated {
		return nil, fmt.Errorf("l1 staging item must be validated before promotion: %s", item.ValidationStatus)
	}
	message := strings.TrimSpace(item.SummaryDraft)
	if message == "" {
		message = strings.TrimSpace(item.RawText)
	}
	if message == "" {
		return nil, errors.New("l1 staging item has no promotable text")
	}
	now := time.Now().UTC()
	meta := map[string]interface{}{}
	for k, v := range item.Meta {
		meta[k] = v
	}
	meta["staging_id"] = item.ID
	meta["staging_kind"] = item.Kind
	meta["staging_namespace"] = item.Namespace
	meta["event_id"] = item.EventID
	meta["source_id"] = item.SourceID
	meta["source_url"] = item.SourceURL
	meta["raw_hash"] = item.RawHash
	meta["license_note"] = item.LicenseNote
	meta["promoted_by"] = promotedBy
	meta["validation_status"] = item.ValidationStatus
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal l1 staging promoted memory meta")
	if err != nil {
		return nil, err
	}
	sessionID := metaString(item.Meta, "session_id")
	threadID := metaInt64(item.Meta, "thread_id")
	promoted := &L1MemoryEvent{
		ID:          fmt.Sprintf("%s:%s:%d", targetNamespace, item.ID, now.UnixNano()),
		Namespace:   targetNamespace,
		SessionID:   sessionID,
		ThreadID:    threadID,
		Speaker:     domconv.SpeakerMemory,
		Message:     message,
		Meta:        meta,
		MemoryState: MemoryStateConfirmed,
		Layer:       MemoryLayerL1,
		Source:      "promoter",
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := validateL1MemoryEvent(*promoted); err != nil {
		return nil, err
	}
	// SQLite is the source of truth for promotion. The promoted row and event log
	// commit atomically; DuckDB archive sync follows the commit and keeps the
	// existing error-return semantics as a best-effort downstream follower.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_memory_event (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, promoted.ID, promoted.Namespace, promoted.SessionID, promoted.ThreadID, string(promoted.Speaker), promoted.Message, metaJSON,
		promoted.MemoryState, promoted.Layer, promoted.Source, promoted.CreatedAt, promoted.UpdatedAt)
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to promote l1 staging item to memory: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "memory.promoted_from_staging", targetNamespace, sessionID, threadID, map[string]interface{}{
		"staging_id":         item.ID,
		"promoted_memory_id": promoted.ID,
		"promoted_by":        promotedBy,
		"source_namespace":   item.Namespace,
		"memory_state":       promoted.MemoryState,
	}, "promoter"); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to append l1 staging promoted event log: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to commit l1 staging memory promotion transaction: %w", err))
	}
	if err := s.archivePromotedMemory(ctx, *promoted); err != nil {
		return nil, err
	}
	return promoted, nil
}

func (s *L1SQLiteStore) PromoteValidatedStagingItemToNews(ctx context.Context, id string, category string) (*L1NewsItem, error) {
	id = strings.TrimSpace(id)
	category = NormalizeNewsCategory(category)
	if id == "" {
		return nil, errors.New("l1 staging item id is required")
	}
	if category == "" {
		return nil, errors.New("l1 news category is required")
	}
	item, err := s.stagingItemByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.ValidationStatus != L1StagingStatusValidated {
		return nil, fmt.Errorf("l1 staging item must be validated before news promotion: %s", item.ValidationStatus)
	}
	if item.Kind != L1StagingKindExternalFetch && item.Kind != L1StagingKindSearchResult {
		return nil, fmt.Errorf("l1 staging item kind cannot be promoted to news: %s", item.Kind)
	}
	now := time.Now().UTC()
	meta := map[string]interface{}{}
	for k, v := range item.Meta {
		meta[k] = v
	}
	meta["staging_id"] = item.ID
	meta["staging_namespace"] = item.Namespace
	meta["event_id"] = item.EventID
	meta["validation_status"] = item.ValidationStatus
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal l1 news meta")
	if err != nil {
		return nil, err
	}
	keywordsJSON, err := json.Marshal(item.Keywords)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 news keywords: %w", err)
	}
	news := &L1NewsItem{
		ID:           fmt.Sprintf("news:%s:%s", item.EventID, item.RawHash[:12]),
		StagingID:    item.ID,
		Category:     category,
		SourceID:     item.SourceID,
		SourceURL:    item.SourceURL,
		PublishedAt:  item.PublishedAt,
		FetchedAt:    item.FetchedAt,
		RawText:      item.RawText,
		RawHash:      item.RawHash,
		SummaryDraft: item.SummaryDraft,
		Keywords:     item.Keywords,
		LicenseNote:  item.LicenseNote,
		Meta:         meta,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	var publishedAt interface{}
	if !news.PublishedAt.IsZero() {
		publishedAt = news.PublishedAt
	}
	newsNamespace, err := BuildL1Namespace(NamespaceKindKnowledge, "news")
	if err != nil {
		return nil, err
	}
	// SQLite is the source of truth for promotion. The news row and event log
	// commit atomically; DuckDB archive sync follows the commit and keeps the
	// existing error-return semantics as a best-effort downstream follower.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_news_item (
	id, staging_id, category, source_id, source_url, published_at, fetched_at,
	raw_text, raw_hash, summary_draft, keywords_json, license_note, meta_json,
	created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(staging_id) DO UPDATE SET
	category = excluded.category,
	source_id = excluded.source_id,
	source_url = excluded.source_url,
	published_at = excluded.published_at,
	fetched_at = excluded.fetched_at,
	raw_text = excluded.raw_text,
	raw_hash = excluded.raw_hash,
	summary_draft = excluded.summary_draft,
	keywords_json = excluded.keywords_json,
	license_note = excluded.license_note,
	meta_json = excluded.meta_json,
	updated_at = excluded.updated_at
`, news.ID, news.StagingID, news.Category, news.SourceID, news.SourceURL, publishedAt, news.FetchedAt,
		news.RawText, news.RawHash, news.SummaryDraft, string(keywordsJSON), news.LicenseNote, metaJSON,
		news.CreatedAt, news.UpdatedAt)
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to promote l1 staging item to news: %w", err))
	}
	if _, err := appendL1EventLog(ctx, tx, "news.promoted_from_staging", newsNamespace, "", 0, map[string]interface{}{
		"news_id":    news.ID,
		"staging_id": item.ID,
		"category":   news.Category,
		"source_id":  news.SourceID,
		"raw_hash":   news.RawHash,
	}, "promoter"); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to append l1 news promoted event log: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to commit l1 staging news promotion transaction: %w", err))
	}
	if err := s.archivePromotedNews(ctx, *news); err != nil {
		return nil, err
	}
	return news, nil
}

func (s *L1SQLiteStore) PromoteValidatedStagingItemToKnowledge(ctx context.Context, id string, domain string) (*L1KnowledgeItem, error) {
	id = strings.TrimSpace(id)
	if err := ValidateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	domain = NormalizeNewsCategory(domain)
	if id == "" {
		return nil, errors.New("l1 staging item id is required")
	}
	if err := ValidateL1Namespace("kb:" + domain); err != nil {
		return nil, err
	}
	item, err := s.stagingItemByID(ctx, id)
	if err != nil {
		return nil, err
	}
	if item.ValidationStatus != L1StagingStatusValidated {
		return nil, fmt.Errorf("l1 staging item must be validated before knowledge promotion: %s", item.ValidationStatus)
	}
	title := strings.TrimSpace(metaString(item.Meta, "title"))
	if title == "" {
		title = item.EventID
	}
	now := time.Now().UTC()
	meta := map[string]interface{}{}
	for k, v := range item.Meta {
		meta[k] = v
	}
	meta["staging_id"] = item.ID
	meta["staging_namespace"] = item.Namespace
	meta["event_id"] = item.EventID
	meta["validation_status"] = item.ValidationStatus
	metaJSON, err := marshalL1MetaJSON(meta, "failed to marshal l1 knowledge meta")
	if err != nil {
		return nil, err
	}
	keywordsJSON, err := json.Marshal(item.Keywords)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 knowledge keywords: %w", err)
	}
	kb := &L1KnowledgeItem{
		ID:           fmt.Sprintf("kb:%s:%s:%s", domain, item.EventID, item.RawHash[:12]),
		StagingID:    item.ID,
		Domain:       domain,
		Title:        title,
		SourceID:     item.SourceID,
		SourceURL:    item.SourceURL,
		RawText:      item.RawText,
		RawHash:      item.RawHash,
		SummaryDraft: item.SummaryDraft,
		Keywords:     item.Keywords,
		LicenseNote:  item.LicenseNote,
		Meta:         meta,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	namespace, err := BuildL1Namespace(NamespaceKindKnowledge, domain)
	if err != nil {
		return nil, err
	}
	// SQLite is the source of truth for promotion. The knowledge row, FTS row,
	// and event log commit atomically; DuckDB archive and vector sync follow the
	// commit and keep the existing error-return semantics as best-effort followers.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_knowledge_item (
	id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash,
	summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(staging_id) DO UPDATE SET
	domain = excluded.domain,
	title = excluded.title,
	source_id = excluded.source_id,
	source_url = excluded.source_url,
	raw_text = excluded.raw_text,
	raw_hash = excluded.raw_hash,
	summary_draft = excluded.summary_draft,
	keywords_json = excluded.keywords_json,
	license_note = excluded.license_note,
	meta_json = excluded.meta_json,
	updated_at = excluded.updated_at
`, kb.ID, kb.StagingID, kb.Domain, kb.Title, kb.SourceID, kb.SourceURL, kb.RawText, kb.RawHash,
		kb.SummaryDraft, string(keywordsJSON), kb.LicenseNote, metaJSON, kb.CreatedAt, kb.UpdatedAt)
	if err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to promote l1 staging item to knowledge: %w", err))
	}
	if err := upsertKnowledgeFTS(ctx, tx, kb); err != nil {
		return nil, rollbackL1Tx(tx, err)
	}
	if _, err := appendL1EventLog(ctx, tx, "knowledge.promoted_from_staging", namespace, "", 0, map[string]interface{}{
		"knowledge_id": kb.ID,
		"staging_id":   item.ID,
		"domain":       kb.Domain,
		"title":        kb.Title,
		"source_id":    kb.SourceID,
	}, "promoter"); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to append l1 knowledge promoted event log: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return nil, rollbackL1Tx(tx, fmt.Errorf("failed to commit l1 staging knowledge promotion transaction: %w", err))
	}
	if err := s.archivePromotedKnowledge(ctx, *kb); err != nil {
		return nil, err
	}
	if err := s.syncPromotedKnowledgeVector(ctx, *kb); err != nil {
		return nil, err
	}
	return kb, nil
}

func (s *L1SQLiteStore) archivePromotedMemory(ctx context.Context, item L1MemoryEvent) error {
	if s.archiveStore == nil {
		return nil
	}
	if err := s.archiveStore.ArchiveL1MemoryEvents(ctx, []L1MemoryEvent{item}); err != nil {
		return fmt.Errorf("failed to archive promoted l1 memory: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) archivePromotedNews(ctx context.Context, item L1NewsItem) error {
	if s.archiveStore == nil {
		return nil
	}
	if err := s.archiveStore.ArchiveL1NewsItems(ctx, []L1NewsItem{item}); err != nil {
		return fmt.Errorf("failed to archive promoted l1 news: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) archivePromotedKnowledge(ctx context.Context, item L1KnowledgeItem) error {
	if s.archiveStore == nil {
		return nil
	}
	if err := s.archiveStore.ArchiveL1KnowledgeItems(ctx, []L1KnowledgeItem{item}); err != nil {
		return fmt.Errorf("failed to archive promoted l1 knowledge: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) syncPromotedKnowledgeVector(ctx context.Context, item L1KnowledgeItem) error {
	if s.knowledgeVectorSink == nil {
		return nil
	}
	if err := s.knowledgeVectorSink.SaveL1KnowledgeItem(ctx, item); err != nil {
		return fmt.Errorf("failed to sync promoted l1 knowledge to vector sink: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) archiveStagingItem(ctx context.Context, item L1StagingItem) error {
	if s.archiveStore == nil {
		return nil
	}
	if err := s.archiveStore.ArchiveL1StagingItems(ctx, []L1StagingItem{item}); err != nil {
		return fmt.Errorf("failed to archive l1 staging item: %w", err)
	}
	return nil
}
