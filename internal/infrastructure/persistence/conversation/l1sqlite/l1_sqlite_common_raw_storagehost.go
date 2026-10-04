package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const commonRawStorageHostOperation = "intake_common_raw"

var commonRawStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type CommonRawStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

type CommonRawStorageHostReceiptState uint8

const (
	CommonRawStorageHostReceiptAbsent CommonRawStorageHostReceiptState = iota
	CommonRawStorageHostReceiptPending
	CommonRawStorageHostReceiptCommitted
)

func NewCommonRawStorageHostOperationIdentity(opID, payloadSHA256 string, writerGeneration int64) (CommonRawStorageHostOperationIdentity, error) {
	identity := CommonRawStorageHostOperationIdentity{OpID: opID, Operation: commonRawStorageHostOperation, PayloadSHA256: payloadSHA256, WriterGeneration: writerGeneration}
	if err := identity.validate(); err != nil {
		return CommonRawStorageHostOperationIdentity{}, err
	}
	return identity, nil
}

func (identity CommonRawStorageHostOperationIdentity) validate() error {
	if !commonRawStorageHostOpIDPattern.MatchString(identity.OpID) || identity.Operation != commonRawStorageHostOperation || identity.WriterGeneration <= 0 {
		return errors.New("common raw storage-host operation identity is invalid")
	}
	if len(identity.PayloadSHA256) != sha256.Size*2 || identity.PayloadSHA256 != strings.ToLower(identity.PayloadSHA256) {
		return errors.New("common raw storage-host payload hash is invalid")
	}
	if _, err := hex.DecodeString(identity.PayloadSHA256); err != nil {
		return errors.New("common raw storage-host payload hash is invalid")
	}
	return nil
}

