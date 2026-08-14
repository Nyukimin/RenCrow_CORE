package complexity

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
)

func TestJSONLStoreSavesAndListsComplexityRecords(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
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
	if err := store.SaveScanEvent(context.Background(), scan); err != nil {
		t.Fatalf("SaveScanEvent() error = %v", err)
	}
	if err := store.SaveHotspot(context.Background(), hotspot); err != nil {
		t.Fatalf("SaveHotspot() error = %v", err)
	}
	if err := store.SaveHotspotEvidence(context.Background(), evidence); err != nil {
		t.Fatalf("SaveHotspotEvidence() error = %v", err)
	}
	if err := store.SaveReportArtifact(context.Background(), domaincomplexity.ReportArtifact{
		ArtifactID: "art_1",
		ScanID:     "scan_1",
		Type:       "complexity_hotspot_report",
		Title:      "Complexity Hotspot Report",
		Status:     "generated",
		Content:    "# Complexity Hotspot Report",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("SaveReportArtifact() error = %v", err)
	}
	updatedReport := domaincomplexity.ReportArtifact{
		ArtifactID: "art_1",
		ScanID:     "scan_1",
		Type:       "complexity_hotspot_report",
		Title:      "Complexity Hotspot Report",
		Status:     "pending_review",
		Content:    "# Updated Complexity Hotspot Report",
		CreatedAt:  now.Add(time.Second),
	}
	if err := store.SaveReportArtifact(context.Background(), updatedReport); err != nil {
		t.Fatalf("SaveReportArtifact(updated) error = %v", err)
	}
	scans, err := store.ListScanEvents(context.Background(), 10)
	if err != nil || len(scans) != 1 {
		t.Fatalf("ListScanEvents() = %#v, %v", scans, err)
	}
	hotspots, err := store.ListHotspots(context.Background(), 10)
	if err != nil || len(hotspots) != 1 {
		t.Fatalf("ListHotspots() = %#v, %v", hotspots, err)
	}
	evidenceItems, err := store.ListHotspotEvidence(context.Background(), 10)
	if err != nil || len(evidenceItems) != 1 {
		t.Fatalf("ListHotspotEvidence() = %#v, %v", evidenceItems, err)
	}
	reports, err := store.ListReportArtifacts(context.Background(), 10)
	if err != nil || len(reports) != 1 || reports[0].Status != "pending_review" {
		t.Fatalf("ListReportArtifacts() = %#v, %v", reports, err)
	}
}

func TestJSONLStoreFindComplexityRecordsByIDReturnsLatestExactRecords(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	hotspot := domaincomplexity.Hotspot{
		HotspotID: "hot-exact", ScanID: "scan-1", FilePath: "src/app.go", HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "first", CreatedAt: now,
	}
	hotspotSuffix := hotspot
	hotspotSuffix.HotspotID = "hot-exact-suffix"
	hotspotLatest := hotspot
	hotspotLatest.Summary = "latest"
	hotspotLatest.CreatedAt = now.Add(time.Minute)
	for _, item := range []domaincomplexity.Hotspot{hotspot, hotspotSuffix, hotspotLatest} {
		if err := store.SaveHotspot(ctx, item); err != nil {
			t.Fatalf("SaveHotspot(%q) failed: %v", item.HotspotID, err)
		}
	}
	gotHotspot, found, err := store.FindHotspotByID(ctx, hotspot.HotspotID)
	if err != nil || !found || gotHotspot.Summary != hotspotLatest.Summary {
		t.Fatalf("FindHotspotByID() = %#v, found=%v, err=%v", gotHotspot, found, err)
	}
	if gotHotspot, found, err := store.FindHotspotByID(ctx, "missing"); err != nil || found || !reflect.DeepEqual(gotHotspot, domaincomplexity.Hotspot{}) {
		t.Fatalf("missing FindHotspotByID() = %#v, found=%v, err=%v", gotHotspot, found, err)
	}

	report := domaincomplexity.ReportArtifact{
		ArtifactID: "report-exact", ScanID: "scan-1", Type: "complexity_hotspot_report", Title: "Report", Status: "generated", Content: "first", CreatedAt: now,
	}
	reportSuffix := report
	reportSuffix.ArtifactID = "report-exact-suffix"
	reportLatest := report
	reportLatest.Content = "latest"
	reportLatest.CreatedAt = now.Add(time.Minute)
	for _, item := range []domaincomplexity.ReportArtifact{report, reportSuffix, reportLatest} {
		if err := store.SaveReportArtifact(ctx, item); err != nil {
			t.Fatalf("SaveReportArtifact(%q) failed: %v", item.ArtifactID, err)
		}
	}
	gotReport, found, err := store.FindReportArtifactByID(ctx, report.ArtifactID)
	if err != nil || !found || gotReport.Content != reportLatest.Content {
		t.Fatalf("FindReportArtifactByID() = %#v, found=%v, err=%v", gotReport, found, err)
	}
}

func TestJSONLStoreFindComplexityRecordsByIDRejectsMalformedJSON(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	if err := os.WriteFile(filepath.Join(root, "complexity_hotspot.jsonl"), []byte("{malformed}\n"), 0644); err != nil {
		t.Fatalf("write malformed hotspot: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "complexity_report_artifact.jsonl"), []byte("{malformed}\n"), 0644); err != nil {
		t.Fatalf("write malformed report: %v", err)
	}
	ctx := context.Background()
	if _, found, err := store.FindHotspotByID(ctx, "hotspot"); err == nil || found {
		t.Fatalf("expected malformed hotspot error, found=%v err=%v", found, err)
	}
	if _, found, err := store.FindReportArtifactByID(ctx, "report"); err == nil || found {
		t.Fatalf("expected malformed report error, found=%v err=%v", found, err)
	}
}

func TestJSONLStoreFindComplexityRecordsByIDRejectsValidJSONInvalidDomain(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	invalid := []struct {
		name  string
		path  string
		id    string
		value any
		find  func() (bool, error)
	}{
		{
			name: "hotspot",
			path: filepath.Join(root, "complexity_hotspot.jsonl"),
			id:   "hot-invalid",
			value: domaincomplexity.Hotspot{
				HotspotID: "hot-invalid", ScanID: "scan-1", HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "summary", CreatedAt: now,
			},
			find: func() (bool, error) { _, found, err := store.FindHotspotByID(ctx, "hot-invalid"); return found, err },
		},
		{
			name: "report",
			path: filepath.Join(root, "complexity_report_artifact.jsonl"),
			id:   "report-invalid",
			value: domaincomplexity.ReportArtifact{
				ArtifactID: "report-invalid", ScanID: "scan-1", Type: "complexity_hotspot_report", Title: "Report", Status: "generated", CreatedAt: now,
			},
			find: func() (bool, error) {
				_, found, err := store.FindReportArtifactByID(ctx, "report-invalid")
				return found, err
			},
		},
	}
	for _, item := range invalid {
		line, err := json.Marshal(item.value)
		if err != nil {
			t.Fatalf("marshal invalid %s payload: %v", item.name, err)
		}
		if err := os.WriteFile(item.path, append(line, '\n'), 0644); err != nil {
			t.Fatalf("write invalid %s payload: %v", item.name, err)
		}
		found, err := item.find()
		if err == nil || found {
			t.Fatalf("expected valid-JSON invalid-domain error for %s id %q, found=%v err=%v", item.name, item.id, found, err)
		}
	}
}
