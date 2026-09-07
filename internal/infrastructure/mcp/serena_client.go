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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/userhome"
)

// SerenaClient は Serena MCP サーバーのサブプロセスを管理し、
// JSON-RPC over stdin/stdout でツールを呼び出す。
type SerenaClient struct {
	mu              sync.Mutex
	sendOnce        sync.Once
	sendGate        chan struct{}
	receiveMu       sync.Mutex
	receiver        *serenaReceiver
	stdout          io.ReadCloser
	cmd             *exec.Cmd
	stdin           io.WriteCloser
	scanner         *bufio.Scanner
	nextID          atomic.Int64
	started         bool
	processWaitDone chan struct{}
	processWaitErr  error

	generation   uint64
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
	Tools []tool.MCPToolDefinition `json:"tools"`
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
	if ctx == nil {
		return fmt.Errorf("Serena startup context is required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if c.started {
		c.receiveMu.Lock()
		usable := c.cmd != nil && c.cmd.Process != nil && c.stdin != nil && c.stdout != nil && c.receiver != nil
		if usable {
			c.receiver.mu.Lock()
			usable = c.receiver.terminal == nil
			c.receiver.mu.Unlock()
		}
		c.receiveMu.Unlock()
		if usable {
			return nil
		}
		if err := c.stopLocked(); err != nil {
			return fmt.Errorf("failed Serena transport retirement incomplete: %w", err)
		}
	}
	if c.cmd != nil {
		if err := c.stopLocked(); err != nil {
			return fmt.Errorf("previous Serena process retirement incomplete: %w", err)
		}
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
	c.processWaitDone = nil
	c.processWaitErr = nil
	c.installTransport(stdin, stdout)

	// MCP initialize ハンドシェイク
	if err := c.initialize(ctx); err != nil {
		if stopErr := c.stopLocked(); stopErr != nil {
			return fmt.Errorf("mcp initialize: %w; process retirement: %v", err, stopErr)
		}
		return fmt.Errorf("mcp initialize: %w", err)
	}

	c.started = true
	log.Printf("[SerenaClient] started (pid=%d workspace=%s)", cmd.Process.Pid, c.workspaceDir)
	return nil
}

// installTransport publishes one coherent stdin/stdout/receiver generation.
func (c *SerenaClient) installTransport(stdin io.WriteCloser, stdout io.ReadCloser) {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	if c.receiver != nil {
		c.receiver.close(fmt.Errorf("serena transport generation replaced"))
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	c.generation++
	c.stdin, c.stdout = stdin, stdout
	c.scanner = bufio.NewScanner(stdout)
	c.scanner.Buffer(make([]byte, 1<<20), 1<<20)
	c.receiver = nil
}

// Stop はサブプロセスを終了する。
func (c *SerenaClient) Stop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.stopLocked(); err != nil {
		log.Printf("[SerenaClient] process retirement incomplete: %v", err)
	}
}

const serenaStopTimeout = 5 * time.Second

// stopLocked retains process ownership until the one Wait has completed.
func (c *SerenaClient) stopLocked() error {
	c.started = false
	c.receiveMu.Lock()
	if c.receiver != nil {
		c.receiver.close(fmt.Errorf("serena transport stopped"))
	}
	if c.stdin != nil {
		_ = c.stdin.Close()
	}
	if c.stdout != nil {
		_ = c.stdout.Close()
	}
	c.stdin, c.stdout = nil, nil
	c.receiveMu.Unlock()
	if c.cmd == nil || c.cmd.Process == nil {
		return nil
	}
	if c.processWaitDone == nil {
		_ = c.cmd.Process.Kill()
		done := make(chan struct{})
		c.processWaitDone = done
		cmd := c.cmd
		go func() { c.processWaitErr = cmd.Wait(); close(done) }()
	}
	timer := time.NewTimer(serenaStopTimeout)
	defer timer.Stop()
	select {
	case <-c.processWaitDone:
		// ExitError after Kill is expected; ProcessState proves Wait reaped it.
		if c.cmd.ProcessState == nil {
			return fmt.Errorf("Serena process Wait did not confirm exit: %v", c.processWaitErr)
		}
		c.cmd = nil
		c.processWaitDone = nil
		c.processWaitErr = nil
		return nil
	case <-timer.C:
		return fmt.Errorf("Serena process %d retirement remains pending", c.cmd.Process.Pid)
	}
}

// ListTools は利用可能なツール名一覧を返す。
func (c *SerenaClient) ListTools(ctx context.Context) ([]tool.MCPToolDefinition, error) {
	resp, err := c.call(ctx, "tools/list", nil)
	if err != nil {
		return nil, err
	}
	var result mcpToolsListResult
	if err := json.Unmarshal(resp, &result); err != nil {
		return nil, fmt.Errorf("tools/list parse: %w", err)
	}
	return result.Tools, nil
}

// CallTool は指定ツールを呼び出し、テキスト結果を返す。
func (c *SerenaClient) CallTool(ctx context.Context, toolName string, args map[string]any) (string, error) {
	return c.callTool(ctx, 0, toolName, args)
}
func (c *SerenaClient) ConnectionGeneration() uint64 {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	if c.stdin == nil || c.stdout == nil {
		return 0
	}
	if c.receiver != nil {
		c.receiver.mu.Lock()
		retired := c.receiver.terminal != nil
		c.receiver.mu.Unlock()
		if retired {
			return 0
		}
	}
	return c.generation
}
func (c *SerenaClient) CallToolAtGeneration(ctx context.Context, generation uint64, toolName string, args map[string]any) (string, error) {
	if generation == 0 {
		return "", fmt.Errorf("MCP observed connection generation is required")
	}
	return c.callTool(ctx, generation, toolName, args)
}
func (c *SerenaClient) callTool(ctx context.Context, generation uint64, toolName string, args map[string]any) (string, error) {
	params := map[string]any{
		"name":      toolName,
		"arguments": args,
	}
	resp, err := c.callWithGeneration(ctx, generation, "tools/call", params)
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
	return c.notify(initCtx, "notifications/initialized", nil)
}

// call はJSON-RPCリクエストを送信し、レスポンスのresultを返す。
func (c *SerenaClient) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	return c.callWithGeneration(ctx, 0, method, params)
}
func (c *SerenaClient) callWithGeneration(ctx context.Context, generation uint64, method string, params any) (json.RawMessage, error) {
	if ctx == nil {
		return nil, fmt.Errorf("Serena request context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	id := c.nextID.Add(1)
	req := jsonrpcRequest{JSONRPC: "2.0", ID: id, Method: method, Params: params}
	receiver, err := c.responseReceiver()
	if err != nil {
		return nil, err
	}
	if generation != 0 && receiver.generation != generation {
		return nil, fmt.Errorf("MCP observed connection generation changed")
	}
	return receiver.wait(ctx, id, func() error {
		if err := c.send(ctx, req, receiver); err != nil {
			return fmt.Errorf("send %s: %w", method, err)
		}
		return nil
	})
}

// notify はレスポンスを期待しないJSON-RPC通知を送る。
func (c *SerenaClient) notify(ctx context.Context, method string, params any) error {
	req := jsonrpcRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}
	receiver, err := c.responseReceiver()
	if err != nil {
		return err
	}
	return c.send(ctx, req, receiver)
}

func (c *SerenaClient) send(ctx context.Context, req jsonrpcRequest, expected *serenaReceiver) error {
	if ctx == nil {
		return fmt.Errorf("Serena send context is required")
	}
	c.sendOnce.Do(func() { c.sendGate = make(chan struct{}, 1) })
	select {
	case <-ctx.Done():
		return ctx.Err()
	case c.sendGate <- struct{}{}:
	}
	defer func() { <-c.sendGate }()
	if err := ctx.Err(); err != nil {
		return err
	}
	c.receiveMu.Lock()
	stdin, stdout, receiver := c.stdin, c.stdout, c.receiver
	c.receiveMu.Unlock()
	if expected == nil || receiver != expected {
		return fmt.Errorf("serena transport generation changed before send")
	}
	receiver.mu.Lock()
	terminal := receiver.terminal
	receiver.mu.Unlock()
	if terminal != nil {
		return terminal
	}

	if stdin == nil {
		return fmt.Errorf("serena stdin unavailable")
	}
	b, err := json.Marshal(req)
	if err != nil {
		return err
	}
	frame := append(b, '\n')
	// A canceled or partial frame poisons this generation. Close only the
	// captured streams, never a replacement generation installed by recovery.
	retire := func(cause error) {
		if receiver != nil {
			receiver.close(cause)
		}
		_ = stdin.Close()
		if stdout != nil {
			_ = stdout.Close()
		}
	}
	canceled := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(canceled); retire(ctx.Err()) })
	n, writeErr := stdin.Write(frame)
	if !stop() {
		<-canceled
	}
	if err := ctx.Err(); err != nil {
		retire(err)
		return err
	}
	if writeErr == nil && n != len(frame) {
		writeErr = io.ErrShortWrite
	}
	if writeErr != nil {
		retire(fmt.Errorf("serena transport write failed: %w", writeErr))
	}
	return writeErr
}

