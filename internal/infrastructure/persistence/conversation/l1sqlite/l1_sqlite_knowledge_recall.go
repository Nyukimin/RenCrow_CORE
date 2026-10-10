package l1sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

// SearchKnowledgeItemsForCategoryRecall searches and verifies eligible quote
// sources inside one read transaction. Query scope is never an authorization
// input; private Raw access comes only from ToolExecutionScope in ctx.
func (s *L1SQLiteStore) SearchKnowledgeItemsForCategoryRecall(
	ctx context.Context,
	domain string,
	query string,
	limit int,
	eligible func(L1KnowledgeItem) bool,
) ([]L1KnowledgeItem, error) {
	if s == nil || s.db == nil {
		return nil, domainmemory.ErrCommonRawUnavailable
	}
	if eligible == nil {
		return nil, errors.New("l1 knowledge recall eligibility callback is required")
	}
	domain = strings.TrimSpace(domain)
	if err := ValidateKnowledgeDomain(domain); err != nil {
		return nil, err
	}
	domain = NormalizeNewsCategory(domain)
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.New("l1 knowledge recall query is required")
	}
	if limit <= 0 {
		limit = 3
	}
	if limit > 24 {
		limit = 24
	}
	scope, scopeOK := domaintool.ToolExecutionScopeFromContext(ctx)
	ownerID, rawAccess := knowledgeRawReadOwner(scope, scopeOK)
	userScope := knowledgeRecallUserScope(scope, scopeOK)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("begin l1 knowledge recall transaction: %w", err)
	}
	defer tx.Rollback()

	items, err := searchKnowledgeItemsForRecallTx(ctx, tx, domain, query, userScope, limit)
	if err != nil {
		return nil, err
	}
	eligibleItems := make([]L1KnowledgeItem, 0, len(items))
	for index := range items {
		item := &items[index]
		if !eligible(*item) {
			continue
		}
		if !knowledgeItemScopeAllowsQuery(*item, userScope) {
			if knowledgeSummaryIsExactCandidate(*item) {
				item.SourceFailure = L1KnowledgeSourceFailureScopeDenied
			}
			eligibleItems = append(eligibleItems, *item)
			continue
		}
		if !knowledgeSummaryIsExactCandidate(*item) {
			eligibleItems = append(eligibleItems, *item)
			continue
		}
		if !rawAccess {
			item.SourceFailure = L1KnowledgeSourceFailureScopeDenied
			eligibleItems = append(eligibleItems, *item)
			continue
		}

		s.rawMu.Lock()
		rawRoot := s.rawSourceRoot
		s.rawMu.Unlock()
		proof, verifyErr := verifyKnowledgeRawQuoteSource(ctx, tx, rawRoot, *item, ownerID)
		if verifyErr != nil {
			item.SourceFailure = knowledgeSourceFailureFor(verifyErr)
			eligibleItems = append(eligibleItems, *item)
			continue
		}
		summary := strings.TrimSpace(item.SummaryDraft)
		start := strings.Index(string(proof.content), summary)
		if start < 0 || !utf8.Valid(proof.content) || !utf8.ValidString(summary) {
			item.SourceFailure = L1KnowledgeSourceFailureInvalid
			eligibleItems = append(eligibleItems, *item)
			continue
		}
		item.PromptSource = &llm.PromptSourceRef{
			Owner:             "RenCrow_CORE",
			SourceID:          proof.rawRecordID,
			RawHash:           proof.rawHash,
			ProjectionVersion: "knowledge-recall-quote/v1",
			Range:             llm.ByteRange{Start: uint64(start), End: uint64(start + len(summary))},
			Origin:            "unknown",
			Sequence:          0,
		}
		eligibleItems = append(eligibleItems, *item)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit l1 knowledge recall transaction: %w", err)
	}
	return eligibleItems, nil
}

