package knowledgememory

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

const (
	maxNewsReceiptSourceKeyRunes = 512
	maxNewsReceiptActorIDRunes   = 256
)

// NewsKnowledgeReceiptWriter is the owner-controlled source-bound write
// surface for private Gmail/news knowledge overlays. It is intentionally
// separate from Store so JSONL and imported source writes cannot accidentally
// claim receipt semantics.
type NewsKnowledgeReceiptWriter interface {
	SaveNewsKnowledgeItemWithReceipt(ctx context.Context, item domainkm.NewsKnowledgeItem, sourceKey string, actorID string) (reused bool, err error)
}

var ErrNewsKnowledgeReceiptIntegrity = errors.New("news knowledge receipt integrity failure")

type newsKnowledgeReceipt struct {
	SourceKey           string
	ItemID              string
	UserID              string
	ActorID             string
	OriginalPayloadHash string
	CurrentPayloadHash  string
}

func newsKnowledgeReceiptSchemaStatements() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS knowledge_memory_news_receipts (
			source_key TEXT NOT NULL PRIMARY KEY,
			item_id TEXT NOT NULL UNIQUE,
			user_id TEXT NOT NULL,
			actor_id TEXT NOT NULL,
			original_payload_hash TEXT NOT NULL,
			current_payload_hash TEXT NOT NULL
		)`,
	}
}

func (s *SQLiteStore) newsKnowledgeReceiptSchemaPresent(ctx context.Context) (bool, error) {
	if s == nil || s.db == nil {
		return false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'knowledge_memory_news_receipts'`).Scan(&count); err != nil {
		return false, err
	}
	return count == 1, nil
}

