package main

import (
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func TestLocalLLMBaseURLForAlias_UsesRoleOverride(t *testing.T) {
	cfg := &config.Config{
		LocalLLM: config.LocalLLMConfig{
			BaseURL:       "http://192.168.1.31:8081",
			ChatBaseURL:   "http://192.168.1.31:8081",
			WorkerBaseURL: "http://192.168.1.31:8082",
			HeavyBaseURL:  "http://192.168.1.31:8083",
			WildBaseURL:   "http://192.168.1.31:8084",
		},
	}

	localCfg := localRuntimeConfigFromAppConfig(cfg)
	if got := modulellm.LocalBaseURLForAlias(localCfg, "Chat"); got != "http://192.168.1.31:8081" {
		t.Fatalf("unexpected chat base url: %s", got)
	}
	if got := modulellm.LocalBaseURLForAlias(localCfg, "Worker"); got != "http://192.168.1.31:8082" {
		t.Fatalf("unexpected worker base url: %s", got)
	}
	if got := modulellm.LocalBaseURLForAlias(localCfg, "Heavy"); got != "http://192.168.1.31:8083" {
		t.Fatalf("unexpected heavy base url: %s", got)
	}
	if got := modulellm.LocalBaseURLForAlias(localCfg, "Wild"); got != "http://192.168.1.31:8084" {
		t.Fatalf("unexpected wild base url: %s", got)
	}
}

func TestLocalLLMBaseURLForAlias_HeavyFallsBackToWorker(t *testing.T) {
	cfg := &config.Config{
		LocalLLM: config.LocalLLMConfig{
			BaseURL:       "http://192.168.1.31:8081",
			WorkerBaseURL: "http://192.168.1.31:8082",
			WorkerModel:   "Worker",
			HeavyModel:    "Heavy",
		},
	}

	localCfg := localRuntimeConfigFromAppConfig(cfg)
	if got := modulellm.LocalBaseURLForAlias(localCfg, "Heavy"); got != "http://192.168.1.31:8082" {
		t.Fatalf("unexpected heavy fallback base url: %s", got)
	}
	if got := modulellm.LocalModelForAlias(localCfg, "Heavy"); got != "Worker" {
		t.Fatalf("unexpected heavy fallback model: %s", got)
	}
}

func TestLocalLLMBaseURLForAlias_FallsBackToBaseURL(t *testing.T) {
	cfg := &config.Config{
		LocalLLM: config.LocalLLMConfig{
			BaseURL: "http://192.168.1.31:8081",
		},
	}

	localCfg := localRuntimeConfigFromAppConfig(cfg)
	if got := modulellm.LocalBaseURLForAlias(localCfg, "Worker"); got != "http://192.168.1.31:8081" {
		t.Fatalf("unexpected fallback base url: %s", got)
	}
}

func TestLocalLLMTimeoutForAlias_UsesRoleSpecificTimeouts(t *testing.T) {
	cfg := &config.Config{
		LocalLLM: config.LocalLLMConfig{
			TimeoutSec: 120,
		},
	}

	// TimeoutSec=120 はすべてのロール（Chat/Wild/Heavy/Worker）に適用される
	cases := map[string]time.Duration{
		"Chat":   120 * time.Second,
		"Wild":   120 * time.Second,
		"Heavy":  120 * time.Second,
		"Worker": 120 * time.Second,
	}
	for alias, want := range cases {
		if got := modulellm.LocalTimeoutForAlias(localRuntimeConfigFromAppConfig(cfg), alias); got != want {
			t.Fatalf("%s timeout = %s, want %s", alias, got, want)
		}
	}
}

func TestLocalLLMTimeoutForAlias_DefaultsWorkerTo120Seconds(t *testing.T) {
	if got := modulellm.LocalTimeoutForAlias(localRuntimeConfigFromAppConfig(&config.Config{}), "Worker"); got != 120*time.Second {
		t.Fatalf("Worker timeout = %s, want 120s", got)
	}
}

func TestResolveCoderPersonalityUsesCharacterBundle(t *testing.T) {
	cfg := &config.Config{
		Prompts: &config.LoadedPrompts{
			CharacterPrompts: map[string]string{
				"aka": "aka character prompt",
			},
		},
	}

	got, source := resolveCoderPersonality(cfg, config.CoderConfig{Name: "Aka"})
	if got != "aka character prompt" {
		t.Fatalf("personality = %q, want character prompt", got)
	}
	if source != "character bundle: aka" {
		t.Fatalf("source = %q, want character bundle", source)
	}
}

func TestResolveCoderPersonalityPrefersInlineOverCharacterBundle(t *testing.T) {
	cfg := &config.Config{
		Prompts: &config.LoadedPrompts{
			CharacterPrompts: map[string]string{
				"aka": "aka character prompt",
			},
		},
	}

	got, source := resolveCoderPersonality(cfg, config.CoderConfig{Name: "aka", Personality: "inline prompt"})
	if got != "inline prompt" {
		t.Fatalf("personality = %q, want inline prompt", got)
	}
	if source != "inline personality" {
		t.Fatalf("source = %q, want inline personality", source)
	}
}
