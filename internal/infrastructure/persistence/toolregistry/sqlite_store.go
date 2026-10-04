package toolregistry

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
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	_ "modernc.org/sqlite"
)

// SQLiteToolRegistryStore はSQLite（pure Go, modernc.org/sqlite）を使った ToolRegistry 実装。
type SQLiteToolRegistryStore struct {
	db *sql.DB
	mu sync.Mutex
}

const sqliteBusyTimeoutMilliseconds = 5000

const ToolRegistryRegisterWithReceiptOperation = "register_with_receipt"

const maxToolRegistryStorageHostResult = 1 << 20

var toolRegistryStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

var (
	// ErrToolRegistryRequestConflict indicates that a request ID was already
	// used with a different actor or payload.
	ErrToolRegistryRequestConflict = errors.New("tool registry request conflict")
	// ErrToolRegistryEntryConflict indicates that a new request attempted to
	// replace an existing ToolEntry with different content.
	ErrToolRegistryEntryConflict = errors.New("tool registry entry conflict")
	// ErrToolRegistryRequestNotFound indicates that an exact receipt is absent.
	ErrToolRegistryRequestNotFound = errors.New("tool registry request receipt not found")
)

// ToolRegistryStorageHostOperationIdentity binds one recovery proof to the
// storage-host journal entry that first accepted the request.
type ToolRegistryStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

// ToolRegistryStorageHostReceiptOwner is the storage-host-specific exact
// result hook. The canonical ToolRegistryReceiptOwner API remains unchanged.
type ToolRegistryStorageHostReceiptOwner interface {
	RegisterWithReceiptForStorageHost(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string, identity ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, error)
	FindStorageHostRegistrationResult(ctx context.Context, identity ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, bool, error)
}

// NewSQLiteToolRegistryStore は新しい SQLiteToolRegistryStore を作成する。
// dbPath が空の場合はインメモリ DB（":memory:"）を使用する。
func NewSQLiteToolRegistryStore(dbPath string) (*SQLiteToolRegistryStore, error) {
	if dbPath == "" {
		dbPath = ":memory:"
	}
	db, err := sql.Open("sqlite", fmt.Sprintf("%s?_pragma=busy_timeout%%3d%d&_time_format=sqlite", dbPath, sqliteBusyTimeoutMilliseconds))
	if err != nil {
		return nil, fmt.Errorf("failed to open tool registry sqlite: %w", err)
	}
	// Keep the in-process SQLite connection serialized. This also preserves
	// schema visibility for :memory: tests while receipt writes use a single
	// transaction as their atomic boundary.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	store := &SQLiteToolRegistryStore{db: db}
	if err := store.initTables(context.Background()); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize tool_registry table: %w", err)
	}
	return store, nil
}

// Close はデータベース接続を閉じる
func (s *SQLiteToolRegistryStore) Close() error {
	return s.db.Close()
}

// initTables は tool_registry テーブルを初期化する
func (s *SQLiteToolRegistryStore) initTables(ctx context.Context) error {
	schema := `
	PRAGMA journal_mode=WAL;
	CREATE TABLE IF NOT EXISTS tool_registry (
		name         TEXT PRIMARY KEY,
		description  TEXT NOT NULL,
		schema_json  TEXT NOT NULL,
		platforms    TEXT NOT NULL,
		source       TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL,
		created_by   TEXT NOT NULL
	);
	CREATE TABLE IF NOT EXISTS tool_registry_request_receipts (
		action_id    TEXT PRIMARY KEY,
		actor_id     TEXT NOT NULL,
		payload_hash TEXT NOT NULL,
		tool_name    TEXT NOT NULL,
		created_at   TIMESTAMP NOT NULL
	);
	CREATE TABLE IF NOT EXISTS tool_registry_storagehost_receipts (
		op_id             TEXT NOT NULL PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
		operation         TEXT NOT NULL CHECK(operation = 'register_with_receipt'),
		payload_sha256    TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
		writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
		result_json       TEXT NOT NULL CHECK(length(result_json) BETWEEN 1 AND 1048576),
		result_sha256     TEXT NOT NULL CHECK(length(result_sha256) = 64 AND lower(result_sha256) = result_sha256 AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
		effect_sha256     TEXT NOT NULL CHECK(length(effect_sha256) = 64 AND lower(effect_sha256) = effect_sha256 AND effect_sha256 NOT GLOB '*[^0-9a-f]*'),
		proof_sha256      TEXT NOT NULL CHECK(length(proof_sha256) = 64 AND lower(proof_sha256) = proof_sha256 AND proof_sha256 NOT GLOB '*[^0-9a-f]*'),
		created_at        TIMESTAMP NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_tool_registry_request_receipts_tool_name
		ON tool_registry_request_receipts(tool_name);
	`
	_, err := s.db.ExecContext(ctx, schema)
	return err
}

