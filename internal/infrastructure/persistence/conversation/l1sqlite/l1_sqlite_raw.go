package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

type preparedCommonRawAsset struct {
	input     domainmemory.CommonRawAsset
	hash      string
	objectRef string
	size      int64
}

type preparedCommonRawRecord struct {
	input     domainmemory.CommonRawRecord
	hash      string
	storage   string
	objectRef string
	assets    []domainmemory.CommonRawAssetRef
	size      int64
}

// SetCommonRawSourceRoot installs the CORE-trusted object root. It validates
// and reconciles an existing tree without creating a missing root; intake
// remains the only operation that creates the configured object hierarchy.
func (s *L1SQLiteStore) SetCommonRawSourceRoot(root string) error {
	if s == nil || s.db == nil {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "conversation_l1 store is unavailable")
	}
	root = strings.TrimSpace(root)
	s.rawMu.Lock()
	defer s.rawMu.Unlock()
	if err := commonRawRootPathError(root); err != nil {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}
	cleanRoot := filepath.Clean(root)
	info, err := os.Lstat(cleanRoot)
	if errors.Is(err, os.ErrNotExist) {
		expected, expectedErr := s.commonRawExpectedObjects()
		if expectedErr != nil {
			return expectedErr
		}
		if len(expected) != 0 {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "referenced raw object is missing because the configured root does not exist")
		}
		s.rawSourceRoot = cleanRoot
		return nil
	}
	if err != nil {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root cannot be inspected")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is not a regular directory")
	}
	if err := s.reconcileCommonRawRoot(cleanRoot); err != nil {
		return err
	}
	s.rawSourceRoot = cleanRoot
	return nil
}

