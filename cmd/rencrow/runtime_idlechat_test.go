package main

import (
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
)

func TestSelectForecastProviderPrefersCoderPriorityOverWorker(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	chat := fakeConversationProvider{name: "chat-provider"}
	provider, label := selectForecastProvider(&config.Config{
		LocalLLM: config.LocalLLMConfig{
			Enabled:     true,
			WorkerModel: "Worker",
			ChatModel:   "Chat",
		},
		Coder2: config.CoderConfig{
			Enabled:  true,
			Provider: "openai",
			Model:    "gpt-4o-mini",
			APIKey:   "test-key",
		},
		Coder1: config.CoderConfig{
			Enabled:  true,
			Provider: "local_openai",
			Model:    "Worker",
			BaseURL:  "http://127.0.0.1:8082",
		},
	}, chat, worker, nil)

	if provider == nil || provider == worker || provider == chat {
		t.Fatalf("expected Coder1 provider, got %#v", provider)
	}
	if !strings.Contains(label, "Coder1") || !strings.Contains(label, "local_openai") {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestSelectForecastProviderSkipsBrokenCoderAndUsesNextLocalCoder(t *testing.T) {
	primary, primaryLabel := selectForecastProviders(&config.Config{
		Coder1: config.CoderConfig{
			Enabled:  true,
			Provider: "local_openai",
			Model:    "Worker",
		},
		Coder2: config.CoderConfig{
			Enabled:  true,
			Provider: "local_openai",
			Model:    "Worker",
			BaseURL:  "http://127.0.0.1:8082",
		},
	})

	if primary == nil {
		t.Fatal("expected Coder2 local provider")
	}
	if !strings.Contains(primaryLabel, "Coder2") {
		t.Fatalf("unexpected primary label: %q", primaryLabel)
	}
}

func TestSelectForecastProviderSkipsOpenAIByDefault(t *testing.T) {
	primary, primaryLabel := selectForecastProviders(&config.Config{
		Coder1: config.CoderConfig{
			Enabled:  true,
			Provider: "local_openai",
			Model:    "Worker",
		},
		Coder2: config.CoderConfig{
			Enabled:  true,
			Provider: "openai",
			Model:    "gpt-4o-mini",
			APIKey:   "test-key",
		},
	})

	if primary != nil || primaryLabel != "" {
		t.Fatalf("OpenAI explicit use disabled by default; got provider=%#v label=%q", primary, primaryLabel)
	}
}

func TestSelectForecastProviderUsesOpenAIOnlyWhenExternalEnabled(t *testing.T) {
	primary, primaryLabel := selectForecastProviders(&config.Config{
		IdleChat: config.IdleChatConfig{
			ForecastExternalEnabled: true,
		},
		Coder1: config.CoderConfig{
			Enabled:  true,
			Provider: "local_openai",
			Model:    "Worker",
		},
		Coder2: config.CoderConfig{
			Enabled:  true,
			Provider: "openai",
			Model:    "gpt-4o-mini",
			APIKey:   "test-key",
		},
	})

	if primary == nil {
		t.Fatal("expected Coder2 OpenAI provider when external use is explicitly enabled")
	}
	if !strings.Contains(primaryLabel, "Coder2 openai") || !strings.Contains(primaryLabel, "gpt-4o-mini") {
		t.Fatalf("unexpected primary label: %q", primaryLabel)
	}
}

func TestSelectForecastProviderDoesNotUseChatWhenNoCoderAvailable(t *testing.T) {
	chat := fakeConversationProvider{name: "chat-provider"}
	provider, label := selectForecastProvider(&config.Config{
		LocalLLM: config.LocalLLMConfig{
			Enabled:   true,
			ChatModel: "Chat",
		},
	}, chat, nil, nil)

	if provider != nil {
		t.Fatalf("Forecast must not fall back to Chat provider, got %#v", provider)
	}
	if label != "" {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestSelectForecastProviderUsesWorkerWhenNoLocalCoderAvailableAtRuntime(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	chat := fakeConversationProvider{name: "chat-provider"}
	provider, label := selectForecastProvider(&config.Config{
		Coder2: config.CoderConfig{
			Enabled:  true,
			Provider: "openai",
			Model:    "gpt-4o-mini",
			APIKey:   "test-key",
		},
	}, chat, worker, nil)

	if provider != worker {
		t.Fatalf("expected Worker local provider, got %#v", provider)
	}
	if label != modulechat.ForecastWorkerFallbackLabel {
		t.Fatalf("unexpected label: %q", label)
	}
}

func TestCoderProviderIsExternal(t *testing.T) {
	tests := []struct {
		provider string
		want     bool
	}{
		{provider: "local_openai", want: false},
		{provider: "ollama", want: false},
		{provider: "openai", want: true},
		{provider: "claude", want: true},
		{provider: "deepseek", want: true},
	}
	for _, tt := range tests {
		got := coderProviderIsExternal(config.CoderConfig{Provider: tt.provider})
		if got != tt.want {
			t.Fatalf("coderProviderIsExternal(%q)=%t, want %t", tt.provider, got, tt.want)
		}
	}
}
