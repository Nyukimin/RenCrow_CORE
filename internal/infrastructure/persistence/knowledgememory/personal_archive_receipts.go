package knowledgememory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

// PersonalArchiveReceiptWriter is the durable source-bound archive write
// surface. Stores without an atomic receipt implementation must not claim to
// have completed an idempotent source import.
type PersonalArchiveReceiptWriter interface {
	SavePersonalArchiveEntryWithReceipt(ctx context.Context, item domainkm.PersonalArchiveEntry, sourceKey string, actorID string) (reused bool, err error)
}

// PersonalArchiveOwnerReader returns protected original text only for the
// exact authenticated owner and entry identity.
type PersonalArchiveOwnerReader interface {
	GetPersonalArchiveEntry(ctx context.Context, entryID, userID string) (item domainkm.PersonalArchiveEntry, found bool, err error)
}

var ErrPersonalArchiveReceiptIntegrity = errors.New("personal archive receipt integrity failure")

type personalArchiveReceipt struct {
	SourceKey           string
	EntryID             string
	UserID              string
	ActorID             string
	OriginalPayloadHash string
	CurrentPayloadHash  string
}

func personalArchiveReceiptSchemaStatements() []string {
	return []string{`CREATE TABLE IF NOT EXISTS knowledge_memory_personal_archive_receipts (
		source_key TEXT NOT NULL PRIMARY KEY,
		entry_id TEXT NOT NULL UNIQUE,
		user_id TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		original_payload_hash TEXT NOT NULL,
		current_payload_hash TEXT NOT NULL
	)`}
}

