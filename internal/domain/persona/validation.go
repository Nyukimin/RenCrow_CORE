package persona

import (
	"errors"
	"strings"
)

func ValidateDiscomfortLog(item DiscomfortLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.CharacterID) == "" {
		return errors.New("character_id is required")
	}
	if strings.TrimSpace(item.Discomfort) == "" {
		return errors.New("discomfort is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateTriggerLog(item TriggerLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.CharacterID) == "" {
		return errors.New("character_id is required")
	}
	if strings.TrimSpace(item.TriggerID) == "" {
		return errors.New("trigger_id is required")
	}
	if item.Confidence < 0 || item.Confidence > 1 {
		return errors.New("confidence must be between 0 and 1")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateCanonicalResponseLog(item CanonicalResponseLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.CharacterID) == "" {
		return errors.New("character_id is required")
	}
	if strings.TrimSpace(item.ResponseID) == "" {
		return errors.New("response_id is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateObservationLog(item ObservationLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.ObserverID) == "" {
		return errors.New("observer_id is required")
	}
	if strings.TrimSpace(item.TargetID) == "" {
		return errors.New("target_id is required")
	}
	if strings.TrimSpace(item.ObservationType) == "" {
		return errors.New("observation_type is required")
	}
	if strings.TrimSpace(item.Sensitivity) == "" {
		return errors.New("sensitivity is required")
	}
	if strings.TrimSpace(item.ReviewStatus) == "" {
		return errors.New("review_status is required")
	}
	if item.Sensitivity != "normal" && item.ReviewStatus == "approved" {
		return errors.New("sensitive observation cannot be auto-approved")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateMetaProfileUpdate(item MetaProfileUpdate) error {
	if strings.TrimSpace(item.UpdateID) == "" {
		return errors.New("update_id is required")
	}
	if strings.TrimSpace(item.ObserverID) == "" {
		return errors.New("observer_id is required")
	}
	if strings.TrimSpace(item.TargetID) == "" {
		return errors.New("target_id is required")
	}
	if strings.TrimSpace(item.Section) == "" {
		return errors.New("section is required")
	}
	if strings.TrimSpace(item.ProposedContent) == "" {
		return errors.New("proposed_content is required")
	}
	if strings.TrimSpace(item.Sensitivity) == "" {
		return errors.New("sensitivity is required")
	}
	switch strings.TrimSpace(item.ReviewStatus) {
	case "pending", "approved", "rejected":
		if item.CreatedAt.IsZero() {
			return errors.New("created_at is required")
		}
		return nil
	default:
		return errors.New("review_status must be pending, approved, or rejected")
	}
}

func ValidateMetaProfileUpdateReview(item MetaProfileUpdate) error {
	if err := ValidateMetaProfileUpdate(item); err != nil {
		return err
	}
	switch strings.TrimSpace(item.ReviewStatus) {
	case "approved", "rejected":
		return nil
	default:
		return errors.New("review_status must be approved or rejected")
	}
}

func ValidateInterfaceSession(item InterfaceSession) error {
	if strings.TrimSpace(item.SessionID) == "" {
		return errors.New("session_id is required")
	}
	if strings.TrimSpace(item.CharacterID) == "" {
		return errors.New("character_id is required")
	}
	if strings.TrimSpace(item.InterfaceType) == "" {
		return errors.New("interface_type is required")
	}
	if strings.TrimSpace(item.SessionKey) == "" {
		return errors.New("session_key is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}
