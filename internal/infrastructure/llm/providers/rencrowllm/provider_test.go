package rencrowllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func writeToolChatSSE(w http.ResponseWriter, chunks ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, chunk := range chunks {
		_, _ = fmt.Fprintf(w, "data: %s\n\n", chunk)
	}
	_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
}

func TestNewGatewayProvider(t *testing.T) {
	provider := NewGatewayProvider("test-api-key", "gpt-4")

	if provider == nil {
		t.Fatal("NewGatewayProvider should not return nil")
	}

	if provider.Name() != "rencrow_llm-gpt-4" {
		t.Errorf("Expected name 'rencrow_llm-gpt-4', got '%s'", provider.Name())
	}
}

func TestGatewayProviderSendsRenCrowExecutionMetadata(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second).
		WithRenCrowExecution("shiro", "worker", "worker")
	ctx := llm.WithExecutionObservation(context.Background(), llm.ExecutionObservation{
		RequestID: "request-1",
		TraceID:   "trace-1",
		JobID:     "job-1",
		SessionID: "session-1",
		Initiator: "shiro",
		Caller:    "orchestrator.ops",
		Purpose:   "execute_task",
	})
	_, err := provider.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "run"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	metadata, _ := payload["rencrow"].(map[string]any)
	for key, want := range map[string]string{
		"agent_id":        "shiro",
		"execution_role":  "worker",
		"execution_alias": "worker",
		"request_id":      "request-1",
		"trace_id":        "trace-1",
		"job_id":          "job-1",
		"session_id":      "session-1",
		"initiator":       "shiro",
		"caller":          "orchestrator.ops",
		"purpose":         "execute_task",
	} {
		if metadata[key] != want {
			t.Errorf("rencrow.%s=%#v want %q", key, metadata[key], want)
		}
	}
}

func TestGatewayProviderChatSendsExecutionObservation(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)
	ctx := llm.WithExecutionObservation(context.Background(), llm.ExecutionObservation{
		RequestID: "request-chat",
		Initiator: "shiro",
		Caller:    "heartbeat.backlog",
		Purpose:   "process_backlog_item",
	})
	if _, err := provider.Chat(ctx, llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "run"}},
	}); err != nil {
		t.Fatal(err)
	}

	metadata, _ := payload["rencrow"].(map[string]any)
	for key, want := range map[string]string{
		"request_id": "request-chat",
		"initiator":  "shiro",
		"caller":     "heartbeat.backlog",
		"purpose":    "process_backlog_item",
	} {
		if metadata[key] != want {
			t.Errorf("rencrow.%s=%#v want %q", key, metadata[key], want)
		}
	}
}

func TestGatewayProviderChatSendsLowReasoningContract(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)
	if _, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages:        []llm.ChatMessage{{Role: "user", Content: "run"}},
		MaxTokens:       4096,
		ReasoningEffort: llm.ReasoningEffortLow,
	}); err != nil {
		t.Fatal(err)
	}
	if payload["think"] != "low" || payload["reasoning_effort"] != "low" {
		t.Fatalf("low reasoning fields missing: %#v", payload)
	}
	kwargs, ok := payload["chat_template_kwargs"].(map[string]any)
	if !ok || kwargs["enable_thinking"] != true || kwargs["reasoning_effort"] != "low" {
		t.Fatalf("low chat template kwargs missing: %#v", payload)
	}
	if payload["max_tokens"] != float64(4096) {
		t.Fatalf("max_tokens = %#v, want 4096", payload["max_tokens"])
	}
}

