package l1sqlite

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestL1SQLiteStore_WebGatherFetchCacheAndRateState(t *testing.T) {
	ctx := context.Background()
	store, err := NewL1SQLiteStore(filepath.Join(l1TestTempDir(t), "l1.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore failed: %v", err)
	}
	defer store.Close()

	_, err = store.SaveWebGatherFetchCache(ctx, "https://Example.com/a#section", "http", "html_basic", "ok", `{"url":"https://example.com/a","status":"ok"}`, time.Hour)
	if err != nil {
		t.Fatalf("SaveWebGatherFetchCache failed: %v", err)
	}
	entry, err := store.GetFreshWebGatherFetchCache(ctx, "https://example.com/a", "http", "html_basic", time.Now().UTC())
	if err != nil {
		t.Fatalf("GetFreshWebGatherFetchCache failed: %v", err)
	}
	if entry == nil || entry.URL != "https://example.com/a" || entry.Status != "ok" {
		t.Fatalf("unexpected fetch cache entry: %+v", entry)
	}

	at := time.Date(2026, 6, 1, 1, 2, 3, 0, time.UTC)
	if _, err := store.SaveWebGatherRateState(ctx, "Example.com", at); err != nil {
		t.Fatalf("SaveWebGatherRateState failed: %v", err)
	}
	state, err := store.GetWebGatherRateState(ctx, "example.com")
	if err != nil {
		t.Fatalf("GetWebGatherRateState failed: %v", err)
	}
	if state == nil || state.Domain != "example.com" || !state.LastFetchAt.Equal(at) {
		t.Fatalf("unexpected rate state: %+v", state)
	}
}
