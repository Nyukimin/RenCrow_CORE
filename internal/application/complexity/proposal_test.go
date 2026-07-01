package complexity

import (
	"strings"
	"testing"

	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
)

func TestValidateConcreteDiffForHotspotRequiresUnifiedDiffScopedToFile(t *testing.T) {
	hotspot := domaincomplexity.Hotspot{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "lookup",
	}
	diff := `diff --git a/internal/application/example.go b/internal/application/example.go
--- a/internal/application/example.go
+++ b/internal/application/example.go
@@ -1,3 +1,3 @@
-old
+new
`
	if err := ValidateConcreteDiffForHotspot(hotspot, diff); err != nil {
		t.Fatalf("expected valid diff: %v", err)
	}
	if err := ValidateConcreteDiffForHotspot(hotspot, "not a diff"); err == nil {
		t.Fatal("expected invalid diff error")
	}
	other := strings.ReplaceAll(diff, "internal/application/example.go", "internal/application/other.go")
	if err := ValidateConcreteDiffForHotspot(hotspot, other); err == nil {
		t.Fatal("expected wrong file scope error")
	}
}

func TestBuildConcreteDiffProposalMarkdownIsReviewOnly(t *testing.T) {
	hotspot := domaincomplexity.Hotspot{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "internal/application/example.go",
		HotspotType:         "nested_lookup",
		EstimatedComplexity: "O(n*m)",
		RiskLevel:           "medium",
		Summary:             "lookup",
	}
	diff := `--- a/internal/application/example.go
+++ b/internal/application/example.go
@@ -1 +1 @@
-old
+new`
	body := BuildConcreteDiffProposalMarkdown(hotspot, diff, "sandbox/reports/test.txt", "sandbox/reports/rollback.md")
	for _, want := range []string{
		"Complexity Concrete Diff Proposal",
		"Patch applied: `false`",
		"Human approval required: `true`",
		"```diff",
		"Sandbox Promotion Gate",
		"External PR Review Checklist",
		"sandbox/reports/test.txt",
		"sandbox/reports/rollback.md",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in:\n%s", want, body)
		}
	}
}