func (s *SQLiteStore) SaveNewsKnowledgeItemWithReceipt(ctx context.Context, item domainkm.NewsKnowledgeItem, sourceKey string, actorID string) (bool, error) {
	sourceKey, actorID, err := validateNewsKnowledgeReceiptInput(item, sourceKey, actorID)
	if err != nil {
		return false, err
	}
	payload, err := marshalKnowledgeItem(item)
	if err != nil {
		return false, err
	}
	payloadHash := newsKnowledgePayloadHash(payload)
	if s == nil || s.db == nil {
		return false, fmt.Errorf("knowledge memory sqlite store is closed")
	}
	newsReceiptsReady, err := s.newsKnowledgeReceiptSchemaPresent(ctx)
	if err != nil {
		return false, err
	}
	if !newsReceiptsReady {
		return false, fmt.Errorf("knowledge memory news receipt schema is unavailable")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	receipt, found, err := findNewsKnowledgeReceiptByColumn(ctx, tx, "source_key", sourceKey)
	if err != nil {
		return false, err
	}
	if !found {
		receipt, found, err = findNewsKnowledgeReceiptByColumn(ctx, tx, "item_id", item.ItemID)
		if err != nil {
			return false, err
		}
	}
	if found {
		if !newsKnowledgeReceiptInputMatches(receipt, item, sourceKey, actorID, payloadHash) {
			return false, newsKnowledgeReceiptConflict("source or input binding does not match")
		}
		if _, _, err := loadAndVerifyNewsKnowledgeReceipt(ctx, tx, receipt); err != nil {
			return false, err
		}
		// The original payload is the replay binding. The current row may have
		// changed through the owner review route, so a valid replay never writes
		// the old candidate back over that later state.
		return true, nil
	}

	var existingPayload string
	err = tx.QueryRowContext(ctx, `SELECT payload FROM news_knowledge WHERE item_id = ?`, item.ItemID).Scan(&existingPayload)
	if err == nil {
		return false, newsKnowledgeReceiptConflict("item_id is already bound to an imported or ordinary news row")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}

	projection := newsSearchProjection(item)
	if err := saveKnowledgeItemWithProjectionTx(ctx, tx, newsKnowledgeRecordType, item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), payload, projection); err != nil {
		return false, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO knowledge_memory_news_receipts
		(source_key, item_id, user_id, actor_id, original_payload_hash, current_payload_hash)
		VALUES (?, ?, ?, ?, ?, ?)`, sourceKey, item.ItemID, item.UserID, actorID, payloadHash, payloadHash); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return false, newsKnowledgeReceiptConflict("source or item binding was claimed concurrently")
		}
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, err
	}
	return false, nil
}

func (s *SQLiteStore) saveNewsKnowledgeItem(ctx context.Context, item domainkm.NewsKnowledgeItem) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("knowledge memory sqlite store is closed")
	}
	newsReceiptsReady, err := s.newsKnowledgeReceiptSchemaPresent(ctx)
	if err != nil {
		return err
	}
	payload, err := marshalKnowledgeItem(item)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if newsReceiptsReady {
		receipt, found, err := findNewsKnowledgeReceiptByColumn(ctx, tx, "item_id", item.ItemID)
		if err != nil {
			return err
		}
		if found {
			if err := s.updateReceiptBoundNewsTx(ctx, tx, item, payload, receipt); err != nil {
				return err
			}
		} else if err := saveKnowledgeItemWithProjectionTx(ctx, tx, newsKnowledgeRecordType, item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), payload, newsSearchProjection(item)); err != nil {
			return err
		}
	} else if err := saveKnowledgeItemWithProjectionTx(ctx, tx, newsKnowledgeRecordType, item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), payload, newsSearchProjection(item)); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLiteStore) updateReceiptBoundNewsTx(ctx context.Context, tx *sql.Tx, item domainkm.NewsKnowledgeItem, payload string, receipt newsKnowledgeReceipt) error {
	stored, _, err := loadAndVerifyNewsKnowledgeReceipt(ctx, tx, receipt)
	if err != nil {
		return err
	}
	if err := validateReceiptBoundNewsUpdate(stored, item, receipt); err != nil {
		return err
	}
	if err := saveKnowledgeItemWithProjectionTx(ctx, tx, newsKnowledgeRecordType, item.ItemID, item.CreatedAt.Format(timeFormatRFC3339Nano), payload, newsSearchProjection(item)); err != nil {
		return err
	}
	currentHash := newsKnowledgePayloadHash(payload)
	result, err := tx.ExecContext(ctx, `UPDATE knowledge_memory_news_receipts
		SET current_payload_hash = ? WHERE source_key = ? AND item_id = ?`, currentHash, receipt.SourceKey, receipt.ItemID)
	if err != nil {
		return err
	}
	if affected, err := result.RowsAffected(); err != nil {
		return err
	} else if affected != 1 {
		return fmt.Errorf("%w: receipt row disappeared during update", ErrNewsKnowledgeReceiptIntegrity)
	}
	return nil
}

func validateNewsKnowledgeReceiptInput(item domainkm.NewsKnowledgeItem, sourceKey, actorID string) (string, string, error) {
	if err := validateReceiptBoundNewsItem(item, true); err != nil {
		return "", "", err
	}
	sourceKey = strings.TrimSpace(sourceKey)
	actorID = strings.TrimSpace(actorID)
	if err := validateNewsReceiptOpaqueValue("source_key", sourceKey, maxNewsReceiptSourceKeyRunes); err != nil {
		return "", "", err
	}
	if err := validateNewsReceiptOpaqueValue("actor_id", actorID, maxNewsReceiptActorIDRunes); err != nil {
		return "", "", err
	}
	return sourceKey, actorID, nil
}

func validateReceiptBoundNewsItem(item domainkm.NewsKnowledgeItem, initial bool) error {
	if err := domainkm.ValidateNewsKnowledgeItem(item); err != nil {
		return err
	}
	if strings.TrimSpace(item.UserID) == "" {
		return fmt.Errorf("user_id is required for receipt-bound news")
	}
	if strings.TrimSpace(item.Visibility) != "private" {
		return fmt.Errorf("receipt-bound news visibility must be private")
	}
	if initial && item.Status != "candidate" && item.Status != "reviewed" {
		return fmt.Errorf("receipt-bound news status must be candidate or reviewed")
	}
	return nil
}

func validateNewsReceiptOpaqueValue(field, value string, maxRunes int) error {
	if value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return fmt.Errorf("%s contains forbidden control characters", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
	}
	return nil
}

func newsKnowledgeReceiptInputMatches(receipt newsKnowledgeReceipt, item domainkm.NewsKnowledgeItem, sourceKey, actorID, payloadHash string) bool {
	return receipt.SourceKey == sourceKey && receipt.ItemID == item.ItemID && receipt.UserID == item.UserID && receipt.ActorID == actorID && receipt.OriginalPayloadHash == payloadHash
}

func newsKnowledgeReceiptConflict(reason string) error {
	return fmt.Errorf("%w: %s", ErrKnowledgeMemoryRequestConflict, reason)
}

func findNewsKnowledgeReceiptByColumn(ctx context.Context, queryer knowledgeMemoryQueryer, column, value string) (newsKnowledgeReceipt, bool, error) {
	if column != "source_key" && column != "item_id" {
		return newsKnowledgeReceipt{}, false, fmt.Errorf("unsupported news receipt lookup column %q", column)
	}
	var receipt newsKnowledgeReceipt
	err := queryer.QueryRowContext(ctx, `SELECT source_key, item_id, user_id, actor_id, original_payload_hash, current_payload_hash
		FROM knowledge_memory_news_receipts WHERE `+column+` = ?`, value).Scan(
		&receipt.SourceKey, &receipt.ItemID, &receipt.UserID, &receipt.ActorID, &receipt.OriginalPayloadHash, &receipt.CurrentPayloadHash)
	if errors.Is(err, sql.ErrNoRows) {
		return newsKnowledgeReceipt{}, false, nil
	}
	if err != nil {
		return newsKnowledgeReceipt{}, false, err
	}
	return receipt, true, nil
}

func loadAndVerifyNewsKnowledgeReceipt(ctx context.Context, queryer knowledgeMemoryQueryer, receipt newsKnowledgeReceipt) (domainkm.NewsKnowledgeItem, string, error) {
	if err := validateNewsKnowledgeReceiptMetadata(receipt); err != nil {
		return domainkm.NewsKnowledgeItem{}, "", err
	}
	var payload string
	if err := queryer.QueryRowContext(ctx, `SELECT payload FROM news_knowledge WHERE item_id = ?`, receipt.ItemID).Scan(&payload); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domainkm.NewsKnowledgeItem{}, "", fmt.Errorf("%w: receipt item is missing", ErrNewsKnowledgeReceiptIntegrity)
		}
		return domainkm.NewsKnowledgeItem{}, "", err
	}
	var item domainkm.NewsKnowledgeItem
	if err := json.Unmarshal([]byte(payload), &item); err != nil {
		return domainkm.NewsKnowledgeItem{}, "", fmt.Errorf("%w: receipt item payload is invalid", ErrNewsKnowledgeReceiptIntegrity)
	}
	if item.ItemID != receipt.ItemID || item.UserID != receipt.UserID {
		return domainkm.NewsKnowledgeItem{}, "", fmt.Errorf("%w: receipt owner binding does not match item", ErrNewsKnowledgeReceiptIntegrity)
	}
	if err := validateReceiptBoundNewsItem(item, false); err != nil {
		return domainkm.NewsKnowledgeItem{}, "", fmt.Errorf("%w: receipt item is invalid", ErrNewsKnowledgeReceiptIntegrity)
	}
	if newsKnowledgePayloadHash(payload) != receipt.CurrentPayloadHash {
		return domainkm.NewsKnowledgeItem{}, "", fmt.Errorf("%w: current payload hash does not match item", ErrNewsKnowledgeReceiptIntegrity)
	}
	return item, payload, nil
}

func validateNewsKnowledgeReceiptMetadata(receipt newsKnowledgeReceipt) error {
	if err := validateNewsReceiptOpaqueValue("source_key", receipt.SourceKey, maxNewsReceiptSourceKeyRunes); err != nil {
		return fmt.Errorf("%w: %v", ErrNewsKnowledgeReceiptIntegrity, err)
	}
	if err := validateNewsReceiptOpaqueValue("actor_id", receipt.ActorID, maxNewsReceiptActorIDRunes); err != nil {
		return fmt.Errorf("%w: %v", ErrNewsKnowledgeReceiptIntegrity, err)
	}
	if strings.TrimSpace(receipt.ItemID) == "" || strings.TrimSpace(receipt.UserID) == "" {
		return fmt.Errorf("%w: receipt item and user binding are required", ErrNewsKnowledgeReceiptIntegrity)
	}
	if !validNewsKnowledgePayloadHash(receipt.OriginalPayloadHash) || !validNewsKnowledgePayloadHash(receipt.CurrentPayloadHash) {
		return fmt.Errorf("%w: receipt payload hash is invalid", ErrNewsKnowledgeReceiptIntegrity)
	}
	return nil
}

func validateReceiptBoundNewsUpdate(stored, next domainkm.NewsKnowledgeItem, receipt newsKnowledgeReceipt) error {
	if err := validateReceiptBoundNewsItem(next, false); err != nil {
		return err
	}
	if next.ItemID != receipt.ItemID || next.UserID != receipt.UserID {
		return newsKnowledgeReceiptConflict("owner or item binding cannot change")
	}
	if next.Source != stored.Source || next.URL != stored.URL || next.Visibility != stored.Visibility {
		return newsKnowledgeReceiptConflict("source binding cannot change")
	}
	return nil
}

func newsKnowledgePayloadHash(payload string) string {
	hash := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(hash[:])
}

func validNewsKnowledgePayloadHash(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func (s *SQLiteStore) verifyNewsKnowledgeReceiptOverlays(ctx context.Context) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT source_key, item_id, user_id, actor_id, original_payload_hash, current_payload_hash
		FROM knowledge_memory_news_receipts ORDER BY source_key`)
	if err != nil {
		return false, err
	}
	receipts := []newsKnowledgeReceipt{}
	for rows.Next() {
		var receipt newsKnowledgeReceipt
		if err := rows.Scan(&receipt.SourceKey, &receipt.ItemID, &receipt.UserID, &receipt.ActorID, &receipt.OriginalPayloadHash, &receipt.CurrentPayloadHash); err != nil {
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
		if _, _, err := loadAndVerifyNewsKnowledgeReceipt(ctx, s.db, receipt); err != nil {
			if errors.Is(err, ErrNewsKnowledgeReceiptIntegrity) {
				return false, nil
			}
			return false, err
		}
	}
	return true, nil
}
