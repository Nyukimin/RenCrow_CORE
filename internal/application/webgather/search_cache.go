package webgather

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"strings"
	"time"
)

type L1SearchCacheStore interface {
	GetFreshSearchCache(ctx context.Context, provider string, rawQuery string, now time.Time) (*l1sqlite.L1SearchCacheEntry, error)
	SaveSearchCache(ctx context.Context, provider string, rawQuery string, resultsJSON string, sourceURLs []string, ttl time.Duration) (*l1sqlite.L1SearchCacheEntry, error)
	RecentSearchCache(ctx context.Context, limit int) ([]l1sqlite.L1SearchCacheEntry, error)
}

type L1SearchCache struct {
	store L1SearchCacheStore
}

func NewL1SearchCache(store L1SearchCacheStore) *L1SearchCache {
	if store == nil {
		return nil
	}
	return &L1SearchCache{store: store}
}

func (c *L1SearchCache) Get(ctx context.Context, provider string, query string, now time.Time) ([]modulewebgather.SearchResult, bool, error) {
	if c == nil || c.store == nil {
		return nil, false, nil
	}
	entry, err := c.store.GetFreshSearchCache(ctx, provider, query, now)
	if err != nil || entry == nil {
		return nil, false, err
	}
	results, err := decodeSearchResults(entry.ResultsJSON)
	return results, err == nil, err
}

func (c *L1SearchCache) Save(ctx context.Context, provider string, query string, results []modulewebgather.SearchResult, ttl time.Duration) error {
	if c == nil || c.store == nil {
		return nil
	}
	b, err := json.Marshal(results)
	if err != nil {
		return err
	}
	urls := make([]string, 0, len(results))
	for _, result := range results {
		if strings.TrimSpace(result.URL) != "" {
			urls = append(urls, strings.TrimSpace(result.URL))
		}
	}
	_, err = c.store.SaveSearchCache(ctx, provider, query, string(b), urls, ttl)
	return err
}

func (c *L1SearchCache) SearchLocal(ctx context.Context, query string, limit int, now time.Time) ([]modulewebgather.SearchResult, bool, error) {
	if c == nil || c.store == nil {
		return nil, false, nil
	}
	entries, err := c.store.RecentSearchCache(ctx, 100)
	if err != nil {
		return nil, false, err
	}
	queryTokens := tokenSet(query)
	var out []modulewebgather.SearchResult
	seen := map[string]bool{}
	for _, entry := range entries {
		if !entry.ExpiresAt.IsZero() && !now.IsZero() && !entry.ExpiresAt.After(now) {
			continue
		}
		results, err := decodeSearchResults(entry.ResultsJSON)
		if err != nil {
			continue
		}
		for _, result := range results {
			if len(queryTokens) > 0 && !matchesSearchResult(queryTokens, result) {
				continue
			}
			key := strings.TrimSpace(result.URL)
			if key == "" {
				key = result.Title + "\x00" + result.Snippet
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			result.Rank = len(out) + 1
			if result.SourceEngine == "" {
				result.SourceEngine = "local_cache:" + entry.Provider
			}
			out = append(out, result)
			if limit > 0 && len(out) >= limit {
				return out, true, nil
			}
		}
	}
	return out, len(out) > 0, nil
}

func decodeSearchResults(raw string) ([]modulewebgather.SearchResult, error) {
	var results []modulewebgather.SearchResult
	if strings.TrimSpace(raw) == "" {
		return results, nil
	}
	if err := json.Unmarshal([]byte(raw), &results); err == nil && hasSearchResultURL(results) {
		for i := range results {
			if results[i].Rank == 0 {
				results[i].Rank = i + 1
			}
		}
		return results, nil
	}
	var legacy []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	}
	if err := json.Unmarshal([]byte(raw), &legacy); err != nil {
		return nil, err
	}
	results = make([]modulewebgather.SearchResult, 0, len(legacy))
	for i, item := range legacy {
		results = append(results, modulewebgather.SearchResult{
			URL:          strings.TrimSpace(item.Link),
			Title:        strings.TrimSpace(item.Title),
			Snippet:      strings.TrimSpace(item.Snippet),
			Rank:         i + 1,
			SourceEngine: "legacy_web_search",
		})
	}
	return results, nil
}

func hasSearchResultURL(results []modulewebgather.SearchResult) bool {
	for _, result := range results {
		if strings.TrimSpace(result.URL) != "" {
			return true
		}
	}
	return len(results) == 0
}

func matchesSearchResult(tokens map[string]bool, result modulewebgather.SearchResult) bool {
	text := strings.ToLower(result.Title + " " + result.Snippet + " " + result.URL)
	for token := range tokens {
		if strings.Contains(text, token) {
			return true
		}
	}
	return len(tokens) == 0
}

func tokenSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, token := range strings.Fields(strings.ToLower(text)) {
		token = strings.Trim(token, " \t\r\n.,;:!?()[]{}\"'")
		if token != "" {
			out[token] = true
		}
	}
	return out
}
