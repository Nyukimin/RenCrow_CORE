package main

import (
	"context"
	"errors"
	"fmt"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
)

type serenaRuntimeClientStub struct {
	startErr               error
	listErr                error
	toolNames              []string
	definitions            []domaintool.MCPToolDefinition
	result                 string
	callErr                error
	generation             uint64
	changeGenerationOnList bool
	starts                 int
	lists                  int
	stops                  int
	calls                  int
	name                   string
	args                   map[string]any
}

func (s *serenaRuntimeClientStub) Start(context.Context) error {
	s.starts++
	return s.startErr
}

func (s *serenaRuntimeClientStub) Stop() {
	s.stops++
}

func (s *serenaRuntimeClientStub) ListTools(context.Context) ([]domaintool.MCPToolDefinition, error) {
	s.lists++
	if s.changeGenerationOnList {
		s.generation = 2
	}
	if s.definitions != nil {
		return s.definitions, s.listErr
	}
	return runtimeMCPFixtureDefinitions(s.toolNames), s.listErr
}

func (s *serenaRuntimeClientStub) CallTool(_ context.Context, name string, args map[string]any) (string, error) {
	s.calls++
	s.name = name
	s.args = args
	return s.result, s.callErr
}

func TestSerenaMCPRuntimeDisabledDoesNotConstructClient(t *testing.T) {
	factoryCalls := 0
	runtime := newSerenaMCPRuntime(context.Background(), false, "/workspace", func(string) serenaMCPClient {
		factoryCalls++
		return &serenaRuntimeClientStub{}
	})

	if runtime.state != serenaMCPStateDisabled || runtime.client != nil || runtime.catalog != nil {
		t.Fatalf("disabled runtime=%#v", runtime)
	}
	if factoryCalls != 0 {
		t.Fatalf("disabled runtime constructed client %d times", factoryCalls)
	}
	if len(runtime.observations) != 1 || runtime.observations[0].Available {
		t.Fatalf("disabled observation=%#v", runtime.observations)
	}
}

func TestSerenaMCPRuntimeStartupAndListingFailuresFailClosed(t *testing.T) {
	t.Run("startup failure", func(t *testing.T) {
		client := &serenaRuntimeClientStub{startErr: errors.New("start failed")}
		runtime := newSerenaMCPRuntime(context.Background(), true, "/workspace", func(string) serenaMCPClient {
			return client
		})
		assertUnavailableSerenaRuntime(t, runtime, "起動")
		if client.starts != 1 || client.lists != 0 || client.stops != 1 {
			t.Fatalf("lifecycle=%+v", client)
		}
	})

	t.Run("listing failure", func(t *testing.T) {
		client := &serenaRuntimeClientStub{listErr: errors.New("list failed")}
		runtime := newSerenaMCPRuntime(context.Background(), true, "/workspace", func(string) serenaMCPClient {
			return client
		})
		assertUnavailableSerenaRuntime(t, runtime, "一覧")
		if client.starts != 1 || client.lists != 1 || client.stops != 1 {
			t.Fatalf("lifecycle=%+v", client)
		}
	})
}

