package knowledge

import (
	"context"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIndexKnowledgeWikiIndexesFrontmatterPages(t *testing.T) {
	ctx := context.Background()
	repoRoot := t.TempDir()
	wikiDir := filepath.Join(repoRoot, "docs", "wiki", "concepts")
	if err := os.MkdirAll(wikiDir, 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	pagePath := filepath.Join(wikiDir, "recall-pack.md")
	if err := os.WriteFile(pagePath, []byte(`---
type: concept
status: active
owner: core
canonical_source: docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md
source:
  - docs/01_正本仕様/18_Memory_Lifecycle_Recall_Context.md
  - internal/domain/conversation/recall_pack.go
related:
  - docs/wiki/concepts/memory-lifecycle.md
updated: 2026-06-25
---

# RecallPack

RecallPack は Mio に渡す文脈を選別済みにする prompt 注入用フォーマット。
`), 0o644); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	result, err := IndexKnowledgeWiki(ctx, store, WikiIndexOptions{
		RootDir:  filepath.Join(repoRoot, "docs", "wiki"),
		RepoRoot: repoRoot,
		Now:      func() time.Time { return time.Date(2026, 6, 25, 12, 0, 0, 0, time.UTC) },
	})
	if err != nil {
		t.Fatalf("IndexKnowledgeWiki failed: %v", err)
	}
	if result.Indexed != 1 {
		t.Fatalf("expected 1 indexed page, got %+v", result)
	}
	results, err := store.SearchWikiPageIndex(ctx, "RecallPack prompt", 10)
	if err != nil {
		t.Fatalf("SearchWikiPageIndex failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 wiki result, got %+v", results)
	}
	if results[0].PageID != "concept:recall-pack" || results[0].Path != "docs/wiki/concepts/recall-pack.md" {
		t.Fatalf("unexpected wiki index item: %+v", results[0])
	}
	if len(results[0].SourcePaths) != 2 {
		t.Fatalf("source paths not indexed: %+v", results[0].SourcePaths)
	}
}
