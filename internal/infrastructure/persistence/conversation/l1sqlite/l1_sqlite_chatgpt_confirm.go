package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	chatGPTConfirmAuditEventType    = "memory.chatgpt_import_candidates_confirmed"
	chatGPTConfirmCandidatePageSize = 100
)

var chatGPTConfirmErrorMessages = map[domainmemory.ChatGPTImportErrorCode]string{
	domainmemory.ChatGPTImportErrorInvalid:       "invalid ChatGPT import confirmation request",
	domainmemory.ChatGPTImportErrorForbidden:     "ChatGPT import confirmation is forbidden",
	domainmemory.ChatGPTImportErrorNotFound:      "ChatGPT import was not found",
	domainmemory.ChatGPTImportErrorConflict:      "ChatGPT import confirmation conflicts with current state",
	domainmemory.ChatGPTImportErrorSourceChanged: "ChatGPT import source changed",
	domainmemory.ChatGPTImportErrorInternal:      "ChatGPT import confirmation storage failed",
	domainmemory.ChatGPTImportErrorUnavailable:   "ChatGPT import confirmation is unavailable",
}

func chatGPTConfirmError(code domainmemory.ChatGPTImportErrorCode) error {
	return domainmemory.NewChatGPTImportError(code, chatGPTConfirmErrorMessages[code])
}

func chatGPTConfirmInternalError() error {
	return chatGPTConfirmError(domainmemory.ChatGPTImportErrorInternal)
}

func chatGPTConfirmUnavailableError() error {
	return domainmemory.NewChatGPTImportError(domainmemory.ChatGPTImportErrorUnavailable, chatGPTConfirmErrorMessages[domainmemory.ChatGPTImportErrorUnavailable])
}

func rollbackChatGPTConfirm(tx *sql.Tx, cause error) error {
	if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
		return chatGPTConfirmError(domainmemory.ChatGPTImportErrorInternal)
	}
	return cause
}

type chatGPTConfirmReceiptRow struct {
	ReceiptID           string
	RequestID           string
	OwnerID             string
	ActorID             string
	ExportID            string
	Apply               int
	ReasonHash          string
	PayloadHash         string
	Matched             int
	Confirmed           int
	ProjectionPending   int
	ProjectionRunning   int
	ProjectionRetryWait int
	ProjectionFailed    int
	ProjectionCompleted int
	IdempotentReplay    int
	AuditReference      string
	ResultJSON          string
	CreatedAt           time.Time
}

type chatGPTConfirmRawTarget struct {
	RawRecordID      string
	ManifestID       string
	ManifestSHA256   string
	SourceCount      int
	SchemaVersion    string
	ConverterVersion string
	RawBinding       chatGPTRawBinding
	SourceRecordID   string
	ParentID         string
	ThreadID         string
	ContentSHA256    string
	ContentSize      int64
	StorageKind      string
	ObjectRef        string
	Event            L1MemoryEvent
	OriginalRole     string
	OnCurrentBranch  bool
	MessageCount     int
	BatchCount       int
}

type chatGPTConfirmManifestBinding struct {
	ManifestSHA256   string
	SourceCount      int
	SchemaVersion    string
	ConverterVersion string
	RawBinding       chatGPTRawBinding
}

type chatGPTConfirmProjectionReceipt struct {
	ID           string
	Projection   string
	OutputStore  string
	OutputID     string
	RawIDsJSON   string
	Revision     string
	InputSHA256  string
	OutputSHA256 string
	Status       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	Failure      string
	RawIDs       []string
}

type chatGPTConfirmCandidateRef struct {
	RowID int64
	ID    string
}

