package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
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
	if len(tools) != 2 || tools[0].Name != "find_symbol" || tools[1].Name != "replace_symbol_body" {
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
		if err := client.notify(context.Background(), "notifications/initialized", nil); err == nil {
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
	home := filepath.Join(t.TempDir(), "rencrow-home")
	// os.UserHomeDir が参照する環境変数はOSごとに異なる（Unix系は HOME、
	// Windows は USERPROFILE）。両方を設定する
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("PATH", filepath.Join(home, "existing-bin"))
	env := enrichedEnv()
	var path string
	for _, item := range env {
		if strings.HasPrefix(item, "PATH=") {
			path = strings.TrimPrefix(item, "PATH=")
			break
		}
	}
	wantPrefix := strings.Join([]string{filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin")}, string(os.PathListSeparator)) + string(os.PathListSeparator)
	if !strings.HasPrefix(path, wantPrefix) {
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

func TestSerenaClientCanceledCallDoesNotSend(t *testing.T) {
	stdin := &bufferWriteCloser{}
	client := newTestSerenaClient(stdin, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.CallTool(ctx, "read_file", nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error: %v", err)
	}
	if stdin.Len() != 0 {
		t.Fatalf("canceled call sent request: %s", stdin.String())
	}
}

func TestSerenaClientLateCanceledReplyKeepsNextOwner(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	client := &SerenaClient{scanner: bufio.NewScanner(reader)}
	first, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, err := client.recv(first, 1); done <- err }()
	// Synchronize with the reader by sending a harmless notification line.
	if _, err := writer.Write([]byte("{}\n")); err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("first: %v", err)
	}
	next, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	receiver, err := client.responseReceiver()
	if err != nil {
		t.Fatal(err)
	}
	result, err := receiver.wait(next, 2, func() error {
		go func() {
			_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"owner":"second"}}` + "\n"))
			_, _ = writer.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"owner":"first"}}` + "\n"))
		}()
		return nil
	})
	if err != nil || string(result) != `{"owner":"second"}` {
		t.Fatalf("next owner lost: %s %v", result, err)
	}
}

