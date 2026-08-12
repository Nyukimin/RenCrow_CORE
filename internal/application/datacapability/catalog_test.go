package datacapability

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCatalogContainsEveryKnownStoreWithoutPaths(t *testing.T) {
	states := map[string]StoreState{"glossary": {Configured: true, Exists: true}, "movie_catalog": {Configured: true, Exists: true}}
	catalog := Build(states)
	all := catalog.All()
	if len(all) != len(KnownStoreKeys()) {
		t.Fatalf("entries=%d keys=%d", len(all), len(KnownStoreKeys()))
	}
	encoded, err := json.Marshal(all)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "/srv/") || strings.Contains(string(encoded), ".db") {
		t.Fatalf("catalog leaked a path: %s", encoded)
	}
	for _, key := range KnownStoreKeys() {
		entry, err := catalog.Describe(key)
		if err != nil || entry.PhysicalKey != "storage.databases."+key {
			t.Fatalf("missing key %s: %#v err=%v", key, entry, err)
		}
	}
	if entry, _ := catalog.Describe("glossary"); entry.Status != "available" || entry.ToolID != "glossary.lookup" {
		t.Fatalf("glossary=%#v", entry)
	}
	if entry, _ := catalog.Describe("conversation_l1"); entry.Status != "restricted" {
		t.Fatalf("conversation=%#v", entry)
	}
	if entry, _ := catalog.Describe("knowledge_memory"); entry.Status != KnowledgeMemoryStatusUnavailable || entry.Reason != KnowledgeMemoryReasonDatabaseUnavailable {
		t.Fatalf("knowledge=%#v", entry)
	}
}

func TestCatalogMissingSemanticStoreIsUnavailable(t *testing.T) {
	catalog := Build(map[string]StoreState{"glossary": {Configured: true, Exists: false}})
	entry, err := catalog.Describe("glossary")
	if err != nil || entry.Status != "unavailable" || entry.Reason != "database_unavailable" {
		t.Fatalf("entry=%#v err=%v", entry, err)
	}
	if _, err := catalog.Describe(""); err == nil {
		t.Fatal("expected blank-name error")
	}
	if _, err := catalog.Describe("unknown"); err == nil {
		t.Fatal("expected unknown-name error")
	}
}

func TestCatalogKnowledgeMemoryFollowsPromotionGates(t *testing.T) {
	tests := []struct {
		name       string
		state      KnowledgeMemoryState
		status     string
		reason     string
		operations []string
		toolID     string
	}{
		{
			name:   "database missing",
			state:  KnowledgeMemoryState{Configured: true, DatabaseAvailable: false},
			status: KnowledgeMemoryStatusUnavailable,
			reason: KnowledgeMemoryReasonDatabaseUnavailable,
		},
		{
			name:   "schema missing",
			state:  KnowledgeMemoryState{Configured: true, DatabaseAvailable: true, SchemaReady: false},
			status: KnowledgeMemoryStatusBlocked,
			reason: KnowledgeMemoryReasonSchemaMissing,
		},
		{
			name:   "backfill indexing",
			state:  KnowledgeMemoryState{Configured: true, DatabaseAvailable: true, SchemaReady: true, IndexReady: true, CoverageState: KnowledgeMemoryCoverageIndexing},
			status: KnowledgeMemoryStatusBlocked,
			reason: KnowledgeMemoryReasonIndexing,
		},
		{
			name:   "integrity failed",
			state:  KnowledgeMemoryState{Configured: true, DatabaseAvailable: true, SchemaReady: true, IndexReady: true, CoverageState: KnowledgeMemoryCoverageReady, IntegrityState: KnowledgeMemoryIntegrityFailed},
			status: KnowledgeMemoryStatusBlocked,
			reason: KnowledgeMemoryReasonIntegrityFailed,
		},
		{
			name:       "public ready without private scope",
			state:      KnowledgeMemoryState{Configured: true, DatabaseAvailable: true, SchemaReady: true, IndexReady: true, CoverageState: KnowledgeMemoryCoverageReady, IntegrityState: KnowledgeMemoryIntegrityReady, ToolReady: true},
			status:     KnowledgeMemoryStatusAvailable,
			reason:     KnowledgeMemoryReasonScopeUnavailable,
			operations: []string{KnowledgeMemoryOperationPublicSearch},
			toolID:     KnowledgeMemoryToolID,
		},
		{
			name:       "private ready only with trusted scope",
			state:      KnowledgeMemoryState{Configured: true, DatabaseAvailable: true, SchemaReady: true, IndexReady: true, CoverageState: KnowledgeMemoryCoverageReady, IntegrityState: KnowledgeMemoryIntegrityReady, ToolReady: true, PrivateScopeReady: true},
			status:     KnowledgeMemoryStatusAvailable,
			operations: []string{KnowledgeMemoryOperationPublicSearch, KnowledgeMemoryOperationUserPrivateSearch},
			toolID:     KnowledgeMemoryToolID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			catalog := Build(map[string]StoreState{"knowledge_memory": {Configured: tt.state.Configured, Exists: tt.state.DatabaseAvailable, KnowledgeMemory: &tt.state}})
			entry, err := catalog.Describe("knowledge_memory")
			if err != nil {
				t.Fatal(err)
			}
			if entry.Status != tt.status || entry.Reason != tt.reason {
				t.Fatalf("entry status/reason = %q/%q, want %q/%q: %#v", entry.Status, entry.Reason, tt.status, tt.reason, entry)
			}
			if strings.Join(entry.SafeOperations, "\x00") != strings.Join(tt.operations, "\x00") {
				t.Fatalf("safe operations = %#v, want %#v", entry.SafeOperations, tt.operations)
			}
			if entry.ToolID != tt.toolID {
				t.Fatalf("tool id = %q, want %q", entry.ToolID, tt.toolID)
			}
		})
	}
}
