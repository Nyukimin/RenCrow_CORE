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
	"reflect"
	"sort"
	"strings"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

const (
	// ChatGPTRawContentType is the versioned media type of a ChatGPT adapter
	// payload stored in Common Raw. The original ChatGPT content type is kept
	// inside that payload and is not used as the Raw media type.
	ChatGPTRawContentType        = "application/vnd.rencrow.chatgpt-raw+json;v=1"
	ChatGPTRawProjectionType     = "chatgpt_l3"
	ChatGPTRawProjectionRevision = "chatgpt-l3/v1"
	chatGPTRawSourceType         = "chatgpt_export"
	chatGPTRawAdapterVersion     = "chatgpt-raw-adapter/v1"
)

type ChatGPTRawImportBatch = domainmemory.ChatGPTRawImportBatch
type ChatGPTRawImportResult = domainmemory.ChatGPTRawImportResult

type chatGPTRawBinding struct {
	Adapter          string `json:"adapter"`
	ManifestSHA256   string `json:"manifest_sha256"`
	ArtifactSHA256   string `json:"artifact_sha256"`
	SourceCount      int    `json:"source_count"`
	SchemaVersion    string `json:"schema_version"`
	ConverterVersion string `json:"converter_version"`
	BatchCount       int    `json:"batch_count"`
}

type chatGPTRawPlan struct {
	Batch       ChatGPTRawImportBatch
	ExportID    string
	Binding     chatGPTRawBinding
	Provenance  string
	Input       domainmemory.CommonRawIntakeRequest
	Prepared    preparedCommonRawIntake
	RecordsByID map[string]preparedCommonRawRecord
	ItemsByID   map[string]ChatGPTL3ImportRecord
	LineByID    map[string]int
}

type chatGPTRawProjectionRecord struct {
	RawRecordID string
	ContentHash string
	Item        ChatGPTL3ImportRecord
}

type chatGPTProjectionReceiptRow struct {
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
}

// ImportChatGPTRawBatch validates, durably intakes, and projects one
// ChatGPT batch. The legacy ImportChatGPTL3Records method is intentionally
// untouched for compatibility callers.
func (s *L1SQLiteStore) ImportChatGPTRawBatch(ctx context.Context, requestID, ownerID, actorID string, batch ChatGPTRawImportBatch, apply bool) (ChatGPTRawImportResult, error) {
	result := ChatGPTRawImportResult{
		Validated:              len(batch.Records),
		ExternalManifestSHA256: strings.TrimSpace(batch.ManifestSHA256),
		ArtifactSHA256:         strings.TrimSpace(batch.ArtifactSHA256),
		SourceCount:            batch.SourceCount,
		SchemaVersion:          strings.TrimSpace(batch.SchemaVersion),
		ConverterVersion:       strings.TrimSpace(batch.ConverterVersion),
		BatchIndex:             batch.BatchIndex,
		BatchCount:             batch.BatchCount,
		StartLine:              batch.StartLine,
		RawRecordIDs:           []string{},
	}
	if s == nil || s.db == nil {
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "conversation_l1 store is unavailable")
	}
	requestID = strings.TrimSpace(requestID)
	ownerID = strings.TrimSpace(ownerID)
	actorID = strings.TrimSpace(actorID)
	if requestID == "" || ownerID == "" || actorID == "" {
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "request, owner and actor identity are required")
	}
	if err := validateCommonRawOwnerScope(ctx, requestID, ownerID, actorID); err != nil {
		return result, err
	}
	plan, err := prepareChatGPTRawPlan(ownerID, batch)
	if err != nil {
		return result, err
	}
	result.Validated = len(plan.Prepared.records)
	result.ExternalManifestSHA256 = plan.Binding.ManifestSHA256
	result.ArtifactSHA256 = plan.Binding.ArtifactSHA256
	result.SourceCount = plan.Binding.SourceCount
	result.SchemaVersion = plan.Binding.SchemaVersion
	result.ConverterVersion = plan.Binding.ConverterVersion
	result.BatchCount = plan.Binding.BatchCount
	result.ManifestID = domainmemory.DeterministicCommonRawManifestID(ownerID, plan.Prepared.manifest.Scope, plan.Prepared.manifest.SourceType, plan.Prepared.manifest.SourceIdentity, plan.Prepared.manifestSHA256)
	result.InternalManifestSHA256 = plan.Prepared.manifestSHA256
	for _, record := range plan.Prepared.records {
		result.RawRecordIDs = append(result.RawRecordIDs, domainmemory.DeterministicCommonRawRecordID(ownerID, plan.Prepared.manifest.Scope, plan.Prepared.manifest.SourceType, plan.Prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash))
	}
	sort.Strings(result.RawRecordIDs)
	if err := s.verifyChatGPTRawBinding(ctx, ownerID, plan); err != nil {
		return result, err
	}
	if err := s.validateChatGPTProjectionPlan(ctx, ownerID, plan); err != nil {
		return result, err
	}
	if plan.Prepared.requiresObject && commonRawRootPathError(s.rawSourceRoot) != nil {
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}
	if !apply {
		return result, nil
	}

	receipt, err := s.IntakeCommonRaw(ctx, requestID, ownerID, actorID, plan.Input)
	if err != nil {
		return result, err
	}
	result.RawReceipt = receipt
	if receipt.IdempotentReplay {
		result.RawReplayed = len(receipt.Records)
	} else {
		result.RawImported = len(receipt.Records)
	}
	projectionRecords, err := s.readChatGPTRawProjectionRecords(ctx, ownerID, plan, receipt)
	if err != nil {
		return result, err
	}
	if err := s.commitChatGPTPendingReceipts(ctx, projectionRecords); err != nil {
		return result, err
	}
	projected, existing, queued, err := s.projectChatGPTRawRecords(ctx, ownerID, actorID, plan, projectionRecords)
	if err != nil {
		return result, err
	}
	result.Projected = projected
	result.Existing = existing
	result.Queued = queued
	return result, nil
}

