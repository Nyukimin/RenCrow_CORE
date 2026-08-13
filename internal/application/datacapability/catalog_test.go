package datacapability

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	coreconfig "github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
)

func allConfiguredStoreStates() map[string]StoreState {
	states := make(map[string]StoreState, len(KnownStoreKeys()))
	for _, key := range KnownStoreKeys() {
		states[key] = StoreState{Configured: true, Exists: true}
	}
	return states
}

func sortedSetKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func databasePathsConfigYAMLKeys(t *testing.T) map[string]struct{} {
	t.Helper()
	typeOfConfig := reflect.TypeOf(coreconfig.DatabasePathsConfig{})
	keys := make(map[string]struct{}, typeOfConfig.NumField())
	for i := 0; i < typeOfConfig.NumField(); i++ {
		field := typeOfConfig.Field(i)
		tag := strings.Split(field.Tag.Get("yaml"), ",")[0]
		if tag == "" || tag == "-" {
			t.Fatalf("DatabasePathsConfig.%s must have a yaml key", field.Name)
		}
		if _, exists := keys[tag]; exists {
			t.Fatalf("duplicate DatabasePathsConfig yaml key %q", tag)
		}
		keys[tag] = struct{}{}
	}
	return keys
}

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

func TestCatalogKeysExactlyMatchDatabasePathsConfig(t *testing.T) {
	catalog := Build(allConfiguredStoreStates())
	want := databasePathsConfigYAMLKeys(t)
	if len(want) != 20 {
		t.Fatalf("DatabasePathsConfig yaml keys=%d, want 20", len(want))
	}

	got := make(map[string]struct{}, len(catalog.All()))
	for _, entry := range catalog.All() {
		got[entry.Name] = struct{}{}
	}
	if !reflect.DeepEqual(sortedSetKeys(got), sortedSetKeys(want)) {
		t.Fatalf("catalog keys=%v, want DatabasePathsConfig keys=%v", sortedSetKeys(got), sortedSetKeys(want))
	}
}

func TestCatalogEveryStoreHasMachineReadableRecallDisposition(t *testing.T) {
	catalog := Build(allConfiguredStoreStates())
	for _, entry := range catalog.All() {
		switch entry.Status {
		case "available", "restricted":
			if entry.ToolID == "" && len(entry.SafeOperations) == 0 {
				t.Errorf("store %q is %q without a machine-readable safe recall route; reason %q is insufficient", entry.Name, entry.Status, entry.Reason)
			}
		case "blocked", "unavailable":
			if strings.TrimSpace(entry.Reason) == "" {
				t.Errorf("store %q is %q without a machine-readable disposition reason", entry.Name, entry.Status)
			}
		default:
			t.Errorf("store %q has unsupported catalog status %q", entry.Name, entry.Status)
		}
	}
}

func TestCatalogRestrictedStoresExposeWorkerRecallOperations(t *testing.T) {
	wantOperations := map[string]string{
		"conversation_l1":        "recall_pack",
		"conversation_archive":   "recall_pack",
		"tool_registry":          "runtime_snapshot",
		"advisor":                "advice_runs",
		"sandbox":                "sandboxes",
		"dci":                    "search_traces",
		"skill_governance":       "skill_manifests",
		"workstream":             "goals",
		"revenue":                "opportunities",
		"persona_architecture":   "canonical_responses",
		"browser_trace_to_api":   "validated_candidates",
		"complexity_hotspot":     "hotspots",
		"super_agent_harness":    "agent_runs",
		"ai_workflow":            "command_registry",
		"durable_store_workflow": "exact_request",
	}
	catalog := Build(allConfiguredStoreStates())
	for name, operation := range wantOperations {
		entry, err := catalog.Describe(name)
		if err != nil {
			t.Fatalf("restricted store %q missing from catalog: %v", name, err)
		}
		if entry.Status != "restricted" {
			t.Errorf("store %q status = %q, want restricted", name, entry.Status)
		}
		if !reflect.DeepEqual(entry.SafeOperations, []string{operation}) {
			t.Errorf("store %q safe operations = %#v, want %#v", name, entry.SafeOperations, []string{operation})
		}
		if entry.ToolID != "" {
			t.Errorf("store %q exposes tool id %q; restricted recall must remain Worker metadata", name, entry.ToolID)
		}
	}
}

func TestCatalogOperationalStoresAreNotNormalChatAutoRecall(t *testing.T) {
	catalog := Build(allConfiguredStoreStates())
	operationalStores := []string{
		"tool_registry",
		"advisor",
		"sandbox",
		"dci",
		"skill_governance",
		"workstream",
		"revenue",
		"persona_architecture",
		"browser_trace_to_api",
		"complexity_hotspot",
		"super_agent_harness",
		"ai_workflow",
		"durable_store_workflow",
	}
	available := make(map[string]struct{})
	for _, entry := range catalog.ListAvailable() {
		available[entry.Name] = struct{}{}
	}
	for _, key := range operationalStores {
		entry, err := catalog.Describe(key)
		if err != nil {
			t.Fatalf("operational store %q missing from catalog: %v", key, err)
		}
		if _, ok := available[key]; ok || entry.Status == "available" {
			t.Errorf("operational store %q must not be available for normal-chat automatic recall: %#v", key, entry)
		}
	}
}

func TestCatalogInvestmentIsAvailableOnlyThroughValidatedL1Projection(t *testing.T) {
	available := Build(map[string]StoreState{"investment": {RecallReady: true}})
	entry, err := available.Describe("investment")
	if err != nil || entry.Status != "available" || strings.Join(entry.SafeOperations, "\x00") != "validated_l1_projection" || entry.ToolID != "" {
		t.Fatalf("investment projection entry=%#v err=%v", entry, err)
	}
	unavailable := Build(map[string]StoreState{"investment": {RecallReady: false}})
	entry, _ = unavailable.Describe("investment")
	if entry.Status != "unavailable" {
		t.Fatalf("investment without validated L1 projection=%#v", entry)
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
