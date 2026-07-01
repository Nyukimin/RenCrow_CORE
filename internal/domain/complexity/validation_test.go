package complexity

import (
	"strings"
	"testing"
	"time"
)

func TestValidateScanEventRequiresReportOnlyMode(t *testing.T) {
	now := fixedComplexityValidationTime()
	item := ScanEvent{
		ScanID:    "scan_1",
		Repo:      "repo",
		Mode:      "apply",
		Status:    "completed",
		CreatedAt: now,
	}
	if err := ValidateScanEvent(item); err == nil || !strings.Contains(err.Error(), "report_only") {
		t.Fatalf("expected report_only validation error, got %v", err)
	}
}

func TestValidateScanEventRejectsCompletedWithoutCompletedAt(t *testing.T) {
	err := ValidateScanEvent(ScanEvent{
		ScanID:    "scan_1",
		Repo:      "repo",
		Mode:      "report_only",
		Status:    "completed",
		CreatedAt: fixedComplexityValidationTime(),
	})
	if err == nil || !strings.Contains(err.Error(), "completed_at") {
		t.Fatalf("expected completed_at validation error, got %v", err)
	}
}

func TestValidateHotspotRejectsInvalidConfidence(t *testing.T) {
	now := fixedComplexityValidationTime()
	item := Hotspot{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "src/app.go",
		HotspotType:         "nested_loop",
		EstimatedComplexity: "O(n^2)",
		RiskLevel:           "medium",
		Summary:             "nested loop",
		Confidence:          1.2,
		CreatedAt:           now,
	}
	if err := ValidateHotspot(item); err == nil || !strings.Contains(err.Error(), "confidence") {
		t.Fatalf("expected confidence validation error, got %v", err)
	}
}

func TestValidateComplexityAcceptsCompleteRecords(t *testing.T) {
	now := fixedComplexityValidationTime()
	if err := ValidateScanEvent(ScanEvent{
		ScanID:        "scan_1",
		Repo:          "repo",
		Mode:          "report_only",
		FilesScanned:  10,
		HotspotsFound: 1,
		Status:        "completed",
		CreatedAt:     now,
		CompletedAt:   now.Add(time.Second),
	}); err != nil {
		t.Fatalf("scan should validate: %v", err)
	}
	if err := ValidateHotspot(Hotspot{
		HotspotID:           "hot_1",
		ScanID:              "scan_1",
		FilePath:            "src/app.go",
		LineStart:           10,
		LineEnd:             20,
		HotspotType:         "large_function",
		EstimatedComplexity: "high",
		RiskLevel:           "medium",
		PriorityScore:       0.7,
		Confidence:          0.8,
		Summary:             "function is too large",
		CreatedAt:           now,
	}); err != nil {
		t.Fatalf("hotspot should validate: %v", err)
	}
	if err := ValidateHotspotEvidence(HotspotEvidence{
		EvidenceID: "ev_1",
		HotspotID:  "hot_1",
		FilePath:   "src/app.go",
		LineStart:  10,
		LineEnd:    20,
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("evidence should validate: %v", err)
	}
	if err := ValidateReportArtifact(ReportArtifact{
		ArtifactID: "art_1",
		ScanID:     "scan_1",
		Type:       "complexity_hotspot_report",
		Title:      "Complexity Hotspot Report",
		Status:     "generated",
		Content:    "report",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("report should validate: %v", err)
	}
}

func TestValidateComplexityRejectsInvalidRangesAndScores(t *testing.T) {
	now := fixedComplexityValidationTime()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "negative scan count",
			err: ValidateScanEvent(ScanEvent{
				ScanID:       "scan_1",
				Repo:         "repo",
				Mode:         "report_only",
				FilesScanned: -1,
				Status:       "running",
				CreatedAt:    now,
			}),
			want: "scan counts",
		},
		{
			name: "hotspot line order",
			err: ValidateHotspot(Hotspot{
				HotspotID:           "hot_1",
				ScanID:              "scan_1",
				FilePath:            "src/app.go",
				LineStart:           20,
				LineEnd:             10,
				HotspotType:         "large_function",
				EstimatedComplexity: "high",
				RiskLevel:           "medium",
				Summary:             "bad range",
				CreatedAt:           now,
			}),
			want: "line_end",
		},
		{
			name: "hotspot priority",
			err: ValidateHotspot(Hotspot{
				HotspotID:           "hot_1",
				ScanID:              "scan_1",
				FilePath:            "src/app.go",
				HotspotType:         "large_function",
				EstimatedComplexity: "high",
				RiskLevel:           "medium",
				PriorityScore:       -0.1,
				Summary:             "bad score",
				CreatedAt:           now,
			}),
			want: "priority_score",
		},
		{
			name: "evidence line order",
			err: ValidateHotspotEvidence(HotspotEvidence{
				EvidenceID: "ev_1",
				HotspotID:  "hot_1",
				FilePath:   "src/app.go",
				LineStart:  20,
				LineEnd:    10,
				CreatedAt:  now,
			}),
			want: "line_end",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", tt.err, tt.want)
			}
		})
	}
}

