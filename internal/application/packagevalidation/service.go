package packagevalidation

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type ValidationRequest struct {
	Paths                []string `json:"paths"`
	RollbackEvidencePath string   `json:"rollback_evidence_path,omitempty"`
	HumanApproved        bool     `json:"human_approved"`
	RequestedBy          string   `json:"requested_by,omitempty"`
	Reason               string   `json:"reason,omitempty"`
}

type ValidationReport struct {
	Status               string   `json:"status"`
	InstallAllowed       bool     `json:"install_allowed"`
	RequiresManualReview bool     `json:"requires_manual_review"`
	RiskFlags            []string `json:"risk_flags,omitempty"`
	PackagePaths         []string `json:"package_paths,omitempty"`
	ValidatedPaths       []string `json:"validated_paths"`
	RollbackEvidencePath string   `json:"rollback_evidence_path,omitempty"`
	MissingRequirements  []string `json:"missing_requirements,omitempty"`
	RequestedBy          string   `json:"requested_by,omitempty"`
	Reason               string   `json:"reason,omitempty"`
	CheckedAt            string   `json:"checked_at"`
}

type Service struct {
	workspaceRoot string
	clock         func() time.Time
}

func NewService(workspaceRoot string) *Service {
	return &Service{
		workspaceRoot: workspaceRoot,
		clock:         func() time.Time { return time.Now().UTC() },
	}
}

func (s *Service) ValidateUpdate(_ context.Context, req ValidationRequest) (ValidationReport, error) {
	if s == nil {
		return ValidationReport{}, fmt.Errorf("package validation service unavailable")
	}
	root, err := resolveRoot(s.workspaceRoot)
	if err != nil {
		return ValidationReport{}, err
	}
	paths, err := validatePathsInside(root, req.Paths)
	if err != nil {
		return ValidationReport{}, err
	}
	if len(paths) == 0 {
		return ValidationReport{}, fmt.Errorf("paths is required")
	}

	report := ValidationReport{
		Status:         "allowed",
		InstallAllowed: true,
		ValidatedPaths: paths,
		RequestedBy:    strings.TrimSpace(req.RequestedBy),
		Reason:         strings.TrimSpace(req.Reason),
		CheckedAt:      s.clock().UTC().Format(time.RFC3339),
	}
	for _, path := range paths {
		if isPackageDefinitionPath(path) {
			report.PackagePaths = append(report.PackagePaths, path)
			report.RiskFlags = appendUnique(report.RiskFlags, "dependency_change")
		}
	}
	if len(report.PackagePaths) == 0 {
		return report, nil
	}

	report.RequiresManualReview = true
	if !req.HumanApproved {
		report.MissingRequirements = append(report.MissingRequirements, "human_approved")
	}
	if strings.TrimSpace(req.RollbackEvidencePath) == "" {
		report.MissingRequirements = append(report.MissingRequirements, "rollback_evidence_path")
	} else {
		rollbackPath, err := validatePathInside(root, req.RollbackEvidencePath)
		if err != nil {
			return ValidationReport{}, fmt.Errorf("rollback_evidence_path: %w", err)
		}
		if _, err := os.Stat(filepath.Join(root, rollbackPath)); err != nil {
			return ValidationReport{}, fmt.Errorf("rollback_evidence_path must exist: %w", err)
		}
		report.RollbackEvidencePath = rollbackPath
	}
	if len(report.MissingRequirements) > 0 {
		report.Status = "blocked"
		report.InstallAllowed = false
		return report, nil
	}
	report.Status = "manual_review_satisfied"
	report.InstallAllowed = true
	return report, nil
}

func resolveRoot(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", fmt.Errorf("workspace root is required")
	}
	abs, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return "", fmt.Errorf("resolve workspace root: %w", err)
	}
	return abs, nil
}

func validatePathsInside(root string, paths []string) ([]string, error) {
	out := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		clean, err := validatePathInside(root, path)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	return out, nil
}

func validatePathInside(root string, path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}
	if filepath.IsAbs(path) {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	clean := filepath.Clean(path)
	if clean == "." || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("path must stay inside workspace")
	}
	target := filepath.Join(root, clean)
	abs, err := filepath.Abs(filepath.Clean(target))
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", fmt.Errorf("check path: %w", err)
	}
	if rel == "." || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path must stay inside workspace")
	}
	return filepath.ToSlash(rel), nil
}

func isPackageDefinitionPath(path string) bool {
	base := filepath.Base(filepath.ToSlash(path))
	switch base {
	case "go.mod", "go.sum", "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb", "requirements.txt", "pyproject.toml", "poetry.lock", "uv.lock", "Cargo.toml", "Cargo.lock":
		return true
	default:
		return false
	}
}

func appendUnique(existing []string, value string) []string {
	for _, item := range existing {
		if item == value {
			return existing
		}
	}
	return append(existing, value)
}
