package durablestore

import (
	"bytes"
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

	domain "github.com/Nyukimin/RenCrow_CORE/internal/domain/durablestore"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// ErrDurableWorkflowStorageHostOperationConflict reports an op_id that was
// already bound to a different storage-host payload.
var ErrDurableWorkflowStorageHostOperationConflict = errors.New("durable workflow storage-host operation conflict")

// ErrDurableWorkflowStorageHostRequestConflict reports a canonical workflow
// or ActionID row that already exists for a distinct host operation.
var ErrDurableWorkflowStorageHostRequestConflict = errors.New("durable workflow storage-host request conflict")

const durableWorkflowStorageHostMaxResult = 64

var durableWorkflowStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// DurableWorkflowStorageHostOperationIdentity binds a host op_id to the exact
// typed save payload accepted by the durable-workflow owner.
type DurableWorkflowStorageHostOperationIdentity struct {
	OpID             string
	PayloadSHA256    string
	ActionID         modulecore.ActionID
	RequirementID    string
	WriterGeneration int64
}

// SaveWithReceiptForStorageHostOperation is the atomic storage-host owner
// write. The workflow, request receipt, and host receipt must share one tx.
func (s *SQLiteStore) SaveWithReceiptForStorageHostOperation(ctx context.Context, identity DurableWorkflowStorageHostOperationIdentity, result *domain.WorkflowResult, receipt domain.RequestReceipt) error {
	return s.saveWithReceiptForStorageHostOperation(ctx, identity, result, receipt)
}

// LookupStorageHostOperationReceipt is the read-only host reconciliation hook.
func (s *SQLiteStore) LookupStorageHostOperationReceipt(ctx context.Context, identity DurableWorkflowStorageHostOperationIdentity) (json.RawMessage, bool, error) {
	if s == nil || s.db == nil {
		return nil, false, errors.New("durable workflow storage-host owner is unavailable")
	}
	return lookupStorageHostOperationReceipt(ctx, s.db, identity)
}

func createStorageHostOperationReceiptTable(tx *sql.Tx) error {
	_, err := tx.Exec(`CREATE TABLE IF NOT EXISTS durable_store_workflow_storagehost_receipt (
		op_id TEXT PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64 AND lower(payload_sha256) = payload_sha256 AND payload_sha256 NOT GLOB '*[^0-9a-f]*'),
		writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
		action_id TEXT NOT NULL CHECK(length(action_id) BETWEEN 1 AND 512),
		requirement_id TEXT NOT NULL CHECK(length(requirement_id) BETWEEN 1 AND 512),
		effect_sha256 TEXT NOT NULL CHECK(length(effect_sha256) = 64 AND lower(effect_sha256) = effect_sha256 AND effect_sha256 NOT GLOB '*[^0-9a-f]*'),
		result_json TEXT NOT NULL CHECK(length(result_json) <= 64),
		result_sha256 TEXT NOT NULL CHECK(length(result_sha256) = 64 AND lower(result_sha256) = result_sha256 AND result_sha256 NOT GLOB '*[^0-9a-f]*'),
		created_at TEXT NOT NULL
	)`)
	return err
}

func (identity DurableWorkflowStorageHostOperationIdentity) validate() error {
	if !durableWorkflowStorageHostOpIDPattern.MatchString(identity.OpID) {
		return errors.New("durable workflow storage-host op_id is invalid")
	}
	if len(identity.PayloadSHA256) != sha256.Size*2 || identity.PayloadSHA256 != string(bytes.ToLower([]byte(identity.PayloadSHA256))) {
		return errors.New("durable workflow storage-host payload hash is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil {
		return errors.New("durable workflow storage-host payload hash is invalid")
	}
	actionID := strings.TrimSpace(string(identity.ActionID))
	if actionID == "" || actionID != string(identity.ActionID) || len(actionID) > 512 {
		return errors.New("durable workflow storage-host action id is invalid")
	}
	if !strings.HasPrefix(actionID, "legacy/") {
		if err := identity.ActionID.Validate(); err != nil {
			return errors.New("durable workflow storage-host action id is invalid")
		}
	}
	if strings.TrimSpace(identity.RequirementID) == "" || strings.TrimSpace(identity.RequirementID) != identity.RequirementID || len(identity.RequirementID) > 512 {
		return errors.New("durable workflow storage-host requirement id is invalid")
	}
	if identity.WriterGeneration <= 0 {
		return errors.New("durable workflow storage-host writer generation is invalid")
	}
	return nil
}

// SaveWithReceiptForStorageHostOperation is the atomic storage-host owner
// write. The workflow, request receipt, and host receipt share one SQLite tx.
func (s *SQLiteStore) saveWithReceiptForStorageHostOperation(ctx context.Context, identity DurableWorkflowStorageHostOperationIdentity, result *domain.WorkflowResult, receipt domain.RequestReceipt) error {
	if s == nil || s.db == nil {
		return errors.New("durable workflow storage-host owner is unavailable")
	}
	if err := identity.validate(); err != nil {
		return err
	}
	receipt, payload, err := prepareWorkflowSave(result, receipt)
	if err != nil {
		return err
	}
	if identity.ActionID != receipt.ActionID || identity.RequirementID != receipt.RequirementID {
		return ErrDurableWorkflowStorageHostOperationConflict
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, found, err := lookupStorageHostOperationReceipt(ctx, tx, identity); err != nil {
		return err
	} else if found {
		return nil
	}
	if conflict, err := durableWorkflowStorageHostRequestConflict(ctx, tx, result, receipt); err != nil {
		return err
	} else if conflict {
		return ErrDurableWorkflowStorageHostRequestConflict
	}
	if err := saveWithReceiptTx(ctx, tx, result, receipt, payload); err != nil {
		return err
	}
	effect, err := loadDurableWorkflowCanonicalEffect(ctx, tx, identity)
	if err != nil {
		return err
	}
	effectHash, err := hashDurableWorkflowCanonicalEffect(effect)
	if err != nil {
		return err
	}
	encoded := []byte("null")
	resultHash := sha256.Sum256(encoded)
	if _, err := tx.ExecContext(ctx, `INSERT INTO durable_store_workflow_storagehost_receipt
		(op_id, payload_sha256, writer_generation, action_id, requirement_id, effect_sha256, result_json, result_sha256, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, identity.PayloadSHA256, identity.WriterGeneration, string(identity.ActionID), identity.RequirementID,
		effectHash, string(encoded), hex.EncodeToString(resultHash[:]), time.Now().UTC().Format(timeFormat)); err != nil {
		return fmt.Errorf("write durable workflow storage-host receipt: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit durable workflow storage-host operation: %w", err)
	}
	return nil
}

type durableWorkflowStorageHostReceiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type durableWorkflowCanonicalEffect struct {
	ActionID              string `json:"action_id"`
	UserScope             string `json:"user_scope"`
	RequestPayloadHash    string `json:"request_payload_hash"`
	ReceiptRequirementID  string `json:"receipt_requirement_id"`
	ReceiptCreatedAt      string `json:"receipt_created_at"`
	WorkflowRequirementID string `json:"workflow_requirement_id"`
	WorkflowDedupeKey     string `json:"workflow_dedupe_key"`
	WorkflowStatus        string `json:"workflow_status"`
	WorkflowLifecycle     string `json:"workflow_lifecycle"`
	WorkflowCreatedAt     string `json:"workflow_created_at"`
	WorkflowUpdatedAt     string `json:"workflow_updated_at"`
	WorkflowPayload       string `json:"workflow_payload"`
}

func lookupStorageHostOperationReceipt(ctx context.Context, query durableWorkflowStorageHostReceiptQuery, identity DurableWorkflowStorageHostOperationIdentity) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	var payloadSHA256, actionID, requirementID, effectSHA256, resultJSON, resultSHA256, createdAt string
	var writerGeneration int64
	err := query.QueryRowContext(ctx, `SELECT payload_sha256, writer_generation, action_id, requirement_id, effect_sha256, result_json, result_sha256, created_at
		FROM durable_store_workflow_storagehost_receipt WHERE op_id = ?`, identity.OpID).
		Scan(&payloadSHA256, &writerGeneration, &actionID, &requirementID, &effectSHA256, &resultJSON, &resultSHA256, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read durable workflow storage-host receipt: %w", err)
	}
	if payloadSHA256 != identity.PayloadSHA256 || writerGeneration != identity.WriterGeneration || actionID != string(identity.ActionID) || requirementID != identity.RequirementID {
		return nil, false, ErrDurableWorkflowStorageHostOperationConflict
	}
	if len(resultJSON) == 0 || len(resultJSON) > durableWorkflowStorageHostMaxResult || !json.Valid([]byte(resultJSON)) {
		return nil, false, errors.New("durable workflow storage-host receipt result is malformed")
	}
	if !bytes.Equal(bytes.TrimSpace([]byte(resultJSON)), []byte("null")) {
		return nil, false, errors.New("durable workflow storage-host receipt result is not null")
	}
	resultHash := sha256.Sum256([]byte(resultJSON))
	if resultSHA256 != hex.EncodeToString(resultHash[:]) {
		return nil, false, errors.New("durable workflow storage-host receipt result integrity mismatch")
	}
	parsedCreatedAt, err := time.Parse(timeFormat, createdAt)
	if err != nil || parsedCreatedAt.IsZero() {
		return nil, false, errors.New("durable workflow storage-host receipt timestamp is malformed")
	}
	effect, err := loadDurableWorkflowCanonicalEffect(ctx, query, identity)
	if err != nil {
		return nil, false, err
	}
	actualEffectSHA256, err := hashDurableWorkflowCanonicalEffect(effect)
	if err != nil {
		return nil, false, err
	}
	if effectSHA256 != actualEffectSHA256 {
		return nil, false, errors.New("durable workflow storage-host canonical effect integrity mismatch")
	}
	return append(json.RawMessage(nil), resultJSON...), true, nil
}

func durableWorkflowStorageHostRequestConflict(ctx context.Context, query durableWorkflowStorageHostReceiptQuery, result *domain.WorkflowResult, receipt domain.RequestReceipt) (bool, error) {
	var exists int
	err := query.QueryRowContext(ctx, `SELECT 1 FROM durable_store_workflow_receipt WHERE action_id = ?`, string(receipt.ActionID)).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	if result == nil {
		return false, nil
	}
	err = query.QueryRowContext(ctx, `SELECT 1 FROM durable_store_workflow WHERE requirement_id = ? OR dedupe_key = ? LIMIT 1`, result.Requirement.RequirementID, result.Requirement.DedupeKey).Scan(&exists)
	if err == nil {
		return true, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, err
	}
	return false, nil
}

func loadDurableWorkflowCanonicalEffect(ctx context.Context, query durableWorkflowStorageHostReceiptQuery, identity DurableWorkflowStorageHostOperationIdentity) (durableWorkflowCanonicalEffect, error) {
	var effect durableWorkflowCanonicalEffect
	effect.ActionID = string(identity.ActionID)
	if err := query.QueryRowContext(ctx, `SELECT user_scope, payload_hash, requirement_id, created_at
		FROM durable_store_workflow_receipt WHERE action_id = ?`, effect.ActionID).
		Scan(&effect.UserScope, &effect.RequestPayloadHash, &effect.ReceiptRequirementID, &effect.ReceiptCreatedAt); err != nil {
		return durableWorkflowCanonicalEffect{}, fmt.Errorf("read durable workflow canonical action receipt: %w", err)
	}
	if effect.ReceiptRequirementID != identity.RequirementID {
		return durableWorkflowCanonicalEffect{}, errors.New("durable workflow canonical action receipt requirement mismatch")
	}
	createdAt, err := time.Parse(timeFormat, effect.ReceiptCreatedAt)
	if err != nil {
		return durableWorkflowCanonicalEffect{}, errors.New("durable workflow canonical action receipt timestamp is malformed")
	}
	receipt := domain.RequestReceipt{
		ActionID: identity.ActionID, UserScope: effect.UserScope, PayloadHash: effect.RequestPayloadHash,
		RequirementID: effect.ReceiptRequirementID, CreatedAt: createdAt,
	}
	if err := domain.ValidateRequestReceipt(receipt); err != nil {
		return durableWorkflowCanonicalEffect{}, errors.New("durable workflow canonical action receipt is malformed")
	}
	if err := query.QueryRowContext(ctx, `SELECT requirement_id, dedupe_key, status, lifecycle, created_at, updated_at, payload
		FROM durable_store_workflow WHERE requirement_id = ?`, identity.RequirementID).
		Scan(&effect.WorkflowRequirementID, &effect.WorkflowDedupeKey, &effect.WorkflowStatus, &effect.WorkflowLifecycle,
			&effect.WorkflowCreatedAt, &effect.WorkflowUpdatedAt, &effect.WorkflowPayload); err != nil {
		return durableWorkflowCanonicalEffect{}, fmt.Errorf("read durable workflow canonical row: %w", err)
	}
	workflow, err := decodeWorkflowPayload(effect.WorkflowRequirementID, effect.WorkflowPayload)
	if err != nil || workflow.Requirement.DedupeKey != effect.WorkflowDedupeKey || string(workflow.Status) != effect.WorkflowStatus ||
		string(workflow.Lifecycle) != effect.WorkflowLifecycle || workflow.CreatedAt.UTC().Format(timeFormat) != effect.WorkflowCreatedAt ||
		workflow.UpdatedAt.UTC().Format(timeFormat) != effect.WorkflowUpdatedAt {
		return durableWorkflowCanonicalEffect{}, errors.New("durable workflow canonical row is malformed or inconsistent")
	}
	return effect, nil
}

func hashDurableWorkflowCanonicalEffect(effect durableWorkflowCanonicalEffect) (string, error) {
	encoded, err := json.Marshal(effect)
	if err != nil {
		return "", fmt.Errorf("encode durable workflow canonical effect: %w", err)
	}
	hash := sha256.Sum256(encoded)
	return hex.EncodeToString(hash[:]), nil
}