func TestValidateComplexityRejectsMissingCreatedAt(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "scan",
			err: ValidateScanEvent(ScanEvent{
				ScanID: "scan_1",
				Repo:   "repo",
				Mode:   "report_only",
				Status: "running",
			}),
		},
		{
			name: "hotspot",
			err: ValidateHotspot(Hotspot{
				HotspotID:           "hot_1",
				ScanID:              "scan_1",
				FilePath:            "src/app.go",
				HotspotType:         "nested_loop",
				EstimatedComplexity: "O(n^2)",
				RiskLevel:           "medium",
				Summary:             "nested loop",
			}),
		},
		{
			name: "evidence",
			err: ValidateHotspotEvidence(HotspotEvidence{
				EvidenceID: "ev_1",
				HotspotID:  "hot_1",
				FilePath:   "src/app.go",
			}),
		},
		{
			name: "report",
			err: ValidateReportArtifact(ReportArtifact{
				ArtifactID: "art_1",
				ScanID:     "scan_1",
				Type:       "complexity_hotspot_report",
				Title:      "Complexity Hotspot Report",
				Status:     "generated",
				Content:    "report",
			}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), "created_at") {
				t.Fatalf("validation error = %v, want created_at", tt.err)
			}
		})
	}
}

func TestValidateHotspotEvidenceRequiresIDs(t *testing.T) {
	err := ValidateHotspotEvidence(HotspotEvidence{FilePath: "src/app.go"})
	if err == nil || !strings.Contains(err.Error(), "evidence_id") {
		t.Fatalf("expected evidence_id validation error, got %v", err)
	}
}

func TestValidateReportArtifactRequiresContent(t *testing.T) {
	err := ValidateReportArtifact(ReportArtifact{
		ArtifactID: "art_1",
		ScanID:     "scan_1",
		Type:       "complexity_hotspot_report",
		Title:      "Complexity Hotspot Report",
		Status:     "generated",
	})
	if err == nil || !strings.Contains(err.Error(), "content") {
		t.Fatalf("expected content validation error, got %v", err)
	}
}

