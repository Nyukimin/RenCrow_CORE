package complexity

import (
	"fmt"
	"strings"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
)

func BuildReportMarkdown(result domaincomplexity.ScanResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Complexity Hotspot Report\n\n")
	fmt.Fprintf(&b, "## Summary\n\n")
	fmt.Fprintf(&b, "- Scan scope: %s\n", strings.Join(result.Scan.ScanScope, ", "))
	fmt.Fprintf(&b, "- Files scanned: %d\n", result.Scan.FilesScanned)
	fmt.Fprintf(&b, "- Hotspots found: %d\n", len(result.Hotspots))
	fmt.Fprintf(&b, "- Status: %s\n\n", result.Scan.Status)
	fmt.Fprintf(&b, "## Hotspots\n\n")
	if len(result.Hotspots) == 0 {
		fmt.Fprintf(&b, "No hotspots were detected.\n")
		return b.String()
	}
	evidenceByHotspot := map[string][]domaincomplexity.HotspotEvidence{}
	for _, ev := range result.Evidence {
		evidenceByHotspot[ev.HotspotID] = append(evidenceByHotspot[ev.HotspotID], ev)
	}
	for i, hotspot := range result.Hotspots {
		fmt.Fprintf(&b, "### %d. %s:%d-%d\n\n", i+1, hotspot.FilePath, hotspot.LineStart, hotspot.LineEnd)
		fmt.Fprintf(&b, "- Type: %s\n", hotspot.HotspotType)
		fmt.Fprintf(&b, "- Current complexity: %s\n", hotspot.EstimatedComplexity)
		if hotspot.EstimatedAfter != "" {
			fmt.Fprintf(&b, "- Possible after: %s\n", hotspot.EstimatedAfter)
		}
		fmt.Fprintf(&b, "- Risk: %s\n", hotspot.RiskLevel)
		fmt.Fprintf(&b, "- Summary: %s\n", hotspot.Summary)
		if hotspot.SuggestedImprovement != "" {
			fmt.Fprintf(&b, "- Possible improvement: %s\n", hotspot.SuggestedImprovement)
		}
		if len(hotspot.RequiredTests) > 0 {
			fmt.Fprintf(&b, "- Required tests: %s\n", strings.Join(hotspot.RequiredTests, ", "))
		}
		for _, ev := range evidenceByHotspot[hotspot.HotspotID] {
			fmt.Fprintf(&b, "\nEvidence:\n\n```text\n%s\n```\n", ev.Snippet)
			if ev.Reason != "" {
				fmt.Fprintf(&b, "\nReason: %s\n", ev.Reason)
			}
		}
		fmt.Fprintf(&b, "\n")
	}
	return b.String()
}
