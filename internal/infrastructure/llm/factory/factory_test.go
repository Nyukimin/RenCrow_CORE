package factory

import (
	"testing"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/adapter/config"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/claude"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/deepseek"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/gemini"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/ollama"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/llm/providers/openai"
)

func TestCreateProvider_Disabled(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "deepseek",
		Model:    "test-model",
		APIKey:   "test-key",
		Enabled:  false,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Errorf("Expected no error for disabled config, got: %v", err)
	}
	if provider != nil {
		t.Errorf("Expected nil provider for disabled config, got: %T", provider)
	}
}

func TestCreateProvider_DeepSeek(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "deepseek",
		Model:    "deepseek-coder",
		APIKey:   "test-key",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}

	if _, ok := provider.(*deepseek.DeepSeekProvider); !ok {
		t.Errorf("Expected *deepseek.DeepSeekProvider, got %T", provider)
	}
}

func TestCreateProvider_OpenAI(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "openai",
		Model:    "gpt-4-turbo",
		APIKey:   "test-key",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}

	if _, ok := provider.(*openai.OpenAIProvider); !ok {
		t.Errorf("Expected *openai.OpenAIProvider, got %T", provider)
	}
}

func TestCreateProvider_LocalOpenAI(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "local_openai",
		Model:    "Worker",
		BaseURL:  "http://127.0.0.1:8080",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}

	if _, ok := provider.(*openai.OpenAIProvider); !ok {
		t.Errorf("Expected *openai.OpenAIProvider, got %T", provider)
	}
}

func TestCreateProvider_Claude(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "claude",
		Model:    "claude-sonnet-4",
		APIKey:   "test-key",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}

	if _, ok := provider.(*claude.ClaudeProvider); !ok {
		t.Errorf("Expected *claude.ClaudeProvider, got %T", provider)
	}
}

func TestCreateProvider_Gemini(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "gemini",
		Model:    "gemini-2.0-flash-exp",
		APIKey:   "test-key",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}

	if _, ok := provider.(*gemini.Provider); !ok {
		t.Errorf("Expected *gemini.Provider, got %T", provider)
	}
}

func TestCreateProvider_Ollama(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "ollama",
		Model:    "qwen3.5:9b",
		BaseURL:  "http://localhost:11434",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err != nil {
		t.Fatalf("CreateProvider failed: %v", err)
	}

	if _, ok := provider.(*ollama.OllamaProvider); !ok {
		t.Errorf("Expected *ollama.OllamaProvider, got %T", provider)
	}
}

func TestCreateProvider_UnknownProvider(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "unknown-provider",
		Model:    "test-model",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err == nil {
		t.Error("Expected error for unknown provider")
	}
	if provider != nil {
		t.Errorf("Expected nil provider for unknown provider, got %T", provider)
	}
}

func TestCreateProvider_MissingAPIKey(t *testing.T) {
	tests := []struct {
		name     string
		provider string
	}{
		{"DeepSeek", "deepseek"},
		{"OpenAI", "openai"},
		{"Claude", "claude"},
		{"Gemini", "gemini"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cc := config.CoderConfig{
				Provider: tt.provider,
				Model:    "test-model",
				APIKey:   "", // Missing API key
				Enabled:  true,
			}

			provider, err := CreateProvider(cc)
			if err == nil {
				t.Errorf("Expected error for missing API key")
			}
			if provider != nil {
				t.Errorf("Expected nil provider, got %T", provider)
			}
		})
	}
}

func TestCreateProvider_OllamaMissingBaseURL(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "ollama",
		Model:    "test-model",
		BaseURL:  "", // Missing BaseURL
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err == nil {
		t.Error("Expected error for missing BaseURL")
	}
	if provider != nil {
		t.Errorf("Expected nil provider, got %T", provider)
	}
}

func TestCreateProvider_LocalOpenAIMissingBaseURL(t *testing.T) {
	cc := config.CoderConfig{
		Provider: "local_openai",
		Model:    "Worker",
		BaseURL:  "",
		Enabled:  true,
	}

	provider, err := CreateProvider(cc)
	if err == nil {
		t.Error("Expected error for missing BaseURL")
	}
	if provider != nil {
		t.Errorf("Expected nil provider, got %T", provider)
	}
}
