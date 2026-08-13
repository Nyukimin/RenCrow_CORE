package datacapability

import (
	"fmt"
	"sort"
	"strings"
)

type StoreState struct {
	Configured      bool
	Exists          bool
	RecallReady     bool
	KnowledgeMemory *KnowledgeMemoryState
}

// KnowledgeMemoryState is the startup-only evidence used to promote the
// knowledge memory capability. It deliberately carries no database path or
// row data; those remain owned by the persistence boundary.
type KnowledgeMemoryState struct {
	Configured        bool
	DatabaseAvailable bool
	SchemaReady       bool
	IndexReady        bool
	CoverageState     string
	IntegrityState    string
	ToolReady         bool
	PrivateScopeReady bool
}

const (
	KnowledgeMemoryStatusAvailable   = "available"
	KnowledgeMemoryStatusUnavailable = "unavailable"
	KnowledgeMemoryStatusBlocked     = "blocked"

	KnowledgeMemoryReasonDatabaseUnavailable = "database_unavailable"
	KnowledgeMemoryReasonSchemaMissing       = "schema_missing"
	KnowledgeMemoryReasonIndexing            = "indexing"
	KnowledgeMemoryReasonIntegrityFailed     = "integrity_failed"
	KnowledgeMemoryReasonScopeUnavailable    = "scope_unavailable"
	KnowledgeMemoryReasonToolUnavailable     = "tool_unavailable"

	KnowledgeMemoryCoverageIndexing = "indexing"
	KnowledgeMemoryCoverageReady    = "ready"
	KnowledgeMemoryIntegrityReady   = "ready"
	KnowledgeMemoryIntegrityFailed  = "failed"

	KnowledgeMemoryOperationPublicSearch      = "public_search"
	KnowledgeMemoryOperationUserPrivateSearch = "user_private_search"
	KnowledgeMemoryToolID                     = "knowledge.search"
)

type Entry struct {
	Name           string   `json:"name"`
	PhysicalKey    string   `json:"physical_key"`
	Owner          string   `json:"owner"`
	Categories     []string `json:"categories"`
	Status         string   `json:"status"`
	SafeOperations []string `json:"safe_operations"`
	ToolID         string   `json:"tool_id,omitempty"`
	Sensitivity    string   `json:"sensitivity"`
	Reason         string   `json:"reason,omitempty"`
}

type Catalog struct{ entries map[string]Entry }

var storeDefinitions = []Entry{
	{Name: "conversation_l1", Owner: "RenCrow_CORE Conversation", Categories: []string{"conversation", "memory", "knowledge", "news", "audit"}, Status: "restricted", SafeOperations: []string{"recall_pack"}, Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "conversation_archive", Owner: "RenCrow_CORE Conversation Archive", Categories: []string{"conversation", "memory", "knowledge", "news"}, Status: "restricted", SafeOperations: []string{"recall_pack"}, Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "tool_registry", Owner: "RenCrow_CORE Runtime Capability", Categories: []string{"tool_metadata"}, Status: "restricted", SafeOperations: []string{"runtime_snapshot"}, Sensitivity: "internal", Reason: "use_runtime_capability_snapshot"},
	{Name: "glossary", Owner: "RenCrow_CORE Glossary", Categories: []string{"term", "definition"}, SafeOperations: []string{"define_term", "list_category"}, ToolID: "glossary.lookup", Sensitivity: "normal"},
	{Name: "movie_catalog", Owner: "RenCrow_CORE Movie Catalog", Categories: []string{"movie", "person", "credit"}, SafeOperations: []string{"lookup"}, ToolID: "movie_catalog.lookup", Sensitivity: "normal"},
	{Name: "hobby_graph", Owner: "RenCrow_CORE Hobby Graph", Categories: []string{"drama", "award", "music", "anime", "novel", "manga"}, SafeOperations: []string{"person_related_lookup", "music_lookup", "lyrics_rights_lookup", "lyrics_syntax_lookup", "licensed_lyrics_lookup"}, ToolID: "person_related_catalog.lookup", Sensitivity: "mixed", Reason: "deployment_or_index_unavailable"},
	{Name: "investment", Owner: "RenCrow_TRADE Investment Projection", Categories: []string{"investment", "snapshot", "event"}, SafeOperations: []string{"validated_l1_projection"}, Sensitivity: "financial", Reason: "deployment_unavailable"},
	{Name: "advisor", Owner: "RenCrow_CORE Advisor", Categories: []string{"advisor", "policy"}, Status: "restricted", SafeOperations: []string{"advice_runs"}, Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "sandbox", Owner: "RenCrow_CORE Sandbox", Categories: []string{"execution"}, Status: "restricted", SafeOperations: []string{"sandboxes"}, Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "dci", Owner: "RenCrow_CORE DCI", Categories: []string{"search_trace", "evidence"}, Status: "restricted", SafeOperations: []string{"search_traces"}, Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "skill_governance", Owner: "RenCrow_CORE Skill Governance", Categories: []string{"skill", "governance", "audit"}, Status: "restricted", SafeOperations: []string{"skill_manifests"}, Sensitivity: "internal", Reason: "use_runtime_capability_snapshot"},
	{Name: "workstream", Owner: "RenCrow_CORE Workstream", Categories: []string{"workstream", "goal", "artifact"}, Status: "restricted", SafeOperations: []string{"goals"}, Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "revenue", Owner: "RenCrow_CORE Revenue", Categories: []string{"revenue", "opportunity", "delivery"}, Status: "restricted", SafeOperations: []string{"opportunities"}, Sensitivity: "commercial", Reason: "owner_service_only"},
	{Name: "persona_architecture", Owner: "RenCrow_CORE Persona Architecture", Categories: []string{"persona", "observation"}, Status: "restricted", SafeOperations: []string{"canonical_responses"}, Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "browser_trace_to_api", Owner: "RenCrow_CORE Browser Trace", Categories: []string{"browser_trace", "api_candidate"}, Status: "restricted", SafeOperations: []string{"validated_candidates"}, Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "complexity_hotspot", Owner: "RenCrow_CORE Complexity", Categories: []string{"complexity", "evidence"}, Status: "restricted", SafeOperations: []string{"hotspots"}, Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "super_agent_harness", Owner: "RenCrow_CORE SuperAgent Harness", Categories: []string{"agent_run", "trace"}, Status: "restricted", SafeOperations: []string{"agent_runs"}, Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "ai_workflow", Owner: "RenCrow_CORE AI Workflow", Categories: []string{"workflow", "command", "worktree"}, Status: "restricted", SafeOperations: []string{"command_registry"}, Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "knowledge_memory", Owner: "RenCrow_CORE Knowledge Memory", Categories: []string{"knowledge", "personal_archive"}, Sensitivity: "mixed"},
	{Name: "durable_store_workflow", Owner: "RenCrow_CORE Durable Store", Categories: []string{"workflow", "receipt"}, Status: "restricted", SafeOperations: []string{"exact_request"}, Sensitivity: "internal", Reason: "owner_service_only"},
}