func TestSerenaClientConcurrentRepliesKeepRequestOwner(t *testing.T) {
	requests, stdin := io.Pipe()
	stdout, responses := io.Pipe()
	defer requests.Close()
	defer stdin.Close()
	defer stdout.Close()
	defer responses.Close()
	client := &SerenaClient{stdin: stdin, scanner: bufio.NewScanner(stdout)}
	serverDone := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(requests)
		var calls []struct {
			ID     int64 `json:"id"`
			Params struct {
				Name string `json:"name"`
			} `json:"params"`
		}
		for i := 0; i < 2; i++ {
			if !scanner.Scan() {
				serverDone <- fmt.Errorf("missing request %d", i)
				return
			}
			var call struct {
				ID     int64 `json:"id"`
				Params struct {
					Name string `json:"name"`
				} `json:"params"`
			}
			if err := json.Unmarshal(scanner.Bytes(), &call); err != nil {
				serverDone <- err
				return
			}
			calls = append(calls, call)
		}
		for i := len(calls) - 1; i >= 0; i-- {
			reply := map[string]any{"jsonrpc": "2.0", "id": calls[i].ID, "result": map[string]any{"content": []map[string]string{{"type": "text", "text": calls[i].Params.Name}}}}
			data, err := json.Marshal(reply)
			if err != nil {
				serverDone <- err
				return
			}
			if _, err := responses.Write(append(data, '\n')); err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	done := make(chan error, 2)
	for _, name := range []string{"first", "second"} {
		go func(name string) {
			result, err := client.CallTool(ctx, name, nil)
			if err == nil && result != name {
				err = fmt.Errorf("request %s received %s", name, result)
			}
			done <- err
		}(name)
	}
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestSerenaClientEOFResolvesAllPendingRequests(t *testing.T) {
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	client := &SerenaClient{scanner: bufio.NewScanner(reader)}
	receiver, err := client.responseReceiver()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	ready := make(chan struct{}, 2)
	done := make(chan error, 2)
	for id := int64(1); id <= 2; id++ {
		go func(id int64) {
			_, err := receiver.wait(ctx, id, func() error { ready <- struct{}{}; return nil })
			done <- err
		}(id)
	}
	<-ready
	<-ready
	_ = writer.Close()
	for i := 0; i < 2; i++ {
		if err := <-done; err == nil || !strings.Contains(err.Error(), "stdout closed") {
			t.Fatalf("EOF pending result: %v", err)
		}
	}
}

type signalMCPWriter struct {
	io.WriteCloser
	once    sync.Once
	entered chan struct{}
}

func (w *signalMCPWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	return w.WriteCloser.Write(p)
}

func TestSerenaClientQueuedSendCancellation(t *testing.T) {
	reader, pipe := io.Pipe()
	defer reader.Close()
	defer pipe.Close()
	writer := &signalMCPWriter{WriteCloser: pipe, entered: make(chan struct{})}
	client := newTestSerenaClient(writer, "")
	receiver, err := client.responseReceiver()
	if err != nil {
		t.Fatal(err)
	}
	first, cancelFirst := context.WithCancel(context.Background())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- client.send(first, jsonrpcRequest{JSONRPC: "2.0", ID: 1, Method: "first"}, receiver)
	}()
	<-writer.entered
	queued, cancelQueued := context.WithCancel(context.Background())
	queuedDone := make(chan error, 1)
	go func() {
		queuedDone <- client.send(queued, jsonrpcRequest{JSONRPC: "2.0", ID: 2, Method: "second"}, receiver)
	}()
	cancelQueued()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-queuedDone:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("queued error: %v", err)
		}
	case <-timer.C:
		t.Error("queued send ignored cancellation")
		_ = reader.Close()
	}
	select {
	case err := <-firstDone:
		t.Fatalf("queued cancellation disturbed active writer: %v", err)
	default:
	}
	cancelFirst()
	_ = reader.Close()
	<-firstDone
}

func TestSerenaClientBlockedSendRetiresTransport(t *testing.T) {
	reader, pipe := io.Pipe()
	defer reader.Close()
	defer pipe.Close()
	writer := &signalMCPWriter{WriteCloser: pipe, entered: make(chan struct{})}
	client := newTestSerenaClient(writer, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := client.CallTool(ctx, "read_file", nil); done <- err }()
	<-writer.entered
	cancel()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("active send: %v", err)
		}
	case <-timer.C:
		t.Error("blocked write ignored cancellation")
		_ = reader.Close()
		<-done
	}
	if _, err := client.CallTool(context.Background(), "read_file", nil); err == nil {
		t.Fatal("retired transport reused")
	}
}

type shortMCPWriter struct {
	calls  int
	closed bool
}

func (w *shortMCPWriter) Write(p []byte) (int, error) { w.calls++; return len(p) - 1, nil }
func (w *shortMCPWriter) Close() error                { w.closed = true; return nil }

func TestSerenaClientShortFrameRetiresTransport(t *testing.T) {
	writer := &shortMCPWriter{}
	client := newTestSerenaClient(writer, "")
	if _, err := client.CallTool(context.Background(), "read_file", nil); !errors.Is(err, io.ErrShortWrite) {
		t.Fatalf("short frame: %v", err)
	}
	if !writer.closed {
		t.Fatal("partial frame left transport open")
	}
	if _, err := client.CallTool(context.Background(), "read_file", nil); err == nil {
		t.Fatal("partial frame transport reused")
	}
	if writer.calls != 1 {
		t.Fatalf("subsequent frame sent on retired transport: %d", writer.calls)
	}
}

