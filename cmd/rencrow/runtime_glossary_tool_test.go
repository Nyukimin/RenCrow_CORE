package main

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	glossaryapp "github.com/Nyukimin/RenCrow_CORE/internal/application/glossary"
	_ "modernc.org/sqlite"
)

func seedRuntimeGlossary(t *testing.T, withIndexes bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "glossary.db")
	db, err := sql.Open("sqlite", p)
	if err != nil {
		t.Fatal(err)
	}
	schema := `CREATE TABLE glossary_items(id TEXT PRIMARY KEY,term TEXT,explanation TEXT,source TEXT,category TEXT,created_at TEXT,updated_at TEXT);`
	if withIndexes {
		schema += `CREATE INDEX idx_term ON glossary_items(term);CREATE INDEX idx_category ON glossary_items(category);`
	}
	schema += `INSERT INTO glossary_items VALUES('1','Go','language','test','tech','','2026');`
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	db.Close()
	return p
}
func TestRuntimeGlossaryReadOnlyLookup(t *testing.T) {
	p := seedRuntimeGlossary(t, true)
	lookup, err := prepareRuntimeGlossaryLookup(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := lookup.Lookup(context.Background(), "define_term", "Go", "", 10)
	if err != nil || len(raw.(glossaryapp.LookupResult).Items) != 1 {
		t.Fatalf("raw=%#v err=%v", raw, err)
	}
	db, err := openRuntimeGlossaryReadOnly(p)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE forbidden(id)`); err == nil {
		t.Fatal("read-only write accepted")
	}
}
func TestRuntimeGlossaryMissingIndexUnavailable(t *testing.T) {
	if _, err := prepareRuntimeGlossaryLookup(context.Background(), seedRuntimeGlossary(t, false)); err == nil {
		t.Fatal("missing index accepted")
	}
	if _, err := prepareRuntimeGlossaryLookup(context.Background(), filepath.Join(t.TempDir(), "missing.db")); err == nil {
		t.Fatal("missing DB accepted")
	}
}

func TestMissingGlossaryLeavesToolUnregisteredAndCatalogUnavailable(t *testing.T) {
	disabled := false
	cfg := &config.Config{WorkspaceDir: t.TempDir(), ToolHarness: config.ToolHarnessConfig{Enabled: &disabled, RecordEvents: &disabled}}
	cfg.Storage.Databases.Glossary = filepath.Join(t.TempDir(), "missing.db")
	runtime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, nil)
	metadata, err := runtime.WorkerRunnerV2.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hasToolMetadata(metadata, "glossary.lookup") {
		t.Fatal("missing glossary Tool registered")
	}
	if !hasToolMetadata(metadata, "data_capability.describe") {
		t.Fatal("metadata catalog missing")
	}
	entry, _ := buildRuntimeDataCapabilityCatalog(cfg, false, false).catalog.Describe("glossary")
	if entry.Status != "unavailable" {
		t.Fatalf("entry=%#v", entry)
	}
}