// ConfirmChatGPTImportCandidates confirms only owner-scoped profile
// candidates whose evidence is proven through the completed ChatGPT Raw
// projection chain. The apply path keeps candidate updates, audit, and the
// immutable idempotency receipt in one SQLite transaction.
func (s *L1SQLiteStore) ConfirmChatGPTImportCandidates(ctx context.Context, input domainmemory.ChatGPTImportConfirmInput) (domainmemory.ChatGPTImportConfirmResult, error) {
	if s == nil || s.db == nil {
		return domainmemory.ChatGPTImportConfirmResult{}, chatGPTConfirmUnavailableError()
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	input.OwnerID = strings.TrimSpace(input.OwnerID)
	input.ActorID = strings.TrimSpace(input.ActorID)
	input.ExportID = strings.TrimSpace(input.ExportID)
	input.Reason = strings.TrimSpace(input.Reason)
	if err := input.Validate(); err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, err
	}
	if err := authorizeChatGPTImportScope(ctx, input.RequestID, input.OwnerID, input.ActorID); err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, chatGPTConfirmError(domainmemory.ChatGPTImportErrorForbidden)
	}

	payloadHash := chatGPTConfirmPayloadHash(input)
	reasonHash := chatGPTConfirmReasonHash(input.Reason)
	receiptID := chatGPTConfirmReceiptID(input.OwnerID, input.RequestID)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, chatGPTConfirmInternalError()
	}
	if stored, found, readErr := readChatGPTConfirmReceipt(ctx, tx, input, receiptID, reasonHash, payloadHash); readErr != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, readErr)
	} else if found {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return domainmemory.ChatGPTImportConfirmResult{}, chatGPTConfirmInternalError()
		}
		stored.IdempotentReplay = true
		return stored, nil
	}

	importEvent, err := latestChatGPTConfirmImportEvent(ctx, tx, input.OwnerID, input.ExportID)
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, err)
	}
	projection, err := s.validateChatGPTConfirmTargets(ctx, tx, input.OwnerID, input.ExportID, importEvent)
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, err)
	}
	if input.Apply && (projection.pending > 0 || projection.running > 0 || projection.retryWait > 0 || projection.failed > 0) {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, chatGPTConfirmError(domainmemory.ChatGPTImportErrorConflict))
	}
	now := time.Now().UTC()
	matched, err := scanChatGPTConfirmCandidates(ctx, tx, input.OwnerID, input.ExportID, input.Apply, now)
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, err)
	}
	result := domainmemory.ChatGPTImportConfirmResult{
		RequestID:           input.RequestID,
		ExportID:            input.ExportID,
		Apply:               input.Apply,
		Matched:             matched,
		ProjectionPending:   projection.pending,
		ProjectionRunning:   projection.running,
		ProjectionRetryWait: projection.retryWait,
		ProjectionFailed:    projection.failed,
		ProjectionCompleted: projection.completed,
	}
	if !input.Apply {
		if err := tx.Rollback(); err != nil && !errors.Is(err, sql.ErrTxDone) {
			return domainmemory.ChatGPTImportConfirmResult{}, chatGPTConfirmInternalError()
		}
		return result, nil
	}
	result.Confirmed = matched
	result.AuditReference = chatGPTConfirmAuditReference(input)
	_, err = appendL1EventLog(ctx, tx, chatGPTConfirmAuditEventType, "user:"+input.OwnerID, "", 0, map[string]interface{}{
		"request_id":            input.RequestID,
		"export_id":             input.ExportID,
		"reason":                input.Reason,
		"matched":               result.Matched,
		"confirmed":             result.Confirmed,
		"projection_pending":    result.ProjectionPending,
		"projection_running":    result.ProjectionRunning,
		"projection_retry_wait": result.ProjectionRetryWait,
		"projection_failed":     result.ProjectionFailed,
		"projection_completed":  result.ProjectionCompleted,
		"audit_reference":       result.AuditReference,
	}, "chatgpt_import_confirm")
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, chatGPTConfirmInternalError())
	}
	resultJSON, err := json.Marshal(result)
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, chatGPTConfirmInternalError())
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO l1_chatgpt_import_confirm_receipt (
	receipt_id, request_id, owner_id, actor_id, export_id, apply, reason_hash, payload_hash,
	matched, confirmed, projection_pending, projection_running, projection_retry_wait,
	projection_failed, projection_completed, idempotent_replay, audit_reference, result_json, created_at
) VALUES (?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, ?, ?, ?)`,
		receiptID, input.RequestID, input.OwnerID, input.ActorID, input.ExportID, reasonHash, payloadHash,
		result.Matched, result.Confirmed, result.ProjectionPending, result.ProjectionRunning,
		result.ProjectionRetryWait, result.ProjectionFailed, result.ProjectionCompleted,
		result.AuditReference, string(resultJSON), now)
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, rollbackChatGPTConfirm(tx, chatGPTConfirmInternalError())
	}
	if err := tx.Commit(); err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, chatGPTConfirmInternalError()
	}
	return result, nil
}

// chatGPTConfirmImportEvent is deliberately loaded by owner and export. A
// missing row therefore gives the same result for an unknown export and an
// export owned by another user.
func latestChatGPTConfirmImportEvent(ctx context.Context, tx *sql.Tx, ownerID, exportID string) (domainmemory.ChatGPTImportEvent, error) {
	event, err := queryChatGPTImportEvent(ctx, tx, `SELECT `+chatGPTImportEventColumns+` FROM l1_chatgpt_import_event WHERE owner_id = ? AND export_id = ? ORDER BY rowid DESC LIMIT 1`, ownerID, exportID)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmemory.ChatGPTImportEvent{}, chatGPTConfirmError(domainmemory.ChatGPTImportErrorNotFound)
	}
	if err != nil {
		return domainmemory.ChatGPTImportEvent{}, chatGPTConfirmInternalError()
	}
	if event.State != domainmemory.ChatGPTImportStateCompleted || !event.Apply {
		return domainmemory.ChatGPTImportEvent{}, chatGPTConfirmError(domainmemory.ChatGPTImportErrorConflict)
	}
	return event, nil
}

func validateChatGPTConfirmLedgerBinding(event domainmemory.ChatGPTImportEvent, exportID string, targetCount int) error {
	if event.Binding.ExportID != exportID || event.Binding.MessageCount != targetCount || event.Counts.MessageCount != event.Binding.MessageCount || event.Counts.SourceCount <= 0 || event.Counts.BatchCount <= 0 {
		return chatGPTConfirmError(domainmemory.ChatGPTImportErrorSourceChanged)
	}
	return nil
}

func validateChatGPTConfirmTargetBinding(event domainmemory.ChatGPTImportEvent, target chatGPTConfirmRawTarget) error {
	binding := target.RawBinding
	if binding.Adapter != chatGPTRawAdapterVersion || binding.ManifestSHA256 != event.Binding.ManifestSHA256 || binding.ArtifactSHA256 != event.Binding.ArtifactSHA256 || binding.SourceCount != event.Counts.SourceCount || binding.BatchCount != event.Counts.BatchCount || binding.SchemaVersion != event.Binding.SchemaVersion || binding.ConverterVersion != event.Binding.ConverterVersion {
		return chatGPTConfirmError(domainmemory.ChatGPTImportErrorSourceChanged)
	}
	return nil
}

func readChatGPTConfirmReceipt(ctx context.Context, tx *sql.Tx, input domainmemory.ChatGPTImportConfirmInput, receiptID, reasonHash, payloadHash string) (domainmemory.ChatGPTImportConfirmResult, bool, error) {
	var row chatGPTConfirmReceiptRow
	err := tx.QueryRowContext(ctx, `
SELECT receipt_id, request_id, owner_id, actor_id, export_id, apply, reason_hash, payload_hash,
       matched, confirmed, projection_pending, projection_running, projection_retry_wait,
       projection_failed, projection_completed, idempotent_replay, audit_reference, result_json, created_at
