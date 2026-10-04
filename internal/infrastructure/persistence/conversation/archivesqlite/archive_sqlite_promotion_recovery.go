package archivesqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

var l1PromotionArchiveOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type l1PromotionArchiveReceipt struct {
	Identity     l1sqlite.ConversationL1OperationIdentity
	EventID      string
	EventSHA256  string
	ResultJSON   string
	ResultSHA256 string
	CreatedAt    time.Time
}

func (d *ArchiveSQLiteStore) initL1PromotionArchiveReceiptSchema(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS conversation_l1_promotion_archive_receipt (
		op_id TEXT PRIMARY KEY NOT NULL,
		operation TEXT NOT NULL CHECK(operation = 'promote_memory_to_namespace'),
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64),
		writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
		event_id TEXT NOT NULL,
		event_sha256 TEXT NOT NULL CHECK(length(event_sha256) = 64),
		result_json TEXT NOT NULL CHECK(length(result_json) <= 8388608),
		result_sha256 TEXT NOT NULL CHECK(length(result_sha256) = 64),
		created_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("initialize L1 promotion archive receipt: %w", err)
	}
	return nil
}

func (d *ArchiveSQLiteStore) ArchiveL1PromotionForOperation(ctx context.Context, identity l1sqlite.ConversationL1OperationIdentity, event l1sqlite.L1MemoryEvent) error {
	expected, err := newL1PromotionArchiveReceipt(identity, event)
	if err != nil {
		return err
	}
	if err := validateArchiveThreadTuple(event.ThreadID, event.ThreadSeq, event.ThreadKind, true); err != nil {
		return fmt.Errorf("L1 promotion archive thread identity is invalid: %w", err)
	}
	metaJSON, err := json.Marshal(normalizeArchiveMeta(event.Meta))
	if err != nil {
		return fmt.Errorf("encode L1 promotion archive metadata: %w", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin L1 promotion archive transaction: %w", err)
	}
	defer tx.Rollback()
	stored, found, err := findL1PromotionArchiveReceipt(ctx, tx, identity.OpID)
	if err != nil {
		return err
	}
	if found {
		if !l1PromotionArchiveReceiptEqual(stored, expected) {
			return errors.New("L1 promotion archive receipt conflicts with operation")
		}
		if err := verifyL1PromotionArchiveEffect(ctx, tx, event); err != nil {
			return err
		}
		return tx.Commit()
	}
	existing, eventFound, err := findArchiveMemoryEventByID(ctx, tx, event.ID)
	if err != nil {
		return fmt.Errorf("read L1 promotion archive effect: %w", err)
	}
	if eventFound && !archiveL1MemoryEventEqual(existing, event) {
		return errors.New("L1 promotion archive event conflicts with existing row")
	}
	if !eventFound {
		if _, err := tx.ExecContext(ctx, `INSERT INTO l1_memory_event_archive (
			id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
			memory_state, layer, source, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, event.ID, event.Namespace, event.SessionID,
			string(event.ThreadID), int64(event.ThreadSeq), string(event.ThreadKind), string(event.Speaker), event.Message,
			string(metaJSON), event.MemoryState, event.Layer, event.Source, event.CreatedAt, event.UpdatedAt); err != nil {
			return fmt.Errorf("insert L1 promotion archive effect: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO conversation_l1_promotion_archive_receipt
		(op_id, operation, payload_sha256, writer_generation, event_id, event_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, expected.Identity.OpID, expected.Identity.Operation, expected.Identity.PayloadSHA256,
		expected.Identity.WriterGeneration, expected.EventID, expected.EventSHA256, expected.ResultJSON, expected.ResultSHA256, expected.CreatedAt); err != nil {
		return fmt.Errorf("insert L1 promotion archive receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit L1 promotion archive transaction: %w", err)
	}
	return nil
}

func (d *ArchiveSQLiteStore) LookupL1PromotionOperationReceipt(ctx context.Context, identity l1sqlite.ConversationL1OperationIdentity, event l1sqlite.L1MemoryEvent) (json.RawMessage, bool, error) {
	expected, err := newL1PromotionArchiveReceipt(identity, event)
	if err != nil {
		return nil, false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	tx, err := d.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, fmt.Errorf("begin L1 promotion archive reconciliation: %w", err)
	}
	defer tx.Rollback()
	stored, found, err := findL1PromotionArchiveReceipt(ctx, tx, identity.OpID)
	if err != nil || !found {
		return nil, found, err
	}
	if !l1PromotionArchiveReceiptEqual(stored, expected) {
		return nil, false, errors.New("L1 promotion archive receipt conflicts with operation")
	}
	if err := verifyL1PromotionArchiveEffect(ctx, tx, event); err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("finish L1 promotion archive reconciliation: %w", err)
	}
	return append(json.RawMessage(nil), stored.ResultJSON...), true, nil
}

func newL1PromotionArchiveReceipt(identity l1sqlite.ConversationL1OperationIdentity, event l1sqlite.L1MemoryEvent) (l1PromotionArchiveReceipt, error) {
	if !validL1PromotionArchiveIdentity(identity) || event.ID == "" || event.MemoryState != l1sqlite.MemoryStateConfirmed || event.Source != "promoter" {
		return l1PromotionArchiveReceipt{}, errors.New("L1 promotion archive binding is invalid")
	}
	if err := l1sqlite.ValidateL1Namespace(event.Namespace); err != nil {
		return l1PromotionArchiveReceipt{}, errors.New("L1 promotion archive namespace is invalid")
	}
	eventSHA256, err := l1sqlite.CanonicalL1MemoryEventSHA256(event)
	if err != nil {
		return l1PromotionArchiveReceipt{}, fmt.Errorf("hash L1 promotion archive event: %w", err)
	}
	result, err := json.Marshal(&event)
	if err != nil {
		return l1PromotionArchiveReceipt{}, fmt.Errorf("encode L1 promotion archive result: %w", err)
	}
	resultHash := sha256.Sum256(result)
	return l1PromotionArchiveReceipt{Identity: identity, EventID: event.ID, EventSHA256: eventSHA256,
		ResultJSON: string(result), ResultSHA256: hex.EncodeToString(resultHash[:]), CreatedAt: time.Now().UTC()}, nil
}

func findL1PromotionArchiveReceipt(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, opID string) (l1PromotionArchiveReceipt, bool, error) {
	var receipt l1PromotionArchiveReceipt
	err := query.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, event_id, event_sha256, result_json, result_sha256, created_at
		FROM conversation_l1_promotion_archive_receipt WHERE op_id = ?`, opID).
		Scan(&receipt.Identity.Operation, &receipt.Identity.PayloadSHA256, &receipt.Identity.WriterGeneration, &receipt.EventID,
			&receipt.EventSHA256, &receipt.ResultJSON, &receipt.ResultSHA256, &receipt.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, fmt.Errorf("read L1 promotion archive receipt: %w", err)
	}
	receipt.Identity.OpID = opID
	if !validL1PromotionArchiveIdentity(receipt.Identity) || receipt.EventID == "" || !validArchiveSHA256(receipt.EventSHA256) ||
		!validArchiveSHA256(receipt.ResultSHA256) || receipt.CreatedAt.IsZero() || !json.Valid([]byte(receipt.ResultJSON)) {
		return receipt, false, errors.New("L1 promotion archive receipt is malformed")
	}
	hash := sha256.Sum256([]byte(receipt.ResultJSON))
	if receipt.ResultSHA256 != hex.EncodeToString(hash[:]) {
		return receipt, false, errors.New("L1 promotion archive receipt result integrity mismatch")
	}
	return receipt, true, nil
}

func verifyL1PromotionArchiveEffect(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}, expected l1sqlite.L1MemoryEvent) error {
	stored, found, err := findArchiveMemoryEventByID(ctx, query, expected.ID)
	if err != nil {
		return fmt.Errorf("read L1 promotion archive effect: %w", err)
	}
	if !found || !archiveL1MemoryEventEqual(stored, expected) {
		return errors.New("L1 promotion archive receipt has no exact event effect")
	}
	return nil
}

func validL1PromotionArchiveIdentity(identity l1sqlite.ConversationL1OperationIdentity) bool {
	return identity.Operation == "promote_memory_to_namespace" && l1PromotionArchiveOpIDPattern.MatchString(identity.OpID) &&
		validArchiveSHA256(identity.PayloadSHA256) && identity.WriterGeneration > 0
}

func l1PromotionArchiveReceiptEqual(stored, expected l1PromotionArchiveReceipt) bool {
	return stored.Identity == expected.Identity && stored.EventID == expected.EventID && stored.EventSHA256 == expected.EventSHA256 &&
		stored.ResultJSON == expected.ResultJSON && stored.ResultSHA256 == expected.ResultSHA256
}

var _ l1sqlite.L1PromotionArchiveStore = (*ArchiveSQLiteStore)(nil)
