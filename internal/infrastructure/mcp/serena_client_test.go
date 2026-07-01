package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type bufferWriteCloser struct {
	bytes.Buffer
	closeErr error
}

func (b *bufferWriteCloser) Close() error {
	return b.closeErr
}

func newTestSerenaClient(stdin io.WriteCloser, stdoutLines string) *SerenaClient {
	return &SerenaClient{
		stdin:   stdin,
		scanner: bufio.NewScanner(strings.NewReader(stdoutLines)),
	}
}

func TestSerenaClientListToolsAndCallTool(t *testing.T) {
	stdin := &bufferWriteCloser{}
	output := strings.Join([]string{
		`not json`,
		`{"jsonrpc":"2.0","id":999,"result":{}}`,
		`{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"find_symbol"},{"name":"replace_symbol_body"}]}}`,
		`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"hello"},{"type":"image","text":"ignored"},{"type":"text","text":" world"}]}}`,
	}, "\n")
	client := newTestSerenaClient(stdin, output)

	tools, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatalf("ListTools failed: %v", err)
	}
	if strings.Join(tools, ",") != "find_symbol,replace_symbol_body" {
		t.Fatalf("unexpected tools: %#v", tools)
	}

	result, err := client.CallTool(context.Background(), "find_symbol", map[string]any{"name_path": "Foo"})
	if err != nil {
		t.Fatalf("CallTool failed: %v", err)
	}
	if result != "hello world" {
		t.Fatalf("unexpected tool result: %q", result)
	}

	var requests []jsonrpcRequest
	for _, line := range strings.Split(strings.TrimSpace(stdin.String()), "\n") {
		var req jsonrpcRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			t.Fatalf("request JSON parse failed: %v", err)
		}
		requests = append(requests, req)
	}
	if requests[0].Method != "tools/list" || requests[1].Method != "tools/call" {
		t.Fatalf("unexpected requests: %#v", requests)
	}
}

