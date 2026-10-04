package l1sqlite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

const (
	l1PromotionArchivePending   = "pending"
	l1PromotionArchiveCommitted = "committed"
)

// L1PromotionArchiveStore is the closed archive-owner extension required for
// recoverable cross-database promotion. The archive effect and its exact
// result receipt are atomic; a missing receipt is the only retry authority.
type L1PromotionArchiveStore interface {
	ArchiveL1PromotionForOperation(context.Context, ConversationL1OperationIdentity, L1MemoryEvent) error
	LookupL1PromotionOperationReceipt(context.Context, ConversationL1OperationIdentity, L1MemoryEvent) (json.RawMessage, bool, error)
}

type l1PromotionArchiveOutbox struct {
	Identity     ConversationL1OperationIdentity
	EventID      string
	EventSHA256  string
	ResultJSON   string
	ResultSHA256 string
	Status       string
}

func initL1PromotionArchiveOutboxSchema(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversation_l1_promotion_archive_outbox (
		op_id TEXT PRIMARY KEY NOT NULL,
		operation TEXT NOT NULL CHECK(operation = 'promote_memory_to_namespace'),
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64),
		writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
		event_id TEXT NOT NULL,
		event_sha256 TEXT NOT NULL CHECK(length(event_sha256) = 64),
		result_json TEXT NOT NULL CHECK(length(result_json) <= 8388608),
		result_sha256 TEXT NOT NULL CHECK(length(result_sha256) = 64),
		status TEXT NOT NULL CHECK(status IN ('pending', 'committed')),
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("initialize L1 promotion archive outbox: %w", err)
	}
	return nil
}

func insertL1PromotionArchiveOutbox(ctx context.Context, tx *sql.Tx, identity ConversationL1OperationIdentity, event L1MemoryEvent) error {
	if err := identity.validate(); err != nil || identity.Operation != "promote_memory_to_namespace" {
		return errors.New("L1 promotion archive identity is invalid")
	}
	eventSHA256, err := CanonicalL1MemoryEventSHA256(event)
	if err != nil {
		return fmt.Errorf("hash L1 promotion archive event: %w", err)
	}
	result, err := json.Marshal(&event)
	if err != nil {
		return fmt.Errorf("encode L1 promotion archive result: %w", err)
	}
	resultHash := sha256.Sum256(result)
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, `INSERT INTO conversation_l1_promotion_archive_outbox
		(op_id, operation, payload_sha256, writer_generation, event_id, event_sha256, result_json, result_sha256, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, identity.Operation, identity.PayloadSHA256,
		identity.WriterGeneration, event.ID, eventSHA256, string(result), hex.EncodeToString(resultHash[:]),
		l1PromotionArchivePending, now, now)
	if err != nil {
		return fmt.Errorf("insert L1 promotion archive outbox: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) completeL1PromotionArchive(ctx context.Context, identity ConversationL1OperationIdentity, expected L1MemoryEvent) error {
	archive, ok := s.archiveStore.(L1PromotionArchiveStore)
	if !ok || archive == nil {
		return ErrConversationL1OperationNeedsSingleSQLiteStore
	}
	outbox, err := s.loadL1PromotionArchiveOutbox(ctx, identity)
	if err != nil {
		return err
	}
	if !l1PromotionArchiveOutboxMatches(outbox, expected) {
		return errors.New("L1 promotion archive outbox does not match canonical result")
	}
	raw, found, err := archive.LookupL1PromotionOperationReceipt(ctx, identity, expected)
	if err != nil {
		return fmt.Errorf("reconcile L1 promotion archive: %w", err)
	}
	if !found {
		if err := archive.ArchiveL1PromotionForOperation(ctx, identity, expected); err != nil {
			return fmt.Errorf("deliver L1 promotion archive: %w", err)
		}
		raw, found, err = archive.LookupL1PromotionOperationReceipt(ctx, identity, expected)
		if err != nil {
			return fmt.Errorf("prove L1 promotion archive commit: %w", err)
		}
	}
	if !found || !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace([]byte(outbox.ResultJSON))) {
		return errors.New("L1 promotion archive commit is not proven by the exact result receipt")
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE conversation_l1_promotion_archive_outbox
		SET status = ?, updated_at = ?
		WHERE op_id = ? AND operation = ? AND payload_sha256 = ? AND writer_generation = ?
			AND event_id = ? AND event_sha256 = ? AND result_sha256 = ?`,
		l1PromotionArchiveCommitted, time.Now().UTC(), identity.OpID, identity.Operation, identity.PayloadSHA256,
		identity.WriterGeneration, outbox.EventID, outbox.EventSHA256, outbox.ResultSHA256); err != nil {
		return fmt.Errorf("mark L1 promotion archive committed: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) loadL1PromotionArchiveOutbox(ctx context.Context, identity ConversationL1OperationIdentity) (l1PromotionArchiveOutbox, error) {
	var outbox l1PromotionArchiveOutbox
	var operation, payloadSHA256 string
	var writerGeneration int64
	err := s.db.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, event_id, event_sha256, result_json, result_sha256, status
		FROM conversation_l1_promotion_archive_outbox WHERE op_id = ?`, identity.OpID).
		Scan(&operation, &payloadSHA256, &writerGeneration, &outbox.EventID, &outbox.EventSHA256, &outbox.ResultJSON, &outbox.ResultSHA256, &outbox.Status)
	if err != nil {
		return outbox, fmt.Errorf("read L1 promotion archive outbox: %w", err)
	}
	outbox.Identity = ConversationL1OperationIdentity{OpID: identity.OpID, Operation: operation, PayloadSHA256: payloadSHA256, WriterGeneration: writerGeneration}
	if outbox.Identity != identity || (outbox.Status != l1PromotionArchivePending && outbox.Status != l1PromotionArchiveCommitted) ||
		!validConversationL1SHA256(outbox.EventSHA256) || !validConversationL1SHA256(outbox.ResultSHA256) || !json.Valid([]byte(outbox.ResultJSON)) {
		return outbox, errors.New("L1 promotion archive outbox is malformed or conflicts with the operation")
	}
	resultHash := sha256.Sum256([]byte(outbox.ResultJSON))
	if outbox.ResultSHA256 != hex.EncodeToString(resultHash[:]) {
		return outbox, errors.New("L1 promotion archive outbox result integrity mismatch")
	}
	return outbox, nil
}

func l1PromotionArchiveOutboxMatches(outbox l1PromotionArchiveOutbox, event L1MemoryEvent) bool {
	eventSHA256, err := CanonicalL1MemoryEventSHA256(event)
	if err != nil || eventSHA256 != outbox.EventSHA256 || event.ID != outbox.EventID {
		return false
	}
	encoded, err := json.Marshal(&event)
	return err == nil && bytes.Equal(encoded, []byte(outbox.ResultJSON))
}

func validConversationL1SHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == string(bytes.ToLower([]byte(value)))
}
