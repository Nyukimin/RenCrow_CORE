package tools

import (
	"context"
	"fmt"
	"testing"
)

func TestSubagent_Execute(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		Subagents: map[string]SubagentFunc{
			"echo": func(ctx context.Context, message string) (string, error) {
				return "echo: " + message, nil
			},
		},
	})

	result, err := runner.Execute(context.Background(), "subagent", map[string]interface{}{
		"agent":   "echo",
		"message": "hello",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "echo: hello" {
		t.Errorf("result = %q, want %q", result, "echo: hello")
	}
}

func TestSubagent_UnknownAgent(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		Subagents: map[string]SubagentFunc{
			"echo": func(ctx context.Context, message string) (string, error) {
				return "", nil
			},
		},
	})

	_, err := runner.Execute(context.Background(), "subagent", map[string]interface{}{
		"agent":   "nonexistent",
		"message": "hello",
	})
	if err == nil {
		t.Error("expected error for unknown agent")
	}
}

func TestSubagent_MissingArgs(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		Subagents: map[string]SubagentFunc{
			"echo": func(ctx context.Context, message string) (string, error) {
				return "", nil
			},
		},
	})

	// Missing agent
	_, err := runner.Execute(context.Background(), "subagent", map[string]interface{}{
		"message": "hello",
	})
	if err == nil {
		t.Error("expected error for missing agent")
	}

	// Missing message
	_, err = runner.Execute(context.Background(), "subagent", map[string]interface{}{
		"agent": "echo",
	})
	if err == nil {
		t.Error("expected error for missing message")
	}
}

func TestSubagent_NotRegisteredWithoutConfig(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{})

	_, err := runner.Execute(context.Background(), "subagent", map[string]interface{}{
		"agent":   "echo",
		"message": "hello",
	})
	if err == nil {
		t.Error("expected error when subagent tool is not registered")
	}
}

func TestSubagent_AgentError(t *testing.T) {
	runner := NewToolRunner(ToolRunnerConfig{
		Subagents: map[string]SubagentFunc{
			"fail": func(ctx context.Context, message string) (string, error) {
				return "", fmt.Errorf("agent failed")
			},
		},
	})

	_, err := runner.Execute(context.Background(), "subagent", map[string]interface{}{
		"agent":   "fail",
		"message": "hello",
	})
	if err == nil {
		t.Error("expected error from failing agent")
	}
}