func TestSerenaClientOldRequestCannotWriteReplacement(t *testing.T) {
	oldWriter := &bufferWriteCloser{}
	newWriter := &bufferWriteCloser{}
	client := newTestSerenaClient(oldWriter, "")
	old, err := client.responseReceiver()
	if err != nil {
		t.Fatal(err)
	}
	_, err = old.wait(context.Background(), 1, func() error {
		client.installTransport(newWriter, io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"new"}]}}`)))
		return client.send(context.Background(), jsonrpcRequest{JSONRPC: "2.0", ID: 1, Method: "old-request"}, old)
	})
	if err == nil {
		t.Fatal("old request accepted")
	}
	if newWriter.Len() != 0 {
		t.Fatalf("old request crossed generation: %s", newWriter.String())
	}
	result, err := client.CallTool(context.Background(), "new-request", nil)
	if err != nil || result != "new" {
		t.Fatalf("replacement request: %q %v", result, err)
	}
}

func TestSerenaClientActiveOldSendCannotRetireReplacement(t *testing.T) {
	oldReader, oldPipe := io.Pipe()
	defer oldReader.Close()
	defer oldPipe.Close()
	oldWriter := &signalMCPWriter{WriteCloser: oldPipe, entered: make(chan struct{})}
	client := newTestSerenaClient(oldWriter, "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	oldDone := make(chan error, 1)
	go func() { _, err := client.CallTool(ctx, "old", nil); oldDone <- err }()
	<-oldWriter.entered
	newReader, newResponses := io.Pipe()
	defer newReader.Close()
	defer newResponses.Close()
	newWriter := &bufferWriteCloser{}
	client.installTransport(newWriter, newReader)
	if err := <-oldDone; err == nil {
		t.Fatal("old in-flight frame unexpectedly succeeded")
	}
	go func() {
		_, _ = newResponses.Write([]byte(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"replacement"}]}}` + "\n"))
	}()
	result, err := client.CallTool(ctx, "new", nil)
	if err != nil || result != "replacement" {
		t.Fatalf("old send retired replacement: %q %v", result, err)
	}
	if strings.Contains(newWriter.String(), `"name":"old"`) {
		t.Fatal("old request reached new writer")
	}
}

func TestSerenaOwnedProcessHelper(t *testing.T) {
	if os.Getenv("RENCROW_MCP_PROCESS_FIXTURE") != "1" {
		return
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

func TestSerenaClientStopReapsOwnedChild(t *testing.T) {
	for _, initialized := range []bool{true, false} {
		t.Run(fmt.Sprint(initialized), func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestSerenaOwnedProcessHelper$")
			cmd.Env = append(os.Environ(), "RENCROW_MCP_PROCESS_FIXTURE=1")
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if cmd.ProcessState == nil {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
				}
			}()
			client := NewSerenaClient(t.TempDir())
			client.cmd = cmd
			client.stdin = stdin
			client.started = initialized
			client.Stop()
			if cmd.ProcessState == nil {
				t.Fatal("owned child was not reaped")
			}
			client.Stop()
		})
	}
}

func TestSerenaClientUnconfirmedRetirementBlocksStart(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestSerenaOwnedProcessHelper$")
	cmd.Env = append(os.Environ(), "RENCROW_MCP_PROCESS_FIXTURE=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	client := NewSerenaClient(t.TempDir())
	client.cmd = cmd
	client.stdin = stdin
	// Hold completion publication to model an existing, unresolved Wait owner.
	done := make(chan struct{})
	release := make(chan struct{})
	client.processWaitDone = done
	go func() { <-release; client.processWaitErr = cmd.Wait(); close(done) }()
	defer func() { _ = cmd.Process.Kill(); close(release); <-done; client.Stop() }()
	err = client.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "retirement incomplete") {
		t.Fatalf("replacement was not blocked by retirement: %v", err)
	}
	if client.cmd != cmd || client.started {
		t.Fatal("unconfirmed process ownership was overwritten")
	}
}

