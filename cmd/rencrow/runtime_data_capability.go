package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
)

type runtimeDataCapabilityCatalog struct {
	catalog        *datacapability.Catalog
	recallRegistry *runtimeDataRecallRegistry
	writeRegistry  *runtimeDataWriteRegistry
}

// renderRuntimeDataRouteContext projects the actual registries into the
// stable Agent context. A route absent here is unavailable, regardless of any
// static catalog text.
func renderRuntimeDataRouteContext(recall *runtimeDataRecallRegistry, write *runtimeDataWriteRegistry) string {
	var lines []string
	lines = append(lines,
		"## Operational Data Route Snapshot",
		"Only the routes listed below are executable through data.recall/data.write. A route absent here is unavailable. Use data_capability.describe for the current machine-readable contract.",
	)
	for _, route := range recall.Snapshot() {
		lines = append(lines, fmt.Sprintf("- data.recall %s/%s access=%s request=query,limit", route.Store, route.Operation, route.Access))
	}
	for _, route := range write.Snapshot() {
		line := fmt.Sprintf("- data.write %s/%s access=%s", route.Store, route.Operation, route.Access)
		if len(route.RequiredPayloadFields) > 0 {
			line += " required=" + strings.Join(route.RequiredPayloadFields, ",")
		}
		if len(route.OptionalPayloadFields) > 0 {
			line += " optional=" + strings.Join(route.OptionalPayloadFields, ",")
		}
		lines = append(lines, line)
	}
	if len(lines) == 2 {
		lines = append(lines, "- No operational data routes are registered.")
	}
	return strings.Join(lines, "\n")
}

func combineRuntimeCapabilityContexts(parts ...string) string {
	combined := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			combined = append(combined, part)
		}
	}
	return strings.Join(combined, "\n\n")
}

func (c *runtimeDataCapabilityCatalog) BindRouteRegistries(recall *runtimeDataRecallRegistry, write *runtimeDataWriteRegistry) {
	if c == nil {
		return
	}
	c.recallRegistry = recall
	c.writeRegistry = write
}

func (c *runtimeDataCapabilityCatalog) Entries(context.Context) ([]datacapability.Entry, error) {
	if c == nil || c.catalog == nil {
		return nil, nil
	}
	return c.decorateEntries(c.catalog.All()), nil
}

func (c *runtimeDataCapabilityCatalog) Execute(operation, name string) (any, error) {
	if operation == "list_catalog" {
		return c.decorateEntries(c.catalog.All()), nil
	}
	if operation == "list_available" {
		entries := c.decorateEntries(c.catalog.All())
		available := make([]datacapability.Entry, 0, len(entries))
		for _, entry := range entries {
			if entry.Status == "available" {
				available = append(available, entry)
			}
		}
		return available, nil
	}
	entry, err := c.catalog.Describe(name)
	if err != nil {
		return nil, err
	}
	return c.decorateEntry(entry), nil
}

func (c *runtimeDataCapabilityCatalog) decorateEntries(entries []datacapability.Entry) []datacapability.Entry {
	out := make([]datacapability.Entry, 0, len(entries))
	for _, entry := range entries {
		out = append(out, c.decorateEntry(entry))
	}
	return out
}

func (c *runtimeDataCapabilityCatalog) decorateEntry(entry datacapability.Entry) datacapability.Entry {
	entry.RecallRoutes = []datacapability.Route{}
	entry.WriteRoutes = []datacapability.Route{}
	if c == nil {
		return entry
	}
	for _, route := range c.recallRegistry.Snapshot() {
		if route.Store == entry.Name {
			entry.RecallRoutes = append(entry.RecallRoutes, datacapability.Route{Operation: route.Operation, ToolID: "data.recall", Access: string(route.Access)})
		}
	}
	for _, route := range c.writeRegistry.Snapshot() {
		if route.Store == entry.Name {
			entry.WriteRoutes = append(entry.WriteRoutes, datacapability.Route{
				Operation: route.Operation, ToolID: "data.write", Access: string(route.Access),
				RequiredFields: append([]string(nil), route.RequiredPayloadFields...),
				OptionalFields: append([]string(nil), route.OptionalPayloadFields...),
			})
		}
	}
	// Availability is an executable contract, not a static catalog label. A
	// memory source is available to RenCrow only when its Owner read and write
	// routes are both registered in this process.
	switch {
	case len(entry.RecallRoutes) > 0 && len(entry.WriteRoutes) > 0:
		entry.Status = "available"
		entry.Reason = ""
	case len(entry.RecallRoutes) > 0 || len(entry.WriteRoutes) > 0:
		entry.Status = "blocked"
		entry.Reason = "owner_route_incomplete"
	case entry.Status == "available" || entry.Status == "restricted":
		entry.Status = "blocked"
		entry.Reason = "owner_route_unavailable"
	}
	return entry
}

func buildRuntimeDataCapabilityCatalog(cfg *config.Config, glossaryReady bool, movieReady bool, hobbyReady ...bool) *runtimeDataCapabilityCatalog {
	return buildRuntimeDataCapabilityCatalogWithKnowledgeState(cfg, glossaryReady, movieReady, hobbyReady, nil)
}

func buildRuntimeDataCapabilityCatalogWithKnowledgeState(cfg *config.Config, glossaryReady bool, movieReady bool, hobbyReady []bool, knowledgeState *datacapability.KnowledgeMemoryState) *runtimeDataCapabilityCatalog {
	paths := map[string]string{}
	if cfg != nil {
		d := cfg.Storage.Databases
		paths = map[string]string{
			"conversation_l1": d.ConversationL1, "conversation_archive": d.ConversationArchive, "tool_registry": d.ToolRegistry, "glossary": d.Glossary, "movie_catalog": d.MovieCatalog, "hobby_graph": d.HobbyGraph, "advisor": d.Advisor, "sandbox": d.Sandbox, "dci": d.DCI, "skill_governance": d.SkillGovernance, "workstream": d.Workstream, "revenue": d.Revenue, "persona_architecture": d.PersonaArchitecture, "browser_trace_to_api": d.BrowserTraceToAPI, "complexity_hotspot": d.ComplexityHotspot, "super_agent_harness": d.SuperAgentHarness, "ai_workflow": d.AIWorkflow, "knowledge_memory": d.KnowledgeMemory, "durable_store_workflow": d.DurableStoreWorkflow,
		}
	}
	states := map[string]datacapability.StoreState{}
	for _, key := range datacapability.KnownStoreKeys() {
		path := strings.TrimSpace(paths[key])
		exists := false
		if path != "" {
			if info, err := os.Stat(path); err == nil && !info.IsDir() {
				exists = true
			}
		}
		states[key] = datacapability.StoreState{Configured: path != "", Exists: exists}
	}
	if knowledgeState != nil {
		state := states["knowledge_memory"]
		state.KnowledgeMemory = knowledgeState
		states["knowledge_memory"] = state
	}
	if state := states["glossary"]; state.Configured {
		state.Exists = glossaryReady
		states["glossary"] = state
	}
	if state := states["movie_catalog"]; state.Configured {
		state.Exists = movieReady
		states["movie_catalog"] = state
	}
	if state := states["hobby_graph"]; state.Configured {
		ready := false
		if len(hobbyReady) > 0 {
			ready = hobbyReady[0]
		}
		state.Exists = ready
		states["hobby_graph"] = state
	}
	return &runtimeDataCapabilityCatalog{catalog: datacapability.Build(states)}
}
