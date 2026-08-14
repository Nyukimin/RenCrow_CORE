package archivesqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// ArchiveUserMemoryWithReceipt persists one already-authorized L1 memory event
// and its request receipt in one transaction. The event is copied verbatim;
// this method never accepts or creates a summary/content projection.
//
// The returned bool is true only when the exact same request binding already
// exists. A different request that names an identical archived event is a
// semantic dedupe, but still creates its own receipt and returns false.
func (d *ArchiveSQLiteStore) ArchiveUserMemoryWithReceipt(ctx context.Context, item l1sqlite.L1MemoryEvent, receipt ArchiveRequestReceipt) (bool, error) {
	if d == nil || d.db == nil {
		return false, errors.New("archive sqlite store is closed")
	}
	receipt.RequestID = strings.TrimSpace(receipt.RequestID)
	receipt.UserID = strings.TrimSpace(receipt.UserID)
	receipt.ActorID = strings.TrimSpace(receipt.ActorID)
	receipt.PayloadHash = strings.TrimSpace(receipt.PayloadHash)
	receipt.MemoryID = strings.TrimSpace(receipt.MemoryID)
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	} else {
		receipt.CreatedAt = receipt.CreatedAt.UTC()
	}
	if err := validateArchiveUserMemoryBinding(item, receipt); err != nil {
		return false, err
	}
	metaJSON, err := json.Marshal(item.Meta)
	if err != nil {
		return false, fmt.Errorf("failed to marshal archive user memory meta: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("failed to begin archive user memory transaction: %w", err)
	}
	defer tx.Rollback()

	existingReceipt, found, err := findArchiveRequestReceipt(ctx, tx, receipt.RequestID, "")
	if err != nil {
		return false, err
	}
	if found {
		if !archiveRequestReceiptBindingEqual(existingReceipt, receipt) {
			return false, errors.New("conversation archive request receipt conflict")
		}
		existingEvent, eventFound, err := findArchiveMemoryEvent(ctx, tx, "user:"+receipt.UserID, receipt.MemoryID)
		if err != nil {
			return false, err
		}
		if !eventFound {
			return false, errors.New("conversation archive request receipt references a missing memory")
		}
		if !archiveL1MemoryEventEqual(existingEvent, item) {
			return false, errors.New("conversation archive request receipt memory conflicts with archived event")
		}
		return true, nil
	}

	existingEvent, eventFound, err := findArchiveMemoryEvent(ctx, tx, "user:"+receipt.UserID, receipt.MemoryID)
	if err != nil {
		return false, err
	}
	if eventFound && !archiveL1MemoryEventEqual(existingEvent, item) {
		return false, errors.New("conversation archive memory conflict")
	}
	if !eventFound {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event_archive (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`, item.ID, item.Namespace, item.SessionID, item.ThreadID, string(item.Speaker), item.Message, string(metaJSON),
			item.MemoryState, item.Layer, item.Source, item.CreatedAt, item.UpdatedAt); err != nil {
			return false, fmt.Errorf("failed to archive user memory event: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_archive_request_receipt (
	request_id, user_id, actor_id, payload_hash, memory_id, created_at
) VALUES (?, ?, ?, ?, ?, ?)
`, receipt.RequestID, receipt.UserID, receipt.ActorID, receipt.PayloadHash, receipt.MemoryID, receipt.CreatedAt); err != nil {
		return false, fmt.Errorf("failed to persist conversation archive request receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("failed to commit conversation archive request: %w", err)
	}
	return false, nil
}

// ArchiveUserMemoryWithRequest is an explicit alias for callers that name the
// operation by its trusted request binding.
func (d *ArchiveSQLiteStore) ArchiveUserMemoryWithRequest(ctx context.Context, item l1sqlite.L1MemoryEvent, receipt ArchiveRequestReceipt) (bool, error) {
	return d.ArchiveUserMemoryWithReceipt(ctx, item, receipt)
}

// FindUserMemoryArchive performs an exact user-and-memory lookup. The user
// namespace predicate is part of the SQL query so a cross-user ID never leaks
// an archived row.
func (d *ArchiveSQLiteStore) FindUserMemoryArchive(ctx context.Context, userID, memoryID string) (l1sqlite.L1MemoryEvent, bool, error) {
	if d == nil || d.db == nil {
		return l1sqlite.L1MemoryEvent{}, false, errors.New("archive sqlite store is closed")
	}
	userID = strings.TrimSpace(userID)
	memoryID = strings.TrimSpace(memoryID)
	if userID == "" || memoryID == "" {
		return l1sqlite.L1MemoryEvent{}, false, errors.New("archive user memory user_id and memory_id are required")
	}
	return findArchiveMemoryEvent(ctx, d.db, "user:"+userID, memoryID)
}

// FindArchivedUserMemory is the descriptive alias retained for archive
// readers that use the past-tense name.
func (d *ArchiveSQLiteStore) FindArchivedUserMemory(ctx context.Context, userID, memoryID string) (l1sqlite.L1MemoryEvent, bool, error) {
	return d.FindUserMemoryArchive(ctx, userID, memoryID)
}

// FindArchiveRequestReceipt performs an exact request-and-user lookup.
func (d *ArchiveSQLiteStore) FindArchiveRequestReceipt(ctx context.Context, userID, requestID string) (ArchiveRequestReceipt, bool, error) {
	if d == nil || d.db == nil {
		return ArchiveRequestReceipt{}, false, errors.New("archive sqlite store is closed")
	}
	userID = strings.TrimSpace(userID)
	requestID = strings.TrimSpace(requestID)
	if userID == "" || requestID == "" {
		return ArchiveRequestReceipt{}, false, errors.New("archive request user_id and request_id are required")
	}
	return findArchiveRequestReceipt(ctx, d.db, requestID, userID)
}

// FindConversationArchiveRequest is an explicit alias for the runtime route.
func (d *ArchiveSQLiteStore) FindConversationArchiveRequest(ctx context.Context, userID, requestID string) (ArchiveRequestReceipt, bool, error) {
	return d.FindArchiveRequestReceipt(ctx, userID, requestID)
}

type archiveRowScanner interface {
	Scan(dest ...interface{}) error
}

func validateArchiveUserMemoryBinding(item l1sqlite.L1MemoryEvent, receipt ArchiveRequestReceipt) error {
	if receipt.RequestID == "" || receipt.UserID == "" || receipt.ActorID == "" || receipt.PayloadHash == "" || receipt.MemoryID == "" {
		return errors.New("conversation archive request binding is incomplete")
	}
	if item.ID == "" || item.ID != receipt.MemoryID {
		return errors.New("conversation archive memory_id does not match L1 event")
	}
	if item.Namespace != "user:"+receipt.UserID {
		return errors.New("conversation archive memory namespace does not match authenticated user")
	}
	if item.MemoryState != l1sqlite.MemoryStateConfirmed && item.MemoryState != l1sqlite.MemoryStatePinned {
		return errors.New("only confirmed or pinned user memory may be archived")
	}
	if strings.TrimSpace(item.Message) == "" || strings.TrimSpace(string(item.Speaker)) == "" || strings.TrimSpace(item.Layer) == "" || strings.TrimSpace(item.Source) == "" {
		return errors.New("conversation archive L1 memory event is incomplete")
	}
	if item.CreatedAt.IsZero() || item.UpdatedAt.IsZero() {
		return errors.New("conversation archive L1 memory event timestamps are required")
	}
	return nil
}

func archiveRequestReceiptBindingEqual(left, right ArchiveRequestReceipt) bool {
	return left.RequestID == right.RequestID && left.UserID == right.UserID && left.ActorID == right.ActorID && left.PayloadHash == right.PayloadHash && left.MemoryID == right.MemoryID
}

func findArchiveRequestReceipt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, requestID, userID string) (ArchiveRequestReceipt, bool, error) {
	query := `SELECT request_id, user_id, actor_id, payload_hash, memory_id, created_at FROM conversation_archive_request_receipt WHERE request_id = ?`
	args := []interface{}{requestID}
	if userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	var receipt ArchiveRequestReceipt
	if err := queryer.QueryRowContext(ctx, query, args...).Scan(&receipt.RequestID, &receipt.UserID, &receipt.ActorID, &receipt.PayloadHash, &receipt.MemoryID, &receipt.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ArchiveRequestReceipt{}, false, nil
		}
		return ArchiveRequestReceipt{}, false, fmt.Errorf("failed to read conversation archive request receipt: %w", err)
	}
	return receipt, true, nil
}

