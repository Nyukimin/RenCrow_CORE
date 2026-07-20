package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/claude"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/deepseek"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/openai"
)

// createProviderFromConfig は CoderConfig から LLM Provider を作成
func createProviderFromConfig(cfg config.CoderConfig) (llm.LLMProvider, error) {
	// APIKey が環境変数参照形式（${...}）の場合は展開
	apiKey := os.ExpandEnv(cfg.APIKey)

	switch cfg.Provider {
	case "deepseek":
		if apiKey == "" {
			return nil, fmt.Errorf("DeepSeek provider requires API key")
		}
		model := cfg.Model
		if model == "" {
			model = "deepseek-chat"
		}
		return deepseek.NewDeepSeekProvider(apiKey, model), nil

	case "openai":
		if apiKey == "" {
			return nil, fmt.Errorf("OpenAI provider requires API key")
		}
		model := cfg.Model
		if model == "" {
			model = "gpt-4"
		}
		return openai.NewOpenAIProvider(apiKey, model), nil

	case "claude":
		if apiKey == "" {
			return nil, fmt.Errorf("Claude provider requires API key")
		}
		model := cfg.Model
		if model == "" {
			model = "claude-3-5-sonnet-20241022"
		}
		return claude.NewClaudeProvider(apiKey, model), nil

	default:
		return nil, fmt.Errorf("unsupported provider: %s", cfg.Provider)
	}
}

// loadDotEnv は指定パスの.envファイルを読み込み、未設定の環境変数をセット
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return // ファイルがなければスキップ
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		if os.Getenv(key) == "" { // 既存の環境変数を上書きしない
			os.Setenv(key, val)
		}
	}
}
