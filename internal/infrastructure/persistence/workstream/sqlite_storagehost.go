package workstream

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	domainbacklog "github.com/Nyukimin/RenCrow_CORE/internal/domain/backlog"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
)

const (
	WorkstreamSaveGoalStorageHostOperation               = "save_goal"
	WorkstreamSaveWorkstreamStorageHostOperation         = "save_workstream"
	WorkstreamSaveArtifactStorageHostOperation           = "save_artifact"
	WorkstreamSaveArtifactAnnotationStorageHostOperation = "save_artifact_annotation"
	WorkstreamSaveSteeringItemStorageHostOperation       = "save_steering_item"
	WorkstreamSaveHeartbeatScheduleStorageHostOperation  = "save_heartbeat_schedule"
	WorkstreamSaveVaultUpdateLogStorageHostOperation     = "save_vault_update_log"
	WorkstreamSaveQueueFreezeStorageHostOperation        = "save_queue_freeze"
	WorkstreamSaveStageRunReceiptStorageHostOperation    = "save_stage_run_receipt"
	WorkstreamSaveClosureReceiptStorageHostOperation     = "save_closure_receipt"
	WorkstreamAcquireLeaseStorageHostOperation           = "acquire_implementation_lease_if_unfrozen"
	WorkstreamResolveFreezeStorageHostOperation          = "resolve_queue_freeze_and_acquire_lease"
	WorkstreamReleaseLeaseStorageHostOperation           = "release_implementation_lease"
	WorkstreamHeartbeatLeaseStorageHostOperation         = "heartbeat_implementation_lease"
)

const workstreamStorageHostReceiptTable = "workstream_storagehost_operation_receipt"

type WorkstreamLeaseAcquireResult struct {
	Acquired bool   `json:"acquired"`
	Reason   string `json:"reason"`
}

type WorkstreamFreezeLeaseResult struct {
	Freeze   domainworkstream.QueueFreeze         `json:"freeze"`
	Lease    domainworkstream.ImplementationLease `json:"lease"`
	Acquired bool                                 `json:"acquired"`
}

type WorkstreamLeaseMutationEffect struct {
	Lease          domainworkstream.ImplementationLease `json:"lease"`
	LeaseFound     bool                                 `json:"lease_found"`
	RequestedLease domainworkstream.ImplementationLease `json:"requested_lease,omitempty"`
	QueueFrozen    bool                                 `json:"queue_frozen,omitempty"`
	Freeze         domainworkstream.QueueFreeze         `json:"freeze,omitempty"`
	FreezeFound    bool                                 `json:"freeze_found,omitempty"`
}

var workstreamStorageHostOpIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

type WorkstreamStorageHostOperationIdentity struct {
	OpID             string
	Operation        string
	PayloadSHA256    string
	WriterGeneration int64
}

func (identity WorkstreamStorageHostOperationIdentity) validate() error {
	if !workstreamStorageHostOpIDPattern.MatchString(identity.OpID) || !validWorkstreamStorageHostOperation(identity.Operation) || len(identity.PayloadSHA256) != 64 || identity.WriterGeneration <= 0 {
		return errors.New("workstream storage-host operation identity is incomplete")
	}
	for _, char := range identity.PayloadSHA256 {
		if !(char >= '0' && char <= '9') && !(char >= 'a' && char <= 'f') {
			return errors.New("workstream storage-host payload hash is malformed")
		}
	}
	return nil
}

func validWorkstreamStorageHostOperation(operation string) bool {
	switch operation {
	case WorkstreamSaveGoalStorageHostOperation, WorkstreamSaveWorkstreamStorageHostOperation,
		WorkstreamSaveArtifactStorageHostOperation, WorkstreamSaveArtifactAnnotationStorageHostOperation,
		WorkstreamSaveSteeringItemStorageHostOperation, WorkstreamSaveHeartbeatScheduleStorageHostOperation,
		WorkstreamSaveVaultUpdateLogStorageHostOperation, WorkstreamSaveQueueFreezeStorageHostOperation,
		WorkstreamSaveStageRunReceiptStorageHostOperation, WorkstreamSaveClosureReceiptStorageHostOperation,
		WorkstreamAcquireLeaseStorageHostOperation, WorkstreamResolveFreezeStorageHostOperation,
		WorkstreamReleaseLeaseStorageHostOperation, WorkstreamHeartbeatLeaseStorageHostOperation:
		return true
	default:
		return false
	}
}

// SaveGoalForStorageHostOperation commits the Goal and its exact recovery proof
// in the same owner transaction.
func (s *SQLiteStore) SaveGoalForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.Goal) (domainworkstream.Goal, error) {
	if err := identity.validate(); err != nil {
		return domainworkstream.Goal{}, err
	}
	if err := domainworkstream.ValidateGoal(item); err != nil {
		return domainworkstream.Goal{}, err
	}
	if s == nil || s.db == nil {
		return domainworkstream.Goal{}, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return domainworkstream.Goal{}, err
	}
	if identity.Operation != WorkstreamSaveGoalStorageHostOperation {
		return domainworkstream.Goal{}, errors.New("workstream operation does not match goal save")
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "workstream_goal", "goal_id", item.GoalID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano))
}

