package vision

import "context"

// AnalyzeRequest is CORE's module-level request to RenCrow_Vision.
type AnalyzeRequest struct {
	RequestID   string
	SessionID   string
	Prompt      string
	Kind        string
	Filename    string
	ContentType string
	Data        []byte
	MaxFrames   int
	Language    string
}

type Segment struct {
	StartMS    int64   `json:"start_ms"`
	EndMS      int64   `json:"end_ms"`
	Text       string  `json:"text"`
	Confidence float64 `json:"confidence"`
}

// AnalyzeResult is the normalized response owned by RenCrow_Vision.
type AnalyzeResult struct {
	OK        bool           `json:"ok"`
	RequestID string         `json:"request_id"`
	Provider  string         `json:"provider"`
	Model     string         `json:"model"`
	Kind      string         `json:"kind"`
	Summary   string         `json:"summary"`
	Text      string         `json:"text"`
	Segments  []Segment      `json:"segments"`
	Metadata  map[string]any `json:"metadata"`
	ErrorCode string         `json:"error_code,omitempty"`
	Message   string         `json:"message,omitempty"`
}

type ReadyState struct {
	ModelLoaded     bool `json:"model_loaded"`
	TmpWritable     bool `json:"tmp_writable"`
	FFmpegAvailable bool `json:"ffmpeg_available"`
}

type HealthReport struct {
	OK       bool       `json:"ok"`
	Status   string     `json:"status"`
	Service  string     `json:"service"`
	Version  string     `json:"version"`
	Provider string     `json:"provider"`
	Model    string     `json:"model"`
	Ready    ReadyState `json:"ready"`
}

// Analyzer is the only recognition port CORE orchestration depends on.
type Analyzer interface {
	Analyze(ctx context.Context, request AnalyzeRequest) (AnalyzeResult, error)
	Health(ctx context.Context) (HealthReport, error)
}
