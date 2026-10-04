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
	"io"
	"regexp"
	"time"
)

const (
	conversationL1OperationReceiptTable = "conversation_l1_operation_receipt"
	conversationL1OperationMaxResult    = 8 << 20
)

var conversationL1OperationIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// ConversationL1OperationIdentity binds a storage-host L1 mutation to the
// exact request accepted by the host. WriterGeneration is the generation that
// first began the operation, so it remains stable during host restart recovery.
type ConversationL1OperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

// ConversationL1SearchCacheInvalidationResult keeps the affected count bound
// to the exact audit event emitted by the same owner transaction.
type ConversationL1SearchCacheInvalidationResult struct {
	Affected int64  `json:"affected"`
	EventID  string `json:"event_id"`
}

var conversationL1Operations = map[string]struct{}{
	"save_message":                {},
	"save_search_cache":           {},
	"invalidate_search_cache":     {},
	"append_event":                {},
	"update_memory_state":         {},
	"promote_memory_to_namespace": {},
	"save_recall_trace":           {},
}

type conversationL1ReceiptQuery interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func (identity ConversationL1OperationIdentity) validate() error {
	if !conversationL1OperationIDPattern.MatchString(identity.OpID) {
		return errors.New("conversation L1 operation op_id is invalid")
	}
	if _, ok := conversationL1Operations[identity.Operation]; !ok {
		return errors.New("conversation L1 operation name is invalid")
	}
	if len(identity.PayloadSHA256) != sha256.Size*2 {
		return errors.New("conversation L1 operation payload hash is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil || identity.PayloadSHA256 != string(bytes.ToLower([]byte(identity.PayloadSHA256))) {
		return errors.New("conversation L1 operation payload hash is invalid")
	}
	if identity.WriterGeneration <= 0 {
		return errors.New("conversation L1 operation writer generation is invalid")
	}
	return nil
}

func (s *L1SQLiteStore) initConversationL1OperationSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS conversation_l1_operation_receipt (
	op_id TEXT NOT NULL PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
	operation TEXT NOT NULL CHECK(operation IN (
		'save_message', 'save_search_cache', 'invalidate_search_cache', 'append_event',
		'update_memory_state', 'promote_memory_to_namespace', 'save_recall_trace'
	)),
	payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
	writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
	result_json TEXT NOT NULL CHECK(length(result_json) <= 8388608),
	result_sha256 TEXT NOT NULL DEFAULT '' CHECK(result_sha256 = '' OR (length(result_sha256) = 64 AND lower(result_sha256) = result_sha256 AND result_sha256 NOT GLOB '*[^0-9a-f]*')),
	created_at TIMESTAMP NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("failed to initialize conversation L1 operation receipt: %w", err)
	}
	if err := s.ensureConversationL1OperationResultHashColumn(ctx); err != nil {
		return fmt.Errorf("failed to migrate conversation L1 operation receipt integrity: %w", err)
	}
	if err := initL1PromotionArchiveOutboxSchema(ctx, s.db); err != nil {
		return err
	}
	return nil
}

func (s *L1SQLiteStore) ensureConversationL1OperationResultHashColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(conversation_l1_operation_receipt)`)
	if err != nil {
		return err
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return err
		}
		if name == "result_sha256" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if found {
		return nil
	}
	_, err = s.db.ExecContext(ctx, `ALTER TABLE conversation_l1_operation_receipt ADD COLUMN result_sha256 TEXT NOT NULL DEFAULT ''`)
	return err
}

// LookupConversationL1OperationReceipt is the read-only owner reconciliation
// path. A missing row is distinguishable from a failed, malformed, or
// mismatched lookup; callers may treat only the former as confirmed absence.
func (s *L1SQLiteStore) LookupConversationL1OperationReceipt(ctx context.Context, identity ConversationL1OperationIdentity) (json.RawMessage, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("conversation L1 operation owner is unavailable")
	}
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	result, found, err := lookupConversationL1OperationReceipt(ctx, s.db, identity)
	if err != nil || !found || identity.Operation != "promote_memory_to_namespace" || s.archiveStore == nil {
		return result, found, err
	}
	var event *L1MemoryEvent
	if err := decodeConversationL1OperationResult(result, &event); err != nil || event == nil {
		return nil, false, errors.New("conversation L1 promotion receipt result is malformed")
	}
	if err := s.completeL1PromotionArchive(ctx, identity, *event); err != nil {
		return nil, false, err
	}
	return result, true, nil
}

func lookupConversationL1OperationReceipt(ctx context.Context, query conversationL1ReceiptQuery, identity ConversationL1OperationIdentity) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	var operation, payloadSHA256, resultJSON, resultSHA256 string
	var writerGeneration int64
	var createdAt time.Time
	err := query.QueryRowContext(ctx, `
