package webgather

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"

	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
)

type ResearchCacheScope struct {
	DataScope string
	OwnerID   string
}

type ResearchCacheScopeResolver func(context.Context) (ResearchCacheScope, bool)

// ResearchScopedSearchCache partitions only Research cache operations. The
// original query is not persisted in the shared L1 search cache key.
type ResearchScopedSearchCache struct {
	next    SearchCache
	resolve ResearchCacheScopeResolver
}

func NewResearchScopedSearchCache(next SearchCache, resolve ResearchCacheScopeResolver) *ResearchScopedSearchCache {
	return &ResearchScopedSearchCache{next: next, resolve: resolve}
}

func (c *ResearchScopedSearchCache) Get(ctx context.Context, provider, query string, now time.Time) ([]modulewebgather.SearchResult, bool, error) {
	if c == nil || c.next == nil {
		return nil, false, nil
	}
	key, ok := researchSearchCacheQuery(ctx, c.resolve, query)
	if !ok {
		return nil, false, nil
	}
	return c.next.Get(ctx, provider, key, now)
}

func (c *ResearchScopedSearchCache) Save(ctx context.Context, provider, query string, results []modulewebgather.SearchResult, ttl time.Duration) error {
	if c == nil || c.next == nil {
		return nil
	}
	key, ok := researchSearchCacheQuery(ctx, c.resolve, query)
	if !ok {
		return nil
	}
	return c.next.Save(ctx, provider, key, results, ttl)
}

// The shared recent-cache API has no scope filter, so it is intentionally
// unavailable to Research rather than exposing another owner's cached rows.
func (c *ResearchScopedSearchCache) SearchLocal(ctx context.Context, _ string, _ int, _ time.Time) ([]modulewebgather.SearchResult, bool, error) {
	if _, ok := researchCacheScope(ctx, c.scopeResolver()); !ok {
		return nil, false, errors.New("research search cache scope is unavailable")
	}
	return nil, false, errors.New("research local-cache search is unavailable without a scoped owner query")
}

func (c *ResearchScopedSearchCache) scopeResolver() ResearchCacheScopeResolver {
	if c == nil {
		return nil
	}
	return c.resolve
}

type ResearchScopedFetchCache struct {
	next    FetchCache
	resolve ResearchCacheScopeResolver
}

func NewResearchScopedFetchCache(next FetchCache, resolve ResearchCacheScopeResolver) *ResearchScopedFetchCache {
	return &ResearchScopedFetchCache{next: next, resolve: resolve}
}

func (c *ResearchScopedFetchCache) Get(ctx context.Context, request modulewebgather.FetchRequest, now time.Time) (modulewebgather.FetchResponse, bool, error) {
	if c == nil || c.next == nil {
		return modulewebgather.FetchResponse{}, false, nil
	}
	scoped, ok := researchScopedFetchRequest(ctx, c.resolve, request)
	if !ok {
		return modulewebgather.FetchResponse{}, false, nil
	}
	return c.next.Get(ctx, scoped, now)
}

func (c *ResearchScopedFetchCache) Save(ctx context.Context, request modulewebgather.FetchRequest, response modulewebgather.FetchResponse, ttl time.Duration) error {
	if c == nil || c.next == nil {
		return nil
	}
	scoped, ok := researchScopedFetchRequest(ctx, c.resolve, request)
	if !ok {
		return nil
	}
	return c.next.Save(ctx, scoped, response, ttl)
}

// Rate limiting remains host-wide, matching the existing public HTTP policy.
func (c *ResearchScopedFetchCache) RateDelay(ctx context.Context, rawURL string, now time.Time, minInterval time.Duration) (time.Duration, error) {
	if c == nil || c.next == nil {
		return 0, nil
	}
	return c.next.RateDelay(ctx, rawURL, now, minInterval)
}

func (c *ResearchScopedFetchCache) RecordRate(ctx context.Context, rawURL string, at time.Time) error {
	if c == nil || c.next == nil {
		return nil
	}
	return c.next.RecordRate(ctx, rawURL, at)
}

func researchSearchCacheQuery(ctx context.Context, resolve ResearchCacheScopeResolver, query string) (string, bool) {
	scope, ok := researchCacheScope(ctx, resolve)
	query = strings.TrimSpace(query)
	if !ok || query == "" {
		return "", false
	}
	queryDigest := sha256.Sum256([]byte(query))
	return "research:" + scope + ":" + hex.EncodeToString(queryDigest[:]), true
}

func researchScopedFetchRequest(ctx context.Context, resolve ResearchCacheScopeResolver, request modulewebgather.FetchRequest) (modulewebgather.FetchRequest, bool) {
	scope, ok := researchCacheScope(ctx, resolve)
	if !ok {
		return modulewebgather.FetchRequest{}, false
	}
	u, err := url.Parse(strings.TrimSpace(request.URL))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return modulewebgather.FetchRequest{}, false
	}
	partition := "__rencrow_research_scope=" + scope
	if u.RawQuery == "" {
		u.RawQuery = partition
	} else {
		u.RawQuery += "&" + partition
	}
	request.URL = u.String()
	return request, true
}

func researchCacheScope(ctx context.Context, resolve ResearchCacheScopeResolver) (string, bool) {
	if ctx == nil || resolve == nil {
		return "", false
	}
	scope, ok := resolve(ctx)
	if !ok {
		return "", false
	}
	identity := scope.DataScope
	switch scope.DataScope {
	case "public":
	case "user":
		if scope.OwnerID == "" || strings.TrimSpace(scope.OwnerID) != scope.OwnerID || strings.ContainsAny(scope.OwnerID, "\r\n\x00") {
			return "", false
		}
		identity += "\x00" + scope.OwnerID
	default:
		return "", false
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:]), true
}
