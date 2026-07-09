package main

import (
	"bytes"
	"context"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunKnowledgeCommandImportCoreJSONL(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	inputPath := filepath.Join(t.TempDir(), "knowledge.jsonl")
	if err := os.WriteFile(inputPath, []byte(`{"id":"movie:test","domain":"movie","title":"Test Movie","summary":"映画メモ","source_id":"manual:seed"}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"import-core-jsonl", inputPath, "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("import should pass, code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"imported":1`) {
		t.Fatalf("expected json result, got %s", out.String())
	}
	items, err := store.RecentStagingItems(context.Background(), l1sqlite.L1StagingStatusPending, 10)
	if err != nil {
		t.Fatalf("RecentStagingItems failed: %v", err)
	}
	if len(items) != 1 || items[0].Namespace != "kb:movie" || items[0].EventID != "movie:test" {
		t.Fatalf("unexpected staged items: %+v", items)
	}
}

func TestRunKnowledgeCommandIndexWiki(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()
	repoRoot := t.TempDir()
	wikiDir := filepath.Join(repoRoot, "docs", "wiki", "concepts")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wikiDir, "source-registry.md"), []byte(`---
type: concept
status: active
owner: core
canonical_source: docs/10_新仕様/09_Memory_SourceRegistry仕様.md
source:
  - docs/10_新仕様/09_Memory_SourceRegistry仕様.md
related:
  - docs/wiki/concepts/memory-lifecycle.md
updated: 2026-06-25
---

# Source Registry

Source Registry は外部 source の登録と検証境界。
`), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	var out, errOut bytes.Buffer

	code := runKnowledgeCommand([]string{"index-wiki", filepath.Join(repoRoot, "docs", "wiki"), "--repo-root", repoRoot, "--json"}, store, &out, &errOut)
	if code != 0 {
		t.Fatalf("index-wiki should pass, code=%d err=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), `"indexed":1`) {
		t.Fatalf("expected json result, got %s", out.String())
	}
	results, err := store.SearchWikiPageIndex(context.Background(), "Source Registry", 10)
	if err != nil {
		t.Fatalf("SearchWikiPageIndex failed: %v", err)
	}
	if len(results) != 1 || results[0].PageID != "concept:source-registry" {
		t.Fatalf("unexpected wiki index results: %+v", results)
	}
}
