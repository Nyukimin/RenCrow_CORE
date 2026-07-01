package conversation

import (
	"testing"
)

func TestRedisMetrics_Initialization(t *testing.T) {
	metrics := &RedisMetrics{}

	if metrics.SessionHits != 0 {
		t.Errorf("Expected SessionHits to be 0, got %d", metrics.SessionHits)
	}
	if metrics.SessionMisses != 0 {
		t.Errorf("Expected SessionMisses to be 0, got %d", metrics.SessionMisses)
	}
	if metrics.ThreadHits != 0 {
		t.Errorf("Expected ThreadHits to be 0, got %d", metrics.ThreadHits)
	}
	if metrics.ThreadMisses != 0 {
		t.Errorf("Expected ThreadMisses to be 0, got %d", metrics.ThreadMisses)
	}
}

func TestRedisMetrics_HitRate_Zero(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	if sessionRate != 0 {
		t.Errorf("Expected 0%% hit rate with no data, got %.2f%%", sessionRate)
	}
	if threadRate != 0 {
		t.Errorf("Expected 0%% hit rate with no data, got %.2f%%", threadRate)
	}
}

func TestRedisMetrics_HitRate_AllHits(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   10,
			SessionMisses: 0,
			ThreadHits:    5,
			ThreadMisses:  0,
		},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	if sessionRate != 100.0 {
		t.Errorf("Expected 100%% session hit rate, got %.2f%%", sessionRate)
	}
	if threadRate != 100.0 {
		t.Errorf("Expected 100%% thread hit rate, got %.2f%%", threadRate)
	}
}

func TestRedisMetrics_HitRate_AllMisses(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   0,
			SessionMisses: 10,
			ThreadHits:    0,
			ThreadMisses:  5,
		},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	if sessionRate != 0.0 {
		t.Errorf("Expected 0%% session hit rate, got %.2f%%", sessionRate)
	}
	if threadRate != 0.0 {
		t.Errorf("Expected 0%% thread hit rate, got %.2f%%", threadRate)
	}
}

func TestRedisMetrics_HitRate_Mixed(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   8,
			SessionMisses: 2,
			ThreadHits:    6,
			ThreadMisses:  4,
		},
	}

	sessionRate, threadRate := store.GetCacheHitRate()

	expectedSessionRate := 80.0 // 8/(8+2) * 100
	if sessionRate != expectedSessionRate {
		t.Errorf("Expected %.2f%% session hit rate, got %.2f%%", expectedSessionRate, sessionRate)
	}

	expectedThreadRate := 60.0 // 6/(6+4) * 100
	if threadRate != expectedThreadRate {
		t.Errorf("Expected %.2f%% thread hit rate, got %.2f%%", expectedThreadRate, threadRate)
	}
}

func TestRedisMetrics_GetMetrics(t *testing.T) {
	store := &RedisStore{
		metrics: &RedisMetrics{
			SessionHits:   5,
			SessionMisses: 3,
			ThreadHits:    7,
			ThreadMisses:  2,
		},
	}

	metrics := store.GetMetrics()

	if metrics.SessionHits != 5 {
		t.Errorf("Expected 5 session hits, got %d", metrics.SessionHits)
	}
	if metrics.SessionMisses != 3 {
		t.Errorf("Expected 3 session misses, got %d", metrics.SessionMisses)
	}
	if metrics.ThreadHits != 7 {
		t.Errorf("Expected 7 thread hits, got %d", metrics.ThreadHits)
	}
	if metrics.ThreadMisses != 2 {
		t.Errorf("Expected 2 thread misses, got %d", metrics.ThreadMisses)
	}
}
