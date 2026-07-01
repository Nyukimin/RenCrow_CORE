package main

import (
	"context"
	"time"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

type conversationWebSearchCacheStore interface {
	GetFreshWebSearchCache(ctx context.Context, query string) ([]conversationpersistence.WebSearchResult, bool, error)
	SaveWebSearchCache(ctx context.Context, query string, results []conversationpersistence.WebSearchResult, ttl time.Duration) error
}

type conversationWebSearchCacheAdapter struct {
	store conversationWebSearchCacheStore
}

func newConversationWebSearchCacheAdapter(store conversationWebSearchCacheStore) *conversationWebSearchCacheAdapter {
	if store == nil {
		return nil
	}
	return &conversationWebSearchCacheAdapter{store: store}
}

func (a *conversationWebSearchCacheAdapter) GetFreshWebSearchCache(ctx context.Context, query string) ([]tools.GoogleSearchItem, bool, error) {
	results, hit, err := a.store.GetFreshWebSearchCache(ctx, query)
	if err != nil || !hit {
		return nil, hit, err
	}
	items := make([]tools.GoogleSearchItem, 0, len(results))
	for _, result := range results {
		items = append(items, tools.GoogleSearchItem{
			Title:   result.Title,
			Link:    result.Link,
			Snippet: result.Snippet,
		})
	}
	return items, true, nil
}

func (a *conversationWebSearchCacheAdapter) SaveWebSearchCache(ctx context.Context, query string, items []tools.GoogleSearchItem, ttl time.Duration) error {
	results := make([]conversationpersistence.WebSearchResult, 0, len(items))
	for _, item := range items {
		results = append(results, conversationpersistence.WebSearchResult{
			Title:   item.Title,
			Link:    item.Link,
			Snippet: item.Snippet,
		})
	}
	return a.store.SaveWebSearchCache(ctx, query, results, ttl)
}