func TestSerenaMCPRuntimeObservesOnceAndReusesClientForWorkerService(t *testing.T) {
	client := &serenaRuntimeClientStub{
		toolNames: []string{"replace_symbol", "find_symbol", "find_symbol"},
		result:    "remote result",
	}
	var factoryWorkspace string
	runtime := newSerenaMCPRuntime(context.Background(), true, "/workspace", func(workspace string) serenaMCPClient {
		factoryWorkspace = workspace
		return client
	})

	if runtime.state != serenaMCPStateAvailable || runtime.client != client || runtime.catalog == nil {
		t.Fatalf("available runtime=%#v", runtime)
	}
	if factoryWorkspace != "/workspace" || client.starts != 1 || client.lists != 1 || client.stops != 0 {
		t.Fatalf("startup lifecycle workspace=%q client=%+v", factoryWorkspace, client)
	}
	if got := runtime.catalog.Entries(); len(got) != 2 || got[0].ToolID != "mcp.serena.find_symbol" || got[1].ToolID != "mcp.serena.replace_symbol" {
		t.Fatalf("catalog=%#v", got)
	}
	if len(runtime.observations) != 2 || runtime.observations[0].ExposedName != "mcp.serena.find_symbol" {
		t.Fatalf("observations=%#v", runtime.observations)
	}

	owner, executionCtx := runtimeToolOwnerFixture(t, "", "shiro")
	worker := service.NewWorkerExecutionService(config.WorkerConfig{Workspace: "/workspace"}, owner)
	toolRuntime := buildToolRuntimeWithCapabilities(owner, &config.Config{}, nil, nil, nil, nil, runtime.catalog, testCanonicalMediationStore(t))
	caller := &workerMCPObservationCaller{runner: toolRuntime.WorkerRuntimeRunnerV2, catalog: runtime.catalog}
	worker.SetMCPToolCaller(caller)
	results, err := worker.ExecuteObservation(executionCtx, []service.ObservationAction{{
		Action: "mcp_tool",
		Target: "find_symbol",
		Args:   map[string]any{"name_path": "Foo"},
	}})
	if err != nil || len(results) != 1 || results[0].Status != "ok" || results[0].Output != "remote result" {
		t.Fatalf("worker observation results=%#v err=%v", results, err)
	}
	if client.calls != 1 || client.name != "find_symbol" || client.args["name_path"] != "Foo" {
		t.Fatalf("worker did not reuse Serena client: calls=%d name=%q args=%#v", client.calls, client.name, client.args)
	}
	if _, err := caller.CallTool(executionCtx, "replace_symbol", nil); err == nil || client.calls != 1 {
		t.Fatalf("mutation accepted as observation: err=%v calls=%d", err, client.calls)
	}
	// The adapter cannot bypass the real runtime admission, even when called
	// outside the Worker's independent entry guard.
	if _, err := caller.CallTool(context.Background(), "find_symbol", nil); err == nil || client.calls != 1 {
		t.Fatalf("real runtime admission bypassed: err=%v calls=%d", err, client.calls)
	}
}

func TestSerenaMCPRuntimeStopsDuringDependenciesShutdown(t *testing.T) {
	client := &serenaRuntimeClientStub{}
	deps := &Dependencies{serenaMCPClient: client}
	deps.Shutdown()
	if client.stops != 1 {
		t.Fatalf("Serena Stop calls=%d, want 1", client.stops)
	}
}

func TestSerenaMCPRuntimeProjectsCanonicalNamesToSnapshotAndWorkerOnlyRunner(t *testing.T) {
	client := &serenaRuntimeClientStub{toolNames: []string{"find_symbol"}, result: "ok"}
	runtime := newSerenaMCPRuntime(context.Background(), true, "/workspace", func(string) serenaMCPClient {
		return client
	})
	cfg := &config.Config{ToolHarness: config.ToolHarnessConfig{}}
	owner, executionCtx := runtimeToolOwnerFixture(t, cfg.WorkspaceDir, "shiro")
	toolRuntime := buildToolRuntimeWithCapabilities(owner, cfg, nil, nil, nil, nil, runtime.catalog, testCanonicalMediationStore(t))
	workerMetas, err := toolRuntime.WorkerRuntimeRunnerV2.ListTools(context.Background())
	if err != nil || !hasToolMetadata(workerMetas, "mcp.serena.find_symbol") {
		t.Fatalf("Worker metadata=%#v err=%v", workerMetas, err)
	}
	definitionFound := false
	for _, definition := range toolRuntime.WorkerRunnerV2.ToolDefinitions() {
		if definition.Function.Name == "mcp.serena.find_symbol" {
			definitionFound = true
			break
		}
	}
	if !definitionFound {
		t.Fatal("Worker ToolDefinitions missing canonical Serena tool")
	}
	chatMetas, err := toolRuntime.ChatRuntimeRunnerV2.ListTools(context.Background())
	if err != nil || hasToolMetadata(chatMetas, "mcp.serena.find_symbol") {
		t.Fatalf("Chat metadata=%#v err=%v", chatMetas, err)
	}
	if _, err := toolRuntime.ChatRuntimeRunnerV2.ExecuteV2(executionCtx, "mcp.serena.find_symbol", nil); err == nil {
		t.Fatal("Chat runner must not execute Serena MCP")
	}
	contextText := buildRuntimeCapabilityContext(nil, nil, runtime.observations)
	if !strings.Contains(contextText, "利用可能: mcp.serena.find_symbol") {
		t.Fatalf("snapshot does not expose canonical Serena name:\n%s", contextText)
	}
}