func TestGatewayProviderChatOmitsReasoningContractWhenUnspecified(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)
	if _, err := provider.Chat(context.Background(), llm.ChatRequest{Messages: []llm.ChatMessage{{Role: "user", Content: "run"}}}); err != nil {
		t.Fatal(err)
	}
	if _, exists := payload["reasoning_effort"]; exists {
		t.Fatalf("unspecified reasoning effort changed existing payload: %#v", payload)
	}
	if kwargs, ok := payload["chat_template_kwargs"].(map[string]any); ok {
		if _, exists := kwargs["reasoning_effort"]; exists {
			t.Fatalf("unspecified reasoning effort leaked into chat template kwargs: %#v", payload)
		}
	}
	if _, exists := payload["max_tokens"]; exists {
		t.Fatalf("unspecified max tokens changed existing payload: %#v", payload)
	}
}

func TestGatewayProviderChatSendsConfiguredMaxTokens(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)
	if _, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages:  []llm.ChatMessage{{Role: "user", Content: "run"}},
		MaxTokens: 16384,
	}); err != nil {
		t.Fatal(err)
	}
	if payload["max_tokens"] != float64(16384) {
		t.Fatalf("max_tokens = %#v, want 16384", payload["max_tokens"])
	}
}

func TestGatewayProviderAddsCanonicalMetadataForKnownAlias(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "run"}},
	}); err != nil {
		t.Fatal(err)
	}

	metadata, _ := payload["rencrow"].(map[string]any)
	for key, want := range map[string]string{
		"agent_id":        "shiro",
		"execution_role":  "worker",
		"execution_alias": "worker",
		"initiator":       "shiro",
		"caller":          "core.unattributed",
		"purpose":         "unattributed",
	} {
		if metadata[key] != want {
			t.Errorf("rencrow.%s=%#v want %q", key, metadata[key], want)
		}
	}
	requestID, _ := metadata["request_id"].(string)
	if !strings.HasPrefix(requestID, "llmreq_") {
		t.Fatalf("rencrow.request_id=%q want generated llmreq_ id", requestID)
	}
}

func TestStreamingGatewayErrorIsNotAnEmptySuccess(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "mio", "http://gateway.invalid", time.Second)
	stream := strings.NewReader("data: {\"error\":{\"code\":\"EMPTY_FINAL_CONTENT\",\"message\":\"target stream returned no content\"}}\n\ndata: [DONE]\n\n")

	_, err := provider.readChatCompletionsStream(stream, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "EMPTY_FINAL_CONTENT") {
		t.Fatalf("error=%v", err)
	}
}

func TestStreamingGatewayNumericErrorCodeIsDecoded(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "mio", "http://gateway.invalid", time.Second)
	stream := strings.NewReader("data: {\"error\":{\"code\":503,\"message\":\"backend unavailable\"}}\n\ndata: [DONE]\n\n")

	_, err := provider.readChatCompletionsStream(stream, func(string) {})
	if err == nil || !strings.Contains(err.Error(), "gateway stream error 503: backend unavailable") {
		t.Fatalf("error=%v", err)
	}
}

func TestGatewayErrorPreservesRetryableContract(t *testing.T) {
	err := decodeGatewayError(http.StatusBadGateway, []byte(`{"error":{"code":"TARGET_UNAVAILABLE","message":"configured target is unavailable","retryable":true}}`))
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) {
		t.Fatalf("error type = %T, want *GatewayError", err)
	}
	if gatewayErr.Code != "TARGET_UNAVAILABLE" || !gatewayErr.Retryable() {
		t.Fatalf("gateway error contract was not preserved: %#v", gatewayErr)
	}
}

func TestStreamingGatewayErrorPreservesRetryableContract(t *testing.T) {
	provider := NewGatewayProvider("", "worker")
	_, err := provider.readChatCompletionsStream(strings.NewReader("data: {\"error\":{\"code\":\"NORMALIZATION_ERROR\",\"message\":\"structured response failed\",\"retryable\":true}}\n\n"), func(string) {})
	var gatewayErr *GatewayError
	if !errors.As(err, &gatewayErr) || !gatewayErr.Retryable() {
		t.Fatalf("stream retry contract was not preserved: %T %v", err, err)
	}
}

