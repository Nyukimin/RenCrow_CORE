package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	browsertraceapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/browsertrace"
	domaintrace "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/browsertrace"
	domainworkstream "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/workstream"
)

type stubBrowserTraceAPIStore struct {
	runs        []domaintrace.TraceRun
	candidates  []domaintrace.APICandidate
	schemas     []domaintrace.APICandidateSchema
	validations []domaintrace.APICandidateValidationResult
	coverage    []domaintrace.APICoverageReport
	artifacts   []domaintrace.APIArtifact
}

func (s *stubBrowserTraceAPIStore) ListTraceRuns(_ context.Context, _ int) ([]domaintrace.TraceRun, error) {
	return s.runs, nil
}
func (s *stubBrowserTraceAPIStore) ListAPICandidates(_ context.Context, _ int) ([]domaintrace.APICandidate, error) {
	return s.candidates, nil
}
func (s *stubBrowserTraceAPIStore) ListAPICandidateSchemas(_ context.Context, _ int) ([]domaintrace.APICandidateSchema, error) {
	return s.schemas, nil
}
func (s *stubBrowserTraceAPIStore) ListAPICandidateValidationResults(_ context.Context, _ int) ([]domaintrace.APICandidateValidationResult, error) {
	return s.validations, nil
}
func (s *stubBrowserTraceAPIStore) ListAPICoverageReports(_ context.Context, _ int) ([]domaintrace.APICoverageReport, error) {
	return s.coverage, nil
}
func (s *stubBrowserTraceAPIStore) ListAPIArtifacts(_ context.Context, _ int) ([]domaintrace.APIArtifact, error) {
	return s.artifacts, nil
}
func (s *stubBrowserTraceAPIStore) SaveTraceRun(_ context.Context, item domaintrace.TraceRun) error {
	if err := domaintrace.ValidateTraceRun(item); err != nil {
		return err
	}
	s.runs = append(s.runs, item)
	return nil
}
func (s *stubBrowserTraceAPIStore) SaveAPICandidate(_ context.Context, item domaintrace.APICandidate) error {
	if err := domaintrace.ValidateAPICandidate(item); err != nil {
		return err
	}
	s.candidates = append(s.candidates, item)
	return nil
}
func (s *stubBrowserTraceAPIStore) SaveAPICandidateSchema(_ context.Context, item domaintrace.APICandidateSchema) error {
	if err := domaintrace.ValidateAPICandidateSchema(item); err != nil {
		return err
	}
	s.schemas = append(s.schemas, item)
	return nil
}
func (s *stubBrowserTraceAPIStore) SaveAPICandidateValidationResult(_ context.Context, item domaintrace.APICandidateValidationResult) error {
	if err := domaintrace.ValidateAPICandidateValidationResult(item); err != nil {
		return err
	}
	s.validations = append(s.validations, item)
	return nil
}
func (s *stubBrowserTraceAPIStore) SaveAPICoverageReport(_ context.Context, item domaintrace.APICoverageReport) error {
	if err := domaintrace.ValidateAPICoverageReport(item); err != nil {
		return err
	}
	s.coverage = append(s.coverage, item)
	return nil
}
func (s *stubBrowserTraceAPIStore) SaveAPIArtifact(_ context.Context, item domaintrace.APIArtifact) error {
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, item)
	return nil
}

type stubBrowserTraceDiscoverer struct {
	result domaintrace.DiscoveryResult
}

func (s stubBrowserTraceDiscoverer) Discover(_ browsertraceapp.DiscoverRequest) (domaintrace.DiscoveryResult, error) {
	return s.result, nil
}

type stubBrowserTraceCandidateSink struct {
	results []domaintrace.DiscoveryResult
}

func (s *stubBrowserTraceCandidateSink) SaveBrowserTraceAPICandidates(_ context.Context, result domaintrace.DiscoveryResult) error {
	s.results = append(s.results, result)
	return nil
}

type stubBrowserTraceWorkstreamArtifactSink struct {
	artifacts []domainworkstream.Artifact
}

func (s *stubBrowserTraceWorkstreamArtifactSink) SaveArtifact(_ context.Context, item domainworkstream.Artifact) error {
	if err := domainworkstream.ValidateArtifact(item); err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, item)
	return nil
}