// IntakeCommonRaw is the trusted CORE-owned immutable intake boundary. It
// validates the complete request before writing any object, finalizes objects
// before one SQLite transaction, and deliberately does not write projection,
// Conversation, UserMemory, Knowledge, or Recall rows.
func (s *L1SQLiteStore) IntakeCommonRaw(ctx context.Context, requestID, ownerID, actorID string, input domainmemory.CommonRawIntakeRequest) (domainmemory.CommonRawIntakeReceipt, error) {
	if s == nil || s.db == nil {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "conversation_l1 store is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	if ctx == nil || requestID == "" || ownerID == "" || actorID == "" {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "request, owner and actor identity are required")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{name: "request id", value: requestID},
		{name: "owner id", value: ownerID},
		{name: "actor id", value: actorID},
	} {
		if len([]byte(field.value)) > domainmemory.CommonRawMaxMetadataSize {
			return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, field.name+" exceeds the bounded metadata limit")
		}
	}
	if err := validateCommonRawOwnerScope(ctx, requestID, ownerID, actorID); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	}

	s.rawMu.Lock()
	defer s.rawMu.Unlock()

	prepared, err := prepareCommonRawIntake(ownerID, input)
	if err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	}
	if prepared.requiresObject && commonRawRootPathError(s.rawSourceRoot) != nil {
		return domainmemory.CommonRawIntakeReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}

	if replay, found, err := s.findCommonRawManifestReplay(ctx, ownerID, prepared); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	} else if found {
		replay.IdempotentReplay = true
		return replay, nil
	}
	manifestID := domainmemory.DeterministicCommonRawManifestID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, prepared.manifestSHA256)
	if err := s.rejectCommonRawSourceConflicts(ctx, ownerID, prepared.manifest, prepared.records, manifestID); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, err
	}

	newObjects := make([]string, 0, len(prepared.assets)+len(prepared.records))
	cleanup := func() {
		for _, path := range newObjects {
			rel, err := filepath.Rel(s.rawSourceRoot, path)
			if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			ref := filepath.ToSlash(rel)
			var directRefs, assetRefs int
			if err := s.db.QueryRow(`SELECT count(*) FROM l1_raw_record WHERE object_ref = ?`, ref).Scan(&directRefs); err != nil {
				continue
			}
			if err := s.db.QueryRow(`SELECT count(*) FROM l1_raw_record WHERE asset_refs_json LIKE ?`, "%"+ref+"%").Scan(&assetRefs); err != nil {
				continue
			}
			if directRefs == 0 && assetRefs == 0 {
				_ = os.Remove(path)
			}
		}
	}
	if prepared.requiresObject {
		if err := ensureCommonRawRoot(s.rawSourceRoot); err != nil {
			return domainmemory.CommonRawIntakeReceipt{}, err
		}
		for i := range prepared.assets {
			ref, path, created, err := writeCommonRawObject(s.rawSourceRoot, prepared.assets[i].input.Content, prepared.assets[i].hash)
			if err != nil {
				cleanup()
				return domainmemory.CommonRawIntakeReceipt{}, err
			}
			prepared.assets[i].objectRef = ref
			prepared.assets[i].size = int64(len(prepared.assets[i].input.Content))
			if created {
				newObjects = append(newObjects, path)
			}
		}
		for i := range prepared.records {
			if prepared.records[i].storage != domainmemory.CommonRawStorageObject {
				continue
			}
			ref, path, created, err := writeCommonRawObject(s.rawSourceRoot, prepared.records[i].input.Content, prepared.records[i].hash)
			if err != nil {
				cleanup()
				return domainmemory.CommonRawIntakeReceipt{}, err
			}
			prepared.records[i].objectRef = ref
			if created {
				newObjects = append(newObjects, path)
			}
		}
		assetObjects := make(map[string]string, len(prepared.assets))
		for _, asset := range prepared.assets {
			assetObjects[asset.input.SourceAssetID] = asset.objectRef
		}
		for i := range prepared.records {
			for j := range prepared.records[i].assets {
				prepared.records[i].assets[j].ObjectRef = assetObjects[prepared.records[i].assets[j].SourceAssetID]
			}
		}
	}

	now := time.Now().UTC()
	receipt := commonRawReceiptFromPrepared(requestID, ownerID, prepared.manifest.Scope, manifestID, prepared, now)
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		cleanup()
		return domainmemory.CommonRawIntakeReceipt{}, fmt.Errorf("%w: marshal common raw receipt", domainmemory.ErrCommonRawUnavailable)
	}
	checkpointJSON, err := json.Marshal(struct {
		ManifestID  string                      `json:"manifest_id"`
		SourceCount int                         `json:"source_count"`
		AssetCount  int                         `json:"asset_count"`
		Status      domainmemory.CommonRawState `json:"status"`
	}{manifestID, len(prepared.records), len(prepared.assets), domainmemory.CommonRawStateCompleted})
	if err != nil {
		cleanup()
		return domainmemory.CommonRawIntakeReceipt{}, fmt.Errorf("%w: marshal common raw checkpoint", domainmemory.ErrCommonRawUnavailable)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		cleanup()
		return domainmemory.CommonRawIntakeReceipt{}, fmt.Errorf("%w: begin common raw intake transaction", domainmemory.ErrCommonRawUnavailable)
	}
	rollback := func(cause error) (domainmemory.CommonRawIntakeReceipt, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			cause = fmt.Errorf("%w: rollback common raw intake: %v", domainmemory.ErrCommonRawUnavailable, rollbackErr)
		}
		cleanup()
		return domainmemory.CommonRawIntakeReceipt{}, cause
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_raw_source_manifest (
	manifest_id, contract_version, source_type, source_identity, manifest_sha256,
	source_count, asset_count, schema_version, converter_version, owner_id, scope,
	sensitivity, rights, license, provenance, allow_empty, request_id, actor_id,
	intake_status, checkpoint_json, receipt_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		manifestID, prepared.manifest.ContractVersion, prepared.manifest.SourceType, prepared.manifest.SourceIdentity,
		prepared.manifestSHA256, len(prepared.records), len(prepared.assets), prepared.manifest.SchemaVersion,
		prepared.manifest.ConverterVersion, ownerID, prepared.manifest.Scope, prepared.manifest.Sensitivity,
		prepared.manifest.Rights, prepared.manifest.License, prepared.manifest.Provenance, boolInt(prepared.manifest.AllowEmpty),
		requestID, actorID, domainmemory.CommonRawStateCompleted, string(checkpointJSON), string(receiptJSON), now, now); err != nil {
		return rollback(fmt.Errorf("%w: insert common raw manifest", domainmemory.ErrCommonRawUnavailable))
	}
	for _, record := range prepared.records {
		assetJSON, err := json.Marshal(record.assets)
		if err != nil {
			return rollback(fmt.Errorf("%w: marshal common raw asset refs", domainmemory.ErrCommonRawUnavailable))
		}
		inlinePayload := interface{}(nil)
		objectRef := record.objectRef
		if record.storage == domainmemory.CommonRawStorageInline {
			inlinePayload = record.input.Content
			objectRef = ""
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_raw_record (
	raw_record_id, manifest_id, contract_version, source_type, source_identity, source_record_id,
	parent_id, thread_id, owner_id, scope, sensitivity, role, content_type, occurred_at, ingested_at,
	storage_kind, inline_payload, object_ref, content_sha256, content_size, asset_refs_json,
	provenance, rights, license, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			domainmemory.DeterministicCommonRawRecordID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash),
			manifestID, domainmemory.CommonRawContractVersion, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID,
			record.input.ParentID, record.input.ThreadID, ownerID, prepared.manifest.Scope, valueOrDefault(record.input.Sensitivity, prepared.manifest.Sensitivity),
			record.input.Role, record.input.ContentType, record.input.OccurredAt.UTC(), now, record.storage, inlinePayload,
			objectRef, record.hash, record.size, string(assetJSON), valueOrDefault(record.input.Provenance, prepared.manifest.Provenance),
			valueOrDefault(record.input.Rights, prepared.manifest.Rights), valueOrDefault(record.input.License, prepared.manifest.License), now); err != nil {
			return rollback(fmt.Errorf("%w: insert common raw record", domainmemory.ErrCommonRawUnavailable))
		}
		stateEventID := domainmemory.DeterministicCommonRawStateEventID(domainmemory.DeterministicCommonRawRecordID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash), "ingested", record.hash)
		if _, err := tx.ExecContext(ctx, `
INSERT INTO l1_raw_state_event (state_event_id, raw_record_id, manifest_id, event_type, event_hash, owner_id, scope, request_id, actor_id, reason_code, payload_json, created_at)
VALUES (?, ?, ?, 'ingested', ?, ?, ?, ?, ?, ?, ?, ?)`, stateEventID,
			domainmemory.DeterministicCommonRawRecordID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash), manifestID, record.hash, ownerID, prepared.manifest.Scope, requestID, actorID, "ingested", "{}", now); err != nil {
			return rollback(fmt.Errorf("%w: insert common raw ingested state", domainmemory.ErrCommonRawUnavailable))
		}
	}
	if err := tx.Commit(); err != nil {
		cleanup()
		return domainmemory.CommonRawIntakeReceipt{}, fmt.Errorf("%w: commit common raw intake transaction", domainmemory.ErrCommonRawUnavailable)
	}
	return receipt, nil
}

