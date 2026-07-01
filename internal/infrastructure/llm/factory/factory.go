package factory

import (
	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/claude"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/deepseek"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/gemini"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/openai"
	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

// CreateProvider は CoderConfig から適切な LLMProvider を生成する。
// Enabled=false の場合は nil を返す（エラーではない）。
// Provider が未知の場合はエラーを返す。
func CreateProvider(cc config.CoderConfig) (llm.LLMProvider, error) {
	plan, err := modulellm.BuildCoderProviderPlan(modulellm.CoderProviderConfig{
		Enabled:  cc.Enabled,
		Provider: cc.Provider,
		Model:    cc.Model,
		APIKey:   cc.APIKey,
		BaseURL:  cc.BaseURL,
	})
	if err != nil {
		return nil, err
	}
	if !plan.Enabled {
		return nil, nil
	}

	switch plan.Provider {
	case modulellm.CoderProviderDeepSeek:
		return deepseek.NewDeepSeekProvider(plan.APIKey, plan.Model), nil
	case modulellm.CoderProviderOpenAI:
		return openai.NewOpenAIProvider(plan.APIKey, plan.Model), nil
	case modulellm.CoderProviderLocalOpenAI:
		return openai.NewOpenAIProviderWithOptions(plan.APIKey, plan.Model, plan.BaseURL, plan.Timeout), nil
	case modulellm.CoderProviderClaude:
		return claude.NewClaudeProvider(plan.APIKey, plan.Model), nil
	case modulellm.CoderProviderGemini:
		return gemini.NewProvider(plan.APIKey, plan.Model), nil
	case modulellm.CoderProviderOllama:
		return ollama.NewOllamaProvider(plan.BaseURL, plan.Model), nil
	}
	return nil, nil
}
