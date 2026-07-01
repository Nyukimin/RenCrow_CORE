package complexity

import (
	"fmt"
	"strings"
)

func ValidateScanEvent(item ScanEvent) error {
	if strings.TrimSpace(item.ScanID) == "" {
		return fmt.Errorf("scan_id is required")
	}
	if strings.TrimSpace(item.Repo) == "" {
		return fmt.Errorf("repo is required")
	}
	if strings.TrimSpace(item.Mode) == "" {
		return fmt.Errorf("mode is required")
	}
	if item.Mode != "report_only" {
		return fmt.Errorf("mode must be report_only")
	}
	if item.FilesScanned < 0 || item.HotspotsFound < 0 {
		return fmt.Errorf("scan counts must be >= 0")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	if item.Status == "completed" && item.CompletedAt.IsZero() {
		return fmt.Errorf("completed_at is required for completed scan")
	}
	return nil
}

func ValidateHotspot(item Hotspot) error {
	if strings.TrimSpace(item.HotspotID) == "" {
		return fmt.Errorf("hotspot_id is required")
	}
	if strings.TrimSpace(item.ScanID) == "" {
		return fmt.Errorf("scan_id is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return fmt.Errorf("file_path is required")
	}
	if strings.TrimSpace(item.HotspotType) == "" {
		return fmt.Errorf("hotspot_type is required")
	}
	if strings.TrimSpace(item.EstimatedComplexity) == "" {
		return fmt.Errorf("estimated_complexity is required")
	}
	if strings.TrimSpace(item.RiskLevel) == "" {
		return fmt.Errorf("risk_level is required")
	}
	if item.LineStart < 0 || item.LineEnd < 0 {
		return fmt.Errorf("line range must be >= 0")
	}
	if item.LineStart > 0 && item.LineEnd > 0 && item.LineEnd < item.LineStart {
		return fmt.Errorf("line_end must be >= line_start")
	}
	if item.Confidence < 0 || item.Confidence > 1 {
		return fmt.Errorf("confidence must be between 0 and 1")
	}
	if item.PriorityScore < 0 || item.PriorityScore > 1 {
		return fmt.Errorf("priority_score must be between 0 and 1")
	}
	if strings.TrimSpace(item.Summary) == "" {
		return fmt.Errorf("summary is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateHotspotEvidence(item HotspotEvidence) error {
	if strings.TrimSpace(item.EvidenceID) == "" {
		return fmt.Errorf("evidence_id is required")
	}
	if strings.TrimSpace(item.HotspotID) == "" {
		return fmt.Errorf("hotspot_id is required")
	}
	if strings.TrimSpace(item.FilePath) == "" {
		return fmt.Errorf("file_path is required")
	}
	if item.LineStart < 0 || item.LineEnd < 0 {
		return fmt.Errorf("line range must be >= 0")
	}
	if item.LineStart > 0 && item.LineEnd > 0 && item.LineEnd < item.LineStart {
		return fmt.Errorf("line_end must be >= line_start")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}

func ValidateReportArtifact(item ReportArtifact) error {
	if strings.TrimSpace(item.ArtifactID) == "" {
		return fmt.Errorf("artifact_id is required")
	}
	if strings.TrimSpace(item.ScanID) == "" {
		return fmt.Errorf("scan_id is required")
	}
	if strings.TrimSpace(item.Type) == "" {
		return fmt.Errorf("artifact_type is required")
	}
	if strings.TrimSpace(item.Title) == "" {
		return fmt.Errorf("title is required")
	}
	if strings.TrimSpace(item.Status) == "" {
		return fmt.Errorf("status is required")
	}
	if strings.TrimSpace(item.Content) == "" {
		return fmt.Errorf("content is required")
	}
	if item.CreatedAt.IsZero() {
		return fmt.Errorf("created_at is required")
	}
	return nil
}
