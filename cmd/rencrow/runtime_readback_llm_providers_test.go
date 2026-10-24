package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/actionmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/transportmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
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

func TestBuildLLMRuntimePrimaryChatWriteDiagnosticsReachLocalEndpoint(t *testing.T) {
	requestIDs := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestIDs <- r.Header.Get("X-RenCrow-Request-ID")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"total_tokens":8}}`))
	}))
	defer server.Close()

	actions := actionmanager.New(nil)
	tasks := taskmanager.New(nil, taskmanager.DefaultParallelLimits())
	transport := transportmanager.New(nil, actions)
	runtime := buildLLMRuntimeProviders(&config.Config{
		LLMGateway: config.LLMGatewayConfig{BaseURL: server.URL, TimeoutSec: 1},
	}, nil, newLLMBusyTracker(), actions, tasks, transport)

	if got := len(runtime.Primary.GatewayWriteDiagnostics); got != 1 {
		t.Fatalf("raw diagnostic handles = %d, want only the canary Primary.Chat handle", got)
	}
	if _, ok := runtime.ModuleProviders["chat"].(modulellm.GatewayRequestWriteDiagnosticsProvider); !ok {
		t.Fatal("module adapter did not retain the explicit Primary.Chat diagnostics capability")
	}
	// Call the captured raw Primary.Chat provider used beneath the production
	// middleware stack, avoiding that stack's persistent RawLog/PromptReceipt sinks.
	rawPrimaryChat, ok := runtime.Primary.GatewayWriteDiagnostics["chat"].(llm.LLMProvider)
	if !ok {
		t.Fatal("captured Primary.Chat diagnostics handle is not the Gateway provider")
	}
	timingCtx := turntiming.WithRecorder(context.Background(), turntiming.New(time.Now()))
	if _, err := rawPrimaryChat.Generate(turntiming.WithBodyGenerationContext(timingCtx), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "canary request"}},
	}); err != nil {
		t.Fatalf("captured Primary.Chat Gateway Generate() error = %v", err)
	}
	receivedRequestID := <-requestIDs
	if receivedRequestID == "" {
		t.Fatal("Gateway did not receive the CORE request ID header")
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/viewer/modules/llm/diagnostics", nil)
	request.RemoteAddr = "127.0.0.1:1234"
	handleModuleLLMDiagnostics(runtime.ModuleProviders)(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("diagnostics endpoint status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var snapshot modulellm.DiagnosticsSnapshot
	if err := json.Unmarshal(recorder.Body.Bytes(), &snapshot); err != nil {
		t.Fatalf("decode diagnostics response: %v", err)
	}
	for _, role := range snapshot.Roles {
		if role.Role != modulellm.RoleChat {
			continue
		}
		if role.GatewayRequestWrite == nil || len(role.GatewayRequestWrite.Requests) != 1 {
			t.Fatalf("Primary.Chat write diagnostic missing from endpoint response: %+v", role.GatewayRequestWrite)
		}
		if got := role.GatewayRequestWrite.Requests[0].RequestID; got != receivedRequestID {
			t.Fatalf("endpoint request ID %q does not match Gateway header %q", got, receivedRequestID)
		}
		return
	}
	t.Fatalf("chat role missing from diagnostics response: %+v", snapshot.Roles)
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