FROM l1_chatgpt_import_confirm_receipt
WHERE owner_id = ? AND request_id = ?`, input.OwnerID, input.RequestID).Scan(
		&row.ReceiptID, &row.RequestID, &row.OwnerID, &row.ActorID, &row.ExportID, &row.Apply,
		&row.ReasonHash, &row.PayloadHash, &row.Matched, &row.Confirmed, &row.ProjectionPending,
		&row.ProjectionRunning, &row.ProjectionRetryWait, &row.ProjectionFailed,
		&row.ProjectionCompleted, &row.IdempotentReplay, &row.AuditReference, &row.ResultJSON, &row.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return domainmemory.ChatGPTImportConfirmResult{}, false, nil
	}
	if err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	if row.ReceiptID != receiptID || row.RequestID != input.RequestID || row.OwnerID != input.OwnerID {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	if row.ActorID != input.ActorID || row.ExportID != input.ExportID || row.Apply != boolInt(input.Apply) || row.ReasonHash != reasonHash || row.PayloadHash != payloadHash {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmError(domainmemory.ChatGPTImportErrorConflict)
	}
	if row.Apply != 1 || row.IdempotentReplay != 0 || row.CreatedAt.IsZero() || !validLowerSHA256Claim(row.ReasonHash) || !validLowerSHA256Claim(row.PayloadHash) {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	var result domainmemory.ChatGPTImportConfirmResult
	decoder := json.NewDecoder(strings.NewReader(row.ResultJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	var trailing interface{}
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	if result.RequestID != row.RequestID || result.ExportID != row.ExportID || !result.Apply || result.IdempotentReplay || result.AuditReference != row.AuditReference ||
		result.Matched != row.Matched || result.Confirmed != row.Confirmed || result.ProjectionPending != row.ProjectionPending ||
		result.ProjectionRunning != row.ProjectionRunning || result.ProjectionRetryWait != row.ProjectionRetryWait ||
		result.ProjectionFailed != row.ProjectionFailed || result.ProjectionCompleted != row.ProjectionCompleted ||
		result.Confirmed < 0 || result.Confirmed > result.Matched || result.Matched < 0 || result.AuditReference == "" ||
		result.ProjectionPending < 0 || result.ProjectionRunning < 0 || result.ProjectionRetryWait < 0 ||
		result.ProjectionFailed < 0 || result.ProjectionCompleted < 0 ||
		strings.ContainsAny(result.AuditReference, "\r\n\x00/\\") {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	canonicalResult, err := json.Marshal(result)
	if err != nil || string(canonicalResult) != row.ResultJSON {
		return domainmemory.ChatGPTImportConfirmResult{}, false, chatGPTConfirmInternalError()
	}
	return result, true, nil
}

func chatGPTConfirmPayloadHash(input domainmemory.ChatGPTImportConfirmInput) string {
	apply := "0"
	if input.Apply {
		apply = "1"
	}
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-confirm-payload.v1\x00" + input.OwnerID + "\x00" + input.ActorID + "\x00" + input.ExportID + "\x00" + input.Reason + "\x00" + apply))
	return hex.EncodeToString(digest[:])
}

func chatGPTConfirmReasonHash(reason string) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-confirm-reason.v1\x00" + reason))
	return hex.EncodeToString(digest[:])
}

func chatGPTConfirmReceiptID(ownerID, requestID string) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-confirm-receipt.v1\x00" + ownerID + "\x00" + requestID))
	return "chatgpt-confirm-receipt:" + hex.EncodeToString(digest[:])
}

func chatGPTConfirmAuditReference(input domainmemory.ChatGPTImportConfirmInput) string {
	digest := sha256.Sum256([]byte("rencrow.chatgpt-import-confirm-audit.v1\x00" + input.OwnerID + "\x00" + input.ActorID + "\x00" + input.RequestID + "\x00" + input.ExportID + "\x00" + input.Reason))
	return "chatgpt-confirm-audit:" + hex.EncodeToString(digest[:])
}

func (s *L1SQLiteStore) validateChatGPTConfirmTargets(ctx context.Context, tx *sql.Tx, ownerID, exportID string, importEvent domainmemory.ChatGPTImportEvent) (chatGPTConfirmProjectionCounts, error) {
	if err := validateChatGPTConfirmLedgerBinding(importEvent, exportID, importEvent.Binding.MessageCount); err != nil {
		return chatGPTConfirmProjectionCounts{}, err
	}
	counts := chatGPTConfirmProjectionCounts{}
	var cursor int64
	targetCount := 0
	for {
		target, rowID, found, err := s.loadNextChatGPTConfirmTarget(ctx, tx, ownerID, exportID, cursor, importEvent)
		if err != nil {
			return chatGPTConfirmProjectionCounts{}, err
		}
		if !found {
			break
		}
		cursor = rowID
		targetCount++
		if err := validateChatGPTConfirmTargetBinding(importEvent, target); err != nil {
			return chatGPTConfirmProjectionCounts{}, err
		}
		if err := validateChatGPTConfirmProjectionTarget(ctx, tx, target); err != nil {
			return chatGPTConfirmProjectionCounts{}, err
		}
		jobState, err := validateChatGPTConfirmTargetJob(ctx, tx, target)
		if err != nil {
			return chatGPTConfirmProjectionCounts{}, err
		}
		if err := addChatGPTConfirmProjectionState(&counts, jobState); err != nil {
			return chatGPTConfirmProjectionCounts{}, err
		}
	}
	if err := validateChatGPTConfirmLedgerBinding(importEvent, exportID, targetCount); err != nil {
		return chatGPTConfirmProjectionCounts{}, err
	}
	if err := validateChatGPTConfirmOrphanJobs(ctx, tx, ownerID, exportID); err != nil {
		return chatGPTConfirmProjectionCounts{}, err
	}
	return counts, nil
}

// loadNextChatGPTConfirmTarget uses a rowid keyset and returns exactly one
// target. A confirmation therefore never retains payloads, L3 rows, or Raw
// bindings for the whole export in Go memory.
func (s *L1SQLiteStore) loadNextChatGPTConfirmTarget(ctx context.Context, tx *sql.Tx, ownerID, exportID string, afterRowID int64, importEvent domainmemory.ChatGPTImportEvent) (chatGPTConfirmRawTarget, int64, bool, error) {
	ownerScope := "user:" + ownerID
	var rowID int64
	var rawID, manifestID, sourceID, parentID, threadID, storedOwner, storedScope, sourceType, sourceIdentity, rawRole, contentType, sensitivity, contentHash, storageKind, objectRef, assetRefsJSON, provenance, rights, license string
	var inlinePayload []byte
	var contentSize int64
	err := tx.QueryRowContext(ctx, `
