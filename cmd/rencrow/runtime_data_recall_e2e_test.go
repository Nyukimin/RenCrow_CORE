package main

import (
	"context"
	"errors"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

func TestOperationalDataRecallE2E_AllOwnerAdaptersThroughWorkerTool(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	registrations := []error{
		registerRuntimeDataRecallAdvisor(registry, &dataRecallAdvisorListerStub{}),
		registerRuntimeDataRecallSandbox(registry, &dataRecallSandboxListerStub{}),
		registerRuntimeDataRecallDCI(registry, &dataRecallDCIListerStub{}),
		registerRuntimeDataRecallSkillGovernance(registry, &dataRecallSkillGovernanceListerStub{}),
		registerRuntimeDataRecallWorkstream(registry, &dataRecallWorkstreamListerStub{}),
		registerRuntimeDataRecallRevenue(registry, &dataRecallRevenueListerStub{}),
		registerRuntimeDataRecallPersonaArchitecture(registry, &dataRecallPersonaListerStub{}),
		registerRuntimeDataRecallBrowserTraceToAPI(registry, &dataRecallBrowserListerStub{}),
		registerRuntimeDataRecallComplexityHotspot(registry, &dataRecallComplexityListerStub{}),
		registerRuntimeDataRecallSuperAgentHarness(registry, &dataRecallSuperAgentListerStub{}),
		registerRuntimeDataRecallAIWorkflow(registry, &dataRecallAIWorkflowListerStub{}),
		registerRuntimeDataRecallDurableStoreWorkflow(registry, &dataRecallDurableStoreStub{}),
	}
	for i, err := range registrations {
		if err != nil {
			t.Fatalf("registration %d: %v", i, err)
		}
	}

	worker := tools.NewToolRunner(tools.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	routes := []struct {
		store, operation string
		user             bool
	}{
		{"advisor", "advice_runs", false}, {"sandbox", "sandboxes", false}, {"dci", "search_traces", false},
		{"skill_governance", "skill_manifests", false}, {"workstream", "goals", true}, {"revenue", "opportunities", false},
		{"persona_architecture", "canonical_responses", true}, {"browser_trace_to_api", "validated_candidates", false},
		{"complexity_hotspot", "hotspots", false}, {"super_agent_harness", "agent_runs", false},
		{"ai_workflow", "command_registry", false}, {"durable_store_workflow", "exact_request", true},
	}
	for _, route := range routes {
		ctx := dataRecallInternalContext(t)
		if route.user {
			ctx = dataRecallUserContext(t)
		}
		response, err := worker.ExecuteV2(ctx, "data.recall", map[string]any{"store": route.store, "operation": route.operation, "query": "e2e", "limit": 1})
		if err != nil || response == nil || response.IsError() {
			t.Fatalf("%s/%s response=%#v err=%v", route.store, route.operation, response, err)
		}
		result, ok := response.Result.(runtimeDataRecallResult)
		if !ok || result.Store != route.store || result.Operation != route.operation || result.Records == nil {
			t.Fatalf("%s/%s result=%#v", route.store, route.operation, response.Result)
		}
	}
}

func TestOperationalDataRecallE2E_ChatAndScopeBoundariesFailClosed(t *testing.T) {
	registry := newRuntimeDataRecallRegistry()
	if err := registerRuntimeDataRecallAdvisor(registry, &dataRecallAdvisorListerStub{}); err != nil {
		t.Fatal(err)
	}
	worker := tools.NewToolRunner(tools.ToolRunnerConfig{OperationalDataRecall: registry, DisableToolHarness: true})
	chat := tools.NewToolRunner(tools.ToolRunnerConfig{DisableToolHarness: true})
	args := map[string]any{"store": "advisor", "operation": "advice_runs", "query": "e2e"}
	if response, err := worker.ExecuteV2(dataRecallUserContext(t), "data.recall", args); err != nil || response == nil || !response.IsError() {
		t.Fatalf("user scope must be denied response=%#v err=%v", response, err)
	}
	if response, err := worker.ExecuteV2(context.Background(), "data.recall", args); err != nil || response == nil || !response.IsError() {
		t.Fatalf("missing scope must be denied response=%#v err=%v", response, err)
	}
	if _, err := chat.ExecuteV2(dataRecallInternalContext(t), "data.recall", args); !errors.Is(err, tools.ErrUnknownTool) {
		t.Fatalf("chat error=%v", err)
	}
}