func searchKnowledgeItemsForRecallTx(ctx context.Context, tx *sql.Tx, domain, query, userScope string, limit int) ([]L1KnowledgeItem, error) {
	scopeExpr := `CASE WHEN json_valid(k.meta_json) THEN COALESCE(NULLIF(json_extract(k.meta_json, '$.scope'), ''), 'public') ELSE 'public' END`
	userOwner := ""
	if strings.HasPrefix(userScope, "user:") {
		userOwner = strings.TrimPrefix(userScope, "user:")
	}
	rows, err := tx.QueryContext(ctx, `
SELECT k.id, k.staging_id, k.domain, k.title, k.source_id, k.source_url, k.raw_text, k.raw_hash,
       k.summary_draft, k.keywords_json, k.license_note, k.meta_json, k.created_at, k.updated_at
FROM l1_knowledge_item_fts f
JOIN l1_knowledge_item k ON k.id = f.id
WHERE (f.title LIKE ? OR f.raw_text LIKE ? OR f.summary_draft LIKE ? OR f.keywords_text LIKE ?)
  AND f.domain = ?
  AND (`+scopeExpr+` IN ('public', 'all') OR (? <> 'public' AND `+scopeExpr+` IN (?, ?)))
ORDER BY k.updated_at DESC, k.rowid DESC
LIMIT ?`, LikeQuery(query), LikeQuery(query), LikeQuery(query), LikeQuery(query), domain, userScope, userScope, userOwner, limit)
	if err != nil {
		return nil, fmt.Errorf("search l1 knowledge recall candidates: %w", err)
	}
	items, scanErr := ScanL1KnowledgeItems(rows)
	closeErr := rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close l1 knowledge recall rows: %w", closeErr)
	}
	if len(items) > 0 {
		return items, nil
	}

	terms := knowledgeSearchTerms(query)
	if len(terms) == 0 {
		return []L1KnowledgeItem{}, nil
	}
	clauses := make([]string, 0, len(terms))
	args := make([]any, 0, len(terms)*4+2)
	for _, term := range terms {
		clauses = append(clauses, `(f.title LIKE ? OR f.raw_text LIKE ? OR f.summary_draft LIKE ? OR f.keywords_text LIKE ?)`)
		like := LikeQuery(term)
		args = append(args, like, like, like, like)
	}
	args = append(args, domain, userScope, userScope, userOwner, limit)
	rows, err = tx.QueryContext(ctx, `
SELECT k.id, k.staging_id, k.domain, k.title, k.source_id, k.source_url, k.raw_text, k.raw_hash,
       k.summary_draft, k.keywords_json, k.license_note, k.meta_json, k.created_at, k.updated_at
FROM l1_knowledge_item_fts f
JOIN l1_knowledge_item k ON k.id = f.id
WHERE (`+strings.Join(clauses, " OR ")+`) AND f.domain = ?
  AND (`+scopeExpr+` IN ('public', 'all') OR (? <> 'public' AND `+scopeExpr+` IN (?, ?)))
ORDER BY k.updated_at DESC, k.rowid DESC
LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("search l1 knowledge recall terms: %w", err)
	}
	items, scanErr = ScanL1KnowledgeItems(rows)
	closeErr = rows.Close()
	if scanErr != nil {
		return nil, scanErr
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close l1 knowledge recall term rows: %w", closeErr)
	}
	return items, nil
}

func knowledgeSummaryIsExactCandidate(item L1KnowledgeItem) bool {
	if strings.TrimSpace(item.SummaryDraft) == "" {
		return false
	}
	return strings.Contains(item.RawText, strings.TrimSpace(item.SummaryDraft))
}

func knowledgeRawReadOwner(scope domaintool.ToolExecutionScope, found bool) (string, bool) {
	if !found || scope.Validate() != nil || scope.AuthenticatedUserID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "", false
	}
	if scope.ActorKind == domaintool.ActorKindAgent &&
		(scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || !scope.Allows(domaintool.DataScopeInternal)) {
		return "", false
	}
	return scope.AuthenticatedUserID, true
}

func knowledgeRecallUserScope(scope domaintool.ToolExecutionScope, found bool) string {
	if !found || scope.Validate() != nil || scope.AuthenticatedUserID == "" || !scope.Allows(domaintool.DataScopeUser) {
		return "public"
	}
	return "user:" + scope.AuthenticatedUserID
}

func knowledgeItemScopeAllowsQuery(item L1KnowledgeItem, queryScope string) bool {
	itemScope := strings.TrimSpace(metaString(item.Meta, "scope"))
	switch strings.ToLower(itemScope) {
	case "", "public", "all":
		return true
	}
	if !strings.HasPrefix(queryScope, "user:") {
		return false
	}
	ownerID := strings.TrimPrefix(queryScope, "user:")
	return itemScope == ownerID || itemScope == queryScope
}

type knowledgeRawQuoteProof struct {
	rawRecordID string
	rawHash     string
	content     []byte
}

type knowledgeRawQuoteHeader struct {
	rawRecordID, manifestID, rawContract, rawSourceType, rawSourceIdentity                 string
	rawSourceRecordID, rawOwnerID, rawScope, rawSensitivity, rawRole, rawContentType       string
	rawStorage, rawObjectRef, rawHash, rawRights, rawLicense, rawAssetRefs                 string
	rawSize                                                                                int64
	manifestContract, manifestSourceType, manifestSourceIdentity, manifestHash             string
	manifestSchema, manifestConverter, manifestOwnerID, manifestScope, manifestSensitivity string
	manifestRights, manifestLicense, manifestRequestID, manifestActorID, manifestStatus    string
	manifestCheckpoint, manifestReceipt                                                    string
	manifestSourceCount, manifestAssetCount                                                int
	manifestAllowEmpty                                                                     int
	manifestCreatedAt, manifestUpdatedAt                                                   time.Time
}

func verifyKnowledgeRawQuoteSource(ctx context.Context, tx *sql.Tx, root string, item L1KnowledgeItem, ownerID string) (knowledgeRawQuoteProof, error) {
	knowledgeHash, err := knowledgeItemContentHash(item)
	if err != nil {
		return knowledgeRawQuoteProof{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "knowledge raw projection hash is invalid")
	}
	if !utf8.ValidString(item.RawText) || !utf8.ValidString(strings.TrimSpace(item.SummaryDraft)) {
		return knowledgeRawQuoteProof{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "knowledge quote is not valid UTF-8")
	}

	receipt, err := readKnowledgeQuoteProjectionReceipt(ctx, tx, item.ID, knowledgeHash)
	if err != nil {
		return knowledgeRawQuoteProof{}, err
	}
	header, err := readKnowledgeRawQuoteHeader(ctx, tx, receipt.rawRecordID)
	if err != nil {
		return knowledgeRawQuoteProof{}, err
	}
	if err := validateKnowledgeRawQuoteHeader(header, item, receipt, knowledgeHash, ownerID); err != nil {
		return knowledgeRawQuoteProof{}, err
	}
	if err := validateKnowledgeRawQuoteIntake(header, item, receipt); err != nil {
		return knowledgeRawQuoteProof{}, err
	}
	if err := validateKnowledgeRawQuoteLifecycle(ctx, tx, header); err != nil {
		return knowledgeRawQuoteProof{}, err
	}

	var content []byte
	switch header.rawStorage {
	case domainmemory.CommonRawStorageInline:
		if header.rawObjectRef != "" || header.rawSize > domainmemory.CommonRawMaxInlinePayloadSize {
			return knowledgeRawQuoteProof{}, domainmemory.ErrCommonRawInvalid
		}
		err := tx.QueryRowContext(ctx, `
SELECT inline_payload
FROM l1_raw_record
WHERE raw_record_id = ? AND owner_id = ? AND scope = ? AND content_sha256 = ?
  AND storage_kind = ? AND object_ref = '' AND content_size = ?
  AND length(inline_payload) = ? AND length(inline_payload) <= ?`,
			header.rawRecordID, ownerID, "user:"+ownerID, header.rawHash,
			domainmemory.CommonRawStorageInline, header.rawSize, header.rawSize, domainmemory.CommonRawMaxInlinePayloadSize).Scan(&content)
		if errors.Is(err, sql.ErrNoRows) {
			return knowledgeRawQuoteProof{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "inline Common Raw payload size differs from its validated header")
		}
		if err != nil {
			return knowledgeRawQuoteProof{}, fmt.Errorf("read inline Common Raw quote bytes: %w", err)
		}
	case domainmemory.CommonRawStorageObject:
		if header.rawAssetRefs != "[]" || header.rawObjectRef == "" {
			return knowledgeRawQuoteProof{}, domainmemory.ErrCommonRawInvalid
		}
		content, err = readCommonRawObjectOnce(root, header.rawObjectRef, header.rawSize)
		if err != nil {
			return knowledgeRawQuoteProof{}, err
		}
	default:
		return knowledgeRawQuoteProof{}, domainmemory.ErrCommonRawInvalid
	}
	if int64(len(content)) != header.rawSize || header.rawSize != int64(len(item.RawText)) ||
		domainmemory.SHA256Hex(content) != header.rawHash || !bytesEqual(content, []byte(item.RawText)) {
		return knowledgeRawQuoteProof{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "Common Raw quote bytes differ from the Knowledge projection")
	}
	return knowledgeRawQuoteProof{rawRecordID: header.rawRecordID, rawHash: header.rawHash, content: content}, nil
}

type knowledgeQuoteProjectionReceipt struct {
	receiptID, outputStore, outputRecordID, rawRecordIDsJSON string
	inputHash, outputHash, status, revision, projectionType  string
	rawRecordID                                              string
}

func readKnowledgeQuoteProjectionReceipt(ctx context.Context, tx *sql.Tx, itemID, rawHash string) (knowledgeQuoteProjectionReceipt, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT projection_receipt_id, output_store, output_record_id, raw_record_ids_json,
       input_sha256, output_sha256, status, revision, projection_type
FROM l1_raw_projection_receipt
WHERE output_record_id = ? AND projection_type = ? AND revision = ? AND status = 'completed'`,
		itemID, knowledgeRawProjectionType, knowledgeRawProjectionRevision)
	if err != nil {
		return knowledgeQuoteProjectionReceipt{}, fmt.Errorf("read Knowledge projection receipt: %w", err)
	}
	defer rows.Close()
	var receipt knowledgeQuoteProjectionReceipt
	count := 0
	for rows.Next() {
		count++
		if count > 1 {
			return knowledgeQuoteProjectionReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Knowledge projection receipt is ambiguous")
		}
		if err := rows.Scan(&receipt.receiptID, &receipt.outputStore, &receipt.outputRecordID, &receipt.rawRecordIDsJSON,
			&receipt.inputHash, &receipt.outputHash, &receipt.status, &receipt.revision, &receipt.projectionType); err != nil {
			return knowledgeQuoteProjectionReceipt{}, fmt.Errorf("scan Knowledge projection receipt: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return knowledgeQuoteProjectionReceipt{}, fmt.Errorf("read Knowledge projection receipt rows: %w", err)
	}
	if count != 1 || receipt.outputStore != "conversation_l1" || receipt.outputRecordID != itemID ||
		receipt.inputHash != rawHash || receipt.outputHash != rawHash || receipt.status != "completed" ||
		receipt.revision != knowledgeRawProjectionRevision || receipt.projectionType != knowledgeRawProjectionType {
		return knowledgeQuoteProjectionReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Knowledge projection receipt is missing or inconsistent")
	}
	var rawIDs []string
	if err := json.Unmarshal([]byte(receipt.rawRecordIDsJSON), &rawIDs); err != nil || len(rawIDs) != 1 || strings.TrimSpace(rawIDs[0]) == "" {
		return knowledgeQuoteProjectionReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Knowledge projection receipt must bind one Raw record")
	}
	receipt.rawRecordID = rawIDs[0]
	if receipt.receiptID != knowledgeRawProjectionReceiptID("completed", receipt.rawRecordID) {
		return knowledgeQuoteProjectionReceipt{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Knowledge projection receipt identity is inconsistent")
	}
	return receipt, nil
}

func readKnowledgeRawQuoteHeader(ctx context.Context, tx *sql.Tx, rawRecordID string) (knowledgeRawQuoteHeader, error) {
	var header knowledgeRawQuoteHeader
	err := tx.QueryRowContext(ctx, `
SELECT r.raw_record_id, r.manifest_id, r.contract_version, r.source_type, r.source_identity,
       r.source_record_id, r.owner_id, r.scope, r.sensitivity, r.role, r.content_type,
       r.storage_kind, r.object_ref, r.content_sha256, r.content_size, r.rights, r.license, r.asset_refs_json,
       m.contract_version, m.source_type, m.source_identity, m.manifest_sha256, m.source_count, m.asset_count,
       m.schema_version, m.converter_version, m.owner_id, m.scope, m.sensitivity, m.rights, m.license,
       m.request_id, m.actor_id, m.intake_status, m.checkpoint_json, m.receipt_json, m.allow_empty,
       m.created_at, m.updated_at
FROM l1_raw_record r
JOIN l1_raw_source_manifest m ON m.manifest_id = r.manifest_id
WHERE r.raw_record_id = ?`, rawRecordID).Scan(
		&header.rawRecordID, &header.manifestID, &header.rawContract, &header.rawSourceType, &header.rawSourceIdentity,
		&header.rawSourceRecordID, &header.rawOwnerID, &header.rawScope, &header.rawSensitivity, &header.rawRole, &header.rawContentType,
		&header.rawStorage, &header.rawObjectRef, &header.rawHash, &header.rawSize, &header.rawRights, &header.rawLicense, &header.rawAssetRefs,
		&header.manifestContract, &header.manifestSourceType, &header.manifestSourceIdentity, &header.manifestHash, &header.manifestSourceCount, &header.manifestAssetCount,
		&header.manifestSchema, &header.manifestConverter, &header.manifestOwnerID, &header.manifestScope, &header.manifestSensitivity, &header.manifestRights, &header.manifestLicense,
		&header.manifestRequestID, &header.manifestActorID, &header.manifestStatus, &header.manifestCheckpoint, &header.manifestReceipt, &header.manifestAllowEmpty,
		&header.manifestCreatedAt, &header.manifestUpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return knowledgeRawQuoteHeader{}, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "verified Common Raw source is missing")
	}
	if err != nil {
		return knowledgeRawQuoteHeader{}, fmt.Errorf("read Knowledge Common Raw header: %w", err)
	}
	return header, nil
}

func validateKnowledgeRawQuoteHeader(header knowledgeRawQuoteHeader, item L1KnowledgeItem, receipt knowledgeQuoteProjectionReceipt, rawHash, ownerID string) error {
	scope := "user:" + ownerID
	if header.rawOwnerID != ownerID || header.manifestOwnerID != ownerID || header.rawScope != scope || header.manifestScope != scope {
		return domainmemory.ErrCommonRawForbidden
	}
	if header.rawRecordID != receipt.rawRecordID || header.rawSourceRecordID != item.ID || header.manifestID == "" ||
		header.rawContract != domainmemory.CommonRawContractVersion || header.manifestContract != domainmemory.CommonRawContractVersion ||
		header.rawSourceType != knowledgeRawSourceType || header.manifestSourceType != knowledgeRawSourceType ||
		header.rawSourceIdentity != knowledgeRawSourceIdentity || header.manifestSourceIdentity != knowledgeRawSourceIdentity ||
		header.rawSensitivity != domainmemory.CommonRawPrivateSensitivity || header.manifestSensitivity != domainmemory.CommonRawPrivateSensitivity ||
		header.rawRole != knowledgeRawRole || header.rawContentType != knowledgeRawContentType ||
		header.rawHash != rawHash || header.rawSize < 0 || header.rawSize > domainmemory.CommonRawMaxPayloadSize ||
		header.rawRights != "owner" || header.rawLicense != "private" || header.manifestRights != "owner" || header.manifestLicense != "private" ||
		header.manifestSchema != knowledgeRawSchemaVersion || header.manifestConverter != knowledgeRawConverterVersion ||
		header.manifestHash == "" || !validLowerSHA256Claim(header.manifestHash) || header.manifestSourceCount <= 0 ||
		header.manifestAssetCount != 0 || header.manifestAllowEmpty != 0 || header.manifestStatus != string(domainmemory.CommonRawStateCompleted) ||
		header.manifestRequestID == "" || header.manifestActorID == "" || header.rawAssetRefs != "[]" ||
		header.rawRecordID != domainmemory.DeterministicCommonRawRecordID(ownerID, scope, knowledgeRawSourceType, knowledgeRawSourceIdentity, item.ID, rawHash) ||
		header.manifestID != domainmemory.DeterministicCommonRawManifestID(ownerID, scope, knowledgeRawSourceType, knowledgeRawSourceIdentity, header.manifestHash) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw source header is inconsistent")
	}
	return nil
}

