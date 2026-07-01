package tools_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

// mockBaseRunner は base RunnerV2 のモック
type mockBaseRunner struct {
	knownTools map[string]*tool.ToolResponse
}

func (m *mockBaseRunner) ExecuteV2(ctx context.Context, toolName string, args map[string]any) (*tool.ToolResponse, error) {
	if resp, ok := m.knownTools[toolName]; ok {
		return resp, nil
	}
	return nil, fmt.Errorf("unknown tool: %s", toolName)
}

func (m *mockBaseRunner) ListTools(ctx context.Context) ([]tool.ToolMetadata, error) {
	metas := make([]tool.ToolMetadata, 0, len(m.knownTools))
	for name := range m.knownTools {
		metas = append(metas, tool.ToolMetadata{ToolID: name, Description: name})
	}
	return metas, nil
}

// mockToolRegistry は ToolRegistry のメモリ実装
type mockToolRegistry struct {
	entries map[string]capability.ToolEntry
}

func (r *mockToolRegistry) Register(ctx context.Context, entry capability.ToolEntry) error {
	r.entries[entry.Name] = entry
	return nil
}

func (r *mockToolRegistry) ListForPlatform(ctx context.Context, platform string) ([]capability.ToolEntry, error) {
	var result []capability.ToolEntry
	for _, e := range r.entries {
		result = append(result, e)
	}
	return result, nil
}

func (r *mockToolRegistry) Get(ctx context.Context, name string) (capability.ToolEntry, error) {
	e, ok := r.entries[name]
	if !ok {
		return capability.ToolEntry{}, fmt.Errorf("not found: %s", name)
	}
	return e, nil
}

func (r *mockToolRegistry) Close() error { return nil }

func TestCompositeRunnerV2_KnownTool_DelegatesToBase(t *testing.T) {
	base := &mockBaseRunner{
		knownTools: map[string]*tool.ToolResponse{
			"shell": tool.NewSuccess("hello"),
		},
	}
	runner := tools.NewCompositeRunnerV2(base, &mockToolRegistry{entries: map[string]capability.ToolEntry{}}, "/tmp")

	resp, err := runner.ExecuteV2(context.Background(), "shell", map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.String() != "hello" {
		t.Errorf("expected 'hello', got %q", resp.String())
	}
}

func TestCompositeRunnerV2_UnknownTool_Registry_ExecutesScript(t *testing.T) {
	// 一時ディレクトリにスクリプト作成
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(toolsDir, "my_tool.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho 'script_output'"), 0755); err != nil {
		t.Fatal(err)
	}

	registry := &mockToolRegistry{
		entries: map[string]capability.ToolEntry{
			"my_tool": {
				Name:      "my_tool",
				Platforms: []string{"linux", "darwin"},
			},
		},
	}
	base := &mockBaseRunner{knownTools: map[string]*tool.ToolResponse{}}
	runner := tools.NewCompositeRunnerV2(base, registry, dir)

	resp, err := runner.ExecuteV2(context.Background(), "my_tool", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("unexpected tool error: %s", resp.String())
	}
	if !containsString(resp.String(), "script_output") {
		t.Errorf("expected 'script_output' in output, got %q", resp.String())
	}
}

func TestCompositeRunnerV2_UnknownTool_NotInRegistry_ReturnsOriginalError(t *testing.T) {
	registry := &mockToolRegistry{entries: map[string]capability.ToolEntry{}}
	base := &mockBaseRunner{knownTools: map[string]*tool.ToolResponse{}}
	runner := tools.NewCompositeRunnerV2(base, registry, "/tmp")

	_, err := runner.ExecuteV2(context.Background(), "nonexistent", map[string]any{})
	if err == nil {
		t.Error("expected error for nonexistent tool")
	}
	if !containsString(err.Error(), "unknown tool") {
		t.Errorf("expected 'unknown tool' error, got %q", err.Error())
	}
}

func TestCompositeRunnerV2_RegisteredToolRejectsInvalidName(t *testing.T) {
	registry := &mockToolRegistry{
		entries: map[string]capability.ToolEntry{
			"../escape": {Name: "../escape"},
		},
	}
	base := &mockBaseRunner{knownTools: map[string]*tool.ToolResponse{}}
	runner := tools.NewCompositeRunnerV2(base, registry, t.TempDir())

	resp, err := runner.ExecuteV2(context.Background(), "../escape", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrValidationFailed {
		t.Fatalf("expected validation error, got %+v", resp)
	}
}

func TestCompositeRunnerV2_RegisteredToolRejectsBlockedScriptCommand(t *testing.T) {
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	scriptPath := filepath.Join(toolsDir, "danger.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\nrm -rf \"$1\"\n"), 0755); err != nil {
		t.Fatal(err)
	}

	registry := &mockToolRegistry{
		entries: map[string]capability.ToolEntry{
			"danger": {Name: "danger"},
		},
	}
	base := &mockBaseRunner{knownTools: map[string]*tool.ToolResponse{}}
	runner := tools.NewCompositeRunnerV2(base, registry, dir)

	resp, err := runner.ExecuteV2(context.Background(), "danger", map[string]any{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Error == nil || resp.Error.Code != tool.ErrPermissionDenied {
		t.Fatalf("expected permission denied, got %+v", resp)
	}
	if !containsString(resp.String(), "registered tool script rejected") {
		t.Fatalf("expected script rejection message, got %q", resp.String())
	}
}

func TestCompositeRunnerV2_ListTools_MergesBaseAndRegistry(t *testing.T) {
	registry := &mockToolRegistry{
		entries: map[string]capability.ToolEntry{
			"custom_tool": {Name: "custom_tool"},
		},
	}
	base := &mockBaseRunner{
		knownTools: map[string]*tool.ToolResponse{
			"shell": tool.NewSuccess("ok"),
		},
	}
	runner := tools.NewCompositeRunnerV2(base, registry, "/tmp")

	metas, err := runner.ListTools(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	names := make(map[string]bool)
	for _, m := range metas {
		names[m.ToolID] = true
	}
	if !names["shell"] {
		t.Error("expected 'shell' in ListTools")
	}
	if !names["custom_tool"] {
		t.Error("expected 'custom_tool' in ListTools")
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
