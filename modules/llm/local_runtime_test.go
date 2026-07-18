package llm

import (
	"testing"
	"time"
)

func TestLocalBaseURLForAliasUsesRoleOverride(t *testing.T) {
	cfg := LocalRuntimeConfig{
		BaseURL:           "http://192.168.1.31:8081",
		ChatBaseURL:       "http://192.168.1.31:8081",
		WorkerBaseURL:     "http://192.168.1.31:8082",
		ChatWorkerBaseURL: "http://192.168.1.31:18082",
		HeavyBaseURL:      "http://192.168.1.31:8083",
		WildBaseURL:       "http://192.168.1.31:8084",
	}

	cases := map[string]string{
		"Chat":       "http://192.168.1.31:8081",
		"Worker":     "http://192.168.1.31:8082",
		"ChatWorker": "http://192.168.1.31:18082",
		"Heavy":      "http://192.168.1.31:8083",
		"Wild":       "http://192.168.1.31:8084",
	}
	for alias, want := range cases {
		if got := LocalBaseURLForAlias(cfg, alias); got != want {
			t.Fatalf("%s base url = %s, want %s", alias, got, want)
		}
	}
}

func TestLocalHeavyFallsBackToWorkerBaseAndModel(t *testing.T) {
	cfg := LocalRuntimeConfig{
		BaseURL:         "http://192.168.1.31:8081",
		WorkerBaseURL:   "http://192.168.1.31:8082",
		WorkerModel:     "Worker",
		ChatWorkerModel: "ChatWorker",
		HeavyModel:      "Heavy",
	}

	if got := LocalBaseURLForAlias(cfg, "Heavy"); got != "http://192.168.1.31:8082" {
		t.Fatalf("heavy base url = %s", got)
	}
	if got := LocalModelForAlias(cfg, "Heavy"); got != "Worker" {
		t.Fatalf("heavy model = %s", got)
	}
	if got := LocalModelForAlias(cfg, "ChatWorker"); got != "ChatWorker" {
		t.Fatalf("chatworker model = %s", got)
	}
}

func TestLocalChatWorkerModelFallsBackToWorkerModel(t *testing.T) {
	cfg := LocalRuntimeConfig{WorkerModel: "Worker"}

	if got := LocalModelForAlias(cfg, "ChatWorker"); got != "Worker" {
		t.Fatalf("chatworker fallback model = %s", got)
	}
}

func TestLocalTimeoutForAliasUsesRoleSpecificTimeouts(t *testing.T) {
	cfg := LocalRuntimeConfig{TimeoutSec: 120}
	cases := map[string]time.Duration{
		"Chat":       120 * time.Second,
		"ChatWorker": 120 * time.Second,
		"Wild":       120 * time.Second,
		"Heavy":      120 * time.Second,
		"Worker":     120 * time.Second,
	}
	for alias, want := range cases {
		if got := LocalTimeoutForAlias(cfg, alias); got != want {
			t.Fatalf("%s timeout = %s, want %s", alias, got, want)
		}
	}
}

func TestLocalChatUsesDedicatedTimeoutAndContext(t *testing.T) {
	cfg := LocalRuntimeConfig{
		TimeoutSec:       120,
		ChatTimeoutSec:   300,
		ModelContext:     131072,
		ChatModelContext: 4096,
	}

	if got := LocalTimeoutForAlias(cfg, "Chat"); got != 300*time.Second {
		t.Fatalf("Chat timeout = %s, want 300s", got)
	}
	if got := LocalTimeoutForAlias(cfg, "Worker"); got != 120*time.Second {
		t.Fatalf("Worker timeout = %s, want 120s", got)
	}
	if got := LocalModelContextForAlias(cfg, "Chat"); got != 4096 {
		t.Fatalf("Chat context = %d, want 4096", got)
	}
	if got := LocalModelContextForAlias(cfg, "Worker"); got != 131072 {
		t.Fatalf("Worker context = %d, want 131072", got)
	}
}

func TestLocalQueueTimeoutForAliasUsesRoleSpecificTimeouts(t *testing.T) {
	cases := map[string]time.Duration{
		"Chat":       time.Second,
		"ChatWorker": 2 * time.Second,
		"Wild":       2 * time.Second,
		"Heavy":      5 * time.Second,
		"Worker":     5 * time.Second,
	}
	for alias, want := range cases {
		if got := LocalQueueTimeoutForAlias(alias); got != want {
			t.Fatalf("%s queue timeout = %s, want %s", alias, got, want)
		}
	}
}

func TestBuildLocalAliasConfigNormalizesProviderAndConcurrency(t *testing.T) {
	got := BuildLocalAliasConfig(LocalRuntimeConfig{
		Provider:         "ollama",
		BaseURL:          "http://127.0.0.1:11434",
		ChatModel:        "chat-model",
		ModelConcurrency: 2,
	}, "Chat")

	if got.Provider != LocalProviderOllama || got.Model != "chat-model" || got.Concurrency != 2 || got.NumCtx != LocalOllamaDefaultNumCtx {
		t.Fatalf("unexpected alias config: %+v", got)
	}
	if got.QueueTimeout != LocalChatQueueTimeout || got.QueuePolicy != LocalQueuePolicyWait {
		t.Fatalf("unexpected alias config: %+v", got)
	}
}

func TestBuildLocalAliasConfigUsesConfiguredModelContext(t *testing.T) {
	got := BuildLocalAliasConfig(LocalRuntimeConfig{
		Provider:     "local_openai",
		ChatModel:    "Chat",
		ModelContext: 131072,
	}, "Chat")

	if got.NumCtx != 131072 {
		t.Fatalf("num_ctx = %d, want 131072", got.NumCtx)
	}
}
