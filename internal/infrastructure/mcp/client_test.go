package mcp

import (
	"context"
	"testing"
)

func TestNewMCPClient(t *testing.T) {
	client := NewMCPClient()

	if client == nil {
		t.Fatal("NewMCPClient should not return nil")
	}
}

func TestMCPClient_RegisterServer(t *testing.T) {
	client := NewMCPClient()

	config := ServerConfig{
		Name:    "test-server",
		Command: "node",
		Args:    []string{"server.js"},
	}

	err := client.RegisterServer(config)
	if err != nil {
		t.Fatalf("RegisterServer failed: %v", err)
	}
}

func TestMCPClient_RegisterServer_Duplicate(t *testing.T) {
	client := NewMCPClient()

	config := ServerConfig{
		Name:    "test-server",
		Command: "node",
		Args:    []string{"server.js"},
	}

	client.RegisterServer(config)

	// 重複登録はエラー
	err := client.RegisterServer(config)
	if err == nil {
		t.Error("Expected error when registering duplicate server")
	}
}

func TestMCPClient_ListServers(t *testing.T) {
	client := NewMCPClient()

	config1 := ServerConfig{
		Name:    "server1",
		Command: "node",
		Args:    []string{"s1.js"},
	}
	config2 := ServerConfig{
		Name:    "server2",
		Command: "python",
		Args:    []string{"s2.py"},
	}

	client.RegisterServer(config1)
	client.RegisterServer(config2)

	servers := client.ListServers()

	if len(servers) != 2 {
		t.Errorf("Expected 2 servers, got %d", len(servers))
	}

	found1, found2 := false, false
	for _, s := range servers {
		if s == "server1" {
			found1 = true
		}
		if s == "server2" {
			found2 = true
		}
	}

	if !found1 || !found2 {
		t.Error("Not all servers found in list")
	}
}

func TestMCPClient_ListTools_ServerNotRegistered(t *testing.T) {
	client := NewMCPClient()

	_, err := client.ListTools(context.Background(), "nonexistent-server")
	if err == nil {
		t.Error("Expected error for non-existent server")
	}
}

func TestMCPClient_CallTool_ServerNotRegistered(t *testing.T) {
	client := NewMCPClient()

	args := map[string]interface{}{"key": "value"}

	_, err := client.CallTool(context.Background(), "nonexistent-server", "some-tool", args)
	if err == nil {
		t.Error("Expected error for non-existent server")
	}
}

// Mock MCP Server for testing
func TestMCPClient_WithMockServer_ListTools(t *testing.T) {
	t.Skip("Skipping integration test - requires mock MCP server")

	client := NewMCPClient()

	// Mock serverの登録（実際のテストではモックサーバーが必要）
	config := ServerConfig{
		Name:    "mock-server",
		Command: "node",
		Args:    []string{"mock-mcp-server.js"},
	}

	client.RegisterServer(config)

	tools, err := client.ListTools(context.Background(), "mock-server")
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}

	if len(tools) == 0 {
		t.Error("Expected at least one tool from mock server")
	}
}

func TestMCPClient_WithMockServer_CallTool(t *testing.T) {
	t.Skip("Skipping integration test - requires mock MCP server")

	client := NewMCPClient()

	config := ServerConfig{
		Name:    "mock-server",
		Command: "node",
		Args:    []string{"mock-mcp-server.js"},
	}

	client.RegisterServer(config)

	args := map[string]interface{}{
		"message": "test",
	}

	result, err := client.CallTool(context.Background(), "mock-server", "echo", args)
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}

	if result == "" {
		t.Error("Expected non-empty result from tool call")
	}
}

func TestMCPClient_UnregisterServer(t *testing.T) {
	client := NewMCPClient()

	config := ServerConfig{
		Name:    "test-server",
		Command: "node",
		Args:    []string{"server.js"},
	}

	client.RegisterServer(config)

	err := client.UnregisterServer("test-server")
	if err != nil {
		t.Fatalf("UnregisterServer failed: %v", err)
	}

	// 削除後はリストに含まれない
	servers := client.ListServers()
	for _, s := range servers {
		if s == "test-server" {
			t.Error("Server should not be in list after unregister")
		}
	}
}

func TestMCPClient_UnregisterServer_NotFound(t *testing.T) {
	client := NewMCPClient()

	err := client.UnregisterServer("nonexistent-server")
	if err == nil {
		t.Error("Expected error when unregistering non-existent server")
	}
}

func TestServerConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  ServerConfig
		wantErr bool
	}{
		{
			name: "Valid config",
			config: ServerConfig{
				Name:    "test",
				Command: "node",
				Args:    []string{"server.js"},
			},
			wantErr: false,
		},
		{
			name: "Missing name",
			config: ServerConfig{
				Name:    "",
				Command: "node",
				Args:    []string{"server.js"},
			},
			wantErr: true,
		},
		{
			name: "Missing command",
			config: ServerConfig{
				Name:    "test",
				Command: "",
				Args:    []string{"server.js"},
			},
			wantErr: true,
		},
		{
			name: "Empty args is ok",
			config: ServerConfig{
				Name:    "test",
				Command: "node",
				Args:    []string{},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
