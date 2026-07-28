package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/providers/rencrowllm"
)

// createProviderFromConfig creates a provider for a logical RenCrow_LLM alias.
// Physical provider and model settings remain owned by RenCrow_LLM.
func createProviderFromConfig(cfg *config.Config, alias string) (llm.LLMProvider, error) {
	if cfg == nil {
		return nil, fmt.Errorf("config is required")
	}
	return rencrowllm.NewGatewayProviderWithOptions(
		llmGatewayAPIKey(cfg),
		strings.TrimSpace(alias),
		cfg.LLMGateway.BaseURL,
		time.Duration(cfg.LLMGateway.TimeoutSec)*time.Second,
	), nil
}

func llmGatewayAPIKey(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	envName := strings.TrimSpace(cfg.LLMGateway.APIKeyEnv)
	if envName == "" {
		return ""
	}
	return strings.TrimSpace(os.Getenv(envName))
}

// loadDotEnv loads a simple KEY=VALUE file without overwriting existing values.
func loadDotEnv(path string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
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
		if os.Getenv(key) == "" {
			_ = os.Setenv(key, val)
		}
	}
}
