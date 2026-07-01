package complexity

import (
	"fmt"
	"strings"

	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
)

func BuildPatchProposalMarkdown(hotspot domaincomplexity.Hotspot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Complexity Patch Proposal\n\n")
	fmt.Fprintf(&b, "## Scope\n\n")
	fmt.Fprintf(&b, "- Hotspot: `%s`\n", hotspot.HotspotID)
	fmt.Fprintf(&b, "- File: `%s`\n", hotspot.FilePath)
	if hotspot.LineStart > 0 {
		if hotspot.LineEnd > hotspot.LineStart {
			fmt.Fprintf(&b, "- Lines: %d-%d\n", hotspot.LineStart, hotspot.LineEnd)
		} else {
			fmt.Fprintf(&b, "- Line: %d\n", hotspot.LineStart)
		}
	}
	fmt.Fprintf(&b, "- Type: `%s`\n", hotspot.HotspotType)
	fmt.Fprintf(&b, "- Risk: `%s`\n\n", hotspot.RiskLevel)
	fmt.Fprintf(&b, "## Current Pattern\n\n")
	fmt.Fprintf(&b, "%s\n\n", fallbackText(hotspot.Summary, "No summary was recorded."))
	fmt.Fprintf(&b, "## Proposed Change\n\n")
	fmt.Fprintf(&b, "%s\n\n", fallbackText(hotspot.SuggestedImprovement, "Create a minimal behavior-compatible patch proposal for this hotspot only."))
	if hotspot.EstimatedComplexity != "" || hotspot.EstimatedAfter != "" {
		fmt.Fprintf(&b, "## Complexity Estimate\n\n")
		if hotspot.EstimatedComplexity != "" {
			fmt.Fprintf(&b, "- Before: `%s`\n", hotspot.EstimatedComplexity)
		}
		if hotspot.EstimatedAfter != "" {
			fmt.Fprintf(&b, "- After candidate: `%s`\n", hotspot.EstimatedAfter)
		}
		fmt.Fprintf(&b, "\n")
	}
	fmt.Fprintf(&b, "## Required Tests\n\n")
	if len(hotspot.RequiredTests) == 0 {
		fmt.Fprintf(&b, "- Existing focused tests for `%s`\n", hotspot.FilePath)
		fmt.Fprintf(&b, "- Diff review confirming unrelated files were not changed\n")
	} else {
		for _, test := range hotspot.RequiredTests {
			test = strings.TrimSpace(test)
			if test != "" {
				fmt.Fprintf(&b, "- %s\n", test)
			}
		}
	}
	fmt.Fprintf(&b, "\n## Patch Boundary\n\n")
	fmt.Fprintf(&b, "- Do not apply automatically.\n")
	fmt.Fprintf(&b, "- Do not change unrelated hotspots in the same patch.\n")
	fmt.Fprintf(&b, "- Human approval is required before Coder generates or Worker applies a concrete diff.\n")
	writeExternalPRReviewChecklist(&b, hotspot)
	writeMigrationReviewChecklist(&b, hotspot)
	return b.String()
}

func BuildCoderDiffRequestMarkdown(hotspot domaincomplexity.Hotspot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Coder Concrete Diff Request\n\n")
	fmt.Fprintf(&b, "## Target Hotspot\n\n")
	fmt.Fprintf(&b, "- Hotspot: `%s`\n", hotspot.HotspotID)
	fmt.Fprintf(&b, "- File: `%s`\n", hotspot.FilePath)
	if hotspot.LineStart > 0 {
		if hotspot.LineEnd > hotspot.LineStart {
			fmt.Fprintf(&b, "- Lines: %d-%d\n", hotspot.LineStart, hotspot.LineEnd)
		} else {
			fmt.Fprintf(&b, "- Line: %d\n", hotspot.LineStart)
		}
	}
	fmt.Fprintf(&b, "- Type: `%s`\n", hotspot.HotspotType)
	fmt.Fprintf(&b, "- Risk: `%s`\n\n", hotspot.RiskLevel)
	fmt.Fprintf(&b, "## Coder Task\n\n")
	fmt.Fprintf(&b, "Generate one minimal concrete diff proposal for this hotspot only. Do not apply the diff.\n\n")
	fmt.Fprintf(&b, "## Required Patch Boundary\n\n")
	fmt.Fprintf(&b, "- Keep the change scoped to `%s`.\n", hotspot.FilePath)
	fmt.Fprintf(&b, "- Do not mix unrelated cleanup or other hotspots.\n")
	fmt.Fprintf(&b, "- Preserve behavior unless the Goal Contract explicitly approves a behavior change.\n")
	fmt.Fprintf(&b, "- Include the exact test commands and expected evidence before promotion.\n\n")
	fmt.Fprintf(&b, "## Required Tests\n\n")
	if len(hotspot.RequiredTests) == 0 {
		fmt.Fprintf(&b, "- Existing focused tests for `%s`\n", hotspot.FilePath)
		fmt.Fprintf(&b, "- Diff review confirming unrelated files were not changed\n")
	} else {
		for _, test := range hotspot.RequiredTests {
			test = strings.TrimSpace(test)
			if test != "" {
				fmt.Fprintf(&b, "- %s\n", test)
			}
		}
	}
	fmt.Fprintf(&b, "\n## Promotion Rule\n\n")
	fmt.Fprintf(&b, "The generated diff must enter Sandbox Promotion Gate with diff, test result, rollback plan, and Human approval. A missing item is a failure, not success.\n")
	writeExternalPRReviewChecklist(&b, hotspot)
	writeMigrationReviewChecklist(&b, hotspot)
	return b.String()
}

