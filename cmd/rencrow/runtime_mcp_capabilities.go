package main

import (
	"context"
	"fmt"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"

	mcpinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/mcp"
	toolsinfra "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/tools"
)

type serenaMCPState string

const (
	serenaMCPStateDisabled    serenaMCPState = "disabled"
	serenaMCPStateAvailable   serenaMCPState = "available"
	serenaMCPStateUnavailable serenaMCPState = "unavailable"
)

// serenaMCPClient is the lifecycle and call surface needed by the runtime.
// Tests provide a fake; production uses the existing SerenaClient.
type serenaMCPClient interface {
	Start(context.Context) error
	Stop()
	ListTools(context.Context) ([]domaintool.MCPToolDefinition, error)
	ConnectionGeneration() uint64
	CallToolAtGeneration(context.Context, uint64, string, map[string]any) (string, error)
}

type serenaMCPClientFactory func(workspace string) serenaMCPClient

type serenaMCPRuntime struct {
	client       serenaMCPClient
	catalog      *toolsinfra.MCPToolCatalog
	observations []runtimeMCPObservation
	state        serenaMCPState
	reason       string
}

func newSerenaMCPRuntime(
	ctx context.Context,
	enabled bool,
	workspace string,
	factory serenaMCPClientFactory,
) serenaMCPRuntime {
	if !enabled {
		return unavailableSerenaMCPRuntime(serenaMCPStateDisabled, "Serena MCPが無効化されています")
	}
	if factory == nil {
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCPクライアントが未構成です")
	}
	client := factory(workspace)
	if client == nil {
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCPクライアントが未構成です")
	}
	if err := client.Start(ctx); err != nil {
		client.Stop()
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCPの起動に失敗しました")
	}
	generation := client.ConnectionGeneration()
	definitions, err := client.ListTools(ctx)
	if err != nil {
		client.Stop()
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCP Tool一覧を取得できません")
	}
	if generation == 0 || client.ConnectionGeneration() != generation {
		client.Stop()
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCP connection changed during discovery")
	}
	catalog := toolsinfra.NewMCPToolCatalog("serena", client, definitions, generation)
	entries := catalog.Entries()
	if len(entries) == 0 {
		client.Stop()
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCP Toolが未観測です")
	}

	observations := make([]runtimeMCPObservation, 0, len(entries))
	for _, entry := range entries {
		observations = append(observations, runtimeMCPObservation{
			ServerName:  "serena",
			ToolName:    entry.RemoteName,
			ExposedName: entry.ToolID,
			Origin:      "serena",
			Available:   true,
		})
	}
	return serenaMCPRuntime{
		client:       client,
		catalog:      catalog,
		observations: observations,
		state:        serenaMCPStateAvailable,
	}
}

func unavailableSerenaMCPRuntime(state serenaMCPState, reason string) serenaMCPRuntime {
	return serenaMCPRuntime{
		observations: []runtimeMCPObservation{{
			ServerName: "serena",
			Origin:     "serena",
			Reason:     reason,
		}},
		state:  state,
		reason: reason,
	}
}

func productionSerenaMCPClientFactory(workspace string) serenaMCPClient {
	return mcpinfra.NewSerenaClient(workspace)
}

// workerMCPObservationCaller adapts the legacy observation name through the
// canonical startup catalog; policy and execution remain in the runtime runner.
type workerMCPObservationCaller struct {
	runner  domaintool.RunnerV2
	catalog *toolsinfra.MCPToolCatalog
}

func (c *workerMCPObservationCaller) CallTool(ctx context.Context, name string, args map[string]any) (string, error) {
	if c == nil || c.runner == nil || c.catalog == nil {
		return "", fmt.Errorf("Worker MCP observation runtime is unavailable")
	}
	for _, entry := range c.catalog.Entries() {
		if entry.RemoteName != name {
			continue
		}
		metas, err := c.runner.ListTools(ctx)
		if err != nil {
			return "", fmt.Errorf("MCP observation metadata unavailable: %w", err)
		}
		matches, query := 0, false
		for _, meta := range metas {
			if meta.ToolID == entry.ToolID {
				matches++
				query = meta.Category == "query"
			}
		}
		if matches != 1 || !query {
			return "", fmt.Errorf("MCP observation requires unambiguous query metadata: %s", entry.ToolID)
		}
		response, err := c.runner.ExecuteV2(ctx, entry.ToolID, args)
		if err != nil {
			return "", err
		}
		if response == nil {
			return "", fmt.Errorf("Worker MCP observation returned no result")
		}
		if response.Error != nil {
			return "", response.Error
		}
		return response.String(), nil
	}
	return "", fmt.Errorf("MCP observation tool is not in the startup catalog: %q", name)
}

func (r serenaMCPRuntime) currentObservations() []runtimeMCPObservation {
	observations := append([]runtimeMCPObservation(nil), r.observations...)
	if r.catalog == nil || r.catalog.Len() == 0 {
		for i := range observations {
			if observations[i].Available {
				observations[i].Available = false
				observations[i].Reason = "MCP observed connection is unavailable; rediscovery required"
			}
		}
	}
	return observations
}