// Register はツールを登録または更新する（name が同じ場合は上書き）
func (s *SQLiteToolRegistryStore) Register(ctx context.Context, entry capability.ToolEntry) error {
	platformsJSON, err := json.Marshal(entry.Platforms)
	if err != nil {
		return fmt.Errorf("marshal platforms: %w", err)
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = time.Now()
	}

	query := `
	INSERT INTO tool_registry (name, description, schema_json, platforms, source, created_at, created_by)
	VALUES (?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (name) DO UPDATE SET
		description = excluded.description,
		schema_json = excluded.schema_json,
		platforms   = excluded.platforms,
		source      = excluded.source,
		created_by  = excluded.created_by
	`
	_, err = s.db.ExecContext(ctx, query,
		entry.Name,
		entry.Description,
		entry.SchemaJSON,
		string(platformsJSON),
		string(entry.Source),
		entry.CreatedAt,
		entry.CreatedBy,
	)
	if err != nil {
		return fmt.Errorf("register tool %q: %w", entry.Name, err)
	}
	return nil
}

// RegisterWithReceipt is the receipt-aware Owner write path. It never
// overwrites an existing ToolEntry: an identical entry receives a semantic
// dedupe receipt, while different content is rejected. The entry and receipt
// are inserted in one SQLite transaction.
func (s *SQLiteToolRegistryStore) RegisterWithReceipt(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string) (capability.ToolRegistryRegistrationResult, error) {
	return s.registerWithReceipt(ctx, entry, actionID, actorID, payloadHash, nil)
}

// RegisterWithReceiptForStorageHost commits the normal action receipt and an
// exact storage-host recovery proof atomically with the canonical tool row.
func (s *SQLiteToolRegistryStore) RegisterWithReceiptForStorageHost(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string, identity ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, error) {
	if err := identity.validate(); err != nil {
		return capability.ToolRegistryRegistrationResult{}, err
	}
	return s.registerWithReceipt(ctx, entry, actionID, actorID, payloadHash, &identity)
}

