//go:build e2e

package e2e_test

import (
	"context"
	"testing"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/claude"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/deepseek"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/openai"
)

func TestE2E_APIProvider_Generate(t *testing.T) {
	cfg := getConfig(t)

	cases := []struct {
		name     string
		apiKey   string
		provider llm.LLMProvider
	}{
		{"Claude", cfg.Claude.APIKey, claude.NewClaudeProvider(cfg.Claude.APIKey, cfg.Claude.Model)},
		{"DeepSeek", cfg.DeepSeek.APIKey, deepseek.NewDeepSeekProvider(cfg.DeepSeek.APIKey, cfg.DeepSeek.Model)},
		{"OpenAI", cfg.OpenAI.APIKey, openai.NewOpenAIProvider(cfg.OpenAI.APIKey, cfg.OpenAI.Model)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.apiKey == "" {
				t.Skipf("%s API key not configured", tc.name)
			}

			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			resp, err := tc.provider.Generate(ctx, llm.GenerateRequest{
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
			t.Logf("%s response: %s", tc.name, resp.Content)
		})
	}
}
