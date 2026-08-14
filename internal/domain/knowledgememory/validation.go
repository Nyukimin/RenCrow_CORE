package knowledgememory

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	// Creative candidate limits keep the model-owned payload bounded before it
	// reaches the durable knowledge-memory store. Limits are expressed in
	// Unicode code points so multi-byte UTF-8 text is treated consistently.
	MaxCreativeCandidateTitleRunes     = 512
	MaxCreativeCandidateWorkTypeRunes  = 128
	MaxCreativeCandidateEntryRunes     = 512
	MaxCreativeCandidateArrayItems     = 32
	MaxCreativeCandidateTotalTextRunes = 8192
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

// ValidateCreativeCandidate validates the private, candidate-only shape used
// by the authenticated knowledge-memory owner route. Identity, status and
// visibility are owner-controlled values and therefore are checked here in
// addition to the general creative knowledge validation.
func ValidateCreativeCandidate(item CreativeKnowledgeItem) error {
	if err := ValidateCreativeKnowledgeItem(item); err != nil {
		return err
	}
	if strings.TrimSpace(item.UserID) == "" {
		return fmt.Errorf("user_id is required for creative candidate")
	}
	if strings.TrimSpace(item.Status) != "candidate" {
		return fmt.Errorf("creative candidate status must be candidate")
	}
	if strings.TrimSpace(item.Visibility) != "private" {
		return fmt.Errorf("creative candidate visibility must be private")
	}
	if err := validateCreativeCandidateText("item_id", item.ItemID, MaxCreativeCandidateEntryRunes, true); err != nil {
		return err
	}
	if err := validateCreativeCandidateText("user_id", item.UserID, MaxCreativeCandidateEntryRunes, true); err != nil {
		return err
	}
	if err := validateCreativeCandidateText("title", item.Title, MaxCreativeCandidateTitleRunes, true); err != nil {
		return err
	}
	if err := validateCreativeCandidateText("work_type", item.WorkType, MaxCreativeCandidateWorkTypeRunes, false); err != nil {
		return err
	}
	if err := validateCreativeCandidateEntries("creator_names", item.CreatorNames); err != nil {
		return err
	}
	if err := validateCreativeCandidateEntries("related_works", item.RelatedWorks); err != nil {
		return err
	}
	if err := validateCreativeCandidateEntries("content_hints", item.ContentHints); err != nil {
		return err
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}

	totalTextRunes := utf8.RuneCountInString(strings.TrimSpace(item.Title)) +
		utf8.RuneCountInString(strings.TrimSpace(item.WorkType))
	for _, values := range [][]string{item.CreatorNames, item.RelatedWorks, item.ContentHints} {
		for _, value := range values {
			totalTextRunes += utf8.RuneCountInString(strings.TrimSpace(value))
		}
	}
	if totalTextRunes > MaxCreativeCandidateTotalTextRunes {
		return fmt.Errorf("creative candidate text exceeds %d characters", MaxCreativeCandidateTotalTextRunes)
	}
	return nil
}

func validateCreativeCandidateEntries(field string, values []string) error {
	if len(values) > MaxCreativeCandidateArrayItems {
		return fmt.Errorf("%s exceeds %d entries", field, MaxCreativeCandidateArrayItems)
	}
	for index, value := range values {
		if err := validateCreativeCandidateText(fmt.Sprintf("%s[%d]", field, index), value, MaxCreativeCandidateEntryRunes, true); err != nil {
			return err
		}
	}
	return nil
}

func validateCreativeCandidateText(field, value string, maxRunes int, required bool) error {
	if !utf8.ValidString(value) {
		return fmt.Errorf("%s must be valid UTF-8", field)
	}
	value = strings.TrimSpace(value)
	if required && value == "" {
		return fmt.Errorf("%s is required", field)
	}
	if utf8.RuneCountInString(value) > maxRunes {
		return fmt.Errorf("%s exceeds %d characters", field, maxRunes)
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
	case "adopted":
		if item.Status != "reviewed" && item.Status != "promoted" {
			return fmt.Errorf("dream consolidation cannot be auto-adopted")
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
	case "pending", "adopted", "rejected":
		return true
	default:
		return false
	}
}
