package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type fakeConversationProvider struct {
	name string
}

func TestBuildConversationEmbedderUsesLocalOpenAIWhenLocalLLMEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "Embed" {
			t.Fatalf("unexpected model: %v", req["model"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float64{0.1, 0.2}}},
		})
	}))
	defer srv.Close()

	embedder, label := buildConversationEmbedder(&config.Config{
		LocalLLM: config.LocalLLMConfig{
			Enabled:    true,
			Provider:   "local_openai",
			BaseURL:    srv.URL,
			TimeoutSec: 1,
		},
		Conversation: config.ConversationConfig{EmbedModel: "Embed"},
	})
	if embedder == nil {
		t.Fatal("expected embedder")
	}
	if !strings.Contains(label, "local_llm embedding") {
		t.Fatalf("unexpected label: %s", label)
	}
	got, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 dims, got %d", len(got))
	}
}

func TestBuildConversationEmbedderUsesExplicitOllamaProvider(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/embeddings" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["model"] != "Embed" {
			t.Fatalf("unexpected model: %v", req["model"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"embedding": []float64{0.3, 0.4, 0.5},
		})
	}))
	defer srv.Close()

	embedder, label := buildConversationEmbedder(&config.Config{
		LocalLLM: config.LocalLLMConfig{
			Enabled:    true,
			Provider:   "local_openai",
			BaseURL:    "http://local-openai.invalid",
			TimeoutSec: 1,
		},
		Conversation: config.ConversationConfig{
			EmbedProvider: "ollama",
			EmbedBaseURL:  srv.URL,
			EmbedModel:    "Embed",
		},
	})
	if embedder == nil {
		t.Fatal("expected embedder")
	}
	if !strings.Contains(label, "conversation embedding ollama") {
		t.Fatalf("unexpected label: %s", label)
	}
	got, err := embedder.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("Embed failed: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 dims, got %d", len(got))
	}
}

func (f fakeConversationProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{Content: "ok"}, nil
}

func (f fakeConversationProvider) Name() string {
	return f.name
}

func TestBuildConversationTextProviderUsesLocalWorkerWhenLocalLLMEnabled(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	provider, label := buildConversationTextProvider(&config.Config{
		LocalLLM: config.LocalLLMConfig{Enabled: true},
	}, primaryLLMProviders{Worker: worker})

	if provider != worker {
		t.Fatalf("expected local Worker provider, got %#v", provider)
	}
	if label != "local_llm Worker" {
		t.Fatalf("unexpected label: %s", label)
	}
}
