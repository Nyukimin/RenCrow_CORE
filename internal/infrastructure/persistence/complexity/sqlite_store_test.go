package complexity

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	domaincomplexity "github.com/Nyukimin/RenCrow_CORE/internal/domain/complexity"
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

func TestSQLiteStoreFindComplexityRecordsByIDUsesExactPrimaryKeys(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "complexity.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	hotspot := domaincomplexity.Hotspot{HotspotID: "hot-exact", ScanID: "scan-1", FilePath: "src/app.go", HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "summary", CreatedAt: now}
	report := domaincomplexity.ReportArtifact{ArtifactID: "report-exact", ScanID: "scan-1", Type: "complexity_hotspot_report", Title: "Report", Status: "generated", Content: "content", CreatedAt: now}
	if err := store.SaveHotspot(ctx, hotspot); err != nil {
		t.Fatalf("SaveHotspot() failed: %v", err)
	}
	if err := store.SaveReportArtifact(ctx, report); err != nil {
		t.Fatalf("SaveReportArtifact() failed: %v", err)
	}
	gotHotspot, found, err := store.FindHotspotByID(ctx, hotspot.HotspotID)
	if err != nil || !found || !reflect.DeepEqual(gotHotspot, hotspot) {
		t.Fatalf("FindHotspotByID() = %#v, found=%v, err=%v", gotHotspot, found, err)
	}
	gotReport, found, err := store.FindReportArtifactByID(ctx, report.ArtifactID)
	if err != nil || !found || !reflect.DeepEqual(gotReport, report) {
		t.Fatalf("FindReportArtifactByID() = %#v, found=%v, err=%v", gotReport, found, err)
	}
	if got, found, err := store.FindHotspotByID(ctx, "hot-exact-suffix"); err != nil || found || !reflect.DeepEqual(got, domaincomplexity.Hotspot{}) {
		t.Fatalf("missing FindHotspotByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindReportArtifactByID(ctx, "report-exact-suffix"); err != nil || found || !reflect.DeepEqual(got, domaincomplexity.ReportArtifact{}) {
		t.Fatalf("missing FindReportArtifactByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestSQLiteStoreFindComplexityRecordsByIDRejectsMalformedPayload(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "complexity.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	hotspot := domaincomplexity.Hotspot{HotspotID: "hot-malformed", ScanID: "scan-1", FilePath: "src/app.go", HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "summary", CreatedAt: now}
	report := domaincomplexity.ReportArtifact{ArtifactID: "report-malformed", ScanID: "scan-1", Type: "complexity_hotspot_report", Title: "Report", Status: "generated", Content: "content", CreatedAt: now}
	if err := store.SaveHotspot(ctx, hotspot); err != nil {
		t.Fatalf("SaveHotspot() failed: %v", err)
	}
	if err := store.SaveReportArtifact(ctx, report); err != nil {
		t.Fatalf("SaveReportArtifact() failed: %v", err)
	}
	corrupt := []struct {
		query string
		id    string
		find  func() (bool, error)
	}{
		{query: `UPDATE complexity_hotspot SET payload = ? WHERE hotspot_id = ?`, id: hotspot.HotspotID, find: func() (bool, error) {
			_, found, err := store.FindHotspotByID(ctx, hotspot.HotspotID)
			return found, err
		}},
		{query: `UPDATE complexity_report_artifact SET payload = ? WHERE artifact_id = ?`, id: report.ArtifactID, find: func() (bool, error) {
			_, found, err := store.FindReportArtifactByID(ctx, report.ArtifactID)
			return found, err
		}},
	}
	for _, item := range corrupt {
		if _, err := store.db.Exec(item.query, "{malformed}", item.id); err != nil {
			t.Fatalf("corrupt %q: %v", item.id, err)
		}
		found, err := item.find()
		if err == nil || found {
			t.Fatalf("expected malformed payload error for %q, found=%v err=%v", item.id, found, err)
		}
	}
}

func TestSQLiteStoreFindComplexityRecordsByIDRejectsValidJSONInvalidDomain(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "complexity.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	invalid := []struct {
		name   string
		query  string
		id     string
		value  any
		lookup func() (bool, error)
	}{
		{
			name: "hotspot", query: `INSERT INTO complexity_hotspot (hotspot_id, payload) VALUES (?, ?)`, id: "hot-invalid",
			value:  domaincomplexity.Hotspot{HotspotID: "hot-invalid", ScanID: "scan-1", HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "summary", CreatedAt: now},
			lookup: func() (bool, error) { _, found, err := store.FindHotspotByID(ctx, "hot-invalid"); return found, err },
		},
		{
			name: "report", query: `INSERT INTO complexity_report_artifact (artifact_id, payload) VALUES (?, ?)`, id: "report-invalid",
			value: domaincomplexity.ReportArtifact{ArtifactID: "report-invalid", ScanID: "scan-1", Type: "complexity_hotspot_report", Title: "Report", Status: "generated", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindReportArtifactByID(ctx, "report-invalid")
				return found, err
			},
		},
	}
	for _, item := range invalid {
		payload, err := json.Marshal(item.value)
		if err != nil {
			t.Fatalf("marshal invalid %s payload: %v", item.name, err)
		}
		if _, err := store.db.Exec(item.query, item.id, string(payload)); err != nil {
			t.Fatalf("insert invalid %s payload: %v", item.name, err)
		}
		found, err := item.lookup()
		if err == nil || found {
			t.Fatalf("expected valid-JSON invalid-domain error for %s id %q, found=%v err=%v", item.name, item.id, found, err)
		}
	}
}

func TestSQLiteStoreFindComplexityRecordsByIDRejectsPayloadIDMismatch(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "complexity.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	mismatched := []struct {
		name   string
		query  string
		rowID  string
		value  any
		lookup func() (bool, error)
	}{
		{
			name: "hotspot", query: `INSERT INTO complexity_hotspot (hotspot_id, payload) VALUES (?, ?)`, rowID: "row-hotspot",
			value:  domaincomplexity.Hotspot{HotspotID: "payload-hotspot", ScanID: "scan-1", FilePath: "src/app.go", HotspotType: "nested_loop", EstimatedComplexity: "O(n^2)", RiskLevel: "medium", Summary: "summary", CreatedAt: now},
			lookup: func() (bool, error) { _, found, err := store.FindHotspotByID(ctx, "row-hotspot"); return found, err },
		},
		{
			name: "report", query: `INSERT INTO complexity_report_artifact (artifact_id, payload) VALUES (?, ?)`, rowID: "row-report",
			value: domaincomplexity.ReportArtifact{ArtifactID: "payload-report", ScanID: "scan-1", Type: "complexity_hotspot_report", Title: "Report", Status: "generated", Content: "content", CreatedAt: now},
			lookup: func() (bool, error) {
				_, found, err := store.FindReportArtifactByID(ctx, "row-report")
				return found, err
			},
		},
	}
	for _, item := range mismatched {
		payload, err := json.Marshal(item.value)
		if err != nil {
			t.Fatalf("marshal mismatched %s payload: %v", item.name, err)
		}
		if _, err := store.db.Exec(item.query, item.rowID, string(payload)); err != nil {
			t.Fatalf("insert mismatched %s payload: %v", item.name, err)
		}
		found, err := item.lookup()
		if err == nil || found || !strings.Contains(err.Error(), "does not match") {
			t.Fatalf("expected primary-key/payload-ID mismatch for %s row %q, found=%v err=%v", item.name, item.rowID, found, err)
		}
	}
}

func TestSQLiteStoreConfiguresSingleConnectionAndBusyTimeout(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "complexity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := store.db.Stats().MaxOpenConnections; got != 1 {
		t.Fatalf("max open connections=%d want=1", got)
	}
	var busyTimeout int
	if err := store.db.QueryRow("PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if busyTimeout != 5000 {
		t.Fatalf("busy timeout=%d want=5000", busyTimeout)
	}
}