type serenaReply struct {
	result json.RawMessage
	err    error
}

// serenaReceiver owns one stdout generation. Only its read loop uses Scan.
type serenaReceiver struct {
	generation uint64
	mu         sync.Mutex
	scanner    *bufio.Scanner
	pending    map[int64]chan serenaReply
	running    bool
	terminal   error
}

func (c *SerenaClient) responseReceiver() (*serenaReceiver, error) {
	c.receiveMu.Lock()
	defer c.receiveMu.Unlock()
	if c.scanner == nil {
		return nil, fmt.Errorf("serena stdout unavailable")
	}
	if c.receiver == nil {
		c.receiver = &serenaReceiver{generation: c.generation, scanner: c.scanner, pending: make(map[int64]chan serenaReply)}
	}
	return c.receiver, nil
}

func (c *SerenaClient) recv(ctx context.Context, wantID int64) (json.RawMessage, error) {
	r, err := c.responseReceiver()
	if err != nil {
		return nil, err
	}
	return r.wait(ctx, wantID, nil)
}

func (r *serenaReceiver) wait(ctx context.Context, id int64, send func() error) (json.RawMessage, error) {
	if ctx == nil {
		return nil, fmt.Errorf("Serena receive context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	reply := make(chan serenaReply, 1)
	r.mu.Lock()
	if r.terminal != nil {
		err := r.terminal
		r.mu.Unlock()
		return nil, err
	}
	if _, exists := r.pending[id]; exists {
		r.mu.Unlock()
		return nil, fmt.Errorf("duplicate pending Serena request ID: %d", id)
	}
	r.pending[id] = reply
	start := !r.running
	if start {
		r.running = true
	}
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		if r.pending[id] == reply {
			delete(r.pending, id)
		}
		r.mu.Unlock()
	}()
	// Register before send so another active reader can already route this ID.
	// Start the first reader after sending the registered request.
	var sendErr error
	if send != nil {
		sendErr = send()
	}
	if start {
		go r.read()
	}
	if sendErr != nil {
		return nil, sendErr
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case response := <-reply:
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		return response.result, response.err
	}
}

func (r *serenaReceiver) close(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.terminal != nil {
		return
	}
	r.terminal = err
	for id, reply := range r.pending {
		reply <- serenaReply{err: err}
		delete(r.pending, id)
	}
}

func (r *serenaReceiver) read() {
	for {
		r.mu.Lock()
		if r.terminal != nil || len(r.pending) == 0 {
			r.running = false
			r.mu.Unlock()
			return
		}
		r.mu.Unlock()
		if !r.scanner.Scan() {
			err := r.scanner.Err()
			if err != nil {
				r.close(fmt.Errorf("serena stdout read: %w", err))
			} else {
				r.close(fmt.Errorf("serena stdout closed"))
			}
			return
		}
		var response jsonrpcResponse
		if err := json.Unmarshal(r.scanner.Bytes(), &response); err != nil {
			continue
		}
		r.mu.Lock()
		if reply, ok := r.pending[response.ID]; ok {
			value := serenaReply{result: response.Result}
			if response.Error != nil {
				value.err = fmt.Errorf("jsonrpc error %d: %s", response.Error.Code, response.Error.Message)
			}
			reply <- value
			delete(r.pending, response.ID)
		}
		r.mu.Unlock()
	}
}

// enrichedEnv は現在の環境変数に ~/.local/bin / ~/go/bin を補完した env を返す。
// rencrow 自体が systemd 等の制限環境で起動している場合でも Serena が go/gopls を見つけられる。
func enrichedEnv() []string {
	env := os.Environ()
	extra := []string{}
	// home が解決できない場合は PATH へ無効なパスを足さない
	if home, err := userhome.Dir(); err == nil {
		extra = append(extra,
			filepath.Join(home, ".local", "bin"),
			filepath.Join(home, "go", "bin"),
		)
	}
	if runtime.GOOS != "windows" {
		extra = append(extra, "/usr/local/go/bin")
	}
	separator := string(os.PathListSeparator)
	// 既存 PATH に追記
	for i, e := range env {
		if current, ok := strings.CutPrefix(e, "PATH="); ok {
			env[i] = "PATH=" + strings.Join(extra, separator) + separator + current
			return env
		}
	}
	env = append(env, "PATH="+strings.Join(extra, separator)+separator+os.Getenv("PATH"))
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
