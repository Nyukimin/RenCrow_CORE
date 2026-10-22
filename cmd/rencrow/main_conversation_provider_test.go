package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	llmmiddleware "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/llm/middleware"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type fakeConversationProvider struct {
	name string
}

func TestBuildConversationEmbedderUsesRenCrowLLMGateway(t *testing.T) {
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
		LLMGateway:   config.LLMGatewayConfig{BaseURL: srv.URL, TimeoutSec: 1},
		Conversation: config.ConversationConfig{EmbedModel: "Embed"},
	})
	if embedder == nil {
		t.Fatal("expected embedder")
	}
	if !strings.Contains(label, "RenCrow_LLM embedding alias") {
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

func TestBuildConversationEmbedderUsesConfiguredGatewayAlias(t *testing.T) {
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
			"data": []map[string]any{{"embedding": []float64{0.3, 0.4, 0.5}}},
		})
	}))
	defer srv.Close()

	embedder, label := buildConversationEmbedder(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: srv.URL, TimeoutSec: 1},
		Conversation: config.ConversationConfig{
			EmbedModel: "Embed",
		},
	})
	if embedder == nil {
		t.Fatal("expected embedder")
	}
	if !strings.Contains(label, "RenCrow_LLM embedding alias") {
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

func TestBuildConversationTextProviderUsesRenCrowLLMWorker(t *testing.T) {
	worker := fakeConversationProvider{name: "worker-provider"}
	provider, label := buildConversationTextProvider(&config.Config{}, primaryLLMProviders{Worker: worker})

	if provider != worker {
		t.Fatalf("expected RenCrow_LLM Worker provider, got %#v", provider)
	}
	if label != "RenCrow_LLM worker" {
		t.Fatalf("unexpected label: %s", label)
	}
}

func TestBuildGatewayPrimaryProvidersExposeIsolatedConversationGateway(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if request["model"] != "mio" {
			t.Fatalf("conversation route model alias = %v, want mio", request["model"])
		}
		format, _ := request["response_format"].(map[string]any)
		if format["type"] != "json_object" {
			t.Fatalf("response_format = %#v, want json_object", format)
		}
		metadata, _ := request["rencrow"].(map[string]any)
		if metadata["agent_id"] != "mio" || metadata["execution_role"] != "chat" || metadata["execution_alias"] != "mio" {
			t.Fatalf("RenCrow execution metadata = %#v, want Mio Chat alias", metadata)
		}
		if request["think"] != false {
			t.Fatalf("think = %v, want false for the Chat role", request["think"])
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{
				"role": "assistant", "content": `{"schema_version":"mio.turn.v1","kind":"reply","user_message":{"to":"user","text":"ok"}}`,
			}, "finish_reason": "stop"}},
			"usage": map[string]any{"total_tokens": 12},
		})
	}))
	defer srv.Close()
	recorder := &runtimeContextBudgetRecorderStub{}
	providers := buildGatewayPrimaryLLMProviders(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: srv.URL, TimeoutSec: 1},
		AIWorkflow: config.AIWorkflowConfig{
			ContextBudgetTokens:    1000,
			ContextBudgetWarnRatio: 0.8,
			ContextBudgetStopRatio: 0.95,
		},
	}, recorder)
	if providers.Conversation == nil {
		t.Fatal("conversation provider is nil")
	}
	if _, ok := any(providers.Conversation).(llm.LLMProvider); ok {
		t.Fatal("conversation provider must require an explicit ConversationScope")
	}
	if _, ok := any(providers.Conversation).(llm.ToolCallingProvider); ok {
		t.Fatal("conversation provider must not support Task ToolCalling")
	}
	scope := conversation.ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "user-1",
		InputID:       core.NewMessageID(),
		GenerationID:  "generation-1",
		Purpose:       conversation.PurposeMioTurn,
		PolicyVersion: "policy-v1",
	}
	if _, err := providers.Conversation.GenerateConversation(context.Background(), scope, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello", Type: llm.PromptContextUser}},
	}); err != nil {
		t.Fatalf("GenerateConversation() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("Gateway calls = %d, want one", calls)
	}
	if len(recorder.usages) != 0 || len(recorder.events) != 0 {
		t.Fatalf("conversation generation persisted context budget state: usages=%d events=%d", len(recorder.usages), len(recorder.events))
	}
}

func TestConversationExecutionSlotTracksGenerationInRAM(t *testing.T) {
	tracker := newLLMBusyTracker()
	slot := conversationLLMExecutionSlot{tracker: tracker}
	scope := conversation.ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "user-1",
		InputID:       core.NewMessageID(),
		GenerationID:  "generation-1",
		Purpose:       conversation.PurposeMioTurn,
		PolicyVersion: "policy-v1",
	}

	callCtx, release, err := slot.AcquireConversation(context.Background(), scope)
	if err != nil {
		t.Fatalf("AcquireConversation() error = %v", err)
	}
	if callCtx == nil || !tracker.ExternalBusy() {
		t.Fatal("conversation generation slot did not mark the LLM busy in RAM")
	}
	release()
	if tracker.ExternalBusy() {
		t.Fatal("conversation generation slot remained busy after release")
	}
	var _ llmmiddleware.ConversationExecutionSlot = slot
}
