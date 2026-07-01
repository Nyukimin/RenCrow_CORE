package knowledgememory

import (
	"fmt"
	"strings"
)

func ValidatePersonalArchiveEntry(item PersonalArchiveEntry) error {
	if strings.TrimSpace(item.EntryID) == "" {
		return fmt.Errorf("entry_id is required")
	}
	if strings.TrimSpace(item.UserID) == "" {
		return fmt.Errorf("user_id is required")
	}
	if strings.TrimSpace(item.OriginalText) == "" {
		return fmt.Errorf("original_text is required")
	}
	if !item.Protected {
		return fmt.Errorf("personal archive original must be protected")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateCreativeKnowledgeItem(item CreativeKnowledgeItem) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return fmt.Errorf("item_id is required")
	}
	if strings.TrimSpace(item.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if !isKnowledgeItemStatus(item.Status) {
		return fmt.Errorf("unsupported creative knowledge status")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateNewsKnowledgeItem(item NewsKnowledgeItem) error {
	if strings.TrimSpace(item.ItemID) == "" {
		return fmt.Errorf("item_id is required")
	}
	if strings.TrimSpace(item.Source) == "" {
		return fmt.Errorf("source is required")
	}
	if strings.TrimSpace(item.Topic) == "" {
		return fmt.Errorf("topic is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if !isKnowledgeItemStatus(item.Status) {
		return fmt.Errorf("unsupported news knowledge status")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateDailyIntakeRule(item DailyIntakeRule) error {
	if strings.TrimSpace(item.RuleID) == "" {
		return fmt.Errorf("rule_id is required")
	}
	if strings.TrimSpace(item.UserID) == "" {
		return fmt.Errorf("user_id is required")
	}
	if strings.TrimSpace(item.Topic) == "" {
		return fmt.Errorf("topic is required")
	}
	if strings.TrimSpace(item.Cadence) == "" {
		return fmt.Errorf("cadence is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if !isDailyIntakeStatus(item.Status) {
		return fmt.Errorf("unsupported daily intake status")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateTemporalMemoryMarker(item TemporalMemoryMarker) error {
	if strings.TrimSpace(item.MarkerID) == "" {
		return fmt.Errorf("marker_id is required")
	}
	switch strings.TrimSpace(item.Layer) {
	case "thread", "today", "3days", "week", "month", "year", "long_term":
	default:
		return fmt.Errorf("unsupported temporal memory layer")
	}
	if strings.TrimSpace(item.ReferenceID) == "" {
		return fmt.Errorf("reference_id is required")
	}
	if strings.TrimSpace(item.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if item.AccessCount < 0 {
		return fmt.Errorf("access_count must be >= 0")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateDreamConsolidationRun(item DreamConsolidationRun) error {
	if strings.TrimSpace(item.RunID) == "" {
		return fmt.Errorf("run_id is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if strings.TrimSpace(item.ReviewStatus) == "" {
		return fmt.Errorf("review_status is required")
	}
	if !isDreamStatus(item.Status) {
		return fmt.Errorf("unsupported dream consolidation status")
	}
	if !isDreamReviewStatus(item.ReviewStatus) {
		return fmt.Errorf("unsupported dream consolidation review_status")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	switch item.ReviewStatus {
	case "pending":
		if item.Status != "draft" && item.Status != "proposal" {
			return fmt.Errorf("dream consolidation pending review requires draft or proposal status")
		}
	case "approved":
		if item.Status != "reviewed" && item.Status != "promoted" {
			return fmt.Errorf("dream consolidation cannot be auto-approved")
		}
	case "rejected":
		if item.Status != "rejected" {
			return fmt.Errorf("dream consolidation rejected review requires rejected status")
		}
	}
	return nil
}

func isKnowledgeItemStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "candidate", "reviewed", "promoted", "rejected":
		return true
	default:
		return false
	}
}

func isDailyIntakeStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "candidate", "reviewed", "enabled", "active", "rejected":
		return true
	default:
		return false
	}
}

func isDreamStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "draft", "proposal", "reviewed", "promoted", "rejected":
		return true
	default:
		return false
	}
}

func isDreamReviewStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case "pending", "approved", "rejected":
		return true
	default:
		return false
	}
}