func BuildConcreteDiffProposalMarkdown(hotspot domaincomplexity.Hotspot, concreteDiff string, testResultPath string, rollbackPlanPath string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Complexity Concrete Diff Proposal\n\n")
	fmt.Fprintf(&b, "## Scope\n\n")
	fmt.Fprintf(&b, "- Hotspot: `%s`\n", hotspot.HotspotID)
	fmt.Fprintf(&b, "- File: `%s`\n", hotspot.FilePath)
	fmt.Fprintf(&b, "- Type: `%s`\n", hotspot.HotspotType)
	fmt.Fprintf(&b, "- Risk: `%s`\n\n", hotspot.RiskLevel)
	fmt.Fprintf(&b, "## Review State\n\n")
	fmt.Fprintf(&b, "- Patch applied: `false`\n")
	fmt.Fprintf(&b, "- Human approval required: `true`\n")
	if strings.TrimSpace(testResultPath) != "" {
		fmt.Fprintf(&b, "- Test result path: `%s`\n", strings.TrimSpace(testResultPath))
	}
	if strings.TrimSpace(rollbackPlanPath) != "" {
		fmt.Fprintf(&b, "- Rollback plan path: `%s`\n", strings.TrimSpace(rollbackPlanPath))
	}
	fmt.Fprintf(&b, "\n## Concrete Diff\n\n")
	fmt.Fprintf(&b, "```diff\n%s\n```\n\n", strings.TrimSpace(concreteDiff))
	fmt.Fprintf(&b, "## Promotion Rule\n\n")
	fmt.Fprintf(&b, "This diff is a review artifact only. Worker must not apply it until Sandbox Promotion Gate has diff path, test result, rollback plan, and Human approval.\n")
	writeExternalPRReviewChecklist(&b, hotspot)
	writeMigrationReviewChecklist(&b, hotspot)
	return b.String()
}

func ValidateConcreteDiffForHotspot(hotspot domaincomplexity.Hotspot, concreteDiff string) error {
	concreteDiff = strings.TrimSpace(concreteDiff)
	if concreteDiff == "" {
		return fmt.Errorf("concrete_diff is required")
	}
	if !looksLikeUnifiedDiff(concreteDiff) {
		return fmt.Errorf("concrete_diff must be unified diff text")
	}
	filePath := strings.TrimSpace(hotspot.FilePath)
	if filePath == "" {
		return fmt.Errorf("hotspot file_path is required")
	}
	if !diffMentionsPath(concreteDiff, filePath) {
		return fmt.Errorf("concrete_diff must be scoped to hotspot file %s", filePath)
	}
	return nil
}

func looksLikeUnifiedDiff(diff string) bool {
	return (strings.Contains(diff, "--- ") && strings.Contains(diff, "+++ ") && strings.Contains(diff, "@@")) ||
		(strings.Contains(diff, "diff --git ") && strings.Contains(diff, "@@"))
}

func diffMentionsPath(diff string, filePath string) bool {
	normalized := strings.TrimPrefix(filepathSlash(filePath), "a/")
	for _, line := range strings.Split(diff, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "diff --git ") || strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			cleaned := strings.ReplaceAll(line, "\\", "/")
			if strings.Contains(cleaned, "a/"+normalized) || strings.Contains(cleaned, "b/"+normalized) || strings.Contains(cleaned, normalized) {
				return true
			}
		}
	}
	return false
}

func filepathSlash(path string) string {
	return strings.ReplaceAll(path, "\\", "/")
}

func writeExternalPRReviewChecklist(b *strings.Builder, hotspot domaincomplexity.Hotspot) {
	fmt.Fprintf(b, "\n## External PR Review Checklist\n\n")
	fmt.Fprintf(b, "- Do not create an external PR from this artifact automatically.\n")
	fmt.Fprintf(b, "- Confirm this is a real observed performance or complexity problem, not a speculative cleanup.\n")
	fmt.Fprintf(b, "- Keep one PR to one hotspot and one intent: `%s`.\n", fallbackText(hotspot.HotspotID, "unknown"))
	fmt.Fprintf(b, "- Search existing open and closed issues / PRs before external submission.\n")
	fmt.Fprintf(b, "- Include complete diff, test result, rollback plan, and risk explanation for human review.\n")
	fmt.Fprintf(b, "- If the change is project-specific, keep it local instead of proposing it as a core or upstream change.\n")
}

func writeMigrationReviewChecklist(b *strings.Builder, hotspot domaincomplexity.Hotspot) {
	fmt.Fprintf(b, "\n## Migration / High-risk Review Checklist\n\n")
	fmt.Fprintf(b, "- Treat DB query rewrites, schema migrations, cache behavior, concurrency, API call ordering, and auth-related changes as high risk.\n")
	fmt.Fprintf(b, "- Separate migration or external integration changes into their own Goal / PR.\n")
	fmt.Fprintf(b, "- Define forward migration, rollback migration, data compatibility, and post-apply verification before promotion.\n")
	fmt.Fprintf(b, "- Do not mix migration review with low-risk lookup or loop refactors.\n")
	if strings.EqualFold(strings.TrimSpace(hotspot.RiskLevel), "high") {
		fmt.Fprintf(b, "- This hotspot is already marked high risk; require a dedicated high-risk review before concrete diff generation.\n")
	}
}

func fallbackText(value string, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