// LookupWorkstreamStorageHostOperationReceipt accepts only an exact receipt
// whose Goal effect remains byte-for-byte equal. A missing receipt is absent
// only if the submitted Goal ID is also absent.
func (s *SQLiteStore) LookupWorkstreamStorageHostOperationReceipt(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, expected domainworkstream.Goal) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	if err := domainworkstream.ValidateGoal(expected); err != nil {
		return nil, false, err
	}
	if identity.Operation != WorkstreamSaveGoalStorageHostOperation {
		return nil, false, errors.New("workstream operation does not match goal lookup")
	}
	return s.LookupWorkstreamStorageHostOperationResult(ctx, identity, "workstream_goal", "goal_id", expected.GoalID)
}

func (s *SQLiteStore) ensureWorkstreamStorageHostReceiptSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS `+workstreamStorageHostReceiptTable+` (
		op_id TEXT PRIMARY KEY,
		operation TEXT NOT NULL,
		payload_sha256 TEXT NOT NULL,
		writer_generation INTEGER NOT NULL,
		effect_id TEXT NOT NULL,
		result_json TEXT NOT NULL,
		result_sha256 TEXT NOT NULL,
		effect_json TEXT NOT NULL DEFAULT '',
		effect_sha256 TEXT NOT NULL DEFAULT ''
	)`)
	if err != nil {
		return err
	}
	if err := addColumnIfMissing(s.db, workstreamStorageHostReceiptTable, "effect_json", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return addColumnIfMissing(s.db, workstreamStorageHostReceiptTable, "effect_sha256", "TEXT NOT NULL DEFAULT ''")
}

// saveWorkstreamRowWithStorageHostReceipt is an owner-internal helper used only
// by the closed typed methods below. Its table/column arguments are constants
// selected by those methods; they are never accepted from the storage-host
// request. The canonical row and its exact typed result receipt share one TX.
func saveWorkstreamRowWithStorageHostReceipt[T any](ctx context.Context, s *SQLiteStore, identity WorkstreamStorageHostOperationIdentity, item T, table, idColumn, id string, parentColumn, parentID, createdAt string) (T, error) {
	var zero T
	if err := identity.validate(); err != nil {
		return zero, err
	}
	if s == nil || s.db == nil {
		return zero, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return zero, err
	}
	resultJSON, err := json.Marshal(item)
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, found, err := lookupWorkstreamStorageHostReceiptJSON(ctx, tx, identity, table, idColumn, id); err != nil {
		return zero, err
	} else if found {
		if !bytes.Equal(prior, resultJSON) {
			return zero, errors.New("workstream storage-host operation id is bound to a different result")
		}
		var replay T
		if err := json.Unmarshal(prior, &replay); err != nil {
			return zero, errors.New("workstream storage-host receipt result is malformed")
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return replay, nil
	}
	var writeErr error
	if parentColumn == "" {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, created_at, payload) VALUES (?, ?, ?)`, table, idColumn)
		_, writeErr = tx.ExecContext(ctx, query, id, createdAt, string(resultJSON))
	} else {
		query := fmt.Sprintf(`INSERT OR REPLACE INTO %s (%s, %s, created_at, payload) VALUES (?, ?, ?, ?)`, table, idColumn, parentColumn)
		_, writeErr = tx.ExecContext(ctx, query, id, parentID, createdAt, string(resultJSON))
	}
	if writeErr != nil {
		return zero, writeErr
	}
	var effectJSON string
	query := fmt.Sprintf(`SELECT payload FROM %s WHERE %s = ?`, table, idColumn)
	if err := tx.QueryRowContext(ctx, query, id).Scan(&effectJSON); err != nil || effectJSON != string(resultJSON) {
		return zero, errors.New("workstream storage-host canonical row differs from requested result")
	}
	if err := insertWorkstreamStorageHostReceipt(ctx, tx, identity, id, string(resultJSON), effectJSON); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return item, nil
}

