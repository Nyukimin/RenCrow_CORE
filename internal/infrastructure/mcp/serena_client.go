package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// SerenaClient は Serena MCP サーバーのサブプロセスを管理し、
// JSON-RPC over stdin/stdout でツールを呼び出す。
type SerenaClient struct {
	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	nextID  atomic.Int64
	started bool

	workspaceDir string // --project-from-cwd のための作業ディレクトリ
}

// jsonrpcRequest はJSON-RPCリクエスト
type jsonrpcRequest struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id,omitempty"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// jsonrpcResponse はJSON-RPCレスポンス
type jsonrpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *jsonrpcError   `json:"error,omitempty"`
}

type jsonrpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// mcpToolsListResult は tools/list のレスポンス
type mcpToolsListResult struct {
	Tools []mcpToolDef `json:"tools"`
}

type mcpToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// mcpToolCallResult は tools/call のレスポンス
type mcpToolCallResult struct {
	Content []mcpContent `json:"content"`
	IsError bool         `json:"isError,omitempty"`
}

type mcpContent struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// NewSerenaClient は指定ワークスペースを対象とした SerenaClient を生成する。
// Start() を呼ぶまでサブプロセスは起動しない。
func NewSerenaClient(workspaceDir string) *SerenaClient {
	return &SerenaClient{workspaceDir: workspaceDir}
}

// Start は Serena サブプロセスを起動し MCP ハンドシェイクを行う。
func (c *SerenaClient) Start(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.started {
		return nil
	}

	serenaCmd, err := c.resolveCommand()
	if err != nil {
		return fmt.Errorf("serena command not found: %w", err)
	}

	// .serena/project.yml が存在する場合は --project でプロジェクトを明示指定する。
	// なければ --project-from-cwd にフォールバック。
	projectArgs := []string{"--enable-web-dashboard", "False"}
	projectYML := filepath.Join(c.workspaceDir, ".serena", "project.yml")
	if _, err := os.Stat(projectYML); err == nil {
		projectArgs = append(projectArgs, "--project", c.workspaceDir)
	} else {
		projectArgs = append(projectArgs, "--project-from-cwd")
	}

	cmd := exec.CommandContext(ctx, serenaCmd[0], append(serenaCmd[1:], projectArgs...)...)
	cmd.Dir = c.workspaceDir
	cmd.Stderr = os.Stderr
	// Goバイナリ等をサブプロセスから見えるよう PATH を引き継ぎつつ ~/.local/bin を補完
	cmd.Env = enrichedEnv()

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start serena: %w", err)
	}

	c.cmd = cmd
	c.stdin = stdin
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 1<<20), 1<<20) // 1MB バッファ

	// MCP initialize ハンドシェイク
	if err := c.initialize(ctx); err != nil {
		_ = cmd.Process.Kill()
		return fmt.Errorf("mcp initialize: %w", err)
	}

	c.started = true
	log.Printf("[SerenaClient] started (pid=%d workspace=%s)", cmd.Process.Pid, c.workspaceDir)
	return nil
}

// Stop はサブプロセスを終了する。
func (c *SerenaClient) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.started {
		return
	}
	c.started = false
	_ = c.stdin.Close()
	if c.cmd != nil && c.cmd.Process != nil {
		_ = c.cmd.Process.Kill()
	}
}

// ListTools は利用可能なツール名一覧を返す。
func (c *SerenaClient) ListTools(ctx context.Context) ([]string, error) {
	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result mcpToolsListResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("tools/list parse: %w", err)
	}
	names := make([]string, len(result.Tools))
	for i, t := range result.Tools {
		names[i] = t.Name
	}
	return names, nil
}

// CallTool は指定ツールを呼び出し、テキスト結果を返す。
func (c *SerenaClient) CallTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}
	resp, err := c.call(ctx, "tools/call", params)
	if err != nil {
		return "", err
	}
	var result mcpToolCallResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return "", fmt.Errorf("tools/call parse: %w", err)
	}
	if result.IsError {
		for _, c := range result.Content {
			if c.Type == "text" {
				return "", fmt.Errorf("serena tool error: %s", c.Text)
			}
		}
		return "", fmt.Errorf("serena tool error (no detail)")
	}
	var sb strings.Builder
	for _, c := range result.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

