package complexity

import (
	"strings"
	"testing"
	"time"

	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
)

func TestBuildReportMarkdownIncludesHotspotRiskAndEvidence(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	report := BuildReportMarkdown(domaincomplexity.ScanResult{
		Scan: domaincomplexity.ScanEvent{
			ScanID:        "scan_1",
			Repo:          "repo",
			ScanScope:     []string{"internal"},
			Mode:          "report_only",
			FilesScanned:  1,
			HotspotsFound: 1,
			Status:        "completed",
			CreatedAt:     now,
		},
		Hotspots: []domaincomplexity.Hotspot{{
			HotspotID:            "hot_1",
			ScanID:               "scan_1",
			FilePath:             "internal/app.go",
			LineStart:            10,
			LineEnd:              18,
			HotspotType:          "nested_lookup",
			EstimatedComplexity:  "O(n*m)",
			EstimatedAfter:       "O(n)",
			RiskLevel:            "medium",
			Summary:              "map path contains find lookup",
			SuggestedImprovement: "Map lookup",
			RequiredTests:        []string{"duplicate data"},
			CreatedAt:            now,
		}},
		Evidence: []domaincomplexity.HotspotEvidence{{
			EvidenceID: "ev_1",
			HotspotID:  "hot_1",
			FilePath:   "internal/app.go",
			Snippet:    "items.map(item => users.find(...))",
			Reason:     "map with repeated lookup",
			CreatedAt:  now,
		}},
	})
	for _, want := range []string{"# Complexity Hotspot Report", "nested_lookup", "O(n*m)", "medium", "items.map"} {
		if !strings.Contains(report, want) {
			t.Fatalf("report does not contain %q:\n%s", want, report)
		}
	}
}
