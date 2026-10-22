package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestBuildGatewayPrimaryProvidersExposeReadbackProviders(t *testing.T) {
	var mu sync.Mutex
	requests := make(map[string]map[string]any)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode Gateway request: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		model, _ := request["model"].(string)
		mu.Lock()
		requests[model] = request
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"total_tokens":8}}`))
	}))
	defer server.Close()

	providers := buildGatewayPrimaryLLMProviders(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL, TimeoutSec: 1},
	}, nil)
	if providers.Conversation == nil {
		t.Fatal("legacy Mio conversation provider was lost")
	}
	if got, want := len(providers.Readback), 3; got != want {
		t.Fatalf("readback provider count = %d, want %d", got, want)
	}

	tests := []struct {
		assignee  string
		role      string
		think     any
		wantThink bool
	}{
		{assignee: "shiro", role: "chatworker", think: "low", wantThink: true},
		{assignee: "kuro", role: "heavy"},
		{assignee: "midori", role: "wild", think: false, wantThink: true},
	}
	for _, test := range tests {
		t.Run(test.assignee, func(t *testing.T) {
			provider := providers.Readback[test.assignee]
			if provider == nil {
				t.Fatalf("readback provider for %s is nil", test.assignee)
			}
			if _, ok := any(provider).(llm.LLMProvider); ok {
				t.Fatalf("readback provider for %s exposes the Task-scoped LLMProvider", test.assignee)
			}
			if _, ok := any(provider).(llm.ToolCallingProvider); ok {
				t.Fatalf("readback provider for %s exposes ToolCallingProvider", test.assignee)
			}
			if _, err := provider.GenerateConversation(context.Background(), readbackProviderTestScope(), llm.GenerateRequest{
				Messages:       []llm.Message{{Role: "user", Content: "readback request"}},
				ResponseFormat: llm.ResponseFormatText,
			}); err != nil {
				t.Fatalf("GenerateConversation() error = %v", err)
			}
		})
	}

	mu.Lock()
	defer mu.Unlock()
	if got, want := len(requests), 3; got != want {
		t.Fatalf("Gateway request count = %d, want %d", got, want)
	}
	for _, test := range tests {
		t.Run(test.assignee+"_request_contract", func(t *testing.T) {
			request, ok := requests[test.assignee]
			if !ok {
				t.Fatalf("Gateway request for alias %s is missing", test.assignee)
			}
			if request["model"] != test.assignee {
				t.Errorf("model alias = %v, want %s", request["model"], test.assignee)
			}
			if request["stream"] == true {
				t.Error("readback request streamed tokens")
			}
			format, _ := request["response_format"].(map[string]any)
			if format["type"] != "json_object" {
				t.Errorf("response_format = %#v, want JSON object", format)
			}
			metadata, _ := request["rencrow"].(map[string]any)
			if metadata["agent_id"] != test.assignee || metadata["execution_role"] != test.role || metadata["execution_alias"] != test.assignee {
				t.Errorf("RenCrow execution metadata = %#v, want authenticated %s/%s/%s", metadata, test.assignee, test.role, test.assignee)
			}
			if test.wantThink && request["think"] != test.think {
				t.Errorf("think policy = %#v, want %#v", request["think"], test.think)
			}
			if test.assignee == "kuro" {
				if _, exists := request["think"]; exists {
					t.Errorf("Heavy role unexpectedly forced a thinking policy: %#v", request["think"])
				}
			}
		})
	}
}

func TestBuildLLMRuntimeReadbackProvidersBypassActionOwnedLegacyProviders(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{}"},"finish_reason":"stop"}],"usage":{"total_tokens":8}}`))
	}))
	defer server.Close()

	actions := actionmanager.New(nil)
	tasks := taskmanager.New(nil, taskmanager.DefaultParallelLimits())
	transport := transportmanager.New(nil, actions)
	providers := buildLLMRuntimeProviders(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL, TimeoutSec: 1},
	}, nil, newLLMBusyTracker(), actions, tasks, transport)
	if got, want := len(providers.Readback), 3; got != want {
		t.Fatalf("runtime readback provider count = %d, want %d", got, want)
	}
	if providers.Primary.Readback == nil {
		t.Fatal("runtime primary providers lost the raw readback provider map")
	}
	for _, assignee := range []string{"shiro", "kuro", "midori"} {
		provider := providers.Readback[assignee]
		if provider == nil {
			t.Fatalf("runtime readback provider for %s is nil", assignee)
		}
		if _, ok := any(provider).(llm.LLMProvider); ok {
			t.Fatalf("runtime readback provider for %s was replaced by an action-owned LLM provider", assignee)
		}
		if _, err := provider.GenerateConversation(context.Background(), readbackProviderTestScope(), llm.GenerateRequest{
			Messages: []llm.Message{{Role: "user", Content: "readback request"}},
		}); err != nil {
			t.Fatalf("readback request for %s went through a Task/Action owner or failed: %v", assignee, err)
		}
	}
	if calls != 3 {
		t.Fatalf("direct readback Gateway requests = %d, want 3", calls)
	}
}

func readbackProviderTestScope() conversation.ConversationScope {
	return conversation.ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "authenticated-user",
		InputID:       core.NewMessageID(),
		GenerationID:  "readback-generation-1",
		Purpose:       conversation.PurposeReadback,
		PolicyVersion: "response-first-v1.0",
	}
}
