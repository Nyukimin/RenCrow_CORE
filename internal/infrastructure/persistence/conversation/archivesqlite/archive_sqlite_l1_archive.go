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
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
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
		return false, fmt.Errorf("%w: archive sqlite store is closed", domainmemory.ErrUserMemoryOwnerUnavailable)
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
		return false, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerInvalid, err)
	}
	metaJSON, err := json.Marshal(item.Meta)
	if err != nil {
		return false, fmt.Errorf("failed to marshal archive user memory meta: %w", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("%w: failed to begin archive user memory transaction: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	defer tx.Rollback()

	existingReceipt, found, err := findArchiveRequestReceipt(ctx, tx, receipt.RequestID, "")
	if err != nil {
		return false, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if found {
		if !archiveRequestReceiptBindingEqual(existingReceipt, receipt) {
			return false, fmt.Errorf("%w: conversation archive request receipt conflict", domainmemory.ErrUserMemoryOwnerConflict)
		}
		existingEvent, eventFound, err := findArchiveMemoryEvent(ctx, tx, "user:"+receipt.UserID, receipt.MemoryID)
		if err != nil {
			return false, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
		if !eventFound {
			return false, fmt.Errorf("%w: conversation archive request receipt references a missing memory", domainmemory.ErrUserMemoryOwnerConflict)
		}
		if !archiveL1MemoryEventEqual(existingEvent, item) {
			return false, fmt.Errorf("%w: conversation archive request receipt memory conflicts with archived event", domainmemory.ErrUserMemoryOwnerConflict)
		}
		return true, nil
	}

	existingEvent, eventFound, err := findArchiveMemoryEvent(ctx, tx, "user:"+receipt.UserID, receipt.MemoryID)
	if err != nil {
		return false, fmt.Errorf("%w: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if eventFound && !archiveL1MemoryEventEqual(existingEvent, item) {
		return false, fmt.Errorf("%w: conversation archive memory conflict", domainmemory.ErrUserMemoryOwnerConflict)
	}
	if !eventFound {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event_archive (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, item.ID, item.Namespace, item.SessionID, item.ThreadID, string(item.Speaker), item.Message, string(metaJSON),
			item.MemoryState, item.Layer, item.Source, item.CreatedAt, item.UpdatedAt); err != nil {
			return false, fmt.Errorf("%w: failed to archive user memory event: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_archive_request_receipt (
	request_id, user_id, actor_id, payload_hash, memory_id, created_at
) VALUES (?, ?, ?, ?, ?, ?)
	`, receipt.RequestID, receipt.UserID, receipt.ActorID, receipt.PayloadHash, receipt.MemoryID, receipt.CreatedAt); err != nil {
		return false, fmt.Errorf("%w: failed to persist conversation archive request receipt: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("%w: failed to commit conversation archive request: %v", domainmemory.ErrUserMemoryOwnerUnavailable, err)
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
		return l1sqlite.L1MemoryEvent{}, false, fmt.Errorf("%w: archive sqlite store is closed", domainmemory.ErrUserMemoryOwnerUnavailable)
	}
	userID = strings.TrimSpace(userID)
	memoryID = strings.TrimSpace(memoryID)
	if userID == "" || memoryID == "" {
		return l1sqlite.L1MemoryEvent{}, false, fmt.Errorf("%w: archive user memory user_id and memory_id are required", domainmemory.ErrUserMemoryOwnerInvalid)
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
		return ArchiveRequestReceipt{}, false, fmt.Errorf("%w: archive sqlite store is closed", domainmemory.ErrUserMemoryOwnerUnavailable)
	}
	userID = strings.TrimSpace(userID)
	requestID = strings.TrimSpace(requestID)
	if userID == "" || requestID == "" {
		return ArchiveRequestReceipt{}, false, fmt.Errorf("%w: archive request user_id and request_id are required", domainmemory.ErrUserMemoryOwnerInvalid)
	}
	return findArchiveRequestReceipt(ctx, d.db, requestID, userID)
}

// FindConversationArchiveRequest is an explicit alias for the runtime route.
func (d *ArchiveSQLiteStore) FindConversationArchiveRequest(ctx context.Context, userID, requestID string) (ArchiveRequestReceipt, bool, error) {
	return d.FindArchiveRequestReceipt(ctx, userID, requestID)
}

// FindStagingItemByNamespaceEventID performs one exact, bounded lookup for an
// archived L1 staging projection.  Archive rows intentionally do not use a
// unique constraint for this pair; duplicates are an integrity failure rather
// than an arbitrary first-row result.
func (d *ArchiveSQLiteStore) FindStagingItemByNamespaceEventID(ctx context.Context, namespace, eventID string) (l1sqlite.L1StagingItem, bool, error) {
	if d == nil || d.db == nil {
		return l1sqlite.L1StagingItem{}, false, errors.New("archive sqlite store is closed")
	}
	if ctx == nil {
		return l1sqlite.L1StagingItem{}, false, errors.New("archive staging lookup context is required")
	}
	if strings.TrimSpace(namespace) != namespace {
		return l1sqlite.L1StagingItem{}, false, errors.New("archive staging namespace must not have surrounding whitespace")
	}
	if err := l1sqlite.ValidateL1Namespace(namespace); err != nil {
		return l1sqlite.L1StagingItem{}, false, err
	}
	if eventID == "" || strings.TrimSpace(eventID) != eventID {
		return l1sqlite.L1StagingItem{}, false, errors.New("archive staging event_id is required")
	}
	rows, err := d.db.QueryContext(ctx, `
SELECT id, kind, namespace, event_id, source_id, source_url, fetched_at, published_at,
       raw_text, raw_hash, summary_draft, keywords_json, license_note,
       validation_status, meta_json, created_at, updated_at
FROM l1_staging_item_archive
WHERE namespace = ? AND event_id = ?
LIMIT 2
`, namespace, eventID)
	if err != nil {
		return l1sqlite.L1StagingItem{}, false, err
	}
	defer rows.Close()
	items, err := l1sqlite.ScanL1StagingItems(rows)
	if err != nil {
		return l1sqlite.L1StagingItem{}, false, err
	}
	if len(items) == 0 {
		return l1sqlite.L1StagingItem{}, false, nil
	}
	if len(items) > 1 {
		return l1sqlite.L1StagingItem{}, false, errors.New("archive staging namespace and event_id are not unique")
	}
	if items[0].Namespace != namespace || items[0].EventID != eventID {
		return l1sqlite.L1StagingItem{}, false, errors.New("archive staging lookup returned a mismatched key")
	}
	return items[0], true, nil
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

// ArchiveL1RawLifecycleEvent archives one exact old conversation event and
// its lifecycle receipt in a single archive transaction. The source L1 store
// is intentionally not reachable from this adapter, so replay after a crash
// is safe and never deletes or mutates the source.
func (d *ArchiveSQLiteStore) ArchiveL1RawLifecycleEvent(ctx context.Context, item l1sqlite.L1MemoryEvent, receipt l1sqlite.L1RawLifecycleArchiveReceipt) error {
	if d == nil || d.db == nil {
		return fmt.Errorf("%w: %w: archive sqlite store is closed", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, domainmemory.ErrUserMemoryOwnerUnavailable)
	}
	receipt.OutboxID = strings.TrimSpace(receipt.OutboxID)
	receipt.EventID = strings.TrimSpace(receipt.EventID)
	receipt.EventSHA256 = strings.TrimSpace(receipt.EventSHA256)
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	} else {
		receipt.CreatedAt = receipt.CreatedAt.UTC()
	}
	if receipt.OutboxID == "" || receipt.EventID == "" || receipt.EventSHA256 == "" || item.ID == "" || item.ID != receipt.EventID {
		return fmt.Errorf("%w: raw lifecycle archive binding is incomplete", l1sqlite.ErrL1RawLifecycleArchiveConflict)
	}
	if !strings.HasPrefix(item.Namespace, "conv:") || item.MemoryState != l1sqlite.MemoryStateObserved || item.Layer != l1sqlite.MemoryLayerL1 {
		return fmt.Errorf("%w: raw lifecycle archive event is not an L1 conversation event", l1sqlite.ErrL1RawLifecycleArchiveConflict)
	}
	eventSHA256, err := l1sqlite.CanonicalL1MemoryEventSHA256(item)
	if err != nil {
		return fmt.Errorf("%w: compute raw lifecycle archive hash: %v", l1sqlite.ErrL1RawLifecycleArchiveConflict, err)
	}
	if eventSHA256 != receipt.EventSHA256 {
		return fmt.Errorf("%w: raw lifecycle archive hash does not match event %s", l1sqlite.ErrL1RawLifecycleArchiveConflict, item.ID)
	}
	if want := l1sqlite.L1RawLifecycleOutboxID(item.ID, eventSHA256); want != receipt.OutboxID {
		return fmt.Errorf("%w: raw lifecycle archive outbox binding does not match event %s", l1sqlite.ErrL1RawLifecycleArchiveConflict, item.ID)
	}
	metaJSON, err := json.Marshal(normalizeArchiveMeta(item.Meta))
	if err != nil {
		return fmt.Errorf("%w: marshal raw lifecycle archive metadata: %v", l1sqlite.ErrL1RawLifecycleArchiveConflict, err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("%w: begin raw lifecycle archive transaction: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}
	defer tx.Rollback()

	existingReceipt, found, err := findRawLifecycleArchiveReceipt(ctx, tx, receipt.OutboxID, "")
	if err != nil {
		return fmt.Errorf("%w: read raw lifecycle archive receipt: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}
	if found {
		if existingReceipt.EventID != receipt.EventID || existingReceipt.EventSHA256 != receipt.EventSHA256 {
			return fmt.Errorf("%w: raw lifecycle archive receipt conflict for outbox %s", l1sqlite.ErrL1RawLifecycleArchiveConflict, receipt.OutboxID)
		}
		existingEvent, eventFound, err := findArchiveMemoryEventByID(ctx, tx, receipt.EventID)
		if err != nil {
			return fmt.Errorf("%w: read raw lifecycle archived event: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
		}
		if !eventFound || !rawLifecycleArchiveEventEqual(existingEvent, item) {
			return fmt.Errorf("%w: raw lifecycle archive receipt references conflicting event %s", l1sqlite.ErrL1RawLifecycleArchiveConflict, receipt.EventID)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("%w: replay raw lifecycle archive transaction: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
		}
		return nil
	}
	var eventReceiptOutboxID string
	if err := tx.QueryRowContext(ctx, `
SELECT outbox_id
FROM conversation_lifecycle_raw_archive_receipt
WHERE event_id = ?`, receipt.EventID).Scan(&eventReceiptOutboxID); err == nil {
		return fmt.Errorf("%w: raw lifecycle archive event %s is already bound to outbox %s", l1sqlite.ErrL1RawLifecycleArchiveConflict, receipt.EventID, eventReceiptOutboxID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("%w: read raw lifecycle archive event binding: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}

	existingEvent, eventFound, err := findArchiveMemoryEventByID(ctx, tx, receipt.EventID)
	if err != nil {
		return fmt.Errorf("%w: read raw lifecycle archived event: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}
	if eventFound {
		if !rawLifecycleArchiveEventEqual(existingEvent, item) {
			return fmt.Errorf("%w: raw lifecycle archived event %s conflicts with source", l1sqlite.ErrL1RawLifecycleArchiveConflict, receipt.EventID)
		}
	} else if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_memory_event_archive (
	id, namespace, session_id, thread_id, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, item.ID, item.Namespace, item.SessionID, item.ThreadID, string(item.Speaker), item.Message, string(metaJSON), item.MemoryState, item.Layer, item.Source, item.CreatedAt, item.UpdatedAt); err != nil {
		return fmt.Errorf("%w: insert raw lifecycle archived event: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_lifecycle_raw_archive_receipt (
	outbox_id, event_id, event_sha256, created_at
) VALUES (?, ?, ?, ?)`, receipt.OutboxID, receipt.EventID, receipt.EventSHA256, receipt.CreatedAt); err != nil {
		return fmt.Errorf("%w: insert raw lifecycle archive receipt: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: commit raw lifecycle archive transaction: %v", l1sqlite.ErrL1RawLifecycleArchiveUnavailable, err)
	}
	return nil
}

type rawLifecycleArchiveReceipt struct {
	OutboxID    string
	EventID     string
	EventSHA256 string
	CreatedAt   time.Time
}

func findRawLifecycleArchiveReceipt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, outboxID, eventID string) (rawLifecycleArchiveReceipt, bool, error) {
	query := `SELECT outbox_id, event_id, event_sha256, created_at FROM conversation_lifecycle_raw_archive_receipt WHERE outbox_id = ?`
	args := []interface{}{outboxID}
	if strings.TrimSpace(eventID) != "" {
		query = `SELECT outbox_id, event_id, event_sha256, created_at FROM conversation_lifecycle_raw_archive_receipt WHERE outbox_id = ? AND event_id = ?`
		args = append(args, eventID)
	}
	var receipt rawLifecycleArchiveReceipt
	if err := queryer.QueryRowContext(ctx, query, args...).Scan(&receipt.OutboxID, &receipt.EventID, &receipt.EventSHA256, &receipt.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return rawLifecycleArchiveReceipt{}, false, nil
		}
		return rawLifecycleArchiveReceipt{}, false, err
	}
	return receipt, true, nil
}

func findArchiveMemoryEventByID(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, eventID string) (l1sqlite.L1MemoryEvent, bool, error) {
	event, err := scanArchiveMemoryEvent(queryer.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event_archive WHERE id = ?`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return l1sqlite.L1MemoryEvent{}, false, nil
	}
	if err != nil {
		return l1sqlite.L1MemoryEvent{}, false, err
	}
	return event, true, nil
}

func normalizeArchiveMeta(meta map[string]interface{}) map[string]interface{} {
	if meta == nil {
		return map[string]interface{}{}
	}
	return meta
}

func rawLifecycleArchiveEventEqual(left, right l1sqlite.L1MemoryEvent) bool {
	leftHash, leftErr := l1sqlite.CanonicalL1MemoryEventSHA256(left)
	rightHash, rightErr := l1sqlite.CanonicalL1MemoryEventSHA256(right)
	return leftErr == nil && rightErr == nil && leftHash == rightHash
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