SELECT rowid, raw_record_id, manifest_id, source_record_id, parent_id, thread_id, owner_id, scope,
       source_type, source_identity, role, content_type, sensitivity, content_sha256, content_size,
       storage_kind, object_ref, inline_payload, asset_refs_json, provenance, rights, license
FROM l1_raw_record
WHERE owner_id = ? AND scope = ? AND source_type = ? AND source_identity = ? AND rowid > ?
ORDER BY rowid ASC
LIMIT 1`, ownerID, ownerScope, chatGPTRawSourceType, exportID, afterRowID).Scan(
		&rowID, &rawID, &manifestID, &sourceID, &parentID, &threadID, &storedOwner, &storedScope,
		&sourceType, &sourceIdentity, &rawRole, &contentType, &sensitivity, &contentHash, &contentSize,
		&storageKind, &objectRef, &inlinePayload, &assetRefsJSON, &provenance, &rights, &license)
	if errors.Is(err, sql.ErrNoRows) {
		return chatGPTConfirmRawTarget{}, afterRowID, false, nil
	}
	if err != nil {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	if rowID <= afterRowID || rawID == "" || manifestID == "" || sourceID == "" || storedOwner != ownerID || storedScope != ownerScope || sourceType != chatGPTRawSourceType || sourceIdentity != exportID || sensitivity != domainmemory.CommonRawPrivateSensitivity || rawRole == "" || !chatGPTConfirmValidRole(rawRole) || contentType != ChatGPTRawContentType || threadID == "" || provenance == "" || rights != "owner" || license != "private" || contentSize < 0 || !validLowerSHA256Claim(contentHash) || (storageKind != domainmemory.CommonRawStorageInline && storageKind != domainmemory.CommonRawStorageObject) || (storageKind == domainmemory.CommonRawStorageInline && objectRef != "") || rawID != domainmemory.DeterministicCommonRawRecordID(ownerID, ownerScope, sourceType, sourceIdentity, sourceID, contentHash) {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	if storageKind == domainmemory.CommonRawStorageObject && objectRef == "" {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	if storageKind == domainmemory.CommonRawStorageInline {
		if inlinePayload == nil || int64(len(inlinePayload)) != contentSize || domainmemory.SHA256Hex(inlinePayload) != contentHash {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
		}
	} else {
		if inlinePayload != nil {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
		}
		if strings.TrimSpace(s.rawSourceRoot) == "" {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmUnavailableError()
		}
		if err := verifyCommonRawStoredObject(s.rawSourceRoot, objectRef, contentSize, contentHash); err != nil {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmError(domainmemory.ChatGPTImportErrorInternal)
		}
	}
	var assetRefs []interface{}
	if assetRefsJSON != "[]" || json.Unmarshal([]byte(assetRefsJSON), &assetRefs) != nil || len(assetRefs) != 0 {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	manifest, err := validateChatGPTConfirmManifest(ctx, tx, ownerID, exportID, manifestID, rawID, sourceID, contentHash)
	if err != nil {
		return chatGPTConfirmRawTarget{}, afterRowID, false, err
	}
	rawPayload := inlinePayload
	if storageKind == domainmemory.CommonRawStorageObject {
		if strings.TrimSpace(s.rawSourceRoot) == "" {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmUnavailableError()
		}
		path, pathErr := commonRawStoredObjectPath(s.rawSourceRoot, objectRef)
		if pathErr != nil {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
		}
		rawPayload, err = os.ReadFile(path)
		if err != nil || int64(len(rawPayload)) != contentSize || domainmemory.SHA256Hex(rawPayload) != contentHash {
			return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
		}
	}
	event, err := loadChatGPTEventQueryer(ctx, tx, sourceID)
	if err != nil {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	role, roleOK := chatGPTConfirmStringMeta(event.Meta, "original_role")
	branch, branchOK := chatGPTConfirmBoolMeta(event.Meta, "on_current_branch")
	externalSource, sourceOK := chatGPTConfirmStringMeta(event.Meta, "external_source")
	storedExport, exportOK := chatGPTConfirmStringMeta(event.Meta, "export_id")
	expectedNamespace := chatGPTConversationNamespace(threadID)
	if !roleOK || !branchOK || !sourceOK || !exportOK {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	if externalSource != chatGPTRawSourceType || storedExport != exportID || role != rawRole || event.ID != sourceID || event.Source != chatGPTRawSourceType || event.Namespace != expectedNamespace || event.SessionID != strings.TrimPrefix(expectedNamespace, "conv:") || event.ThreadID != chatGPTConversationThreadID(threadID) || event.Speaker != chatGPTRawSpeaker(role) {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmError(domainmemory.ChatGPTImportErrorSourceChanged)
	}
	if event.Layer != "L3" || event.MemoryState != MemoryStateObserved {
		return chatGPTConfirmRawTarget{}, afterRowID, false, chatGPTConfirmInternalError()
	}
	target := chatGPTConfirmRawTarget{
		RawRecordID: rawID, ManifestID: manifestID, ManifestSHA256: manifest.ManifestSHA256,
		SourceCount: manifest.SourceCount, SchemaVersion: manifest.SchemaVersion, ConverterVersion: manifest.ConverterVersion,
		RawBinding: manifest.RawBinding, SourceRecordID: sourceID, ParentID: parentID, ThreadID: threadID,
		ContentSHA256: contentHash, ContentSize: contentSize, StorageKind: storageKind, ObjectRef: objectRef,
		Event: *event, OriginalRole: role, OnCurrentBranch: branch,
		MessageCount: importEvent.Binding.MessageCount, BatchCount: importEvent.Counts.BatchCount,
	}
	if err := validateChatGPTConfirmRawPayload(rawPayload, target); err != nil {
		return chatGPTConfirmRawTarget{}, afterRowID, false, err
	}
	return target, rowID, true, nil
}

func validateChatGPTConfirmManifest(ctx context.Context, tx *sql.Tx, ownerID, exportID, manifestID, rawID, sourceID, contentHash string) (chatGPTConfirmManifestBinding, error) {
	var contractVersion, sourceType, sourceIdentity, manifestHash, storedOwner, scope, sensitivity, intakeStatus, receiptJSON, schemaVersion, converterVersion, provenance string
	var sourceCount int
	if err := tx.QueryRowContext(ctx, `SELECT contract_version, source_type, source_identity, manifest_sha256, source_count, schema_version, converter_version, owner_id, scope, sensitivity, intake_status, receipt_json, provenance FROM l1_raw_source_manifest WHERE manifest_id = ?`, manifestID).Scan(&contractVersion, &sourceType, &sourceIdentity, &manifestHash, &sourceCount, &schemaVersion, &converterVersion, &storedOwner, &scope, &sensitivity, &intakeStatus, &receiptJSON, &provenance); err != nil {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	if contractVersion != domainmemory.CommonRawContractVersion || sourceType != chatGPTRawSourceType || sourceIdentity != exportID || storedOwner != ownerID || scope != "user:"+ownerID || sensitivity != domainmemory.CommonRawPrivateSensitivity || intakeStatus != string(domainmemory.CommonRawStateCompleted) || !validLowerSHA256Claim(manifestHash) || sourceCount < 0 || manifestID != domainmemory.DeterministicCommonRawManifestID(ownerID, scope, sourceType, sourceIdentity, manifestHash) {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	var rawBinding chatGPTRawBinding
	if err := json.Unmarshal([]byte(provenance), &rawBinding); err != nil || rawBinding.Adapter != chatGPTRawAdapterVersion || rawBinding.ManifestSHA256 == "" || rawBinding.ArtifactSHA256 == "" || rawBinding.SchemaVersion != schemaVersion || rawBinding.ConverterVersion != converterVersion {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	canonicalBinding, err := json.Marshal(rawBinding)
	if err != nil || string(canonicalBinding) != provenance || !validLowerSHA256Claim(rawBinding.ManifestSHA256) || !validLowerSHA256Claim(rawBinding.ArtifactSHA256) || rawBinding.SourceCount <= 0 || rawBinding.BatchCount <= 0 {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	var receipt domainmemory.CommonRawIntakeReceipt
	if err := json.Unmarshal([]byte(receiptJSON), &receipt); err != nil || receipt.ManifestID != manifestID || receipt.Status != domainmemory.CommonRawStateCompleted || receipt.ManifestSHA256 != manifestHash || receipt.SourceCount != sourceCount || len(receipt.Records) != sourceCount || receipt.Checkpoint != "completed" {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	canonicalReceipt, err := json.Marshal(receipt)
	if err != nil || string(canonicalReceipt) != receiptJSON {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	found := 0
	for _, item := range receipt.Records {
		if item.SourceRecordID == sourceID {
			found++
			if item.RawRecordID != rawID || item.ContentSHA256 != contentHash || !validLowerSHA256Claim(item.ContentSHA256) {
				return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
			}
		}
	}
	if found != 1 {
		return chatGPTConfirmManifestBinding{}, chatGPTConfirmInternalError()
	}
	return chatGPTConfirmManifestBinding{ManifestSHA256: manifestHash, SourceCount: sourceCount, SchemaVersion: schemaVersion, ConverterVersion: converterVersion, RawBinding: rawBinding}, nil
}

type chatGPTConfirmRawPayload struct {
	Format                string          `json:"format"`
	ExportID              string          `json:"export_id"`
	EvidenceID            string          `json:"evidence_id"`
	ConversationID        string          `json:"conversation_id"`
	ConversationTitle     string          `json:"conversation_title"`
	ConversationCreatedAt time.Time       `json:"conversation_created_at"`
	ConversationUpdatedAt time.Time       `json:"conversation_updated_at"`
	NodeID                string          `json:"node_id"`
	ParentNodeID          string          `json:"parent_node_id"`
	ChildNodeIDs          []string        `json:"child_node_ids"`
	OnCurrentBranch       bool            `json:"on_current_branch"`
	MessageID             string          `json:"message_id"`
	MessageCreatedAt      time.Time       `json:"message_created_at"`
	Role                  string          `json:"role"`
	ContentType           string          `json:"content_type"`
	Text                  string          `json:"text"`
	Content               json.RawMessage `json:"content"`
	Metadata              json.RawMessage `json:"metadata"`
	ArtifactLine          int             `json:"artifact_line"`
	ManifestSHA256        string          `json:"manifest_sha256"`
	ArtifactSHA256        string          `json:"artifact_sha256"`
	SourceCount           int             `json:"source_count"`
	SchemaVersion         string          `json:"schema_version"`
	ConverterVersion      string          `json:"converter_version"`
	BatchIndex            int             `json:"batch_index"`
	BatchCount            int             `json:"batch_count"`
	StartLine             int             `json:"start_line"`
}

func validateChatGPTConfirmRawPayload(rawPayload []byte, target chatGPTConfirmRawTarget) error {
	var payload chatGPTConfirmRawPayload
	if len(rawPayload) == 0 || json.Unmarshal(rawPayload, &payload) != nil {
		return chatGPTConfirmInternalError()
	}
	canonical, err := json.Marshal(payload)
	if err != nil || string(canonical) != string(rawPayload) {
		return chatGPTConfirmInternalError()
	}
	item := domainmemory.ChatGPTL3ImportRecord{
		Format: payload.Format, ExportID: payload.ExportID, EvidenceID: payload.EvidenceID,
		ConversationID: payload.ConversationID, ConversationTitle: payload.ConversationTitle,
		ConversationCreatedAt: payload.ConversationCreatedAt, ConversationUpdatedAt: payload.ConversationUpdatedAt,
		NodeID: payload.NodeID, ParentNodeID: payload.ParentNodeID, ChildNodeIDs: payload.ChildNodeIDs,
		OnCurrentBranch: payload.OnCurrentBranch, MessageID: payload.MessageID,
		MessageCreatedAt: payload.MessageCreatedAt, Role: payload.Role, ContentType: payload.ContentType,
		Text: payload.Text, Content: payload.Content, Metadata: payload.Metadata,
	}
	if err := domainmemory.ValidateChatGPTL3ImportRecord(item); err != nil {
		return chatGPTConfirmInternalError()
	}
	storedExport, ok := chatGPTConfirmStringMeta(target.Event.Meta, "export_id")
	if !ok {
		return chatGPTConfirmInternalError()
	}
	if payload.ExportID != storedExport || payload.EvidenceID != target.SourceRecordID || payload.ConversationID != target.ThreadID || payload.ParentNodeID != target.ParentID || payload.Role != target.OriginalRole || payload.OnCurrentBranch != target.OnCurrentBranch || payload.ManifestSHA256 != target.RawBinding.ManifestSHA256 || payload.ArtifactSHA256 != target.RawBinding.ArtifactSHA256 || payload.SourceCount != target.RawBinding.SourceCount || payload.SchemaVersion != target.RawBinding.SchemaVersion || payload.ConverterVersion != target.RawBinding.ConverterVersion || payload.BatchCount != target.RawBinding.BatchCount || payload.BatchCount != target.BatchCount || payload.BatchCount <= 0 || payload.BatchIndex < 0 || payload.BatchIndex >= payload.BatchCount || payload.StartLine < 1 || payload.StartLine > target.MessageCount || payload.ArtifactLine < payload.StartLine || payload.ArtifactLine > target.MessageCount {
		return chatGPTConfirmInternalError()
	}
	if target.Event.CreatedAt.IsZero() || !target.Event.CreatedAt.Equal(chatGPTRawOccurredAt(item)) {
		return chatGPTConfirmInternalError()
	}
	if err := validateChatGPTLegacyEvent(&target.Event, item); err != nil {
		return chatGPTConfirmInternalError()
	}
	return nil
}

type chatGPTConfirmProjectionCounts struct {
	pending, running, retryWait, failed, completed int
}

func validateChatGPTConfirmProjectionTarget(ctx context.Context, tx *sql.Tx, target chatGPTConfirmRawTarget) error {
	expectedPending := chatGPTRawProjectionReceiptID("pending", target.RawRecordID)
	expectedCompleted := chatGPTRawProjectionReceiptID("completed", target.RawRecordID)
	related, err := loadChatGPTConfirmProjectionReceipts(ctx, tx, expectedPending, expectedCompleted)
	if err != nil {
		return err
	}
	var pending, completed *chatGPTConfirmProjectionReceipt
	for i := range related {
		receipt := related[i]
		if receipt.ID != expectedPending && receipt.ID != expectedCompleted {
			return chatGPTConfirmInternalError()
		}
		if receipt.ID == expectedPending {
			if pending != nil {
				return chatGPTConfirmInternalError()
			}
			pending = &related[i]
		}
		if receipt.ID == expectedCompleted {
			if completed != nil {
				return chatGPTConfirmInternalError()
			}
			completed = &related[i]
		}
	}
	if pending == nil || completed == nil || !verifyChatGPTConfirmProjectionReceipt(*pending, target, "pending", "") || completed.CreatedAt.Before(pending.CreatedAt) {
		return chatGPTConfirmInternalError()
	}
	outputHash, err := CanonicalL1MemoryEventSHA256(target.Event)
	if err != nil || !verifyChatGPTConfirmProjectionReceipt(*completed, target, "completed", outputHash) {
		return chatGPTConfirmInternalError()
	}
	return nil
}

// loadChatGPTConfirmProjectionReceipts uses the deterministic receipt IDs as
// the lookup key. The Common Raw projection table may contain receipts for a
// very large export, so confirmation must never scan or materialize it all.
func loadChatGPTConfirmProjectionReceipts(ctx context.Context, tx *sql.Tx, pendingID, completedID string) ([]chatGPTConfirmProjectionReceipt, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT projection_receipt_id, projection_type, output_store, output_record_id, raw_record_ids_json,
       revision, input_sha256, output_sha256, status, created_at, updated_at, failure_reason
FROM l1_raw_projection_receipt
WHERE projection_receipt_id IN (?, ?)
ORDER BY projection_receipt_id ASC`, pendingID, completedID)
	if err != nil {
		return nil, chatGPTConfirmInternalError()
	}
	defer rows.Close()
	receipts := make([]chatGPTConfirmProjectionReceipt, 0, 3)
	for rows.Next() {
		var receipt chatGPTConfirmProjectionReceipt
		if err := rows.Scan(&receipt.ID, &receipt.Projection, &receipt.OutputStore, &receipt.OutputID, &receipt.RawIDsJSON, &receipt.Revision, &receipt.InputSHA256, &receipt.OutputSHA256, &receipt.Status, &receipt.CreatedAt, &receipt.UpdatedAt, &receipt.Failure); err != nil {
			return nil, chatGPTConfirmInternalError()
		}
		if err := json.Unmarshal([]byte(receipt.RawIDsJSON), &receipt.RawIDs); err != nil {
			return nil, chatGPTConfirmInternalError()
		}
		receipts = append(receipts, receipt)
	}
	if err := rows.Err(); err != nil {
		return nil, chatGPTConfirmInternalError()
	}
	if len(receipts) != 2 {
		return nil, chatGPTConfirmInternalError()
	}
	return receipts, nil
}