func prepareChatGPTRawPlan(ownerID string, batch ChatGPTRawImportBatch) (chatGPTRawPlan, error) {
	if !validLowerSHA256Claim(batch.ManifestSHA256) || !validLowerSHA256Claim(batch.ArtifactSHA256) {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "external manifest and artifact sha256 claims must be lowercase 64-hex values")
	}
	if batch.SourceCount <= 0 {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "source_count must be positive")
	}
	if len(batch.Records) == 0 || len(batch.Records) > 100 {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "ChatGPT Raw import batch must contain 1..100 records")
	}
	// SourceCount binds the export's source-chunk total, while StartLine and
	// BatchCount index the verified message-line stream (54K+ lines for 1.7K
	// chunks in a real export). Coupling them rejected every production-sized
	// import; per-batch bounds stay enforced by the 1..100 record and payload
	// checks.
	if batch.BatchCount <= 0 || batch.BatchIndex < 0 || batch.BatchIndex >= batch.BatchCount {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "batch index/count is outside the source bounds")
	}
	if batch.StartLine < 1 {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "start_line and batch size are outside the source bounds")
	}
	if strings.TrimSpace(batch.SchemaVersion) == "" || batch.SchemaVersion != ChatGPTL3ArtifactFormat {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSchema, "ChatGPT schema version is unsupported")
	}
	if strings.TrimSpace(batch.ConverterVersion) == "" || batch.ConverterVersion != strings.TrimSpace(batch.ConverterVersion) {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "converter_version is required")
	}
	if strings.TrimSpace(ownerID) == "" {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "owner_id is required")
	}

	exportID := strings.TrimSpace(batch.ExportID)
	if batch.ExportID != exportID {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "export_id must not contain surrounding whitespace")
	}
	seen := make(map[string]struct{}, len(batch.Records))
	lineByID := make(map[string]int, len(batch.Records))
	for index, item := range batch.Records {
		if err := ValidateChatGPTL3ImportRecord(item); err != nil {
			return chatGPTRawPlan{}, fmt.Errorf("ChatGPT record %d is invalid: %w", index, err)
		}
		if item.ExportID != strings.TrimSpace(item.ExportID) {
			return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "record export_id must not contain surrounding whitespace")
		}
		if exportID == "" {
			exportID = item.ExportID
		}
		if item.ExportID != exportID {
			return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "record export_id does not match the batch export")
		}
		if _, exists := seen[item.EvidenceID]; exists {
			return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorConflict, "duplicate ChatGPT evidence/source record id")
		}
		seen[item.EvidenceID] = struct{}{}
		lineByID[item.EvidenceID] = batch.StartLine + index
	}
	if exportID == "" {
		return chatGPTRawPlan{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "export_id is required")
	}

	binding := chatGPTRawBinding{
		Adapter:          chatGPTRawAdapterVersion,
		ManifestSHA256:   batch.ManifestSHA256,
		ArtifactSHA256:   batch.ArtifactSHA256,
		SourceCount:      batch.SourceCount,
		SchemaVersion:    batch.SchemaVersion,
		ConverterVersion: batch.ConverterVersion,
		BatchCount:       batch.BatchCount,
	}
	provenanceBytes, err := json.Marshal(binding)
	if err != nil {
		return chatGPTRawPlan{}, fmt.Errorf("marshal ChatGPT Raw provenance: %w", err)
	}
	provenance := string(provenanceBytes)

	records := make([]domainmemory.CommonRawRecord, 0, len(batch.Records))
	for index, item := range batch.Records {
		payload, err := domainmemory.MarshalChatGPTRawPayload(batch, index)
		if err != nil {
			return chatGPTRawPlan{}, fmt.Errorf("canonicalize ChatGPT Raw record %s: %w", item.EvidenceID, err)
		}
		occurredAt := chatGPTRawOccurredAt(item)
		records = append(records, domainmemory.CommonRawRecord{
			SourceRecordID: item.EvidenceID,
			ParentID:       item.ParentNodeID,
			ThreadID:       item.ConversationID,
			Sensitivity:    domainmemory.CommonRawPrivateSensitivity,
			Role:           item.Role,
			ContentType:    ChatGPTRawContentType,
			OccurredAt:     occurredAt,
			Content:        payload,
			ContentSHA256:  domainmemory.SHA256Hex(payload),
			Provenance:     provenance,
			Rights:         "owner",
			License:        "private",
		})
	}
	manifest := domainmemory.CommonRawManifest{
		ContractVersion:  domainmemory.CommonRawContractVersion,
		SourceType:       chatGPTRawSourceType,
		SourceIdentity:   exportID,
		SourceCount:      len(records),
		AssetCount:       0,
		SchemaVersion:    batch.SchemaVersion,
		ConverterVersion: batch.ConverterVersion,
		Sensitivity:      domainmemory.CommonRawPrivateSensitivity,
		Rights:           "owner",
		License:          "private",
		Provenance:       provenance,
		AllowEmpty:       false,
	}
	manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, records, nil)
	if err != nil {
		return chatGPTRawPlan{}, fmt.Errorf("compute ChatGPT Raw input hash: %w", err)
	}
	input := domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: records}
	prepared, err := prepareCommonRawIntake(ownerID, input)
	if err != nil {
		return chatGPTRawPlan{}, err
	}
	recordsByID := make(map[string]preparedCommonRawRecord, len(prepared.records))
	for _, record := range prepared.records {
		recordsByID[record.input.SourceRecordID] = record
	}
	itemsByID := make(map[string]ChatGPTL3ImportRecord, len(batch.Records))
	for _, item := range batch.Records {
		itemsByID[item.EvidenceID] = item
	}
	return chatGPTRawPlan{Batch: batch, ExportID: exportID, Binding: binding, Provenance: provenance, Input: input, Prepared: prepared, RecordsByID: recordsByID, ItemsByID: itemsByID, LineByID: lineByID}, nil
}

