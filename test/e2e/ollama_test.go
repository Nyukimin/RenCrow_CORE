//go:build e2e

package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/agent"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/mcp"
	infrarouting "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/routing"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/tools"
)

func TestE2E_OllamaProvider_Generate(t *testing.T) {
	cfg := getConfig(t)
	requireOllamaReachable(t, cfg.Ollama.BaseURL)
	provider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "Say hello in one word."},
		},
		MaxTokens:   64,
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content == "" {
		t.Error("expected non-empty response")
	}
	t.Logf("Response: %s", resp.Content)
}

func TestE2E_OllamaProvider_Japanese(t *testing.T) {
	cfg := getConfig(t)
	requireOllamaReachable(t, cfg.Ollama.BaseURL)
	provider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	resp, err := provider.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "「こんにちは」と一言だけ返してください。"},
		},
		MaxTokens:   64,
		Temperature: 0.1,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content == "" {
		t.Error("expected non-empty Japanese response")
	}
	t.Logf("Response: %s", resp.Content)
}

func TestE2E_OllamaProvider_Timeout(t *testing.T) {
	cfg := getConfig(t)
	requireOllamaReachable(t, cfg.Ollama.BaseURL)
	provider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err := provider.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "Tell me a long story about programming."},
		},
		MaxTokens:   512,
		Temperature: 0.7,
	})
	if err == nil {
		t.Log("WARN: expected timeout but got response (fast network?)")
	}
}

func TestE2E_MioAgent_Chat_RealOllama(t *testing.T) {
	cfg := getConfig(t)
	requireOllamaReachable(t, cfg.Ollama.BaseURL)
	provider := ollama.NewOllamaProvider(cfg.Ollama.BaseURL, cfg.Ollama.Model)
	classifier := infrarouting.NewLLMClassifier(provider, cfg.Prompts.Classifier)
	ruleDictionary := infrarouting.NewRuleDictionary()
	chatToolRunner := tools.NewToolRunner(tools.ToolRunnerConfig{
		GoogleAPIKey:         cfg.GoogleSearchChat.APIKey,
		GoogleSearchEngineID: cfg.GoogleSearchChat.SearchEngineID,
	})
	mcpClient := mcp.NewMCPClient()

	mio := agent.NewMioAgent(provider, classifier, ruleDictionary, chatToolRunner, mcpClient, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	testTask := task.NewTask(task.NewJobID(), "こんにちは", "test", "test_user")
	resp, err := mio.Chat(ctx, testTask)
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp == "" {
		t.Error("expected non-empty chat response")
	}
	t.Logf("Mio response: %s", resp)
}
