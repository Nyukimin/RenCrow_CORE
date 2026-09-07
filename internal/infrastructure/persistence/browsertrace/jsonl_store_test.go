package browsertrace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestJSONLStoreBrowserTraceToAPI(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	run := domaintrace.TraceRun{TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio", TracePath: "traces/trace_1", CreatedAt: now}
	candidate := domaintrace.APICandidate{
		CandidateID: "api_cand_1",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}
	schema := domaintrace.APICandidateSchema{
		SchemaID:    "schema_1",
		CandidateID: "api_cand_1",
		SchemaType:  "response",
		SchemaJSON:  `{"type":"object"}`,
		SampleCount: 1,
		CreatedAt:   now,
	}
	validation := domaintrace.APICandidateValidationResult{
		ValidationID: "api_val_1",
		CandidateID:  "api_cand_1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Passed: false,
		Status: "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		CreatedAt: now,
	}
	coverage := domaintrace.APICoverageReport{
		ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
		Kind:       modulecore.ArtifactKindReport,
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		ObservedEndpoints: []string{"GET /api/items"},
		CreatedAt:         now,
	}

	if err := store.SaveTraceRun(ctx, run); err != nil {
		t.Fatalf("SaveTraceRun() error = %v", err)
	}
	if err := store.SaveAPICandidate(ctx, candidate); err != nil {
		t.Fatalf("SaveAPICandidate() error = %v", err)
	}
	if err := store.SaveAPICandidateSchema(ctx, schema); err != nil {
		t.Fatalf("SaveAPICandidateSchema() error = %v", err)
	}
	if err := store.SaveAPICandidateValidationResult(ctx, validation); err != nil {
		t.Fatalf("SaveAPICandidateValidationResult() error = %v", err)
	}
	if err := store.SaveAPICoverageReport(ctx, coverage); err != nil {
		t.Fatalf("SaveAPICoverageReport() error = %v", err)
	}
	if err := store.SaveAPIArtifact(ctx, domaintrace.APIArtifact{
		ArtifactID: "art_openapi_1",
		TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Type:      "observed_openapi",
		Title:     "Observed OpenAPI",
		Status:    "generated",
		Content:   "openapi: 3.1.0",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("SaveAPIArtifact() error = %v", err)
	}

	runs, err := store.ListTraceRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].RunID != "run_00000000-0000-5000-8000-000000000002" {
		t.Fatalf("ListTraceRuns() = %#v, %v", runs, err)
	}
	candidates, err := store.ListAPICandidates(ctx, 10)
	if err != nil || len(candidates) != 1 || candidates[0].CandidateID != "api_cand_1" {
		t.Fatalf("ListAPICandidates() = %#v, %v", candidates, err)
	}
	schemas, err := store.ListAPICandidateSchemas(ctx, 10)
	if err != nil || len(schemas) != 1 || schemas[0].SchemaID != "schema_1" {
		t.Fatalf("ListAPICandidateSchemas() = %#v, %v", schemas, err)
	}
	validations, err := store.ListAPICandidateValidationResults(ctx, 10)
	if err != nil || len(validations) != 1 || validations[0].ValidationID != "api_val_1" {
		t.Fatalf("ListAPICandidateValidationResults() = %#v, %v", validations, err)
	}
	reports, err := store.ListAPICoverageReports(ctx, 10)
	if err != nil || len(reports) != 1 || reports[0].ArtifactID != modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003") {
		t.Fatalf("ListAPICoverageReports() = %#v, %v", reports, err)
	}
	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_openapi_1" {
		t.Fatalf("ListAPIArtifacts() = %#v, %v", artifacts, err)
	}
}

func TestJSONLStoreRejectsWriteMethodCandidate(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	err := store.SaveAPICandidate(context.Background(), domaintrace.APICandidate{
		CandidateID: "api_cand_1",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "DELETE",
		ObservedURL:          "https://example.com/api/items/1",
		ContainsPersonalData: "unknown",
		Status:               "candidate",
	})
	if err == nil {
		t.Fatal("expected DELETE candidate to fail")
	}
}

