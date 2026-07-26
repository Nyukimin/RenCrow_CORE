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
		"human" + "_adopted",
		"human" + "adopted",
		"human" + "-decision-gate",
		"human" + "decisiongate",
		"human" + " decision gate",
		"人間" + "承認",
		"承認" + "待ち",
		"承認" + "ゲート",
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
		"rules",
		"scripts",
		"skills/core",
		"test/e2e",
		"tools",
		"AGENTS.md",
		"CLAUDE.md",
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
		path == "rencrow-data/approvals" ||
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
	base := filepath.Base(path)
	return strings.HasPrefix(base, "RELEASE_NOTES") ||
		strings.HasPrefix(base, "refactor") ||
		strings.HasPrefix(base, "phase") ||
		strings.HasSuffix(base, ".db") ||
		strings.HasSuffix(base, ".pyc")
}
