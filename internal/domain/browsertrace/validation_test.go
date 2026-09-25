package browsertrace

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// testContentHashDigest is the reference SHA-256 content digest used by the
// Step13 content-hash tests. It is computed with crypto/sha256 so the
// expectation stays independent from the producer helper in modules/core.
func testContentHashDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func TestValidateAPICandidateRejectsWriteMethods(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	item := APICandidate{
		CandidateID: "api_cand_1",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
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
		TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		TracePath: "traces/trace_1.json",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("trace run should validate: %v", err)
	}
	if err := ValidateAPICandidate(APICandidate{
		CandidateID: "api_cand_1",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
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
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
		Kind:       modulecore.ArtifactKindReport,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		// Every body list is nil here, so the digested body is the four empty lists
		// in the order the domain declares.
		ContentHash: coverageBodyDigest("", "", "", ""),
		CreatedAt:   now,
	}); err != nil {
		t.Fatalf("coverage report should validate: %v", err)
	}
	if err := ValidateAPIArtifact(APIArtifact{
		ArtifactID: "art_00000000-0000-5000-8000-00000000000a",
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Type:        "observed_openapi",
		Title:       "Observed OpenAPI",
		Status:      "pending_review",
		Content:     "openapi: 3.1.0",
		ContentHash: testContentHashDigest("openapi: 3.1.0"),
		CreatedAt:   now,
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
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Status:    "needs_review",
		CreatedAt: now,
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
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Status:    "adopted",
		CreatedAt: now,
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
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Passed:    true,
		Status:    "validated",
		CreatedAt: now,
	}
	if err := ValidateAPICandidateValidationResult(validated); err != nil {
		t.Fatalf("ValidateAPICandidateValidationResult() error = %v", err)
	}
	needsReview := APICandidateValidationResult{
		ValidationID: "api_val_2",
		CandidateID:  "api_cand_1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Passed:    false,
		Status:    "needs_review",
		CreatedAt: now,
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
		{name: "validated without passed", item: APICandidateValidationResult{ValidationID: "api_val_3", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Status: "validated", CreatedAt: now}},
		{name: "validated with issues", item: APICandidateValidationResult{ValidationID: "api_val_4", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Passed: true, Status: "validated", CreatedAt: now, Issues: []APIValidationIssue{{Code: "terms", Message: "terms issue"}}}},
		{name: "needs review with passed", item: APICandidateValidationResult{ValidationID: "api_val_5", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Passed: true, Status: "needs_review", CreatedAt: now, Issues: []APIValidationIssue{{Code: "terms", Message: "terms issue"}}}},
		{name: "needs review without issues", item: APICandidateValidationResult{ValidationID: "api_val_6", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Status: "needs_review", CreatedAt: now}},
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
				TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
				TracePath: "traces/trace_1.json",
			}),
		},
		{
			name: "candidate",
			err: ValidateAPICandidate(APICandidate{
				CandidateID: "api_cand_1",
				TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
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
				TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
				Passed: true,
				Status: "validated",
			}),
		},
		{
			name: "coverage",
			err: ValidateAPICoverageReport(APICoverageReport{
				ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
				Kind:       modulecore.ArtifactKindReport,
				TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			}),
		},
		{
			name: "artifact",
			err: ValidateAPIArtifact(APIArtifact{
				ArtifactID: "art_00000000-0000-5000-8000-00000000000a",
				Kind:       modulecore.ArtifactKindSpecification,
				TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
				Type:    "observed_openapi",
				Title:   "Observed OpenAPI",
				Status:  "generated",
				Content: "openapi: 3.1.0",
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
		ArtifactID: "art_00000000-0000-5000-8000-00000000000a",
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Type:      "observed_openapi",
		Title:     "Observed OpenAPI",
		Status:    "generated",
		CreatedAt: now,
	})
	if err == nil {
		t.Fatal("expected missing content to fail")
	}
	err = ValidateAPIArtifact(APIArtifact{
		ArtifactID: "art_00000000-0000-5000-8000-00000000000a",
		Kind:       modulecore.ArtifactKindSpecification,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Type:      "observed_openapi",
		Title:     "Observed OpenAPI",
		Status:    "promoted",
		Content:   "openapi: 3.1.0",
		CreatedAt: now,
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
		{name: "trace task id", err: ValidateTraceRun(TraceRun{RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", TracePath: "trace.json", CreatedAt: now}), want: "task_id"},
		{name: "trace run id", err: ValidateTraceRun(TraceRun{TaskID: "tsk_00000000-0000-5000-8000-000000000001", ActorID: "mio", TracePath: "trace.json", CreatedAt: now}), want: "run_id"},
		{name: "trace actor id", err: ValidateTraceRun(TraceRun{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", TracePath: "trace.json", CreatedAt: now}), want: "actor_id"},
		{name: "trace path", err: ValidateTraceRun(TraceRun{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", CreatedAt: now}), want: "trace_path"},
		{name: "candidate id", err: ValidateAPICandidate(APICandidate{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "candidate_id"},
		{name: "candidate owner", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "task_id"},
		{name: "candidate method", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "method"},
		{name: "candidate url", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Method: "GET", ContainsPersonalData: "unknown", Status: "candidate", CreatedAt: now}), want: "observed_url"},
		{name: "candidate status", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", CreatedAt: now}), want: "status"},
		{name: "candidate personal data", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Method: "GET", ObservedURL: "https://example.com/api", Status: "candidate", CreatedAt: now}), want: "contains_personal_data"},
		{name: "candidate confidence", err: ValidateAPICandidate(APICandidate{CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Method: "GET", ObservedURL: "https://example.com/api", ContainsPersonalData: "unknown", Status: "candidate", Confidence: -0.1, CreatedAt: now}), want: "confidence"},
		{name: "schema id", err: ValidateAPICandidateSchema(APICandidateSchema{CandidateID: "api_cand_1", SchemaType: "response", SchemaJSON: `{}`, CreatedAt: now}), want: "schema_id"},
		{name: "schema candidate", err: ValidateAPICandidateSchema(APICandidateSchema{SchemaID: "schema_1", SchemaType: "response", SchemaJSON: `{}`, CreatedAt: now}), want: "candidate_id"},
		{name: "schema type", err: ValidateAPICandidateSchema(APICandidateSchema{SchemaID: "schema_1", CandidateID: "api_cand_1", SchemaJSON: `{}`, CreatedAt: now}), want: "schema_type"},
		{name: "schema sample", err: ValidateAPICandidateSchema(APICandidateSchema{SchemaID: "schema_1", CandidateID: "api_cand_1", SchemaType: "response", SchemaJSON: `{}`, SampleCount: -1, CreatedAt: now}), want: "sample_count"},
		{name: "validation id", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Passed: true, Status: "validated", CreatedAt: now}), want: "validation_id"},
		{name: "validation candidate", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Passed: true, Status: "validated", CreatedAt: now}), want: "candidate_id"},
		{name: "validation owner", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", Passed: true, Status: "validated", CreatedAt: now}), want: "task_id"},
		{name: "validation status", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", CreatedAt: now}), want: "status"},
		{name: "validation passed mismatch", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Passed: true, Status: "needs_review", CreatedAt: now, Issues: []APIValidationIssue{{Code: "terms", Message: "terms issue"}}}), want: "passed validation"},
		{name: "validation issue message", err: ValidateAPICandidateValidationResult(APICandidateValidationResult{ValidationID: "api_val_1", CandidateID: "api_cand_1", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Status: "needs_review", Issues: []APIValidationIssue{{Code: "terms"}}, CreatedAt: now}), want: "message"},
		{name: "coverage report id", err: ValidateAPICoverageReport(APICoverageReport{Kind: modulecore.ArtifactKindReport, TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", CreatedAt: now}), want: "artifact_id"},
		{name: "coverage artifact kind", err: ValidateAPICoverageReport(APICoverageReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"), TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", CreatedAt: now}), want: "artifact_kind"},
		{name: "coverage owner", err: ValidateAPICoverageReport(APICoverageReport{ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"), Kind: modulecore.ArtifactKindReport, CreatedAt: now}), want: "task_id"},
		{name: "artifact id", err: ValidateAPIArtifact(APIArtifact{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "artifact_id"},
		{name: "artifact owner", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", Kind: modulecore.ArtifactKindSpecification, Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "task_id"},
		{name: "artifact type", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "artifact_type"},
		{name: "artifact title", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", Kind: modulecore.ArtifactKindSpecification, TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Type: "observed_openapi", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "title"},
		{name: "artifact status", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", Kind: modulecore.ArtifactKindSpecification, TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Type: "observed_openapi", Title: "Observed OpenAPI", Content: "openapi: 3.1.0", CreatedAt: now}), want: "status"},
		{name: "artifact kind", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "artifact_kind"},
		{name: "artifact kind mismatch", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", Kind: modulecore.ArtifactKindReport, TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "does not match"},
		{name: "artifact kind invalid", err: ValidateAPIArtifact(APIArtifact{ArtifactID: "art_00000000-0000-5000-8000-00000000000a", Kind: "spec", TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", Type: "observed_openapi", Title: "Observed OpenAPI", Status: "generated", Content: "openapi: 3.1.0", CreatedAt: now}), want: "artifact_kind"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.err == nil || !strings.Contains(tt.err.Error(), tt.want) {
				t.Fatalf("err=%v, want %s", tt.err, tt.want)
			}
		})
	}
}

func TestValidateTraceRunRejectsMalformedAndLegacyIdentity(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	tests := []TraceRun{
		{TaskID: "trace_legacy", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", TracePath: "trace.json", CreatedAt: now},
		{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "trace_legacy", ActorID: "mio", TracePath: "trace.json", CreatedAt: now},
		{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "worker", TracePath: "trace.json", CreatedAt: now},
	}
	for _, item := range tests {
		if err := ValidateTraceRun(item); err == nil {
			t.Fatalf("legacy or malformed identity unexpectedly passed: %#v", item)
		}
	}
}

func fixedBrowserTraceValidationTime() time.Time {
	return time.Date(2026, 5, 20, 6, 40, 0, 0, time.UTC)
}

func TestValidateAPICandidateValidationResultRequiresReviewerForReviewNote(t *testing.T) {
	item := APICandidateValidationResult{
		ValidationID: "api_val_owner_audit",
		CandidateID:  "api_cand_1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Status:     "needs_review",
		ReviewNote: "terms checked",
		CreatedAt:  fixedBrowserTraceValidationTime(),
		Issues: []APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
	}
	if err := ValidateAPICandidateValidationResult(item); err == nil || !strings.Contains(err.Error(), "reviewer") {
		t.Fatalf("validation error = %v, want reviewer requirement", err)
	}
	item.Reviewer = "ren"
	if err := ValidateAPICandidateValidationResult(item); err != nil {
		t.Fatalf("owner-reviewed validation should pass: %v", err)
	}
}

func TestValidateAPICandidateValidationResultAcceptsRejectedWithIssues(t *testing.T) {
	item := APICandidateValidationResult{
		ValidationID: "api_val_rejected",
		CandidateID:  "api_cand_1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Status: "rejected",
		Issues: []APIValidationIssue{{
			Code:     "terms_review_required",
			Message:  "terms review is required",
			Severity: "high",
		}},
		CreatedAt: fixedBrowserTraceValidationTime(),
	}
	if err := ValidateAPICandidateValidationResult(item); err != nil {
		t.Fatalf("rejected validation should be accepted: %v", err)
	}
}

func TestValidateAPICandidateValidationResultRejectsInvalidRejectedState(t *testing.T) {
	base := APICandidateValidationResult{
		ValidationID: "api_val_rejected_invalid",
		CandidateID:  "api_cand_1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Status:    "rejected",
		CreatedAt: fixedBrowserTraceValidationTime(),
	}
	for _, item := range []APICandidateValidationResult{
		base,
		func() APICandidateValidationResult {
			item := base
			item.Passed = true
			item.Issues = []APIValidationIssue{{Code: "risk", Message: "risk review is required"}}
			return item
		}(),
	} {
		if err := ValidateAPICandidateValidationResult(item); err == nil {
			t.Fatalf("invalid rejected validation unexpectedly passed: %#v", item)
		}
	}
}

// TestValidateAPIArtifactRejectsNonCanonicalArtifactID pins the Step13 contract
// that a persisted APIArtifact carries a canonical ArtifactID: the shared
// modules/core validator (art_ prefix plus UUIDv5 or UUIDv7) must reject an
// opaque but non-empty ID. Opaque IDs such as "art_openapi_1" are what the
// browsertrace fixtures use today, so accepting them would keep a second,
// non-canonical artifact identity next to the canonical one.
func TestValidateAPIArtifactRejectsNonCanonicalArtifactID(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	invalid := []struct {
		artifactID string
		want       string
	}{
		{"art_openapi_1", "artifact_id"},                                     // opaque ID used by the store fixtures
		{"art_1", "artifact_id"},                                             // opaque ID used by the validation fixtures
		{"art_00000000-0000-1000-8000-000000000003", "UUIDv1"},               // UUIDv1 is not a canonical version
		{"art_00000000000000000000000000000003", "UUIDv0"},                   // hyphen-less form parses as UUIDv0, not canonical
		{"00000000-0000-5000-8000-000000000003", "must use prefix"},          // missing art_ prefix
		{"artifact_00000000-0000-5000-8000-000000000003", "must use prefix"}, // wrong artifact prefix
	}
	for _, tt := range invalid {
		err := ValidateAPIArtifact(APIArtifact{
			ArtifactID: modulecore.ArtifactID(tt.artifactID),
			Kind:       modulecore.ArtifactKindSpecification,
			TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Type:      "observed_openapi",
			Title:     "Observed OpenAPI",
			Status:    "generated",
			Content:   "openapi: 3.1.0",
			CreatedAt: now,
		})
		if err == nil {
			t.Errorf("ValidateAPIArtifact() accepted non-canonical artifact_id %q, want rejection", tt.artifactID)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("ValidateAPIArtifact(%q) error = %v, want reason %q", tt.artifactID, err, tt.want)
		}
	}
}

// TestValidateAPIArtifactAcceptsCanonicalArtifactIDVersions pins that the shared
// validator accepts both canonical UUID versions for ArtifactID, so Step13 does
// not reject the deterministic migration IDs (UUIDv5) next to the runtime IDs
// (UUIDv7).
func TestValidateAPIArtifactAcceptsCanonicalArtifactIDVersions(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	for _, artifactID := range []string{
		"art_00000000-0000-5000-8000-00000000000a", // UUIDv5: NewMigrationID shape
		"art_018db8d4-8a2a-7a3e-9a1c-2b5f0d1e4c20", // UUIDv7: NewArtifactID shape
	} {
		if err := ValidateAPIArtifact(APIArtifact{
			ArtifactID:  modulecore.ArtifactID(artifactID),
			ContentHash: testContentHashDigest("openapi: 3.1.0"),
			Kind:        modulecore.ArtifactKindSpecification,
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Type:      "observed_openapi",
			Title:     "Observed OpenAPI",
			Status:    "generated",
			Content:   "openapi: 3.1.0",
			CreatedAt: now,
		}); err != nil {
			t.Errorf("ValidateAPIArtifact() rejected canonical artifact_id %q: %v", artifactID, err)
		}
	}
}

// TestValidateAPIArtifactRejectsUnknownArtifactType pins that every accepted
// artifact_type maps to a browsertrace-owned ArtifactKind. A type the domain
// does not know must be rejected instead of silently defaulting to a kind.
func TestValidateAPIArtifactRejectsUnknownArtifactType(t *testing.T) {
	now := fixedBrowserTraceValidationTime()
	for _, artifactType := range []string{"harvest_summary", "observed-openapi"} {
		err := ValidateAPIArtifact(APIArtifact{
			ArtifactID: "art_00000000-0000-5000-8000-00000000000a",
			Kind:       modulecore.ArtifactKindReport,
			TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Type:      artifactType,
			Title:     "Harvest summary",
			Status:    "generated",
			Content:   "openapi: 3.1.0",
			CreatedAt: now,
		})
		if err == nil {
			t.Errorf("ValidateAPIArtifact() accepted unknown artifact_type %q, want rejection", artifactType)
			continue
		}
		if !strings.Contains(err.Error(), "unknown browsertrace artifact type") {
			t.Errorf("ValidateAPIArtifact(%q) error = %v, want unknown artifact type reason", artifactType, err)
		}
	}
}

// TestAPIArtifactKeepsArtifactKindInJSON pins the persisted shape: an
// APIArtifact decoded from a store payload must keep artifact_kind when it is
// encoded again, otherwise the SQLite payload column and the JSONL row lose the
// canonical kind of the artifact.
func TestAPIArtifactKeepsArtifactKindInJSON(t *testing.T) {
	raw := `{"artifact_id":"art_00000000-0000-5000-8000-00000000000a","artifact_kind":"specification","task_id":"tsk_00000000-0000-5000-8000-000000000001","run_id":"run_00000000-0000-5000-8000-000000000002","actor_id":"mio","artifact_type":"observed_openapi","title":"Observed OpenAPI","status":"generated","content":"openapi: 3.1.0","created_at":"2026-05-20T06:40:00Z"}`
	var item APIArtifact
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal artifact payload: %v", err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal artifact payload: %v", err)
	}
	if !strings.Contains(string(encoded), `"artifact_kind":"specification"`) {
		t.Errorf("artifact payload lost artifact_kind, got %s", encoded)
	}
}

// TestArtifactKindForAPIArtifactType pins the browsertrace-owned content-role to
// ArtifactKind correspondence in one place: every persisted type maps to its
// canonical kind and an unknown type errors instead of falling back.
func TestArtifactKindForAPIArtifactType(t *testing.T) {
	tests := []struct {
		artifactType string
		want         modulecore.ArtifactKind
	}{
		{APIArtifactTypeObservedOpenAPI, modulecore.ArtifactKindSpecification},
		{APIArtifactTypeCoverageReport, modulecore.ArtifactKindReport},
		{APIArtifactTypeRiskAssessment, modulecore.ArtifactKindReport},
		{APIArtifactTypeEndpointInventory, modulecore.ArtifactKindDocument},
		{APIArtifactTypeFetcherPlan, modulecore.ArtifactKindDraft},
		{APIArtifactTypeFetcherProposal, modulecore.ArtifactKindDraft},
		{APIArtifactTypeClientDraft, modulecore.ArtifactKindDraft},
	}
	// Coverage is pinned against the domain-owned mapping itself: no content role
	// may exist in the mapping without a pinned expectation here.
	if len(apiArtifactKindByType) != len(tests) {
		t.Fatalf("apiArtifactKindByType = %d entries, want %d browsertrace content roles", len(apiArtifactKindByType), len(tests))
	}
	pinned := map[string]modulecore.ArtifactKind{}
	for _, tt := range tests {
		pinned[tt.artifactType] = tt.want
	}
	for artifactType, mapped := range apiArtifactKindByType {
		want, ok := pinned[artifactType]
		if !ok {
			t.Errorf("apiArtifactKindByType has unpinned content role %q mapped to %q", artifactType, mapped)
			continue
		}
		if mapped != want {
			t.Errorf("apiArtifactKindByType[%q] = %q, want %q", artifactType, mapped, want)
		}
	}
	for _, tt := range tests {
		got, err := ArtifactKindForAPIArtifactType(tt.artifactType)
		if err != nil {
			t.Errorf("ArtifactKindForAPIArtifactType(%q) error = %v", tt.artifactType, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ArtifactKindForAPIArtifactType(%q) = %q, want %q", tt.artifactType, got, tt.want)
		}
	}
	if _, err := ArtifactKindForAPIArtifactType("harvest_summary"); err == nil {
		t.Error("ArtifactKindForAPIArtifactType(\"harvest_summary\") = nil error, want rejection without fallback")
	}
}

// TestAPIArtifactCarriesContentHashAndSupersededByInJSON pins the Step13 Test
// items "Content hash" and "Supersede" for the browsertrace APIArtifact. Both
// stores persist the domain item as a JSON payload, so a content hash or a
// supersession reference that the type does not carry is silently lost at rest
// and cannot be reverified after a reload.
func TestAPIArtifactCarriesContentHashAndSupersededByInJSON(t *testing.T) {
	const content = "openapi: 3.1.0"
	raw := browserTraceAPIArtifactPayload(content, testContentHashDigest(content), `"superseded_by":"art_00000000-0000-5000-8000-00000000000b",`)
	var item APIArtifact
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal artifact payload: %v", err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal artifact payload: %v", err)
	}
	for _, want := range []string{
		`"content_hash":"` + testContentHashDigest(content) + `"`,
		`"superseded_by":"art_00000000-0000-5000-8000-00000000000b"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("artifact payload lost %s, got %s", want, encoded)
		}
	}
}

// TestValidateAPIArtifactChecksContentHashAndSupersession pins the domain-side
// guarantees for one artifact row: content_hash is required, must be the
// sha256-prefixed lowercase digest of the persisted content bytes, and
// superseded_by must be a canonical ArtifactID that is not the artifact itself.
// Whether the referenced artifact exists, shares the owner and forms no cycle
// spans several rows and is closed by the supersede operation, not by this
// single-row domain check.
func TestValidateAPIArtifactChecksContentHashAndSupersession(t *testing.T) {
	const content = "openapi: 3.1.0"
	digest := testContentHashDigest(content)
	bareDigest := strings.TrimPrefix(digest, "sha256:")
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{"missing content hash", browserTraceAPIArtifactPayload(content, "", ""), "content_hash is required"},
		{"digest of other bytes", browserTraceAPIArtifactPayload(content, testContentHashDigest(content+"."), ""), "does not match content"},
		{"uppercase digest", browserTraceAPIArtifactPayload(content, "sha256:"+strings.ToUpper(bareDigest), ""), "content_hash must be"},
		{"digest without prefix", browserTraceAPIArtifactPayload(content, bareDigest, ""), "content_hash must be"},
		{"self supersession", browserTraceAPIArtifactPayload(content, digest, `"superseded_by":"art_00000000-0000-5000-8000-00000000000a",`), "must not reference the artifact itself"},
		{"opaque superseded_by", browserTraceAPIArtifactPayload(content, digest, `"superseded_by":"art_1",`), "superseded_by"},
		{"uuidv4 superseded_by", browserTraceAPIArtifactPayload(content, digest, `"superseded_by":"art_00000000-0000-4000-8000-00000000000a",`), "superseded_by"},
	}
	for _, tt := range cases {
		var item APIArtifact
		if err := json.Unmarshal([]byte(tt.document), &item); err != nil {
			t.Fatalf("%s: unmarshal artifact payload: %v", tt.name, err)
		}
		err := ValidateAPIArtifact(item)
		if err == nil {
			t.Errorf("%s: ValidateAPIArtifact() accepted the artifact, want rejection with %q", tt.name, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want reason %q", tt.name, err, tt.want)
		}
	}
}

// TestAPICoverageReportBodyDigestTreatsNilAndEmptyListsAsOneContent pins the body
// contract that a missing list and a present-but-empty list are the same content and
// therefore one digest. encoding/json encodes a nil slice as null and an empty slice
// as [], so the domain projection has to normalize the lists itself rather than leave
// that to the encoder, otherwise the persisted row and the digest disagree on nothing
// being traced.
func TestAPICoverageReportBodyDigestTreatsNilAndEmptyListsAsOneContent(t *testing.T) {
	nilLists := APICoverageReport{}
	emptyLists := APICoverageReport{
		ObservedFlows:         []string{},
		ObservedEndpoints:     []string{},
		MissingFlows:          []string{},
		RecommendedNextTraces: []string{},
	}
	nilBody, emptyBody := string(APICoverageReportBodyBytes(nilLists)), string(APICoverageReportBodyBytes(emptyLists))
	if nilBody != emptyBody {
		t.Errorf("coverage body bytes differ, nil=%s empty=%s", nilBody, emptyBody)
	}
	if nilDigest, emptyDigest := ComputeAPICoverageReportContentHash(nilLists), ComputeAPICoverageReportContentHash(emptyLists); nilDigest != emptyDigest {
		t.Errorf("coverage digest differs, nil=%s empty=%s", nilDigest, emptyDigest)
	}
}

// testCoverageBodyWithDigest returns a coverage report whose content_hash is the
// digest of its own body, so a test only varies the one field it is about.
func testCoverageBodyWithDigest(observedFlows, observedEndpoints, missingFlows, recommended []string) APICoverageReport {
	item := APICoverageReport{
		ArtifactID:            modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
		Kind:                  modulecore.ArtifactKindReport,
		TaskID:                "tsk_00000000-0000-5000-8000-000000000001",
		RunID:                 "run_00000000-0000-5000-8000-000000000002",
		ActorID:               "mio",
		ObservedFlows:         observedFlows,
		ObservedEndpoints:     observedEndpoints,
		MissingFlows:          missingFlows,
		RecommendedNextTraces: recommended,
		CreatedAt:             fixedBrowserTraceValidationTime(),
	}
	item.ContentHash = ComputeAPICoverageReportContentHash(item)
	return item
}

// TestAPICoverageReportContentHashCoversOnlyBody pins that the coverage digest covers
// the body lists and nothing else: a reminted ArtifactID, a later created_at and an
// appended supersession all keep the digest, while one changed entry or a reordered
// list breaks it. A newly minted ArtifactID is never reused as the hash.
func TestAPICoverageReportContentHashCoversOnlyBody(t *testing.T) {
	base := testCoverageBodyWithDigest([]string{"network_trace"}, []string{"GET /api/items"}, []string{"error cases"}, []string{"empty result"})
	if err := ValidateAPICoverageReport(base); err != nil {
		t.Fatalf("ValidateAPICoverageReport() error = %v", err)
	}

	reminted := base
	reminted.ArtifactID = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000c")
	reminted.CreatedAt = base.CreatedAt.Add(time.Hour)
	if got := ComputeAPICoverageReportContentHash(reminted); got != base.ContentHash {
		t.Errorf("digest after remint = %s, want the body digest %s", got, base.ContentHash)
	}
	reminted.ContentHash = base.ContentHash
	if err := ValidateAPICoverageReport(reminted); err != nil {
		t.Errorf("ValidateAPICoverageReport() reminted artifact error = %v", err)
	}

	superseded := base
	superseded.SupersededBy = modulecore.ArtifactID("art_00000000-0000-5000-8000-00000000000b")
	if got := ComputeAPICoverageReportContentHash(superseded); got != base.ContentHash {
		t.Errorf("digest after supersession = %s, want the body digest %s", got, base.ContentHash)
	}

	changed := base
	changed.ObservedEndpoints = []string{"POST /api/items"}
	if got := ComputeAPICoverageReportContentHash(changed); got == base.ContentHash {
		t.Errorf("digest of a changed list = %s, want it to differ from %s", got, base.ContentHash)
	}

	first := testCoverageBodyWithDigest(nil, []string{"GET /a", "GET /b"}, nil, nil)
	reordered := testCoverageBodyWithDigest(nil, []string{"GET /b", "GET /a"}, nil, nil)
	if first.ContentHash == reordered.ContentHash {
		t.Errorf("reordered list digested the same %s", first.ContentHash)
	}
	// A body that only differs by which list holds an entry is a different content.
	moved := testCoverageBodyWithDigest(nil, []string{"GET /a"}, []string{"GET /b"}, nil)
	if moved.ContentHash == first.ContentHash {
		t.Errorf("entry moved between lists digested the same %s", moved.ContentHash)
	}
}

// TestAPICoverageReportBodyBytesEncodingIsFixed pins the body bytes for quotes, a
// newline and non-ASCII text, so the digest of a persisted row stays reproducible from
// the row itself and does not depend on a caller re-normalizing the lists.
func TestAPICoverageReportBodyBytesEncodingIsFixed(t *testing.T) {
	want := `{"observed_flows":["引用 \"と\" 改行\n付き 日本語"],"observed_endpoints":[],"missing_flows":[],"recommended_next_traces":[]}`
	item := APICoverageReport{ObservedFlows: []string{"引用 \"と\" 改行\n付き 日本語"}}
	if got := string(APICoverageReportBodyBytes(item)); got != want {
		t.Errorf("body bytes = %s, want %s", got, want)
	}
	if got, wantDigest := ComputeAPICoverageReportContentHash(item), testContentHashDigest(want); got != wantDigest {
		t.Errorf("content hash = %s, want %s", got, wantDigest)
	}
}

// TestValidateAPICoverageReportAcceptsCanonicalSuccessors pins that superseded_by takes
// both migrated UUIDv5 and newly minted UUIDv7 ArtifactIDs, and rejects a self
// reference, while the body digest is untouched by the supersession.
func TestValidateAPICoverageReportAcceptsCanonicalSuccessors(t *testing.T) {
	for _, successor := range []string{
		"art_00000000-0000-5000-8000-00000000000b",
		"art_00000000-0000-7000-8000-00000000000d",
	} {
		item := testCoverageBodyWithDigest([]string{"network_trace"}, nil, nil, nil)
		item.SupersededBy = modulecore.ArtifactID(successor)
		if err := ValidateAPICoverageReport(item); err != nil {
			t.Errorf("ValidateAPICoverageReport() successor %s error = %v", successor, err)
		}
	}

	self := testCoverageBodyWithDigest([]string{"network_trace"}, nil, nil, nil)
	self.SupersededBy = self.ArtifactID
	err := ValidateAPICoverageReport(self)
	if err == nil {
		t.Fatal("ValidateAPICoverageReport() accepted a self supersession")
	}
	if !strings.Contains(err.Error(), "must not reference the artifact itself") {
		t.Errorf("error = %v, want self reference reason", err)
	}
}

// TestValidateAPIArtifactAcceptsContentHashOfNonASCIIContent pins that the digest
// covers the content bytes as persisted, non-ASCII included, and that a canonical
// UUIDv5 superseded_by reference is accepted because Step13 keeps the deterministic
// migration IDs next to the runtime IDs.
func TestValidateAPIArtifactAcceptsContentHashOfNonASCIIContent(t *testing.T) {
	const content = "openapi: 3.1.0 # 日本語のタイトル"
	document := browserTraceAPIArtifactPayload(content, testContentHashDigest(content), `"superseded_by":"art_00000000-0000-5000-8000-00000000000b",`)
	var item APIArtifact
	if err := json.Unmarshal([]byte(document), &item); err != nil {
		t.Fatalf("unmarshal artifact payload: %v", err)
	}
	if err := ValidateAPIArtifact(item); err != nil {
		t.Errorf("ValidateAPIArtifact() rejected a matching content hash and canonical superseded_by: %v", err)
	}
}

// browserTraceAPIArtifactPayload builds one persisted APIArtifact payload. The
// digest and the extra fields are spliced as raw JSON so the same document can
// describe a row that carries, or is missing, the Step13 content identity fields.
func browserTraceAPIArtifactPayload(content, contentHash, extraFields string) string {
	hashField := ""
	if contentHash != "" {
		hashField = `"content_hash":"` + contentHash + `",`
	}
	return `{"artifact_id":"art_00000000-0000-5000-8000-00000000000a","artifact_kind":"specification","task_id":"tsk_00000000-0000-5000-8000-000000000001","run_id":"run_00000000-0000-5000-8000-000000000002","actor_id":"mio","artifact_type":"observed_openapi","title":"Observed OpenAPI","status":"generated","content":"` + content + `",` + hashField + extraFields + `"created_at":"2026-05-20T06:40:00Z"}`
}

// browserTraceAPICoverageReportPayload builds a persisted coverage payload. The
// body lists stay as raw JSON so a test can pass a body whose digest does not
// match, which the current type cannot express as a struct field.
func browserTraceAPICoverageReportPayload(body, contentHash, extraFields string) string {
	hashField := ""
	if contentHash != "" {
		hashField = `"content_hash":"` + contentHash + `",`
	}
	return `{"artifact_id":"art_00000000-0000-5000-8000-000000000003","artifact_kind":"report","task_id":"tsk_00000000-0000-5000-8000-000000000001","run_id":"run_00000000-0000-5000-8000-000000000002","actor_id":"mio",` + body + `,` + hashField + extraFields + `"created_at":"2026-05-20T06:40:00Z"}`
}

// coverageBodyDigest is the digest of the coverage body bytes in the canonical
// order declared by the browsertrace domain: observed_flows, observed_endpoints,
// missing_flows, recommended_next_traces as compact JSON. Identity fields,
// created_at, content_hash itself and superseded_by are excluded, so a remint or a
// later supersession never changes the content digest.
func coverageBodyDigest(observedFlows, observedEndpoints, missingFlows, recommended string) string {
	return testContentHashDigest(`{"observed_flows":[` + observedFlows + `],"observed_endpoints":[` + observedEndpoints + `],"missing_flows":[` + missingFlows + `],"recommended_next_traces":[` + recommended + `]}`)
}

// TestAPICoverageReportCarriesContentHashAndSupersededByInJSON pins the Step13 Test
// items "Content hash" and "Supersede" for the browsertrace APICoverageReport, which
// both stores persist as a JSON payload. A digest or supersession reference that the
// type does not carry is silently lost at rest and cannot be reverified after reload.
func TestAPICoverageReportCarriesContentHashAndSupersededByInJSON(t *testing.T) {
	digest := coverageBodyDigest(`"network_trace"`, `"GET /api/items"`, `"error cases"`, `"empty result"`)
	body := `"observed_flows":["network_trace"],"observed_endpoints":["GET /api/items"],"missing_flows":["error cases"],"recommended_next_traces":["empty result"]`
	raw := browserTraceAPICoverageReportPayload(body, digest, `"superseded_by":"art_00000000-0000-5000-8000-00000000000b",`)
	var item APICoverageReport
	if err := json.Unmarshal([]byte(raw), &item); err != nil {
		t.Fatalf("unmarshal coverage payload: %v", err)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("marshal coverage payload: %v", err)
	}
	for _, want := range []string{
		`"content_hash":"` + digest + `"`,
		`"superseded_by":"art_00000000-0000-5000-8000-00000000000b"`,
	} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("coverage payload lost %s, got %s", want, encoded)
		}
	}
}

// TestValidateAPICoverageReportChecksContentHashAndSupersession pins the single-row
// coverage guarantees: content_hash is required, must be the sha256-prefixed
// lowercase digest of the canonical body bytes, and superseded_by must be a
// canonical ArtifactID that is not the report itself. Existence, owner agreement and
// cycle freedom span rows and belong to the supersede operation, not this check.
func TestValidateAPICoverageReportChecksContentHashAndSupersession(t *testing.T) {
	body := `"observed_flows":["network_trace"],"observed_endpoints":["GET /api/items"],"missing_flows":["error cases"],"recommended_next_traces":["empty result"]`
	digest := coverageBodyDigest(`"network_trace"`, `"GET /api/items"`, `"error cases"`, `"empty result"`)
	bareDigest := strings.TrimPrefix(digest, "sha256:")
	cases := []struct {
		name     string
		document string
		want     string
	}{
		{"missing content hash", browserTraceAPICoverageReportPayload(body, "", ""), "content_hash is required"},
		{"digest of other bytes", browserTraceAPICoverageReportPayload(body, coverageBodyDigest(`"network_trace"`, `"GET /api/items"`, `"error cases"`, `"other trace"`), ""), "does not match content"},
		{"digest without prefix", browserTraceAPICoverageReportPayload(body, bareDigest, ""), "content_hash must be"},
		{"self supersession", browserTraceAPICoverageReportPayload(body, digest, `"superseded_by":"art_00000000-0000-5000-8000-000000000003",`), "must not reference the artifact itself"},
		{"opaque superseded_by", browserTraceAPICoverageReportPayload(body, digest, `"superseded_by":"art_1",`), "superseded_by"},
	}
	for _, tt := range cases {
		var item APICoverageReport
		if err := json.Unmarshal([]byte(tt.document), &item); err != nil {
			t.Fatalf("%s: unmarshal coverage payload: %v", tt.name, err)
		}
		err := ValidateAPICoverageReport(item)
		if err == nil {
			t.Errorf("%s: ValidateAPICoverageReport() accepted the coverage report, want rejection with %q", tt.name, tt.want)
			continue
		}
		if !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: error = %v, want reason %q", tt.name, err, tt.want)
		}
	}
}
