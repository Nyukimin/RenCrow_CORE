package main

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	personrelatedcatalogapp "github.com/Nyukimin/RenCrow_CORE/internal/application/personrelatedcatalog"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	_ "modernc.org/sqlite"
)

func seedRuntimeHobbyGraph(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hobby.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Ping(); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func seedRuntimeHobbyDrama(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec(`
INSERT INTO hobby_person_references(person_ref_id,movie_catalog_person_id,identity_state,external_ids_json,evidence_url,run_id)
VALUES('ref-p1','p1','confirmed','{"imdb":"nm0000199"}','https://example.test/p1','run-1');
INSERT INTO hobby_related_items(item_id,category,item_type,display_name,name_original,name_ja,name_state,name_ja_source_url,source_record_id,canonical_url,source,description_original,description_language,description_ja,description_translation_state)
VALUES('d1','drama','series','Drama One','Drama One','ドラマ1','official_ja','https://example.test/ja/d1','source-d1','https://example.test/d1','source','desc','en','説明','human');
INSERT INTO hobby_person_relations(relation_id,person_ref_id,category,target_item_id,relation_type,source,evidence_url,validation_state)
VALUES('rel-1','ref-p1','drama','d1','出演','source','https://example.test/evidence/rel-1','validated');`)
	if err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRuntimePersonRelatedCatalogRequiresBothDatabasesAndUsesReadOnlyQueries(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	if lookup == nil || lookup.movieCatalogPath == "" || lookup.hobbyGraphPath == "" {
		t.Fatalf("unexpected runtime lookup: %#v", lookup)
	}

	movieDB, err := openRuntimeMovieCatalogReadOnly(lookup.movieCatalogPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := movieDB.Exec(`CREATE TABLE forbidden_movie_write(id INTEGER)`); err == nil {
		t.Fatal("movie read-only runtime connection accepted a schema write")
	}
	_ = movieDB.Close()
	hobbyDB, err := openRuntimePersonRelatedCatalogReadOnly(lookup.hobbyGraphPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := hobbyDB.Exec(`CREATE TABLE forbidden_hobby_write(id INTEGER)`); err == nil {
		t.Fatal("hobby read-only runtime connection accepted a schema write")
	}
	_ = hobbyDB.Close()
}

func TestRuntimePersonRelatedCatalogMovieUsesMovieFilmographyWithoutHobbyQuery(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	movieDB, err := sql.Open("sqlite", moviePath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := movieDB.Exec(`UPDATE movies SET synopsis='映画概要' WHERE movie_id='m1'; UPDATE movie_people SET movie_url='https://example.test/m1' WHERE movie_id='m1';`); err != nil {
		_ = movieDB.Close()
		t.Fatal(err)
	}
	_ = movieDB.Close()
	lookup.hobbyGraphPath = filepath.Join(t.TempDir(), "missing-hobby.sqlite")
	result, err := lookup.Lookup(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryMovie, 20)
	if err != nil {
		t.Fatal(err)
	}
	filmography, ok := result.(personrelatedcatalogapp.LookupResult)
	if !ok {
		t.Fatalf("movie category must return person related catalog result, got %T", result)
	}
	if len(filmography.Items) != 1 || filmography.SummaryCoverage != (personrelatedcatalogapp.SummaryCoverage{Ready: 1, Unavailable: 0, Total: 1}) {
		t.Fatalf("unexpected movie filmography result: %#v", filmography)
	}
	if item := filmography.Items[0]; item.DisplayName != "Heat" || item.SummaryJA != "映画概要" || item.SummaryState != "source_summary" || item.SummarySourceURL != "https://example.test/m1" {
		t.Fatalf("unexpected movie summary projection: %#v", filmography.Items[0])
	}
	if _, err := lookup.Lookup(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryDrama, 20); err == nil {
		t.Fatal("non-movie category must require the hobby read-only database")
	}
}

func TestRuntimePersonRelatedCatalogNonMovieResolvesExactPersonThenReadsHobby(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	seedRuntimeHobbyDrama(t, hobbyPath)
	result, err := lookup.Lookup(context.Background(), " AL PACINO ", personrelatedcatalogapp.CategoryDrama, 20)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := result.(personrelatedcatalogapp.LookupResult)
	if !ok || len(items.Items) != 1 || items.Items[0].MovieCatalogPersonID != "p1" || items.Items[0].Category != personrelatedcatalogapp.CategoryDrama || items.SummaryCoverage != (personrelatedcatalogapp.SummaryCoverage{Ready: 0, Unavailable: 1, Total: 1}) {
		t.Fatalf("unexpected hobby result: %#v", result)
	}
}

func TestRuntimePersonRelatedCatalogAmbiguousNameStopsBeforeHobbyQuery(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	db, err := sql.Open("sqlite", moviePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO people(person_id,name,url) VALUES('p2','Al Pacino','https://example.test/p2')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	lookup.hobbyGraphPath = filepath.Join(t.TempDir(), "missing-hobby.sqlite")
	_, err = lookup.Lookup(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryDrama, 20)
	var ambiguous *tools.PersonRelatedCatalogAmbiguousError
	if !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("expected ambiguous exact person result, err=%v", err)
	}
}

func TestRuntimePersonRelatedCatalogMovieStillDetectsAmbiguityAtLimitOne(t *testing.T) {
	moviePath := seedRuntimeMovieCatalog(t)
	hobbyPath := seedRuntimeHobbyGraph(t)
	db, err := sql.Open("sqlite", moviePath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`INSERT INTO people(person_id,name,url) VALUES('p2','Al Pacino','https://example.test/p2')`)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close()
	lookup, err := prepareRuntimePersonRelatedCatalogLookup(context.Background(), moviePath, hobbyPath)
	if err != nil {
		t.Fatal(err)
	}
	lookup.hobbyGraphPath = filepath.Join(t.TempDir(), "missing-hobby.sqlite")
	_, err = lookup.Lookup(context.Background(), "Al Pacino", personrelatedcatalogapp.CategoryMovie, 1)
	var ambiguous *tools.PersonRelatedCatalogAmbiguousError
	if !errors.As(err, &ambiguous) || len(ambiguous.Candidates) != 2 {
		t.Fatalf("expected movie ambiguity independent of result limit, err=%v", err)
	}
}

func TestBuildToolRuntimeRegistersPersonRelatedCatalogForChatWorkerAndSnapshot(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.MovieCatalog = seedRuntimeMovieCatalog(t)
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
	if !hasToolMetadata(chatMetadata, "person_related_catalog.lookup") || !hasToolMetadata(workerMetadata, "person_related_catalog.lookup") {
		t.Fatalf("person related catalog Tool missing chat=%#v worker=%#v", chatMetadata, workerMetadata)
	}
	for name, runner := range map[string]domaintool.RunnerV2{"chat": runtime.ChatRunnerV2, "worker": runtime.WorkerRunnerV2} {
		response, err := runner.ExecuteV2(context.Background(), "person_related_catalog.lookup", map[string]any{"person_name": "Al Pacino", "category": "movie"})
		if err != nil || response == nil || response.IsError() {
			t.Fatalf("%s production runner execution failed response=%#v err=%v", name, response, err)
		}
	}
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(workerMetadata, nil, nil, nil))
	if entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, "person_related_catalog.lookup"); !ok || entry.Status != capdomain.CapabilityStatusAvailable {
		t.Fatalf("snapshot missing person related catalog Tool: %#v", snapshot.Entries)
	}
	hobbyEntry, err := runtime.DataCapabilityCatalog.catalog.Describe("hobby_graph")
	if err != nil || hobbyEntry.Status != "available" || hobbyEntry.ToolID != "person_related_catalog.lookup" {
		t.Fatalf("hobby graph data capability is not ready: entry=%#v err=%v", hobbyEntry, err)
	}
}

func TestBuildToolRuntimeLeavesPersonRelatedCatalogUnregisteredWithoutEitherDatabase(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.MovieCatalog = seedRuntimeMovieCatalog(t)
	cfg.Storage.Databases.HobbyGraph = filepath.Join(t.TempDir(), "missing-hobby.sqlite")
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(metadata, "person_related_catalog.lookup") {
		t.Fatal("person related catalog Tool must be unregistered without hobby schema")
	}
}
