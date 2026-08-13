package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	_ "modernc.org/sqlite"
)

func seedRuntimeEligibleMovieCatalog(t *testing.T) string {
	t.Helper()
	path := seedRuntimeMovieCatalog(t)
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
CREATE TABLE movie_catalog_assessments(
  kind TEXT NOT NULL,
  target_id TEXT NOT NULL,
  target_label TEXT NOT NULL,
  familiarity TEXT NOT NULL DEFAULT '',
  sentiment TEXT NOT NULL DEFAULT '',
  updated_by TEXT NOT NULL,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY(kind,target_id)
);
INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by)
VALUES('person','p1','Al Pacino','known','','test');`)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func validRuntimePersonRelatedCollectionArtifact() []byte {
	return []byte(strings.Join([]string{
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"manifest","run_id":"run-collect-1","person_ref_id":"ref-p1","movie_catalog_person_id":"p1","category":"drama","source":"wikidata","retrieved_at":"2026-08-12T00:00:00Z","item_count":1,"relation_count":1}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"identity","person_ref_id":"ref-p1","movie_catalog_person_id":"p1","identity_state":"confirmed","external_ids":{"wikidata":"Q1"},"evidence_url":"https://example.test/provider/p1"}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"item","item_id":"drama-1","category":"drama","item_type":"series","display_name":"日本語ドラマ","name_original":"Original Drama","name_ja":"日本語ドラマ","name_state":"source_ja","name_ja_source_url":"https://example.test/provider/drama-1","source_record_id":"wikidata:Q2","canonical_url":"https://example.test/provider/drama-1","source":"wikidata","description_original":"description","description_language":"en","description_ja":"説明","description_translation_state":"ready"}`,
		`{"schema_version":"rencrow.person-related-catalog.v1","record_type":"relation","relation_id":"rel-1","person_ref_id":"ref-p1","category":"drama","target_item_id":"drama-1","relation_type":"出演","source":"wikidata","evidence_url":"https://example.test/provider/rel-1","validation_state":"validated"}`,
	}, "\n") + "\n")
}

func TestRuntimePersonRelatedCatalogCollectResolvesEligiblePersonPostsAndImports(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	artifact := validRuntimePersonRelatedCollectionArtifact()
	sum := sha256.Sum256(artifact)
	expectedHash := hex.EncodeToString(sum[:])
	var gotRequest map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/person-related-catalog/collections":
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected collection method: %s", r.Method)
			}
			if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
				t.Fatalf("decode collection request: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"artifact_url": "/artifact.jsonl", "artifact_sha256": expectedHash, "artifact_bytes": int64(len(artifact))})
		case "/artifact.jsonl":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if _, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath); err != nil {
		t.Fatal(err)
	}
	collector, err := prepareRuntimePersonRelatedCatalogCollector(context.Background(), moviePath, hobbyPath, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	result, err := collector.Collect(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryDrama)
	if err != nil || result == nil {
		t.Fatalf("collect failed result=%#v err=%v", result, err)
	}
	if gotRequest["movie_catalog_person_id"] != "p1" || gotRequest["name"] != "Al Pacino" || gotRequest["url"] != "https://example.test/p1" || gotRequest["category"] != "drama" {
		t.Fatalf("unexpected provider request: %#v", gotRequest)
	}
	for key := range gotRequest {
		if key == "db_path" || key == "assessment" || key == "familiarity" || key == "sentiment" {
			t.Fatalf("private field leaked to provider: %#v", gotRequest)
		}
	}

	db, err := sql.Open("sqlite", hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_person_relations WHERE category='drama'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected imported relation count=1, got %d", count)
	}
}

