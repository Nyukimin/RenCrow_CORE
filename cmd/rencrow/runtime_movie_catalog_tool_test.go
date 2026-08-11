package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	capdomain "github.com/Nyukimin/RenCrow_CORE/internal/domain/capability"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	_ "modernc.org/sqlite"
)

func seedRuntimeMovieCatalog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "movie.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE movies(movie_id TEXT PRIMARY KEY,title TEXT NOT NULL,url TEXT NOT NULL,synopsis TEXT);
CREATE TABLE people(person_id TEXT PRIMARY KEY,name TEXT NOT NULL,url TEXT NOT NULL,profile_json TEXT,biography TEXT);
CREATE TABLE movie_people(movie_id TEXT NOT NULL,person_id TEXT NOT NULL,role TEXT NOT NULL,source TEXT NOT NULL,movie_title TEXT,person_name TEXT,movie_url TEXT,person_url TEXT,PRIMARY KEY(movie_id,person_id,role,source));
INSERT INTO movies(movie_id,title,url) VALUES('m1','Heat','https://example.test/m1');
INSERT INTO people(person_id,name,url) VALUES('p1','Al Pacino','https://example.test/p1');
INSERT INTO movie_people(movie_id,person_id,role,source,movie_title,person_name) VALUES('m1','p1','actor','test','Heat','Al Pacino');`)
	closeErr := db.Close()
	if err != nil {
		t.Fatal(err)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	return path
}

func TestPrepareRuntimeMovieCatalogLookupMigratesAndExecutesReadOnly(t *testing.T) {
	path := seedRuntimeMovieCatalog(t)
	lookup, err := prepareRuntimeMovieCatalogLookup(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lookup.Lookup(context.Background(), "person", "AL PACINO", "profile", 10)
	if err != nil || result == nil {
		t.Fatalf("lookup failed result=%#v err=%v", result, err)
	}

	db, err := openRuntimeMovieCatalogReadOnly(lookup.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE forbidden_write(id INTEGER)`); err == nil {
		t.Fatal("read-only runtime connection accepted a schema write")
	}
}

func TestBuildToolRuntimeRegistersMovieCatalogForChatWorkerAndSnapshot(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.MovieCatalog = seedRuntimeMovieCatalog(t)
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	chatMetadata, err := runtime.ChatRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	workerMetadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !hasToolMetadata(chatMetadata, "movie_catalog.lookup") || !hasToolMetadata(workerMetadata, "movie_catalog.lookup") {
		t.Fatalf("Tool missing chat=%#v worker=%#v", chatMetadata, workerMetadata)
	}
	for name, runner := range map[string]domaintool.RunnerV2{"chat": runtime.ChatRunnerV2, "worker": runtime.WorkerRunnerV2} {
		response, err := runner.ExecuteV2(context.Background(), "movie_catalog.lookup", map[string]any{"kind": "movie", "name": "Heat"})
		if err != nil || response == nil || response.IsError() {
			t.Fatalf("%s production runner execution failed response=%#v err=%v", name, response, err)
		}
	}
	snapshot := capdomain.Normalize(buildRuntimeCapabilitySnapshotWithSkills(workerMetadata, nil, nil, nil))
	if entry, ok := findRuntimeCapability(snapshot, capdomain.CapabilityKindTool, "movie_catalog.lookup"); !ok || entry.Status != capdomain.CapabilityStatusAvailable {
		t.Fatalf("snapshot missing Tool: %#v", snapshot.Entries)
	}
}

func TestBuildToolRuntimeLeavesMissingMovieCatalogUnregistered(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.MovieCatalog = filepath.Join(t.TempDir(), "missing.sqlite")
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(metadata, "movie_catalog.lookup") {
		t.Fatal("missing optional DB must leave Tool unregistered")
	}
}

func TestPrepareRuntimeMovieCatalogLookupHonorsCancelledContext(t *testing.T) {
	path := seedRuntimeMovieCatalog(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareRuntimeMovieCatalogLookup(ctx, path); err == nil {
		t.Fatal("cancelled startup migration context must fail closed")
	}
}
