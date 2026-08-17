package l1sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

const (
	knowledgeRawSourceType         = "knowledge_item"
	knowledgeRawSourceIdentity     = "l1_knowledge_item"
	knowledgeRawSchemaVersion      = "rencrow.knowledge_item.v1"
	knowledgeRawConverterVersion   = "knowledge-legacy-backfill/v1"
	knowledgeRawProjectionType     = "knowledge_item"
	knowledgeRawProjectionRevision = "knowledge-raw/v1"
	knowledgeRawContentType        = "text/plain"
	knowledgeRawRole               = "knowledge"
)

type knowledgeRawLink struct {
	Item        L1KnowledgeItem
	Content     []byte
	Hash        string
	RawRecordID string
}

// KnowledgeCommonRawCoverage is the fail-closed production gate. Empty tables,
// unmatched hashes, and missing receipts never count as ready.
func (s *L1SQLiteStore) KnowledgeCommonRawCoverage(ctx context.Context) (itemCount, covered int, ready bool, err error) {
	if s == nil || s.db == nil {
		return 0, 0, false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "conversation_l1 store is unavailable")
	}
	items, err := s.listKnowledgeItemsForRawBackfill(ctx)
	if err != nil {
		return 0, 0, false, err
	}
	itemCount = len(items)
	if itemCount == 0 {
		return 0, 0, false, nil
	}
	for _, item := range items {
		hash, hashErr := knowledgeItemContentHash(item)
		if hashErr != nil {
			return itemCount, covered, false, hashErr
		}
		ok, coverErr := s.knowledgeItemHasCompletedReceipt(ctx, item.ID, hash)
		if coverErr != nil {
			return itemCount, covered, false, coverErr
		}
		if ok {
			covered++
		}
	}
	return itemCount, covered, covered == itemCount, nil
}

