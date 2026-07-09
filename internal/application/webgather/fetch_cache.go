package webgather

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	modulewebgather "github.com/Nyukimin/RenCrow_CORE/modules/webgather"
	"net/url"
	"strings"
	"time"
)

const (
	defaultFetchCacheTTL   = 24 * time.Hour
	defaultFailureCacheTTL = 10 * time.Minute
	defaultRateMinInterval = 3 * time.Second
)

type FetchCache interface {
	Get(ctx context.Context, req modulewebgather.FetchRequest, now time.Time) (modulewebgather.FetchResponse, bool, error)
	Save(ctx context.Context, req modulewebgather.FetchRequest, resp modulewebgather.FetchResponse, ttl time.Duration) error
	RateDelay(ctx context.Context, rawURL string, now time.Time, minInterval time.Duration) (time.Duration, error)
	RecordRate(ctx context.Context, rawURL string, at time.Time) error
}

type L1FetchCacheStore interface {
	GetFreshWebGatherFetchCache(ctx context.Context, rawURL string, fetchProvider string, extractor string, now time.Time) (*l1sqlite.L1WebGatherFetchCacheEntry, error)
	SaveWebGatherFetchCache(ctx context.Context, rawURL string, fetchProvider string, extractor string, status string, responseJSON string, ttl time.Duration) (*l1sqlite.L1WebGatherFetchCacheEntry, error)
	GetWebGatherRateState(ctx context.Context, domain string) (*l1sqlite.L1WebGatherRateState, error)
	SaveWebGatherRateState(ctx context.Context, domain string, at time.Time) (*l1sqlite.L1WebGatherRateState, error)
}

type L1FetchCache struct {
	store L1FetchCacheStore
}

func NewL1FetchCache(store L1FetchCacheStore) *L1FetchCache {
	if store == nil {
		return nil
	}
	return &L1FetchCache{store: store}
}

func (c *L1FetchCache) Get(ctx context.Context, req modulewebgather.FetchRequest, now time.Time) (modulewebgather.FetchResponse, bool, error) {
	if c == nil || c.store == nil {
		return modulewebgather.FetchResponse{}, false, nil
	}
	entry, err := c.store.GetFreshWebGatherFetchCache(ctx, req.URL, req.FetchProvider, req.Extractor, now)
	if err != nil || entry == nil {
		return modulewebgather.FetchResponse{}, false, err
	}
	var resp modulewebgather.FetchResponse
	if err := json.Unmarshal([]byte(entry.ResponseJSON), &resp); err != nil {
		return modulewebgather.FetchResponse{}, false, err
	}
	if resp.Diagnostics == nil {
		resp.Diagnostics = map[string]any{}
	}
	resp.Diagnostics["cache_hit"] = true
	resp.Diagnostics["cache_status"] = entry.Status
	resp.Diagnostics["cache_expires_at"] = entry.ExpiresAt.UTC().Format(time.RFC3339)
	return resp, true, nil
}

func (c *L1FetchCache) Save(ctx context.Context, req modulewebgather.FetchRequest, resp modulewebgather.FetchResponse, ttl time.Duration) error {
	if c == nil || c.store == nil {
		return nil
	}
	b, err := json.Marshal(resp)
	if err != nil {
		return err
	}
	_, err = c.store.SaveWebGatherFetchCache(ctx, req.URL, req.FetchProvider, req.Extractor, resp.Status, string(b), ttl)
	return err
}

func (c *L1FetchCache) RateDelay(ctx context.Context, rawURL string, now time.Time, minInterval time.Duration) (time.Duration, error) {
	if c == nil || c.store == nil {
		return 0, nil
	}
	host := webGatherRateHost(rawURL)
	if host == "" {
		return 0, nil
	}
	if minInterval <= 0 {
		minInterval = defaultRateMinInterval
	}
	state, err := c.store.GetWebGatherRateState(ctx, host)
	if err != nil || state == nil {
		return 0, err
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	nextAllowed := state.LastFetchAt.Add(minInterval)
	if nextAllowed.After(now) {
		return nextAllowed.Sub(now), nil
	}
	return 0, nil
}

func (c *L1FetchCache) RecordRate(ctx context.Context, rawURL string, at time.Time) error {
	if c == nil || c.store == nil {
		return nil
	}
	host := webGatherRateHost(rawURL)
	if host == "" {
		return nil
	}
	_, err := c.store.SaveWebGatherRateState(ctx, host, at)
	return err
}

func webGatherRateHost(rawURL string) string {
	u, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(u.Hostname()))
}
