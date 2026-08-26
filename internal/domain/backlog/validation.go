package backlog

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
)

// IsValidMaturationBypassReason reports whether a forced early decision uses
// one of the explicitly accepted emergency reason codes.
func IsValidMaturationBypassReason(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case MaturationBypassSecurityIssue, MaturationBypassDataLossRisk,
		MaturationBypassProductionFailure, MaturationBypassBreakingChange,
		MaturationBypassBugFix, MaturationBypassRuntimeContinuity:
		return true
	default:
		return false
	}
}

func ValidateSourceRef(ref SourceRef) error {
	if strings.TrimSpace(ref.Type) == "" {
		return errors.New("source ref type is required")
	}
	if strings.TrimSpace(ref.Locator) == "" {
		return errors.New("source ref locator is required")
	}
	return nil
}

func ValidateEvidenceRef(ref EvidenceRef) error {
	if strings.TrimSpace(ref.Stage) == "" && strings.TrimSpace(ref.Kind) == "" {
		return errors.New("evidence stage or kind is required")
	}
	if strings.TrimSpace(ref.Ref) == "" {
		return errors.New("evidence ref is required")
	}
	return nil
}

func ValidateRevalidationRecord(record RevalidationRecord) error {
	if strings.TrimSpace(record.BacklogID) == "" {
		return errors.New("revalidation backlog_id is required")
	}
	if strings.TrimSpace(record.RevalidationDate) == "" {
		return errors.New("revalidation date is required")
	}
	if record.MaturationDays < 0 {
		return errors.New("revalidation maturation_days must not be negative")
	}
	switch strings.ToUpper(strings.TrimSpace(record.Decision)) {
	case RevalidationDecisionPromote, RevalidationDecisionMerge, RevalidationDecisionHold, RevalidationDecisionDrop:
	default:
		return errors.New("invalid revalidation decision")
	}
	if strings.TrimSpace(record.Reason) == "" {
		return errors.New("revalidation reason is required")
	}
	if strings.EqualFold(strings.TrimSpace(record.Decision), RevalidationDecisionMerge) && strings.TrimSpace(record.MergedInto) == "" {
		return errors.New("merged revalidation requires merged_into")
	}
	if strings.EqualFold(strings.TrimSpace(record.Decision), RevalidationDecisionHold) && strings.TrimSpace(record.NextReviewTrigger) == "" {
		return errors.New("HOLD revalidation requires next_review_trigger")
	}
	if len(record.ReviewAgents) == 0 {
		return errors.New("revalidation review_agents are required")
	}
	for _, agent := range record.ReviewAgents {
		if strings.TrimSpace(agent) == "" {
			return errors.New("revalidation review agent is required")
		}
	}
	if record.Forced && strings.TrimSpace(record.BypassReason) != "" && !IsValidMaturationBypassReason(record.BypassReason) {
		return errors.New("invalid maturation bypass reason")
	}
	if record.MaturationBypass {
		if !record.Forced || strings.TrimSpace(record.BypassReason) == "" {
			return errors.New("maturation bypass reason is required")
		}
		if !IsValidMaturationBypassReason(record.BypassReason) {
			return errors.New("invalid maturation bypass reason")
		}
	}
	return nil
}

func ValidateSpecificationArtifact(artifact SpecificationArtifact) error {
	if strings.TrimSpace(artifact.SpecID) == "" {
		return errors.New("specification spec_id is required")
	}
	if artifact.BodyAvailable && strings.TrimSpace(artifact.Content) == "" {
		return errors.New("available specification body is required")
	}
	if strings.TrimSpace(artifact.ContentSHA256) != "" && artifact.Content != "" {
		hash := sha256.Sum256([]byte(artifact.Content))
		if !strings.EqualFold(hex.EncodeToString(hash[:]), strings.TrimSpace(artifact.ContentSHA256)) {
			return errors.New("specification content hash mismatch")
		}
	}
	return nil
}

func ValidateBackfillImportReceipt(receipt BackfillImportReceipt) error {
	if strings.TrimSpace(receipt.RecordType) != "atlas_backfill_import" {
		return errors.New("invalid backfill receipt record type")
	}
	if strings.TrimSpace(receipt.ImportID) == "" || strings.TrimSpace(receipt.DatasetID) == "" || strings.TrimSpace(receipt.PackageSHA256) == "" {
		return errors.New("backfill receipt identity is required")
	}
	if receipt.Revision < 1 || receipt.ItemCount < 0 || receipt.SpecificationCount < 0 {
		return errors.New("invalid backfill receipt counters")
	}
	return nil
}

func ValidateItem(item Item) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return errors.New("item_id is required")
	}
	if strings.TrimSpace(item.Title) == "" {
		return errors.New("title is required")
	}
	if item.SchemaVersion >= SchemaVersion2 {
		if !validConceptState(item.ConceptState) {
			return errors.New("invalid concept_state")
		}
		if !validDeliveryState(item.DeliveryState) {
			return errors.New("invalid delivery_state")
		}
		if strings.TrimSpace(item.MaturationState) != "" && !validMaturationState(item.MaturationState) {
			return errors.New("invalid maturation_state")
		}
	}
	for _, ref := range item.SourceRefs {
		if err := ValidateSourceRef(ref); err != nil {
			return err
		}
	}
	for _, ref := range item.EvidenceRefs {
		if err := ValidateEvidenceRef(ref); err != nil {
			return err
		}
	}
	for _, record := range item.RevalidationRecords {
		if err := ValidateRevalidationRecord(record); err != nil {
			return err
		}
	}
	return nil
}
