package tools_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/capability"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func newRegisterToolRunner(t *testing.T, registry *mockToolRegistry) (*tools.ToolRunner, string) {
	t.Helper()
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolsDir, 0755); err != nil {
		t.Fatal(err)
	}
	cfg := tools.ToolRunnerConfig{
		ToolRegistry:     registry,
		WorkspaceDir:     dir,
		DisableWebSearch: true,
	}
	return tools.NewToolRunner(cfg), dir
}

func TestRegisterTool_InvalidName_ReturnsError(t *testing.T) {
	reg := &mockToolRegistry{entries: map[string]capability.ToolEntry{}}
	runner, _ := newRegisterToolRunner(t, reg)

	resp, err := runner.ExecuteV2(context.Background(), "register_tool", map[string]any{
		"name":        "bad name!", // invalid
		"description": "a tool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError() {
		t.Error("expected error response for invalid name")
	}
}

func TestRegisterTool_MissingScript_ReturnsError(t *testing.T) {
	reg := &mockToolRegistry{entries: map[string]capability.ToolEntry{}}
	runner, _ := newRegisterToolRunner(t, reg)

	resp, err := runner.ExecuteV2(context.Background(), "register_tool", map[string]any{
		"name":        "nonexistent_tool",
		"description": "a tool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsError() {
		t.Error("expected error response for missing script")
	}
}

func TestRegisterTool_Success(t *testing.T) {
	reg := &mockToolRegistry{entries: map[string]capability.ToolEntry{}}
	runner, dir := newRegisterToolRunner(t, reg)

	scriptPath := filepath.Join(dir, "tools", "my_tool.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/sh\necho ok"), 0755); err != nil {
		t.Fatal(err)
	}

	resp, err := runner.ExecuteV2(context.Background(), "register_tool", map[string]any{
		"name":        "my_tool",
		"description": "a test tool",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.IsError() {
		t.Fatalf("unexpected tool error: %s", resp.String())
	}

	_, getErr := reg.Get(context.Background(), "my_tool")
	if getErr != nil {
		t.Fatalf("tool not found in registry: %v", getErr)
	}
}