SELECT operation, payload_sha256, writer_generation, result_json, result_sha256, created_at
FROM conversation_l1_operation_receipt
WHERE op_id = ?`, identity.OpID).Scan(&operation, &payloadSHA256, &writerGeneration, &resultJSON, &resultSHA256, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read conversation L1 operation receipt: %w", err)
	}
	if operation != identity.Operation || payloadSHA256 != identity.PayloadSHA256 || writerGeneration != identity.WriterGeneration {
		return nil, false, errors.New("conversation L1 operation receipt identity mismatch")
	}
	if createdAt.IsZero() || len(resultJSON) == 0 || len(resultJSON) > conversationL1OperationMaxResult || !json.Valid([]byte(resultJSON)) {
		return nil, false, errors.New("conversation L1 operation receipt result is malformed")
	}
	if len(resultSHA256) != sha256.Size*2 || resultSHA256 != string(bytes.ToLower([]byte(resultSHA256))) {
		return nil, false, errors.New("conversation L1 operation receipt result integrity is missing or malformed")
	}
	if _, err := hex.DecodeString(resultSHA256); err != nil {
		return nil, false, errors.New("conversation L1 operation receipt result integrity is malformed")
	}
	resultHash := sha256.Sum256([]byte(resultJSON))
	if resultSHA256 != hex.EncodeToString(resultHash[:]) {
		return nil, false, errors.New("conversation L1 operation receipt result integrity mismatch")
	}
	return append(json.RawMessage(nil), resultJSON...), true, nil
}

func (s *L1SQLiteStore) beginConversationL1Operation(ctx context.Context, identity ConversationL1OperationIdentity) (*sql.Tx, json.RawMessage, bool, error) {
	if s == nil || s.db == nil {
		return nil, nil, false, errors.New("conversation L1 operation owner is unavailable")
	}
	if err := identity.validate(); err != nil {
		return nil, nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	result, found, err := lookupConversationL1OperationReceipt(ctx, tx, identity)
	if err != nil {
		return nil, nil, false, rollbackL1Tx(tx, err)
	}
	if found {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return nil, nil, false, fmt.Errorf("failed to close duplicate conversation L1 operation transaction: %w", err)
		}
		return nil, result, true, nil
	}
	return tx, nil, false, nil
}

func commitConversationL1Operation(ctx context.Context, tx *sql.Tx, identity ConversationL1OperationIdentity, result any) error {
	if tx == nil {
		return errors.New("conversation L1 operation transaction is missing")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > conversationL1OperationMaxResult {
		if err == nil {
			err = errors.New("conversation L1 operation result exceeds the bounded receipt size")
		}
		return rollbackL1Tx(tx, fmt.Errorf("failed to encode conversation L1 operation result: %w", err))
	}
	resultHash := sha256.Sum256(encoded)
	resultSHA256 := hex.EncodeToString(resultHash[:])
	if _, err := tx.ExecContext(ctx, `
INSERT INTO conversation_l1_operation_receipt(op_id, operation, payload_sha256, writer_generation, result_json, result_sha256, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, string(encoded), resultSHA256, time.Now().UTC()); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to write conversation L1 operation receipt: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit conversation L1 operation: %w", err)
	}
	return nil
}

func decodeConversationL1OperationResult(raw json.RawMessage, destination any) error {
	if len(raw) == 0 || len(raw) > conversationL1OperationMaxResult || !json.Valid(raw) {
		return errors.New("conversation L1 operation receipt result is malformed")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("failed to decode conversation L1 operation receipt result: %w", err)
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("conversation L1 operation receipt result has trailing data")
	}
	return nil
}

func validateConversationL1VoidResult(raw json.RawMessage) error {
	if !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return errors.New("conversation L1 void operation receipt result is not null")
	}
	return nil
}