func assertUnavailableSerenaRuntime(t *testing.T, runtime serenaMCPRuntime, reason string) {
	t.Helper()
	if runtime.state != serenaMCPStateUnavailable || runtime.client != nil || runtime.catalog != nil {
		t.Fatalf("runtime should be unavailable: %#v", runtime)
	}
	if len(runtime.observations) != 1 || runtime.observations[0].Available || !strings.Contains(runtime.reason, reason) {
		t.Fatalf("unavailable observation=%#v reason=%q", runtime.observations, runtime.reason)
	}
	contextText := buildRuntimeCapabilityContext(nil, nil, runtime.observations)
	if !strings.Contains(contextText, "利用不可: serena") || strings.Contains(contextText, "利用可能: serena") {
		t.Fatalf("unavailable Serena must not claim availability:\n%s", contextText)
	}
}

func TestSerenaMCPRuntimeObservationRequiresTaskOwner(t *testing.T) {
	client := &serenaRuntimeClientStub{result: "must not run"}
	worker := service.NewWorkerExecutionService(config.WorkerConfig{Workspace: t.TempDir()})
	worker.SetMCPToolCaller(client)
	results, err := worker.ExecuteObservation(context.Background(), []service.ObservationAction{{Action: "mcp_tool", Target: "find_symbol"}})
	if err == nil || client.calls != 0 || len(results) != 0 {
		t.Fatalf("ownerless observation reached MCP: error=%v calls=%d results=%#v", err, client.calls, results)
	}
}

type observationPolicyRunner struct {
	calls    int
	ctx      context.Context
	name     string
	args     map[string]any
	response *domaintool.ToolResponse
	err      error
	metas    []domaintool.ToolMetadata
	listErr  error
}

func (r *observationPolicyRunner) ListTools(context.Context) ([]domaintool.ToolMetadata, error) {
	return r.metas, r.listErr
}
func (r *observationPolicyRunner) ExecuteV2(ctx context.Context, name string, args map[string]any) (*domaintool.ToolResponse, error) {
	r.calls++
	r.ctx = ctx
	r.name = name
	r.args = args
	return r.response, r.err
}

func TestWorkerMCPObservationUsesCatalogAndRuntimePolicy(t *testing.T) {
	client := &serenaRuntimeClientStub{result: "must not be called directly"}
	catalog := toolsinfra.NewMCPToolCatalog("serena", client, runtimeMCPFixtureDefinitions([]string{"find symbol", "find.symbol"}), 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for _, tc := range []struct {
		name      string
		response  *domaintool.ToolResponse
		err       error
		wantError bool
	}{
		{"success", domaintool.NewSuccess("observed"), nil, false},
		{"typed denial", domaintool.NewError(domaintool.ErrValidationFailed, "policy refused", nil), nil, true},
		{"transport error", nil, errors.New("runtime unavailable"), true},
		{"nil result", nil, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &observationPolicyRunner{response: tc.response, err: tc.err, metas: []domaintool.ToolMetadata{{ToolID: "mcp.serena.find_symbol_2", Category: "query"}}}
			caller := &workerMCPObservationCaller{runner: runner, catalog: catalog}
			out, err := caller.CallTool(ctx, "find.symbol", map[string]any{"name_path": "Foo"})
			if (err != nil) != tc.wantError {
				t.Fatalf("out=%q err=%v", out, err)
			}
			if tc.wantError && out != "" {
				t.Fatal("failure output accepted")
			}
			if !tc.wantError && out != "observed" {
				t.Fatalf("output=%q", out)
			}
			if runner.calls != 1 || runner.ctx != ctx || runner.name != "mcp.serena.find_symbol_2" || runner.args["name_path"] != "Foo" {
				t.Fatalf("canonical mapping/context lost: %#v", runner)
			}
			if client.calls != 0 {
				t.Fatal("runtime policy bypassed")
			}
			if _, err := caller.CallTool(ctx, "find_symbol", nil); err == nil || runner.calls != 1 {
				t.Fatal("unobserved alias dispatched")
			}
		})
	}
	for _, caller := range []*workerMCPObservationCaller{nil, {}, {runner: &observationPolicyRunner{}}, {catalog: catalog}} {
		if _, err := caller.CallTool(ctx, "find.symbol", nil); err == nil {
			t.Fatal("unavailable runtime accepted")
		}
	}
}

