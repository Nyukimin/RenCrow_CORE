package llmops

import (
	"sync"
	"time"
)

// ttlCache is a minimal in-memory cache keyed by string. Freshness is decided
// by the caller (storedAt + TTL against the injected clock) so tests can drive
// time deterministically. CORE has no shared TTL cache today; this one is
// private to the LLM Ops service and bounded by node count x query variants.
type ttlCache[T any] struct {
	mu      sync.RWMutex
	entries map[string]cacheEntry[T]
}

type cacheEntry[T any] struct {
	value    T
	storedAt time.Time
}

func newTTLCache[T any]() *ttlCache[T] {
	return &ttlCache[T]{entries: map[string]cacheEntry[T]{}}
}

// get returns the stored value and the time it was stored.
func (c *ttlCache[T]) get(key string) (T, time.Time, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[key]
	if !ok {
		var zero T
		return zero, time.Time{}, false
	}
	return entry.value, entry.storedAt, true
}

// set stores value under key with the given store time.
func (c *ttlCache[T]) set(key string, value T, at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = cacheEntry[T]{value: value, storedAt: at}
}

// deleteWhere removes every entry whose key satisfies pred.
func (c *ttlCache[T]) deleteWhere(pred func(key string) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if pred(key) {
			delete(c.entries, key)
		}
	}
}