func (s *SQLiteStore) personalArchiveReceiptSchemaPresent(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'knowledge_memory_personal_archive_receipts'`).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *SQLiteStore) GetPersonalArchiveEntry(ctx context.Context, entryID, userID string) (domainkm.PersonalArchiveEntry, bool, error) {
	if s == nil || s.db == nil {
		return domainkm.PersonalArchiveEntry{}, false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	entryID = strings.TrimSpace(entryID)
	userID = strings.TrimSpace(userID)
	if entryID == "" || userID == "" {
		return domainkm.PersonalArchiveEntry{}, false, fmt.Errorf("personal archive entry_id and owner user_id are required")
	}
	if ready, err := s.personalArchiveReceiptSchemaPresent(ctx); err != nil {
		return domainkm.PersonalArchiveEntry{}, false, err
	} else if ready {
		receipt, found, err := findPersonalArchiveReceiptByColumn(ctx, s.db, "entry_id", entryID)
		if err != nil {
			return domainkm.PersonalArchiveEntry{}, false, err
		}
		if found {
			item, _, err := loadAndVerifyPersonalArchiveReceipt(ctx, s.db, receipt)
			if err != nil {
				return domainkm.PersonalArchiveEntry{}, false, err
			}
			if item.UserID != userID {
				return domainkm.PersonalArchiveEntry{}, false, nil
			}
			return item, true, nil
		}
	}
	var payload string
	err := s.db.QueryRowContext(ctx, `SELECT payload FROM personal_archive WHERE entry_id = ? AND user_id = ?`, entryID, userID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return domainkm.PersonalArchiveEntry{}, false, nil
	}
	if err != nil {
		return domainkm.PersonalArchiveEntry{}, false, err
	}
	var item domainkm.PersonalArchiveEntry
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainkm.PersonalArchiveEntry{}, false, fmt.Errorf("personal archive item payload is invalid")
	}
	if item.EntryID != entryID || item.UserID != userID {
		return domainkm.PersonalArchiveEntry{}, false, fmt.Errorf("personal archive row owner or identity does not match")
	}
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return domainkm.PersonalArchiveEntry{}, false, fmt.Errorf("personal archive item is invalid: %w", err)
	}
	return item, true, nil
}

func (s *SQLiteStore) SavePersonalArchiveEntryWithReceipt(ctx context.Context, item domainkm.PersonalArchiveEntry, sourceKey, actorID string) (bool, error) {
	item.EntryID = strings.TrimSpace(item.EntryID)
	item.UserID = strings.TrimSpace(item.UserID)
	sourceKey = strings.TrimSpace(sourceKey)
	actorID = strings.TrimSpace(actorID)
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return false, err
	}
	if sourceKey == "" || actorID == "" {
		return false, fmt.Errorf("personal archive source_key and actor_id are required")
	}
	if err := validateNewsReceiptOpaqueValue("source_key", sourceKey, maxNewsReceiptSourceKeyRunes); err != nil {
		return false, err
	}
	if err := validateNewsReceiptOpaqueValue("actor_id", actorID, maxNewsReceiptActorIDRunes); err != nil {
		return false, err
	}
	if item.SourceKind != "lumina_diary_email" || sourceKey != item.SourceRef {
		return false, fmt.Errorf("personal archive receipt must bind the canonical diary source")
	}
	payload, err := json.Marshal(item)
	if err != nil {
		return false, err
	}
	payloadHash := personalArchivePayloadHash(payload)
	if s == nil || s.db == nil {
		return false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	ready, err := s.personalArchiveReceiptSchemaPresent(ctx)
	if err != nil {
		return false, err
	}
	if !ready {
		return false, fmt.Errorf("knowledge memory personal archive receipt schema is unavailable")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	receipt, found, err := findPersonalArchiveReceiptByColumn(ctx, tx, "source_key", sourceKey)
	if err != nil {
		return false, err
	}
	if !found {
		receipt, found, err = findPersonalArchiveReceiptByColumn(ctx, tx, "entry_id", item.EntryID)
		if err != nil {
			return false, err
		}
	}
	if found {
		if receipt.SourceKey != sourceKey || receipt.EntryID != item.EntryID || receipt.UserID != item.UserID || receipt.ActorID != actorID || receipt.OriginalPayloadHash != payloadHash {
			return false, fmt.Errorf("%w: personal archive source or input binding does not match", ErrKnowledgeMemoryRequestConflict)
		}
		if _, _, err := loadAndVerifyPersonalArchiveReceipt(ctx, tx, receipt); err != nil {
			return false, err
		}
		return true, nil
	}
	var existing string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM personal_archive WHERE entry_id = ?`, item.EntryID).Scan(&existing)
	if err == nil {
		return false, fmt.Errorf("%w: personal archive entry_id is already bound to an ordinary row", ErrKnowledgeMemoryRequestConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	projection := personalArchiveSearchProjection(item)
	if _, err := tx.ExecContext(ctx, `INSERT INTO personal_archive (entry_id, user_id, created_at, payload) VALUES (?, ?, ?, ?)`, item.EntryID, item.UserID, item.CreatedAt.UTC().Format(timeFormatRFC3339Nano), string(payload)); err != nil {
		return false, err
	}
	if err := deleteSearchProjectionTx(ctx, tx, personalArchiveRecordType, item.EntryID); err != nil {
		return false, err
	}
	if projection != nil {
		if err := insertSearchProjectionTx(ctx, tx, *projection); err != nil {
			return false, err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_personal_archive_receipts
		(source_key, entry_id, user_id, actor_id, original_payload_hash, current_payload_hash)
		VALUES (?, ?, ?, ?, ?, ?)`, sourceKey, item.EntryID, item.UserID, actorID, payloadHash, payloadHash); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return false, fmt.Errorf("%w: personal archive source or entry binding was claimed concurrently", ErrKnowledgeMemoryRequestConflict)
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func findPersonalArchiveReceiptByColumn(ctx context.Context, queryer knowledgeMemoryQueryer, column, value string) (personalArchiveReceipt, bool, error) {
	if column != "source_key" && column != "entry_id" {
		return personalArchiveReceipt{}, false, fmt.Errorf("unsupported personal archive receipt lookup column %q", column)
	}
	var receipt personalArchiveReceipt
	err := queryer.QueryRowContext(ctx, `SELECT source_key, entry_id, user_id, actor_id, original_payload_hash, current_payload_hash
		FROM knowledge_memory_personal_archive_receipts WHERE `+column+` = ?`, value).Scan(
		&receipt.SourceKey, &receipt.EntryID, &receipt.UserID, &receipt.ActorID, &receipt.OriginalPayloadHash, &receipt.CurrentPayloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return personalArchiveReceipt{}, false, nil
	}
	if err != nil {
		return personalArchiveReceipt{}, false, err
	}
	return receipt, true, nil
}

func loadAndVerifyPersonalArchiveReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, receipt personalArchiveReceipt) (domainkm.PersonalArchiveEntry, string, error) {
	if receipt.SourceKey == "" || receipt.EntryID == "" || receipt.UserID == "" || receipt.ActorID == "" || !validNewsKnowledgePayloadHash(receipt.OriginalPayloadHash) || !validNewsKnowledgePayloadHash(receipt.CurrentPayloadHash) {
		return domainkm.PersonalArchiveEntry{}, "", fmt.Errorf("%w: receipt metadata is invalid", ErrPersonalArchiveReceiptIntegrity)
	}
	var payload string
	if err := queryer.QueryRowContext(ctx, `SELECT payload FROM personal_archive WHERE entry_id = ?`, receipt.EntryID).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainkm.PersonalArchiveEntry{}, "", fmt.Errorf("%w: receipt item is missing", ErrPersonalArchiveReceiptIntegrity)
		}
		return domainkm.PersonalArchiveEntry{}, "", err
	}
	var item domainkm.PersonalArchiveEntry
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainkm.PersonalArchiveEntry{}, "", fmt.Errorf("%w: receipt item payload is invalid", ErrPersonalArchiveReceiptIntegrity)
	}
	if item.EntryID != receipt.EntryID || item.UserID != receipt.UserID || item.SourceRef != receipt.SourceKey || item.SourceKind != "lumina_diary_email" {
		return domainkm.PersonalArchiveEntry{}, "", fmt.Errorf("%w: receipt owner/source binding does not match item", ErrPersonalArchiveReceiptIntegrity)
	}
	if err := domainkm.ValidatePersonalArchiveEntry(item); err != nil {
		return domainkm.PersonalArchiveEntry{}, "", fmt.Errorf("%w: receipt item is invalid", ErrPersonalArchiveReceiptIntegrity)
	}
	if personalArchivePayloadHash([]byte(payload)) != receipt.CurrentPayloadHash {
		return domainkm.PersonalArchiveEntry{}, "", fmt.Errorf("%w: current payload hash does not match item", ErrPersonalArchiveReceiptIntegrity)
	}
	return item, payload, nil
}

func personalArchivePayloadHash(payload []byte) string {
	hash := sha256.Sum256(payload)
	return hex.EncodeToString(hash[:])
}

func (s *SQLiteStore) verifyPersonalArchiveReceiptOverlays(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_key, entry_id, user_id, actor_id, original_payload_hash, current_payload_hash
		FROM knowledge_memory_personal_archive_receipts ORDER BY source_key`)
	if err != nil {
		return false, err
	}
	receipts := []personalArchiveReceipt{}
	for rows.Next() {
		var receipt personalArchiveReceipt
		if err := rows.Scan(&receipt.SourceKey, &receipt.EntryID, &receipt.UserID, &receipt.ActorID, &receipt.OriginalPayloadHash, &receipt.CurrentPayloadHash); err != nil {
			_ = rows.Close()
			return false, err
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	for _, receipt := range receipts {
		item, _, err := loadAndVerifyPersonalArchiveReceipt(ctx, s.db, receipt)
		if err != nil {
			if errors.Is(err, ErrPersonalArchiveReceiptIntegrity) {
				return false, nil
			}
			return false, err
		}
		if err := s.verifyPersonalArchiveReceiptProjection(ctx, item); err != nil {
			if errors.Is(err, ErrPersonalArchiveReceiptIntegrity) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}

func (s *SQLiteStore) verifyPersonalArchiveReceiptProjection(ctx context.Context, item domainkm.PersonalArchiveEntry) error {
	want := personalArchiveSearchProjection(item)
	var got safeSearchProjection
	err := s.db.QueryRowContext(ctx, `SELECT scope, user_id, title, summary, visibility, source_updated_at, content_sha256
		FROM knowledge_memory_search_documents WHERE record_type = ? AND record_id = ?`, personalArchiveRecordType, item.EntryID).Scan(
		&got.scope, &got.userID, &got.title, &got.summary, &got.visibility, &got.sourceUpdatedAt, &got.contentSHA256)
	if want == nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		return fmt.Errorf("%w: non-searchable archive has a search document", ErrPersonalArchiveReceiptIntegrity)
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: archive search document is missing", ErrPersonalArchiveReceiptIntegrity)
		}
		return err
	}
	if got.scope != want.scope || got.userID != want.userID || got.title != want.title || got.summary != want.summary || got.visibility != want.visibility || got.sourceUpdatedAt != want.sourceUpdatedAt || got.contentSHA256 != want.contentSHA256 {
		return fmt.Errorf("%w: archive search document does not match source", ErrPersonalArchiveReceiptIntegrity)
	}
	rows, err := s.db.QueryContext(ctx, `SELECT token FROM knowledge_memory_search_terms WHERE record_type = ? AND record_id = ? ORDER BY token`, personalArchiveRecordType, item.EntryID)
	if err != nil {
		return err
	}
	gotTokens := []string{}
	for rows.Next() {
		var token string
		if err := rows.Scan(&token); err != nil {
			_ = rows.Close()
			return err
		}
		gotTokens = append(gotTokens, token)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	wantTokens := append([]string(nil), want.tokens...)
	sort.Strings(wantTokens)
	if !slicesEqual(gotTokens, wantTokens) {
		return fmt.Errorf("%w: archive search terms do not match source", ErrPersonalArchiveReceiptIntegrity)
	}
	return nil
}
