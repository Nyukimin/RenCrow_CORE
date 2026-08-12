package knowledgememory

import (
	"context"
	"strings"
	"testing"
	"time"

	appkm "github.com/Nyukimin/RenCrow_CORE/internal/application/knowledgememory"
	domainkm "github.com/Nyukimin/RenCrow_CORE/internal/domain/knowledgememory"
)

func TestSQLiteStoreDualWritesSafeProjectionAndIndexedSearch(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir() + "/knowledge_memory.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	err = store.SaveCreativeKnowledgeItem(context.Background(), domainkm.CreativeKnowledgeItem{
		ItemID:       "creative-1",
		Title:        "日本語の作品",
		CreatorNames: []string{"作者名"},
		WorkType:     "小説",
		ContentHints: []string{"private prompt must never be indexed"},
		Status:       "reviewed",
		CreatedAt:    now,
	})
	if err != nil {
		t.Fatalf("SaveCreativeKnowledgeItem() error = %v", err)
	}

	results, err := store.Search(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic},
		Query: "日本語",
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 1 || results[0].RecordID != "creative-1" {
		t.Fatalf("results = %#v", results)
	}
	results, err = store.Search(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic},
		Query: "日本語 作品",
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("multi-token Search() error = %v", err)
	}
	if len(results) != 1 || results[0].RecordID != "creative-1" {
		t.Fatalf("multi-token results = %#v", results)
	}
	if results[0].Summary == "" || strings.Contains(results[0].Summary, "private prompt") {
		t.Fatalf("unsafe projection leaked: %#v", results[0])
	}
	encoded := strings.Join([]string{results[0].Title, results[0].Summary}, " ")
	if strings.Contains(encoded, "payload") || strings.Contains(encoded, "prompt") {
		t.Fatalf("raw/private fields leaked: %#v", results[0])
	}

	if err := store.SaveCreativeKnowledgeItem(context.Background(), domainkm.CreativeKnowledgeItem{
		ItemID:    "creative-1",
		Title:     "別の状態",
		Status:    "candidate",
		CreatedAt: now.Add(time.Minute),
	}); err != nil {
		t.Fatalf("candidate replacement error = %v", err)
	}
	results, err = store.Search(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic},
		Query: "日本語",
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Search() after replacement error = %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("candidate row remained searchable: %#v", results)
	}
}

func TestSQLiteStoreIndexedSearchClampsResultsAndIsolatesScopes(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir() + "/knowledge_memory.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	now := time.Date(2026, 8, 12, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 25; i++ {
		if err := store.SaveNewsKnowledgeItem(context.Background(), domainkm.NewsKnowledgeItem{
			ItemID:    "news-" + string(rune('a'+i)),
			Source:    "source",
			Topic:     "日本語ニュース",
			Summary:   "公開要約",
			Status:    "promoted",
			CreatedAt: now.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("SaveNewsKnowledgeItem(%d) error = %v", i, err)
		}
	}

	results, err := store.Search(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic},
		Query: "日本語",
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 20 {
		t.Fatalf("result count = %d, want 20", len(results))
	}
	if results[0].RecordID != "news-y" {
		t.Fatalf("results are not newest-first: first=%#v", results[0])
	}

	if _, err := store.Search(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopeUser, UserID: "other-user"},
		Query: "日本語",
		Limit: 20,
	}); err != nil {
		t.Fatalf("empty owner scope should be a valid bounded query: %v", err)
	}
}

func TestSQLiteStoreIndexedSearchFailsClosedWhenNamedIndexIsMissing(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir() + "/knowledge_memory.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()
	if _, err := store.db.Exec(`DROP INDEX idx_knowledge_memory_search_terms_lookup`); err != nil {
		t.Fatalf("drop index error = %v", err)
	}
	_, err = store.Search(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic},
		Query: "日本語",
		Limit: 20,
	})
	if err == nil || !strings.Contains(err.Error(), "search index") {
		t.Fatalf("missing index must fail closed, err=%v", err)
	}
}

func TestSQLiteStoreIndexedSearchPlanUsesNamedIndexesWithoutTargetScans(t *testing.T) {
	store, err := NewSQLiteStore(t.TempDir() + "/knowledge_memory.db")
	if err != nil {
		t.Fatalf("NewSQLiteStore() error = %v", err)
	}
	defer store.Close()

	plan, err := store.explainIndexedSearch(context.Background(), appkm.SearchRequest{
		Scope: appkm.SearchScope{Scope: appkm.SearchScopePublic},
		Query: "日本語",
		Limit: 20,
	})
	if err != nil {
		t.Fatalf("EXPLAIN QUERY PLAN error = %v", err)
	}
	joined := strings.ToUpper(strings.Join(plan, "\n"))
	if !strings.Contains(joined, "IDX_KNOWLEDGE_MEMORY_SEARCH_TERMS_LOOKUP") {
		t.Fatalf("term lookup index missing from plan: %s", joined)
	}
	if !strings.Contains(joined, "IDX_KNOWLEDGE_MEMORY_SEARCH_DOCUMENTS_LOOKUP") {
		t.Fatalf("document lookup index missing from plan: %s", joined)
	}
	if strings.Contains(joined, "SCAN KNOWLEDGE_MEMORY_SEARCH_TERMS") || strings.Contains(joined, "SCAN KNOWLEDGE_MEMORY_SEARCH_DOCUMENTS") {
		t.Fatalf("target table scan found in plan: %s", joined)
	}
}