func (s *SQLiteToolRegistryStore) registerWithReceipt(ctx context.Context, entry capability.ToolEntry, actionID modulecore.ActionID, actorID, payloadHash string, identity *ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, error) {
	if s == nil || s.db == nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("tool registry sqlite store is closed")
	}
	actionID = modulecore.ActionID(strings.TrimSpace(string(actionID)))
	actorID = strings.TrimSpace(actorID)
	payloadHash = strings.TrimSpace(payloadHash)
	entry.Name = strings.TrimSpace(entry.Name)
	if strings.TrimSpace(string(actionID)) == "" || actorID == "" || payloadHash == "" || entry.Name == "" {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("tool registry receipt fields are required")
	}

	// The Owner boundary controls timestamps. A caller-provided CreatedAt is
	// deliberately ignored so model payloads cannot forge durable time.
	entry.CreatedAt = time.Now().UTC()
	platformsJSON, err := json.Marshal(entry.Platforms)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("marshal tool platforms: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("begin tool registry receipt transaction: %w", err)
	}
	defer tx.Rollback()
	if identity != nil {
		previous, found, err := lookupToolRegistryStorageHostReceipt(ctx, tx, *identity)
		if err != nil {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("read tool registry storage-host receipt: %w", err)
		}
		if found {
			if previous.Receipt.ActionID != actionID || previous.Receipt.ActorID != actorID || previous.Receipt.PayloadHash != payloadHash || previous.Receipt.ToolName != entry.Name {
				return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("tool registry storage-host receipt does not match action request")
			}
			return previous, nil
		}
	}

	var receipt capability.ToolRegistryRequestReceipt
	var receiptCreatedAt time.Time
	var actionIDRaw string
	requestReplay := false
	err = tx.QueryRowContext(ctx, `
		SELECT action_id, actor_id, payload_hash, tool_name, created_at
		FROM tool_registry_request_receipts WHERE action_id = ?
	`, string(actionID)).Scan(
		&actionIDRaw, &receipt.ActorID, &receipt.PayloadHash, &receipt.ToolName, &receiptCreatedAt,
	)
	if err == nil {
		receipt.ActionID = modulecore.ActionID(actionIDRaw)
		receipt.CreatedAt = receiptCreatedAt
		if receipt.ActorID != actorID || receipt.PayloadHash != payloadHash || receipt.ToolName != entry.Name {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("%w: action_id %q", ErrToolRegistryRequestConflict, actionID)
		}
		requestReplay = true
	}
	if err != nil && err != sql.ErrNoRows {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("read tool registry request receipt: %w", err)
	}

	semanticDedupe := false
	if !requestReplay {
		rowEntry, found, err := scanToolEntry(tx.QueryRowContext(ctx, `
			SELECT name, description, schema_json, platforms, source, created_at, created_by
			FROM tool_registry WHERE name = ?
		`, entry.Name))
		if err != nil {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("read existing tool %q: %w", entry.Name, err)
		}
		if found {
			if !toolEntriesEquivalent(rowEntry, entry) {
				return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("%w: tool %q", ErrToolRegistryEntryConflict, entry.Name)
			}
			semanticDedupe = true
		} else if _, err := tx.ExecContext(ctx, `
			INSERT INTO tool_registry (name, description, schema_json, platforms, source, created_at, created_by)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, entry.Name, entry.Description, entry.SchemaJSON, string(platformsJSON), string(entry.Source), entry.CreatedAt, entry.CreatedBy); err != nil {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("insert tool %q: %w", entry.Name, err)
		}
		receipt = capability.ToolRegistryRequestReceipt{
			ActionID: actionID, ActorID: actorID, PayloadHash: payloadHash,
			ToolName: entry.Name, CreatedAt: entry.CreatedAt,
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO tool_registry_request_receipts (action_id, actor_id, payload_hash, tool_name, created_at)
			VALUES (?, ?, ?, ?, ?)
		`, string(receipt.ActionID), receipt.ActorID, receipt.PayloadHash, receipt.ToolName, receipt.CreatedAt); err != nil {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("insert tool registry request receipt: %w", err)
		}
	}
	result := capability.ToolRegistryRegistrationResult{Receipt: receipt, RequestReplay: requestReplay, SemanticDedupe: semanticDedupe}
	if identity != nil {
		if err := insertToolRegistryStorageHostReceipt(ctx, tx, *identity, result); err != nil {
			return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("write tool registry storage-host receipt: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return capability.ToolRegistryRegistrationResult{}, fmt.Errorf("commit tool registry registration: %w", err)
	}
	return result, nil
}

// FindStorageHostRegistrationResult returns the original typed result only
// when its operation identity, result digest, proof digest and current owner
// effect all match. A missing proof is distinct from a malformed proof.
func (s *SQLiteToolRegistryStore) FindStorageHostRegistrationResult(ctx context.Context, identity ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, bool, error) {
	if s == nil || s.db == nil {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry sqlite store is closed")
	}
	if err := identity.validate(); err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return lookupToolRegistryStorageHostReceipt(ctx, s.db, identity)
}

func (identity ToolRegistryStorageHostOperationIdentity) validate() error {
	if !toolRegistryStorageHostOpIDPattern.MatchString(identity.OpID) || identity.Operation != ToolRegistryRegisterWithReceiptOperation {
		return errors.New("tool registry storage-host operation identity is invalid")
	}
	if len(identity.PayloadSHA256) != sha256.Size*2 || identity.PayloadSHA256 != strings.ToLower(identity.PayloadSHA256) {
		return errors.New("tool registry storage-host payload hash is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil || identity.WriterGeneration <= 0 {
		return errors.New("tool registry storage-host operation generation or payload hash is invalid")
	}
	return nil
}

type toolRegistryStorageHostReceiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type toolRegistryCanonicalEffect struct {
	Receipt capability.ToolRegistryRequestReceipt `json:"receipt"`
	Entry   capability.ToolEntry                  `json:"entry"`
}

type toolRegistryStorageHostProofHashInput struct {
	OpID             string `json:"op_id"`
	Operation        string `json:"operation"`
	PayloadSHA256    string `json:"payload_sha256"`
	WriterGeneration int64  `json:"writer_generation"`
	ResultSHA256     string `json:"result_sha256"`
	EffectSHA256     string `json:"effect_sha256"`
	CreatedAt        string `json:"created_at"`
}

func insertToolRegistryStorageHostReceipt(ctx context.Context, tx *sql.Tx, identity ToolRegistryStorageHostOperationIdentity, result capability.ToolRegistryRegistrationResult) error {
	if err := identity.validate(); err != nil {
		return err
	}
	if err := validateToolRegistryRegistrationResult(result); err != nil {
		return err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil || len(resultJSON) == 0 || len(resultJSON) > maxToolRegistryStorageHostResult {
		return errors.New("tool registry storage-host result is invalid or exceeds its bound")
	}
	effect, err := loadToolRegistryCanonicalEffect(ctx, tx, result.Receipt.ActionID)
	if err != nil {
		return err
	}
	if !sameToolRegistryReceipt(effect.Receipt, result.Receipt) {
		return errors.New("tool registry storage-host result does not match its canonical receipt")
	}
	effectJSON, err := json.Marshal(effect)
	if err != nil {
		return err
	}
	createdAt := time.Now().UTC()
	resultSHA256 := toolRegistrySHA256(resultJSON)
	effectSHA256 := toolRegistrySHA256(effectJSON)
	proofSHA256, err := toolRegistryStorageHostProofSHA256(identity, resultSHA256, effectSHA256, createdAt)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
		INSERT INTO tool_registry_storagehost_receipts
			(op_id, operation, payload_sha256, writer_generation, result_json, result_sha256, effect_sha256, proof_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, string(resultJSON), resultSHA256, effectSHA256, proofSHA256, createdAt)
	return err
}

func lookupToolRegistryStorageHostReceipt(ctx context.Context, query toolRegistryStorageHostReceiptQuery, identity ToolRegistryStorageHostOperationIdentity) (capability.ToolRegistryRegistrationResult, bool, error) {
	if err := identity.validate(); err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, err
	}
	var operation, payloadSHA256, resultJSON, resultSHA256, effectSHA256, proofSHA256 string
	var writerGeneration int64
	var createdAt time.Time
	err := query.QueryRowContext(ctx, `
		SELECT operation, payload_sha256, writer_generation, result_json, result_sha256, effect_sha256, proof_sha256, created_at
		FROM tool_registry_storagehost_receipts WHERE op_id = ?
	`, identity.OpID).Scan(&operation, &payloadSHA256, &writerGeneration, &resultJSON, &resultSHA256, &effectSHA256, &proofSHA256, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return capability.ToolRegistryRegistrationResult{}, false, nil
	}
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, fmt.Errorf("read tool registry storage-host receipt: %w", err)
	}
	if operation != identity.Operation || payloadSHA256 != identity.PayloadSHA256 || writerGeneration != identity.WriterGeneration {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host receipt identity mismatch")
	}
	if createdAt.IsZero() || len(resultJSON) == 0 || len(resultJSON) > maxToolRegistryStorageHostResult || !json.Valid([]byte(resultJSON)) {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host result is malformed")
	}
	if !validToolRegistryDigest(resultSHA256) || resultSHA256 != toolRegistrySHA256([]byte(resultJSON)) ||
		!validToolRegistryDigest(effectSHA256) || !validToolRegistryDigest(proofSHA256) {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host receipt digest is malformed or mismatched")
	}
	var result capability.ToolRegistryRegistrationResult
	decoder := json.NewDecoder(bytes.NewReader([]byte(resultJSON)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, fmt.Errorf("decode tool registry storage-host result: %w", err)
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host result has trailing data")
	}
	if err := validateToolRegistryRegistrationResult(result); err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, err
	}
	effect, err := loadToolRegistryCanonicalEffect(ctx, query, result.Receipt.ActionID)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, err
	}
	if !sameToolRegistryReceipt(effect.Receipt, result.Receipt) {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host result receipt was substituted")
	}
	effectJSON, err := json.Marshal(effect)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, err
	}
	if effectSHA256 != toolRegistrySHA256(effectJSON) {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host canonical effect digest mismatch")
	}
	wantProofSHA256, err := toolRegistryStorageHostProofSHA256(identity, resultSHA256, effectSHA256, createdAt)
	if err != nil {
		return capability.ToolRegistryRegistrationResult{}, false, err
	}
	if proofSHA256 != wantProofSHA256 {
		return capability.ToolRegistryRegistrationResult{}, false, errors.New("tool registry storage-host proof digest mismatch")
	}
	return result, true, nil
}

