package viewer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
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
	// intents holds the creation facts stored with each artifact, keyed by the artifact
	// id the fact binds, so a test can see exactly what the recovery pass would retry.
	intents   map[modulecore.ArtifactID]modulecore.EventEnvelope
	createErr error
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

// CreateAPIArtifactWithPublicationIntent keeps the stub honest about the contract the
// route now depends on: the artifact and its creation fact are stored together, and the
// fact has to be the creation fact of exactly that artifact.
func (s *stubBrowserTraceAPIStore) CreateAPIArtifactWithPublicationIntent(_ context.Context, item domaintrace.APIArtifact, intent modulecore.EventEnvelope) error {
	if s.createErr != nil {
		return s.createErr
	}
	if err := domaintrace.ValidateAPIArtifact(item); err != nil {
		return err
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(item, intent); err != nil {
		return err
	}
	s.artifacts = append(s.artifacts, item)
	if s.intents == nil {
		s.intents = make(map[modulecore.ArtifactID]modulecore.EventEnvelope)
	}
	s.intents[item.ArtifactID] = intent
	return nil
}

// ListAPIArtifactPublicationIntents walks the stored facts by artifact id keyset, the same
// exclusive cursor the artifact stores use, so a recovery pass cannot loop on one page.
func (s *stubBrowserTraceAPIStore) ListAPIArtifactPublicationIntents(_ context.Context, after modulecore.ArtifactID, _ int) ([]modulecore.EventEnvelope, error) {
	ids := make([]modulecore.ArtifactID, 0, len(s.intents))
	for id := range s.intents {
		if id > after {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	const pageLimit = 1000
	if len(ids) > pageLimit {
		ids = ids[:pageLimit]
	}
	page := make([]modulecore.EventEnvelope, 0, len(ids))
	for _, id := range ids {
		page = append(page, s.intents[id])
	}
	return page, nil
}

// stubBrowserTraceCanonicalEvents is the one canonical event store the route delivers
// creation facts to. It assigns sequences like the canonical owner and can refuse an
// append, so a test observes what happens when a fact cannot be delivered.
type stubBrowserTraceCanonicalEvents struct {
	stored      map[modulecore.EventID]modulecore.EventEnvelope
	appendErr   error
	appendCalls int
}

func (s *stubBrowserTraceCanonicalEvents) GetByID(_ context.Context, eventID modulecore.EventID) (modulecore.EventEnvelope, bool, error) {
	event, found := s.stored[eventID]
	return event, found, nil
}

func (s *stubBrowserTraceCanonicalEvents) AppendSequenced(_ context.Context, event modulecore.EventEnvelope) (modulecore.EventEnvelope, error) {
	s.appendCalls++
	if s.appendErr != nil {
		return modulecore.EventEnvelope{}, s.appendErr
	}
	if s.stored == nil {
		s.stored = make(map[modulecore.EventID]modulecore.EventEnvelope)
	}
	if _, exists := s.stored[event.EventID]; exists {
		return modulecore.EventEnvelope{}, fmt.Errorf("duplicate event_id %q", event.EventID)
	}
	event.EventSeq = modulecore.EventSeq(len(s.stored) + 1)
	s.stored[event.EventID] = event
	return event, nil
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

// browserTraceTestWorkstreamID is a canonical workstream id. The creation fact of a
// browsertrace artifact names the canonical workstream its artifact belongs to, so every
// discover fixture that expects success carries one.
const browserTraceTestWorkstreamID = "ws_00000000-0000-7000-8000-00000000000a"

func TestHandleBrowserTraceAPIDiscoverSavesResult(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	events := &stubBrowserTraceCanonicalEvents{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			WorkstreamID: browserTraceTestWorkstreamID,
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
		Schemas: []domaintrace.APICandidateSchema{{
			SchemaID:    "schema_1",
			CandidateID: "api_cand_1",
			SchemaType:  "response",
			SchemaJSON:  `{"type":"object"}`,
			SampleCount: 1,
			CreatedAt:   now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
			Kind:       modulecore.ArtifactKindReport,
			TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			ContentHash: "sha256:9d7f27d546c0c617052f474f4e0202c1409f8c0d9aea341714ef34219d956cd1",
			CreatedAt:   now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_00000000-0000-7000-8000-00000000000a",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, nil, events).ServeHTTP(rec, req)

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
	// A created artifact is only a completed discover once the fact that it was created is
	// durable in the artifact store next to the row and sequenced by the canonical event
	// store. An artifact whose creation fact was written nowhere else is a gap, not a success.
	if len(store.artifacts) == 0 || len(store.intents) != len(store.artifacts) {
		t.Fatalf("intents=%d artifacts=%d: every created artifact needs a stored creation fact", len(store.intents), len(store.artifacts))
	}
	for _, artifact := range store.artifacts {
		intent, ok := store.intents[artifact.ArtifactID]
		if !ok {
			t.Fatalf("artifact %s (%s) has no stored creation fact", artifact.ArtifactID, artifact.Type)
		}
		if err := domaintrace.ValidateAPIArtifactPublicationIntent(artifact, intent); err != nil {
			t.Fatalf("stored creation fact of artifact %s: %v", artifact.ArtifactID, err)
		}
		if intent.EventSeq != 0 {
			t.Fatalf("durable creation fact %s carries event_seq %d: only the canonical store sequences it", intent.EventID, intent.EventSeq)
		}
		confirmed, ok := events.stored[intent.EventID]
		if !ok {
			t.Fatalf("creation fact %s of artifact %s never reached the canonical event store", intent.EventID, artifact.ArtifactID)
		}
		if confirmed.EventSeq == 0 {
			t.Fatalf("canonical event store did not assign a sequence to %s", intent.EventID)
		}
	}
	if events.appendCalls != len(store.artifacts) || len(events.stored) != len(store.artifacts) {
		t.Fatalf("canonical store appends=%d stored=%d, want %d", events.appendCalls, len(events.stored), len(store.artifacts))
	}
}

func TestHandleBrowserTraceAPIDiscoverFailsClosedOnTaskRunOwnership(t *testing.T) {
	store := &stubBrowserTraceAPIStore{}
	events := &stubBrowserTraceCanonicalEvents{}
	body := `{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_00000000-0000-7000-8000-00000000000a",
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
			HandleBrowserTraceAPIDiscover(store, stubBrowserTraceDiscoverer{}, tt.verifier, nil, nil, events).ServeHTTP(rec, req)
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
	HandleBrowserTraceAPIDiscover(&stubBrowserTraceAPIStore{}, stubBrowserTraceDiscoverer{}, stubBrowserTraceRunVerifier{}, nil, nil, &stubBrowserTraceCanonicalEvents{}).ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleBrowserTraceAPIDiscoverStagesAPICandidates(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	events := &stubBrowserTraceCanonicalEvents{}
	sink := &stubBrowserTraceCandidateSink{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			WorkstreamID: browserTraceTestWorkstreamID,
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
			ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
			Kind:       modulecore.ArtifactKindReport,
			TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			ContentHash: "sha256:9d7f27d546c0c617052f474f4e0202c1409f8c0d9aea341714ef34219d956cd1",
			CreatedAt:   now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_00000000-0000-7000-8000-00000000000a",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, sink, nil, events).ServeHTTP(rec, req)

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
	events := &stubBrowserTraceCanonicalEvents{}
	workstreamSink := &stubBrowserTraceWorkstreamArtifactSink{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			WorkstreamID: browserTraceTestWorkstreamID,
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
			ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
			Kind:       modulecore.ArtifactKindReport,
			TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			ContentHash: "sha256:9d7f27d546c0c617052f474f4e0202c1409f8c0d9aea341714ef34219d956cd1",
			CreatedAt:   now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_00000000-0000-7000-8000-00000000000a",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, workstreamSink, events).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(workstreamSink.artifacts) != 6 {
		t.Fatalf("workstream artifacts=%#v", workstreamSink.artifacts)
	}
	if workstreamSink.artifacts[0].WorkstreamID != browserTraceTestWorkstreamID || workstreamSink.artifacts[0].Status != "pending_review" {
		t.Fatalf("unexpected workstream artifact=%#v", workstreamSink.artifacts[0])
	}
	// The six generated artifacts must reach the store and the workstream boundary
	// with the same canonical identity and the kind the browsertrace domain declares
	// for their content role; the boundary may not mint or rewrite them.
	if len(store.artifacts) != 6 {
		t.Fatalf("api artifacts=%#v", store.artifacts)
	}
	wantKinds := []modulecore.ArtifactKind{
		modulecore.ArtifactKindSpecification, // observed_openapi
		modulecore.ArtifactKindReport,        // coverage_report
		modulecore.ArtifactKindDocument,      // endpoint_inventory
		modulecore.ArtifactKindReport,        // risk_assessment
		modulecore.ArtifactKindDraft,         // fetcher_plan
		modulecore.ArtifactKindDraft,         // client_draft
	}
	for i, saved := range store.artifacts {
		if err := saved.ArtifactID.Validate(); err != nil {
			t.Errorf("discover saved artifact %d (%s) artifact_id %q: %v", i, saved.Type, saved.ArtifactID, err)
		}
		if saved.Kind != wantKinds[i] {
			t.Errorf("discover saved artifact %d (%s) artifact_kind = %q, want %q", i, saved.Type, saved.Kind, wantKinds[i])
		}
		if workstreamSink.artifacts[i].ArtifactID != string(saved.ArtifactID) {
			t.Errorf("discover workstream artifact %d artifact_id = %q, want saved api artifact id %q", i, workstreamSink.artifacts[i].ArtifactID, saved.ArtifactID)
		}
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
	events := &stubBrowserTraceCanonicalEvents{}
	body := []byte(`{"candidate_id":"api_cand_1","workstream_id":"` + browserTraceTestWorkstreamID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, stubBrowserTraceRunVerifier{assignee: "mio"}, workstreamSink, events).ServeHTTP(rec, req)

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
	if err := artifact.ArtifactID.Validate(); err != nil {
		t.Errorf("saved fetcher proposal artifact_id %q: %v", artifact.ArtifactID, err)
	}
	if artifact.Kind != modulecore.ArtifactKindDraft {
		t.Errorf("saved fetcher proposal artifact_kind = %q, want %q", artifact.Kind, modulecore.ArtifactKindDraft)
	}
	if workstreamSink.artifacts[0].ArtifactID != string(artifact.ArtifactID) {
		t.Errorf("workstream artifact_id = %q, want the api artifact id %q", workstreamSink.artifacts[0].ArtifactID, artifact.ArtifactID)
	}
	// The stored proposal must carry the content digest of its own body, and a fresh
	// proposal is not a supersession, so it starts without a superseded_by reference.
	sum := sha256.Sum256([]byte(artifact.Content))
	if wantHash := modulecore.ContentHashPrefix + hex.EncodeToString(sum[:]); artifact.ContentHash != wantHash {
		t.Errorf("saved fetcher proposal content_hash = %q, want the digest of its content %q", artifact.ContentHash, wantHash)
	}
	if artifact.SupersededBy != "" {
		t.Errorf("saved fetcher proposal superseded_by = %q, want empty for a new proposal", artifact.SupersededBy)
	}
	var response struct {
		OfficialPromotion   bool `json:"official_promotion"`
		ImplementationApply bool `json:"implementation_apply"`
		APIArtifact         struct {
			ArtifactID   string                  `json:"artifact_id"`
			Kind         modulecore.ArtifactKind `json:"artifact_kind"`
			ContentHash  string                  `json:"content_hash"`
			SupersededBy string                  `json:"superseded_by"`
		} `json:"api_artifact"`
		WorkstreamArtifact struct {
			ArtifactID string `json:"artifact_id"`
		} `json:"workstream_artifact"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.OfficialPromotion || response.ImplementationApply {
		t.Fatalf("response must remain proposal-only: %#v", response)
	}
	// The response body must expose the same canonical identity that was stored, so
	// a caller can follow the artifact across the api and workstream projections.
	if response.APIArtifact.ArtifactID != string(artifact.ArtifactID) {
		t.Errorf("response api_artifact.artifact_id = %q, want %q", response.APIArtifact.ArtifactID, artifact.ArtifactID)
	}
	if response.APIArtifact.Kind != modulecore.ArtifactKindDraft {
		t.Errorf("response api_artifact.artifact_kind = %q, want %q", response.APIArtifact.Kind, modulecore.ArtifactKindDraft)
	}
	if response.WorkstreamArtifact.ArtifactID != string(artifact.ArtifactID) {
		t.Errorf("response workstream_artifact.artifact_id = %q, want %q", response.WorkstreamArtifact.ArtifactID, artifact.ArtifactID)
	}
	if response.APIArtifact.ContentHash != artifact.ContentHash {
		t.Errorf("response api_artifact.content_hash = %q, want the stored digest %q", response.APIArtifact.ContentHash, artifact.ContentHash)
	}
	if response.APIArtifact.SupersededBy != "" {
		t.Errorf("response api_artifact.superseded_by = %q, want empty for a new proposal", response.APIArtifact.SupersededBy)
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

	HandleBrowserTraceAPIFetcherProposal(store, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, &stubBrowserTraceCanonicalEvents{}).ServeHTTP(rec, req)

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

	HandleBrowserTraceAPIFetcherProposal(store, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, &stubBrowserTraceCanonicalEvents{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

// TestHandleBrowserTraceAPIDiscoverRejectsNonCanonicalWorkstreamBeforeWrites covers the
// guard added with creation facts: a workstream id that is not canonical cannot label a
// canonical event, so the route refuses the request before any trace run, candidate or
// artifact is written. A silently unlabeled creation fact would be undeliverable forever.
func TestHandleBrowserTraceAPIDiscoverRejectsNonCanonicalWorkstreamBeforeWrites(t *testing.T) {
	store := &stubBrowserTraceAPIStore{}
	events := &stubBrowserTraceCanonicalEvents{}
	body := `{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_1",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, stubBrowserTraceDiscoverer{}, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, nil, events).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "workstream_id is invalid") {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.runs) != 0 || len(store.artifacts) != 0 || len(store.intents) != 0 || events.appendCalls != 0 {
		t.Fatalf("a refused workstream still persisted work: runs=%d artifacts=%d intents=%d appends=%d",
			len(store.runs), len(store.artifacts), len(store.intents), events.appendCalls)
	}
}

// TestHandleBrowserTraceAPIDiscoverKeepsUndeliveredCreationFactsDurableAndRecoverable holds
// the route accountable for the two-step write: when the canonical store refuses the
// creation facts the route fails the request, the facts stay durable in the artifact store,
// and the workstream registration that must follow a confirmed fact has not happened yet.
// The same durable facts are then what a publication pass without a resent request drains,
// first as new appends and then as already-confirmed facts. This is route and application
// source coverage against stubbed stores, not a restarted-process observation.
func TestHandleBrowserTraceAPIDiscoverKeepsUndeliveredCreationFactsDurableAndRecoverable(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	store := &stubBrowserTraceAPIStore{}
	events := &stubBrowserTraceCanonicalEvents{appendErr: fmt.Errorf("canonical event store unavailable")}
	workstreamSink := &stubBrowserTraceWorkstreamArtifactSink{}
	discoverer := stubBrowserTraceDiscoverer{result: domaintrace.DiscoveryResult{
		Run: domaintrace.TraceRun{
			TaskID: "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			WorkstreamID: browserTraceTestWorkstreamID,
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
		Schemas: []domaintrace.APICandidateSchema{{
			SchemaID:    "schema_1",
			CandidateID: "api_cand_1",
			SchemaType:  "response",
			SchemaJSON:  `{"type":"object"}`,
			SampleCount: 1,
			CreatedAt:   now,
		}},
		Coverage: domaintrace.APICoverageReport{
			ArtifactID: modulecore.ArtifactID("art_00000000-0000-5000-8000-000000000003"),
			Kind:       modulecore.ArtifactKindReport,
			TaskID:     "tsk_00000000-0000-5000-8000-000000000001", RunID: "run_00000000-0000-5000-8000-000000000002", ActorID: "mio",
			ContentHash: "sha256:9d7f27d546c0c617052f474f4e0202c1409f8c0d9aea341714ef34219d956cd1",
			CreatedAt:   now,
		},
	}}
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/discover", bytes.NewBufferString(`{
		"task_id":"tsk_00000000-0000-5000-8000-000000000001",
		"run_id":"run_00000000-0000-5000-8000-000000000002",
		"actor_id":"mio",
		"workstream_id":"ws_00000000-0000-7000-8000-00000000000a",
		"trace_path":"traces/trace_1",
		"requests_path":"traces/trace_1/requests.jsonl",
		"responses_path":"traces/trace_1/responses.jsonl"
	}`))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIDiscover(store, discoverer, stubBrowserTraceRunVerifier{assignee: "mio"}, nil, workstreamSink, events).ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d body=%s: an undeliverable creation fact must fail the request", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) == 0 || len(store.intents) != len(store.artifacts) {
		t.Fatalf("undelivered facts lost: artifacts=%d intents=%d", len(store.artifacts), len(store.intents))
	}
	if len(workstreamSink.artifacts) != 0 {
		t.Fatalf("workstream artifact registered before its creation fact was confirmed: %#v", workstreamSink.artifacts[0])
	}

	// The retry pass a start performs needs no resent request: it drains the facts the
	// route left behind and delivers each one exactly once.
	delivered := &stubBrowserTraceCanonicalEvents{}
	report, err := browsertraceapp.PublishPendingAPIArtifactCreationFacts(context.Background(), store, delivered)
	if err != nil {
		t.Fatalf("recover undelivered creation facts: %v", err)
	}
	if report.Scanned != len(store.intents) || report.Published != len(store.intents) || report.Failed != 0 {
		t.Fatalf("recovery report=%#v, want %d published of %d intents", report, len(store.intents), len(store.intents))
	}
	if len(delivered.stored) != len(store.intents) {
		t.Fatalf("canonical store holds %d events, want %d", len(delivered.stored), len(store.intents))
	}
	again, err := browsertraceapp.PublishPendingAPIArtifactCreationFacts(context.Background(), store, delivered)
	if err != nil {
		t.Fatalf("second recovery pass: %v", err)
	}
	if again.Published != 0 || again.Confirmed != len(store.intents) || again.Failed != 0 {
		t.Fatalf("second pass re-appended confirmed facts: %#v", again)
	}
}

// TestHandleBrowserTraceAPIFetcherProposalStoresAndPublishesCreationFact holds the fetcher
// proposal route to the contract the discover route already meets: a newly created artifact
// is stored together with the canonical creation fact of exactly that artifact, that fact is
// delivered to the one canonical event store before the response, and the workstream
// registration follows the confirmed delivery. An artifact written through the ordinary Save
// path leaves a row whose creation fact was never written anywhere, which no later read can
// recover. This is route source coverage against stubbed stores, not a live route or
// restarted-process observation.
func TestHandleBrowserTraceAPIFetcherProposalStoresAndPublishesCreationFact(t *testing.T) {
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
	events := &stubBrowserTraceCanonicalEvents{}
	workstreamSink := &stubBrowserTraceWorkstreamArtifactSink{}
	body := []byte(`{"candidate_id":"api_cand_1","workstream_id":"` + browserTraceTestWorkstreamID + `"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, stubBrowserTraceRunVerifier{assignee: "mio"}, workstreamSink, events).ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 1 {
		t.Fatalf("api artifacts=%d, want the one fetcher proposal artifact", len(store.artifacts))
	}
	artifact := store.artifacts[0]
	intent, stored := store.intents[artifact.ArtifactID]
	if !stored {
		t.Fatalf("fetcher proposal artifact %s was written with no durable creation fact: intents=%d canonical_appends=%d",
			artifact.ArtifactID, len(store.intents), events.appendCalls)
	}
	if err := domaintrace.ValidateAPIArtifactPublicationIntent(artifact, intent); err != nil {
		t.Fatalf("stored creation fact is not the creation fact of this artifact: %v", err)
	}
	if intent.EventSeq != 0 {
		t.Fatalf("persisted intent carries event_seq %d: the canonical event store assigns sequences", intent.EventSeq)
	}
	if events.appendCalls != 1 {
		t.Fatalf("canonical appends=%d, want the one creation fact of the new artifact", events.appendCalls)
	}
	published, found := events.stored[intent.EventID]
	if !found {
		t.Fatalf("creation fact %s of artifact %s is not in the canonical event store", intent.EventID, artifact.ArtifactID)
	}
	if published.EventSeq == 0 {
		t.Fatalf("published creation fact %s carries no sequence from the canonical owner", published.EventID)
	}
	if len(workstreamSink.artifacts) != 1 || workstreamSink.artifacts[0].ArtifactID != string(artifact.ArtifactID) {
		t.Fatalf("workstream artifacts=%#v, want the registered proposal artifact %s", workstreamSink.artifacts, artifact.ArtifactID)
	}
}

// TestHandleBrowserTraceAPIFetcherProposalRejectsNonCanonicalWorkstreamBeforeWrites covers
// the same guard the discover route has: the creation fact names the canonical workstream
// its artifact belongs to, so a workstream id that is not canonical is refused before the
// proposal row, its creation fact or the workstream registration are written.
func TestHandleBrowserTraceAPIFetcherProposalRejectsNonCanonicalWorkstreamBeforeWrites(t *testing.T) {
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
			Passed:    true,
			Status:    "validated",
			CreatedAt: now,
		}},
	}
	events := &stubBrowserTraceCanonicalEvents{}
	workstreamSink := &stubBrowserTraceWorkstreamArtifactSink{}
	body := []byte(`{"candidate_id":"api_cand_1","workstream_id":"ws_1"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/browser-trace-api/fetcher-proposals", bytes.NewReader(body))
	rec := httptest.NewRecorder()

	HandleBrowserTraceAPIFetcherProposal(store, stubBrowserTraceRunVerifier{assignee: "mio"}, workstreamSink, events).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if len(store.artifacts) != 0 || len(store.intents) != 0 {
		t.Fatalf("artifacts=%d intents=%d, want nothing written for a non-canonical workstream", len(store.artifacts), len(store.intents))
	}
	if events.appendCalls != 0 || len(workstreamSink.artifacts) != 0 {
		t.Fatalf("canonical appends=%d workstream artifacts=%d, want no publication or registration", events.appendCalls, len(workstreamSink.artifacts))
	}
}
