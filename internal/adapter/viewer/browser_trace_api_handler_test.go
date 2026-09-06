package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	browsertraceapp "github.com/Nyukimin/RenCrow_CORE/internal/application/browsertrace"
	domaintrace "github.com/Nyukimin/RenCrow_CORE/internal/domain/browsertrace"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	domainworkstream "github.com/Nyukimin/RenCrow_CORE/internal/domain/workstream"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

type stubBrowserTraceRunVerifier struct {
	assignee string
	err      error
}

type stubBrowserTraceTaskRunStore struct {
	run     domaintask.Run
	taskErr error
	runErr  error
}

func (s stubBrowserTraceTaskRunStore) Get(_ context.Context, taskID modulecore.TaskID) (domaintask.Task, error) {
	return domaintask.Task{TaskID: taskID}, s.taskErr
}

func (s stubBrowserTraceTaskRunStore) GetRun(_ context.Context, _ modulecore.RunID) (domaintask.Run, error) {
	return s.run, s.runErr
}

func (s stubBrowserTraceRunVerifier) VerifyTaskRun(_ context.Context, _ modulecore.TaskID, _ modulecore.RunID) (string, error) {
	return s.assignee, s.err
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
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			TracePath: "traces/trace_1",
		}},
		candidates: []domaintrace.APICandidate{{
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
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
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			TracePath: "traces/trace_1",
			CreatedAt: now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
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
			ReportID: "coverage_1",
			TaskID:   "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			CreatedAt: now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, nil).ServeHTTP(rec, req)

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

func TestHandleBrowserTraceAPIDiscoverFailsClosedOnTaskRunOwnership(t *testing.T) {
	store := &stubBrowserTraceAPIStore{}
	body := `{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"trace_path":"traces/run",
		"requests_path":"traces/run/requests.jsonl",
		"responses_path":"traces/run/responses.jsonl"
	}`
	tests := []struct {
		name     string
		verifier BrowserTraceRunVerifier
		wantCode int
	}{
		{name: "verifier missing", verifier: nil, wantCode: http.StatusServiceUnavailable},
		{name: "task or run missing", verifier: stubBrowserTraceRunVerifier{err: fmt.Errorf("not found")}, wantCode: http.StatusForbidden},
		{name: "actor differs from assignee", verifier: stubBrowserTraceRunVerifier{assignee: "shiro"}, wantCode: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(body))
			rec := httptest.NewRecorder()
			HandleBrowserTraceAPIDiscover(store, stubBrowserTraceDiscoverer{}, tt.verifier, nil, nil).ServeHTTP(rec, req)
			if rec.Code != tt.wantCode {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(store.runs) != 0 {
				t.Fatalf("ownership failure persisted trace runs: %#v", store.runs)
			}
		})
	}
}

func TestBrowserTraceRunVerifierRequiresExistingMatchingTaskRun(t *testing.T) {
	taskID := modulecore.TaskID("tsk_00000000-0000-5000-8000-000000000001")
	runID := modulecore.RunID("run_00000000-0000-5000-8000-000000000002")
	tests := []struct {
		name  string
		store stubBrowserTraceTaskRunStore
		want  string
	}{
		{name: "task missing", store: stubBrowserTraceTaskRunStore{taskErr: fmt.Errorf("not found")}, want: "task verification failed"},
		{name: "run missing", store: stubBrowserTraceTaskRunStore{runErr: fmt.Errorf("not found")}, want: "run verification failed"},
		{name: "run belongs to other task", store: stubBrowserTraceTaskRunStore{run: domaintask.Run{TaskID: "tsk_00000000-0000-5000-8000-000000000003"}}, want: "does not belong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewBrowserTraceRunVerifier(tt.store).VerifyTaskRun(context.Background(), taskID, runID); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("VerifyTaskRun() error=%v want=%q", err, tt.want)
			}
		})
	}
	assignee, err := NewBrowserTraceRunVerifier(stubBrowserTraceTaskRunStore{run: domaintask.Run{TaskID: taskID, Assignee: " mio "}}).VerifyTaskRun(context.Background(), taskID, runID)
	if err != nil || assignee != "mio" {
		t.Fatalf("VerifyTaskRun() assignee=%q error=%v", assignee, err)
	}
}

func TestHandleBrowserTraceAPIDiscoverRejectsMalformedIdentity(t *testing.T) {
	body := `{
		"task_id":"trace_legacy",
		"run_id":"trace_legacy",
		"actor_id":"worker",
		"trace_path":"traces/run",
		"requests_path":"traces/run/requests.jsonl",
		"responses_path":"traces/run/responses.jsonl"
	}`
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()
	HandleBrowserTraceAPIDiscover(&stubBrowserTraceAPIStore{}, stubBrowserTraceDiscoverer{}, stubBrowserTraceRunVerifier{}, nil, nil).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBrowserTraceAPIDiscoverStagesAPICandidates(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	sink := &stubBrowserTraceCandidateSink{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			TracePath: "traces/trace_1",
			CreatedAt: now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID: "coverage_1",
			TaskID:   "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			CreatedAt: now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, sink, nil).ServeHTTP(rec, req)

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
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			WorkstreamID: "ws_1",
			TracePath:    "traces/trace_1",
			CreatedAt:    now,
		},
		Candidates: []domaintrace.APICandidate{{
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ReportID: "coverage_1",
			TaskID:   "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			CreatedAt: now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_1",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, workstreamSink).ServeHTTP(rec, req)

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
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
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
			TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Passed:    true,
			Status:    "validated",
			CreatedAt: now,
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
	body := []byte(`{"candidate_id":"api_cand_1","workstream_id":"ws_1"}`)
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
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			RiskLevel:            "low",
			Status:               "candidate",
			CreatedAt:            now,
		}},
	}
	body := []byte(`{"candidate_id":"api_cand_1","reviewer":"live-e2e","terms_reviewed":true,"official_api_reviewed":true,"pii_reviewed":true,"schema_reviewed":true,"risk_reviewed":true}`)
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

func TestHandleBrowserTraceAPIValidationReviewRecordsMissingEvidenceAsRejected(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{
		candidates: []domaintrace.APICandidate{{
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
	}
	body := []byte(`{"candidate_id":"api_cand_1","reviewer":"reviewer"}`)
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
	if validation.Passed || validation.Status != "rejected" || len(validation.Issues) == 0 || validation.Reviewer != "reviewer" {
		t.Fatalf("validation=%#v", validation)
	}
}

func TestHandleBrowserTraceAPIFetcherProposalRejectsUnvalidatedCandidate(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{
		candidates: []domaintrace.APICandidate{{
			CandidateID: "api_cand_1",
			TaskID:      "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Method:               "GET",
			ObservedURL:          "https://example.com/api/items",
			ContainsPersonalData: "unknown",
			Status:               "candidate",
			CreatedAt:            now,
		}},
		validations: []domaintrace.APICandidateValidationResult{{
			ValidationID: "api_val_1",
			CandidateID:  "api_cand_1",
			TaskID:       "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			Passed:    false,
			Status:    "needs_review",
			CreatedAt: now,
		}},
	}
	body := []byte(`{"candidate_id":"api_cand_1"}`)
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

func TestHandleBrowserTraceAPIFetcherProposalUsesPolicyChecks(t *testing.T) {
	store := &stubBrowserTraceAPIStore{}
	body := []byte(`{"candidate_id":"api_cand_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, nil).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