func validateCommonRawOwnerScope(ctx context.Context, requestID, ownerID, actorID string) error {
	scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
	if !ok || scope.Validate() != nil || scope.RequestID != requestID || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != actorID || scope.AuthenticatedUserID != ownerID || !scope.Allows(domaintool.DataScopeUser) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorForbidden, "trusted private user scope is required")
	}
	return nil
}

type preparedCommonRawIntake struct {
	manifest       domainmemory.CommonRawManifest
	manifestSHA256 string
	records        []preparedCommonRawRecord
	assets         []preparedCommonRawAsset
	requiresObject bool
}

func prepareCommonRawIntake(ownerID string, input domainmemory.CommonRawIntakeRequest) (preparedCommonRawIntake, error) {
	m := input.Manifest
	trustedScope := "user:" + ownerID
	if strings.TrimSpace(m.Scope) != "" {
		return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorForbidden, "caller must not supply owner scope")
	}
	m.Scope = trustedScope
	if err := m.Validate(); err != nil {
		return preparedCommonRawIntake{}, err
	}
	if m.SourceCount != len(input.Records) || m.AssetCount != len(input.Assets) {
		return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "manifest count does not match records or assets")
	}
	if len(input.Records) == 0 && !m.AllowEmpty {
		return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "empty intake requires explicit allow_empty")
	}
	if len(input.Records) > domainmemory.CommonRawMaxRecords || len(input.Assets) > domainmemory.CommonRawMaxAssets {
		return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "intake exceeds the bounded limit")
	}

	assetsByID := make(map[string]preparedCommonRawAsset, len(input.Assets))
	assets := make([]preparedCommonRawAsset, 0, len(input.Assets))
	batchPayloadSize := 0
	for _, asset := range input.Assets {
		asset.SourceAssetID = strings.TrimSpace(asset.SourceAssetID)
		asset.MediaType = strings.TrimSpace(asset.MediaType)
		asset.Provenance = valueOrDefault(asset.Provenance, m.Provenance)
		asset.Rights = valueOrDefault(asset.Rights, m.Rights)
		asset.License = valueOrDefault(asset.License, m.License)
		if err := asset.Validate(); err != nil {
			return preparedCommonRawIntake{}, err
		}
		if len(asset.Content) > domainmemory.CommonRawMaxBatchPayloadSize-batchPayloadSize {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "intake payload exceeds the bounded batch limit")
		}
		batchPayloadSize += len(asset.Content)
		if _, exists := assetsByID[asset.SourceAssetID]; exists {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "duplicate source asset id")
		}
		actualHash := domainmemory.SHA256Hex(asset.Content)
		if actualHash != asset.ContentSHA256 {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "asset content hash does not match claim")
		}
		prepared := preparedCommonRawAsset{input: asset, hash: actualHash, size: int64(len(asset.Content))}
		assetsByID[asset.SourceAssetID] = prepared
		assets = append(assets, prepared)
	}

	records := make([]preparedCommonRawRecord, 0, len(input.Records))
	seenRecords := make(map[string]struct{}, len(input.Records))
	referencedAssets := make(map[string]struct{}, len(input.Assets))
	for _, record := range input.Records {
		record.SourceRecordID = strings.TrimSpace(record.SourceRecordID)
		record.ParentID = strings.TrimSpace(record.ParentID)
		record.ThreadID = strings.TrimSpace(record.ThreadID)
		record.Sensitivity = valueOrDefault(record.Sensitivity, m.Sensitivity)
		record.Role = strings.TrimSpace(record.Role)
		record.ContentType = strings.TrimSpace(record.ContentType)
		record.Provenance = valueOrDefault(record.Provenance, m.Provenance)
		record.Rights = valueOrDefault(record.Rights, m.Rights)
		record.License = valueOrDefault(record.License, m.License)
		if err := record.Validate(); err != nil {
			return preparedCommonRawIntake{}, err
		}
		if len(record.Content) > domainmemory.CommonRawMaxBatchPayloadSize-batchPayloadSize {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "intake payload exceeds the bounded batch limit")
		}
		batchPayloadSize += len(record.Content)
		if _, exists := seenRecords[record.SourceRecordID]; exists {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "duplicate source record id")
		}
		seenRecords[record.SourceRecordID] = struct{}{}
		actualHash := domainmemory.SHA256Hex(record.Content)
		if actualHash != record.ContentSHA256 {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "record content hash does not match claim")
		}
		refs := make([]domainmemory.CommonRawAssetRef, 0, len(record.AssetRefs))
		seenRefs := make(map[string]struct{}, len(record.AssetRefs))
		for _, assetID := range record.AssetRefs {
			assetID = strings.TrimSpace(assetID)
			if assetID == "" {
				return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "empty asset reference")
			}
			if _, duplicate := seenRefs[assetID]; duplicate {
				return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "duplicate asset reference")
			}
			seenRefs[assetID] = struct{}{}
			referencedAssets[assetID] = struct{}{}
			asset, exists := assetsByID[assetID]
			if !exists {
				return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "asset reference is missing from intake")
			}
			refs = append(refs, domainmemory.CommonRawAssetRef{SourceAssetID: assetID, SHA256: asset.hash, Size: asset.size, MediaType: asset.input.MediaType})
		}
		storage := domainmemory.CommonRawStorageInline
		if len(record.Content) > domainmemory.CommonRawMaxInlinePayloadSize {
			storage = domainmemory.CommonRawStorageObject
		}
		record.AssetRefs = make([]string, 0, len(refs))
		for _, ref := range refs {
			record.AssetRefs = append(record.AssetRefs, ref.SourceAssetID)
		}
		sort.Slice(refs, func(i, j int) bool { return refs[i].SourceAssetID < refs[j].SourceAssetID })
		record.AssetRefs = record.AssetRefs[:0]
		for _, ref := range refs {
			record.AssetRefs = append(record.AssetRefs, ref.SourceAssetID)
		}
		records = append(records, preparedCommonRawRecord{input: record, hash: actualHash, storage: storage, assets: refs, size: int64(len(record.Content))})
	}
	for assetID := range assetsByID {
		if _, ok := referencedAssets[assetID]; !ok {
			return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "asset is not referenced by any record")
		}
	}
	// The canonical input hash is independent of caller ordering. The receipt
	// and transaction use the same deterministic source-record order.
	sortPreparedCommonRawRecords(records)
	sortPreparedCommonRawAssets(assets)
	actualManifestHash, err := domainmemory.CommonRawInputHash(m, preparedInputRecords(records), preparedInputAssets(assets))
	if err != nil {
		return preparedCommonRawIntake{}, fmt.Errorf("%w: compute common raw manifest hash", domainmemory.ErrCommonRawInvalid)
	}
	if actualManifestHash != m.ManifestSHA256 {
		return preparedCommonRawIntake{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "manifest hash does not match canonical intake")
	}
	requiresObject := len(assets) > 0
	for _, record := range records {
		if record.storage == domainmemory.CommonRawStorageObject {
			requiresObject = true
		}
	}
	return preparedCommonRawIntake{manifest: m, manifestSHA256: actualManifestHash, records: records, assets: assets, requiresObject: requiresObject}, nil
}

