package main

import (
	"context"
	"log"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	healthadapter "github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/health"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/tool"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/claude"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/deepseek"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/openai"
)

// buildHealthHandler は Health Check HTTP ハンドラを構築
func (d *Dependencies) buildHealthHandler(cfg *config.Config) *healthadapter.Handler {
	return healthadapter.NewHandler(buildHealthService(cfg))
}

// resolveSubagentProvider はサブエージェント用のToolCallingProviderを設定に基づいて選択する
func resolveSubagentProvider(cfg *config.Config, fallback llm.ToolCallingProvider) llm.ToolCallingProvider {
	switch cfg.Subagent.Provider {
	case "claude":
		if cfg.Claude.APIKey == "" {
			log.Fatalf("subagent.provider=claude but claude.api_key is not set")
		}
		model := cfg.Subagent.Model
		if model == "" {
			model = cfg.Claude.Model
		}
		return claude.NewClaudeProvider(cfg.Claude.APIKey, model)

	case "openai":
		if cfg.OpenAI.APIKey == "" {
			log.Fatalf("subagent.provider=openai but openai.api_key is not set")
		}
		model := cfg.Subagent.Model
		if model == "" {
			model = cfg.OpenAI.Model
		}
		return openai.NewOpenAIProvider(cfg.OpenAI.APIKey, model)

	case "deepseek":
		if cfg.DeepSeek.APIKey == "" {
			log.Fatalf("subagent.provider=deepseek but deepseek.api_key is not set")
		}
		model := cfg.Subagent.Model
		if model == "" {
			model = cfg.DeepSeek.Model
		}
		return deepseek.NewDeepSeekProvider(cfg.DeepSeek.APIKey, model)

	default: // "ollama" or empty
		return fallback
	}
}

// mustGetToolList はツールリストを取得（エラーは無視）
func mustGetToolList(runner tool.RunnerV2) []string {
	metas, _ := runner.ListTools(context.Background())
	list := make([]string, 0, len(metas))
	for _, meta := range metas {
		list = append(list, meta.ToolID)
	}
	return list
}
