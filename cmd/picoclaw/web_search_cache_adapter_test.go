package main

import (
	"context"
	"testing"
	"time"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

type fakeConversationWebSearchCache struct {
	results []conversationpersistence.WebSearchResult
	hit     bool
}

func (f *fakeConversationWebSearchCache) GetFreshWebSearchCache(_ context.Context, _ string) ([]conversationpersistence.WebSearchResult, bool, error) {
	return f.results, f.hit, nil
}

func (f *fakeConversationWebSearchCache) SaveWebSearchCache(_ context.Context, _ string, results []conversationpersistence.WebSearchResult, _ time.Duration) error {
	f.results = append([]conversationpersistence.WebSearchResult{}, results...)
	return nil
}

func TestConversationWebSearchCacheAdapter_GetFresh(t *testing.T) {
	adapter := newConversationWebSearchCacheAdapter(&fakeConversationWebSearchCache{
		hit: true,
		results: []conversationpersistence.WebSearchResult{
			{Title: "RenCrow", Link: "https://example.com/rencrow", Snippet: "memo"},
		},
	})

	items, hit, err := adapter.GetFreshWebSearchCache(context.Background(), "RenCrow")
	if err != nil {
		t.Fatalf("GetFreshWebSearchCache failed: %v", err)
	}
	if !hit {
		t.Fatal("expected cache hit")
	}
	if len(items) != 1 || items[0].Title != "RenCrow" || items[0].Link != "https://example.com/rencrow" {
		t.Fatalf("unexpected items: %+v", items)
	}
}

func TestConversationWebSearchCacheAdapter_Save(t *testing.T) {
	store := &fakeConversationWebSearchCache{}
	adapter := newConversationWebSearchCacheAdapter(store)

	err := adapter.SaveWebSearchCache(context.Background(), "RenCrow", []tools.GoogleSearchItem{
		{Title: "RenCrow", Link: "https://example.com/rencrow", Snippet: "memo"},
	}, time.Minute)
	if err != nil {
		t.Fatalf("SaveWebSearchCache failed: %v", err)
	}
	if len(store.results) != 1 || store.results[0].Title != "RenCrow" || store.results[0].Snippet != "memo" {
		t.Fatalf("unexpected saved results: %+v", store.results)
	}
}
