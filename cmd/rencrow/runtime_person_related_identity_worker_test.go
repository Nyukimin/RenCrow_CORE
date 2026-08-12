package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalog "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	_ "modernc.org/sqlite"
)

type fakeRuntimeIdentityResolver struct {
	result personrelatedcatalog.IdentityResolveResult
	err    error
	calls  []personrelatedcatalog.IdentityResolveRequest
}

func (f *fakeRuntimeIdentityResolver) ResolveIdentity(_ context.Context, request personrelatedcatalog.IdentityResolveRequest) (personrelatedcatalog.IdentityResolveResult, error) {
	f.calls = append(f.calls, request)
	return f.result, f.err
}

func TestRuntimePersonRelatedIdentityWorkerResolvesAndPersistsConfirmedEvidence(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	resolver := &fakeRuntimeIdentityResolver{result: personrelatedcatalog.IdentityResolveResult{
		Status: personrelatedcatalog.IdentityStatusConfirmed, ReasonCode: "exact_authority_cross_reference", RetrievedAt: now.Format(time.RFC3339),
		Candidates: []personrelatedcatalog.IdentityEvidence{{Authority: "wikidata_qid", ExternalID: "Q42", CanonicalURL: "https://www.wikidata.org/wiki/Q42", State: personrelatedcatalog.IdentityStatusConfirmed, EvidenceSource: "fixture", EvidenceURL: "https://www.wikidata.org/wiki/Q42", RetrievedAt: now.Format(time.RFC3339), MatchedFields: []string{"birth_date"}}},
	}}
	worker, err := prepareRuntimePersonRelatedIdentityWorker(moviePath, hobbyPath, resolver, config.PersonRelatedCatalogIdentityMappingConfig{Interval: "7m", BatchSize: 10, Lease: "3m", MaxAttempts: 2, BatchCategories: 7}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if worker.Interval() != 7*time.Minute || worker.batchSize != 10 || worker.lease != 3*time.Minute || worker.maxAttempts != 2 {
		t.Fatalf("identity worker config was not applied: interval=%s batch=%d lease=%s attempts=%d", worker.Interval(), worker.batchSize, worker.lease, worker.maxAttempts)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.MigrationQueued != 1 || result.Claimed != 1 || result.Confirmed != 1 || len(resolver.calls) != 1 {
		t.Fatalf("result=%#v calls=%#v", result, resolver.calls)
	}
	db, err := sql.Open("sqlite", filepath.Clean(hobbyPath))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state, externalID string
	if err := db.QueryRow(`SELECT state FROM hobby_person_identity_jobs WHERE movie_catalog_person_id='p1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT state,external_id FROM hobby_person_external_ids WHERE person_id='p1' AND authority='wikidata_qid'`).Scan(&state, &externalID); err != nil {
		t.Fatal(err)
	}
	if state != string(personrelatedcatalog.IdentityStatusConfirmed) || externalID != "Q42" {
		t.Fatalf("mapping state=%q externalID=%q", state, externalID)
	}
}

func TestRuntimePersonRelatedIdentityWorkerKeepsAmbiguousPersonOutOfConfirmedMapping(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	resolver := &fakeRuntimeIdentityResolver{result: personrelatedcatalog.IdentityResolveResult{Status: personrelatedcatalog.IdentityStatusAmbiguous, ReasonCode: "same_name", RetrievedAt: now.Format(time.RFC3339)}}
	worker, err := prepareRuntimePersonRelatedIdentityWorker(moviePath, hobbyPath, resolver, config.PersonRelatedCatalogIdentityMappingConfig{BatchCategories: 7}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Ambiguous != 1 || len(resolver.calls) != 1 {
		t.Fatalf("result=%#v calls=%#v", result, resolver.calls)
	}
	db, err := sql.Open("sqlite", hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var state string
	if err := db.QueryRow(`SELECT state FROM hobby_person_identity_jobs WHERE movie_catalog_person_id='p1'`).Scan(&state); err != nil {
		t.Fatal(err)
	}
	if state != string(personrelatedcatalog.IdentityJobAmbiguous) {
		t.Fatalf("job state=%q", state)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_person_external_ids WHERE person_id='p1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("ambiguous mapping count=%d", count)
	}
}

func TestPrepareRuntimePersonRelatedIdentityWorkerRejectsUnsafeConfig(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	resolver := &fakeRuntimeIdentityResolver{}
	cases := []config.PersonRelatedCatalogIdentityMappingConfig{
		{Interval: "0s", BatchSize: 20, Lease: "2m", MaxAttempts: 3, BatchCategories: 7},
		{Interval: "5m", BatchSize: 21, Lease: "2m", MaxAttempts: 3, BatchCategories: 7},
		{Interval: "5m", BatchSize: 20, Lease: "29s", MaxAttempts: 3, BatchCategories: 7},
		{Interval: "5m", BatchSize: 20, Lease: "2m", MaxAttempts: 4, BatchCategories: 7},
	}
	for index, cfg := range cases {
		if _, err := prepareRuntimePersonRelatedIdentityWorker(moviePath, hobbyPath, resolver, cfg, time.Now); err == nil {
			t.Fatalf("unsafe config case %d unexpectedly accepted", index)
		}
	}
}

func TestRuntimePersonRelatedIdentityWorkerFixedEndpointE2E(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	now := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/person-related-catalog/identities/resolve" || r.Method != http.MethodPost {
			t.Fatalf("unexpected resolver request: %s %s", r.Method, r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode resolver request: %v", err)
		}
		if request["movie_catalog_person_id"] != "p1" || request["name"] != "Al Pacino" {
			t.Fatalf("resolver request=%#v", request)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "confirmed", "reason_code": "exact_authority_cross_reference", "retrieved_at": now.Format(time.RFC3339),
			"candidates": []map[string]any{{"authority": "wikidata_qid", "external_id": "Q42", "canonical_url": "https://www.wikidata.org/wiki/Q42", "state": "confirmed", "evidence_source": "wikidata", "evidence_url": "https://www.wikidata.org/wiki/Q42", "retrieved_at": now.Format(time.RFC3339), "matched_fields": []string{"birth_date"}}},
		})
	}))
	defer server.Close()
	resolver := personrelatedcatalog.NewHTTPIdentityResolver(server.URL, time.Second)
	worker, err := prepareRuntimePersonRelatedIdentityWorker(moviePath, hobbyPath, resolver, config.PersonRelatedCatalogIdentityMappingConfig{BatchCategories: 7}, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || result.Confirmed != 1 {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	db, err := sql.Open("sqlite", hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_person_identity_evidence WHERE person_id='p1' AND authority='wikidata_qid' AND candidate_id='Q42'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("evidence count=%d", count)
	}
}

func TestStartRuntimePersonRelatedIdentityWorkerRunsImmediatelyAndStops(t *testing.T) {
	runner := &recordingIdentityRunner{calls: make(chan struct{}, 4), step: 10 * time.Millisecond}
	cancel := startRuntimePersonRelatedIdentityWorker(runner, backgroundJobFailureReporter{})
	select {
	case <-runner.calls:
	case <-time.After(time.Second):
		t.Fatal("identity worker did not run immediately")
	}
	cancel()
	before := len(runner.calls)
	time.Sleep(30 * time.Millisecond)
	if got := len(runner.calls); got != before {
		t.Fatalf("identity worker continued after cancel: before=%d after=%d", before, got)
	}
}

type recordingIdentityRunner struct {
	calls chan struct{}
	step  time.Duration
}

func (r *recordingIdentityRunner) RunOnce(context.Context) (runtimePersonRelatedIdentityRunResult, error) {
	r.calls <- struct{}{}
	return runtimePersonRelatedIdentityRunResult{}, nil
}

func (r *recordingIdentityRunner) Interval() time.Duration { return r.step }
