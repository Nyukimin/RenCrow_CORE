package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	domainsandbox "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/sandbox"
)

func TestPromotionDiffApplierAppliesUnifiedDiff(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	writeFile(t, filepath.Join(applyRoot, "docs", "example.md"), "one\ntwo\nthree\n")
	diff := `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)

	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)
	result, err := applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	})
	if err != nil {
		t.Fatalf("ApplyPromotionDiff() error = %v", err)
	}
	if result.Status != "applied" || len(result.AppliedFiles) != 1 || result.AppliedFiles[0] != "docs/example.md" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(applyRoot, "docs", "example.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one\nTWO\nthree\n" {
		t.Fatalf("patched content = %q", string(data))
	}
}

func TestPromotionDiffApplierRollsBackAppliedUnifiedDiff(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	writeFile(t, filepath.Join(applyRoot, "docs", "example.md"), "one\ntwo\nthree\n")
	diff := `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)

	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)
	req := domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	}
	if _, err := applier.ApplyPromotionDiff(context.Background(), req); err != nil {
		t.Fatalf("ApplyPromotionDiff() error = %v", err)
	}
	result, err := applier.RollbackPromotionDiff(context.Background(), req)
	if err != nil {
		t.Fatalf("RollbackPromotionDiff() error = %v", err)
	}
	if result.Status != "rolled_back" || len(result.AppliedFiles) != 1 || result.AppliedFiles[0] != "docs/example.md" {
		t.Fatalf("result = %#v", result)
	}
	data, err := os.ReadFile(filepath.Join(applyRoot, "docs", "example.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "one\ntwo\nthree\n" {
		t.Fatalf("rolled back content = %q", string(data))
	}
}

