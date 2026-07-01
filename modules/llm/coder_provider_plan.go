package llm

import (
	"fmt"
	"strings"
	"time"
)

const (
	CoderProviderDeepSeek    = "deepseek"
	CoderProviderOpenAI      = "openai"
	CoderProviderLocalOpenAI = "local_openai"
	CoderProviderClaude      = "claude"
	CoderProviderGemini      = "gemini"
	CoderProviderOllama      = "ollama"

	CoderLocalOpenAITimeout = 120 * time.Second
)

type CoderProviderConfig struct {
	Enabled  bool
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
}

type CoderProviderPlan struct {
	Enabled  bool
	Provider string
	Model    string
	APIKey   string
	BaseURL  string
	Timeout  time.Duration
}

func BuildCoderProviderPlan(cfg CoderProviderConfig) (CoderProviderPlan, error) {
	if !cfg.Enabled {
		return CoderProviderPlan{}, nil
	}
	provider := strings.ToLower(strings.TrimSpace(cfg.Provider))
	plan := CoderProviderPlan{
		Enabled:  true,
		Provider: provider,
		Model:    strings.TrimSpace(cfg.Model),
		APIKey:   strings.TrimSpace(cfg.APIKey),
		BaseURL:  strings.TrimSpace(cfg.BaseURL),
	}
	switch provider {
	case CoderProviderDeepSeek, CoderProviderOpenAI, CoderProviderClaude, CoderProviderGemini:
		if plan.APIKey == "" {
			return CoderProviderPlan{}, fmt.Errorf("%s provider requires api_key", provider)
		}
	case CoderProviderLocalOpenAI:
		if plan.BaseURL == "" {
			return CoderProviderPlan{}, fmt.Errorf("%s provider requires base_url", provider)
		}
		plan.Timeout = CoderLocalOpenAITimeout
	case CoderProviderOllama:
		if plan.BaseURL == "" {
			return CoderProviderPlan{}, fmt.Errorf("%s provider requires base_url", provider)
		}
	default:
		return CoderProviderPlan{}, fmt.Errorf("unknown provider: %s", strings.TrimSpace(cfg.Provider))
	}
	return plan, nil
}