func KnownStoreKeys() []string {
	out := make([]string, 0, len(storeDefinitions))
	for _, entry := range storeDefinitions {
		out = append(out, entry.Name)
	}
	return out
}

func Build(states map[string]StoreState) *Catalog {
	entries := make(map[string]Entry, len(storeDefinitions))
	for _, definition := range storeDefinitions {
		entry := definition
		entry.PhysicalKey = "storage.databases." + entry.Name
		state := states[entry.Name]
		if entry.Name == "knowledge_memory" {
			applyKnowledgeMemoryState(&entry, state)
			entries[entry.Name] = entry
			continue
		}
		if entry.Status == "" {
			if entry.Name == "investment" && state.RecallReady {
				entry.Status = "available"
				entry.Reason = ""
			} else if state.Configured && state.Exists && (entry.Name == "glossary" || entry.Name == "movie_catalog" || entry.Name == "hobby_graph") {
				entry.Status = "available"
				entry.Reason = ""
			} else {
				entry.Status = "unavailable"
				if !state.Configured {
					entry.Reason = "not_configured"
				} else if !state.Exists {
					entry.Reason = "database_unavailable"
				}
			}
		}
		entries[entry.Name] = entry
	}
	return &Catalog{entries: entries}
}

func applyKnowledgeMemoryState(entry *Entry, state StoreState) {
	entry.Status = KnowledgeMemoryStatusUnavailable
	entry.SafeOperations = nil
	entry.ToolID = ""
	entry.Reason = KnowledgeMemoryReasonDatabaseUnavailable
	if state.KnowledgeMemory == nil {
		if !state.Configured {
			return
		}
		if !state.Exists {
			return
		}
		entry.Status = KnowledgeMemoryStatusBlocked
		entry.Reason = KnowledgeMemoryReasonSchemaMissing
		return
	}
	capability := *state.KnowledgeMemory
	if !capability.Configured || !capability.DatabaseAvailable {
		return
	}
	if !capability.SchemaReady || !capability.IndexReady {
		entry.Status = KnowledgeMemoryStatusBlocked
		entry.Reason = KnowledgeMemoryReasonSchemaMissing
		return
	}
	if capability.CoverageState != KnowledgeMemoryCoverageReady {
		entry.Status = KnowledgeMemoryStatusBlocked
		entry.Reason = KnowledgeMemoryReasonIndexing
		return
	}
	if capability.IntegrityState != KnowledgeMemoryIntegrityReady {
		entry.Status = KnowledgeMemoryStatusBlocked
		entry.Reason = KnowledgeMemoryReasonIntegrityFailed
		return
	}
	if !capability.ToolReady {
		entry.Status = KnowledgeMemoryStatusBlocked
		entry.Reason = KnowledgeMemoryReasonToolUnavailable
		return
	}
	entry.Status = KnowledgeMemoryStatusAvailable
	entry.ToolID = KnowledgeMemoryToolID
	entry.SafeOperations = []string{KnowledgeMemoryOperationPublicSearch}
	if capability.PrivateScopeReady {
		entry.SafeOperations = append(entry.SafeOperations, KnowledgeMemoryOperationUserPrivateSearch)
		entry.Reason = ""
		return
	}
	entry.Reason = KnowledgeMemoryReasonScopeUnavailable
}

func (c *Catalog) ListAvailable() []Entry {
	out := []Entry{}
	if c == nil {
		return out
	}
	for _, entry := range c.entries {
		if entry.Status == "available" {
			out = append(out, entry)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (c *Catalog) Describe(name string) (Entry, error) {
	name = strings.TrimSpace(name)
	if c == nil || name == "" {
		return Entry{}, fmt.Errorf("data capability name is required")
	}
	entry, ok := c.entries[name]
	if !ok {
		return Entry{}, fmt.Errorf("data capability %q is unknown", name)
	}
	return entry, nil
}

func (c *Catalog) All() []Entry {
	out := []Entry{}
	if c == nil {
		return out
	}
	for _, entry := range c.entries {
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
