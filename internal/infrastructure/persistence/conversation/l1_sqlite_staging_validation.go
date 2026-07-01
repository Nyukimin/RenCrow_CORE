package conversation

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

func (s *L1SQLiteStore) ValidateStagingItem(ctx context.Context, id string, policy L1StagingValidationPolicy) (*L1StagingValidationResult, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil, errors.New("l1 staging item id is required")
	}
	item, err := s.stagingItemByID(ctx, id)
	if err != nil {
		return nil, err
	}
	result, err := s.validateStagingItemContent(ctx, *item, policy)
	if err != nil {
		return nil, err
	}
	now := policy.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	issuesJSON, err := json.Marshal(result.Issues)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 staging validation issues: %w", err)
	}
	meta := map[string]interface{}{}
	for k, v := range item.Meta {
		meta[k] = v
	}
	meta["validation_issues"] = result.Issues
	meta["validated_at"] = now.Format(time.RFC3339)
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal l1 staging validation meta: %w", err)
	}
	update, err := s.db.ExecContext(ctx, `
UPDATE l1_staging_item
SET validation_status = ?, meta_json = ?, updated_at = ?
WHERE id = ?
`, result.Status, string(metaJSON), now, id)
	if err != nil {
		return nil, fmt.Errorf("failed to update l1 staging validation status: %w", err)
	}
	affected, err := update.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("failed to inspect l1 staging validation update: %w", err)
	}
	if affected == 0 {
		return nil, sql.ErrNoRows
	}
	if _, err := s.AppendEvent(ctx, "staging.item_validated", item.Namespace, "", 0, map[string]interface{}{
		"staging_id":        item.ID,
		"passed":            result.Passed,
		"validation_status": result.Status,
		"issues":            string(issuesJSON),
	}, "validator"); err != nil {
		return nil, fmt.Errorf("failed to append l1 staging validation event log: %w", err)
	}
	if result.Passed && policy.AutoPromoteMemoryCandidate && item.Kind == L1StagingKindMemoryCandidate {
		targetNamespace, ok := stagingMemoryPromotionNamespace(*item)
		if !ok {
			return nil, errors.New("validated memory candidate has no promotable target namespace")
		}
		promoted, err := s.PromoteValidatedStagingItemToMemory(ctx, item.ID, targetNamespace, "validator")
		if err != nil {
			return nil, err
		}
		result.PromotedMemoryID = promoted.ID
		result.PromotedNamespace = promoted.Namespace
	}
	return &result, nil
}

func (s *L1SQLiteStore) validateStagingItemContent(ctx context.Context, item L1StagingItem, policy L1StagingValidationPolicy) (L1StagingValidationResult, error) {
	now := policy.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	now = now.UTC()
	result := L1StagingValidationResult{
		ItemID: item.ID,
		Status: L1StagingStatusValidated,
	}
	addIssue := func(code string, message string) {
		result.Issues = append(result.Issues, L1StagingValidationIssue{Code: code, Message: message})
	}
	if err := validateL1StagingKind(item.Kind); err != nil {
		addIssue("invalid_kind", err.Error())
	}
	if err := ValidateL1Namespace(item.Namespace); err != nil {
		addIssue("invalid_namespace", err.Error())
	}
	if strings.TrimSpace(item.EventID) == "" {
		addIssue("missing_event_id", "event_id is required")
	}
	if strings.TrimSpace(item.SourceID) == "" {
		addIssue("missing_source_id", "source_id is required")
	}
	if err := validateOptionalSourceURL(item.SourceURL); err != nil {
		addIssue("invalid_source_url", err.Error())
	}
	if strings.TrimSpace(item.RawText) == "" {
		addIssue("missing_raw_text", "raw_text is required")
	}
	if rawTextHash(item.RawText) != item.RawHash {
		addIssue("raw_hash_mismatch", "raw_hash does not match raw_text")
	}
	duplicates, err := s.countStagingRawHashDuplicates(ctx, item.ID, item.RawHash)
	if err != nil {
		return result, err
	}
	if duplicates > 0 {
		addIssue("duplicate_raw_hash", "raw_hash already exists in staging")
	}
	if !item.PublishedAt.IsZero() && (item.PublishedAt.After(now) || item.PublishedAt.After(item.FetchedAt)) {
		addIssue("future_published_at", "published_at must not be in the future or after fetched_at")
	}
	if strings.TrimSpace(item.LicenseNote) == "" {
		addIssue("missing_license_note", "license_note is required before promotion")
	}
	if policy.MinimumTrustScore > 0 {
		score, ok := policy.SourceTrustScores[item.SourceID]
		if !ok {
			addIssue("missing_source_trust", "source_id has no trust score")
		} else if score < policy.MinimumTrustScore {
			addIssue("low_source_trust", "source trust score is below minimum")
		}
	}
	if item.Kind == L1StagingKindMemoryCandidate {
		memoryType, ok := item.Meta["type"].(string)
		if !ok || !isAllowedMemoryType(memoryType) {
			addIssue("missing_memory_type", "memory candidate requires an allowed type")
		}
	}
	if containsSensitiveRawText(item.RawText) {
		addIssue("sensitive_raw_text", "raw_text appears to contain sensitive secret material")
	}
	if len(result.Issues) > 0 {
		result.Status = L1StagingStatusRejected
		result.Passed = false
		return result, nil
	}
	result.Passed = true
	return result, nil
}

func stagingMemoryPromotionNamespace(item L1StagingItem) (string, bool) {
	targetNamespace := strings.TrimSpace(stringMeta(item.Meta, "target_namespace"))
	if targetNamespace != "" {
		return targetNamespace, ValidateL1Namespace(targetNamespace) == nil
	}
	if err := ValidateL1Namespace(item.Namespace); err != nil {
		return "", false
	}
	if strings.HasPrefix(item.Namespace, NamespaceKindUser+":") ||
		strings.HasPrefix(item.Namespace, NamespaceKindCharacter+":") ||
		strings.HasPrefix(item.Namespace, NamespaceKindKnowledge+":") {
		return item.Namespace, true
	}
	return "", false
}

func (s *L1SQLiteStore) countStagingRawHashDuplicates(ctx context.Context, id string, rawHash string) (int, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM l1_staging_item
WHERE raw_hash = ? AND id <> ?
`, rawHash, id).Scan(&count); err != nil {
		return 0, fmt.Errorf("failed to count l1 staging raw hash duplicates: %w", err)
	}
	return count, nil
}

func isAllowedMemoryType(memoryType string) bool {
	switch strings.TrimSpace(memoryType) {
	case "profile", "preference", "project", "constraint", "relationship", "episode", "skill", "sensitive":
		return true
	default:
		return false
	}
}

func containsSensitiveRawText(rawText string) bool {
	normalized := strings.ToLower(rawText)
	sensitiveMarkers := []string{"api_key", "apikey", "password", "secret", "token:"}
	for _, marker := range sensitiveMarkers {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
