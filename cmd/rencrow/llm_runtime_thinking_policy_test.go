package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func TestPrimaryLLMProviders_ApplyChatNoThinkChatWorkerLowAndWorkerDefault(t *testing.T) {
	var mu sync.Mutex
	requests := map[string]map[string]any{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		model, _ := payload["model"].(string)
		mu.Lock()
		requests[model] = payload
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{}
	cfg.LLMGateway.Enabled = true
	cfg.LLMGateway.BaseURL = server.URL
	cfg.LLMGateway.TimeoutSec = 10
	providers := buildPrimaryLLMProviders(cfg, nil)
	trueValue := map[string]any{
		"think":                true,
		"reasoning_effort":     "high",
		"chat_template_kwargs": map[string]any{"enable_thinking": true, "reasoning_effort": "high"},
	}
	for name, provider := range map[string]llm.LLMProvider{"mio": providers.Chat, "shiro": providers.ChatWorker} {
		if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
			Messages:        []llm.Message{{Role: "user", Content: "ping"}},
			ProviderOptions: trueValue,
		}); err != nil {
			t.Fatalf("Generate(%s): %v", name, err)
		}
	}
	if _, err := providers.Worker.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	}); err != nil {
		t.Fatalf("Generate(worker): %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	mioKwargs, _ := requests["mio"]["chat_template_kwargs"].(map[string]any)
	if requests["mio"]["think"] != false || mioKwargs["enable_thinking"] != false {
		t.Fatalf("Chat must force NO-Think: %#v", requests["mio"])
	}
	shiroKwargs, _ := requests["shiro"]["chat_template_kwargs"].(map[string]any)
	if requests["shiro"]["think"] != "low" || requests["shiro"]["reasoning_effort"] != "low" || shiroKwargs["enable_thinking"] != true || shiroKwargs["reasoning_effort"] != "low" {
		t.Fatalf("ChatWorker must force reasoning low: %#v", requests["shiro"])
	}
	if _, forced := requests["worker"]["think"]; forced {
		t.Fatalf("Worker must not receive a forced think value: %#v", requests["worker"])
	}
	if _, forced := requests["worker"]["chat_template_kwargs"]; forced {
		t.Fatalf("Worker must not receive forced chat_template_kwargs: %#v", requests["worker"])
	}
}
