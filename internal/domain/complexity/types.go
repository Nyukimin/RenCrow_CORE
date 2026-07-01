package complexity

import "time"

type ScanEvent struct {
	ScanID        string    `json:"scan_id"`
	WorkstreamID  string    `json:"workstream_id,omitempty"`
	Repo          string    `json:"repo"`
	ScanScope     []string  `json:"scan_scope,omitempty"`
	Mode          string    `json:"mode"`
	FilesScanned  int       `json:"files_scanned"`
	HotspotsFound int       `json:"hotspots_found"`
	Status        string    `json:"status"`
	CreatedAt     time.Time `json:"created_at"`
	CompletedAt   time.Time `json:"completed_at,omitempty"`
}

type Hotspot struct {
	HotspotID            string    `json:"hotspot_id"`
	ScanID               string    `json:"scan_id"`
	FilePath             string    `json:"file_path"`
	LineStart            int       `json:"line_start,omitempty"`
	LineEnd              int       `json:"line_end,omitempty"`
	HotspotType          string    `json:"hotspot_type"`
	EstimatedComplexity  string    `json:"estimated_complexity"`
	EstimatedAfter       string    `json:"estimated_after,omitempty"`
	RiskLevel            string    `json:"risk_level"`
	PriorityScore        float64   `json:"priority_score,omitempty"`
	Confidence           float64   `json:"confidence,omitempty"`
	Summary              string    `json:"summary"`
	SuggestedImprovement string    `json:"suggested_improvement,omitempty"`
	RequiredTests        []string  `json:"required_tests,omitempty"`
	CreatedAt            time.Time `json:"created_at"`
}

type HotspotEvidence struct {
	EvidenceID string    `json:"evidence_id"`
	HotspotID  string    `json:"hotspot_id"`
	FilePath   string    `json:"file_path"`
	LineStart  int       `json:"line_start,omitempty"`
	LineEnd    int       `json:"line_end,omitempty"`
	Snippet    string    `json:"snippet,omitempty"`
	Reason     string    `json:"reason,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
}

type ScanResult struct {
	Scan     ScanEvent         `json:"scan"`
	Hotspots []Hotspot         `json:"hotspots"`
	Evidence []HotspotEvidence `json:"evidence"`
}

type ReportArtifact struct {
	ArtifactID   string    `json:"artifact_id"`
	ScanID       string    `json:"scan_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	Type         string    `json:"artifact_type"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
}