func preparedInputRecords(records []preparedCommonRawRecord) []domainmemory.CommonRawRecord {
	result := make([]domainmemory.CommonRawRecord, 0, len(records))
	for _, record := range records {
		result = append(result, record.input)
	}
	return result
}

func preparedInputAssets(assets []preparedCommonRawAsset) []domainmemory.CommonRawAsset {
	result := make([]domainmemory.CommonRawAsset, 0, len(assets))
	for _, asset := range assets {
		result = append(result, asset.input)
	}
	return result
}

func sortPreparedCommonRawRecords(records []preparedCommonRawRecord) {
	sort.Slice(records, func(i, j int) bool { return records[i].input.SourceRecordID < records[j].input.SourceRecordID })
}

func sortPreparedCommonRawAssets(assets []preparedCommonRawAsset) {
	sort.Slice(assets, func(i, j int) bool { return assets[i].input.SourceAssetID < assets[j].input.SourceAssetID })
}

func commonRawReceiptFromPrepared(requestID, ownerID, scope, manifestID string, prepared preparedCommonRawIntake, now time.Time) domainmemory.CommonRawIntakeReceipt {
	receipt := domainmemory.CommonRawIntakeReceipt{RequestID: requestID, ManifestID: manifestID, Status: domainmemory.CommonRawStateCompleted, ManifestSHA256: prepared.manifestSHA256, SourceCount: len(prepared.records), AssetCount: len(prepared.assets), Checkpoint: "completed", Records: make([]domainmemory.CommonRawRecordReceipt, 0, len(prepared.records)), CreatedAt: now}
	assetMap := make(map[string]preparedCommonRawAsset, len(prepared.assets))
	for _, asset := range prepared.assets {
		assetMap[asset.input.SourceAssetID] = asset
	}
	for _, record := range prepared.records {
		refs := append([]domainmemory.CommonRawAssetRef(nil), record.assets...)
		for i := range refs {
			refs[i].ObjectRef = assetObjectRef(assetMap[refs[i].SourceAssetID].hash)
		}
		objectRef := record.objectRef
		if record.storage == domainmemory.CommonRawStorageObject {
			objectRef = objectObjectRef(record.hash)
		}
		receipt.Records = append(receipt.Records, domainmemory.CommonRawRecordReceipt{RawRecordID: domainmemory.DeterministicCommonRawRecordID(ownerID, scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash), SourceRecordID: record.input.SourceRecordID, ContentSHA256: record.hash, ContentSize: record.size, StorageKind: record.storage, ObjectRef: objectRef, AssetRefs: refs})
	}
	return receipt
}

