package browsertrace

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
)

func TestSQLiteStoreBrowserTraceToAPI(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "browser_trace.sqlite"))
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	ctx := context.Background()
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)

	run := domaintrace.TraceRun{TraceRunID: "trace_1", TracePath: "traces/trace_1", CreatedAt: now}
	candidate := domaintrace.APICandidate{
		CandidateID:          "api_cand_1",
		TraceRunID:           "trace_1",
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
		TraceRunID:   "trace_1",
		Status:       "needs_review",
		Issues: []domaintrace.APIValidationIssue{{
			Code:    "terms_review_required",
			Message: "terms review is required",
		}},
		CreatedAt: now,
	}
	coverage := domaintrace.APICoverageReport{ReportID: "coverage_1", TraceRunID: "trace_1", CreatedAt: now}
	artifact := domaintrace.APIArtifact{
		ArtifactID: "art_openapi_1",
		TraceRunID: "trace_1",
		Type:       "observed_openapi",
		Title:      "Observed OpenAPI",
		Status:     "generated",
		Content:    "openapi: 3.1.0",
		CreatedAt:  now,
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
	if err := store.SaveAPIArtifact(ctx, artifact); err != nil {
		t.Fatalf("SaveAPIArtifact() error = %v", err)
	}

	runs, err := store.ListTraceRuns(ctx, 10)
	if err != nil || len(runs) != 1 || runs[0].TraceRunID != "trace_1" {
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
	if err != nil || len(reports) != 1 || reports[0].ReportID != "coverage_1" {
		t.Fatalf("ListAPICoverageReports() = %#v, %v", reports, err)
	}
	artifacts, err := store.ListAPIArtifacts(ctx, 10)
	if err != nil || len(artifacts) != 1 || artifacts[0].ArtifactID != "art_openapi_1" {
		t.Fatalf("ListAPIArtifacts() = %#v, %v", artifacts, err)
	}
}
