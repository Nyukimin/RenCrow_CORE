package datacapability

import (
	"fmt"
	"sort"
	"strings"
)

type StoreState struct {
	Configured bool
	Exists     bool
}

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
	{Name: "conversation_l1", Owner: "RenCrow_CORE Conversation", Categories: []string{"conversation", "memory", "knowledge", "news", "audit"}, Status: "restricted", Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "conversation_archive", Owner: "RenCrow_CORE Conversation Archive", Categories: []string{"conversation", "memory", "knowledge", "news"}, Status: "restricted", Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "tool_registry", Owner: "RenCrow_CORE Runtime Capability", Categories: []string{"tool_metadata"}, Status: "restricted", SafeOperations: []string{"runtime_snapshot"}, Sensitivity: "internal", Reason: "use_runtime_capability_snapshot"},
	{Name: "glossary", Owner: "RenCrow_CORE Glossary", Categories: []string{"term", "definition"}, SafeOperations: []string{"define_term", "list_category"}, ToolID: "glossary.lookup", Sensitivity: "normal"},
	{Name: "movie_catalog", Owner: "RenCrow_CORE Movie Catalog", Categories: []string{"movie", "person", "credit"}, SafeOperations: []string{"lookup"}, ToolID: "movie_catalog.lookup", Sensitivity: "normal"},
	{Name: "hobby_graph", Owner: "RenCrow_CORE Hobby Graph", Categories: []string{"hobby", "item", "relation"}, Sensitivity: "mixed", Reason: "deployment_or_index_unavailable"},
	{Name: "investment", Owner: "RenCrow_CORE Investment Projection", Categories: []string{"investment", "snapshot", "event"}, Sensitivity: "financial", Reason: "deployment_unavailable"},
	{Name: "advisor", Owner: "RenCrow_CORE Advisor", Categories: []string{"advisor", "policy"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "sandbox", Owner: "RenCrow_CORE Sandbox", Categories: []string{"execution"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "dci", Owner: "RenCrow_CORE DCI", Categories: []string{"search_trace", "evidence"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "skill_governance", Owner: "RenCrow_CORE Skill Governance", Categories: []string{"skill", "governance", "audit"}, Status: "restricted", Sensitivity: "internal", Reason: "use_runtime_capability_snapshot"},
	{Name: "workstream", Owner: "RenCrow_CORE Workstream", Categories: []string{"workstream", "goal", "artifact"}, Status: "restricted", Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "revenue", Owner: "RenCrow_CORE Revenue", Categories: []string{"revenue", "opportunity", "delivery"}, Status: "restricted", Sensitivity: "commercial", Reason: "owner_service_only"},
	{Name: "persona_architecture", Owner: "RenCrow_CORE Persona Architecture", Categories: []string{"persona", "observation"}, Status: "restricted", Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "browser_trace_to_api", Owner: "RenCrow_CORE Browser Trace", Categories: []string{"browser_trace", "api_candidate"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "complexity_hotspot", Owner: "RenCrow_CORE Complexity", Categories: []string{"complexity", "evidence"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "super_agent_harness", Owner: "RenCrow_CORE SuperAgent Harness", Categories: []string{"agent_run", "trace"}, Status: "restricted", Sensitivity: "private", Reason: "owner_service_only"},
	{Name: "ai_workflow", Owner: "RenCrow_CORE AI Workflow", Categories: []string{"workflow", "command", "worktree"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
	{Name: "knowledge_memory", Owner: "RenCrow_CORE Knowledge Memory", Categories: []string{"knowledge", "personal_archive"}, Status: "blocked", Sensitivity: "mixed", Reason: "full_scan_policy"},
	{Name: "durable_store_workflow", Owner: "RenCrow_CORE Durable Store", Categories: []string{"workflow", "receipt"}, Status: "restricted", Sensitivity: "internal", Reason: "owner_service_only"},
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
		if entry.Status == "" {
			if state.Configured && state.Exists && (entry.Name == "glossary" || entry.Name == "movie_catalog") {
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