func (s *L1SQLiteStore) ensureCommonRawStorageHostReceiptSchema(ctx context.Context) error {
	if s == nil || s.db == nil {
		return errors.New("common raw storage-host owner is unavailable")
	}
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS l1_common_raw_storagehost_operation_receipt (
		op_id TEXT PRIMARY KEY CHECK(length(op_id) BETWEEN 1 AND 128),
		operation TEXT NOT NULL CHECK(operation = 'intake_common_raw'),
		payload_sha256 TEXT NOT NULL CHECK(length(payload_sha256) = 64),
		writer_generation INTEGER NOT NULL CHECK(writer_generation > 0),
		status TEXT NOT NULL CHECK(status IN ('pending', 'retryable', 'committed')),
		result_json TEXT NOT NULL DEFAULT '',
		result_sha256 TEXT NOT NULL DEFAULT '',
		effect_manifest_id TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMP NOT NULL,
		updated_at TIMESTAMP NOT NULL
	)`)
	if err != nil {
		return fmt.Errorf("create common raw storage-host receipt schema: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) beginCommonRawStorageHostOperation(ctx context.Context, identity CommonRawStorageHostOperationIdentity, requestID, ownerID, actorID string, input domainmemory.CommonRawIntakeRequest) (CommonRawStorageHostReceiptState, bool, domainmemory.CommonRawIntakeReceipt, error) {
	if err := identity.validate(); err != nil {
		return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
	}
	if err := s.ensureCommonRawStorageHostReceiptSchema(ctx); err != nil {
		return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, `INSERT INTO l1_common_raw_storagehost_operation_receipt
		(op_id, operation, payload_sha256, writer_generation, status, created_at, updated_at)
		VALUES (?, ?, ?, ?, 'pending', ?, ?) ON CONFLICT(op_id) DO NOTHING`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, now, now)
	if err != nil {
		return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
	}
	if inserted == 0 {
		resumed, err := tx.ExecContext(ctx, `UPDATE l1_common_raw_storagehost_operation_receipt SET status = 'pending', updated_at = ?
			WHERE op_id = ? AND operation = ? AND payload_sha256 = ? AND writer_generation = ? AND status = 'retryable'`, now, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration)
		if err != nil {
			return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
		}
		resumedCount, err := resumed.RowsAffected()
		if err != nil {
			return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
		}
		if resumedCount == 1 {
			if err := tx.Commit(); err != nil {
				return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
			}
			return CommonRawStorageHostReceiptPending, true, domainmemory.CommonRawIntakeReceipt{}, nil
		}
	}
	if err := tx.Commit(); err != nil {
		return CommonRawStorageHostReceiptAbsent, false, domainmemory.CommonRawIntakeReceipt{}, err
	}
	if inserted == 1 {
		return CommonRawStorageHostReceiptPending, true, domainmemory.CommonRawIntakeReceipt{}, nil
	}
	state, receipt, err := s.LookupCommonRawStorageHostOperationReceipt(ctx, identity, requestID, ownerID, actorID, input)
	return state, false, receipt, err
}

// IntakeCommonRawStorageHost is the storage-host mutation boundary. A pending
// owner receipt is committed before any object file is written; the exact
// result receipt is committed in the same SQLite transaction as the manifest,
// raw records, and ingested state events.
func (s *L1SQLiteStore) IntakeCommonRawStorageHost(ctx context.Context, identity CommonRawStorageHostOperationIdentity, requestID, ownerID, actorID string, input domainmemory.CommonRawIntakeRequest) (domainmemory.CommonRawIntakeReceipt, error) {
	if s == nil || s.db == nil {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "conversation_l1 store is unavailable")
	}
	requestID, ownerID, actorID = strings.TrimSpace(requestID), strings.TrimSpace(ownerID), strings.TrimSpace(actorID)
	if err := identity.validate(); err != nil || ctx == nil || requestID == "" || ownerID == "" || actorID == "" {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "common raw storage-host operation identity is invalid")
	}
	if err := validateCommonRawOwnerScope(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	}
	prepared, err := prepareCommonRawIntake(ownerID, input)
	if err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	}
	if prepared.requiresObject && commonRawRootPathError(s.rawSourceRoot) != nil {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}
	state, prior, err := s.LookupCommonRawStorageHostOperationReceipt(ctx, identity, requestID, ownerID, actorID, input)
	if err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	}
	if state == CommonRawStorageHostReceiptCommitted {
		return prior, nil
	}
	if state == CommonRawStorageHostReceiptPending {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host outcome is unresolved")
	}
	state, created, prior, err := s.beginCommonRawStorageHostOperation(ctx, identity, requestID, ownerID, actorID, input)
	if err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host operation could not be reserved")
	}
	if state == CommonRawStorageHostReceiptCommitted {
		return prior, nil
	}
	if !created {
		if state != CommonRawStorageHostReceiptAbsent {
			return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host outcome is unresolved")
		}
		noEffect, proofErr := s.commonRawStorageHostEffectAbsent(ctx, ownerID, prepared)
		if proofErr != nil || !noEffect {
			return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host outcome is unresolved")
		}
		if err := s.markCommonRawStorageHostRetryable(ctx, identity); err != nil {
			return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host outcome is unresolved")
		}
		state, created, prior, err = s.beginCommonRawStorageHostOperation(ctx, identity, requestID, ownerID, actorID, input)
		if err != nil {
			return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host operation could not resume")
		}
		if state == CommonRawStorageHostReceiptCommitted {
			return prior, nil
		}
	}
	if state != CommonRawStorageHostReceiptPending || !created {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "common raw storage-host outcome is unresolved")
	}
	return s.intakeCommonRaw(ctx, requestID, ownerID, actorID, input, &identity)
}

// LookupCommonRawStorageHostOperationReceipt is read-only. A committed row is
// accepted only when its exact result, manifest, records, ingested events, and
// configured content-addressed objects still verify against the submitted
// input and stable original writer generation.
func (s *L1SQLiteStore) LookupCommonRawStorageHostOperationReceipt(ctx context.Context, identity CommonRawStorageHostOperationIdentity, requestID, ownerID, actorID string, input domainmemory.CommonRawIntakeRequest) (CommonRawStorageHostReceiptState, domainmemory.CommonRawIntakeReceipt, error) {
	if s == nil || s.db == nil || identity.validate() != nil {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host owner is unavailable")
	}
	if err := s.ensureCommonRawStorageHostReceiptSchema(ctx); err != nil {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, err
	}
	if strings.TrimSpace(requestID) != "" {
		if err := validateCommonRawOwnerScope(ctx, requestID, ownerID, actorID); err != nil {
			return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, err
		}
	}
	var operation, payloadHash, status, resultJSON, resultSHA256, effectManifestID string
	var generation int64
	err := s.db.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, status, result_json, result_sha256, effect_manifest_id
		FROM l1_common_raw_storagehost_operation_receipt WHERE op_id = ?`, identity.OpID).
		Scan(&operation, &payloadHash, &generation, &status, &resultJSON, &resultSHA256, &effectManifestID)
	if errors.Is(err, sql.ErrNoRows) {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, nil
	}
	if err != nil {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, err
	}
	if operation != identity.Operation || payloadHash != identity.PayloadSHA256 || generation != identity.WriterGeneration {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host receipt binding mismatch")
	}
	if status == "pending" || status == "retryable" {
		if strings.TrimSpace(ownerID) == "" || strings.TrimSpace(requestID) == "" || strings.TrimSpace(actorID) == "" {
			return CommonRawStorageHostReceiptPending, domainmemory.CommonRawIntakeReceipt{}, nil
		}
		prepared, err := prepareCommonRawIntake(ownerID, input)
		if err != nil {
			return CommonRawStorageHostReceiptPending, domainmemory.CommonRawIntakeReceipt{}, nil
		}
		noEffect, err := s.commonRawStorageHostEffectAbsent(ctx, ownerID, prepared)
		if err == nil && noEffect {
			return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, nil
		}
		return CommonRawStorageHostReceiptPending, domainmemory.CommonRawIntakeReceipt{}, nil
	}
	if status != "committed" || resultJSON == "" || effectManifestID == "" || len(resultSHA256) != sha256.Size*2 {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host receipt is malformed")
	}
	resultHash := sha256.Sum256([]byte(resultJSON))
	if hex.EncodeToString(resultHash[:]) != resultSHA256 {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host result integrity mismatch")
	}
	var receipt domainmemory.CommonRawIntakeReceipt
	if err := json.Unmarshal([]byte(resultJSON), &receipt); err != nil || receipt.ManifestID != effectManifestID || receipt.RequestID != strings.TrimSpace(requestID) {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host result proof is malformed")
	}
	if strings.TrimSpace(requestID) == "" || strings.TrimSpace(ownerID) == "" || strings.TrimSpace(actorID) == "" {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host effect scope is missing")
	}
	prepared, err := prepareCommonRawIntake(ownerID, input)
	if err != nil {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, err
	}
	stored, found, err := s.findCommonRawManifestReplay(ctx, ownerID, prepared)
	if err != nil {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, err
	}
	if !found || stored.ManifestID != receipt.ManifestID || stored.RequestID != receipt.RequestID {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host effect proof is missing")
	}
	stored.IdempotentReplay = receipt.IdempotentReplay
	storedJSON, err := json.Marshal(stored)
	if err != nil || string(storedJSON) != resultJSON {
		return CommonRawStorageHostReceiptAbsent, domainmemory.CommonRawIntakeReceipt{}, errors.New("common raw storage-host effect differs from original result")
	}
	return CommonRawStorageHostReceiptCommitted, receipt, nil
}