func TestHandleBrowserTraceAPIStatus(t *testing.T) {
	store := &stubBrowserTraceAPIStore{
		runs: []domaintrace.TraceRun{{
			TraceRunID: "trace_1",
			TracePath:  "traces/trace_1",
		}},
		candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
		}},
	}
	req := httptest.NewRequest(http.MethodGet, "/viewer/browser-trace-api?limit=5", nil)
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIStatus(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body struct {
		Runs       []domaintrace.TraceRun     `json:"trace_runs"`
		Candidates []domaintrace.APICandidate `json:"api_candidates"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.Runs) != 1 || len(body.Candidates) != 1 {
		t.Fatalf("unexpected body: %#v", body)
	}
}

func TestHandleBrowserTraceAPIDiscoverSavesResult(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TraceRunID: "trace_1",
			TracePath:  "traces/trace_1",
			CreatedAt:  now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Schemas: []domaintrace.APICandidateSchema{{
			SchemaID:    "schema_1",
			CandidateID: "api_cand_1",
			SchemaType:  "response",
			SchemaJSON:  `{"type":"object"}`,
			SampleCount: 1,
			CreatedAt:   now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID:   "coverage_1",
			TraceRunID: "trace_1",
			CreatedAt:  now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"trace_run_id":"trace_1",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, nil, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.runs) != 1 || len(store.candidates) != 1 || len(store.schemas) != 1 || len(store.validations) != 1 || len(store.coverage) != 1 || len(store.artifacts) != 6 {
		t.Fatalf("store=%#v", store)
	}
	if store.validations[0].Status != "needs_review" || store.validations[0].Passed {
		t.Fatalf("validation=%#v", store.validations[0])
	}
	if store.artifacts[0].Type != "observed_openapi" || store.artifacts[1].Type != "coverage_report" || store.artifacts[2].Type != "endpoint_inventory" || store.artifacts[3].Type != "risk_assessment" || store.artifacts[4].Type != "fetcher_plan" || store.artifacts[5].Type != "client_draft" {
		t.Fatalf("artifacts=%#v", store.artifacts)
	}
}

func TestHandleBrowserTraceAPIDiscoverStagesAPICandidates(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	sink := &stubBrowserTraceCandidateSink{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TraceRunID: "trace_1",
			TracePath:  "traces/trace_1",
			CreatedAt:  now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID:   "coverage_1",
			TraceRunID: "trace_1",
			CreatedAt:  now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"trace_run_id":"trace_1",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, sink, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(sink.results) != 1 || len(sink.results[0].Candidates) != 1 {
		t.Fatalf("candidate sink results=%#v", sink.results)
	}
	if sink.results[0].Candidates[0].CandidateID != "api_cand_1" {
		t.Fatalf("unexpected staged candidate: %#v", sink.results[0].Candidates[0])
	}
}

func TestHandleBrowserTraceAPIDiscoverRegistersWorkstreamArtifacts(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	workstreamSink := &stubBrowserTraceWorkstreamArtifactSink{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TraceRunID:   "trace_1",
			WorkstreamID: "ws_1",
			TracePath:    "traces/trace_1",
			CreatedAt:    now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID:   "coverage_1",
			TraceRunID: "trace_1",
			CreatedAt:  now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"trace_run_id":"trace_1",
		"workstream_id":"ws_1",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, nil, workstreamSink).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstreamSink.artifacts) != 6 {
		t.Fatalf("workstream artifacts=%#v", workstreamSink.artifacts)
	}
	if workstreamSink.artifacts[0].WorkstreamID != "ws_1" || workstreamSink.artifacts[0].Status != "pending_review" {
		t.Fatalf("unexpected workstream artifact=%#v", workstreamSink.artifacts[0])
	}
}

func TestHandleBrowserTraceAPIFetcherProposalCreatesReviewArtifactForValidatedCandidate(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{
		candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			PathTemplate:         "/api/items",
			ContainsPersonalData: "none",
			RiskLevel:            "low",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		validations: []domaintrace.APICandidateValidationResult{{
			ValidationID: "api_val_1",
			CandidateID:  "api_cand_1",
			TraceRunID:   "trace_1",
			Passed:       true,
			Status:       "validated",
			CreatedAt:    now,
		}},
		schemas: []domaintrace.APICandidateSchema{{
			SchemaID:    "schema_1",
			CandidateID: "api_cand_1",
			SchemaType:  "response",
			SchemaJSON:  `{"type":"object"}`,
			SampleCount: 2,
			Confidence:  0.8,
			CreatedAt:   now,
		}},
	}
	workstreamSink := &stubBrowserTraceWorkstreamArtifactSink{}
	body := []byte(`{"candidate_id":"api_cand_1","workstream_id":"ws_1","human_approved":true}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, workstreamSink).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 1 {
		t.Fatalf("api artifacts=%#v", store.artifacts)
	}
	artifact := store.artifacts[0]
	if artifact.Type != "fetcher_proposal" || artifact.Status != "pending_review" || !bytes.Contains([]byte(artifact.Content), []byte("no direct promoted DB write")) {
		t.Fatalf("artifact=%#v", artifact)
	}
	if len(workstreamSink.artifacts) != 1 || workstreamSink.artifacts[0].Type != "browser_trace_fetcher_proposal" {
		t.Fatalf("workstream artifacts=%#v", workstreamSink.artifacts)
	}
	var response struct {
		OfficialPromotion   bool `json:"official_promotion"`
		ImplementationApply bool `json:"implementation_apply"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OfficialPromotion || response.ImplementationApply {
		t.Fatalf("response must remain proposal-only: %#v", response)
	}
}

func TestHandleBrowserTraceAPIValidationReviewMarksCandidateValidated(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{
		candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			RiskLevel:            "low",
			Status:               "candidate",
			CreatedAt:            now,
		}},
	}
	body := []byte(`{"candidate_id":"api_cand_1","reviewer":"live-e2e","human_approved":true,"terms_reviewed":true,"official_api_reviewed":true,"pii_reviewed":true,"schema_reviewed":true,"risk_reviewed":true}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/validations", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIValidationReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.validations) != 1 {
		t.Fatalf("validations=%#v", store.validations)
	}
	validation := store.validations[0]
	if !validation.Passed || validation.Status != "validated" || validation.CandidateID != "api_cand_1" {
		t.Fatalf("validation=%#v", validation)
	}
	var response struct {
		OfficialPromotion   bool `json:"official_promotion"`
		ImplementationApply bool `json:"implementation_apply"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OfficialPromotion || response.ImplementationApply {
		t.Fatalf("response must remain review-only: %#v", response)
	}
}

func TestHandleBrowserTraceAPIValidationReviewRecordsMissingEvidenceAsNeedsReview(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{
		candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
	}
	body := []byte(`{"candidate_id":"api_cand_1","reviewer":"reviewer","human_approved":true}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/validations", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIValidationReview(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.validations) != 1 {
		t.Fatalf("validations=%#v", store.validations)
	}
	validation := store.validations[0]
	if validation.Passed || validation.Status != "needs_review" || len(validation.Issues) == 0 {
		t.Fatalf("validation=%#v", validation)
	}
}

func TestHandleBrowserTraceAPIFetcherProposalRejectsUnvalidatedCandidate(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{
		candidates: []domaintrace.APICandidate{{
			CandidateID:          "api_cand_1",
			TraceRunID:           "trace_1",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		validations: []domaintrace.APICandidateValidationResult{{
			ValidationID: "api_val_1",
			CandidateID:  "api_cand_1",
			TraceRunID:   "trace_1",
			Passed:       false,
			Status:       "needs_review",
			CreatedAt:    now,
		}},
	}
	body := []byte(`{"candidate_id":"api_cand_1","human_approved":true}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 0 {
		t.Fatalf("unexpected artifacts=%#v", store.artifacts)
	}
}

func TestHandleBrowserTraceAPIFetcherProposalRequiresHumanApproval(t *testing.T) {
	store := &stubBrowserTraceAPIStore{}
	body := []byte(`{"candidate_id":"api_cand_1","human_approved":false}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