// BackfillKnowledgeCommonRaw copies existing l1_knowledge_item rows into Common
// Raw without rewriting those rows. allow_empty is always false.
func (s *L1SQLiteStore) BackfillKnowledgeCommonRaw(ctx context.Context, requestID, ownerID, actorID string, apply bool) (domainmemory.KnowledgeCommonRawBackfillResult, error) {
	result := domainmemory.KnowledgeCommonRawBackfillResult{Status: domainmemory.CommonRawStateBlocked}
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

	items, err := s.listKnowledgeItemsForRawBackfill(ctx)
	if err != nil {
		return result, err
	}
	result.ItemCount = len(items)
	result.Validated = len(items)
	if len(items) == 0 {
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "empty knowledge backfill requires explicit allow_empty")
	}
	if len(items) > domainmemory.CommonRawMaxRecords {
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "knowledge backfill exceeds the bounded record limit")
	}

	links := make([]knowledgeRawLink, 0, len(items))
	records := make([]domainmemory.CommonRawRecord, 0, len(items))
	provenanceBytes, err := json.Marshal(map[string]string{
		"adapter":      knowledgeRawConverterVersion,
		"source_table": knowledgeRawSourceIdentity,
	})
	if err != nil {
		return result, fmt.Errorf("marshal knowledge raw provenance: %w", err)
	}
	provenance := string(provenanceBytes)
	for _, item := range items {
		hash, hashErr := knowledgeItemContentHash(item)
		if hashErr != nil {
			return result, hashErr
		}
		content := []byte(item.RawText)
		records = append(records, domainmemory.CommonRawRecord{
			SourceRecordID: item.ID,
			ParentID:       item.StagingID,
			ThreadID:       item.Domain,
			Sensitivity:    domainmemory.CommonRawPrivateSensitivity,
			Role:           knowledgeRawRole,
			ContentType:    knowledgeRawContentType,
			OccurredAt:     knowledgeItemOccurredAt(item),
			Content:        content,
			ContentSHA256:  hash,
			Provenance:     provenance,
			Rights:         "owner",
			License:        "private",
		})
		links = append(links, knowledgeRawLink{Item: item, Content: content, Hash: hash})
	}

	manifest := domainmemory.CommonRawManifest{
		ContractVersion:  domainmemory.CommonRawContractVersion,
		SourceType:       knowledgeRawSourceType,
		SourceIdentity:   knowledgeRawSourceIdentity,
		SourceCount:      len(records),
		AssetCount:       0,
		SchemaVersion:    knowledgeRawSchemaVersion,
		ConverterVersion: knowledgeRawConverterVersion,
		Sensitivity:      domainmemory.CommonRawPrivateSensitivity,
		Rights:           "owner",
		License:          "private",
		Provenance:       provenance,
		AllowEmpty:       false,
	}
	manifest.ManifestSHA256, err = domainmemory.CommonRawInputHash(manifest, records, nil)
	if err != nil {
		return result, fmt.Errorf("compute knowledge raw input hash: %w", err)
	}
	input := domainmemory.CommonRawIntakeRequest{Manifest: manifest, Records: records}
	prepared, err := prepareCommonRawIntake(ownerID, input)
	if err != nil {
		return result, err
	}
	result.ManifestID = domainmemory.DeterministicCommonRawManifestID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, prepared.manifestSHA256)
	linkByID := make(map[string]int, len(links))
	for i, link := range links {
		linkByID[link.Item.ID] = i
	}
	for _, record := range prepared.records {
		index, ok := linkByID[record.input.SourceRecordID]
		if !ok {
			return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "knowledge raw plan lost a source record")
		}
		rawID := domainmemory.DeterministicCommonRawRecordID(ownerID, prepared.manifest.Scope, prepared.manifest.SourceType, prepared.manifest.SourceIdentity, record.input.SourceRecordID, record.hash)
		links[index].RawRecordID = rawID
		result.RawRecordIDs = append(result.RawRecordIDs, rawID)
	}
	sort.Strings(result.RawRecordIDs)
	if prepared.requiresObject && commonRawRootPathError(s.rawSourceRoot) != nil {
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorRoot, "configured raw source root is invalid")
	}

	itemCount, covered, ready, err := s.KnowledgeCommonRawCoverage(ctx)
	if err != nil {
		return result, err
	}
	result.ItemCount = itemCount
	result.Coverage = covered
	result.Ready = ready
	if !apply {
		return result, nil
	}

	receipt, err := s.IntakeCommonRaw(ctx, requestID, ownerID, actorID, input)
	if err != nil {
		return result, err
	}
	result.RawReceipt = receipt
	result.ManifestID = receipt.ManifestID
	if receipt.IdempotentReplay {
		result.RawReplayed = len(receipt.Records)
	} else {
		result.RawImported = len(receipt.Records)
	}
	linked, err := s.commitKnowledgeRawProjectionReceipts(ctx, links)
	if err != nil {
		return result, err
	}
	result.Linked = linked
	itemCount, covered, ready, err = s.KnowledgeCommonRawCoverage(ctx)
	if err != nil {
		return result, err
	}
	result.ItemCount = itemCount
	result.Coverage = covered
	result.Ready = ready
	if !ready {
		result.Status = domainmemory.CommonRawStateBlocked
		return result, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "knowledge Common Raw coverage is incomplete")
	}
	result.Status = domainmemory.CommonRawStateCompleted
	return result, nil
}

func (s *L1SQLiteStore) listKnowledgeItemsForRawBackfill(ctx context.Context) ([]L1KnowledgeItem, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, staging_id, domain, title, source_id, source_url, raw_text, raw_hash,
       summary_draft, keywords_json, license_note, meta_json, created_at, updated_at
FROM l1_knowledge_item
ORDER BY id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list knowledge items for raw backfill: %w", err)
	}
	defer rows.Close()
	return ScanL1KnowledgeItems(rows)
}

func knowledgeItemContentHash(item L1KnowledgeItem) (string, error) {
	actual := domainmemory.SHA256Hex([]byte(item.RawText))
	claimed := strings.ToLower(strings.TrimSpace(item.RawHash))
	if !validLowerSHA256Claim(claimed) {
		return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "persisted knowledge raw_hash is not a sha256")
	}
	if actual != claimed {
		return "", domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "persisted knowledge content hash differs from raw_text")
	}
	return actual, nil
}

func knowledgeItemOccurredAt(item L1KnowledgeItem) time.Time {
	if !item.CreatedAt.IsZero() {
		return item.CreatedAt.UTC()
	}
	if !item.UpdatedAt.IsZero() {
		return item.UpdatedAt.UTC()
	}
	return time.Unix(0, 0).UTC()
}