func validLowerSHA256Claim(value string) bool {
	if len(value) != sha256.Size*2 || value != strings.ToLower(value) {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func chatGPTRawOccurredAt(item ChatGPTL3ImportRecord) time.Time {
	for _, value := range []time.Time{item.MessageCreatedAt, item.ConversationCreatedAt, item.ConversationUpdatedAt} {
		if !value.IsZero() {
			return value.UTC()
		}
	}
	return time.Unix(0, 0).UTC()
}

func (s *L1SQLiteStore) verifyChatGPTRawBinding(ctx context.Context, ownerID string, plan chatGPTRawPlan) error {
	rows, err := s.db.QueryContext(ctx, `
SELECT manifest_id, source_count, schema_version, converter_version, provenance
FROM l1_raw_source_manifest
WHERE owner_id = ? AND scope = ? AND source_type = ? AND source_identity = ?
ORDER BY manifest_id ASC`, ownerID, "user:"+ownerID, chatGPTRawSourceType, plan.ExportID)
	if err != nil {
		return fmt.Errorf("read existing ChatGPT Raw binding: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var manifestID, schemaVersion, converterVersion, provenance string
		var sourceCount int
		if err := rows.Scan(&manifestID, &sourceCount, &schemaVersion, &converterVersion, &provenance); err != nil {
			return fmt.Errorf("scan existing ChatGPT Raw binding: %w", err)
		}
		var stored chatGPTRawBinding
		if err := json.Unmarshal([]byte(provenance), &stored); err != nil || stored.Adapter != chatGPTRawAdapterVersion {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw provenance is malformed")
		}
		if sourceCount <= 0 || schemaVersion == "" || converterVersion == "" || stored != plan.Binding {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "ChatGPT Raw export binding changed")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate existing ChatGPT Raw binding: %w", err)
	}
	return nil
}

func (s *L1SQLiteStore) validateChatGPTProjectionPlan(ctx context.Context, ownerID string, plan chatGPTRawPlan) error {
	// Validate any existing output and eligible promotion job without writing.
	// If the Raw manifest is already present, IntakeCommonRaw will repeat the
	// full immutable check on apply; this read path proves that replay/backfill
	// will not accept a changed legacy output.
	for _, record := range plan.Prepared.records {
		item, ok := plan.ItemsByID[record.input.SourceRecordID]
		if !ok {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT projection plan record is missing")
		}
		rawRecordID := domainmemory.DeterministicCommonRawRecordID(ownerID, plan.Prepared.manifest.Scope, chatGPTRawSourceType, plan.ExportID, record.input.SourceRecordID, record.hash)
		projectionRecord := chatGPTRawProjectionRecord{RawRecordID: rawRecordID, ContentHash: record.hash, Item: item}
		receipts, err := loadChatGPTProjectionReceipt(ctx, s.db, item.EvidenceID)
		if err != nil {
			return fmt.Errorf("validate ChatGPT projection receipt history: %w", err)
		}
		if err := validateChatGPTProjectionReceiptHistory(receipts, projectionRecord); err != nil {
			return err
		}

		var count int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM l1_memory_event WHERE id = ?`, record.input.SourceRecordID).Scan(&count); err != nil {
			return fmt.Errorf("validate ChatGPT projection plan: %w", err)
		}
		if count > 1 {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "duplicate ChatGPT L3 output row")
		}
		if count == 1 {
			event, err := loadChatGPTEventQueryer(ctx, s.db, item.EvidenceID)
			if err != nil {
				return err
			}
			if err := validateChatGPTLegacyEvent(event, item); err != nil {
				return err
			}
			completedID := chatGPTRawProjectionReceiptID("completed", rawRecordID)
			if completed, found := receipts[completedID]; found {
				outputHash, err := CanonicalL1MemoryEventSHA256(*event)
				if err != nil {
					return fmt.Errorf("hash persisted ChatGPT L3 output: %w", err)
				}
				if err := verifyChatGPTProjectionReceipt(completed, projectionRecord, "completed", outputHash); err != nil {
					return err
				}
			}
		} else if _, found := receipts[chatGPTRawProjectionReceiptID("completed", rawRecordID)]; found {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "completed ChatGPT projection receipt has no output row")
		}
		if item.Role == "user" && item.OnCurrentBranch {
			var sessionID, threadID, threadKind, state string
			var threadSeq int64
			err := s.db.QueryRowContext(ctx, `SELECT session_id, thread_id, thread_seq, thread_kind, state FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, item.EvidenceID).Scan(&sessionID, &threadID, &threadSeq, &threadKind, &state)
			if err == nil {
				expectedSessionID := chatGPTConversationSessionID(item.ConversationID)
				if sessionID != string(expectedSessionID) || modulecore.ThreadID(threadID) != chatGPTConversationThreadID(item.ConversationID) || modulecore.ThreadSeq(threadSeq) != 1 || modulecore.ThreadKind(threadKind) != modulecore.ThreadKindUserConversation || !validProfilePromotionState(state) {
					return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT promotion job is inconsistent")
				}
			} else if !errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("validate ChatGPT promotion job: %w", err)
			}
		}
	}
	return nil
}