func verifyChatGPTConfirmProjectionReceipt(receipt chatGPTConfirmProjectionReceipt, target chatGPTConfirmRawTarget, status, outputHash string) bool {
	expectedID := chatGPTRawProjectionReceiptID(status, target.RawRecordID)
	return receipt.ID == expectedID && receipt.Projection == ChatGPTRawProjectionType && receipt.OutputStore == "conversation_l1" && receipt.OutputID == target.SourceRecordID && receipt.RawIDsJSON == chatGPTProjectionRawIDsJSON(target.RawRecordID) && len(receipt.RawIDs) == 1 && receipt.RawIDs[0] == target.RawRecordID && receipt.Revision == ChatGPTRawProjectionRevision && receipt.InputSHA256 == target.ContentSHA256 && receipt.Status == status && receipt.CreatedAt.After(time.Time{}) && receipt.UpdatedAt.After(time.Time{}) && receipt.Failure == "" && ((status == "pending" && receipt.OutputSHA256 == "") || (status == "completed" && receipt.OutputSHA256 == outputHash && validLowerSHA256Claim(receipt.OutputSHA256)))
}

func validateChatGPTConfirmTargetJob(ctx context.Context, tx *sql.Tx, target chatGPTConfirmRawTarget) (string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT session_id, thread_id, state
FROM l1_profile_promotion_job
WHERE evidence_event_id = ?
LIMIT 2`, target.SourceRecordID)
	if err != nil {
		return "", chatGPTConfirmInternalError()
	}
	jobs := make([]struct {
		sessionID string
		threadID  int64
		state     string
	}, 0, 2)
	for rows.Next() {
		var job struct {
			sessionID string
			threadID  int64
			state     string
		}
		if err := rows.Scan(&job.sessionID, &job.threadID, &job.state); err != nil {
			_ = rows.Close()
			return "", chatGPTConfirmInternalError()
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return "", chatGPTConfirmInternalError()
	}
	if err := rows.Close(); err != nil {
		return "", chatGPTConfirmInternalError()
	}
	if target.OriginalRole != "user" || !target.OnCurrentBranch {
		if len(jobs) != 0 {
			return "", chatGPTConfirmInternalError()
		}
		return "", nil
	}
	if len(jobs) != 1 || jobs[0].sessionID != target.Event.SessionID || jobs[0].threadID != target.Event.ThreadID {
		return "", chatGPTConfirmInternalError()
	}
	return jobs[0].state, nil
}

func addChatGPTConfirmProjectionState(counts *chatGPTConfirmProjectionCounts, state string) error {
	if state == "" {
		return nil
	}
	switch state {
	case domainmemory.ProfilePromotionPending:
		counts.pending++
	case domainmemory.ProfilePromotionRunning:
		counts.running++
	case domainmemory.ProfilePromotionRetryWait:
		counts.retryWait++
	case domainmemory.ProfilePromotionFailed:
		counts.failed++
	case domainmemory.ProfilePromotionCompleted:
		counts.completed++
	default:
		return chatGPTConfirmInternalError()
	}
	return nil
}

func validateChatGPTConfirmOrphanJobs(ctx context.Context, tx *sql.Tx, ownerID, exportID string) error {
	// A promotion job tied to a ChatGPT L3 event without an exact owner-bound
	// Raw target is an unproven legacy path. Reject it without loading every
	// event/job row into Go memory.
	var orphanJobs int
	if err := tx.QueryRowContext(ctx, `
SELECT count(*)
FROM l1_profile_promotion_job AS job
JOIN l1_memory_event AS event ON event.id = job.evidence_event_id
WHERE event.source = ?
  AND json_extract(event.meta_json, '$.export_id') = ?
  AND NOT EXISTS (
    SELECT 1 FROM l1_raw_record AS raw
    WHERE raw.owner_id = ? AND raw.scope = ? AND raw.source_type = ?
      AND raw.source_identity = ? AND raw.source_record_id = event.id
	  )`, chatGPTRawSourceType, exportID, ownerID, "user:"+ownerID, chatGPTRawSourceType, exportID).Scan(&orphanJobs); err != nil {
		return chatGPTConfirmInternalError()
	}
	if orphanJobs != 0 {
		return chatGPTConfirmInternalError()
	}
	return nil
}

func loadChatGPTConfirmCandidatePage(ctx context.Context, tx *sql.Tx, ownerID string, afterRowID int64) ([]chatGPTConfirmCandidateRef, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT rowid, id
FROM l1_memory_event
WHERE namespace = ? AND source = ? AND memory_state = ? AND rowid > ?
ORDER BY rowid ASC
LIMIT ?`, "user:"+ownerID, "profile_extractor", domainmemory.MemoryStateCandidate, afterRowID, chatGPTConfirmCandidatePageSize)
	if err != nil {
		return nil, chatGPTConfirmInternalError()
	}
	defer rows.Close()
	page := make([]chatGPTConfirmCandidateRef, 0, chatGPTConfirmCandidatePageSize)
	for rows.Next() {
		var item chatGPTConfirmCandidateRef
		if err := rows.Scan(&item.RowID, &item.ID); err != nil || item.RowID <= afterRowID || strings.TrimSpace(item.ID) == "" {
			return nil, chatGPTConfirmInternalError()
		}
		page = append(page, item)
	}
	if err := rows.Err(); err != nil || len(page) > chatGPTConfirmCandidatePageSize {
		return nil, chatGPTConfirmInternalError()
	}
	return page, nil
}