func findArchiveMemoryEvent(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, namespace, memoryID string) (l1sqlite.L1MemoryEvent, bool, error) {
	row := queryer.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event_archive
WHERE namespace = ? AND id = ?
`, namespace, memoryID)
	event, err := scanArchiveMemoryEvent(row)
	if errors.Is(err, sql.ErrNoRows) {
		return l1sqlite.L1MemoryEvent{}, false, nil
	}
	if err != nil {
		return l1sqlite.L1MemoryEvent{}, false, fmt.Errorf("failed to read conversation archive memory: %w", err)
	}
	return event, true, nil
}

func scanArchiveMemoryEvent(row archiveRowScanner) (l1sqlite.L1MemoryEvent, error) {
	var item l1sqlite.L1MemoryEvent
	var speaker string
	var metaJSON sql.NullString
	var createdAt, updatedAt sql.NullTime
	if err := row.Scan(&item.ID, &item.Namespace, &item.SessionID, &item.ThreadID, &speaker, &item.Message, &metaJSON,
		&item.MemoryState, &item.Layer, &item.Source, &createdAt, &updatedAt); err != nil {
		return l1sqlite.L1MemoryEvent{}, err
	}
	item.Speaker = domconv.Speaker(speaker)
	item.CreatedAt = archiveNullTime(createdAt)
	item.UpdatedAt = archiveNullTime(updatedAt)
	if metaJSON.Valid && strings.TrimSpace(metaJSON.String) != "" && strings.TrimSpace(metaJSON.String) != "null" {
		item.Meta = map[string]interface{}{}
		if err := json.Unmarshal([]byte(metaJSON.String), &item.Meta); err != nil {
			return l1sqlite.L1MemoryEvent{}, fmt.Errorf("failed to unmarshal conversation archive memory meta: %w", err)
		}
	}
	return item, nil
}

func archiveNullTime(value sql.NullTime) time.Time {
	if value.Valid {
		return value.Time
	}
	return time.Time{}
}

func archiveL1MemoryEventEqual(left, right l1sqlite.L1MemoryEvent) bool {
	return left.ID == right.ID && left.Namespace == right.Namespace && left.SessionID == right.SessionID && left.ThreadID == right.ThreadID && left.Speaker == right.Speaker && left.Message == right.Message && canonicalArchiveJSON(left.Meta) == canonicalArchiveJSON(right.Meta) && left.MemoryState == right.MemoryState && left.Layer == right.Layer && left.Source == right.Source && left.CreatedAt.Equal(right.CreatedAt) && left.UpdatedAt.Equal(right.UpdatedAt)
}

func canonicalArchiveJSON(value interface{}) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "<invalid>"
	}
	return string(encoded)
}

func (d *ArchiveSQLiteStore) ArchiveL1MemoryEvents(ctx context.Context, items []l1sqlite.L1MemoryEvent) error {
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

func (d *ArchiveSQLiteStore) ArchiveL1NewsItems(ctx context.Context, items []l1sqlite.L1NewsItem) error {
	for _, item := range items {
		keywordsJSON, metaJSON, err := marshalArchiveJSON(item.Keywords, item.Meta)
		if err != nil {
			return err
		}
		var publishedAt any
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

func (d *ArchiveSQLiteStore) ArchiveL1KnowledgeItems(ctx context.Context, items []l1sqlite.L1KnowledgeItem) error {
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

func (d *ArchiveSQLiteStore) SearchKnowledgeArchiveFTS(ctx context.Context, domain string, query string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	if err := l1sqlite.ValidateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	domain = l1sqlite.NormalizeNewsCategory(domain)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("archive sqlite knowledge fts query is required")
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
ORDER BY k.updated_at DESC, k.rowid DESC
LIMIT ?
`, l1sqlite.LikeQuery(query), l1sqlite.LikeQuery(query), l1sqlite.LikeQuery(query), l1sqlite.LikeQuery(query), domain, limit)
	if err != nil {
		return nil, fmt.Errorf("failed to search archive sqlite knowledge fts: %w", err)
	}
	defer rows.Close()
	return l1sqlite.ScanL1KnowledgeItems(rows)
}

func (d *ArchiveSQLiteStore) ArchiveL1StagingItems(ctx context.Context, items []l1sqlite.L1StagingItem) error {
	for _, item := range items {
		keywordsJSON, metaJSON, err := marshalArchiveJSON(item.Keywords, item.Meta)
		if err != nil {
			return err
		}
		var publishedAt any
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

func marshalArchiveJSON(keywords []string, meta map[string]any) (string, string, error) {
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