func (s *L1SQLiteStore) readChatGPTRawProjectionRecords(ctx context.Context, ownerID string, plan chatGPTRawPlan, receipt domainmemory.CommonRawIntakeReceipt) ([]chatGPTRawProjectionRecord, error) {
	if receipt.ManifestID == "" || receipt.Status != domainmemory.CommonRawStateCompleted || len(receipt.Records) != len(plan.Prepared.records) {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "Common Raw receipt does not bind to the ChatGPT batch")
	}
	bySourceID := make(map[string]domainmemory.CommonRawRecordReceipt, len(receipt.Records))
	for _, rawReceipt := range receipt.Records {
		if _, exists := bySourceID[rawReceipt.SourceRecordID]; exists {
			return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "Common Raw receipt contains duplicate source records")
		}
		bySourceID[rawReceipt.SourceRecordID] = rawReceipt
	}
	result := make([]chatGPTRawProjectionRecord, 0, len(receipt.Records))
	for _, prepared := range plan.Prepared.records {
		rawReceipt, ok := bySourceID[prepared.input.SourceRecordID]
		if !ok || rawReceipt.RawRecordID != domainmemory.DeterministicCommonRawRecordID(ownerID, plan.Prepared.manifest.Scope, chatGPTRawSourceType, plan.ExportID, prepared.input.SourceRecordID, prepared.hash) || rawReceipt.ContentSHA256 != prepared.hash {
			return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "Common Raw receipt record binding differs from the batch")
		}
		item, contentHash, err := s.readChatGPTRawPayload(ctx, ownerID, plan, rawReceipt)
		if err != nil {
			return nil, err
		}
		if contentHash != rawReceipt.ContentSHA256 {
			return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "Common Raw payload hash differs from its receipt")
		}
		result = append(result, chatGPTRawProjectionRecord{RawRecordID: rawReceipt.RawRecordID, ContentHash: contentHash, Item: item})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Item.EvidenceID < result[j].Item.EvidenceID })
	return result, nil
}

