package llm

import (
	"testing"
	"time"
)

func TestBuildConversationTextProviderPlanUsesPrimaryWorker(t *testing.T) {
	got := BuildConversationTextProviderPlan(ConversationRuntimeConfig{
		LocalEnabled:       true,
		PrimaryWorkerReady: true,
		SummaryModel:       "summary",
		OllamaBaseURL:      "http://ollama",
	})
	if !got.UseWorker || got.Provider != ConversationTextProviderPrimaryWorker || got.Description != "primary Worker" {
		t.Fatalf("BuildConversationTextProviderPlan() = %#v, want primary worker", got)
	}
}

func TestBuildConversationTextProviderPlanUsesPrimaryWorkerForGateway(t *testing.T) {
	got := BuildConversationTextProviderPlan(ConversationRuntimeConfig{
		LocalEnabled:       false,
		PrimaryWorkerReady: true,
		SummaryModel:       "legacy-summary",
		OllamaBaseURL:      "http://legacy-ollama",
	})
	if !got.UseWorker || got.Provider != ConversationTextProviderPrimaryWorker {
		t.Fatalf("BuildConversationTextProviderPlan() = %#v, want gateway primary worker", got)
	}
}

func TestBuildConversationTextProviderPlanBuildsOllamaSummary(t *testing.T) {
	got := BuildConversationTextProviderPlan(ConversationRuntimeConfig{
		OllamaBaseURL: " http://127.0.0.1:11434 ",
		OllamaModel:   "chat-default",
		SummaryModel:  " summary-model ",
	})
	if got.UseWorker || got.Provider != ConversationTextProviderOllamaSummary {
		t.Fatalf("BuildConversationTextProviderPlan() = %#v, want ollama summary", got)
	}
	if got.BaseURL != "http://127.0.0.1:11434" || got.Model != "summary-model" || got.NumCtx != 32768 {
		t.Fatalf("BuildConversationTextProviderPlan() fields = %#v", got)
	}
}

func TestBuildConversationTextProviderPlanFallsBackToOllamaModel(t *testing.T) {
	got := BuildConversationTextProviderPlan(ConversationRuntimeConfig{
		OllamaBaseURL: "http://127.0.0.1:11434",
		OllamaModel:   "chat-default",
	})
	if got.Model != "chat-default" || got.Unavailable != "" {
		t.Fatalf("BuildConversationTextProviderPlan() = %#v, want ollama model fallback", got)
	}
}

func TestBuildConversationTextProviderPlanUnavailable(t *testing.T) {
	got := BuildConversationTextProviderPlan(ConversationRuntimeConfig{})
	if got.Unavailable == "" {
		t.Fatalf("BuildConversationTextProviderPlan() = %#v, want unavailable reason", got)
	}
}

func TestBuildConversationEmbedderPlanExplicitOllama(t *testing.T) {
	got := BuildConversationEmbedderPlan(ConversationRuntimeConfig{
		OllamaBaseURL: "http://ollama",
		EmbedProvider: " ollama ",
		EmbedModel:    " nomic ",
	})
	if got.Provider != ConversationEmbedProviderOllama || got.BaseURL != "http://ollama" || got.Model != "nomic" {
		t.Fatalf("BuildConversationEmbedderPlan() = %#v, want ollama", got)
	}
}

func TestBuildConversationEmbedderPlanExplicitOpenAI(t *testing.T) {
	got := BuildConversationEmbedderPlan(ConversationRuntimeConfig{
		LocalBaseURL:    "http://local-openai",
		LocalTimeoutSec: 30,
		EmbedProvider:   " openai ",
		EmbedBaseURL:    " http://embed ",
		EmbedModel:      "text-embedding-3-small",
	})
	if got.Provider != ConversationEmbedProviderOpenAI || got.BaseURL != "http://embed" || got.Timeout != 30*time.Second {
		t.Fatalf("BuildConversationEmbedderPlan() = %#v, want openai", got)
	}
}

func TestBuildConversationEmbedderPlanLocalOpenAI(t *testing.T) {
	got := BuildConversationEmbedderPlan(ConversationRuntimeConfig{
		LocalEnabled:    true,
		LocalProvider:   "",
		LocalBaseURL:    "http://local-openai",
		LocalTimeoutSec: 0,
		EmbedModel:      "embed",
	})
	if got.Provider != ConversationEmbedProviderLocalLLM || got.BaseURL != "http://local-openai" || got.Timeout != LocalDefaultTimeout {
		t.Fatalf("BuildConversationEmbedderPlan() = %#v, want local_llm openai-compatible", got)
	}
}

func TestBuildConversationEmbedderPlanLocalOllamaBaseURL(t *testing.T) {
	got := BuildConversationEmbedderPlan(ConversationRuntimeConfig{
		LocalEnabled:  true,
		LocalProvider: "ollama",
		LocalBaseURL:  "http://local-ollama",
		OllamaBaseURL: "http://legacy-ollama",
		EmbedModel:    "embed",
	})
	if got.Provider != ConversationEmbedProviderOllama || got.BaseURL != "http://local-ollama" {
		t.Fatalf("BuildConversationEmbedderPlan() = %#v, want local ollama base URL", got)
	}
}

func TestBuildConversationEmbedderPlanUnavailable(t *testing.T) {
	got := BuildConversationEmbedderPlan(ConversationRuntimeConfig{})
	if got.Unavailable == "" {
		t.Fatalf("BuildConversationEmbedderPlan() = %#v, want unavailable reason", got)
	}
}