func (s *L1SQLiteStore) findCommonRawManifestReplay(ctx context.Context, ownerID string, prepared preparedCommonRawIntake) (domainmemory.CommonRawIntakeReceipt, bool, error) {
	scope := prepared.manifest.Scope
	sourceType := prepared.manifest.SourceType
	sourceIdentity := prepared.manifest.SourceIdentity
	manifestSHA256 := prepared.manifestSHA256
	var storedManifestID, storedOwnerID, storedScope, storedSourceType, storedSourceIdentity, storedHash, storedRequestID, storedActorID, intakeStatus, checkpointJSON, receiptJSON string
	var sourceCount, assetCount int
	var createdAt, updatedAt time.Time
	err := s.db.QueryRowContext(ctx, `SELECT manifest_id, owner_id, scope, source_type, source_identity, manifest_sha256, source_count, asset_count, request_id, actor_id, intake_status, checkpoint_json, receipt_json, created_at, updated_at FROM l1_raw_source_manifest WHERE owner_id = ? AND scope = ? AND source_type = ? AND source_identity = ? AND manifest_sha256 = ?`, ownerID, scope, sourceType, sourceIdentity, manifestSHA256).Scan(&storedManifestID, &storedOwnerID, &storedScope, &storedSourceType, &storedSourceIdentity, &storedHash, &sourceCount, &assetCount, &storedRequestID, &storedActorID, &intakeStatus, &checkpointJSON, &receiptJSON, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmemory.CommonRawIntakeReceipt{}, false, nil
	}
	if err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: read common raw replay receipt", domainmemory.ErrCommonRawUnavailable)
	}
	var receipt domainmemory.CommonRawIntakeReceipt
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: decode common raw replay receipt", domainmemory.ErrCommonRawUnavailable)
	}
	if storedOwnerID != ownerID || storedScope != scope || storedSourceType != sourceType || storedSourceIdentity != sourceIdentity || storedHash != manifestSHA256 || intakeStatus != string(domainmemory.CommonRawStateCompleted) || storedRequestID == "" || storedActorID == "" || receipt.RequestID != storedRequestID || receipt.ManifestID != storedManifestID || receipt.Status != domainmemory.CommonRawStateCompleted || receipt.ManifestSHA256 != manifestSHA256 || receipt.SourceCount != sourceCount || receipt.AssetCount != assetCount || len(receipt.Records) != sourceCount || receipt.Checkpoint != "completed" || receipt.CreatedAt.IsZero() || !receipt.CreatedAt.Equal(createdAt) || updatedAt.Before(createdAt) {
		return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw receipt is inconsistent")
	}
	var checkpoint struct {
		ManifestID  string                      `json:"manifest_id"`
		SourceCount int                         `json:"source_count"`
		AssetCount  int                         `json:"asset_count"`
		Status      domainmemory.CommonRawState `json:"status"`
	}
	if err := json.Unmarshal([]byte(checkpointJSON), &checkpoint); err != nil || checkpoint.ManifestID != storedManifestID || checkpoint.SourceCount != sourceCount || checkpoint.AssetCount != assetCount || checkpoint.Status != domainmemory.CommonRawStateCompleted {
		return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw checkpoint is inconsistent")
	}
	preparedBySourceID := make(map[string]preparedCommonRawRecord, len(prepared.records))
	for _, record := range prepared.records {
		preparedBySourceID[record.input.SourceRecordID] = record
	}
	var storedRecordCount, storedStateCount int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_record WHERE manifest_id = ?`, storedManifestID).Scan(&storedRecordCount); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: verify common raw record count", domainmemory.ErrCommonRawUnavailable)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_raw_state_event WHERE manifest_id = ?`, storedManifestID).Scan(&storedStateCount); err != nil {
		return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: verify common raw state count", domainmemory.ErrCommonRawUnavailable)
	}
	if storedRecordCount != sourceCount || storedStateCount != sourceCount {
		return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw ledger counts are inconsistent")
	}
	seenAssets := make(map[string]struct{})
	for _, expected := range receipt.Records {
		preparedRecord, ok := preparedBySourceID[expected.SourceRecordID]
		if !ok || expected.RawRecordID != domainmemory.DeterministicCommonRawRecordID(ownerID, scope, sourceType, sourceIdentity, expected.SourceRecordID, preparedRecord.hash) || expected.ContentSHA256 != preparedRecord.hash || expected.ContentSize != preparedRecord.size || expected.StorageKind != preparedRecord.storage {
			return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw receipt does not bind to input")
		}
		if len(expected.AssetRefs) != len(preparedRecord.assets) {
			return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw receipt asset binding differs from input")
		}
		for i := range expected.AssetRefs {
			if expected.AssetRefs[i].SourceAssetID != preparedRecord.assets[i].SourceAssetID || expected.AssetRefs[i].SHA256 != preparedRecord.assets[i].SHA256 || expected.AssetRefs[i].Size != preparedRecord.assets[i].Size || expected.AssetRefs[i].MediaType != preparedRecord.assets[i].MediaType {
				return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw receipt asset binding differs from input")
			}
		}
		var storedSourceID, storedHash, storageKind, objectRef, assetJSON string
		var storedSize int64
		var inlinePayload []byte
		if err := s.db.QueryRowContext(ctx, `SELECT source_record_id, content_sha256, storage_kind, object_ref, content_size, inline_payload, asset_refs_json FROM l1_raw_record WHERE raw_record_id = ? AND manifest_id = ?`, expected.RawRecordID, storedManifestID).Scan(&storedSourceID, &storedHash, &storageKind, &objectRef, &storedSize, &inlinePayload, &assetJSON); err != nil {
			return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: verify common raw record", domainmemory.ErrCommonRawUnavailable)
		}
		if storedSourceID != expected.SourceRecordID || !strings.EqualFold(storedHash, expected.ContentSHA256) || storageKind != expected.StorageKind || storedSize != expected.ContentSize || objectRef != expected.ObjectRef {
			return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw record differs from receipt")
		}
		var storedRefs []domainmemory.CommonRawAssetRef
		if err := json.Unmarshal([]byte(assetJSON), &storedRefs); err != nil {
			return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: decode common raw asset refs", domainmemory.ErrCommonRawUnavailable)
		}
		if len(storedRefs) != len(expected.AssetRefs) {
			return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw asset refs differ from receipt")
		}
		for i := range storedRefs {
			if storedRefs[i].SourceAssetID != expected.AssetRefs[i].SourceAssetID || storedRefs[i].SHA256 != expected.AssetRefs[i].SHA256 || storedRefs[i].Size != expected.AssetRefs[i].Size || storedRefs[i].MediaType != expected.AssetRefs[i].MediaType || storedRefs[i].ObjectRef != expected.AssetRefs[i].ObjectRef {
				return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw asset refs differ from receipt")
			}
		}
		if storageKind == domainmemory.CommonRawStorageInline {
			if objectRef != "" || int64(len(inlinePayload)) != expected.ContentSize || domainmemory.SHA256Hex(inlinePayload) != expected.ContentSHA256 {
				return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored inline common raw payload failed verification")
			}
		} else {
			if err := verifyCommonRawStoredObject(s.rawSourceRoot, objectRef, expected.ContentSize, expected.ContentSHA256); err != nil {
				return domainmemory.CommonRawIntakeReceipt{}, false, err
			}
		}
		var stateOwner, stateScope, stateRequest, stateActor, stateReason, statePayload, stateHash string
		var stateCount int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*), owner_id, scope, request_id, actor_id, reason_code, payload_json, event_hash FROM l1_raw_state_event WHERE raw_record_id = ? AND manifest_id = ? AND event_type = 'ingested'`, expected.RawRecordID, storedManifestID).Scan(&stateCount, &stateOwner, &stateScope, &stateRequest, &stateActor, &stateReason, &statePayload, &stateHash); err != nil {
			return domainmemory.CommonRawIntakeReceipt{}, false, fmt.Errorf("%w: verify common raw ingested state", domainmemory.ErrCommonRawUnavailable)
		}
		if stateCount != 1 || stateOwner != ownerID || stateScope != scope || stateRequest != storedRequestID || stateActor != storedActorID || stateReason != "ingested" || statePayload != "{}" || stateHash != expected.ContentSHA256 {
			return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw ingested state differs from receipt")
		}
		for _, ref := range storedRefs {
			if _, duplicate := seenAssets[ref.SourceAssetID]; duplicate {
				continue
			}
			seenAssets[ref.SourceAssetID] = struct{}{}
			if err := verifyCommonRawStoredObject(s.rawSourceRoot, ref.ObjectRef, ref.Size, ref.SHA256); err != nil {
				return domainmemory.CommonRawIntakeReceipt{}, false, err
			}
		}
	}
	if len(seenAssets) != assetCount {
		return domainmemory.CommonRawIntakeReceipt{}, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored common raw assets are incomplete")
	}
	return receipt, true, nil
}

func (s *L1SQLiteStore) rejectCommonRawSourceConflicts(ctx context.Context, ownerID string, manifest domainmemory.CommonRawManifest, records []preparedCommonRawRecord, manifestID string) error {
	for _, record := range records {
		var existingHash, existingManifest string
		err := s.db.QueryRowContext(ctx, `SELECT content_sha256, manifest_id FROM l1_raw_record WHERE owner_id = ? AND scope = ? AND source_type = ? AND source_identity = ? AND source_record_id = ?`, ownerID, manifest.Scope, manifest.SourceType, manifest.SourceIdentity, record.input.SourceRecordID).Scan(&existingHash, &existingManifest)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return fmt.Errorf("%w: inspect common raw source conflict", domainmemory.ErrCommonRawUnavailable)
		}
		if strings.EqualFold(existingHash, record.hash) && existingManifest == manifestID {
			continue
		}
		if !strings.EqualFold(existingHash, record.hash) {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "source record content changed")
		}
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "source record is already bound to another manifest")
	}
	return nil
}

func verifyCommonRawStoredObject(root, ref string, expectedSize int64, expectedHash string) error {
	path, err := commonRawStoredObjectPath(root, ref)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("%w: stored raw object is missing", domainmemory.ErrCommonRawUnavailable)
	}
	return verifyCommonRawObjectHash(path, info, expectedSize, expectedHash)
}

func commonRawStoredObjectPath(root, ref string) (string, error) {
	if err := commonRawRootPathError(root); err != nil {
		return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}
	if strings.TrimSpace(ref) == "" || filepath.IsAbs(filepath.FromSlash(ref)) {
		return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object reference must be relative")
	}
	cleanRef := filepath.Clean(filepath.FromSlash(ref))
	if cleanRef == "." || cleanRef == ".." || strings.HasPrefix(cleanRef, ".."+string(filepath.Separator)) || filepath.VolumeName(cleanRef) != "" || filepath.ToSlash(cleanRef) != ref {
		return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object reference escapes configured root")
	}
	path := filepath.Join(root, cleanRef)
	rel, err := filepath.Rel(root, path)
	if err != nil || filepath.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object reference escapes configured root")
	}
	current := filepath.Clean(root)
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("%w: stored raw object is missing", domainmemory.ErrCommonRawUnavailable)
		}
		if err != nil {
			return "", fmt.Errorf("%w: inspect stored raw object path", domainmemory.ErrCommonRawUnavailable)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "stored raw object path contains a symlink")
		}
		if part != filepath.Base(path) && !info.IsDir() {
			return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "stored raw object path contains a non-directory")
		}
	}
	return path, nil
}

func valueOrDefault(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return strings.TrimSpace(fallback)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func assetObjectRef(hash string) string {
	return objectObjectRef(hash)
}

func objectObjectRef(hash string) string {
	hash = strings.ToLower(strings.TrimSpace(hash))
	if len(hash) < 2 {
		return ""
	}
	return filepath.ToSlash(filepath.Join("objects", "sha256", hash[:2], hash))
}

func commonRawRootPathError(root string) error {
	if strings.TrimSpace(root) == "" || !filepath.IsAbs(root) {
		return errors.New("raw source root must be absolute")
	}
	clean := filepath.Clean(root)
	volume := filepath.VolumeName(clean)
	rootOnly := volume + string(filepath.Separator)
	if clean == rootOnly || clean == string(filepath.Separator) || clean == "." {
		return errors.New("raw source root is too broad")
	}
	return inspectCommonRawPathComponents(clean)
}

func inspectCommonRawPathComponents(path string) error {
	clean := filepath.Clean(path)
	volume := filepath.VolumeName(clean)
	remainder := strings.TrimPrefix(clean, volume)
	separator := string(filepath.Separator)
	remainder = strings.TrimPrefix(remainder, separator)
	current := volume + separator
	if volume == "" && !strings.HasPrefix(clean, separator) {
		return errors.New("raw source root must be absolute")
	}
	if remainder == "" {
		return errors.New("raw source root is too broad")
	}
	for _, part := range strings.Split(remainder, separator) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("raw source root contains a symlink")
		}
		if !info.IsDir() {
			return errors.New("raw source root contains a non-directory")
		}
	}
	return nil
}

func ensureCommonRawRoot(root string) error {
	if err := commonRawRootPathError(root); err != nil {
		return fmt.Errorf("%w: configured raw source root rejected", domainmemory.ErrCommonRawRoot)
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("%w: create configured raw source root", domainmemory.ErrCommonRawUnavailable)
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is not a regular directory")
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(root, 0o700); err != nil {
			return fmt.Errorf("%w: secure configured raw source root", domainmemory.ErrCommonRawUnavailable)
		}
	}
	return nil
}

func writeCommonRawObject(root string, content []byte, hash string) (string, string, bool, error) {
	ref := objectObjectRef(hash)
	if ref == "" {
		return "", "", false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "object hash is invalid")
	}
	target := filepath.Join(root, filepath.FromSlash(ref))
	if err := commonRawRootPathError(root); err != nil {
		return "", "", false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}
	dir := filepath.Dir(target)
	if err := ensureCommonRawDirectory(root, dir); err != nil {
		return "", "", false, err
	}
	if info, err := os.Lstat(target); err == nil {
		if err := verifyCommonRawObject(target, info, content, hash); err != nil {
			return "", "", false, err
		}
		return ref, target, false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", false, fmt.Errorf("%w: inspect existing raw object", domainmemory.ErrCommonRawObject)
	}
	tmp, err := os.CreateTemp(dir, ".common-raw-*")
	if err != nil {
		return "", "", false, fmt.Errorf("%w: create raw object temporary file", domainmemory.ErrCommonRawUnavailable)
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return "", "", false, fmt.Errorf("%w: secure raw object temporary file", domainmemory.ErrCommonRawUnavailable)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return "", "", false, fmt.Errorf("%w: write raw object temporary file", domainmemory.ErrCommonRawUnavailable)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return "", "", false, fmt.Errorf("%w: sync raw object temporary file", domainmemory.ErrCommonRawUnavailable)
	}
	if err := tmp.Close(); err != nil {
		return "", "", false, fmt.Errorf("%w: close raw object temporary file", domainmemory.ErrCommonRawUnavailable)
	}
	info, err := os.Lstat(tmpPath)
	if err != nil {
		return "", "", false, fmt.Errorf("%w: inspect raw object temporary file", domainmemory.ErrCommonRawUnavailable)
	}
	if err := verifyCommonRawObject(tmpPath, info, content, hash); err != nil {
		return "", "", false, err
	}
	if err := finalizeCommonRawObjectNoReplace(tmpPath, target); err != nil {
		if existingInfo, statErr := os.Lstat(target); statErr == nil {
			if verifyErr := verifyCommonRawObject(target, existingInfo, content, hash); verifyErr != nil {
				return "", "", false, verifyErr
			}
			return ref, target, false, nil
		}
		return "", "", false, fmt.Errorf("%w: atomically finalize raw object", domainmemory.ErrCommonRawUnavailable)
	}
	removeTemp = false
	_ = syncCommonRawDirectory(dir)
	return ref, target, true, nil
}

func ensureCommonRawDirectory(root, target string) error {
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "raw object directory escapes configured root")
	}
	current := root
	if err := ensureCommonRawRoot(root); err != nil {
		return err
	}
	if rel == "." {
		return nil
	}
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "" || part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) {
			if err := os.Mkdir(current, 0o700); err != nil {
				return fmt.Errorf("%w: create raw object directory", domainmemory.ErrCommonRawUnavailable)
			}
			continue
		}
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "raw object directory is unsafe")
		}
	}
	return nil
}

func verifyCommonRawObject(path string, info os.FileInfo, expected []byte, hash string) error {
	return verifyCommonRawObjectHash(path, info, int64(len(expected)), hash)
}

func verifyCommonRawObjectHash(path string, info os.FileInfo, expectedSize int64, hash string) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || commonRawLinkCount(info) > 1 {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "existing raw object is not a private regular file")
	}
	if info.Size() != expectedSize {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "existing raw object size differs")
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("%w: read existing raw object", domainmemory.ErrCommonRawUnavailable)
	}
	if domainmemory.SHA256Hex(actual) != hash {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "existing raw object hash differs")
	}
	return nil
}

func commonRawLinkCount(info os.FileInfo) uint64 {
	sys := info.Sys()
	if sys == nil {
		return 0
	}
	value := reflect.ValueOf(sys)
	if value.Kind() == reflect.Ptr {
		if value.IsNil() {
			return 0
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0
	}
	field := value.FieldByName("Nlink")
	if !field.IsValid() {
		return 0
	}
	switch field.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return field.Uint()
	default:
		return 0
	}
}

func syncCommonRawDirectory(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
