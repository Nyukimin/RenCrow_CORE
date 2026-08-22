package modules

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestActiveRepositoryHasNoApprovalContract(t *testing.T) {
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	root := filepath.Dir(filepath.Dir(currentFile))
	forbidden := []string{
		"approval",
		"approver",
		"decision_ref",
		"owner-decision",
		"awaiting_human",
		"pending_human",
		"wait_for_human",
		"human_review_required",
		"manual_review_required",
		"user_approval",
		"human" + "_adopted",
		"human" + "adopted",
		"human" + "-decision-gate",
		"human" + "decisiongate",
		"human" + " decision gate",
		"承" + "認",
	}
	roots := []string{
		"cmd",
		"config",
		"docs",
		"internal",
		"modules",
		"pkg",
		"prompts",
		"rencrow-data",
		"scripts",
		"test/e2e",
		"tools",
		"Makefile",
		"README.md",
		"README.ja.md",
		"README.zh.md",
		"ROADMAP.md",
		"SECURITY.md",
		"TOOL_CONTRACT.md",
	}
	var violations []string
	for _, relative := range roots {
		path := filepath.Join(root, relative)
		info, err := os.Stat(path)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatalf("stat %s: %v", relative, err)
		}
		if !info.IsDir() {
			if hasForbiddenApprovalTerm(t, path, forbidden) {
				violations = append(violations, relative)
			}
			continue
		}
		err = filepath.WalkDir(path, func(itemPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			itemRelative, err := filepath.Rel(root, itemPath)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				if isApprovalGuardExcludedDirectory(itemRelative) {
					return filepath.SkipDir
				}
				return nil
			}
			if isApprovalGuardExcludedFile(itemRelative) {
				return nil
			}
			if hasForbiddenApprovalTerm(t, itemPath, forbidden) {
				violations = append(violations, itemRelative)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", relative, err)
		}
	}
	if len(violations) > 0 {
		t.Fatalf("active repository still contains approval contracts:\n%s", strings.Join(violations, "\n"))
	}
}

func TestCanonicalPolicyRequiresReconsiderationAfterRejection(t *testing.T) {
	root := repositoryRoot(t)
	assertFileContainsAll(t, filepath.Join(root, "docs", "01_システム概要.md"), []string{
		"No-Human-Gate",
		"人の判断待ちを作らない",
		"再考",
	})
	assertFileContainsAll(t, filepath.Join(root, "docs", "07_安全・自動実行・データ方針.md"), []string{
		"Reject-Driven Revision",
		"rejection_reason",
		"parent_attempt_id",
		"changed_dimensions",
		"思想",
		"同じ案",
		"blocked",
	})
}

func TestCodexAndRenCrowAuthorityBoundariesStaySeparated(t *testing.T) {
	root := repositoryRoot(t)
	assertFileContainsAll(t, filepath.Join(root, "AGENTS.md"), []string{
		"Codex User Authorization",
		"ユーザーの明示指示",
		"RenCrow No-Human-Gate",
		"人の判断待ちを作らない",
	})
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve repository root")
	}
	return filepath.Dir(filepath.Dir(currentFile))
}

func assertFileContainsAll(t *testing.T, path string, required []string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := string(body)
	for _, token := range required {
		if !strings.Contains(text, token) {
			t.Errorf("%s must contain %q", path, token)
		}
	}
}

func hasForbiddenApprovalTerm(t *testing.T, path string, forbidden []string) bool {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	text := strings.ToLower(string(body))
	for _, token := range forbidden {
		if strings.Contains(text, strings.ToLower(token)) {
			return true
		}
	}
	return false
}

func isApprovalGuardExcludedDirectory(path string) bool {
	path = filepath.ToSlash(path)
	return path == "docs/調査" ||
		path == "rencrow-data/data" ||
		path == "rencrow-data/reports" ||
		strings.HasSuffix(path, "/__pycache__") ||
		strings.Contains(path, "/archive/") ||
		strings.HasPrefix(path, "internal/application/idlechat/archive/")
}

func isApprovalGuardExcludedFile(path string) bool {
	path = filepath.ToSlash(path)
	if strings.HasSuffix(path, "modules/no_approval_contract_test.go") {
		return true
	}
	// The Atlas backfill package is immutable design-history evidence. Its source
	// wording is displayed read-only and is never interpreted as a runtime gate.
	if strings.HasPrefix(path, "internal/features/backlog/backfill/") ||
		path == "internal/features/backlog/testdata/atlas_backfill_v1.json" {
		return true
	}
	base := filepath.Base(path)
	return strings.HasPrefix(base, "RELEASE_NOTES") ||
		strings.HasPrefix(base, "refactor") ||
		strings.HasPrefix(base, "phase") ||
		strings.HasSuffix(base, ".db") ||
		strings.HasSuffix(base, ".pyc")
}
