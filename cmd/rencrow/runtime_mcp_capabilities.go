package main

import (
	"context"

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
	ListTools(context.Context) ([]string, error)
	CallTool(context.Context, string, map[string]any) (string, error)
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
	remoteNames, err := client.ListTools(ctx)
	if err != nil {
		client.Stop()
		return unavailableSerenaMCPRuntime(serenaMCPStateUnavailable, "Serena MCP Tool一覧を取得できません")
	}
	catalog := toolsinfra.NewMCPToolCatalog("serena", client, remoteNames)
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
