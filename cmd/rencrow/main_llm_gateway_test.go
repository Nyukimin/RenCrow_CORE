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

func TestBuildPrimaryLLMProvidersUsesConfiguredBoardChatBindings(t *testing.T) {
	requests := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		requests[payload["model"].(string)] = payload
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	actors := []string{"kuro", "aka", "ao", "kin", "gin", "lumina"}
	responders := make([]config.BoardCommunicationResponderConfig, 0, len(actors))
	for _, actor := range actors {
		responders = append(responders, config.BoardCommunicationResponderConfig{ActorID: actor})
	}
	providers := buildPrimaryLLMProviders(&config.Config{
		LLMGateway:         config.LLMGatewayConfig{Enabled: true, BaseURL: server.URL, TimeoutSec: 5},
		BoardCommunication: config.BoardCommunicationConfig{Enabled: true, Responders: responders},
	}, nil)
	if got := len(providers.BoardChats); got != len(actors) {
		t.Fatalf("configured board-chat provider count = %d, want %d", got, len(actors))
	}

	for _, actor := range actors {
		t.Run(actor, func(t *testing.T) {
			provider := providers.BoardChats[actor]
			if provider == nil {
				t.Fatalf("configured BOARD CHAT provider for %s is nil", actor)
			}
			if _, ok := any(provider).(llm.LLMProvider); ok {
				t.Fatalf("BOARD CHAT provider for %s exposes the Task-scoped LLMProvider", actor)
			}
			if _, ok := any(provider).(llm.ToolCallingProvider); ok {
				t.Fatalf("BOARD CHAT provider for %s exposes ToolCallingProvider", actor)
			}
			scope := domainconversation.ConversationScope{
				TurnID: modulecore.NewTurnID(), SessionID: modulecore.NewSessionID(), TraceID: modulecore.NewTraceID(),
				ActorID: "board:board-instance:peer-alpha", InputID: modulecore.NewMessageID(),
				GenerationID: actor + "-board-chat-request-provenance", Purpose: domainconversation.PurposeExternalPeer,
				PolicyVersion: "board-chat-test-v1",
			}
			if _, err := provider.GenerateConversation(context.Background(), scope, llm.GenerateRequest{
				Messages: []llm.Message{{Role: "user", Content: "Reply in this thread."}},
			}); err != nil {
				t.Fatalf("GenerateConversation(%s BOARD CHAT): %v", actor, err)
			}
			alias := actor + "-board-chat"
			payload := requests[alias]
			if payload == nil {
				t.Fatalf("Gateway request for exact alias %q is missing", alias)
			}
			metadata, _ := payload["rencrow"].(map[string]any)
			for key, want := range map[string]string{
				"agent_id": actor, "execution_role": "board-chat", "execution_alias": alias,
			} {
				if metadata[key] != want {
					t.Errorf("BOARD CHAT request metadata[%q] = %#v, want %q; all=%#v", key, metadata[key], want, metadata)
				}
			}
			if payload["model"] != alias {
				t.Fatalf("BOARD CHAT request model alias = %#v, want exact alias %q", payload["model"], alias)
			}
		})
	}
	if got := len(requests); got != len(actors) {
		t.Fatalf("Gateway request count = %d, want %d exact configured aliases", got, len(actors))
	}
}

func TestBuildPrimaryLLMProvidersCreatesBoardChatOnlyForConfiguredResponders(t *testing.T) {
	providers := buildPrimaryLLMProviders(&config.Config{
		BoardCommunication: config.BoardCommunicationConfig{Enabled: true, Responders: []config.BoardCommunicationResponderConfig{{ActorID: "shiro"}}},
	}, nil)
	if len(providers.BoardChats) != 0 {
		t.Fatalf("BOARD CHAT providers for ordinary responder = %#v, want none", providers.BoardChats)
	}
	providers = buildPrimaryLLMProviders(&config.Config{
		BoardCommunication: config.BoardCommunicationConfig{Enabled: false, Responders: []config.BoardCommunicationResponderConfig{{ActorID: "aka"}}},
	}, nil)
	if len(providers.BoardChats) != 0 {
		t.Fatalf("BOARD CHAT providers while disabled = %#v, want none", providers.BoardChats)
	}
}

func TestBoardChatUnknownGatewayAliasFailsWithoutFallback(t *testing.T) {
	var requestedAliases []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode Gateway request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		requestedAliases = append(requestedAliases, payload["model"].(string))
		http.Error(w, "alias not configured", http.StatusNotFound)
	}))
	defer server.Close()
	providers := buildPrimaryLLMProviders(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL, TimeoutSec: 5},
		BoardCommunication: config.BoardCommunicationConfig{Enabled: true, Responders: []config.BoardCommunicationResponderConfig{{ActorID: "aka"}}},
	}, nil)
	scope := domainconversation.ConversationScope{
		TurnID: modulecore.NewTurnID(), SessionID: modulecore.NewSessionID(), TraceID: modulecore.NewTraceID(),
		ActorID: "board:board-instance:peer-alpha", InputID: modulecore.NewMessageID(),
		GenerationID: "aka-board-chat-missing-alias", Purpose: domainconversation.PurposeExternalPeer,
		PolicyVersion: "board-chat-test-v1",
	}
	if _, err := providers.BoardChats["aka"].GenerateConversation(context.Background(), scope, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "Reply in this thread."}},
	}); err == nil {
		t.Fatal("unknown configured Gateway alias unexpectedly generated a reply")
	}
	if got := strings.Join(requestedAliases, ","); got != "aka-board-chat" {
		t.Fatalf("Gateway aliases attempted = %q, want only aka-board-chat", got)
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