func TestGatewayProviderGenerate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("Expected path '/v1/chat/completions', got '%s'", r.URL.Path)
		}

		// Authorizationヘッダー確認
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-api-key" {
			t.Errorf("Expected 'Bearer test-api-key', got '%s'", auth)
		}

		// リクエストボディ検証
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		if reqBody["model"] != "gpt-4" {
			t.Errorf("Expected model 'gpt-4', got '%v'", reqBody["model"])
		}

		// レスポンス
		response := map[string]interface{}{
			"id":      "chatcmpl-123",
			"object":  "chat.completion",
			"created": 1677652288,
			"model":   "gpt-4",
			"choices": []map[string]interface{}{
				{
					"index": 0,
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "こんにちは！お手伝いします。",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{
				"prompt_tokens":     10,
				"completion_tokens": 20,
				"total_tokens":      30,
			},
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "こんにちは"},
		},
		MaxTokens:   1000,
		Temperature: 0.7,
	}

	resp, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}

	if resp.Content != "こんにちは！お手伝いします。" {
		t.Errorf("Expected response content, got '%s'", resp.Content)
	}

	if resp.TokensUsed != 30 {
		t.Errorf("Expected 30 tokens used, got %d", resp.TokensUsed)
	}

	if resp.FinishReason != "stop" {
		t.Errorf("Expected finish reason 'stop', got '%s'", resp.FinishReason)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleNoAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" {
			t.Fatalf("expected no Authorization header, got %q", got)
		}
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["model"] != "Chat" {
			t.Fatalf("expected model Chat, got %v", reqBody["model"])
		}
		if reqBody["parse_reasoning"] != true {
			t.Fatalf("expected parse_reasoning=true, got %v", reqBody["parse_reasoning"])
		}
		if reqBody["include_reasoning"] != false {
			t.Fatalf("expected include_reasoning=false, got %v", reqBody["include_reasoning"])
		}
		if reqBody["separate_reasoning"] != true {
			t.Fatalf("expected separate_reasoning=true, got %v", reqBody["separate_reasoning"])
		}
		if _, ok := reqBody["enable_thinking"]; ok {
			t.Fatalf("enable_thinking should not be sent to ThinkingBridge server: %#v", reqBody)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok", "reasoning_content": "hidden", "thinking": "hidden", "raw_content": "<think>hidden</think>ok"},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 1},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Chat", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		MaxTokens: 1,
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "ok" {
		t.Fatalf("unexpected content: %q", resp.Content)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleMergesProviderOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["think"] != false {
			t.Fatalf("expected think=false, got %#v in %#v", reqBody["think"], reqBody)
		}
		if reqBody["parse_reasoning"] != true || reqBody["include_reasoning"] != false || reqBody["separate_reasoning"] != true {
			t.Fatalf("thinking bridge fields should remain enabled: %#v", reqBody)
		}
		if reqBody["model"] != "Worker" {
			t.Fatalf("provider options must not override reserved model field: %#v", reqBody)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Worker", server.URL, 0)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		ProviderOptions: map[string]any{
			"think": false,
			"model": "BadOverride",
		},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestGatewayProviderGenerate_SendsJSONObjectResponseFormat(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		responseFormat, ok := reqBody["response_format"].(map[string]interface{})
		if !ok || responseFormat["type"] != "json_object" {
			t.Fatalf("response_format = %#v, want json_object", reqBody["response_format"])
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": `{"intent":"search"}`},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Mio", server.URL, 0)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:       []llm.Message{{Role: "user", Content: "choose one action"}},
		ResponseFormat: llm.ResponseFormatJSONObject,
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleSendsModelContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		options, ok := reqBody["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected options in local request: %#v", reqBody)
		}
		if got := int(options["num_ctx"].(float64)); got != 131072 {
			t.Fatalf("num_ctx = %d, want 131072", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithModelContext("", "Worker", server.URL, 0, 131072)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestGatewayProviderGenerate_LocalCompatiblePreservesExplicitNumCtx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		options, ok := reqBody["options"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected options in local request: %#v", reqBody)
		}
		if got := int(options["num_ctx"].(float64)); got != 32768 {
			t.Fatalf("num_ctx = %d, want explicit 32768", got)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message":       map[string]interface{}{"role": "assistant", "content": "ok"},
					"finish_reason": "stop",
				},
			},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithModelContext("", "Worker", server.URL, 0, 131072)
	if _, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		ProviderOptions: map[string]any{
			"options": map[string]any{"num_ctx": 32768},
		},
	}); err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleStreamingUsesDeltaContentOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["stream"] != true {
			t.Fatalf("expected stream=true, got %v", reqBody["stream"])
		}
		if reqBody["include_reasoning"] != false || reqBody["separate_reasoning"] != true {
			t.Fatalf("unexpected thinking bridge flags: %#v", reqBody)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"reasoning_content":"hidden","content":""}}]}`,
			`data: {"choices":[{"delta":{"reasoning_content":"","content":"最終"}}]}`,
			`data: {"choices":[{"delta":{"content":"回答"}}]}`,
			`data: [DONE]`,
		} {
			fmt.Fprintln(w, line)
			fmt.Fprintln(w)
		}
	}))
	defer server.Close()

	var tokens []string
	provider := NewGatewayProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:  []llm.Message{{Role: "user", Content: "ping"}},
		OnToken:   func(token string) { tokens = append(tokens, token) },
		MaxTokens: 4,
	})
	if err != nil {
		t.Fatalf("Generate streaming failed: %v", err)
	}
	if resp.Content != "最終回答" {
		t.Fatalf("stream content = %q, want final content only", resp.Content)
	}
	if strings.Join(tokens, "|") != "最終|回答" {
		t.Fatalf("tokens = %#v, want content deltas only", tokens)
	}
}

func TestGatewayProviderGenerate_StreamsOrdinaryContentBeforeDone(t *testing.T) {
	chunkFlushed := make(chan struct{})
	releaseDone := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("response writer is not flushable")
		}
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"逐次"}}]}`)
		fmt.Fprintln(w)
		flusher.Flush()
		close(chunkFlushed)
		<-releaseDone
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"応答"},"finish_reason":"stop"}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: [DONE]`)
		fmt.Fprintln(w)
		flusher.Flush()
	}))
	defer server.Close()

	tokens := make(chan string, 2)
	result := make(chan error, 1)
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	go func() {
		_, err := provider.Generate(context.Background(), llm.GenerateRequest{
			Messages: []llm.Message{{Role: "user", Content: "ping"}},
			OnToken:  func(token string) { tokens <- token },
		})
		result <- err
	}()

	<-chunkFlushed
	select {
	case token := <-tokens:
		if token != "逐次" {
			t.Fatalf("first token = %q, want 逐次", token)
		}
	case <-time.After(100 * time.Millisecond):
		close(releaseDone)
		<-result
		t.Fatal("ordinary content was buffered until stream completion")
	}
	close(releaseDone)
	if err := <-result; err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
}

func TestGatewayProviderGenerate_StreamingReturnsBackendThroughput(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request map[string]any
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		streamOptions, _ := request["stream_options"].(map[string]any)
		if streamOptions["include_usage"] != true {
			t.Fatalf("stream_options = %#v, want include_usage=true", streamOptions)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintln(w, `data: {"choices":[{"delta":{"content":"こんにちは"}}]}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: {"choices":[],"usage":{"completion_tokens":7},"timings":{"predicted_per_second":51.1808}}`)
		fmt.Fprintln(w)
		fmt.Fprintln(w, `data: [DONE]`)
		fmt.Fprintln(w)
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		OnToken:  func(string) {},
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if resp.TokensUsed != 7 || resp.TokensPerSecond != 51.1808 {
		t.Fatalf("stream metrics = tokens:%d rate:%f, want tokens:7 rate:51.1808", resp.TokensUsed, resp.TokensPerSecond)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleStreamingDropsUntaggedReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, line := range []string{
			`data: {"choices":[{"delta":{"content":"Okay, the user is asking for a confirmation message. "}}]}`,
			`data: {"choices":[{"delta":{"content":"Let me check the query again.\n\nFinal answer: "}}]}`,
			`data: {"choices":[{"delta":{"content":"了解しました。"}}]}`,
			`data: [DONE]`,
		} {
			fmt.Fprintln(w, line)
			fmt.Fprintln(w)
		}
	}))
	defer server.Close()

	var tokens []string
	provider := NewGatewayProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "ping"}},
		OnToken:  func(token string) { tokens = append(tokens, token) },
	})
	if err != nil {
		t.Fatalf("Generate streaming failed: %v", err)
	}
	if resp.Content != "了解しました。" {
		t.Fatalf("stream content = %q, want final answer only", resp.Content)
	}
	if strings.Join(tokens, "|") != "了解しました。" {
		t.Fatalf("tokens = %#v, want sanitized final answer only", tokens)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleTreatsNoReasoningLeakAsReasoning(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":         "assistant",
						"content":      "Okay, the user is asking for a confirmation message in Japanese, just one sentence. Let me check the query again.\n\nFinal answer: 了解しました。",
						"parse_status": "no_reasoning",
						"parser_name":  "qwen3",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 32},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "疎通確認"}},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "了解しました。" {
		t.Fatalf("content = %q, want final answer only", resp.Content)
	}
}

func TestGatewayProviderGenerate_LocalCompatibleDropsReasoningOnlyNoReasoningLeak(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":         "assistant",
						"content":      "Okay, the user is asking for a confirmation message in Japanese, just one sentence. Let me check the query again.\n\nThey wrote: \"疎通確認です。\" So they want me to confirm communication.",
						"parse_status": "no_reasoning",
						"parser_name":  "qwen3",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 32},
		})
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "疎通確認"}},
	})
	if err != nil {
		t.Fatalf("Generate failed: %v", err)
	}
	if resp.Content != "" {
		t.Fatalf("content = %q, want reasoning-only leak removed", resp.Content)
	}
}

func TestGatewayProviderGenerate_WithSystemPrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// メッセージリストにsystemメッセージが含まれているか確認
		messages, ok := reqBody["messages"].([]interface{})
		if !ok || len(messages) == 0 {
			t.Error("Request should contain messages")
		}

		firstMsg := messages[0].(map[string]interface{})
		if firstMsg["role"] != "system" {
			t.Errorf("First message should be system, got '%v'", firstMsg["role"])
		}

		if firstMsg["content"] != "You are a helpful assistant" {
			t.Errorf("Expected system content 'You are a helpful assistant', got '%v'", firstMsg["content"])
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "System prompt applied",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 15},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
		SystemPrompt: "You are a helpful assistant",
		MaxTokens:    1000,
	}

	_, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate with system prompt failed: %v", err)
	}
}

func TestConvertMessagesPreservesTypedPromptContextOrder(t *testing.T) {
	provider := NewGatewayProvider("test-api-key", "gpt-4")

	got := provider.convertMessages(llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "character", Type: llm.PromptContextCharacter, Metadata: map[string]string{"character_prompt_block": "00_system.md"}},
			{Role: "system", Content: "stable", Type: llm.PromptContextStable},
			{Role: "system", Content: "recall", Type: llm.PromptContextRecall},
			{Role: "system", Content: "variable", Type: llm.PromptContextVariable},
			{Role: "user", Content: "current user", Type: llm.PromptContextUser},
		},
	})

	if len(got) != 5 {
		t.Fatalf("messages length = %d, want 5: %#v", len(got), got)
	}
	for index, want := range []string{"character_system_prompt", "stable_runtime_context", "recall_pack", "variable_runtime_context", "user_message"} {
		if _, exists := got[index]["metadata"]; exists {
			t.Fatalf("message %d leaked CORE metadata to target payload: %#v", index, got[index])
		}
		blocks := promptContextBlockMetadata(llm.GenerateRequest{Messages: []llm.Message{{Type: llm.PromptContextType(want)}}})
		if len(blocks) != 1 || blocks[0]["type"] != want {
			t.Fatalf("rencrow prompt block metadata missing type %q: %#v", want, blocks)
		}
	}
}

func TestConvertChatMessagesKeepsPromptMetadataGatewayOnly(t *testing.T) {
	provider := NewGatewayProvider("test-api-key", "gpt-4")
	request := llm.ChatRequest{Messages: []llm.ChatMessage{
		{Role: "system", Content: "character", Type: llm.PromptContextCharacter, Metadata: map[string]string{"character_prompt_block": "00_system.md"}},
		{Role: "system", Content: "time", Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "time"}},
		{Role: "user", Content: "hello", Type: llm.PromptContextUser},
	}}
	got := provider.convertChatMessages(request.Messages)
	if len(got) != 3 {
		t.Fatalf("chat messages = %d, want 3: %#v", len(got), got)
	}
	for index, message := range got {
		if _, exists := message["metadata"]; exists {
			t.Fatalf("chat message %d leaked metadata to target: %#v", index, message)
		}
	}
	blocks := promptContextBlockMetadataFromChat(request)
	if len(blocks) != 3 || blocks[0]["character_prompt_block"] != "00_system.md" || blocks[1]["type"] != "variable_runtime_context" {
		t.Fatalf("gateway-only chat metadata = %#v", blocks)
	}
}

func TestGatewayProviderGenerate_MultipleMessages(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		messages, ok := reqBody["messages"].([]interface{})
		if !ok || len(messages) != 3 { // user, assistant, user
			t.Errorf("Expected 3 messages, got %d", len(messages))
		}

		response := map[string]interface{}{
			"choices": []map[string]interface{}{
				{
					"message": map[string]interface{}{
						"role":    "assistant",
						"content": "Multi-turn response",
					},
					"finish_reason": "stop",
				},
			},
			"usage": map[string]interface{}{"total_tokens": 50},
		}

		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "こんにちは"},
			{Role: "assistant", Content: "こんにちは！"},
			{Role: "user", Content: "元気ですか？"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err != nil {
		t.Fatalf("Generate with multiple messages failed: %v", err)
	}
}

func TestGatewayProviderGenerate_APIError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		response := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Rate limit exceeded",
				"type":    "rate_limit_error",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err == nil {
		t.Error("Expected error when API returns rate limit error")
	}
}

// --- Chat (tool calling) テスト ---

func TestChat_WithToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("expected path /v1/chat/completions, got %s", r.URL.Path)
		}

		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		// tools が送信されていることを確認
		tools, ok := reqBody["tools"].([]interface{})
		if !ok || len(tools) == 0 {
			t.Error("expected tools in request")
		}

		writeToolChatSSE(w,
			`{"choices":[{"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_abc123","type":"function","function":{"name":"web_search","arguments":"{\"query\":"}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"RenCrow\"}"}}]},"finish_reason":"tool_calls"}]}`,
		)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "RenCrowを検索して"},
		},
		Tools: []llm.ToolDefinition{
			{
				Type: "function",
				Function: llm.ToolFunctionDef{
					Name:        "web_search",
					Description: "Web検索を実行",
					Parameters:  map[string]any{"type": "object"},
				},
			},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.FinishReason != "tool_calls" {
		t.Errorf("expected finish_reason=tool_calls, got %s", resp.FinishReason)
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(resp.Message.ToolCalls))
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_abc123" {
		t.Errorf("expected ID=call_abc123, got %s", tc.ID)
	}
	if tc.Function.Name != "web_search" {
		t.Errorf("expected tool name=web_search, got %s", tc.Function.Name)
	}
	if tc.Function.Arguments["query"] != "RenCrow" {
		t.Errorf("expected query=RenCrow, got %v", tc.Function.Arguments["query"])
	}
}

func TestChat_WithoutToolCalls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"こんにちは！"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "user", Content: "こんにちは"},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.FinishReason != "stop" {
		t.Errorf("expected finish_reason=stop, got %s", resp.FinishReason)
	}
	if resp.Message.Content != "こんにちは！" {
		t.Errorf("expected content=こんにちは！, got %s", resp.Message.Content)
	}
	if len(resp.Message.ToolCalls) != 0 {
		t.Errorf("expected no tool calls, got %d", len(resp.Message.ToolCalls))
	}
}

func TestChat_LocalCompatibleSendsThinkingBridgeFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&reqBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if reqBody["parse_reasoning"] != true {
			t.Fatalf("expected parse_reasoning=true, got %v", reqBody["parse_reasoning"])
		}
		if reqBody["include_reasoning"] != false {
			t.Fatalf("expected include_reasoning=false, got %v", reqBody["include_reasoning"])
		}
		if reqBody["separate_reasoning"] != true {
			t.Fatalf("expected separate_reasoning=true, got %v", reqBody["separate_reasoning"])
		}
		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"本文"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "Worker", server.URL, 0)
	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "ping"}},
	})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Message.Content != "本文" {
		t.Fatalf("content = %q, want visible content only", resp.Message.Content)
	}
}

func TestChat_ToolResultRoundtrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var reqBody map[string]interface{}
		json.NewDecoder(r.Body).Decode(&reqBody)

		msgs := reqBody["messages"].([]interface{})
		// system, user, assistant(tool_calls), tool, の4メッセージを期待
		if len(msgs) != 4 {
			t.Errorf("expected 4 messages, got %d", len(msgs))
		}

		// tool メッセージの検証
		toolMsg := msgs[3].(map[string]interface{})
		if toolMsg["role"] != "tool" {
			t.Errorf("expected role=tool, got %v", toolMsg["role"])
		}
		if toolMsg["tool_call_id"] != "call_1" {
			t.Errorf("expected tool_call_id=call_1, got %v", toolMsg["tool_call_id"])
		}

		writeToolChatSSE(w, `{"choices":[{"delta":{"role":"assistant","content":"検索結果はこちらです。"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	resp, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{
			{Role: "system", Content: "You are helpful."},
			{Role: "user", Content: "検索して"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "call_1", Function: llm.ToolCallFunction{Name: "web_search", Arguments: map[string]any{"query": "test"}}},
			}},
			{Role: "tool", Content: "result data", ToolCallID: "call_1"},
		},
	})

	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Message.Content != "検索結果はこちらです。" {
		t.Errorf("expected final answer, got %s", resp.Message.Content)
	}
}

func TestChat_ErrorResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"Rate limit exceeded"}}`))
	}))
	defer server.Close()

	provider := NewGatewayProvider("test-api-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	_, err := provider.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.ChatMessage{{Role: "user", Content: "test"}},
	})

	if err == nil {
		t.Error("expected error for 429 response")
	}
}

func TestGatewayProviderGenerate_InvalidAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		response := map[string]interface{}{
			"error": map[string]interface{}{
				"message": "Incorrect API key provided",
				"type":    "invalid_request_error",
			},
		}
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	provider := NewGatewayProvider("invalid-key", "gpt-4")
	provider.SetBaseURL(server.URL)

	req := llm.GenerateRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "テスト"},
		},
	}

	_, err := provider.Generate(context.Background(), req)
	if err == nil {
		t.Error("Expected error for invalid API key")
	}
}
