package packagevalidation

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateUpdateBlocksPackagePathsWithoutApprovalAndRollback(t *testing.T) {
	root := t.TempDir()
	service := NewService(root)

	report, err := service.ValidateUpdate(context.Background(), ValidationRequest{
		Paths: []string{"go.mod", "internal/app.go"},
	})
	if err != nil {
		t.Fatalf("ValidateUpdate() error = %v", err)
	}
	if report.Status != "blocked" || report.InstallAllowed {
		t.Fatalf("unexpected report: %#v", report)
	}
	if len(report.PackagePaths) != 1 || report.PackagePaths[0] != "go.mod" {
		t.Fatalf("package paths = %#v", report.PackagePaths)
	}
	if !containsString(report.MissingRequirements, "human_approved") || !containsString(report.MissingRequirements, "rollback_evidence_path") {
		t.Fatalf("missing requirements = %#v", report.MissingRequirements)
	}
}

func TestValidateUpdateAllowsPackagePathWithApprovalAndRollbackEvidence(t *testing.T) {
	root := t.TempDir()
	rollback := filepath.Join(root, "reports", "rollback.md")
	if err := os.MkdirAll(filepath.Dir(rollback), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(rollback, []byte("rollback evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(root)

	report, err := service.ValidateUpdate(context.Background(), ValidationRequest{
		Paths:                []string{"pyproject.toml"},
		RollbackEvidencePath: "reports/rollback.md",
		HumanApproved:        true,
	})
	if err != nil {
		t.Fatalf("ValidateUpdate() error = %v", err)
	}
	if report.Status != "manual_review_satisfied" || !report.InstallAllowed || !report.RequiresManualReview {
		t.Fatalf("unexpected report: %#v", report)
	}
	if report.RollbackEvidencePath != "reports/rollback.md" {
		t.Fatalf("rollback evidence path = %q", report.RollbackEvidencePath)
	}
}

func TestValidateUpdateAllowsNonPackagePaths(t *testing.T) {
	service := NewService(t.TempDir())

	report, err := service.ValidateUpdate(context.Background(), ValidationRequest{
		Paths: []string{"internal/app.go", "docs/spec.md"},
	})
	if err != nil {
		t.Fatalf("ValidateUpdate() error = %v", err)
	}
	if report.Status != "allowed" || !report.InstallAllowed || report.RequiresManualReview {
		t.Fatalf("unexpected report: %#v", report)
	}
}

func TestValidateUpdateRejectsWorkspaceEscape(t *testing.T) {
	service := NewService(t.TempDir())

	if _, err := service.ValidateUpdate(context.Background(), ValidationRequest{
		Paths: []string{"../outside/go.mod"},
	}); err == nil {
		t.Fatal("expected workspace escape to fail")
	}
}

func containsString(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
