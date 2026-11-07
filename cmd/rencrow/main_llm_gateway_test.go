package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

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
		t.Fatalf("RenCrow_LLM Gateway models = %q", got)
	}
}

func TestBuildPrimaryLLMProvidersUsesGatewayEvenWhenEnabledFlagIsFalse(t *testing.T) {
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
	cfg := &config.Config{
		LLMGateway: config.LLMGatewayConfig{Enabled: false, BaseURL: server.URL, TimeoutSec: 5},
	}
	providers := buildPrimaryLLMProviders(cfg, nil)
	if _, err := providers.Chat.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "ping"}}}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(models, ","); got != "mio" {
		t.Fatalf("RenCrow_LLM Gateway models = %q", got)
	}
}

func TestBuildPrimaryLLMProvidersUsesFixedKuroBoardChatBinding(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	providers := buildPrimaryLLMProviders(&config.Config{
		LLMGateway: config.LLMGatewayConfig{Enabled: true, BaseURL: server.URL, TimeoutSec: 5},
	}, nil)

	scope := domainconversation.ConversationScope{
		TurnID: modulecore.NewTurnID(), SessionID: modulecore.NewSessionID(), TraceID: modulecore.NewTraceID(),
		ActorID: "board:board-instance:peer-alpha", InputID: modulecore.NewMessageID(),
		GenerationID: "kuro-board-chat-request-provenance", Purpose: domainconversation.PurposeExternalPeer,
		PolicyVersion: "board-chat-test-v1",
	}
	if _, err := providers.BoardChat.GenerateConversation(context.Background(), scope, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "Reply in this thread."}},
	}); err != nil {
		t.Fatalf("GenerateConversation(Kuro BOARD CHAT): %v", err)
	}
	metadata, _ := payload["rencrow"].(map[string]any)
	for key, want := range map[string]string{
		"agent_id": "kuro", "execution_role": "board-chat", "execution_alias": "kuro-board-chat",
	} {
		if metadata[key] != want {
			t.Errorf("Kuro BOARD CHAT request metadata[%q] = %#v, want %q; all=%#v", key, metadata[key], want, metadata)
		}
	}
	if payload["model"] != "kuro-board-chat" {
		t.Fatalf("Kuro BOARD CHAT request model alias = %#v, want fixed alias kuro-board-chat", payload["model"])
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