func knowledgeRawProjectionReceiptID(status, rawRecordID string) string {
	digest := sha256.Sum256([]byte(knowledgeRawProjectionRevision + "\x00" + status + "\x00" + rawRecordID))
	return "knowledge-projection:" + status + ":" + hex.EncodeToString(digest[:])
}

func knowledgeProjectionRawIDsJSON(rawRecordID string) string {
	encoded, _ := json.Marshal([]string{rawRecordID})
	return string(encoded)
}

func (s *L1SQLiteStore) knowledgeItemHasCompletedReceipt(ctx context.Context, itemID, hash string) (bool, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT projection_receipt_id, input_sha256, output_sha256, status, revision, projection_type
FROM l1_raw_projection_receipt
WHERE output_record_id = ? AND projection_type = ? AND status = 'completed' AND revision = ?`,
		itemID, knowledgeRawProjectionType, knowledgeRawProjectionRevision)
	if err != nil {
		return false, fmt.Errorf("query knowledge projection coverage: %w", err)
	}
	defer rows.Close()
	matched := 0
	for rows.Next() {
		var id, inputHash, outputHash, status, revision, projectionType string
		if err := rows.Scan(&id, &inputHash, &outputHash, &status, &revision, &projectionType); err != nil {
			return false, err
		}
		if inputHash != hash || outputHash != hash {
			return false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "knowledge projection receipt hash differs from the existing row")
		}
		matched++
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	if matched > 1 {
		return false, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "duplicate knowledge projection receipts")
	}
	return matched == 1, nil
}

func (s *L1SQLiteStore) commitKnowledgeRawProjectionReceipts(ctx context.Context, links []knowledgeRawLink) (int, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	linked := 0
	now := time.Now().UTC()
	for _, link := range links {
		for _, status := range []string{"pending", "completed"} {
			outputHash := ""
			if status == "completed" {
				outputHash = link.Hash
			}
			insert, err := tx.ExecContext(ctx, `
INSERT OR IGNORE INTO l1_raw_projection_receipt (
 projection_receipt_id, projection_type, output_store, output_record_id,
 raw_record_ids_json, revision, input_sha256, output_sha256, status,
 created_at, updated_at, failure_reason
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '')`,
				knowledgeRawProjectionReceiptID(status, link.RawRecordID), knowledgeRawProjectionType, "conversation_l1", link.Item.ID,
				knowledgeProjectionRawIDsJSON(link.RawRecordID), knowledgeRawProjectionRevision,
				link.Hash, outputHash, status, now, now)
			if err != nil {
				return 0, rollbackL1Tx(tx, fmt.Errorf("insert knowledge %s projection receipt: %w", status, err))
			}
			affected, err := insert.RowsAffected()
			if err != nil {
				return 0, rollbackL1Tx(tx, err)
			}
			if status == "completed" && affected == 1 {
				linked++
			}
			if affected == 0 {
				var storedInput, storedOutput, storedStatus, storedType, storedRevision, storedOutputID string
				err := tx.QueryRowContext(ctx, `
SELECT input_sha256, output_sha256, status, projection_type, revision, output_record_id
FROM l1_raw_projection_receipt WHERE projection_receipt_id = ?`, knowledgeRawProjectionReceiptID(status, link.RawRecordID)).Scan(
					&storedInput, &storedOutput, &storedStatus, &storedType, &storedRevision, &storedOutputID)
				if err == sql.ErrNoRows {
					return 0, rollbackL1Tx(tx, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "knowledge projection receipt disappeared"))
				}
				if err != nil {
					return 0, rollbackL1Tx(tx, err)
				}
				if storedInput != link.Hash || storedType != knowledgeRawProjectionType || storedRevision != knowledgeRawProjectionRevision || storedStatus != status || storedOutputID != link.Item.ID {
					return 0, rollbackL1Tx(tx, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "existing knowledge projection receipt binding differs"))
				}
				if status == "completed" && storedOutput != link.Hash {
					return 0, rollbackL1Tx(tx, domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "existing knowledge projection receipt hash differs"))
				}
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return linked, nil
}
