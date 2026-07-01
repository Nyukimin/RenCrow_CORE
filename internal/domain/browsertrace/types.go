package browsertrace

import "time"

type TraceRun struct {
	TraceRunID   string    `json:"trace_run_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	SiteID       string    `json:"site_id,omitempty"`
	Goal         string    `json:"goal,omitempty"`
	TracePath    string    `json:"trace_path"`
	CapturedAt   time.Time `json:"captured_at,omitempty"`
	CreatedAt    time.Time `json:"created_at"`
}

type APICandidate struct {
	CandidateID          string          `json:"candidate_id"`
	TraceRunID           string          `json:"trace_run_id"`
	SiteID               string          `json:"site_id,omitempty"`
	Method               string          `json:"method"`
	ObservedURL          string          `json:"observed_url"`
	TemplatedURL         string          `json:"templated_url,omitempty"`
	PathTemplate         string          `json:"path_template,omitempty"`
	QueryParams          []APIQueryParam `json:"query_params,omitempty"`
	AuthRequired         bool            `json:"auth_required"`
	ContainsPersonalData string          `json:"contains_personal_data"`
	RiskLevel            string          `json:"risk_level"`
	Status               string          `json:"status"`
	Confidence           float64         `json:"confidence,omitempty"`
	CreatedAt            time.Time       `json:"created_at"`
}

type APIQueryParam struct {
	Name           string   `json:"name"`
	Type           string   `json:"type,omitempty"`
	ObservedValues []string `json:"observed_values,omitempty"`
}

type APICandidateSchema struct {
	SchemaID    string    `json:"schema_id"`
	CandidateID string    `json:"candidate_id"`
	SchemaType  string    `json:"schema_type"`
	SchemaJSON  string    `json:"schema_json"`
	SampleCount int       `json:"sample_count"`
	Confidence  float64   `json:"confidence,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

type APICandidateValidationResult struct {
	ValidationID string               `json:"validation_id"`
	CandidateID  string               `json:"candidate_id"`
	TraceRunID   string               `json:"trace_run_id"`
	Passed       bool                 `json:"passed"`
	Status       string               `json:"status"`
	Issues       []APIValidationIssue `json:"issues,omitempty"`
	CreatedAt    time.Time            `json:"created_at"`
}

type APIValidationIssue struct {
	Code     string `json:"code"`
	Message  string `json:"message"`
	Severity string `json:"severity,omitempty"`
}

type APICoverageReport struct {
	ReportID              string    `json:"report_id"`
	TraceRunID            string    `json:"trace_run_id"`
	ObservedFlows         []string  `json:"observed_flows,omitempty"`
	ObservedEndpoints     []string  `json:"observed_endpoints,omitempty"`
	MissingFlows          []string  `json:"missing_flows,omitempty"`
	RecommendedNextTraces []string  `json:"recommended_next_traces,omitempty"`
	CreatedAt             time.Time `json:"created_at"`
}

type NetworkRequest struct {
	RequestID string            `json:"request_id"`
	Method    string            `json:"method"`
	URL       string            `json:"url"`
	Headers   map[string]string `json:"headers,omitempty"`
}

type NetworkResponse struct {
	RequestID string `json:"request_id"`
	URL       string `json:"url,omitempty"`
	Status    int    `json:"status,omitempty"`
	Body      string `json:"body,omitempty"`
}

type Exchange struct {
	Request  NetworkRequest
	Response NetworkResponse
}

type DiscoveryResult struct {
	Run        TraceRun
	Candidates []APICandidate
	Schemas    []APICandidateSchema
	Coverage   APICoverageReport
}

type APIArtifact struct {
	ArtifactID   string    `json:"artifact_id"`
	TraceRunID   string    `json:"trace_run_id"`
	WorkstreamID string    `json:"workstream_id,omitempty"`
	Type         string    `json:"artifact_type"`
	Title        string    `json:"title"`
	Status       string    `json:"status"`
	Content      string    `json:"content"`
	CreatedAt    time.Time `json:"created_at"`
}
