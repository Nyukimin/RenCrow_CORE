package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
)

const userMemoryStorageHostReceiptTable = "user_memory_storagehost_operation_receipt"

var (
	ErrUserMemoryStorageHostOperationConflict = errors.New("user memory storage-host operation identity conflict")
	userMemoryStorageHostOpIDPattern          = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// UserMemoryStorageHostOperationIdentity binds one storage RPC mutation to
// its durable journal identity and exact payload.
type UserMemoryStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

type userMemoryStorageHostEvidence struct {
	OpID             string `json:"op_id"`
	Operation        string `json:"operation"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
	ResultSHA256     string `json:"result_sha256"`
	MemoryID         string `json:"memory_id"`
	EffectEventID    string `json:"effect_event_id"`
}

var userMemoryStorageHostOperations = map[string]struct{}{
	"create_user_memory":                        {},
	"create_user_memory_candidate_with_request": {},
	"update_user_memory_state":                  {},
	"forget_user_memory":                        {},
	"supersede_user_memory":                     {},
}

func NewUserMemoryStorageHostOperationIdentity(opID, operation, payloadSHA256 string, writerGeneration int64) (UserMemoryStorageHostOperationIdentity, error) {
	identity := UserMemoryStorageHostOperationIdentity{OpID: opID, Operation: operation, PayloadSHA256: payloadSHA256, WriterGeneration: writerGeneration}
	return identity, identity.validate()
}

func (identity UserMemoryStorageHostOperationIdentity) validate() error {
	if !userMemoryStorageHostOpIDPattern.MatchString(identity.OpID) {
		return errors.New("user memory storage-host op_id is invalid")
	}
	if _, ok := userMemoryStorageHostOperations[identity.Operation]; !ok {
		return errors.New("user memory storage-host operation is invalid")
	}
	if len(identity.PayloadSHA256) != sha256.Size*2 || identity.PayloadSHA256 != strings.ToLower(identity.PayloadSHA256) {
		return errors.New("user memory storage-host payload hash is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil {
		return errors.New("user memory storage-host payload hash is invalid")
	}
	if identity.WriterGeneration <= 0 {
		return errors.New("user memory storage-host writer generation is invalid")
	}
	return nil
}

func (s *L1SQLiteStore) initUserMemoryStorageHostOperationSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS user_memory_storagehost_operation_receipt (
	op_id TEXT NOT NULL PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128 AND op_id NOT GLOB '*[^A-Za-z0-9._:-]*'),
	operation TEXT NOT NULL CHECK(operation IN (
		'create_user_memory', 'create_user_memory_candidate_with_request', 'update_user_memory_state',
		'forget_user_memory', 'supersede_user_memory'
	)),
	payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
	writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
	result_json TEXT NOT NULL CHECK(length(result_json) BETWEEN 1 AND 8388608),
	result_sha256 TEXT NOT NULL CHECK(length(result_sha256) = 64 AND lower(result_sha256) = result_sha256 AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
	result_event_id TEXT NOT NULL CHECK(length(result_event_id) BETWEEN 1 AND 128),
	created_at TIMESTAMP NOT NULL
)`)
	if err != nil {
		return fmt.Errorf("failed to initialize user memory storage-host operation receipt: %w", err)
	}
	return s.ensureUserMemoryStorageHostResultEventColumn(ctx)
}