func loadToolRegistryCanonicalEffect(ctx context.Context, query toolRegistryStorageHostReceiptQuery, actionID modulecore.ActionID) (toolRegistryCanonicalEffect, error) {
	var effect toolRegistryCanonicalEffect
	var actionIDRaw string
	if err := query.QueryRowContext(ctx, `
		SELECT action_id, actor_id, payload_hash, tool_name, created_at
		FROM tool_registry_request_receipts WHERE action_id = ?
	`, string(actionID)).Scan(&actionIDRaw, &effect.Receipt.ActorID, &effect.Receipt.PayloadHash, &effect.Receipt.ToolName, &effect.Receipt.CreatedAt); err != nil {
		return toolRegistryCanonicalEffect{}, fmt.Errorf("read tool registry canonical receipt: %w", err)
	}
	effect.Receipt.ActionID = modulecore.ActionID(actionIDRaw)
	entry, found, err := scanToolEntry(query.QueryRowContext(ctx, `
		SELECT name, description, schema_json, platforms, source, created_at, created_by
		FROM tool_registry WHERE name = ?
	`, effect.Receipt.ToolName))
	if err != nil {
		return toolRegistryCanonicalEffect{}, fmt.Errorf("read tool registry canonical entry: %w", err)
	}
	if !found || entry.Name != effect.Receipt.ToolName {
		return toolRegistryCanonicalEffect{}, errors.New("tool registry canonical entry is missing")
	}
	effect.Entry = entry
	return effect, nil
}