func (s *L1SQLiteStore) commonRawStorageHostEffectAbsent(ctx context.Context, ownerID string, prepared preparedCommonRawIntake) (bool, error) {
	manifestID := domainmemory.DeterministicCommonRawManifestID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, prepared.manifestSHA256)
	var count int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_source_manifest WHERE manifest_id = ?`, manifestID).Scan(&count); err != nil || count != 0 {
		return false, err
	}
	for _, record := range prepared.records {
		rawID := domainmemory.DeterministicCommonRawRecordID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash)
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_record WHERE raw_record_id = ? OR (owner_id = ? AND scope = ? AND source_type = ? AND source_identity = ? AND source_record_id = ?)`, rawID, ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID).Scan(&count); err != nil || count != 0 {
			return false, err
		}
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_state_event WHERE manifest_id = ?`, manifestID).Scan(&count); err != nil || count != 0 {
		return false, err
	}
	if prepared.requiresObject {
		if commonRawRootPathError(s.rawSourceRoot) != nil {
			return false, errors.New("configured common raw object root is unavailable")
		}
		refs := make([]string, 0, len(prepared.assets)+len(prepared.records))
		for _, asset := range prepared.assets {
			refs = append(refs, assetObjectRef(asset.hash))
		}
		for _, record := range prepared.records {
			if record.storage == domainmemory.CommonRawStorageObject {
				refs = append(refs, objectObjectRef(record.hash))
			}
		}
		for _, ref := range refs {
			path, err := commonRawStoredObjectPath(s.rawSourceRoot, ref)
			if err != nil {
				return false, err
			}
			if _, err := os.Lstat(path); err == nil {
				return false, nil
			} else if !errors.Is(err, os.ErrNotExist) {
				return false, err
			}
			entries, err := os.ReadDir(filepath.Dir(path))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return false, err
			}
			for _, entry := range entries {
				if strings.HasPrefix(entry.Name(), ".common-raw-") {
					return false, nil
				}
			}
		}
	}
	return true, nil
}

func (s *L1SQLiteStore) markCommonRawStorageHostRetryable(ctx context.Context, identity CommonRawStorageHostOperationIdentity) error {
	if err := identity.validate(); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.ExecContext(ctx, `UPDATE l1_common_raw_storagehost_operation_receipt SET status = 'retryable', updated_at = ?
		WHERE op_id = ? AND operation = ? AND payload_sha256 = ? AND writer_generation = ? AND status = 'pending'`, time.Now().UTC(), identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration)
	if err != nil {
		return err
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return errors.New("common raw storage-host pending receipt changed before safe retry")
	}
	return tx.Commit()
}

func writeCommonRawStorageHostReceipt(ctx context.Context, tx *sql.Tx, identity *CommonRawStorageHostOperationIdentity, receipt domainmemory.CommonRawIntakeReceipt) error {
	if identity == nil {
		return nil
	}
	if err := identity.validate(); err != nil {
		return err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	hash := sha256.Sum256(encoded)
	result, err := tx.ExecContext(ctx, `UPDATE l1_common_raw_storagehost_operation_receipt SET status = 'committed', result_json = ?, result_sha256 = ?, effect_manifest_id = ?, updated_at = ?
		WHERE op_id = ? AND operation = ? AND payload_sha256 = ? AND writer_generation = ? AND status = 'pending'`, string(encoded), hex.EncodeToString(hash[:]), receipt.ManifestID, time.Now().UTC(), identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration)
	if err != nil {
		return fmt.Errorf("write common raw storage-host exact result receipt: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil || updated != 1 {
		return errors.New("common raw storage-host result receipt was not reserved")
	}
	return nil
}

func completeCommonRawStorageHostReplay(ctx context.Context, s *L1SQLiteStore, identity *CommonRawStorageHostOperationIdentity, receipt domainmemory.CommonRawIntakeReceipt) error {
	if identity == nil {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := writeCommonRawStorageHostReceipt(ctx, tx, identity, receipt); err != nil {
		return err
	}
	return tx.Commit()
}