func TestJSONLStoreFindAPICandidateByIDReturnsLatestExactRecord(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	first := domaintrace.APICandidate{
		CandidateID: "candidate-exact",
		TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Method:               "GET",
		ObservedURL:          "https://example.com/api/items",
		ContainsPersonalData: "unknown",
		RiskLevel:            "low",
		Status:               "candidate",
		CreatedAt:            now,
	}
	latest := first
	latest.ObservedURL = "https://example.com/api/items?latest=true"
	latest.CreatedAt = now.Add(time.Minute)
	suffix := first
	suffix.CandidateID = "candidate-exact-suffix"

	for _, item := range []domaintrace.APICandidate{first, suffix, latest} {
		if err := store.SaveAPICandidate(ctx, item); err != nil {
			t.Fatalf("SaveAPICandidate(%q) failed: %v", item.CandidateID, err)
		}
	}

	got, found, err := store.FindAPICandidateByID(ctx, "candidate-exact")
	if err != nil || !found || got.CandidateID != "candidate-exact" || got.ObservedURL != latest.ObservedURL {
		t.Fatalf("FindAPICandidateByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindAPICandidateByID(ctx, "missing"); err != nil || found || !reflect.DeepEqual(got, domaintrace.APICandidate{}) {
		t.Fatalf("missing FindAPICandidateByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestJSONLStoreFindAPICandidateValidationResultPreservesOwnerAuditFields(t *testing.T) {
	store := NewJSONLStore(t.TempDir())
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 1, 2, 3, 0, time.UTC)
	first := domaintrace.APICandidateValidationResult{
		ValidationID: "validation-exact",
		CandidateID:  "candidate-1",
		TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
		Passed: false,
		Status: "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		Reviewer:   "ren",
		ReviewNote: "initial review",
		CreatedAt:  now,
	}
	latest := first
	latest.ReviewNote = "latest review"
	latest.CreatedAt = now.Add(time.Minute)
	suffix := first
	suffix.ValidationID = "validation-exact-suffix"

	for _, item := range []domaintrace.APICandidateValidationResult{first, suffix, latest} {
		if err := store.SaveAPICandidateValidationResult(ctx, item); err != nil {
			t.Fatalf("SaveAPICandidateValidationResult(%q) failed: %v", item.ValidationID, err)
		}
	}

	got, found, err := store.FindAPICandidateValidationResultByID(ctx, "validation-exact")
	if err != nil || !found || got.Reviewer != "ren" || got.ReviewNote != "latest review" {
		t.Fatalf("FindAPICandidateValidationResultByID() = %#v, found=%v, err=%v", got, found, err)
	}
	if got, found, err := store.FindAPICandidateValidationResultByID(ctx, "missing"); err != nil || found || !reflect.DeepEqual(got, domaintrace.APICandidateValidationResult{}) {
		t.Fatalf("missing FindAPICandidateValidationResultByID() = %#v, found=%v, err=%v", got, found, err)
	}
}

func TestJSONLStoreFindBrowserTraceByIDRejectsMalformedRecord(t *testing.T) {
	root := t.TempDir()
	store := NewJSONLStore(root)
	if err := os.WriteFile(filepath.Join(root, "api_candidate.jsonl"), []byte("{malformed}\n"), 0644); err != nil {
		t.Fatalf("write malformed candidate: %v", err)
	}
	if _, found, err := store.FindAPICandidateByID(context.Background(), "candidate"); err == nil || found {
		t.Fatalf("expected malformed candidate error, found=%v err=%v", found, err)
	}
	if err := os.WriteFile(filepath.Join(root, "api_candidate_validation.jsonl"), []byte("{malformed}\n"), 0644); err != nil {
		t.Fatalf("write malformed validation: %v", err)
	}
	if _, found, err := store.FindAPICandidateValidationResultByID(context.Background(), "validation"); err == nil || found {
		t.Fatalf("expected malformed validation error, found=%v err=%v", found, err)
	}
}
