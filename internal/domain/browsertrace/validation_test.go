package browsertrace

import (
	"strings"
	"testing"
	"time"
)

func TestValidateAPICandidateRejectsWriteMethods(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	item := APICandidate{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		Confidence:           0.8,
		CreatedAt:            now,
	}
	if err := ValidateAPICandidate(item); err != nil {
		t.Fatalf("ValidateAPICandidate() error = %v", err)
	}
	item.Method = "DELETE"
	if err := ValidateAPICandidate(item); err == nil {
		t.Fatal("expected DELETE candidate to fail")
	}
	item.Method = "GET"
	item.Status = "promoted"
	if err := ValidateAPICandidate(item); err == nil {
		t.Fatal("expected unknown candidate status to fail")
	}
}

func TestValidateBrowserTraceAcceptsCompleteRecords(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	if err := ValidateTraceRun(TraceRun{
		TraceRunID: "trace_1",
		TracePath:  "traces/trace_1.json",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("trace run should validate: %v", err)
	}
	if err := ValidateAPICandidate(APICandidate{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
		Method:               "get",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		Status:               "candidate",
		Confidence:           1,
		CreatedAt:            now,
	}); err != nil {
		t.Fatalf("candidate should validate: %v", err)
	}
	if err := ValidateAPICandidateSchema(APICandidateSchema{
		SchemaID:    "schema_1",
		CandidateID: "api_cand_1",
		SchemaType:  "response",
		SchemaJSON:  `{"type":"object"}`,
		SampleCount: 0,
		Confidence:  1,
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("schema should validate: %v", err)
	}
	if err := ValidateAPICoverageReport(APICoverageReport{
		ReportID:   "coverage_1",
		TraceRunID: "trace_1",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("coverage report should validate: %v", err)
	}
	if err := ValidateAPIArtifact(APIArtifact{
		ArtifactID: "art_1",
		TraceRunID: "trace_1",
		Type:       "observed_openapi",
		Title:      "Observed OpenAPI",
		Status:     "pending_review",
		Content:    "openapi: 3.1.0",
		CreatedAt:  now,
	}); err != nil {
		t.Fatalf("artifact should validate: %v", err)
	}
}

func TestValidateAPICandidateSchema(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	item := APICandidateSchema{
		SchemaID:    "schema_1",
		CandidateID: "api_cand_1",
		SchemaType:  "response",
		SchemaJSON:  `{"type":"object"}`,
		SampleCount: 1,
		CreatedAt:   now,
	}
	if err := ValidateAPICandidateSchema(item); err != nil {
		t.Fatalf("ValidateAPICandidateSchema() error = %v", err)
	}
	item.SchemaJSON = ""
	if err := ValidateAPICandidateSchema(item); err == nil {
		t.Fatal("expected missing schema_json to fail")
	}
	item.SchemaJSON = `{"type":`
	if err := ValidateAPICandidateSchema(item); err == nil {
		t.Fatal("expected invalid schema_json to fail")
	}
	item.SchemaJSON = `{"type":"object"}`
	item.Confidence = 1.1
	if err := ValidateAPICandidateSchema(item); err == nil {
		t.Fatal("expected invalid confidence to fail")
	}
}

func TestValidateAPICandidateValidationResultRequiresIssueCode(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	err := ValidateAPICandidateValidationResult(APICandidateValidationResult{
		ValidationID: "api_val_1",
		CandidateID:  "api_cand_1",
		TraceRunID:   "trace_1",
		Status:       "needs_review",
		CreatedAt:    now,
		Issues: []APIValidationIssue{{
			Message: "terms review is required",
		}},
	})
	if err == nil {
		t.Fatal("expected missing issue code to fail")
	}
	err = ValidateAPICandidateValidationResult(APICandidateValidationResult{
		ValidationID: "api_val_1",
		CandidateID:  "api_cand_1",
		TraceRunID:   "trace_1",
		Status:       "approved",
		CreatedAt:    now,
		Issues: []APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
	})
	if err == nil {
		t.Fatal("expected unknown validation status to fail")
	}
}

func TestValidateAPICandidateValidationResultRequiresStatusPassedIssueConsistency(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	validated := APICandidateValidationResult{
		ValidationID: "api_val_1",
		CandidateID:  "api_cand_1",
		TraceRunID:   "trace_1",
		Passed:       true,
		Status:       "validated",
		CreatedAt:    now,
	}
	if err := ValidateAPICandidateValidationResult(validated); err != nil {
		t.Fatalf("ValidateAPICandidateValidationResult() error = %v", err)
	}
	needsReview := APICandidateValidationResult{
		ValidationID: "api_val_2",
		CandidateID:  "api_cand_1",
		TraceRunID:   "trace_1",
		Passed:       false,
		Status:       "needs_review",
		CreatedAt:    now,
		Issues: []APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
	}
	if err := ValidateAPICandidateValidationResult(needsReview); err != nil {
		t.Fatalf("ValidateAPICandidateValidationResult() error = %v", err)
	}

	tests := []struct {
		name string
		item APICandidateValidationResult
	}{
		{name: "validated without passed", item: APICandidateValidationResult{ValidationID: "api_val_3", CandidateID: "api_cand_1", TraceRunID: "trace_1", Status: "validated", CreatedAt: now}},
		{name: "validated with issues", item: APICandidateValidationResult{ValidationID: "api_val_4", CandidateID: "api_cand_1", TraceRunID: "trace_1", Passed: true, Status: "validated", CreatedAt: now, Issues: []APIValidationIssue{{Code: "terms", Message: "terms issue"}}}},
		{name: "needs review with passed", item: APICandidateValidationResult{ValidationID: "api_val_5", CandidateID: "api_cand_1", TraceRunID: "trace_1", Passed: true, Status: "needs_review", CreatedAt: now, Issues: []APIValidationIssue{{Code: "terms", Message: "terms issue"}}}},
		{name: "needs review without issues", item: APICandidateValidationResult{ValidationID: "api_val_6", CandidateID: "api_cand_1", TraceRunID: "trace_1", Status: "needs_review", CreatedAt: now}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateAPICandidateValidationResult(tt.item); err == nil {
				t.Fatal("expected invalid validation state to fail")
			}
		})
	}
}

func TestValidateBrowserTraceAPIRejectsMissingCreatedAt(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{
			name: "trace run",
			err: ValidateTraceRun(TraceRun{
				TraceRunID: "trace_1",
				TracePath:  "traces/trace_1.json",
			}),
		},
		{
			name: "candidate",
			err: ValidateAPICandidate(APICandidate{
				CandidateID:          "api_cand_1",
				TraceRunID:           "trace_1",
				Method:               "GET",
				ObservedURL:          "https://example.com/api/items",
				ContainsPersonalData: "unknown",
				Status:               "candidate",
			}),
		},
		{
			name: "schema",
			err: ValidateAPICandidateSchema(APICandidateSchema{
				SchemaID:    "schema_1",
				CandidateID: "api_cand_1",
				SchemaType:  "response",
				SchemaJSON:  `{"type":"object"}`,
			}),
		},
		{
			name: "validation",
			err: ValidateAPICandidateValidationResult(APICandidateValidationResult{
				ValidationID: "api_val_1",
				CandidateID:  "api_cand_1",
				TraceRunID:   "trace_1",
				Passed:       true,
				Status:       "validated",
			}),
		},
		{
			name: "coverage",
			err: ValidateAPICoverageReport(APICoverageReport{
				ReportID:   "coverage_1",
				TraceRunID: "trace_1",
			}),
		},
		{
			name: "artifact",
			err: ValidateAPIArtifact(APIArtifact{
				ArtifactID: "art_1",
				TraceRunID: "trace_1",
				Type:       "observed_openapi",
				Title:      "Observed OpenAPI",
				Status:     "generated",
				Content:    "openapi: 3.1.0",
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

func TestValidateAPIArtifactRequiresContent(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	err := ValidateAPIArtifact(APIArtifact{
		ArtifactID: "art_1",
		TraceRunID: "trace_1",
		Type:       "observed_openapi",
		Title:      "Observed OpenAPI",
		Status:     "generated",
		CreatedAt:  now,
	})
	if err == nil {
		t.Fatal("expected missing content to fail")
	}
	err = ValidateAPIArtifact(APIArtifact{
		ArtifactID: "art_1",
		TraceRunID: "trace_1",
		Type:       "observed_openapi",
		Title:      "Observed OpenAPI",
		Status:     "promoted",
		Content:    "openapi: 3.1.0",
		CreatedAt:  now,
	})
	if err == nil {
		t.Fatal("expected unknown artifact status to fail")
	}
}

func TestValidateBrowserTraceRequiredFields(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "trace id", err: ValidateTraceRun(TraceRun{TracePath: "trace.json", CreatedAt: now}), want: "trace_run_id"},
		{name: "trace path", err: ValidateTraceRun(TraceRun{TraceRunID: "trace_1", CreatedAt: now}), want: "trace_path"},
		{name: "candidate id", err: ValidateAPICandidate(APICandidate{TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "candidate_id"},
		{name: "candidate trace", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "trace_run_id"},
		{name: "candidate method", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TraceRunID: "trace_1", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "method"},
		{name: "candidate url", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TraceRunID: "trace_1", Method: "GET", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "observed_url"},
		{name: "candidate status", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", CreatedAt: now}), want: "status"},
		{name: "candidate personal data", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api", Status: "candidate", CreatedAt: now}), want: "contains_personal_data"},
		{name: "candidate confidence", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TraceRunID: "trace_1", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", Confidence: -0.1, CreatedAt: now}), want: "confidence"},
		{name: "schema id", err: ValidateAPICandidateSchema(APICandidateSchema{CandidateID: "api_cand_1", SchemaType: "response", SchemaJSON: `{}`, CreatedAt: now}), want: "schema_id"},
		{name: "schema candidate", err: ValidateAPICandidateSchema(APICandidateSchema{SchemaID: "schema_1", SchemaType: "response", SchemaJSON: `{}`, CreatedAt: now}), want: "candidate_id"},
		{name: "schema type", err: ValidateAPICandidateSchema(APICandidateSchema{SchemaID: "schema_1", CandidateID: "api_cand_1", SchemaJSON: `{}`, CreatedAt: now}), want: "schema_type"},
		{name: "schema sample", err: ValidateAPICandidateSchema(APICandidateSchema{SchemaID: "schema_1", CandidateID: "api_cand_1", SchemaType: "response", SchemaJSON: `{}`, SampleCount: -1, CreatedAt: now}), want: "sample_count"},
		{name: "validation id", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{CandidateID: "api_cand_1", TraceRunID: "trace_1", Passed: true, Status: "validated", CreatedAt: now}), want: "validation_id"},
		{name: "validation candidate", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", TraceRunID: "trace_1", Passed: true, Status: "validated", CreatedAt: now}), want: "candidate_id"},
		{name: "validation trace", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", Passed: true, Status: "validated", CreatedAt: now}), want: "trace_run_id"},
		{name: "validation status", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", TraceRunID: "trace_1", CreatedAt: now}), want: "status"},
		{name: "validation passed mismatch", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", TraceRunID: "trace_1", Passed: true, Status: "needs_review", CreatedAt: now, Issues: []APIValidationIssue{{Code: "terms", Message: "terms issue"}}}), want: "passed validation"},
		{name: "validation issue message", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", TraceRunID: "trace_1", Status: "needs_review", Issues: []APIValidationIssue{{Code: "terms"}}, CreatedAt: now}), want: "message"},
		{name: "coverage report id", err: ValidateAPICoverageReport(APICoverageReport{TraceRunID: "trace_1", CreatedAt: now}), want: "report_id"},
		{name: "coverage trace", err: ValidateAPICoverageReport(APICoverageReport{ReportID: "coverage_1", CreatedAt: now}), want: "trace_run_id"},
		{name: "artifact id", err: ValidateAPIArtifact(APIArtifact{TraceRunID: "trace_1", Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "artifact_id"},
		{name: "artifact trace", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_1", Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "trace_run_id"},
		{name: "artifact type", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_1", TraceRunID: "trace_1", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "artifact_type"},
		{name: "artifact title", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "observed_openapi", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "title"},
		{name: "artifact status", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_1", TraceRunID: "trace_1", Type: "observed_openapi", Title: "Observed OpenAPI", Content: "openapi: 3.1.0", CreatedAt: now}), want: "status"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", tt.err, tt.want)
			}
		})
	}
}

func fixedBrowserTraceValidationTime() time.Time {
	return time.Date(2026, 5, 20, 6, 40, 0, 0, time.UTC)
}