func (s *L1SQLiteStore) readChatGPTRawPayload(ctx context.Context, ownerID string, plan chatGPTRawPlan, receipt domainmemory.CommonRawRecordReceipt) (ChatGPTL3ImportRecord, string, error) {
	var manifestID, sourceType, sourceIdentity, sourceRecordID, parentID, threadID, scope, sensitivity, role, contentType, storageKind, objectRef, contentHash, provenance, rights, license, assetRefsJSON string
	var occurredAt time.Time
	var contentSize int64
	var inlinePayload []byte
	err := s.db.QueryRowContext(ctx, `
SELECT manifest_id, source_type, source_identity, source_record_id, parent_id, thread_id,
       scope, sensitivity, role, content_type, occurred_at, storage_kind, inline_payload,
       object_ref, content_sha256, content_size, provenance, rights, license, asset_refs_json
FROM l1_raw_record WHERE raw_record_id = ?`, receipt.RawRecordID).Scan(
		&manifestID, &sourceType, &sourceIdentity, &sourceRecordID, &parentID, &threadID,
		&scope, &sensitivity, &role, &contentType, &occurredAt, &storageKind, &inlinePayload,
		&objectRef, &contentHash, &contentSize, &provenance, &rights, &license, &assetRefsJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw record is missing")
	}
	if err != nil {
		return ChatGPTL3ImportRecord{}, "", fmt.Errorf("read stored ChatGPT Raw record: %w", err)
	}
	if manifestID != domainmemory.DeterministicCommonRawManifestID(ownerID, plan.Prepared.manifest.Scope, chatGPTRawSourceType, plan.ExportID, plan.Prepared.manifestSHA256) || sourceType != chatGPTRawSourceType || sourceIdentity != plan.ExportID || sourceRecordID != receipt.SourceRecordID || scope != plan.Prepared.manifest.Scope || sensitivity != domainmemory.CommonRawPrivateSensitivity || contentType != ChatGPTRawContentType || role == "" || parentID != plan.RecordsByID[receipt.SourceRecordID].input.ParentID || threadID != plan.RecordsByID[receipt.SourceRecordID].input.ThreadID || provenance != plan.Provenance || rights != "owner" || license != "private" || assetRefsJSON != "[]" || contentHash != receipt.ContentSHA256 || contentSize != receipt.ContentSize || storageKind != receipt.StorageKind || objectRef != receipt.ObjectRef {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw record binding is inconsistent")
	}
	var content []byte
	switch storageKind {
	case domainmemory.CommonRawStorageInline:
		content = inlinePayload
	case domainmemory.CommonRawStorageObject:
		if err := verifyCommonRawStoredObject(s.rawSourceRoot, objectRef, contentSize, contentHash); err != nil {
			return ChatGPTL3ImportRecord{}, "", err
		}
		path, err := commonRawStoredObjectPath(s.rawSourceRoot, objectRef)
		if err != nil {
			return ChatGPTL3ImportRecord{}, "", err
		}
		content, err = os.ReadFile(path)
		if err != nil {
			return ChatGPTL3ImportRecord{}, "", fmt.Errorf("read stored ChatGPT Raw object: %w", err)
		}
	default:
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw storage kind is unsupported")
	}
	if int64(len(content)) != contentSize || domainmemory.SHA256Hex(content) != contentHash {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw payload hash is invalid")
	}
	var payload struct {
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
	if err := json.Unmarshal(content, &payload); err != nil {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw payload is malformed")
	}
	canonical, err := json.Marshal(payload)
	if err != nil || string(canonical) != string(content) {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw payload is not canonical")
	}
	line := plan.LineByID[receipt.SourceRecordID]
	if payload.Format != ChatGPTL3ArtifactFormat || payload.ExportID != plan.ExportID || payload.EvidenceID != receipt.SourceRecordID || payload.ManifestSHA256 != plan.Binding.ManifestSHA256 || payload.ArtifactSHA256 != plan.Binding.ArtifactSHA256 || payload.SourceCount != plan.Binding.SourceCount || payload.SchemaVersion != plan.Binding.SchemaVersion || payload.ConverterVersion != plan.Binding.ConverterVersion || payload.BatchIndex != plan.Batch.BatchIndex || payload.BatchCount != plan.Binding.BatchCount || payload.StartLine != plan.Batch.StartLine || payload.ArtifactLine != line || payload.ConversationID != threadID || payload.ParentNodeID != parentID || payload.Role != role || !occurredAt.Equal(chatGPTRawOccurredAt(ChatGPTL3ImportRecord{MessageCreatedAt: payload.MessageCreatedAt, ConversationCreatedAt: payload.ConversationCreatedAt, ConversationUpdatedAt: payload.ConversationUpdatedAt})) {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw payload binding is inconsistent")
	}
	item := ChatGPTL3ImportRecord{
		Format:                payload.Format,
		ExportID:              payload.ExportID,
		EvidenceID:            payload.EvidenceID,
		ConversationID:        payload.ConversationID,
		ConversationTitle:     payload.ConversationTitle,
		ConversationCreatedAt: payload.ConversationCreatedAt,
		ConversationUpdatedAt: payload.ConversationUpdatedAt,
		NodeID:                payload.NodeID,
		ParentNodeID:          payload.ParentNodeID,
		ChildNodeIDs:          payload.ChildNodeIDs,
		OnCurrentBranch:       payload.OnCurrentBranch,
		MessageID:             payload.MessageID,
		MessageCreatedAt:      payload.MessageCreatedAt,
		Role:                  payload.Role,
		ContentType:           payload.ContentType,
		Text:                  payload.Text,
		Content:               payload.Content,
		Metadata:              payload.Metadata,
	}
	if err := ValidateChatGPTL3ImportRecord(item); err != nil {
		return ChatGPTL3ImportRecord{}, "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "stored ChatGPT Raw payload fails legacy validation")
	}
	return item, contentHash, nil
}

func (s *L1SQLiteStore) commitChatGPTPendingReceipts(ctx context.Context, records []chatGPTRawProjectionRecord) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	for _, record := range records {
		existing, err := loadChatGPTProjectionReceipt(ctx, tx, record.Item.EvidenceID)
		if err != nil {
			return rollbackL1Tx(tx, err)
		}
		pendingID := chatGPTRawProjectionReceiptID("pending", record.RawRecordID)
		_, pendingFound := existing[pendingID]
		if err := validateChatGPTProjectionReceiptHistory(existing, record); err != nil {
			return rollbackL1Tx(tx, err)
		}
		if !pendingFound {
			_, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_raw_projection_receipt (
 projection_receipt_id, projection_type, output_store, output_record_id,
 raw_record_ids_json, revision, input_sha256, output_sha256, status,
 created_at, updated_at, failure_reason
) VALUES (?, ?, ?, ?, ?, ?, ?, '', 'pending', ?, ?, '')`,

				pendingID, ChatGPTRawProjectionType, "conversation_l1", record.Item.EvidenceID,
				chatGPTProjectionRawIDsJSON(record.RawRecordID), ChatGPTRawProjectionRevision,
				record.ContentHash, time.Now().UTC(), time.Now().UTC())
			if err != nil {
				return rollbackL1Tx(tx, fmt.Errorf("insert ChatGPT pending projection receipt: %w", err))
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return rollbackL1Tx(tx, err)
	}
	return nil
}

func (s *L1SQLiteStore) projectChatGPTRawRecords(ctx context.Context, ownerID, actorID string, plan chatGPTRawPlan, records []chatGPTRawProjectionRecord) (int, int, int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, 0, err
	}
	for _, record := range records {
		receipts, err := loadChatGPTProjectionReceipt(ctx, tx, record.Item.EvidenceID)
		if err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, err)
		}
		pendingID := chatGPTRawProjectionReceiptID("pending", record.RawRecordID)
		completedID := chatGPTRawProjectionReceiptID("completed", record.RawRecordID)
		for receiptID := range receipts {
			if receiptID != pendingID && receiptID != completedID {
				return 0, 0, 0, rollbackL1Tx(tx, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT projection receipt history is inconsistent"))
			}
		}
		pending, found := receipts[pendingID]
		if !found {
			return 0, 0, 0, rollbackL1Tx(tx, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "pending ChatGPT projection receipt is required"))
		}
		if err := verifyChatGPTProjectionReceipt(pending, record, "pending", ""); err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, err)
		}
		if completed, found := receipts[completedID]; found {
			if err := verifyChatGPTProjectionReceipt(completed, record, "completed", "*stored*"); err != nil {
				return 0, 0, 0, rollbackL1Tx(tx, err)
			}
		}
	}
	projected, existing, queued := 0, 0, 0
	completedAdded := 0
	for _, record := range records {
		event, inserted, err := ensureChatGPTLegacyEventTx(ctx, tx, record.Item)
		if err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, err)
		}
		if inserted {
			projected++
		} else {
			existing++
		}
		jobCreated, err := ensureChatGPTPromotionJobTx(ctx, tx, ownerID, record.Item)
		if err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, err)
		}
		if jobCreated {
			queued++
		}
		outputHash, err := CanonicalL1MemoryEventSHA256(*event)
		if err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, fmt.Errorf("hash persisted ChatGPT L3 output: %w", err))
		}
		receipts, err := loadChatGPTProjectionReceipt(ctx, tx, record.Item.EvidenceID)
		if err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, err)
		}
		completedID := chatGPTRawProjectionReceiptID("completed", record.RawRecordID)
		if completed, found := receipts[completedID]; found {
			if err := verifyChatGPTProjectionReceipt(completed, record, "completed", outputHash); err != nil {
				return 0, 0, 0, rollbackL1Tx(tx, err)
			}
		} else {
			completedInsert, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_raw_projection_receipt (
 projection_receipt_id, projection_type, output_store, output_record_id,
 raw_record_ids_json, revision, input_sha256, output_sha256, status,
 created_at, updated_at, failure_reason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'completed', ?, ?, '')`,
				completedID, ChatGPTRawProjectionType, "conversation_l1", record.Item.EvidenceID,
				chatGPTProjectionRawIDsJSON(record.RawRecordID), ChatGPTRawProjectionRevision,
				record.ContentHash, outputHash, time.Now().UTC(), time.Now().UTC())
			if err != nil {
				return 0, 0, 0, rollbackL1Tx(tx, fmt.Errorf("insert ChatGPT completed projection receipt: %w", err))
			}
			affected, err := completedInsert.RowsAffected()
			if err != nil {
				return 0, 0, 0, rollbackL1Tx(tx, err)
			}
			if affected == 1 {
				completedAdded++
			} else {
				reloaded, loadErr := loadChatGPTProjectionReceipt(ctx, tx, record.Item.EvidenceID)
				if loadErr != nil {
					return 0, 0, 0, rollbackL1Tx(tx, loadErr)
				}
				stored, ok := reloaded[completedID]
				if !ok {
					return 0, 0, 0, rollbackL1Tx(tx, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "completed ChatGPT projection receipt disappeared"))
				}
				if err := verifyChatGPTProjectionReceipt(stored, record, "completed", outputHash); err != nil {
					return 0, 0, 0, rollbackL1Tx(tx, err)
				}
			}
		}
	}
	if completedAdded > 0 {
		rawIDs := make([]string, 0, len(records))
		for _, record := range records {
			rawIDs = append(rawIDs, record.RawRecordID)
		}
		sort.Strings(rawIDs)
		if _, err := appendL1EventLog(ctx, tx, "memory.chatgpt_raw_l3_projected", "user:"+ownerID, "", "", 0, "", map[string]interface{}{
			"owner_id":              ownerID,
			"actor_id":              actorID,
			"source_type":           chatGPTRawSourceType,
			"source_identity":       plan.ExportID,
			"manifest_sha256":       plan.Binding.ManifestSHA256,
			"artifact_sha256":       plan.Binding.ArtifactSHA256,
			"source_count":          plan.Binding.SourceCount,
			"batch_index":           plan.Batch.BatchIndex,
			"batch_count":           plan.Binding.BatchCount,
			"raw_record_ids":        rawIDs,
			"projected":             projected,
			"existing":              existing,
			"queued_for_projection": queued,
		}, "chatgpt_raw_import"); err != nil {
			return 0, 0, 0, rollbackL1Tx(tx, fmt.Errorf("append ChatGPT Raw projection audit: %w", err))
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, 0, 0, rollbackL1Tx(tx, err)
	}
	return projected, existing, queued, nil
}

