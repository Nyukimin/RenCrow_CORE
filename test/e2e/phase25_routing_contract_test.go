//go:build e2e

package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/mcp"
	infrarouting "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func TestE2E_Phase25ExplicitCodeRoutes(t *testing.T) {
	cfg := getConfig(t)
	ollamaProvider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	mioAgent := agent.NewMioAgent(
		ollamaProvider,
		infrarouting.NewLLMClassifier(ollamaProvider, cfg.Prompts.Classifier),
		infrarouting.NewRuleDictionary(),
		tools.NewToolRunner(tools.ToolRunnerConfig{
			GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
			GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
		}),
		mcp.NewMCPClient(),
		nil,
	)

	tests := []struct {
		name    string
		message string
		want    routing.Route
	}{
		{"generic code", "/code パッチ案を作って", routing.RouteCODE},
		{"code1", "/code1 仕様設計を整理して", routing.RouteCODE1},
		{"code2", "/code2 実装して", routing.RouteCODE2},
		{"code3", "/code3 ブラウザ操作をレビューして", routing.RouteCODE3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()

			decision, err := mioAgent.DecideAction(ctx, task.NewTask(task.NewJobID(), tt.message, "test", "e2e-user"))
			if err != nil {
				t.Fatalf("DecideAction failed: %v", err)
			}
			if decision.Route != tt.want {
				t.Fatalf("route = %s, want %s (reason=%s)", decision.Route, tt.want, decision.Reason)
			}
			if decision.Confidence != 1.0 || decision.Reason != "Explicit command" {
				t.Fatalf("explicit command contract changed: confidence=%.2f reason=%q", decision.Confidence, decision.Reason)
			}
		})
	}
}
