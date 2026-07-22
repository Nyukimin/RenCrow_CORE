package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
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

func TestBuildPrimaryLLMProvidersUsesGatewayAgentIDs(t *testing.T) {
	var models []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		models = append(models, body["model"].(string))
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	cfg := &config.Config{LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: server.URL, TimeoutSec: 5}}

	providers := buildPrimaryLLMProviders(cfg, nil)
	requests := []struct {
		provider interface {
			Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error)
		}
		want string
	}{
		{providers.Chat, "mio"},
		{providers.Worker, "worker"},
		{providers.ChatWorker, "shiro"},
		{providers.Heavy, "kuro"},
		{providers.Wild, "midori"},
	}
	for _, item := range requests {
		if _, err := item.provider.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "ping"}}}); err != nil {
			t.Fatalf("Generate(%s): %v", item.want, err)
		}
	}
	if got := strings.Join(models, ","); got != "mio,worker,shiro,kuro,midori" {
		t.Fatalf("Gateway models = %q", got)
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
