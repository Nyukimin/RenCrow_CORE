package archivesqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

// ReconcileThreadSummaryWrite proves whether one immutable thread summary and
// its receipt were atomically persisted.
func (d *ArchiveSQLiteStore) ReconcileThreadSummaryWrite(ctx context.Context, summary *conversation.ThreadSummary, receipt *conversation.ThreadSummaryReceipt) (bool, error) {
	if d == nil || d.db == nil {
		return false, errors.New("archive SQLite store is closed")
	}
	if ctx == nil {
		return false, errors.New("thread summary reconciliation context is required")
	}
	if err := validateNewThreadSummary(summary, receipt); err != nil {
		return false, fmt.Errorf("thread summary reconciliation input is invalid: %w", err)
	}
	keywordsJSON, embeddingJSON, err := marshalThreadSummaryPayload(summary)
	if err != nil {
		return false, fmt.Errorf("marshal expected thread summary: %w", err)
	}
	rolesJSON, err := json.Marshal(receipt.Roles)
	if err != nil {
		return false, fmt.Errorf("marshal expected thread summary roles: %w", err)
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, fmt.Errorf("begin thread summary reconciliation query: %w", err)
	}
	defer tx.Rollback()
	storedSummary, summaryExists, err := loadStoredThreadSummary(ctx, tx, summary.ThreadID)
	if err != nil {
		return false, err
	}
	storedReceipt, receiptExists, err := loadStoredThreadSummaryReceipt(ctx, tx, summary.ThreadID)
	if err != nil {
		return false, err
	}
	if summaryExists != receiptExists {
		return false, errors.New("thread summary and receipt are not atomically present")
	}
	committed := summaryExists && storedThreadSummaryEqual(storedSummary, summary, keywordsJSON, embeddingJSON) &&
		storedThreadSummaryReceiptEqual(storedReceipt, receipt, string(rolesJSON))
	if summaryExists && !committed {
		return false, errors.New("stored thread summary or receipt conflicts with the requested write")
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("finish thread summary reconciliation query: %w", err)
	}
	return committed, nil
}

type archiveStorageHostUserMemoryReceipt struct {
	OpID             string
	PayloadSHA256    string
	RequestID        string
	UserID           string
	MemoryID         string
	IdempotentReplay bool
	CreatedAt        time.Time
	IntegritySHA256  string
}

// ReconcileUserMemoryArchiveWrite proves the original RPC result from a
// storage-host receipt committed atomically with the product archive receipt
// and the exact archived event.
func (d *ArchiveSQLiteStore) ReconcileUserMemoryArchiveWrite(ctx context.Context, event l1sqlite.L1MemoryEvent, receipt ArchiveRequestReceipt, opID, payloadSHA256 string) (bool, bool, error) {
	return d.reconcileUserMemoryArchiveWrite(ctx, event, receipt, opID, payloadSHA256)
}

func (d *ArchiveSQLiteStore) reconcileUserMemoryArchiveWrite(ctx context.Context, event l1sqlite.L1MemoryEvent, receipt ArchiveRequestReceipt, opID, payloadSHA256 string) (bool, bool, error) {
	if d == nil || d.db == nil {
		return false, false, errors.New("archive SQLite store is closed")
	}
	if ctx == nil {
		return false, false, errors.New("user-memory reconciliation context is required")
	}
	if err := validateArchiveUserMemoryBinding(event, receipt); err != nil {
		return false, false, fmt.Errorf("user-memory reconciliation input is invalid: %w", err)
	}
	if !validArchiveStorageHostOperationIdentity(opID, payloadSHA256) {
		return false, false, errors.New("user-memory reconciliation operation identity is invalid")
	}
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, false, fmt.Errorf("begin user-memory reconciliation query: %w", err)
	}
	defer tx.Rollback()
	operation := archiveStorageHostUserMemoryReceipt{
		OpID: opID, PayloadSHA256: payloadSHA256, RequestID: receipt.RequestID,
		UserID: receipt.UserID, MemoryID: receipt.MemoryID,
	}
	storedOperation, operationFound, err := findArchiveStorageHostUserMemoryReceipt(ctx, tx, opID)
	if err != nil {
		return false, false, err
	}
	if !operationFound {
		if err := tx.Commit(); err != nil {
			return false, false, fmt.Errorf("finish user-memory reconciliation query: %w", err)
		}
		// The operation result receipt is inserted in the same transaction as
		// both owner rows, so its proven absence authorizes the same op_id retry.
		return false, false, nil
	}
	if !archiveStorageHostUserMemoryReceiptBindingEqual(storedOperation, operation) {
		return false, false, errors.New("stored storage-host user-memory receipt conflicts with the requested operation")
	}
	storedReceipt, receiptFound, err := findArchiveRequestReceipt(ctx, tx, receipt.RequestID, "")
	if err != nil {
		return false, false, err
	}
	if !receiptFound || !archiveRequestReceiptBindingEqual(storedReceipt, receipt) {
		return false, false, errors.New("storage-host operation receipt has no matching product receipt")
	}
	storedEvent, eventFound, err := findArchiveMemoryEventByID(ctx, tx, receipt.MemoryID)
	if err != nil {
		return false, false, err
	}
	if !eventFound || !archiveL1MemoryEventEqual(storedEvent, event) || storedEvent.Namespace != "user:"+receipt.UserID {
		return false, false, errors.New("storage-host operation receipt has no matching user memory")
	}
	if err := tx.Commit(); err != nil {
		return false, false, fmt.Errorf("finish user-memory reconciliation query: %w", err)
	}
	return true, storedOperation.IdempotentReplay, nil
}