func TestRuntimePersonRelatedCollectionWorkerSweepsSeenMovieD1Person(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	movieDB, err := sql.Open("sqlite", moviePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = movieDB.Exec(`DELETE FROM movie_catalog_assessments;
INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by)
VALUES('movie','m1','Heat','seen','','test');`)
	_ = movieDB.Close()
	if err != nil {
		t.Fatal(err)
	}
	hobbyPath := seedRuntimeHobbyGraph(t)
	artifact := validRuntimePersonRelatedCollectionArtifact()
	sum := sha256.Sum256(artifact)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/person-related-catalog/collections":
			_ = json.NewEncoder(w).Encode(map[string]any{"artifact_url": "/artifact.jsonl", "artifact_sha256": hex.EncodeToString(sum[:]), "artifact_bytes": int64(len(artifact))})
		case "/artifact.jsonl":
			_, _ = w.Write(artifact)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if _, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath); err != nil {
		t.Fatal(err)
	}
	collector, err := prepareRuntimePersonRelatedCatalogCollector(context.Background(), moviePath, hobbyPath, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	worker, err := prepareRuntimePersonRelatedCollectionWorker(collector, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	result, err := worker.RunOnce(context.Background())
	if err != nil || !result.Advanced || result.PersonID != "p1" || result.Category != personrelatedcatalogapp.CategoryDrama {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	db, err := sql.Open("sqlite", hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	state, err := personrelatedcatalogapp.LoadCollectionSweepState(context.Background(), db)
	if err != nil || state.CursorPersonID != "p1" || state.CategoryIndex != 1 {
		t.Fatalf("state=%#v err=%v", state, err)
	}
}

func TestRuntimePersonRelatedCatalogCollectRejectsIneligibleBeforeProvider(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	db, err := sql.Open("sqlite", moviePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO people(person_id,name,url) VALUES('p2','Unknown Person','https://example.test/p2'); INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by) VALUES('person','p2','Unknown Person','unknown','','test');`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	providerCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		providerCalls++
		http.Error(w, "unexpected provider call", http.StatusInternalServerError)
	}))
	defer server.Close()
	if _, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath); err != nil {
		t.Fatal(err)
	}
	collector, err := prepareRuntimePersonRelatedCatalogCollector(context.Background(), moviePath, hobbyPath, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(context.Background(), "Unknown Person", personrelatedcatalogapp.CategoryDrama); err == nil {
		t.Fatal("ineligible person must be rejected")
	}
	if providerCalls != 0 {
		t.Fatalf("provider called for ineligible person: %d", providerCalls)
	}
}

func TestRuntimePersonRelatedCatalogCollectPlansSourceRecordsNegativeAndReusesTTL(t *testing.T) {
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	var sources []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/person-related-catalog/collections":
			var request map[string]any
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			source, _ := request["source"].(string)
			sources = append(sources, source)
			if source != "jpsearch" {
				t.Fatalf("unexpected source: %q", source)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "unavailable", "source": source, "reason_code": "no_match"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	if _, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath); err != nil {
		t.Fatal(err)
	}
	collector, err := prepareRuntimePersonRelatedCatalogCollector(context.Background(), moviePath, hobbyPath, server.URL)
	if err != nil {
		t.Fatal(err)
	}
	value, err := collector.Collect(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryDrama)
	if err != nil {
		t.Fatal(err)
	}
	result, ok := value.(personrelatedcatalogapp.CollectionPlanResult)
	if !ok || result.StopReason != personrelatedcatalogapp.StopReasonAllSourcesTerminal || result.Status != personrelatedcatalogapp.CollectionStatusUnavailable || len(result.Attempts) != 1 {
		t.Fatalf("unexpected plan result: %#v", value)
	}
	if strings.Join(sources, ",") != "jpsearch" {
		t.Fatalf("sources=%v", sources)
	}
	db, err := sql.Open("sqlite", hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var unavailable int
	if err := db.QueryRow(`SELECT COUNT(*) FROM hobby_collection_attempts WHERE movie_catalog_person_id='p1' AND category='drama' AND status='unavailable'`).Scan(&unavailable); err != nil {
		t.Fatal(err)
	}
	if unavailable != 1 {
		t.Fatalf("attempt receipts unavailable=%d", unavailable)
	}
	second, err := collector.Collect(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryDrama)
	if err != nil {
		t.Fatal(err)
	}
	secondResult, ok := second.(personrelatedcatalogapp.CollectionPlanResult)
	if !ok || secondResult.StopReason != personrelatedcatalogapp.StopReasonAllSourcesTerminal {
		t.Fatalf("fresh negative receipt was not reused: %#v", second)
	}
	if strings.Join(sources, ",") != "jpsearch" {
		t.Fatalf("fresh negative receipt must suppress provider call, sources=%v", sources)
	}
}

func TestBuildToolRuntimeRegistersCollectOnlyForWorkerWhenProviderConfigured(t *testing.T) {
	disabled := false
	server := httptest.NewServer(http.NotFoundHandler())
	defer server.Close()
	t.Setenv("RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL", server.URL)
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.MovieCatalog = seedRuntimeEligibleMovieCatalog(t)
	cfg.Storage.Databases.HobbyGraph = seedRuntimeHobbyGraph(t)
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	chatMetadata, err := runtime.ChatRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	workerMetadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(chatMetadata, "person_related_catalog.collect") {
		t.Fatal("collect Tool must not be registered for Chat")
	}
	if !hasToolMetadata(workerMetadata, "person_related_catalog.collect") || !hasToolMetadata(workerMetadata, "person_related_catalog.lookup") {
		t.Fatalf("Worker collect/lookup metadata missing: %#v", workerMetadata)
	}
}

func TestBuildToolRuntimeLeavesCollectUnregisteredWhenProviderUnset(t *testing.T) {
	disabled := false
	t.Setenv("RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL", "")
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.MovieCatalog = seedRuntimeEligibleMovieCatalog(t)
	cfg.Storage.Databases.HobbyGraph = seedRuntimeHobbyGraph(t)
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(metadata, "person_related_catalog.collect") {
		t.Fatal("provider-unset collect Tool must be unregistered")
	}
	if !hasToolMetadata(metadata, "person_related_catalog.lookup") {
		t.Fatal("lookup Tool must remain registered when provider is unset")
	}
}

func TestLivePersonRelatedCatalogProviderCollectImportLookupE2E(t *testing.T) {
	if os.Getenv("RENCROW_LIVE_PERSON_RELATED_E2E") != "1" {
		t.Skip("set RENCROW_LIVE_PERSON_RELATED_E2E=1 and RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL for the cross-module E2E")
	}
	providerURL := strings.TrimSpace(os.Getenv("RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL"))
	if providerURL == "" {
		t.Fatal("RENCROW_PERSON_RELATED_CATALOG_PROVIDER_URL is required")
	}
	moviePath := seedRuntimeEligibleMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	movieDB, err := sql.Open("sqlite", moviePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = movieDB.Exec(`
INSERT INTO people(person_id,name,url) VALUES('35188','新海誠','https://eiga.com/person/35188/');
INSERT INTO movie_catalog_assessments(kind,target_id,target_label,familiarity,sentiment,updated_by)
VALUES('person','35188','新海誠','known','like','live-e2e');`)
	if closeErr := movieDB.Close(); err != nil {
		t.Fatal(err)
	} else if closeErr != nil {
		t.Fatal(closeErr)
	}
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	collector, err := prepareRuntimePersonRelatedCatalogCollector(context.Background(), moviePath, hobbyPath, providerURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collector.Collect(context.Background(), "新海誠", personrelatedcatalogapp.CategoryAnime); err != nil {
		t.Fatalf("live collect/import failed: %v", err)
	}
	value, err := lookup.Lookup(context.Background(), "新海誠", personrelatedcatalogapp.CategoryAnime, 20)
	if err != nil {
		t.Fatalf("live indexed lookup failed: %v", err)
	}
	result, ok := value.(personrelatedcatalogapp.LookupResult)
	if !ok || len(result.Items) == 0 {
		t.Fatalf("live indexed lookup returned no catalog rows: %#v", value)
	}
	if result.SummaryCoverage.Total != len(result.Items) || result.SummaryCoverage.Ready+result.SummaryCoverage.Unavailable != result.SummaryCoverage.Total {
		t.Fatalf("live summary coverage is inconsistent: %#v", result.SummaryCoverage)
	}
	for _, item := range result.Items {
		if item.DisplayName == "" || item.NameOriginal == "" || item.Source != "mediaarts_db" || item.ValidationState != "validated" {
			t.Fatalf("live item violated the public catalog contract: %#v", item)
		}
		if item.SummaryState == "unavailable" && item.SummaryJA != "" {
			t.Fatalf("unavailable summary leaked or invented Japanese text: %#v", item)
		}
	}
	t.Logf("live CORE E2E: items=%d summaries_ready=%d summaries_unavailable=%d", len(result.Items), result.SummaryCoverage.Ready, result.SummaryCoverage.Unavailable)
}