func TestWorkerMCPObservationRequiresQueryMetadata(t *testing.T) {
	catalog := toolsinfra.NewMCPToolCatalog("serena", &serenaRuntimeClientStub{}, runtimeMCPFixtureDefinitions([]string{"read_file"}), 1)
	for _, tc := range []struct {
		name  string
		metas []domaintool.ToolMetadata
		err   error
	}{
		{"missing", nil, nil},
		{"mutation", []domaintool.ToolMetadata{{ToolID: "mcp.serena.read_file", Category: "mutation"}}, nil},
		{"unknown category", []domaintool.ToolMetadata{{ToolID: "mcp.serena.read_file"}}, nil},
		{"duplicate", []domaintool.ToolMetadata{{ToolID: "mcp.serena.read_file", Category: "query"}, {ToolID: "mcp.serena.read_file", Category: "query"}}, nil},
		{"list failure", nil, errors.New("catalog unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &observationPolicyRunner{metas: tc.metas, listErr: tc.err, response: domaintool.NewSuccess("must not run")}
			caller := &workerMCPObservationCaller{runner: runner, catalog: catalog}
			out, err := caller.CallTool(context.Background(), "read_file", nil)
			if err == nil || out != "" || runner.calls != 0 {
				t.Fatalf("unsafe metadata invoked: err=%v out=%q calls=%d", err, out, runner.calls)
			}
		})
	}
}

func (s *serenaRuntimeClientStub) ConnectionGeneration() uint64 {
	if s.generation == 0 {
		return 1
	}
	return s.generation
}
func (s *serenaRuntimeClientStub) CallToolAtGeneration(ctx context.Context, generation uint64, name string, args map[string]any) (string, error) {
	if generation == 0 || generation != s.ConnectionGeneration() {
		return "", fmt.Errorf("connection generation changed")
	}
	return s.CallTool(ctx, name, args)
}
func TestSerenaRuntimeRejectsReconnectDuringDiscovery(t *testing.T) {
	client := &serenaRuntimeClientStub{toolNames: []string{"read_file"}, changeGenerationOnList: true}
	runtime := newSerenaMCPRuntime(context.Background(), true, "/workspace", func(string) serenaMCPClient { return client })
	if runtime.state != serenaMCPStateUnavailable || client.stops != 1 {
		t.Fatalf("stale discovery available: %#v %+v", runtime, client)
	}
}

func runtimeMCPFixtureDefinitions(names []string) []domaintool.MCPToolDefinition {
	definitions := make([]domaintool.MCPToolDefinition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, domaintool.MCPToolDefinition{Name: name, InputSchema: map[string]any{"type": "object"}})
	}
	return definitions
}

func TestSerenaRuntimeRetainsMCPArgumentSchema(t *testing.T) {
	client := &serenaRuntimeClientStub{definitions: []domaintool.MCPToolDefinition{{Name: "read_file", Description: "Read source", InputSchema: map[string]any{"type": "object", "required": []any{"relative_path"}, "additionalProperties": false}}}}
	runtime := newSerenaMCPRuntime(context.Background(), true, "/workspace", func(string) serenaMCPClient { return client })
	if runtime.state != serenaMCPStateAvailable {
		t.Fatalf("startup: %#v", runtime)
	}
	runner := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{MCPToolCatalog: runtime.catalog})
	for _, definition := range runner.ToolDefinitions() {
		if definition.Function.Name == "mcp.serena.read_file" {
			schema := definition.Function.Parameters
			if schema["additionalProperties"] != false {
				t.Fatalf("startup schema replaced: %#v", schema)
			}
			return
		}
	}
	t.Fatal("observed MCP definition was not exposed to Worker")
}

func TestMCPCapabilityContextTracksRetirement(t *testing.T) {
	client := &serenaRuntimeClientStub{generation: 1, toolNames: []string{"read_file"}}
	runtime := newSerenaMCPRuntime(context.Background(), true, t.TempDir(), func(string) serenaMCPClient { return client })
	runner := toolsinfra.NewToolRunner(toolsinfra.ToolRunnerConfig{MCPToolCatalog: runtime.catalog})
	before := runtime.currentObservations()
	if len(before) != 1 || !before[0].Available {
		t.Fatalf("missing initial observation: %#v", before)
	}
	client.generation = 2
	after := runtime.currentObservations()
	if after[0].Available || !before[0].Available {
		t.Fatal("retirement ignored or historical observation mutated")
	}
	projected := runtimeCapabilityContextFromWorkerRunner(context.Background(), runner, nil, before)
	if !strings.Contains(projected, "Observed MCP tool is unavailable") {
		t.Fatalf("stale observation not reconciled: %s", projected)
	}
}