func validateKnowledgeRawQuoteIntake(header knowledgeRawQuoteHeader, item L1KnowledgeItem, projection knowledgeQuoteProjectionReceipt) error {
	var checkpoint struct {
		ManifestID  string                      `json:"manifest_id"`
		SourceCount int                         `json:"source_count"`
		AssetCount  int                         `json:"asset_count"`
		Status      domainmemory.CommonRawState `json:"status"`
	}
	if err := json.Unmarshal([]byte(header.manifestCheckpoint), &checkpoint); err != nil ||
		checkpoint.ManifestID != header.manifestID || checkpoint.SourceCount != header.manifestSourceCount ||
		checkpoint.AssetCount != header.manifestAssetCount || checkpoint.Status != domainmemory.CommonRawStateCompleted {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw intake checkpoint is inconsistent")
	}
	var intake domainmemory.CommonRawIntakeReceipt
	if err := json.Unmarshal([]byte(header.manifestReceipt), &intake); err != nil ||
		intake.ManifestID != header.manifestID || intake.RequestID != header.manifestRequestID ||
		intake.Status != domainmemory.CommonRawStateCompleted || intake.ManifestSHA256 != header.manifestHash ||
		intake.SourceCount != header.manifestSourceCount || intake.AssetCount != header.manifestAssetCount ||
		intake.Checkpoint != "completed" || len(intake.Records) != header.manifestSourceCount || intake.CreatedAt.IsZero() {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw intake receipt is inconsistent")
	}
	createdAt := header.manifestCreatedAt
	if createdAt.IsZero() || !createdAt.Equal(intake.CreatedAt) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw intake receipt timestamp is inconsistent")
	}
	updatedAt := header.manifestUpdatedAt
	if updatedAt.IsZero() || updatedAt.Before(createdAt) {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw intake update timestamp is inconsistent")
	}
	matching := 0
	for _, record := range intake.Records {
		if record.SourceRecordID != item.ID || record.RawRecordID != projection.rawRecordID {
			continue
		}
		matching++
		if record.ContentSHA256 != projection.inputHash || record.ContentSize != header.rawSize ||
			record.ContentSize != int64(len(item.RawText)) || record.StorageKind != header.rawStorage || record.ObjectRef != header.rawObjectRef ||
			record.StorageKind != "inline" && record.StorageKind != "object" ||
			record.StorageKind == "object" && record.ObjectRef == "" || record.StorageKind == "inline" && record.ObjectRef != "" || len(record.AssetRefs) != 0 {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw intake record receipt is inconsistent")
		}
	}
	if matching != 1 {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw intake receipt does not bind exactly one Knowledge source")
	}
	return nil
}