func validArchiveStorageHostOperationIdentity(opID, payloadSHA256 string) bool {
	if len(opID) < 1 || len(opID) > 128 || len(payloadSHA256) != 64 {
		return false
	}
	for _, character := range opID {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || character == '.' || character == '_' || character == ':' || character == '-') {
			return false
		}
	}
	for _, character := range payloadSHA256 {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func archiveStorageHostUserMemoryReceiptBindingEqual(stored, expected archiveStorageHostUserMemoryReceipt) bool {
	return stored.OpID == expected.OpID && stored.PayloadSHA256 == expected.PayloadSHA256 &&
		stored.RequestID == expected.RequestID && stored.UserID == expected.UserID && stored.MemoryID == expected.MemoryID
}

func archiveStorageHostUserMemoryReceiptIntegrity(receipt archiveStorageHostUserMemoryReceipt) (string, error) {
	canonical := struct {
		Version          string `json:"version"`
		OpID             string `json:"op_id"`
		PayloadSHA256    string `json:"payload_sha256"`
		RequestID        string `json:"request_id"`
		UserID           string `json:"user_id"`
		MemoryID         string `json:"memory_id"`
		IdempotentReplay bool   `json:"idempotent_replay"`
		CreatedAt        string `json:"created_at"`
	}{
		Version: "conversation-archive-storagehost-user-memory-receipt.v1",
		OpID:    receipt.OpID, PayloadSHA256: receipt.PayloadSHA256, RequestID: receipt.RequestID,
		UserID: receipt.UserID, MemoryID: receipt.MemoryID, IdempotentReplay: receipt.IdempotentReplay,
		CreatedAt: receipt.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal storage-host user-memory receipt integrity: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func findArchiveStorageHostUserMemoryReceipt(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, opID string) (archiveStorageHostUserMemoryReceipt, bool, error) {
	var receipt archiveStorageHostUserMemoryReceipt
	var replay int64
	var integritySHA256 sql.NullString
	err := queryer.QueryRowContext(ctx, `
SELECT op_id, payload_sha256, request_id, user_id, memory_id, idempotent_replay, created_at, integrity_sha256
FROM conversation_archive_storagehost_user_memory_receipt WHERE op_id = ?
`, opID).Scan(&receipt.OpID, &receipt.PayloadSHA256, &receipt.RequestID, &receipt.UserID, &receipt.MemoryID, &replay, &receipt.CreatedAt, &integritySHA256)
	if errors.Is(err, sql.ErrNoRows) {
		return archiveStorageHostUserMemoryReceipt{}, false, nil
	}
	if err != nil {
		return archiveStorageHostUserMemoryReceipt{}, false, fmt.Errorf("failed to read storage-host user-memory receipt: %w", err)
	}
	if replay != 0 && replay != 1 {
		return archiveStorageHostUserMemoryReceipt{}, false, errors.New("stored storage-host user-memory replay result is invalid")
	}
	receipt.IdempotentReplay = replay == 1
	if !integritySHA256.Valid {
		return archiveStorageHostUserMemoryReceipt{}, false, errors.New("stored storage-host user-memory receipt integrity is missing")
	}
	receipt.IntegritySHA256 = integritySHA256.String
	if !validArchiveStorageHostOperationIdentity(receipt.OpID, receipt.PayloadSHA256) || receipt.RequestID == "" || receipt.UserID == "" || receipt.MemoryID == "" || receipt.CreatedAt.IsZero() || !validArchiveSHA256(receipt.IntegritySHA256) {
		return archiveStorageHostUserMemoryReceipt{}, false, errors.New("stored storage-host user-memory receipt is incomplete")
	}
	expectedIntegrity, err := archiveStorageHostUserMemoryReceiptIntegrity(receipt)
	if err != nil {
		return archiveStorageHostUserMemoryReceipt{}, false, err
	}
	if receipt.IntegritySHA256 != expectedIntegrity {
		return archiveStorageHostUserMemoryReceipt{}, false, errors.New("stored storage-host user-memory receipt integrity check failed")
	}
	return receipt, true, nil
}

func validArchiveSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func insertArchiveStorageHostUserMemoryReceipt(ctx context.Context, tx *sql.Tx, receipt archiveStorageHostUserMemoryReceipt) error {
	if !validArchiveStorageHostOperationIdentity(receipt.OpID, receipt.PayloadSHA256) || receipt.RequestID == "" || receipt.UserID == "" || receipt.MemoryID == "" {
		return errors.New("storage-host user-memory receipt is incomplete")
	}
	if receipt.CreatedAt.IsZero() {
		receipt.CreatedAt = time.Now().UTC()
	} else {
		receipt.CreatedAt = receipt.CreatedAt.UTC()
	}
	integritySHA256, err := archiveStorageHostUserMemoryReceiptIntegrity(receipt)
	if err != nil {
		return err
	}
	receipt.IntegritySHA256 = integritySHA256
	var replay int64
	if receipt.IdempotentReplay {
		replay = 1
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO conversation_archive_storagehost_user_memory_receipt (
	op_id, payload_sha256, request_id, user_id, memory_id, idempotent_replay, created_at, integrity_sha256
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
`, receipt.OpID, receipt.PayloadSHA256, receipt.RequestID, receipt.UserID, receipt.MemoryID, replay, receipt.CreatedAt, receipt.IntegritySHA256)
	return err
}