func scanChatGPTConfirmCandidates(ctx context.Context, tx *sql.Tx, ownerID, exportID string, apply bool, updatedAt time.Time) (int, error) {
	matched := 0
	var cursor int64
	for {
		page, err := loadChatGPTConfirmCandidatePage(ctx, tx, ownerID, cursor)
		if err != nil {
			return 0, err
		}
		if len(page) == 0 {
			break
		}
		for _, ref := range page {
			events, err := scanL1EventRows(tx.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event
WHERE rowid = ? AND id = ?`, ref.RowID, ref.ID))
			if err != nil || len(events) != 1 {
				return 0, chatGPTConfirmInternalError()
			}
			event := events[0]
			item, strictErr := strictUserMemoryFromEvent(event)
			if strictErr != nil {
				return 0, chatGPTConfirmInternalError()
			}
			if item.UserID != ownerID || item.Namespace != "user:"+ownerID || item.State != domainmemory.MemoryStateCandidate || !item.Active || item.Sensitivity != "normal" || !chatGPTConfirmExplicitActiveNormalEvidence(event.Meta) {
				continue
			}
			evidenceIDs, ok := chatGPTConfirmEvidenceIDs(event.Meta)
			if !ok {
				continue
			}
			allExpected := true
			for _, evidenceID := range evidenceIDs {
				eligible, err := chatGPTConfirmEvidenceEligible(ctx, tx, ownerID, exportID, evidenceID)
				if err != nil {
					return 0, err
				}
				if !eligible {
					allExpected = false
					break
				}
			}
			if !allExpected {
				continue
			}
			matched++
			if !apply {
				continue
			}
			updated, err := tx.ExecContext(ctx, `
UPDATE l1_memory_event
SET memory_state = ?, updated_at = ?
WHERE rowid = ? AND id = ? AND namespace = ? AND source = ? AND memory_state = ?`,
				domainmemory.MemoryStateConfirmed, updatedAt, ref.RowID, ref.ID,
				"user:"+ownerID, "profile_extractor", domainmemory.MemoryStateCandidate)
			if err != nil {
				return 0, chatGPTConfirmInternalError()
			}
			affected, err := updated.RowsAffected()
			if err != nil || affected != 1 {
				return 0, chatGPTConfirmInternalError()
			}
		}
		cursor = page[len(page)-1].RowID
	}
	return matched, nil
}

func chatGPTConfirmEvidenceEligible(ctx context.Context, tx *sql.Tx, ownerID, exportID, evidenceID string) (bool, error) {
	var role string
	var branch int
	err := tx.QueryRowContext(ctx, `
SELECT raw.role,
       CASE json_type(event.meta_json, '$.on_current_branch')
            WHEN 'true' THEN 1 WHEN 'false' THEN 0 ELSE -1 END
FROM l1_raw_record AS raw
JOIN l1_memory_event AS event ON event.id = raw.source_record_id
WHERE raw.owner_id = ? AND raw.scope = ? AND raw.source_type = ?
  AND raw.source_identity = ? AND raw.source_record_id = ?`,
		ownerID, "user:"+ownerID, chatGPTRawSourceType, exportID, evidenceID).Scan(&role, &branch)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil || !chatGPTConfirmValidRole(role) || (branch != 0 && branch != 1) {
		return false, chatGPTConfirmInternalError()
	}
	return role == "user" && branch == 1, nil
}

func chatGPTConfirmExplicitActiveNormalEvidence(meta map[string]interface{}) bool {
	active, ok := meta["active"].(bool)
	if !ok || !active {
		return false
	}
	sensitivity, ok := meta["sensitivity"].(string)
	return ok && strings.TrimSpace(sensitivity) == "normal"
}

func chatGPTConfirmEvidenceIDs(meta map[string]interface{}) ([]string, bool) {
	raw, ok := meta["evidence_event_ids"]
	if !ok {
		return nil, false
	}
	items, ok := raw.([]interface{})
	if !ok || len(items) == 0 {
		return nil, false
	}
	result := make([]string, 0, len(items))
	seen := make(map[string]struct{}, len(items))
	for _, rawID := range items {
		id, ok := rawID.(string)
		id = strings.TrimSpace(id)
		if !ok || id == "" {
			return nil, false
		}
		if _, exists := seen[id]; exists {
			return nil, false
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	return result, true
}

func chatGPTConfirmStringMeta(meta map[string]interface{}, key string) (string, bool) {
	value, ok := meta[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok && strings.TrimSpace(text) != ""
}

func chatGPTConfirmBoolMeta(meta map[string]interface{}, key string) (bool, bool) {
	value, ok := meta[key]
	if !ok {
		return false, false
	}
	result, ok := value.(bool)
	return result, ok
}

func chatGPTConfirmValidRole(role string) bool {
	switch role {
	case "user", "assistant", "system", "tool":
		return true
	default:
		return false
	}
}