func chatGPTRawProjectionReceiptID(status, rawRecordID string) string {
	digest := sha256.Sum256([]byte(ChatGPTRawProjectionRevision + "\x00" + status + "\x00" + rawRecordID))
	return "chatgpt-projection:" + status + ":" + hex.EncodeToString(digest[:])
}

func chatGPTProjectionRawIDsJSON(rawRecordID string) string {
	encoded, _ := json.Marshal([]string{rawRecordID})
	return string(encoded)
}

func loadChatGPTProjectionReceipt(ctx context.Context, queryer interface {
	QueryContext(context.Context, string, ...interface{}) (*sql.Rows, error)
}, outputID string) (map[string]chatGPTProjectionReceiptRow, error) {
	rows, err := queryer.QueryContext(ctx, `
SELECT projection_receipt_id, projection_type, output_store, output_record_id,
       raw_record_ids_json, revision, input_sha256, output_sha256, status,
       created_at, updated_at, failure_reason
FROM l1_raw_projection_receipt
WHERE output_record_id = ?
ORDER BY projection_receipt_id ASC`, outputID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make(map[string]chatGPTProjectionReceiptRow)
	for rows.Next() {
		var row chatGPTProjectionReceiptRow
		if err := rows.Scan(&row.ID, &row.Projection, &row.OutputStore, &row.OutputID, &row.RawIDsJSON, &row.Revision, &row.InputSHA256, &row.OutputSHA256, &row.Status, &row.CreatedAt, &row.UpdatedAt, &row.Failure); err != nil {
			return nil, err
		}
		if _, exists := result[row.ID]; exists {
			return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "duplicate ChatGPT projection receipt ID")
		}
		result[row.ID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func validateChatGPTProjectionReceiptHistory(receipts map[string]chatGPTProjectionReceiptRow, record chatGPTRawProjectionRecord) error {
	pendingID := chatGPTRawProjectionReceiptID("pending", record.RawRecordID)
	completedID := chatGPTRawProjectionReceiptID("completed", record.RawRecordID)
	for receiptID := range receipts {
		if receiptID != pendingID && receiptID != completedID {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT projection receipt history belongs to another Raw record")
		}
	}
	pending, pendingFound := receipts[pendingID]
	completed, completedFound := receipts[completedID]
	if completedFound && !pendingFound {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "completed ChatGPT projection receipt requires its exact pending receipt")
	}
	if pendingFound {
		if err := verifyChatGPTProjectionReceipt(pending, record, "pending", ""); err != nil {
			return err
		}
	}
	if completedFound {
		if err := verifyChatGPTProjectionReceipt(completed, record, "completed", "*stored*"); err != nil {
			return err
		}
	}
	return nil
}

func verifyChatGPTProjectionReceipt(row chatGPTProjectionReceiptRow, record chatGPTRawProjectionRecord, status, outputHash string) error {
	if row.ID != chatGPTRawProjectionReceiptID(status, record.RawRecordID) || row.Projection != ChatGPTRawProjectionType || row.OutputStore != "conversation_l1" || row.OutputID != record.Item.EvidenceID || row.RawIDsJSON != chatGPTProjectionRawIDsJSON(record.RawRecordID) || row.Revision != ChatGPTRawProjectionRevision || row.InputSHA256 != record.ContentHash || row.Status != status || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() || row.Failure != "" {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT projection receipt binding is inconsistent")
	}
	if status == "pending" {
		if row.OutputSHA256 != "" {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "pending ChatGPT projection receipt has an output hash")
		}
	} else if outputHash != "*stored*" && row.OutputSHA256 != outputHash {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "completed ChatGPT projection receipt output hash differs from persisted output")
	} else if !validLowerSHA256Claim(row.OutputSHA256) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "completed ChatGPT projection receipt output hash is invalid")
	}
	return nil
}