func (s *L1SQLiteStore) ensureUserMemoryStorageHostResultEventColumn(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, `PRAGMA table_info(user_memory_storagehost_operation_receipt)`)
	if err != nil {
		return fmt.Errorf("failed to inspect user memory storage-host receipt schema: %w", err)
	}
	found := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, columnType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			_ = rows.Close()
			return fmt.Errorf("failed to inspect user memory storage-host receipt column: %w", err)
		}
		if name == "result_event_id" {
			found = true
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return fmt.Errorf("failed to inspect user memory storage-host receipt columns: %w", err)
	}
	if err := rows.Close(); err != nil {
		return fmt.Errorf("failed to close user memory storage-host receipt schema query: %w", err)
	}
	if found {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE user_memory_storagehost_operation_receipt
	ADD COLUMN result_event_id TEXT NOT NULL DEFAULT 'legacy-unbound' CHECK(length(result_event_id) BETWEEN 1 AND 128)`); err != nil {
		return fmt.Errorf("failed to migrate user memory storage-host receipt result evidence: %w", err)
	}
	return nil
}

type userMemoryStorageHostReceiptQuery interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func lookupUserMemoryStorageHostReceipt(ctx context.Context, query userMemoryStorageHostReceiptQuery, identity UserMemoryStorageHostOperationIdentity) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	var operation, payloadHash, resultJSON, resultHash string
	var generation int64
	var createdAt time.Time
	var resultEventID, eventType, eventNamespace, eventSource, eventPayload string
	err := query.QueryRowContext(ctx, `SELECT r.operation, r.payload_sha256, r.writer_generation, r.result_json, r.result_sha256, r.result_event_id, r.created_at,
	e.event_type, e.namespace, e.source, e.payload_json
	FROM user_memory_storagehost_operation_receipt AS r
	LEFT JOIN l1_event_log AS e ON e.id = r.result_event_id
	WHERE r.op_id = ?`, identity.OpID).
		Scan(&operation, &payloadHash, &generation, &resultJSON, &resultHash, &resultEventID, &createdAt, &eventType, &eventNamespace, &eventSource, &eventPayload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("failed to read user memory storage-host receipt: %w", err)
	}
	if operation != identity.Operation || payloadHash != identity.PayloadSHA256 || generation != identity.WriterGeneration {
		return nil, false, ErrUserMemoryStorageHostOperationConflict
	}
	if createdAt.IsZero() || len(resultJSON) == 0 || len(resultJSON) > 8<<20 || !json.Valid([]byte(resultJSON)) {
		return nil, false, errors.New("user memory storage-host receipt result is malformed")
	}
	if len(resultHash) != sha256.Size*2 || resultHash != strings.ToLower(resultHash) {
		return nil, false, errors.New("user memory storage-host receipt result hash is malformed")
	}
	if _, err := hex.DecodeString(resultHash); err != nil {
		return nil, false, errors.New("user memory storage-host receipt result hash is malformed")
	}
	hash := sha256.Sum256([]byte(resultJSON))
	if resultHash != hex.EncodeToString(hash[:]) {
		return nil, false, errors.New("user memory storage-host receipt result integrity mismatch")
	}
	if resultEventID == "" || eventType != "memory.user_storagehost_operation_result" || eventSource != "storagehost" {
		return nil, false, errors.New("user memory storage-host receipt result evidence is missing")
	}
	var evidence userMemoryStorageHostEvidence
	if err := decodeConversationL1OperationResult(json.RawMessage(eventPayload), &evidence); err != nil {
		return nil, false, errors.New("user memory storage-host receipt result evidence is malformed")
	}
	if evidence.OpID != identity.OpID || evidence.Operation != identity.Operation || evidence.PayloadSHA256 != identity.PayloadSHA256 ||
		evidence.WriterGeneration != identity.WriterGeneration || evidence.ResultSHA256 != resultHash {
		return nil, false, errors.New("user memory storage-host receipt result evidence binding mismatch")
	}
	memoryID, namespace, idempotentReplay, err := userMemoryStorageHostReceiptResultIdentity(identity.Operation, json.RawMessage(resultJSON))
	if err != nil || evidence.MemoryID != memoryID || eventNamespace != namespace {
		return nil, false, errors.New("user memory storage-host receipt result evidence effect mismatch")
	}
	if evidence.EffectEventID != "" {
		var effectType, effectNamespace, effectSource, effectPayload string
		if err := query.QueryRowContext(ctx, `SELECT event_type, namespace, source, payload_json FROM l1_event_log WHERE id = ?`, evidence.EffectEventID).
			Scan(&effectType, &effectNamespace, &effectSource, &effectPayload); err != nil {
			return nil, false, errors.New("user memory storage-host effect audit is missing")
		}
		expectedType := userMemoryStorageHostEffectEventType(identity.Operation)
		var payload struct {
			MemoryID string `json:"memory_id"`
		}
		validSource := effectSource == "memory"
		if identity.Operation == "create_user_memory_candidate_with_request" {
			validSource = strings.HasPrefix(effectSource, "agent:")
		}
		if effectType != expectedType || effectNamespace != namespace || !validSource ||
			json.Unmarshal([]byte(effectPayload), &payload) != nil || payload.MemoryID != memoryID {
			return nil, false, errors.New("user memory storage-host effect audit binding mismatch")
		}
	} else if identity.Operation != "create_user_memory_candidate_with_request" || !idempotentReplay {
		return nil, false, errors.New("user memory storage-host effect audit is missing")
	}
	return json.RawMessage(resultJSON), true, nil
}

func (s *L1SQLiteStore) LookupUserMemoryStorageHostOperationReceipt(ctx context.Context, identity UserMemoryStorageHostOperationIdentity) (json.RawMessage, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("user memory storage-host owner is unavailable")
	}
	return lookupUserMemoryStorageHostReceipt(ctx, s.db, identity)
}

func (s *L1SQLiteStore) beginUserMemoryStorageHostOperation(ctx context.Context, identity UserMemoryStorageHostOperationIdentity) (*sql.Tx, json.RawMessage, bool, error) {
	if s == nil || s.db == nil {
		return nil, nil, false, errors.New("user memory storage-host owner is unavailable")
	}
	if err := identity.validate(); err != nil {
		return nil, nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, false, err
	}
	result, found, err := lookupUserMemoryStorageHostReceipt(ctx, tx, identity)
	if err != nil {
		return nil, nil, false, rollbackL1Tx(tx, err)
	}
	if found {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return nil, nil, false, fmt.Errorf("failed to close duplicate user memory storage-host transaction: %w", err)
		}
		return nil, result, true, nil
	}
	return tx, nil, false, nil
}

func commitUserMemoryStorageHostOperation(ctx context.Context, tx *sql.Tx, identity UserMemoryStorageHostOperationIdentity, result any, effectEventID string) error {
	if tx == nil {
		return errors.New("user memory storage-host operation transaction is missing")
	}
	encoded, err := json.Marshal(result)
	if err != nil || len(encoded) > 8<<20 {
		if err == nil {
			err = errors.New("user memory storage-host result exceeds the bounded receipt size")
		}
		return rollbackL1Tx(tx, fmt.Errorf("failed to encode user memory storage-host result: %w", err))
	}
	resultHash := sha256.Sum256(encoded)
	memoryID, namespace, _, err := userMemoryStorageHostReceiptResultIdentity(identity.Operation, encoded)
	if err != nil {
		return rollbackL1Tx(tx, err)
	}
	resultSHA256 := hex.EncodeToString(resultHash[:])
	evidence := userMemoryStorageHostEvidence{
		OpID: identity.OpID, Operation: identity.Operation, PayloadSHA256: identity.PayloadSHA256,
		WriterGeneration: identity.WriterGeneration, ResultSHA256: resultSHA256,
		MemoryID: memoryID, EffectEventID: effectEventID,
	}
	entry, err := appendL1EventLog(ctx, tx, "memory.user_storagehost_operation_result", namespace, "", "", 0, "", map[string]interface{}{
		"op_id": evidence.OpID, "operation": evidence.Operation, "payload_sha256": evidence.PayloadSHA256,
		"writer_generation": evidence.WriterGeneration, "result_sha256": evidence.ResultSHA256,
		"memory_id": evidence.MemoryID, "effect_event_id": evidence.EffectEventID,
	}, "storagehost")
	if err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to write user memory storage-host result evidence: %w", err))
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO user_memory_storagehost_operation_receipt
	(op_id, operation, payload_sha256, writer_generation, result_json, result_sha256, result_event_id, created_at)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration,
		string(encoded), resultSHA256, entry.ID, time.Now().UTC()); err != nil {
		return rollbackL1Tx(tx, fmt.Errorf("failed to write user memory storage-host receipt: %w", err))
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit user memory storage-host operation: %w", err)
	}
	return nil
}

func userMemoryStorageHostReceiptResultIdentity(operation string, raw json.RawMessage) (string, string, bool, error) {
	if operation == "create_user_memory_candidate_with_request" {
		var result userMemoryStorageHostCandidateResult
		if err := decodeConversationL1OperationResult(raw, &result); err != nil || result.Memory == nil {
			return "", "", false, errors.New("user memory candidate receipt result is malformed")
		}
		return result.Memory.ID, result.Memory.Namespace, result.IdempotentReplay, nil
	}
	var result userMemoryStorageHostResult
	if err := decodeConversationL1OperationResult(raw, &result); err != nil || result.Memory == nil {
		return "", "", false, errors.New("user memory receipt result is malformed")
	}
	return result.Memory.ID, result.Memory.Namespace, false, nil
}

func userMemoryStorageHostEffectEventType(operation string) string {
	switch operation {
	case "create_user_memory", "create_user_memory_candidate_with_request":
		return "memory.user_created"
	case "update_user_memory_state":
		return "memory.state_updated"
	case "forget_user_memory":
		return "memory.user_forgotten"
	case "supersede_user_memory":
		return "memory.user_superseded"
	default:
		return ""
	}
}
