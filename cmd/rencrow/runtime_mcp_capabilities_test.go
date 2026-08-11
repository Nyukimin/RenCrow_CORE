package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/service"
)

type serenaRuntimeClientStub struct {
	startErr  error
	listErr   error
	toolNames []string
	result    string
	callErr   error
	starts    int
	lists     int
	stops     int
	calls     int
	name      string
	args      map[string]any
}

func (s *serenaRuntimeClientStub) Start(context.Context) error {
	s.starts++
	return s.startErr
}

func (s *serenaRuntimeClientStub) Stop() {
	s.stops++
}

func (s *serenaRuntimeClientStub) ListTools(context.Context) ([]string, error) {
	s.lists++
	return append([]string(nil), s.toolNames...), s.listErr
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
		toolNames: []string{"replace_symbol", "find.symbol", "find.symbol"},
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

	worker := service.NewWorkerExecutionService(config.WorkerConfig{Workspace: "/workspace"})
	worker.SetMCPToolCaller(runtime.client)
	results, err := worker.ExecuteObservation(context.Background(), []service.ObservationAction{{
		Action: "mcp_tool",
		Target: "find.symbol",
		Args:   map[string]any{"name_path": "Foo"},
	}})
	if err != nil || len(results) != 1 || results[0].Status != "ok" || results[0].Output != "remote result" {
		t.Fatalf("worker observation results=%#v err=%v", results, err)
	}
	if client.calls != 1 || client.name != "find.symbol" || client.args["name_path"] != "Foo" {
		t.Fatalf("worker did not reuse Serena client: calls=%d name=%q args=%#v", client.calls, client.name, client.args)
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
	toolRuntime := buildToolRuntimeWithCapabilities(cfg, nil, nil, nil, nil, runtime.catalog)
	workerMetas, err := toolRuntime.WorkerRunnerV2.ListTools(context.Background())
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
	chatMetas, err := toolRuntime.ChatRunnerV2.ListTools(context.Background())
	if err != nil || hasToolMetadata(chatMetas, "mcp.serena.find_symbol") {
		t.Fatalf("Chat metadata=%#v err=%v", chatMetas, err)
	}
	if _, err := toolRuntime.ChatRunnerV2.ExecuteV2(context.Background(), "mcp.serena.find_symbol", nil); err == nil {
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