func ensureChatGPTLegacyEventTx(ctx context.Context, tx *sql.Tx, item ChatGPTL3ImportRecord) (*L1MemoryEvent, bool, error) {
	expected, err := expectedChatGPTLegacyEvent(item)
	if err != nil {
		return nil, false, err
	}
	insertResult, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_memory_event (
	id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
	memory_state, layer, source, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		item.EvidenceID, expected.namespace, expected.sessionID, expected.threadID, expected.threadSeq, expected.threadKind, string(expected.speaker), expected.message, expected.metaJSON,
		MemoryStateObserved, "L3", chatGPTRawSourceType, expected.occurredAt, time.Now().UTC())
	if err != nil {
		return nil, false, fmt.Errorf("insert ChatGPT L3 output: %w", err)
	}
	affected, err := insertResult.RowsAffected()
	if err != nil {
		return nil, false, fmt.Errorf("inspect ChatGPT L3 output insert: %w", err)
	}
	event, err := loadChatGPTEventTx(ctx, tx, item.EvidenceID)
	if err != nil {
		return nil, false, err
	}
	if err := validateChatGPTLegacyEvent(event, item); err != nil {
		return nil, false, err
	}
	// A legacy row can have the same created_at as the Raw record; use the
	// SQLite change count rather than timestamps to distinguish backfill from
	// a newly projected row.
	return event, affected == 1, nil
}

func loadChatGPTEventTx(ctx context.Context, tx *sql.Tx, eventID string) (*L1MemoryEvent, error) {
	return loadChatGPTEventQueryer(ctx, tx, eventID)
}

type chatGPTSQLRowQueryer interface {
	QueryRowContext(context.Context, string, ...interface{}) *sql.Row
}

func loadChatGPTEventQueryer(ctx context.Context, queryer chatGPTSQLRowQueryer, eventID string) (*L1MemoryEvent, error) {
	events, err := scanL1EventRows(queryer.QueryRowContext(ctx, `
SELECT id, namespace, session_id, thread_id, thread_seq, thread_kind, speaker, message, meta_json,
       memory_state, layer, source, created_at, updated_at
FROM l1_memory_event WHERE id = ?`, eventID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "ChatGPT L3 output row disappeared")
	}
	if err != nil {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 row is malformed")
	}
	return &events[0], nil
}

type chatGPTLegacyEventExpectation struct {
	namespace   string
	sessionID   string
	threadID    modulecore.ThreadID
	threadSeq   modulecore.ThreadSeq
	threadKind  modulecore.ThreadKind
	speaker     domconv.Speaker
	message     string
	metaJSON    string
	contentHash string
	occurredAt  time.Time
}

func expectedChatGPTLegacyEvent(item ChatGPTL3ImportRecord) (chatGPTLegacyEventExpectation, error) {
	namespace := chatGPTConversationNamespace(item.ConversationID)
	sessionID := chatGPTConversationSessionID(item.ConversationID)
	threadID := chatGPTConversationThreadID(item.ConversationID)
	if sessionID == "" || threadID == "" || namespace == "" {
		return chatGPTLegacyEventExpectation{}, errors.New("ChatGPT conversation canonical identity is unavailable")
	}
	message := strings.TrimSpace(item.Text)
	if message == "" {
		message = "[non-text ChatGPT content: " + firstNonEmptyString(item.ContentType, "unknown") + "]"
	}
	contentHash := chatGPTRecordContentHash(item)
	metaJSON, err := marshalL1MetaJSON(chatGPTLegacyMeta(item, contentHash), "failed to marshal ChatGPT L3 metadata")
	if err != nil {
		return chatGPTLegacyEventExpectation{}, err
	}
	return chatGPTLegacyEventExpectation{
		namespace:   namespace,
		sessionID:   string(sessionID),
		threadID:    threadID,
		threadSeq:   1,
		threadKind:  modulecore.ThreadKindUserConversation,
		speaker:     chatGPTRawSpeaker(item.Role),
		message:     message,
		metaJSON:    metaJSON,
		contentHash: contentHash,
		occurredAt:  chatGPTRawOccurredAt(item),
	}, nil
}

func validateChatGPTLegacyEvent(event *L1MemoryEvent, item ChatGPTL3ImportRecord) error {
	expected, err := expectedChatGPTLegacyEvent(item)
	if err != nil {
		return err
	}
	if event.ID != item.EvidenceID || event.Namespace != expected.namespace || event.SessionID != expected.sessionID || event.ThreadID != expected.threadID || event.ThreadSeq != expected.threadSeq || event.ThreadKind != expected.threadKind || event.Speaker != expected.speaker || event.Message != expected.message || event.MemoryState != MemoryStateObserved || event.Layer != "L3" || event.Source != chatGPTRawSourceType {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 row binding differs from Raw")
	}
	return validateChatGPTLegacyMeta(event.Meta, expected)
}

var chatGPTLegacyRequiredMetaKeys = []string{
	"external_source", "export_id", "conversation_id", "message_id", "node_id", "content_sha256",
}

var chatGPTLegacyOptionalMetaKeys = []string{
	"conversation_title", "parent_node_id", "child_node_ids", "on_current_branch",
	"original_role", "content_type", "artifact_content", "artifact_metadata",
}

var chatGPTLegacyForbiddenMetaKeys = map[string]struct{}{
	"owner_id": {}, "user_id": {}, "scope": {}, "evidence_event_ids": {},
}