func TestValidateComplexityRejectsMissingRequiredFields(t *testing.T) {
	now := fixedComplexityValidationTime()
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "scan missing id", err: ValidateScanEvent(ScanEvent{Repo: "repo", Mode: "report_only", Status: "running", CreatedAt: now}), want: "scan_id"},
		{name: "scan missing repo", err: ValidateScanEvent(ScanEvent{ScanID: "scan_1", Mode: "report_only", Status: "running", CreatedAt: now}), want: "repo"},
		{name: "scan missing mode", err: ValidateScanEvent(ScanEvent{ScanID: "scan_1", Repo: "repo", Status: "running", CreatedAt: now}), want: "mode"},
		{name: "scan missing status", err: ValidateScanEvent(ScanEvent{ScanID: "scan_1", Repo: "repo", Mode: "report_only", CreatedAt: now}), want: "status"},
		{name: "hotspot missing id", err: ValidateHotspot(Hotspot{ScanID: "scan_1", FilePath: "src/app.go", HotspotType: "large_function", EstimatedComplexity: "high", RiskLevel: "medium", Summary: "large", CreatedAt: now}), want: "hotspot_id"},
		{name: "hotspot missing scan id", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", FilePath: "src/app.go", HotspotType: "large_function", EstimatedComplexity: "high", RiskLevel: "medium", Summary: "large", CreatedAt: now}), want: "scan_id"},
		{name: "hotspot missing path", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", ScanID: "scan_1", HotspotType: "large_function", EstimatedComplexity: "high", RiskLevel: "medium", Summary: "large", CreatedAt: now}), want: "file_path"},
		{name: "hotspot missing type", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "src/app.go", EstimatedComplexity: "high", RiskLevel: "medium", Summary: "large", CreatedAt: now}), want: "hotspot_type"},
		{name: "hotspot missing complexity", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "src/app.go", HotspotType: "large_function", RiskLevel: "medium", Summary: "large", CreatedAt: now}), want: "estimated_complexity"},
		{name: "hotspot missing risk", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "src/app.go", HotspotType: "large_function", EstimatedComplexity: "high", Summary: "large", CreatedAt: now}), want: "risk_level"},
		{name: "hotspot negative line", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "src/app.go", LineStart: -1, HotspotType: "large_function", EstimatedComplexity: "high", RiskLevel: "medium", Summary: "large", CreatedAt: now}), want: "line range"},
		{name: "hotspot missing summary", err: ValidateHotspot(Hotspot{HotspotID: "hot_1", ScanID: "scan_1", FilePath: "src/app.go", HotspotType: "large_function", EstimatedComplexity: "high", RiskLevel: "medium", CreatedAt: now}), want: "summary"},
		{name: "evidence missing hotspot id", err: ValidateHotspotEvidence(HotspotEvidence{EvidenceID: "ev_1", FilePath: "src/app.go", CreatedAt: now}), want: "hotspot_id"},
		{name: "evidence missing path", err: ValidateHotspotEvidence(HotspotEvidence{EvidenceID: "ev_1", HotspotID: "hot_1", CreatedAt: now}), want: "file_path"},
		{name: "evidence negative line", err: ValidateHotspotEvidence(HotspotEvidence{EvidenceID: "ev_1", HotspotID: "hot_1", FilePath: "src/app.go", LineEnd: -1, CreatedAt: now}), want: "line range"},
		{name: "report missing artifact id", err: ValidateReportArtifact(ReportArtifact{ScanID: "scan_1", Type: "complexity_hotspot_report", Title: "Complexity Hotspot Report", Status: "generated", Content: "report", CreatedAt: now}), want: "artifact_id"},
		{name: "report missing scan id", err: ValidateReportArtifact(ReportArtifact{ArtifactID: "art_1", Type: "complexity_hotspot_report", Title: "Complexity Hotspot Report", Status: "generated", Content: "report", CreatedAt: now}), want: "scan_id"},
		{name: "report missing type", err: ValidateReportArtifact(ReportArtifact{ArtifactID: "art_1", ScanID: "scan_1", Title: "Complexity Hotspot Report", Status: "generated", Content: "report", CreatedAt: now}), want: "artifact_type"},
		{name: "report missing title", err: ValidateReportArtifact(ReportArtifact{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_hotspot_report", Status: "generated", Content: "report", CreatedAt: now}), want: "title"},
		{name: "report missing status", err: ValidateReportArtifact(ReportArtifact{ArtifactID: "art_1", ScanID: "scan_1", Type: "complexity_hotspot_report", Title: "Complexity Hotspot Report", Content: "report", CreatedAt: now}), want: "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.err == nil || !strings.Contains(tc.err.Error(), tc.want) {
				t.Fatalf("err=%v, want %q", tc.err, tc.want)
			}
		})
	}
}

func fixedComplexityValidationTime() time.Time {
	return time.Date(2026, 5, 20, 6, 45, 0, 0, time.UTC)
}