// initialize は MCP プロトコルの初期ハンドシェイクを行う。
func (c *SerenaClient) initialize(ctx context.Context) error {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	params := map[string]any{
		"protocolVersion": "2024-11-05",
		"capabilities":    map[string]any{},
		"clientInfo": map[string]any{
			"name":    "rencrow",
			"version": "1.0",
		},
	}
	if _, err := c.call(initCtx, "initialize", params); err != nil {
		return err
	}
	// initialized 通知（レスポンスなし）
	return c.notify("notifications/initialized", nil)
}

// call はJSON-RPCリクエストを送信し、レスポンスのresultを返す。
func (c *SerenaClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}
	if err := c.send(req); err != nil {
		return nil, fmt.Errorf("send %s: %w", method, err)
	}
	return c.recv(ctx, id)
}

// notify はレスポンスを期待しないJSON-RPC通知を送る。
func (c *SerenaClient) notify(method string, params any) error {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	return c.send(req)
}

func (c *SerenaClient) send(req jsonrpcRequest) error {
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(c.stdin, "%s\n", b)
	return err
}

func (c *SerenaClient) recv(ctx context.Context, wantID int64) (json.RawMessage, error) {
	done := make(chan struct{})
	var result json.RawMessage
	var recvErr error

	go func() {
		defer close(done)
		for c.scanner.Scan() {
			line := c.scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var resp jsonrpcResponse
			if err := json.Unmarshal(line, &resp); err != nil {
				continue // JSON以外の行（ログ等）はスキップ
			}
			if resp.ID != wantID {
				continue // 別のリクエストへのレスポンスはスキップ
			}
			if resp.Error != nil {
				recvErr = fmt.Errorf("jsonrpc error %d: %s", resp.Error.Code, resp.Error.Message)
				return
			}
			result = resp.Result
			return
		}
		recvErr = fmt.Errorf("serena stdout closed")
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-done:
		return result, recvErr
	}
}

// enrichedEnv は現在の環境変数に ~/.local/bin / ~/go/bin を補完した env を返す。
// rencrow 自体が systemd 等の制限環境で起動している場合でも Serena が go/gopls を見つけられる。
func enrichedEnv() []string {
	env := os.Environ()
	home := os.Getenv("HOME")
	extra := []string{
		filepath.Join(home, ".local", "bin"),
		filepath.Join(home, "go", "bin"),
		"/usr/local/go/bin",
	}
	// 既存 PATH に追記
	for i, e := range env {
		if current, ok := strings.CutPrefix(e, "PATH="); ok {
			env[i] = "PATH=" + strings.Join(extra, ":") + ":" + current
			return env
		}
	}
	env = append(env, "PATH="+strings.Join(extra, ":")+":"+os.Getenv("PATH"))
	return env
}

// resolveCommand は serena-mcp-server のコマンドを解決する。
// 優先順位: .serena/uv-cache 内バイナリ → uvx --from .serena → PATH
func (c *SerenaClient) resolveCommand() ([]string, error) {
	// 1. ワークスペース内 .serena/uv-cache から直接バイナリを探す（最速・確実）
	cacheDir := filepath.Join(c.workspaceDir, ".serena", "uv-cache", "archive-v0")
	if entries, err := os.ReadDir(cacheDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			bin := filepath.Join(cacheDir, e.Name(), "bin", "serena-mcp-server")
			if _, err := os.Stat(bin); err == nil {
				return []string{bin}, nil
			}
		}
	}
	// 2. uvx --from .serena/
	serenaDir := filepath.Join(c.workspaceDir, ".serena")
	if _, err := os.Stat(serenaDir); err == nil {
		if uvx, err := exec.LookPath("uvx"); err == nil {
			return []string{uvx, "--from", serenaDir, "serena-mcp-server"}, nil
		}
	}
	// 3. uvx --from registered serena package
	if uvx, err := exec.LookPath("uvx"); err == nil {
		return []string{uvx, "--from", "serena", "serena-mcp-server"}, nil
	}
	// 4. PATH 上の serena-mcp-server
	if p, err := exec.LookPath("serena-mcp-server"); err == nil {
		return []string{p}, nil
	}
	return nil, fmt.Errorf("serena-mcp-server not found in %s/.serena/uv-cache, uvx, or PATH", c.workspaceDir)
}
