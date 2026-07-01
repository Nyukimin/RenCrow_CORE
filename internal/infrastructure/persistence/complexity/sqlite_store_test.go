package complexity

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domaincomplexity "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/complexity"
)

func TestSQLiteStoreSavesAndListsComplexityRecords(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "complexity.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	scan := domaincomplexity.ScanEvent{
		ScanID:        "scan_1",
		Repo:          "repo",
		Mode:          "report_only",
		FilesScanned:  1,
		HotspotsFound: 1,
		Status:        "completed",
		CreatedAt:     now,
		CompletedAt:   now,
	}
	hotspot := domaincomplexity.Hotspot{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "src/app.go",
		HotspotType:         "nested_loop",
		EstimatedComplexity: "O(n^2)",
		RiskLevel:           "medium",
		Summary:             "nested loop",
		CreatedAt:           now,
	}
	evidence := domaincomplexity.HotspotEvidence{
		EvidenceID: "ev_1",
		HotspotID:  "hot_1",
		FilePath:   "src/app.go",
		Snippet:    "for ...",
		CreatedAt:  now,
	}
	report := domaincomplexity.ReportArtifact{
		ArtifactID: "art_1",
		ScanID:     "scan_1",
		Type:       "complexity_hotspot_report",
		Title:      "Complexity Hotspot Report",
		Status:     "generated",
		Content:    "# Complexity Hotspot Report",
		CreatedAt:  now,
	}
	if err := store.SaveScanEvent(context.Background(), scan); err != nil {
		t.Fatalf("SaveScanEvent() error = %v", err)
	}
	if err := store.SaveHotspot(context.Background(), hotspot); err != nil {
		t.Fatalf("SaveHotspot() error = %v", err)
	}
	if err := store.SaveHotspotEvidence(context.Background(), evidence); err != nil {
		t.Fatalf("SaveHotspotEvidence() error = %v", err)
	}
	if err := store.SaveReportArtifact(context.Background(), report); err != nil {
		t.Fatalf("SaveReportArtifact() error = %v", err)
	}
	scans, err := store.ListScanEvents(context.Background(), 10)
	if err != nil || len(scans) != 1 || scans[0].ScanID != "scan_1" {
		t.Fatalf("ListScanEvents() = %#v, %v", scans, err)
	}
	hotspots, err := store.ListHotspots(context.Background(), 10)
	if err != nil || len(hotspots) != 1 || hotspots[0].HotspotID != "hot_1" {
		t.Fatalf("ListHotspots() = %#v, %v", hotspots, err)
	}
	evidenceItems, err := store.ListHotspotEvidence(context.Background(), 10)
	if err != nil || len(evidenceItems) != 1 || evidenceItems[0].EvidenceID != "ev_1" {
		t.Fatalf("ListHotspotEvidence() = %#v, %v", evidenceItems, err)
	}
	reports, err := store.ListReportArtifacts(context.Background(), 10)
	if err != nil || len(reports) != 1 || reports[0].ArtifactID != "art_1" {
		t.Fatalf("ListReportArtifacts() = %#v, %v", reports, err)
	}
}
