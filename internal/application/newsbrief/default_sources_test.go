package newsbrief

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type defaultSourceRegistryStub struct {
	entries []l1sqlite.L1SourceRegistryEntry
}

func (s *defaultSourceRegistryStub) ListSourceRegistryEntries(context.Context, bool) ([]l1sqlite.L1SourceRegistryEntry, error) {
	return append([]l1sqlite.L1SourceRegistryEntry(nil), s.entries...), nil
}

func (s *defaultSourceRegistryStub) SaveSourceRegistryEntry(_ context.Context, entry l1sqlite.L1SourceRegistryEntry) (*l1sqlite.L1SourceRegistryEntry, error) {
	s.entries = append(s.entries, entry)
	return &entry, nil
}

func TestBootstrapDefaultSourcesAddsOnceAndPreservesDisabledURL(t *testing.T) {
	defaults := DefaultSources()
	store := &defaultSourceRegistryStub{entries: []l1sqlite.L1SourceRegistryEntry{{
		SourceID: "custom:disabled", URL: defaults[0].URL, Enabled: false,
	}}}
	result, err := BootstrapDefaultSources(context.Background(), store)
	if err != nil {
		t.Fatalf("BootstrapDefaultSources failed: %v", err)
	}
	if result.Existing != 1 || result.Added != len(defaults)-1 || result.Failed != 0 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if store.entries[0].Enabled {
		t.Fatal("existing disabled source was changed")
	}
	enabledBySourceID := make(map[string]bool, len(store.entries))
	for _, entry := range store.entries {
		enabledBySourceID[entry.SourceID] = entry.Enabled
	}
	if enabled, ok := enabledBySourceID["rss:news:nhk-sports"]; !ok || enabled {
		t.Fatalf("NHK Sports default source enabled = %v, exists = %v", enabled, ok)
	}
	if enabled, ok := enabledBySourceID["rss:news:nhk-top"]; !ok || !enabled {
		t.Fatalf("NHK Top default source enabled = %v, exists = %v", enabled, ok)
	}
	second, err := BootstrapDefaultSources(context.Background(), store)
	if err != nil {
		t.Fatalf("second bootstrap failed: %v", err)
	}
	if second.Added != 0 || second.Existing != len(defaults) {
		t.Fatalf("bootstrap was not idempotent: %+v", second)
	}
}
