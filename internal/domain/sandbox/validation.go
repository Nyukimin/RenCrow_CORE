package sandbox

import (
	"errors"
	"strings"
)

func ValidateSandboxRecord(item SandboxRecord) error {
	if strings.TrimSpace(item.SandboxID) == "" {
		return errors.New("sandbox_id is required")
	}
	if strings.TrimSpace(item.Type) == "" {
		return errors.New("type is required")
	}
	if strings.TrimSpace(item.Path) == "" {
		return errors.New("path is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidateSandboxArtifact(item SandboxArtifact) error {
	if strings.TrimSpace(item.ArtifactID) == "" {
		return errors.New("artifact_id is required")
	}
	if strings.TrimSpace(item.SandboxID) == "" {
		return errors.New("sandbox_id is required")
	}
	if strings.TrimSpace(item.Type) == "" {
		return errors.New("artifact_type is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return errors.New("file_path is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return errors.New("status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidatePromotionRequest(item PromotionRequest) error {
	if strings.TrimSpace(item.PromotionID) == "" {
		return errors.New("promotion_id is required")
	}
	if strings.TrimSpace(item.SandboxID) == "" {
		return errors.New("sandbox_id is required")
	}
	if strings.TrimSpace(item.TargetPath) == "" {
		return errors.New("target_path is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}

func ValidatePromotionGateLog(item PromotionGateLog) error {
	if strings.TrimSpace(item.EventID) == "" {
		return errors.New("event_id is required")
	}
	if strings.TrimSpace(item.PromotionID) == "" {
		return errors.New("promotion_id is required")
	}
	if strings.TrimSpace(item.GateStatus) == "" {
		return errors.New("gate_status is required")
	}
	if item.CreatedAt.IsZero() {
		return errors.New("created_at is required")
	}
	return nil
}