func toolRegistryStorageHostProofSHA256(identity ToolRegistryStorageHostOperationIdentity, resultSHA256, effectSHA256 string, createdAt time.Time) (string, error) {
	encoded, err := json.Marshal(toolRegistryStorageHostProofHashInput{
		OpID: identity.OpID, Operation: identity.Operation, PayloadSHA256: identity.PayloadSHA256,
		WriterGeneration: identity.WriterGeneration, ResultSHA256: resultSHA256, EffectSHA256: effectSHA256,
		CreatedAt: createdAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return "", err
	}
	return toolRegistrySHA256(encoded), nil
}

func toolRegistrySHA256(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func validToolRegistryDigest(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func validateToolRegistryRegistrationResult(result capability.ToolRegistryRegistrationResult) error {
	if result.Receipt.ActionID.Validate() != nil || strings.TrimSpace(result.Receipt.ActorID) == "" ||
		strings.TrimSpace(result.Receipt.ActorID) != result.Receipt.ActorID || result.Receipt.PayloadHash == "" ||
		strings.TrimSpace(result.Receipt.ToolName) == "" || strings.TrimSpace(result.Receipt.ToolName) != result.Receipt.ToolName ||
		result.Receipt.CreatedAt.IsZero() || (result.RequestReplay && result.SemanticDedupe) {
		return errors.New("tool registry storage-host result is invalid")
	}
	return nil
}

func sameToolRegistryReceipt(left, right capability.ToolRegistryRequestReceipt) bool {
	return left.ActionID == right.ActionID && left.ActorID == right.ActorID && left.PayloadHash == right.PayloadHash &&
		left.ToolName == right.ToolName && left.CreatedAt.Equal(right.CreatedAt)
}

// FindActionReceipt returns the exact durable receipt for actionID.
func (s *SQLiteToolRegistryStore) FindActionReceipt(ctx context.Context, actionID modulecore.ActionID) (capability.ToolRegistryRequestReceipt, bool, error) {
	if s == nil || s.db == nil {
		return capability.ToolRegistryRequestReceipt{}, false, fmt.Errorf("tool registry sqlite store is closed")
	}
	actionID = modulecore.ActionID(strings.TrimSpace(string(actionID)))
	if strings.TrimSpace(string(actionID)) == "" {
		return capability.ToolRegistryRequestReceipt{}, false, nil
	}
	var receipt capability.ToolRegistryRequestReceipt
	var actionIDRaw string
	var createdAt time.Time
	err := s.db.QueryRowContext(ctx, `
		SELECT action_id, actor_id, payload_hash, tool_name, created_at
		FROM tool_registry_request_receipts WHERE action_id = ?
	`, string(actionID)).Scan(&actionIDRaw, &receipt.ActorID, &receipt.PayloadHash, &receipt.ToolName, &createdAt)
	if err == sql.ErrNoRows {
		return capability.ToolRegistryRequestReceipt{}, false, nil
	}
	if err != nil {
		return capability.ToolRegistryRequestReceipt{}, false, fmt.Errorf("find tool registry action receipt %q: %w", actionID, err)
	}
	receipt.ActionID = modulecore.ActionID(actionIDRaw)
	receipt.CreatedAt = createdAt
	return receipt, true, nil
}

// GetActionReceipt is the strict form of FindActionReceipt for direct
// persistence callers that want an error when the action is absent.
func (s *SQLiteToolRegistryStore) GetActionReceipt(ctx context.Context, actionID modulecore.ActionID) (capability.ToolRegistryRequestReceipt, error) {
	receipt, found, err := s.FindActionReceipt(ctx, actionID)
	if err != nil {
		return capability.ToolRegistryRequestReceipt{}, err
	}
	if !found {
		return capability.ToolRegistryRequestReceipt{}, fmt.Errorf("%w: %q", ErrToolRegistryRequestNotFound, strings.TrimSpace(string(actionID)))
	}
	return receipt, nil
}

func scanToolEntry(scanner interface{ Scan(...any) error }) (capability.ToolEntry, bool, error) {
	var entry capability.ToolEntry
	var platformsJSON, source string
	var createdAt time.Time
	if err := scanner.Scan(
		&entry.Name, &entry.Description, &entry.SchemaJSON,
		&platformsJSON, &source, &createdAt, &entry.CreatedBy,
	); err == sql.ErrNoRows {
		return capability.ToolEntry{}, false, nil
	} else if err != nil {
		return capability.ToolEntry{}, false, err
	}
	if err := decodePlatforms(platformsJSON, &entry.Platforms); err != nil {
		return capability.ToolEntry{}, false, fmt.Errorf("decode tool platforms: %w", err)
	}
	entry.Source = capability.ToolSource(source)
	entry.CreatedAt = createdAt
	return entry, true, nil
}

func decodePlatforms(raw string, destination *[]string) error {
	trimmed := strings.TrimSpace(raw)
	if len(trimmed) < 2 || trimmed[0] != '[' || trimmed[len(trimmed)-1] != ']' {
		return errors.New("platforms must be a JSON array")
	}
	var values []string
	if err := json.Unmarshal([]byte(trimmed), &values); err != nil || values == nil {
		if err != nil {
			return err
		}
		return errors.New("platforms array is null")
	}
	*destination = values
	return nil
}

func toolEntriesEquivalent(left, right capability.ToolEntry) bool {
	// CreatedBy is trusted request metadata and may differ for a new semantic
	// request; it must not turn identical tool content into an overwrite.
	if left.Name != right.Name || left.Description != right.Description || left.SchemaJSON != right.SchemaJSON || left.Source != right.Source {
		return false
	}
	leftPlatforms := append([]string(nil), left.Platforms...)
	rightPlatforms := append([]string(nil), right.Platforms...)
	sort.Strings(leftPlatforms)
	sort.Strings(rightPlatforms)
	if len(leftPlatforms) != len(rightPlatforms) {
		return false
	}
	for i := range leftPlatforms {
		if strings.TrimSpace(leftPlatforms[i]) != strings.TrimSpace(rightPlatforms[i]) {
			return false
		}
	}
	return true
}

// ListForPlatform は指定プラットフォームに対応するツールを返す
func (s *SQLiteToolRegistryStore) ListForPlatform(ctx context.Context, platform string) ([]capability.ToolEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT name, description, schema_json, platforms, source, created_at, created_by
		FROM tool_registry
		WHERE platforms LIKE ?
		ORDER BY name
	`, "%\""+platform+"\"%")
	if err != nil {
		return nil, fmt.Errorf("list tools for platform %q: %w", platform, err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

// Get は名前でツールを取得する
func (s *SQLiteToolRegistryStore) Get(ctx context.Context, name string) (capability.ToolEntry, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT name, description, schema_json, platforms, source, created_at, created_by
		FROM tool_registry WHERE name = ?
	`, name)

	var e capability.ToolEntry
	var platformsJSON, source string
	var createdAt time.Time

	if err := row.Scan(
		&e.Name, &e.Description, &e.SchemaJSON,
		&platformsJSON, &source, &createdAt, &e.CreatedBy,
	); err == sql.ErrNoRows {
		return capability.ToolEntry{}, fmt.Errorf("%w: %q", capability.ErrToolRegistryEntryNotFound, name)
	} else if err != nil {
		return capability.ToolEntry{}, fmt.Errorf("get tool %q: %w", name, err)
	}

	if err := decodePlatforms(platformsJSON, &e.Platforms); err != nil {
		return capability.ToolEntry{}, fmt.Errorf("get tool %q: decode platforms: %w", name, err)
	}
	e.Source = capability.ToolSource(source)
	e.CreatedAt = createdAt
	return e, nil
}

// scanEntries は *sql.Rows から ToolEntry スライスを読み取る
func scanEntries(rows *sql.Rows) ([]capability.ToolEntry, error) {
	var entries []capability.ToolEntry
	for rows.Next() {
		var e capability.ToolEntry
		var platformsJSON, source string
		var createdAt time.Time

		if err := rows.Scan(
			&e.Name, &e.Description, &e.SchemaJSON,
			&platformsJSON, &source, &createdAt, &e.CreatedBy,
		); err != nil {
			return nil, err
		}

		if err := decodePlatforms(platformsJSON, &e.Platforms); err != nil {
			return nil, fmt.Errorf("decode tool platforms for %q: %w", e.Name, err)
		}
		e.Source = capability.ToolSource(source)
		e.CreatedAt = createdAt
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

// コンパイル時インターフェース適合チェック
var _ capability.ToolRegistry = (*SQLiteToolRegistryStore)(nil)
var _ capability.ToolRegistryReceiptOwner = (*SQLiteToolRegistryStore)(nil)
