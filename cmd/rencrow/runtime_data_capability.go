package main

import (
	"context"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/datacapability"
)

type runtimeDataCapabilityCatalog struct{ catalog *datacapability.Catalog }

func (c *runtimeDataCapabilityCatalog) Entries(context.Context) ([]datacapability.Entry, error) {
	if c == nil || c.catalog == nil {
		return nil, nil
	}
	return c.catalog.All(), nil
}

func (c *runtimeDataCapabilityCatalog) Execute(operation, name string) (any, error) {
	if operation == "list_catalog" {
		return c.catalog.All(), nil
	}
	if operation == "list_available" {
		return c.catalog.ListAvailable(), nil
	}
	return c.catalog.Describe(name)
}

func buildRuntimeDataCapabilityCatalog(cfg *config.Config, glossaryReady bool, movieReady bool, hobbyReady ...bool) *runtimeDataCapabilityCatalog {
	return buildRuntimeDataCapabilityCatalogWithKnowledgeState(cfg, glossaryReady, movieReady, hobbyReady, nil)
}

func buildRuntimeDataCapabilityCatalogWithKnowledgeState(cfg *config.Config, glossaryReady bool, movieReady bool, hobbyReady []bool, knowledgeState *datacapability.KnowledgeMemoryState) *runtimeDataCapabilityCatalog {
	paths := map[string]string{}
	if cfg != nil {
		d := cfg.Storage.Databases
		paths = map[string]string{
			"conversation_l1": d.ConversationL1, "conversation_archive": d.ConversationArchive, "tool_registry": d.ToolRegistry, "glossary": d.Glossary, "movie_catalog": d.MovieCatalog, "hobby_graph": d.HobbyGraph, "investment": d.Investment, "advisor": d.Advisor, "sandbox": d.Sandbox, "dci": d.DCI, "skill_governance": d.SkillGovernance, "workstream": d.Workstream, "revenue": d.Revenue, "persona_architecture": d.PersonaArchitecture, "browser_trace_to_api": d.BrowserTraceToAPI, "complexity_hotspot": d.ComplexityHotspot, "super_agent_harness": d.SuperAgentHarness, "ai_workflow": d.AIWorkflow, "knowledge_memory": d.KnowledgeMemory, "durable_store_workflow": d.DurableStoreWorkflow,
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
	l1State := states["conversation_l1"]
	investmentState := states["investment"]
	investmentState.RecallReady = l1State.Configured && l1State.Exists
	states["investment"] = investmentState
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