func TestSerenaClientCallToolErrorResponses(t *testing.T) {
	t.Run("json rpc error", func(t *testing.T) {
		client := newTestSerenaClient(&bufferWriteCloser{}, `{"jsonrpc":"2.0","id":1,"error":{"code":-32601,"message":"missing method"}}`)
		_, err := client.ListTools(context.Background())
		if err == nil || !strings.Contains(err.Error(), "jsonrpc error -32601") {
			t.Fatalf("expected jsonrpc error, got %v", err)
		}
	})

	t.Run("tool isError text", func(t *testing.T) {
		client := newTestSerenaClient(&bufferWriteCloser{}, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"text","text":"bad args"}]}}`)
		_, err := client.CallTool(context.Background(), "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "bad args") {
			t.Fatalf("expected tool error detail, got %v", err)
		}
	})

	t.Run("tool isError no detail", func(t *testing.T) {
		client := newTestSerenaClient(&bufferWriteCloser{}, `{"jsonrpc":"2.0","id":1,"result":{"isError":true,"content":[{"type":"image"}]}}`)
		_, err := client.CallTool(context.Background(), "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "no detail") {
			t.Fatalf("expected no-detail tool error, got %v", err)
		}
	})

	t.Run("parse errors", func(t *testing.T) {
		client := newTestSerenaClient(&bufferWriteCloser{}, `{"jsonrpc":"2.0","id":1,"result":{"tools":"bad"}}`)
		_, err := client.ListTools(context.Background())
		if err == nil || !strings.Contains(err.Error(), "tools/list parse") {
			t.Fatalf("expected list parse error, got %v", err)
		}

		client = newTestSerenaClient(&bufferWriteCloser{}, `{"jsonrpc":"2.0","id":1,"result":{"content":"bad"}}`)
		_, err = client.CallTool(context.Background(), "tool", nil)
		if err == nil || !strings.Contains(err.Error(), "tools/call parse") {
			t.Fatalf("expected call parse error, got %v", err)
		}
	})
}

func TestSerenaClientNotifySendAndRecvFailures(t *testing.T) {
	t.Run("send close pipe", func(t *testing.T) {
		reader, writer := io.Pipe()
		_ = reader.Close()
		_ = writer.Close()
		client := newTestSerenaClient(writer, "")
		if err := client.notify("notifications/initialized", nil); err == nil {
			t.Fatal("expected send error")
		}
	})

	t.Run("stdout closed", func(t *testing.T) {
		client := newTestSerenaClient(&bufferWriteCloser{}, "")
		_, err := client.recv(context.Background(), 1)
		if err == nil || !strings.Contains(err.Error(), "serena stdout closed") {
			t.Fatalf("expected stdout closed error, got %v", err)
		}
	})

	t.Run("context canceled", func(t *testing.T) {
		reader, writer := io.Pipe()
		defer reader.Close()
		defer writer.Close()
		client := &SerenaClient{scanner: bufio.NewScanner(reader)}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.recv(ctx, 1)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected context canceled, got %v", err)
		}
	})
}

func TestSerenaClientResolveCommandFromWorkspaceCache(t *testing.T) {
	root := t.TempDir()
	bin := filepath.Join(root, ".serena", "uv-cache", "archive-v0", "pkg", "bin", "serena-mcp-server")
	if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
		t.Fatalf("MkdirAll failed: %v", err)
	}
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("WriteFile failed: %v", err)
	}

	cmd, err := NewSerenaClient(root).resolveCommand()
	if err != nil {
		t.Fatalf("resolveCommand failed: %v", err)
	}
	if len(cmd) != 1 || cmd[0] != bin {
		t.Fatalf("unexpected command: %#v", cmd)
	}
}

func TestSerenaClientInitializeSendsNotification(t *testing.T) {
	stdin := &bufferWriteCloser{}
	client := newTestSerenaClient(stdin, `{"jsonrpc":"2.0","id":1,"result":{"serverInfo":{"name":"serena"}}}`)

	if err := client.initialize(context.Background()); err != nil {
		t.Fatalf("initialize failed: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdin.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected initialize request and initialized notification, got %q", stdin.String())
	}
	var initReq jsonrpcRequest
	if err := json.Unmarshal([]byte(lines[0]), &initReq); err != nil {
		t.Fatalf("init request parse failed: %v", err)
	}
	if initReq.Method != "initialize" || initReq.ID != 1 {
		t.Fatalf("unexpected init request: %#v", initReq)
	}
	var notification jsonrpcRequest
	if err := json.Unmarshal([]byte(lines[1]), &notification); err != nil {
		t.Fatalf("notification parse failed: %v", err)
	}
	if notification.Method != "notifications/initialized" || notification.ID != 0 {
		t.Fatalf("unexpected notification: %#v", notification)
	}
}

func TestEnrichedEnvAddsToolPaths(t *testing.T) {
	t.Setenv("HOME", "/tmp/rencrow-home")
	t.Setenv("PATH", "/usr/bin")
	env := enrichedEnv()
	var path string
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			path = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	if !strings.HasPrefix(path, "/tmp/rencrow-home/.local/bin:/tmp/rencrow-home/go/bin:/usr/local/go/bin:") {
		t.Fatalf("PATH was not enriched as expected: %q", path)
	}
}

func TestSerenaClientStopIdempotent(t *testing.T) {
	client := NewSerenaClient(t.TempDir())
	client.Stop()

	stdin := &bufferWriteCloser{}
	client.stdin = stdin
	client.started = true
	client.Stop()
	if client.started {
		t.Fatal("Stop should clear started")
	}
}

func TestSerenaClientRecvHonorsDelayedResponse(t *testing.T) {
	reader, writer := io.Pipe()
	client := &SerenaClient{scanner: bufio.NewScanner(reader)}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	go func() {
		defer writer.Close()
		_, _ = writer.Write([]byte("log line\n"))
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"ignored":true}}` + "\n"))
		_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"ok":true}}` + "\n"))
	}()

	result, err := client.recv(ctx, 1)
	if err != nil {
		t.Fatalf("recv failed: %v", err)
	}
	if string(result) != `{"ok":true}` {
		t.Fatalf("unexpected result: %s", result)
	}
}