func insertWorkstreamStorageHostReceipt(ctx context.Context, tx *sql.Tx, identity WorkstreamStorageHostOperationIdentity, effectID, resultJSON, effectJSON string) error {
	resultHash := fmt.Sprintf("%x", sha256.Sum256([]byte(resultJSON)))
	effectHash := fmt.Sprintf("%x", sha256.Sum256([]byte(effectJSON)))
	_, err := tx.ExecContext(ctx, `INSERT INTO `+workstreamStorageHostReceiptTable+` (op_id, operation, payload_sha256, writer_generation, effect_id, result_json, result_sha256, effect_json, effect_sha256) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, identity.OpID, identity.Operation, identity.PayloadSHA256, identity.WriterGeneration, effectID, resultJSON, resultHash, effectJSON, effectHash)
	return err
}

func lookupWorkstreamStorageHostReceiptJSON(ctx context.Context, query workstreamStorageHostReceiptQuery, identity WorkstreamStorageHostOperationIdentity, table, idColumn, expectedID string) (json.RawMessage, bool, error) {
	result, effectJSON, effectID, found, err := lookupWorkstreamStorageHostReceiptProof(ctx, query, identity)
	if err != nil || !found {
		if err != nil || found {
			return nil, found, err
		}
		var existing string
		effectErr := query.QueryRowContext(ctx, fmt.Sprintf(`SELECT payload FROM %s WHERE %s = ?`, table, idColumn), expectedID).Scan(&existing)
		if errors.Is(effectErr, sql.ErrNoRows) {
			return nil, false, nil
		}
		if effectErr != nil {
			return nil, false, effectErr
		}
		return nil, false, errors.New("workstream canonical effect exists without its exact storage-host receipt")
	}
	if effectID != expectedID {
		return nil, false, errors.New("workstream storage-host receipt binding mismatch")
	}
	if !bytes.Equal(result, []byte(effectJSON)) {
		return nil, false, errors.New("workstream storage-host receipt result differs from canonical row effect")
	}
	var effect any
	if err := json.Unmarshal([]byte(effectJSON), &effect); err != nil {
		return nil, false, errors.New("workstream storage-host receipt effect is malformed")
	}
	return result, true, nil
}

func lookupWorkstreamStorageHostReceiptProof(ctx context.Context, query workstreamStorageHostReceiptQuery, identity WorkstreamStorageHostOperationIdentity) (json.RawMessage, string, string, bool, error) {
	var operation, payloadHash, effectID, resultJSON, resultHash, effectJSON, effectHash string
	var generation int64
	err := query.QueryRowContext(ctx, `SELECT operation, payload_sha256, writer_generation, effect_id, result_json, result_sha256, effect_json, effect_sha256 FROM `+workstreamStorageHostReceiptTable+` WHERE op_id = ?`, identity.OpID).Scan(&operation, &payloadHash, &generation, &effectID, &resultJSON, &resultHash, &effectJSON, &effectHash)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", "", false, nil
	}
	if err != nil {
		return nil, "", "", false, err
	}
	if operation != identity.Operation || payloadHash != identity.PayloadSHA256 || generation != identity.WriterGeneration {
		return nil, "", "", false, errors.New("workstream storage-host receipt binding mismatch")
	}
	if fmt.Sprintf("%x", sha256.Sum256([]byte(resultJSON))) != resultHash || fmt.Sprintf("%x", sha256.Sum256([]byte(effectJSON))) != effectHash {
		return nil, "", "", false, errors.New("workstream storage-host receipt checksum mismatch")
	}
	var resultValue, effectValue any
	if err := json.Unmarshal([]byte(resultJSON), &resultValue); err != nil {
		return nil, "", "", false, errors.New("workstream storage-host receipt result is malformed")
	}
	if err := json.Unmarshal([]byte(effectJSON), &effectValue); err != nil {
		return nil, "", "", false, errors.New("workstream storage-host receipt effect is malformed")
	}
	if err := validateWorkstreamCustomReceiptProof(identity.Operation, resultJSON, effectJSON, effectID); err != nil {
		return nil, "", "", false, err
	}
	return json.RawMessage(resultJSON), effectJSON, effectID, true, nil
}

func validateWorkstreamCustomReceiptProof(operation, resultJSON, effectJSON, effectID string) error {
	switch operation {
	case WorkstreamAcquireLeaseStorageHostOperation:
		var result WorkstreamLeaseAcquireResult
		var effect WorkstreamLeaseMutationEffect
		if json.Unmarshal([]byte(resultJSON), &result) != nil || json.Unmarshal([]byte(effectJSON), &effect) != nil || effect.Lease.LeaseName != effectID && effect.LeaseFound || effect.RequestedLease.LeaseName != effectID || domainworkstream.ValidateImplementationLease(effect.RequestedLease) != nil {
			return errors.New("workstream lease acquire receipt proof is malformed")
		}
		if result.Acquired {
			if result.Reason != "" || effect.QueueFrozen || !effect.LeaseFound || effect.Lease != effect.RequestedLease {
				return errors.New("workstream lease acquire result differs from its effect proof")
			}
			return nil
		}
		if result.Reason == domainworkstream.ErrQueueFrozen.Error() && effect.QueueFrozen {
			return nil
		}
		if result.Reason == domainworkstream.ErrImplementationLeaseHeld.Error() && !effect.QueueFrozen && effect.LeaseFound && effect.Lease.HolderUnitID != "" && effect.Lease.HolderUnitID != effect.RequestedLease.HolderUnitID {
			return nil
		}
		return errors.New("workstream lease acquire result differs from its effect proof")
	case WorkstreamResolveFreezeStorageHostOperation:
		var result WorkstreamFreezeLeaseResult
		var effect WorkstreamLeaseMutationEffect
		if json.Unmarshal([]byte(resultJSON), &result) != nil || json.Unmarshal([]byte(effectJSON), &effect) != nil || effect.Freeze.FreezeID != effectID || !effect.FreezeFound || effect.RequestedLease.LeaseName == "" || effect.RequestedLease.HolderUnitID == "" || domainworkstream.ValidateImplementationLease(effect.RequestedLease) != nil {
			return errors.New("workstream freeze resolution receipt proof is malformed")
		}
		if !result.Acquired {
			freezeJSON, freezeErr := json.Marshal(result.Freeze)
			effectFreezeJSON, effectFreezeErr := json.Marshal(effect.Freeze)
			if freezeErr != nil || effectFreezeErr != nil || !bytes.Equal(freezeJSON, effectFreezeJSON) || effect.Freeze.Status != domainworkstream.QueueFreezeActive && effect.Freeze.Status != "" || !effect.LeaseFound || effect.Lease.LeaseName != effect.RequestedLease.LeaseName || effect.Lease.HolderUnitID == "" || effect.Lease.HolderUnitID == effect.RequestedLease.HolderUnitID || result.Lease != (domainworkstream.ImplementationLease{}) {
				return errors.New("workstream freeze resolution result differs from its effect proof")
			}
			return nil
		}
		freezeJSON, freezeErr := json.Marshal(result.Freeze)
		effectFreezeJSON, effectFreezeErr := json.Marshal(effect.Freeze)
		leaseJSON, leaseErr := json.Marshal(result.Lease)
		effectLeaseJSON, effectLeaseErr := json.Marshal(effect.Lease)
		if freezeErr != nil || effectFreezeErr != nil || leaseErr != nil || effectLeaseErr != nil || !bytes.Equal(freezeJSON, effectFreezeJSON) || !bytes.Equal(leaseJSON, effectLeaseJSON) || !effect.LeaseFound || effect.Lease != effect.RequestedLease || effect.Freeze.Status != domainworkstream.QueueFreezeResolved || !effect.Freeze.ResolutionAcquired || effect.Freeze.ReplacementLease != effect.RequestedLease {
			return errors.New("workstream freeze resolution result differs from its effect proof")
		}
		return nil
	case WorkstreamReleaseLeaseStorageHostOperation, WorkstreamHeartbeatLeaseStorageHostOperation:
		var effect WorkstreamLeaseMutationEffect
		if !bytes.Equal([]byte(resultJSON), []byte("{}")) || json.Unmarshal([]byte(effectJSON), &effect) != nil {
			return errors.New("workstream lease mutation receipt proof is malformed")
		}
		if effect.LeaseFound && effect.Lease.LeaseName != effectID {
			return errors.New("workstream lease mutation effect binding mismatch")
		}
		if operation == WorkstreamHeartbeatLeaseStorageHostOperation && !effect.LeaseFound {
			return errors.New("workstream heartbeat receipt lacks its lease effect")
		}
		return nil
	default:
		if !bytes.Equal([]byte(resultJSON), []byte(effectJSON)) || effectID == "" {
			return errors.New("workstream canonical row receipt result/effect mismatch")
		}
		return nil
	}
}

func (s *SQLiteStore) LookupWorkstreamStorageHostOperationResult(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, table, idColumn, effectID string) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	if s == nil || s.db == nil {
		return nil, false, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	return lookupWorkstreamStorageHostReceiptJSON(ctx, tx, identity, table, idColumn, effectID)
}

func (s *SQLiteStore) LookupWorkstreamStorageHostOperationProof(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, expectedEffectID string) (json.RawMessage, bool, error) {
	if err := identity.validate(); err != nil {
		return nil, false, err
	}
	if s == nil || s.db == nil {
		return nil, false, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return nil, false, err
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = tx.Rollback() }()
	result, _, effectID, found, err := lookupWorkstreamStorageHostReceiptProof(ctx, tx, identity)
	if err != nil || !found {
		return nil, found, err
	}
	if effectID != expectedEffectID {
		return nil, false, errors.New("workstream storage-host receipt effect binding mismatch")
	}
	return result, true, nil
}

func commitWorkstreamCustomStorageHostMutation(ctx context.Context, s *SQLiteStore, identity WorkstreamStorageHostOperationIdentity, effectID string, apply func(*sql.Tx) (any, any, error)) (json.RawMessage, error) {
	if err := identity.validate(); err != nil {
		return nil, err
	}
	if s == nil || s.db == nil {
		return nil, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, _, priorEffectID, found, err := lookupWorkstreamStorageHostReceiptProof(ctx, tx, identity); err != nil {
		return nil, err
	} else if found {
		if priorEffectID != effectID {
			return nil, errors.New("workstream storage-host receipt effect binding mismatch")
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return prior, nil
	}
	result, effect, err := apply(tx)
	if err != nil {
		return nil, err
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return nil, err
	}
	effectJSON, err := json.Marshal(effect)
	if err != nil {
		return nil, err
	}
	if err := insertWorkstreamStorageHostReceipt(ctx, tx, identity, effectID, string(resultJSON), string(effectJSON)); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return resultJSON, nil
}

// SaveWorkstreamRowForStorageHostOperation persists only the canonical durable
// row. Native VaultRoot directory/file setup remains a CORE-side operation.
func (s *SQLiteStore) SaveWorkstreamRowForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.Workstream) (domainworkstream.Workstream, error) {
	if identity.Operation != WorkstreamSaveWorkstreamStorageHostOperation {
		return domainworkstream.Workstream{}, errors.New("workstream operation does not match workstream save")
	}
	if err := domainworkstream.ValidateWorkstream(item); err != nil {
		return domainworkstream.Workstream{}, err
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "workstream", "workstream_id", item.WorkstreamID, "", "", item.CreatedAt.Format(timeFormatRFC3339Nano))
}

func (s *SQLiteStore) SaveArtifactForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.Artifact) (domainworkstream.Artifact, error) {
	if identity.Operation != WorkstreamSaveArtifactStorageHostOperation {
		return domainworkstream.Artifact{}, errors.New("workstream operation does not match artifact save")
	}
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return domainworkstream.Artifact{}, err
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "artifact", "artifact_id", item.ArtifactID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano))
}

func (s *SQLiteStore) SaveArtifactAnnotationForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.ArtifactAnnotation) (domainworkstream.ArtifactAnnotation, error) {
	if identity.Operation != WorkstreamSaveArtifactAnnotationStorageHostOperation {
		return domainworkstream.ArtifactAnnotation{}, errors.New("workstream operation does not match artifact annotation save")
	}
	if err := domainworkstream.ValidateArtifactAnnotation(item); err != nil {
		return domainworkstream.ArtifactAnnotation{}, err
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "artifact_annotation", "annotation_id", item.AnnotationID, "artifact_id", item.ArtifactID, item.CreatedAt.Format(timeFormatRFC3339Nano))
}

func (s *SQLiteStore) SaveSteeringItemForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.SteeringItem) (domainworkstream.SteeringItem, error) {
	if identity.Operation != WorkstreamSaveSteeringItemStorageHostOperation {
		return domainworkstream.SteeringItem{}, errors.New("workstream operation does not match steering item save")
	}
	if err := domainworkstream.ValidateSteeringItem(item); err != nil {
		return domainworkstream.SteeringItem{}, err
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "steering_queue", "steering_id", item.SteeringID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano))
}

func (s *SQLiteStore) SaveHeartbeatScheduleForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.HeartbeatSchedule) (domainworkstream.HeartbeatSchedule, error) {
	if identity.Operation != WorkstreamSaveHeartbeatScheduleStorageHostOperation {
		return domainworkstream.HeartbeatSchedule{}, errors.New("workstream operation does not match heartbeat schedule save")
	}
	if err := domainworkstream.ValidateHeartbeatSchedule(item); err != nil {
		return domainworkstream.HeartbeatSchedule{}, err
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "heartbeat_schedule", "schedule_id", string(item.ScheduleID), "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano))
}

func (s *SQLiteStore) SaveVaultUpdateLogForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.VaultUpdateLog) (domainworkstream.VaultUpdateLog, error) {
	if identity.Operation != WorkstreamSaveVaultUpdateLogStorageHostOperation {
		return domainworkstream.VaultUpdateLog{}, errors.New("workstream operation does not match vault update log save")
	}
	if err := domainworkstream.ValidateVaultUpdateLog(item); err != nil {
		return domainworkstream.VaultUpdateLog{}, err
	}
	return saveWorkstreamRowWithStorageHostReceipt(ctx, s, identity, item, "vault_update_log", "update_id", item.UpdateID, "workstream_id", item.WorkstreamID, item.CreatedAt.Format(timeFormatRFC3339Nano))
}

func (s *SQLiteStore) SaveQueueFreezeForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.QueueFreeze) (domainworkstream.QueueFreeze, error) {
	if identity.Operation != WorkstreamSaveQueueFreezeStorageHostOperation {
		return domainworkstream.QueueFreeze{}, errors.New("workstream operation does not match queue freeze save")
	}
	if item.FreezeRevision < 1 {
		item.FreezeRevision = 1
	}
	if item.Status == "" {
		item.Status = domainworkstream.QueueFreezeActive
	}
	if item.UpdatedAt.IsZero() {
		item.UpdatedAt = item.CreatedAt
	}
	if err := domainworkstream.ValidateQueueFreeze(item); err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	return saveWorkstreamQueueFreezeWithStorageHostReceipt(ctx, s, identity, item)
}

func saveWorkstreamQueueFreezeWithStorageHostReceipt(ctx context.Context, s *SQLiteStore, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.QueueFreeze) (domainworkstream.QueueFreeze, error) {
	if err := identity.validate(); err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	if s == nil || s.db == nil {
		return domainworkstream.QueueFreeze{}, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	resultJSON, err := json.Marshal(item)
	if err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, found, err := lookupWorkstreamStorageHostReceiptJSON(ctx, tx, identity, "queue_freeze", "freeze_id", item.FreezeID); err != nil {
		return domainworkstream.QueueFreeze{}, err
	} else if found {
		if !bytes.Equal(prior, resultJSON) {
			return domainworkstream.QueueFreeze{}, errors.New("workstream storage-host operation id is bound to a different queue freeze")
		}
		var replay domainworkstream.QueueFreeze
		if err := json.Unmarshal(prior, &replay); err != nil {
			return domainworkstream.QueueFreeze{}, errors.New("workstream storage-host receipt result is malformed")
		}
		if err := tx.Commit(); err != nil {
			return domainworkstream.QueueFreeze{}, err
		}
		return replay, nil
	}
	_, err = tx.ExecContext(ctx, `INSERT OR REPLACE INTO queue_freeze (freeze_id, blocked_unit_id, blocked_revision, created_at, payload) VALUES (?, ?, ?, ?, ?)`, item.FreezeID, item.BlockedUnitID, item.BlockedRevision, item.CreatedAt.Format(timeFormatRFC3339Nano), string(resultJSON))
	if err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	var effectJSON string
	if err := tx.QueryRowContext(ctx, `SELECT payload FROM queue_freeze WHERE freeze_id = ?`, item.FreezeID).Scan(&effectJSON); err != nil || effectJSON != string(resultJSON) {
		return domainworkstream.QueueFreeze{}, errors.New("workstream storage-host queue freeze differs from requested result")
	}
	if err := insertWorkstreamStorageHostReceipt(ctx, tx, identity, item.FreezeID, string(resultJSON), effectJSON); err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	if err := tx.Commit(); err != nil {
		return domainworkstream.QueueFreeze{}, err
	}
	return item, nil
}

func (s *SQLiteStore) SaveStageRunReceiptForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.StageRunReceipt) (domainworkstream.StageRunReceipt, error) {
	if identity.Operation != WorkstreamSaveStageRunReceiptStorageHostOperation {
		return domainworkstream.StageRunReceipt{}, errors.New("workstream operation does not match stage run receipt save")
	}
	if err := domainworkstream.ValidateStageRunReceipt(item); err != nil {
		return domainworkstream.StageRunReceipt{}, err
	}
	return saveWorkstreamLifecycleWithStorageHostReceipt(ctx, s, identity, "stage_run_receipt", string(item.ReceiptID), item.IdempotencyKey, item.UnitID, item.ImplementationRevision, item.CreatedAt, item)
}

func (s *SQLiteStore) SaveClosureReceiptForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.ClosureReceipt) (domainworkstream.ClosureReceipt, error) {
	if identity.Operation != WorkstreamSaveClosureReceiptStorageHostOperation {
		return domainworkstream.ClosureReceipt{}, errors.New("workstream operation does not match closure receipt save")
	}
	if err := domainworkstream.ValidateClosureReceipt(item); err != nil {
		return domainworkstream.ClosureReceipt{}, err
	}
	return saveWorkstreamLifecycleWithStorageHostReceipt(ctx, s, identity, "closure_receipt", string(item.ReceiptID), item.IdempotencyKey, item.UnitID, item.ImplementationRevision, item.CreatedAt, item)
}

func (s *SQLiteStore) AcquireImplementationLeaseIfUnfrozenForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.ImplementationLease) (WorkstreamLeaseAcquireResult, error) {
	if identity.Operation != WorkstreamAcquireLeaseStorageHostOperation {
		return WorkstreamLeaseAcquireResult{}, errors.New("workstream operation does not match implementation lease acquire")
	}
	if err := domainworkstream.ValidateImplementationLease(item); err != nil {
		return WorkstreamLeaseAcquireResult{}, err
	}
	raw, err := commitWorkstreamCustomStorageHostMutation(ctx, s, identity, item.LeaseName, func(tx *sql.Tx) (any, any, error) {
		frozen, err := activeQueueFreezeTx(ctx, tx)
		if err != nil {
			return nil, nil, err
		}
		result := WorkstreamLeaseAcquireResult{}
		if frozen {
			result.Reason = domainworkstream.ErrQueueFrozen.Error()
		} else {
			var holder string
			err = tx.QueryRowContext(ctx, `SELECT holder_unit_id FROM implementation_lease WHERE lease_name = ?`, item.LeaseName).Scan(&holder)
			if err == nil && holder != "" && holder != item.HolderUnitID {
				result.Reason = domainworkstream.ErrImplementationLeaseHeld.Error()
			} else if err != nil && err != sql.ErrNoRows {
				return nil, nil, err
			} else if err == sql.ErrNoRows {
				_, err = tx.ExecContext(ctx, `INSERT INTO implementation_lease (lease_name, holder_unit_id, holder_workstream_id, stage, revision, acquired_at, heartbeat_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, item.LeaseName, item.HolderUnitID, item.HolderWorkstreamID, item.Stage, item.Revision, item.AcquiredAt.Format(timeFormatRFC3339Nano), item.HeartbeatAt.Format(timeFormatRFC3339Nano))
				if err != nil {
					return nil, nil, err
				}
				result.Acquired = true
			} else {
				_, err = tx.ExecContext(ctx, `UPDATE implementation_lease SET holder_unit_id=?, holder_workstream_id=?, stage=?, revision=?, acquired_at=?, heartbeat_at=? WHERE lease_name=?`, item.HolderUnitID, item.HolderWorkstreamID, item.Stage, item.Revision, item.AcquiredAt.Format(timeFormatRFC3339Nano), item.HeartbeatAt.Format(timeFormatRFC3339Nano), item.LeaseName)
				if err != nil {
					return nil, nil, err
				}
				result.Acquired = true
			}
		}
		lease, found, err := implementationLeaseTx(ctx, tx, item.LeaseName)
		if err != nil {
			return nil, nil, err
		}
		return result, WorkstreamLeaseMutationEffect{Lease: lease, LeaseFound: found, RequestedLease: item, QueueFrozen: frozen}, nil
	})
	if err != nil {
		return WorkstreamLeaseAcquireResult{}, err
	}
	var result WorkstreamLeaseAcquireResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return WorkstreamLeaseAcquireResult{}, errors.New("workstream lease acquire receipt result is malformed")
	}
	return result, nil
}

