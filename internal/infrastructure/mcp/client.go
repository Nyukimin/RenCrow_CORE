package mcp

import (
	"context"
	"fmt"
	"sync"
)

// MCPClient はModel Context Protocolクライアント実装
type MCPClient struct {
	servers map[string]*ServerConfig
	mu      sync.RWMutex
}

// ServerConfig はMCPサーバー設定
type ServerConfig struct {
	Name    string   // サーバー名
	Command string   // 実行コマンド
	Args    []string // コマンド引数
	Env     []string // 環境変数（オプション）
}

// NewMCPClient は新しいMCPClientを作成
func NewMCPClient() *MCPClient {
	return &MCPClient{
		servers: make(map[string]*ServerConfig),
	}
}

// RegisterServer はMCPサーバーを登録
func (c *MCPClient) RegisterServer(config ServerConfig) error {
	if err := config.Validate(); err != nil {
		return fmt.Errorf("invalid server config: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.servers[config.Name]; exists {
		return fmt.Errorf("server '%s' already registered", config.Name)
	}

	c.servers[config.Name] = &config
	return nil
}

// UnregisterServer はMCPサーバーの登録を解除
func (c *MCPClient) UnregisterServer(serverName string) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if _, exists := c.servers[serverName]; !exists {
		return fmt.Errorf("server '%s' not found", serverName)
	}

	delete(c.servers, serverName)
	return nil
}

// ListServers は登録されているサーバー一覧を返す
func (c *MCPClient) ListServers() []string {
	c.mu.RLock()
	defer c.mu.RUnlock()

	servers := make([]string, 0, len(c.servers))
	for name := range c.servers {
		servers = append(servers, name)
	}
	return servers
}

// ListTools は指定されたサーバーの利用可能なツール一覧を返す
func (c *MCPClient) ListTools(ctx context.Context, serverName string) ([]string, error) {
	c.mu.RLock()
	config, exists := c.servers[serverName]
	c.mu.RUnlock()

	if !exists {
		return nil, fmt.Errorf("server '%s' not registered", serverName)
	}

	// NOTE: MCP完全実装（JSON-RPC over stdin/stdout subprocess管理）はv4スコープ外。
	// 現在はスタブとして空リストを返す。ShiroAgentのMCPToolオプション利用時は
	// RegisterServer()でサーバーを登録後、このメソッドがツール一覧を返す想定。
	// 実装時: exec.Command(config.Command, config.Args...) → JSON-RPC "tools/list" 送信
	_ = config
	return []string{}, nil
}

// CallTool は指定されたサーバーのツールを呼び出す
func (c *MCPClient) CallTool(ctx context.Context, serverName, toolName string, args map[string]interface{}) (string, error) {
	c.mu.RLock()
	config, exists := c.servers[serverName]
	c.mu.RUnlock()

	if !exists {
		return "", fmt.Errorf("server '%s' not registered", serverName)
	}

	// NOTE: MCP完全実装（JSON-RPC over stdin/stdout subprocess管理）はv4スコープ外。
	// 現在はスタブとしてエラーを返す。
	// 実装時: exec.Command(config.Command, config.Args...) → JSON-RPC "tools/call" 送信
	_ = config
	_ = toolName
	_ = args
	return "", fmt.Errorf("MCP tool execution not yet implemented")
}

// Validate は設定の妥当性を検証
func (c *ServerConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("server name is required")
	}
	if c.Command == "" {
		return fmt.Errorf("command is required")
	}
	return nil
}