func validateKnowledgeRawQuoteLifecycle(ctx context.Context, tx *sql.Tx, header knowledgeRawQuoteHeader) error {
	rows, err := tx.QueryContext(ctx, `
SELECT state_event_id, raw_record_id, manifest_id, event_type, event_hash, owner_id, scope,
       request_id, actor_id, reason_code, payload_json
FROM l1_raw_state_event WHERE raw_record_id = ?`, header.rawRecordID)
	if err != nil {
		return fmt.Errorf("read Knowledge Common Raw lifecycle: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		count++
		var stateID, rawID, manifestID, eventType, eventHash, ownerID, scope, requestID, actorID, reason, payload string
		if err := rows.Scan(&stateID, &rawID, &manifestID, &eventType, &eventHash, &ownerID, &scope, &requestID, &actorID, &reason, &payload); err != nil {
			return fmt.Errorf("scan Knowledge Common Raw lifecycle: %w", err)
		}
		if count != 1 || stateID != domainmemory.DeterministicCommonRawStateEventID(header.rawRecordID, "ingested", header.rawHash) ||
			rawID != header.rawRecordID || manifestID != header.manifestID || eventType != "ingested" || eventHash != header.rawHash ||
			ownerID != header.manifestOwnerID || scope != header.manifestScope || requestID != header.manifestRequestID ||
			actorID != header.manifestActorID || reason != "ingested" || payload != "{}" {
			return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw lifecycle is inconsistent")
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read Knowledge Common Raw lifecycle rows: %w", err)
	}
	if count != 1 {
		return domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw lifecycle must contain exactly one ingested event")
	}
	return nil
}

func readCommonRawObjectOnce(root, ref string, expectedSize int64) ([]byte, error) {
	if expectedSize <= 0 || expectedSize > domainmemory.CommonRawMaxPayloadSize {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "Common Raw object size is outside the bounded limit")
	}
	path, err := commonRawStoredObjectPath(root, ref)
	if err != nil {
		return nil, err
	}
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Size() != expectedSize {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "Common Raw object is missing or has an invalid file type")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open Common Raw object: %w", domainmemory.ErrCommonRawUnavailable)
	}
	defer file.Close()
	afterOpen, err := file.Stat()
	if err != nil || !afterOpen.Mode().IsRegular() || !os.SameFile(before, afterOpen) || afterOpen.Size() != expectedSize {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "Common Raw object changed before read")
	}
	content, err := io.ReadAll(io.LimitReader(file, expectedSize+1))
	if err != nil {
		return nil, fmt.Errorf("read Common Raw object: %w", domainmemory.ErrCommonRawUnavailable)
	}
	afterRead, err := file.Stat()
	if err != nil || !afterRead.Mode().IsRegular() || !os.SameFile(afterOpen, afterRead) || afterRead.Size() != expectedSize || int64(len(content)) != expectedSize {
		return nil, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorObject, "Common Raw object changed during read")
	}
	return content, nil
}

func knowledgeSourceFailureFor(err error) L1KnowledgeSourceFailure {
	switch domainmemory.CommonRawErrorCodeOf(err) {
	case domainmemory.CommonRawErrorForbidden:
		return L1KnowledgeSourceFailureScopeDenied
	case domainmemory.CommonRawErrorInvalid, domainmemory.CommonRawErrorObject, domainmemory.CommonRawErrorRoot,
		domainmemory.CommonRawErrorSchema, domainmemory.CommonRawErrorSourceChanged, domainmemory.CommonRawErrorConflict:
		return L1KnowledgeSourceFailureInvalid
	default:
		return L1KnowledgeSourceFailureSourceUnavailable
	}
}

func bytesEqual(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