func (s *SQLiteStore) ResolveQueueFreezeAndAcquireLeaseForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, freezeID string, resolution domainworkstream.QueueFreezeResolution, replacement domainworkstream.ImplementationLease) (WorkstreamFreezeLeaseResult, error) {
	if identity.Operation != WorkstreamResolveFreezeStorageHostOperation {
		return WorkstreamFreezeLeaseResult{}, errors.New("workstream operation does not match freeze resolution")
	}
	if freezeID == "" {
		return WorkstreamFreezeLeaseResult{}, errors.New("freeze_id is required")
	}
	if err := domainworkstream.ValidateQueueFreezeResolution(resolution, replacement); err != nil {
		return WorkstreamFreezeLeaseResult{}, err
	}
	raw, err := commitWorkstreamCustomStorageHostMutation(ctx, s, identity, freezeID, func(tx *sql.Tx) (any, any, error) {
		var payload string
		err := tx.QueryRowContext(ctx, `SELECT payload FROM queue_freeze WHERE freeze_id = ?`, freezeID).Scan(&payload)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, domainworkstream.ErrQueueFreezeNotFound
		}
		if err != nil {
			return nil, nil, err
		}
		var freeze domainworkstream.QueueFreeze
		if err := json.Unmarshal([]byte(payload), &freeze); err != nil {
			return nil, nil, err
		}
		if freeze.Status == domainworkstream.QueueFreezeResolved {
			if !freeze.MatchesResolved(resolution) {
				return nil, nil, domainworkstream.ErrQueueFreezeResolutionConflict
			}
			lease, _, err := implementationLeaseTx(ctx, tx, freeze.ReplacementLease.LeaseName)
			if err != nil {
				return nil, nil, err
			}
			result := WorkstreamFreezeLeaseResult{Freeze: freeze, Lease: freeze.ReplacementLease, Acquired: freeze.ResolutionAcquired}
			return result, WorkstreamLeaseMutationEffect{Freeze: freeze, FreezeFound: true, Lease: lease, LeaseFound: lease.LeaseName != "", RequestedLease: replacement}, nil
		}
		if freeze.Status != domainworkstream.QueueFreezeActive && freeze.Status != "" {
			return nil, nil, domainworkstream.ErrQueueFreezeResolutionConflict
		}
		if freeze.FreezeRevision != resolution.ExpectedFreezeRevision {
			return nil, nil, fmt.Errorf("%w: expected %d current %d", domainworkstream.ErrQueueFreezeRevisionConflict, resolution.ExpectedFreezeRevision, freeze.FreezeRevision)
		}
		var holder string
		leaseErr := tx.QueryRowContext(ctx, `SELECT holder_unit_id FROM implementation_lease WHERE lease_name = ?`, replacement.LeaseName).Scan(&holder)
		if leaseErr != nil && !errors.Is(leaseErr, sql.ErrNoRows) {
			return nil, nil, leaseErr
		}
		if leaseErr == nil && holder != "" && holder != replacement.HolderUnitID {
			lease, found, err := implementationLeaseTx(ctx, tx, replacement.LeaseName)
			if err != nil {
				return nil, nil, err
			}
			result := WorkstreamFreezeLeaseResult{Freeze: freeze, Acquired: false}
			return result, WorkstreamLeaseMutationEffect{Freeze: freeze, FreezeFound: true, Lease: lease, LeaseFound: found, RequestedLease: replacement}, nil
		}
		now := time.Now().UTC()
		freeze.Status = domainworkstream.QueueFreezeResolved
		freeze.ActionID = resolution.ActionID
		freeze.ReplacementUnitID = resolution.ReplacementUnitID
		freeze.ReplacementLease = replacement
		freeze.ResolutionAcquired = true
		freeze.SupersedesUnitID = resolution.SupersedesUnitID
		freeze.BlockerResolutionRefs = append([]domainbacklog.EvidenceRef(nil), resolution.BlockerResolutionRefs...)
		freeze.ResolutionPayloadHash = resolution.ResolutionPayloadHash
		freeze.UpdatedAt = now
		freeze.ResolvedAt = now
		resolvedPayload, err := json.Marshal(freeze)
		if err != nil {
			return nil, nil, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE queue_freeze SET blocked_unit_id=?, blocked_revision=?, created_at=?, payload=? WHERE freeze_id=?`, freeze.BlockedUnitID, freeze.BlockedRevision, freeze.CreatedAt.Format(timeFormatRFC3339Nano), string(resolvedPayload), freeze.FreezeID); err != nil {
			return nil, nil, err
		}
		if errors.Is(leaseErr, sql.ErrNoRows) {
			if _, err := tx.ExecContext(ctx, `INSERT INTO implementation_lease (lease_name, holder_unit_id, holder_workstream_id, stage, revision, acquired_at, heartbeat_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, replacement.LeaseName, replacement.HolderUnitID, replacement.HolderWorkstreamID, replacement.Stage, replacement.Revision, replacement.AcquiredAt.Format(timeFormatRFC3339Nano), replacement.HeartbeatAt.Format(timeFormatRFC3339Nano)); err != nil {
				return nil, nil, err
			}
		} else if _, err := tx.ExecContext(ctx, `UPDATE implementation_lease SET holder_unit_id=?, holder_workstream_id=?, stage=?, revision=?, acquired_at=?, heartbeat_at=? WHERE lease_name=?`, replacement.HolderUnitID, replacement.HolderWorkstreamID, replacement.Stage, replacement.Revision, replacement.AcquiredAt.Format(timeFormatRFC3339Nano), replacement.HeartbeatAt.Format(timeFormatRFC3339Nano), replacement.LeaseName); err != nil {
			return nil, nil, err
		}
		result := WorkstreamFreezeLeaseResult{Freeze: freeze, Lease: replacement, Acquired: true}
		return result, WorkstreamLeaseMutationEffect{Freeze: freeze, FreezeFound: true, Lease: replacement, LeaseFound: true, RequestedLease: replacement}, nil
	})
	if err != nil {
		return WorkstreamFreezeLeaseResult{}, err
	}
	var result WorkstreamFreezeLeaseResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return WorkstreamFreezeLeaseResult{}, errors.New("workstream freeze resolution receipt result is malformed")
	}
	return result, nil
}