func TestPromotionDiffApplierPreviewsUnifiedDiffSideBySideRows(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	diff := `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	preview, err := applier.PreviewPromotionDiff(context.Background(), domainsandbox.PromotionRequest{
		DiffPath: "diff.patch",
	})
	if err != nil {
		t.Fatalf("PreviewPromotionDiff() error = %v", err)
	}
	if preview.Status != "previewed" || preview.FileCount != 1 || preview.AddedLines != 1 || preview.RemovedLines != 1 {
		t.Fatalf("preview = %#v", preview)
	}
	file := preview.Files[0]
	if file.Path != "docs/example.md" || file.HunkCount != 1 || len(file.Hunks[0].Rows) != 4 {
		t.Fatalf("file preview = %#v", file)
	}
	if file.Hunks[0].Rows[1].Op != "removed" || file.Hunks[0].Rows[1].OldLine != 2 || file.Hunks[0].Rows[1].OldText != "two" {
		t.Fatalf("removed row = %#v", file.Hunks[0].Rows[1])
	}
	if file.Hunks[0].Rows[2].Op != "added" || file.Hunks[0].Rows[2].NewLine != 2 || file.Hunks[0].Rows[2].NewText != "TWO" {
		t.Fatalf("added row = %#v", file.Hunks[0].Rows[2])
	}
}

func TestPromotionDiffApplierPreviewsManualReviewRiskFlags(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	diff := strings.Join([]string{
		"diff --git a/go.mod b/go.mod",
		"--- a/go.mod",
		"+++ b/go.mod",
		"@@ -1,3 +1,4 @@",
		" module example.test/app",
		" ",
		" go 1.22",
		"+require example.test/lib v1.2.3",
		"diff --git a/db/migrations/001_init.sql b/db/migrations/001_init.sql",
		"--- a/db/migrations/001_init.sql",
		"+++ b/db/migrations/001_init.sql",
		"@@ -1 +1 @@",
		"-CREATE TABLE old_items(id TEXT);",
		"+CREATE TABLE items(id TEXT);",
		"",
	}, "\n")
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	preview, err := applier.PreviewPromotionDiff(context.Background(), domainsandbox.PromotionRequest{
		DiffPath: "diff.patch",
	})
	if err != nil {
		t.Fatalf("PreviewPromotionDiff() error = %v", err)
	}
	if preview.Status != "needs_manual_review" || !preview.RequiresManualReview {
		t.Fatalf("preview risk status = %#v", preview)
	}
	if !hasRiskFlag(preview.RiskFlags, "dependency_change") || !hasRiskFlag(preview.RiskFlags, "db_migration") {
		t.Fatalf("risk flags = %#v", preview.RiskFlags)
	}
	if !preview.Files[0].RequiresManualReview || !hasRiskFlag(preview.Files[0].RiskFlags, "dependency_change") {
		t.Fatalf("dependency file risk = %#v", preview.Files[0])
	}
	if !preview.Files[1].RequiresManualReview || !hasRiskFlag(preview.Files[1].RiskFlags, "db_migration") {
		t.Fatalf("migration file risk = %#v", preview.Files[1])
	}
}

func TestPromotionDiffApplierPreviewsUnsupportedDiffAsManualReview(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	diff := `diff --git a/docs/old.md b/docs/new.md
similarity index 100%
rename from docs/old.md
rename to docs/new.md
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	preview, err := applier.PreviewPromotionDiff(context.Background(), domainsandbox.PromotionRequest{
		DiffPath: "diff.patch",
	})
	if err != nil {
		t.Fatalf("PreviewPromotionDiff() error = %v", err)
	}
	if preview.Status != "needs_manual_review" || !preview.RequiresManualReview || !hasRiskFlag(preview.RiskFlags, "rename_diff") {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestPromotionDiffApplierRejectsManualReviewRiskOnApply(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	writeFile(t, filepath.Join(applyRoot, "go.mod"), "module example.test/app\n\ngo 1.22\n")
	diff := strings.Join([]string{
		"diff --git a/go.mod b/go.mod",
		"--- a/go.mod",
		"+++ b/go.mod",
		"@@ -1,3 +1,4 @@",
		" module example.test/app",
		" ",
		" go 1.22",
		"+require example.test/lib v1.2.3",
		"",
	}, "\n")
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	_, err := applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	})
	if err == nil {
		t.Fatal("expected dependency change rejection")
	}
	if !strings.Contains(err.Error(), "requires manual review") || !strings.Contains(err.Error(), "dependency_change") {
		t.Fatalf("err = %v", err)
	}
	data, readErr := os.ReadFile(filepath.Join(applyRoot, "go.mod"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "module example.test/app\n\ngo 1.22\n" {
		t.Fatalf("target was changed: %q", string(data))
	}
}

func TestPromotionDiffApplierRejectsDiffOutsideSandboxRoot(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	_, err := applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "../diff.patch"},
	})
	if err == nil {
		t.Fatal("expected sandbox root rejection")
	}
	if !strings.Contains(err.Error(), "inside sandbox root") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromotionDiffApplierRejectsSecretPath(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	diff := `diff --git a/.env b/.env
--- a/.env
+++ b/.env
@@ -1 +1 @@
-OLD=1
+NEW=1
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	writeFile(t, filepath.Join(applyRoot, ".env"), "OLD=1\n")
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	_, err := applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	})
	if err == nil {
		t.Fatal("expected secret path rejection")
	}
	if !strings.Contains(err.Error(), "secret guard") {
		t.Fatalf("err = %v", err)
	}
}

func TestPromotionDiffApplierRejectsContextMismatchWithoutPartialWrite(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	writeFile(t, filepath.Join(applyRoot, "docs", "example.md"), "one\nchanged\nthree\n")
	diff := `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	_, err := applier.ApplyPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	data, readErr := os.ReadFile(filepath.Join(applyRoot, "docs", "example.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "one\nchanged\nthree\n" {
		t.Fatalf("target was partially changed: %q", string(data))
	}
}

func TestPromotionDiffApplierRollbackRejectsContextMismatchWithoutPartialWrite(t *testing.T) {
	sandboxRoot := t.TempDir()
	applyRoot := t.TempDir()
	writeFile(t, filepath.Join(applyRoot, "docs", "example.md"), "one\nunexpected\nthree\n")
	writeFile(t, filepath.Join(applyRoot, "docs", "other.md"), "alpha\nBETA\n")
	diff := `diff --git a/docs/example.md b/docs/example.md
--- a/docs/example.md
+++ b/docs/example.md
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
diff --git a/docs/other.md b/docs/other.md
--- a/docs/other.md
+++ b/docs/other.md
@@ -1,2 +1,2 @@
 alpha
-beta
+BETA
`
	writeFile(t, filepath.Join(sandboxRoot, "diff.patch"), diff)
	applier := NewPromotionDiffApplier(sandboxRoot, applyRoot)

	_, err := applier.RollbackPromotionDiff(context.Background(), domainsandbox.PromotionApplyRequest{
		Promotion: domainsandbox.PromotionRequest{DiffPath: "diff.patch"},
	})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
	data, readErr := os.ReadFile(filepath.Join(applyRoot, "docs", "other.md"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "alpha\nBETA\n" {
		t.Fatalf("target was partially changed: %q", string(data))
	}
}

func hasRiskFlag(flags []string, want string) bool {
	for _, flag := range flags {
		if flag == want {
			return true
		}
	}
	return false
}

func writeFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