func TestSerenaClientStartRejectsKnownFailedState(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	for _, mode := range []string{"retired", "missing receiver", "missing process"} {
		t.Run(mode, func(t *testing.T) {
			client := NewSerenaClient(t.TempDir())
			var stdin io.WriteCloser = &bufferWriteCloser{}
			if mode != "missing process" {
				cmd := exec.Command(os.Args[0], "-test.run=^TestSerenaOwnedProcessHelper$")
				cmd.Env = append(os.Environ(), "RENCROW_MCP_PROCESS_FIXTURE=1")
				pipe, err := cmd.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := cmd.Start(); err != nil {
					t.Fatal(err)
				}
				client.cmd = cmd
				stdin = pipe
			}
			t.Cleanup(client.Stop)
			client.installTransport(stdin, io.NopCloser(strings.NewReader("")))
			if mode != "missing receiver" {
				receiver, err := client.responseReceiver()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "retired" {
					receiver.close(fmt.Errorf("fixture retired transport"))
				}
			}
			client.started = true
			if err := client.Start(context.Background()); err == nil {
				t.Fatal("known-failed runtime reported started")
			}
			if client.started {
				t.Fatal("failed startup kept stale started flag")
			}
		})
	}
}

func TestSerenaClientObservedGenerationCannotFollowReconnect(t *testing.T) {
	client := NewSerenaClient(t.TempDir())
	client.installTransport(&bufferWriteCloser{}, io.NopCloser(strings.NewReader("")))
	observed := client.ConnectionGeneration()
	writer := &bufferWriteCloser{}
	client.installTransport(writer, io.NopCloser(strings.NewReader(`{"jsonrpc":"2.0","id":2,"result":{"content":[{"type":"text","text":"new observation"}]}}`)))
	if observed == 0 || observed == client.ConnectionGeneration() {
		t.Fatal("connection identity did not advance")
	}
	if _, err := client.CallToolAtGeneration(context.Background(), observed, "read_file", nil); err == nil {
		t.Fatal("stale catalog generation followed reconnect")
	}
	if _, err := client.CallToolAtGeneration(context.Background(), 0, "read_file", nil); err == nil {
		t.Fatal("missing observation generation accepted")
	}
	if writer.Len() != 0 {
		t.Fatal("stale observation sent a request")
	}
	result, err := client.CallToolAtGeneration(context.Background(), client.ConnectionGeneration(), "read_file", nil)
	if err != nil || result != "new observation" {
		t.Fatalf("fresh observation failed: %q %v", result, err)
	}
}

func TestSerenaClientListToolsRetainsDefinition(t *testing.T) {
	client := newTestSerenaClient(&bufferWriteCloser{}, `{"jsonrpc":"2.0","id":1,"result":{"tools":[{"name":"read_file","description":"Read source","inputSchema":{"type":"object","properties":{"relative_path":{"type":"string"}},"required":["relative_path"],"additionalProperties":false}}]}}`)
	definitions, err := client.ListTools(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(definitions)
	if err != nil {
		t.Fatal(err)
	}
	var decoded []map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("tool definitions were reduced to names: %s %v", raw, err)
	}
	schema, ok := decoded[0]["inputSchema"].(map[string]any)
	if !ok || schema["additionalProperties"] != false || decoded[0]["description"] != "Read source" {
		t.Fatalf("observed definition lost: %s", raw)
	}
}

func TestSerenaClientRetiredGenerationIsUnavailable(t *testing.T) {
	for _, mode := range []string{"stop before receiver", "stop after receiver", "receiver failed"} {
		t.Run(mode, func(t *testing.T) {
			client := NewSerenaClient(t.TempDir())
			client.installTransport(&bufferWriteCloser{}, io.NopCloser(strings.NewReader("")))
			if client.ConnectionGeneration() == 0 {
				t.Fatal("installed transport unavailable")
			}
			if mode != "stop before receiver" {
				receiver, err := client.responseReceiver()
				if err != nil {
					t.Fatal(err)
				}
				if mode == "receiver failed" {
					receiver.close(io.EOF)
				}
			}
			if mode != "receiver failed" {
				client.Stop()
			}
			if client.ConnectionGeneration() != 0 {
				t.Fatal("retired transport advertised current generation")
			}
		})
	}
}