func (s *SQLiteStore) ReleaseImplementationLeaseForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, leaseName, holderUnitID string) error {
	if identity.Operation != WorkstreamReleaseLeaseStorageHostOperation {
		return errors.New("workstream operation does not match implementation lease release")
	}
	_, err := commitWorkstreamCustomStorageHostMutation(ctx, s, identity, leaseName, func(tx *sql.Tx) (any, any, error) {
		if holderUnitID == "" {
			_, err := tx.ExecContext(ctx, `DELETE FROM implementation_lease WHERE lease_name = ?`, leaseName)
			if err != nil {
				return nil, nil, err
			}
		} else if _, err := tx.ExecContext(ctx, `DELETE FROM implementation_lease WHERE lease_name = ? AND holder_unit_id = ?`, leaseName, holderUnitID); err != nil {
			return nil, nil, err
		}
		lease, found, err := implementationLeaseTx(ctx, tx, leaseName)
		if err != nil {
			return nil, nil, err
		}
		return struct{}{}, WorkstreamLeaseMutationEffect{Lease: lease, LeaseFound: found}, nil
	})
	return err
}

func (s *SQLiteStore) HeartbeatImplementationLeaseForStorageHostOperation(ctx context.Context, identity WorkstreamStorageHostOperationIdentity, item domainworkstream.ImplementationLease) error {
	if identity.Operation != WorkstreamHeartbeatLeaseStorageHostOperation {
		return errors.New("workstream operation does not match implementation lease heartbeat")
	}
	if item.LeaseName == "" || item.HolderUnitID == "" {
		return errors.New("lease_name and holder_unit_id are required")
	}
	_, err := commitWorkstreamCustomStorageHostMutation(ctx, s, identity, item.LeaseName, func(tx *sql.Tx) (any, any, error) {
		if item.HeartbeatAt.IsZero() {
			item.HeartbeatAt = time.Now().UTC()
		}
		result, err := tx.ExecContext(ctx, `UPDATE implementation_lease SET stage=?, revision=?, heartbeat_at=? WHERE lease_name=? AND holder_unit_id=?`, item.Stage, item.Revision, item.HeartbeatAt.Format(timeFormatRFC3339Nano), item.LeaseName, item.HolderUnitID)
		if err != nil {
			return nil, nil, err
		}
		count, err := result.RowsAffected()
		if err != nil {
			return nil, nil, err
		}
		if count == 0 {
			return nil, nil, fmt.Errorf("implementation lease is not held by %s", item.HolderUnitID)
		}
		lease, found, err := implementationLeaseTx(ctx, tx, item.LeaseName)
		if err != nil {
			return nil, nil, err
		}
		return struct{}{}, WorkstreamLeaseMutationEffect{Lease: lease, LeaseFound: found}, nil
	})
	return err
}

