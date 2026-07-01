package skillgovernance

import (
	"errors"
	"strings"
)

func ValidateSkillManifest(item SkillManifest) error {
	if strings.TrimSpace(item.SkillID) == "" {
		return errors.New("skill_id is required")
	}
	if strings.TrimSpace(item.Name) == "" {
		return errors.New("name is required")
	}
	if strings.TrimSpace(item.Scope) == "" {
		return errors.New("scope is required")
	}
	if strings.TrimSpace(item.Path) == "" {
		return errors.New("path is required")
	}
	if item.UpdatedAt.IsZero() {
		return errors.New("updated_at is required")
	}
	return nil
}

func ValidateSkillTriggerLog(item SkillTriggerLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.SkillID) == "" {
		return errors.New("skill_id is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateSkillChangeLog(item SkillChangeLog) error {
	if strings.TrimSpace(item.ChangeID) == "" {
		return errors.New("change_id is required")
	}
	if strings.TrimSpace(item.SkillID) == "" {
		return errors.New("skill_id is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateContributionGateLog(item ContributionGateLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.Repo) == "" {
		return errors.New("repo is required")
	}
	if strings.TrimSpace(item.GateStatus) == "" {
		return errors.New("gate_status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateCoderTranscriptEntry(item CoderTranscriptEntry) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.Role) == "" {
		return errors.New("role is required")
	}
	if strings.TrimSpace(item.Segment) == "" {
		return errors.New("segment is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}