func validateChatGPTLegacyMeta(actual map[string]interface{}, expected chatGPTLegacyEventExpectation) error {
	var expectedMap map[string]interface{}
	if err := json.Unmarshal([]byte(expected.metaJSON), &expectedMap); err != nil {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "expected ChatGPT L3 metadata is malformed")
	}
	actualNorm, err := normalizeChatGPTLegacyMeta(actual)
	if err != nil {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 metadata is malformed")
	}
	expectedNorm, err := normalizeChatGPTLegacyMeta(expectedMap)
	if err != nil {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "expected ChatGPT L3 metadata is malformed")
	}
	actualHash := strings.TrimSpace(fmt.Sprint(actualNorm["content_sha256"]))
	if actualHash != expected.contentHash {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "persisted ChatGPT L3 content hash differs from Raw")
	}
	known := make(map[string]struct{}, len(chatGPTLegacyRequiredMetaKeys)+len(chatGPTLegacyOptionalMetaKeys))
	for _, key := range chatGPTLegacyRequiredMetaKeys {
		known[key] = struct{}{}
		if _, ok := actualNorm[key]; !ok {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 metadata is missing identity field "+key)
		}
		if !jsonValuesEqual(actualNorm[key], expectedNorm[key]) {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 metadata identity differs from Raw")
		}
	}
	for _, key := range chatGPTLegacyOptionalMetaKeys {
		known[key] = struct{}{}
		if _, ok := actualNorm[key]; !ok {
			continue
		}
		if !jsonValuesEqual(actualNorm[key], expectedNorm[key]) {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 metadata differs from Raw")
		}
	}
	for key := range actualNorm {
		if _, ok := known[key]; ok {
			continue
		}
		if _, forbidden := chatGPTLegacyForbiddenMetaKeys[key]; forbidden {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT L3 metadata contains forbidden identity field")
		}
	}
	return nil
}

func normalizeChatGPTLegacyMeta(meta map[string]interface{}) (map[string]interface{}, error) {
	if meta == nil {
		return map[string]interface{}{}, nil
	}
	encoded, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	var decoded map[string]interface{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, err
	}
	if decoded == nil {
		return map[string]interface{}{}, nil
	}
	return decoded, nil
}

func jsonValuesEqual(left, right interface{}) bool {
	return reflect.DeepEqual(decodeJSONValue(left), decodeJSONValue(right))
}

func decodeJSONValue(value interface{}) interface{} {
	switch typed := value.(type) {
	case json.RawMessage:
		if len(typed) == 0 {
			return nil
		}
		var decoded interface{}
		if err := json.Unmarshal(typed, &decoded); err != nil {
			return string(typed)
		}
		return decoded
	default:
		encoded, err := json.Marshal(value)
		if err != nil {
			return value
		}
		var decoded interface{}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			return value
		}
		return decoded
	}
}

func chatGPTLegacyMeta(item ChatGPTL3ImportRecord, contentHash string) map[string]interface{} {
	return map[string]interface{}{
		"external_source": "chatgpt_export", "export_id": item.ExportID,
		"conversation_id": item.ConversationID, "conversation_title": item.ConversationTitle,
		"node_id": item.NodeID, "parent_node_id": item.ParentNodeID,
		"child_node_ids": item.ChildNodeIDs, "on_current_branch": item.OnCurrentBranch,
		"message_id": item.MessageID, "original_role": item.Role,
		"content_type": item.ContentType, "content_sha256": contentHash,
		"artifact_content": json.RawMessage(item.Content), "artifact_metadata": json.RawMessage(item.Metadata),
	}
}

func chatGPTRawSpeaker(role string) domconv.Speaker {
	switch role {
	case "user":
		return domconv.SpeakerUser
	case "tool":
		return domconv.SpeakerTool
	default:
		return domconv.SpeakerSystem
	}
}

func ensureChatGPTPromotionJobTx(ctx context.Context, tx *sql.Tx, ownerID string, item ChatGPTL3ImportRecord) (bool, error) {
	if item.Role != "user" || !item.OnCurrentBranch {
		return false, nil
	}
	sessionID := chatGPTConversationSessionID(item.ConversationID)
	threadID := chatGPTConversationThreadID(item.ConversationID)
	if sessionID == "" || threadID == "" {
		return false, errors.New("ChatGPT conversation canonical identity is unavailable")
	}
	var existingSession, existingState string
	var existingThread, existingKind string
	var existingSeq int64
	err := tx.QueryRowContext(ctx, `SELECT session_id, thread_id, thread_seq, thread_kind, state FROM l1_profile_promotion_job WHERE evidence_event_id = ?`, item.EvidenceID).Scan(&existingSession, &existingThread, &existingSeq, &existingKind, &existingState)
	if errors.Is(err, sql.ErrNoRows) {
		createdAt := chatGPTRawOccurredAt(item)
		completedInsert, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_profile_promotion_job (
	 evidence_event_id, session_id, thread_id, thread_seq, thread_kind, state, attempt_count,
	 lease_token, last_error, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, 0, '', '', ?, ?)`, item.EvidenceID, string(sessionID), threadID, 1, modulecore.ThreadKindUserConversation, domainmemory.ProfilePromotionPending, createdAt, time.Now().UTC())
		if err != nil {
			return false, fmt.Errorf("queue ChatGPT profile promotion: %w", err)
		}
		affected, err := completedInsert.RowsAffected()
		if err != nil {
			return false, err
		}
		if err := ensureChatGPTProfilePromotionBindingTx(ctx, tx, ownerID, item.ExportID, item.EvidenceID); err != nil {
			return false, err
		}
		return affected == 1, nil
	}
	if err != nil {
		return false, err
	}
	if existingSession != string(sessionID) || modulecore.ThreadID(existingThread) != threadID || modulecore.ThreadSeq(existingSeq) != 1 || modulecore.ThreadKind(existingKind) != modulecore.ThreadKindUserConversation || !validProfilePromotionState(existingState) {
		return false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "persisted ChatGPT promotion job is inconsistent")
	}
	if err := ensureChatGPTProfilePromotionBindingTx(ctx, tx, ownerID, item.ExportID, item.EvidenceID); err != nil {
		return false, err
	}
	return false, nil
}

func validProfilePromotionState(state string) bool {
	switch state {
	case domainmemory.ProfilePromotionPending, domainmemory.ProfilePromotionRunning, domainmemory.ProfilePromotionRetryWait, domainmemory.ProfilePromotionFailed, domainmemory.ProfilePromotionCompleted:
		return true
	default:
		return false
	}
}