func implementationLeaseTx(ctx context.Context, tx *sql.Tx, leaseName string) (domainworkstream.ImplementationLease, bool, error) {
	var item domainworkstream.ImplementationLease
	var acquired, heartbeat string
	err := tx.QueryRowContext(ctx, `SELECT lease_name, holder_unit_id, holder_workstream_id, stage, revision, acquired_at, heartbeat_at FROM implementation_lease WHERE lease_name = ?`, leaseName).Scan(&item.LeaseName, &item.HolderUnitID, &item.HolderWorkstreamID, &item.Stage, &item.Revision, &acquired, &heartbeat)
	if errors.Is(err, sql.ErrNoRows) {
		return domainworkstream.ImplementationLease{}, false, nil
	}
	if err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	item.AcquiredAt, err = time.Parse(timeFormatRFC3339Nano, acquired)
	if err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	item.HeartbeatAt, err = time.Parse(timeFormatRFC3339Nano, heartbeat)
	if err != nil {
		return domainworkstream.ImplementationLease{}, false, err
	}
	return item, item.HolderUnitID != "", nil
}

func saveWorkstreamLifecycleWithStorageHostReceipt[T any](ctx context.Context, s *SQLiteStore, identity WorkstreamStorageHostOperationIdentity, table string, receiptID, idempotencyKey, unitID string, revision int, createdAt time.Time, item T) (T, error) {
	var zero T
	if err := identity.validate(); err != nil {
		return zero, err
	}
	if s == nil || s.db == nil {
		return zero, errors.New("workstream sqlite store is closed")
	}
	if err := s.ensureWorkstreamStorageHostReceiptSchema(ctx); err != nil {
		return zero, err
	}
	resultJSON, err := json.Marshal(item)
	if err != nil {
		return zero, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return zero, err
	}
	defer func() { _ = tx.Rollback() }()
	if prior, found, err := lookupWorkstreamStorageHostReceiptJSON(ctx, tx, identity, table, "receipt_id", string(receiptID)); err != nil {
		return zero, err
	} else if found {
		if !bytes.Equal(prior, resultJSON) {
			return zero, errors.New("workstream storage-host operation id is bound to a different lifecycle receipt")
		}
		var replay T
		if err := json.Unmarshal(prior, &replay); err != nil {
			return zero, errors.New("workstream storage-host receipt result is malformed")
		}
		if err := tx.Commit(); err != nil {
			return zero, err
		}
		return replay, nil
	}
	_, err = tx.ExecContext(ctx, fmt.Sprintf(`INSERT OR REPLACE INTO %s (receipt_id, idempotency_key, unit_id, implementation_revision, created_at, payload) VALUES (?, ?, ?, ?, ?, ?)`, table), receiptID, idempotencyKey, unitID, revision, createdAt.Format(timeFormatRFC3339Nano), string(resultJSON))
	if err != nil {
		return zero, err
	}
	var effectJSON string
	if err := tx.QueryRowContext(ctx, fmt.Sprintf(`SELECT payload FROM %s WHERE receipt_id = ?`, table), receiptID).Scan(&effectJSON); err != nil || effectJSON != string(resultJSON) {
		return zero, errors.New("workstream storage-host lifecycle row differs from requested result")
	}
	if err := insertWorkstreamStorageHostReceipt(ctx, tx, identity, string(receiptID), string(resultJSON), effectJSON); err != nil {
		return zero, err
	}
	if err := tx.Commit(); err != nil {
		return zero, err
	}
	return item, nil
}

type workstreamStorageHostReceiptQuery interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
