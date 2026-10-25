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
	domaintransport "github.com/Nyukimin/RenCrow_CORE/internal/domain/transport"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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
	taskID := modulecore.NewTaskID()
	ctx := llm.WithExecutionObservation(context.Background(), llm.ExecutionObservation{
		TraceID:   "trace-1",
		TaskID:    taskID,
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
		"trace_id":        "trace-1",
		"task_id":         string(taskID),
		"session_id":      "session-1",
		"initiator":       "shiro",
		"caller":          "orchestrator.ops",
		"purpose":         "execute_task",
	} {
		if metadata[key] != want {
			t.Errorf("rencrow.%s=%#v want %q", key, metadata[key], want)
		}
	}
	transportRequestID, _ := metadata["request_id"].(string)
	if err := modulecore.RequestID(transportRequestID).Validate(); err != nil {
		t.Fatalf("rencrow.request_id=%q is not a canonical transport ID: %v", transportRequestID, err)
	}
	if _, ok := metadata["job_id"]; ok {
		t.Fatalf("rencrow metadata contains retired job_id: %#v", metadata)
	}
}

func TestGatewayProviderPreservesNativeToolCallPresenceWithTextContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"valid text","tool_calls":[{"id":"call-1","type":"function","function":{"name":"run","arguments":"{}"}}]},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "coder1", server.URL, time.Second)
	response, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "return a coder message"}},
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if response.Content != "valid text" || response.FinishReason != "stop" {
		t.Fatalf("response text/finish changed: %#v", response)
	}
	if !response.HasToolCalls {
		t.Fatal("native tool_calls were discarded from GenerateResponse")
	}
}

func TestReadGenerateStreamPreservesNativeToolCallPresenceWithTextContent(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "coder1", "http://127.0.0.1:1", time.Second)
	response, err := provider.readChatCompletionsStream(strings.NewReader(
		`data: {"choices":[{"delta":{"role":"assistant","content":"valid text"}}]}`+"\n\n"+
			`data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call-1","function":{"name":"run","arguments":"{}"}}]},"finish_reason":"stop"}]}`+"\n\n"+
			"data: [DONE]\n\n",
	), func(string) {})
	if err != nil {
		t.Fatalf("readChatCompletionsStream: %v", err)
	}
	if response.Content != "valid text" || response.FinishReason != "stop" {
		t.Fatalf("response text/finish changed: %#v", response)
	}
	if !response.HasToolCalls {
		t.Fatal("stream decoder discarded native tool_calls")
	}
}

func TestReadGenerateStreamRequiresExplicitTerminalFinishReason(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "coder1", "http://127.0.0.1:1", time.Second)
	for _, test := range []struct {
		name  string
		input string
	}{
		{
			name:  "abrupt EOF after complete-looking JSON",
			input: `data: {"choices":[{"delta":{"content":"{\"ok\":true}"}}]}` + "\n\n",
		},
		{
			name:  "DONE without finish reason",
			input: `data: {"choices":[{"delta":{"content":"{\"ok\":true}"}}]}` + "\n\n" + "data: [DONE]\n\n",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, err := provider.readChatCompletionsStream(strings.NewReader(test.input), func(string) {})
			if err == nil || !strings.Contains(err.Error(), "terminal finish_reason") {
				t.Fatalf("response=%#v err=%v, want missing terminal finish_reason error", response, err)
			}
			if response != (llm.GenerateResponse{}) {
				t.Fatalf("response=%#v, want no usable response on incomplete stream", response)
			}
		})
	}
}

func TestReadGenerateStreamAcceptsExplicitFinishAtEOFAndPreservesTerminalReasons(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "coder1", "http://127.0.0.1:1", time.Second)
	t.Run("stop at EOF without DONE", func(t *testing.T) {
		response, err := provider.readChatCompletionsStream(strings.NewReader(
			`data: {"choices":[{"delta":{"content":"{\"ok\":true}"},"finish_reason":"stop"}]}`+"\n\n",
		), func(string) {})
		if err != nil || response.FinishReason != "stop" || response.Content != `{"ok":true}` {
			t.Fatalf("response=%#v err=%v", response, err)
		}
	})
	for _, finishReason := range []string{"length", "tool_calls"} {
		t.Run(finishReason, func(t *testing.T) {
			input := `data: {"choices":[{"delta":{"content":"partial"},"finish_reason":"` + finishReason + `"}]}` + "\n\n"
			if finishReason == "tool_calls" {
				input = `data: {"choices":[{"delta":{"content":"partial","tool_calls":[{"index":0,"id":"call-1"}]},"finish_reason":"tool_calls"}]}` + "\n\n" + "data: [DONE]\n\n"
			}
			response, err := provider.readChatCompletionsStream(strings.NewReader(input), func(string) {})
			if err != nil || response.FinishReason != finishReason {
				t.Fatalf("response=%#v err=%v, want finish_reason=%q", response, err, finishReason)
			}
			if finishReason == "tool_calls" && !response.HasToolCalls {
				t.Fatal("native tool_calls were not preserved")
			}
		})
	}
}

func TestGatewayGenerateKeepsSharedTokenLimitAndOmitsTemperature(t *testing.T) {
	var payload map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "coder1", server.URL, time.Second)
	_, err := provider.Generate(context.Background(), llm.GenerateRequest{
		Messages:  []llm.Message{{Role: "user", Content: "task"}},
		MaxTokens: 8192,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if payload["max_tokens"] != float64(8192) {
		t.Fatalf("wire max_tokens = %#v, want 8192", payload["max_tokens"])
	}
	if _, exists := payload["temperature"]; exists {
		t.Fatalf("wire request unexpectedly set temperature: %#v", payload["temperature"])
	}
}

func TestGatewayProviderGenerateOwnsCanonicalTransportRequestAndResponse(t *testing.T) {
	owner := &llmTransportReceiptOwner{}
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	var header string
	var metadataRequestID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("X-RenCrow-Request-ID")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		metadata, _ := payload["rencrow"].(map[string]any)
		metadataRequestID, _ = metadata["request_id"].(string)
		w.Header().Set("X-Provider-Request-ID", "provider-request-1")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)

	response, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "run"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.requests) != 1 || len(owner.responses) != 1 || len(owner.failed) != 0 {
		t.Fatalf("receipt calls: requests=%d responses=%d failed=%d", len(owner.requests), len(owner.responses), len(owner.failed))
	}
	requestID := owner.requests[0].RequestID
	if header != string(requestID) || metadataRequestID != string(requestID) {
		t.Fatalf("request identity header=%q metadata=%q receipt=%q", header, metadataRequestID, requestID)
	}
	if response.RequestID != requestID || response.ResponseID != owner.responses[0].ResponseID {
		t.Fatalf("response identity=%#v receipt=%#v", response, owner.responses[0])
	}
	if owner.responses[0].ExternalRef != "provider-request-1" {
		t.Fatalf("provider external ref=%q", owner.responses[0].ExternalRef)
	}
}

func TestGatewayProviderRecordsRequestWriteOnlyWhenTurnTimingIsOptedIn(t *testing.T) {
	receivedRequestIDs := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedRequestIDs <- r.Header.Get("X-RenCrow-Request-ID")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second).
		WithGatewayRequestWriteRecorder(NewGatewayRequestWriteRecorder())
	recorder := turntiming.New(time.Now())
	ctx := turntiming.WithBodyGenerationContext(turntiming.WithRecorder(context.Background(), recorder))
	if _, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "run"}}}); err != nil {
		t.Fatal(err)
	}
	receivedRequestID := <-receivedRequestIDs

	diagnostics := provider.GatewayRequestWriteDiagnostics()
	if diagnostics == nil {
		t.Fatal("opted-in request did not produce Gateway write diagnostics")
	}
	if diagnostics.Capacity != gatewayRequestWriteDiagnosticCapacity || diagnostics.Dropped != 0 || len(diagnostics.Requests) != 1 {
		t.Fatalf("unexpected diagnostic sample snapshot: %+v", diagnostics)
	}
	sample := diagnostics.Requests[0]
	if sample.RequestID == "" || sample.RequestID != receivedRequestID {
		t.Fatalf("diagnostic request ID %q did not match Gateway header %q", sample.RequestID, receivedRequestID)
	}
	if _, err := time.Parse(time.RFC3339Nano, sample.DoStartedAtUTC); err != nil {
		t.Fatalf("invalid Do start timestamp %q: %v", sample.DoStartedAtUTC, err)
	}
	if _, err := time.Parse(time.RFC3339Nano, sample.WroteRequestAtUTC); err != nil {
		t.Fatalf("invalid WroteRequest timestamp %q: %v", sample.WroteRequestAtUTC, err)
	}
	if sample.DoToWriteDurationNS == nil || *sample.DoToWriteDurationNS < 0 {
		t.Fatalf("missing or negative monotonic Do-to-write duration: %v", sample.DoToWriteDurationNS)
	}
	if _, ok := turntiming.FromContext(ctx).Snapshot().PointsMS[string(turntiming.PointGatewayRequestStarted)]; !ok {
		t.Fatal("existing turn timing request-start point was not preserved")
	}

	plainProvider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	if _, err := plainProvider.Generate(context.Background(), llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "run"}}}); err != nil {
		t.Fatal(err)
	}
	if got := plainProvider.GatewayRequestWriteDiagnostics(); got != nil {
		t.Fatalf("non-opted-in request unexpectedly produced diagnostics: %+v", got)
	}
}

func TestGatewayProviderWriteDiagnosticCapacityDropsWithoutOverwriting(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "mio", "http://127.0.0.1", time.Second).
		WithGatewayRequestWriteRecorder(NewGatewayRequestWriteRecorder())
	for i := 0; i < gatewayRequestWriteDiagnosticCapacity+2; i++ {
		startedAt := time.Now()
		index := provider.beginGatewayRequestWriteDiagnostic(fmt.Sprintf("request-%02d", i), startedAt)
		if index >= 0 {
			provider.completeGatewayRequestWriteDiagnostic(index, time.Now(), time.Since(startedAt))
		}
	}

	got := provider.GatewayRequestWriteDiagnostics()
	if got == nil || got.Dropped != 2 || len(got.Requests) != gatewayRequestWriteDiagnosticCapacity {
		t.Fatalf("unexpected bounded sample snapshot: %+v", got)
	}
	if got.Requests[0].RequestID != "request-00" || got.Requests[len(got.Requests)-1].RequestID != fmt.Sprintf("request-%02d", gatewayRequestWriteDiagnosticCapacity-1) {
		t.Fatalf("fixed recorder did not preserve its first samples: first=%q last=%q", got.Requests[0].RequestID, got.Requests[len(got.Requests)-1].RequestID)
	}
}

func TestGatewayProviderRetainsDoStartWhenRequestWriteDoesNotComplete(t *testing.T) {
	provider := NewGatewayProviderWithOptions("", "mio", "http://127.0.0.1", time.Second).
		WithGatewayRequestWriteRecorder(NewGatewayRequestWriteRecorder())
	provider.client = &http.Client{Transport: llmRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("write failed before trace completion")
	})}
	ctx := turntiming.WithBodyGenerationContext(turntiming.WithRecorder(context.Background(), turntiming.New(time.Now())))
	if _, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "run"}}}); err == nil {
		t.Fatal("request write failure was accepted")
	}

	diagnostics := provider.GatewayRequestWriteDiagnostics()
	if diagnostics == nil || len(diagnostics.Requests) != 1 {
		t.Fatalf("failed request Do start was not retained: %+v", diagnostics)
	}
	sample := diagnostics.Requests[0]
	if sample.RequestID == "" || sample.DoStartedAtUTC == "" {
		t.Fatalf("failed request is missing its correlation ID or Do-start timestamp: %+v", sample)
	}
	if sample.WroteRequestAtUTC != "" || sample.DoToWriteDurationNS != nil {
		t.Fatalf("failed request claimed a completed write: %+v", sample)
	}
}

func TestGatewayProviderTimingMarksHTTPBoundaryOnlyForBodyGeneration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(3 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second).
		WithGatewayRequestWriteRecorder(NewGatewayRequestWriteRecorder())
	request := llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}}
	recorder := turntiming.New(time.Now().Add(-time.Second).Round(0))
	ctx := turntiming.WithBodyGenerationContext(turntiming.WithRecorder(context.Background(), recorder))

	if _, err := provider.Generate(ctx, request); err != nil {
		t.Fatalf("first Generate() error = %v", err)
	}
	first := recorder.Snapshot()
	requestStarted, startedOK := first.PointsMS[string(turntiming.PointGatewayRequestStarted)]
	responseHeaders, headersOK := first.PointsMS[string(turntiming.PointGatewayResponseHeaders)]
	if !startedOK || !headersOK {
		t.Fatalf("gateway HTTP points missing: %#v", first.PointsMS)
	}
	if responseHeaders <= requestStarted {
		t.Fatalf("gateway response headers %.6fms must follow request start %.6fms", responseHeaders, requestStarted)
	}
	if _, err := provider.Generate(ctx, request); err != nil {
		t.Fatalf("retry Generate() error = %v", err)
	}
	second := recorder.Snapshot().PointsMS
	if second[string(turntiming.PointGatewayRequestStarted)] != requestStarted || second[string(turntiming.PointGatewayResponseHeaders)] != responseHeaders {
		t.Fatalf("gateway points moved to retry request: first=%#v second=%#v", first.PointsMS, second)
	}

	untimedRecorder := turntiming.New(time.Now())
	untimedCtx := turntiming.WithRecorder(context.Background(), untimedRecorder)
	diagnosticsBeforeUntimed := provider.GatewayRequestWriteDiagnostics()
	if _, err := provider.Generate(untimedCtx, request); err != nil {
		t.Fatalf("routing-style Generate() error = %v", err)
	}
	untimed := untimedRecorder.Snapshot().PointsMS
	if _, ok := untimed[string(turntiming.PointGatewayRequestStarted)]; ok {
		t.Fatalf("non-body-generation request recorded gateway timing: %#v", untimed)
	}
	diagnosticsAfterUntimed := provider.GatewayRequestWriteDiagnostics()
	if diagnosticsAfterUntimed == nil || diagnosticsBeforeUntimed == nil || len(diagnosticsAfterUntimed.Requests) != len(diagnosticsBeforeUntimed.Requests) {
		t.Fatalf("non-body-generation request changed Gateway write diagnostics: before=%+v after=%+v", diagnosticsBeforeUntimed, diagnosticsAfterUntimed)
	}
}

func TestGatewayTimingPayloadPreservesFractionalMilliseconds(t *testing.T) {
	snapshot := turntiming.Snapshot{
		SchemaVersion: turntiming.SchemaVersion,
		PointsMS: map[string]float64{
			string(turntiming.PointGatewayRequestStarted):  1.25,
			string(turntiming.PointGatewayResponseHeaders): 2.75,
		},
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var decoded turntiming.Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.PointsMS[string(turntiming.PointGatewayRequestStarted)] != 1.25 || decoded.PointsMS[string(turntiming.PointGatewayResponseHeaders)] != 2.75 {
		t.Fatalf("fractional gateway offsets were truncated: %s => %#v", encoded, decoded.PointsMS)
	}
}

func TestGatewayProviderStreamingResponseKeepsTransportIdentity(t *testing.T) {
	owner := &llmTransportReceiptOwner{}
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeToolChatSSE(w, `{"choices":[{"delta":{"content":"ok"},"finish_reason":"stop"}]}`)
	}))
	defer server.Close()
	provider := NewGatewayProviderWithOptions("", "worker", server.URL, time.Second)

	response, err := provider.Generate(ctx, llm.GenerateRequest{
		Messages: []llm.Message{{Role: "user", Content: "run"}},
		OnToken:  func(string) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(owner.requests) != 1 || len(owner.responses) != 1 ||
		response.RequestID != owner.requests[0].RequestID ||
		response.ResponseID != owner.responses[0].ResponseID {
		t.Fatalf("stream transport identity response=%#v requests=%#v responses=%#v", response, owner.requests, owner.responses)
	}
}

func TestGatewayProviderNoResponseFailurePersistsRequestWithoutResponse(t *testing.T) {
	owner := &llmTransportReceiptOwner{}
	ctx, err := domaintransport.WithReceiptOwner(context.Background(), owner)
	if err != nil {
		t.Fatal(err)
	}
	provider := NewGatewayProviderWithOptions("", "worker", "http://llm.invalid", time.Second)
	provider.client = &http.Client{Transport: llmRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("dial failed")
	})}

	if _, err := provider.Generate(ctx, llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "run"}}}); err == nil {
		t.Fatal("no-response failure was accepted")
	}
	if len(owner.requests) != 1 || len(owner.failed) != 1 || len(owner.responses) != 0 ||
		owner.failed[0] != owner.requests[0].RequestID {
		t.Fatalf("no-response receipts requests=%#v failed=%#v responses=%#v", owner.requests, owner.failed, owner.responses)
	}
}

type llmRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f llmRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type llmTransportReceiptOwner struct {
	requests  []domaintransport.Request
	responses []domaintransport.Response
	failed    []modulecore.RequestID
}

func (o *llmTransportReceiptOwner) BeginRequest(_ context.Context, operation string) (domaintransport.Request, error) {
	request := domaintransport.Request{RequestID: modulecore.NewRequestID(), Operation: operation}
	o.requests = append(o.requests, request)
	return request, nil
}

func (o *llmTransportReceiptOwner) CompleteResponse(_ context.Context, requestID modulecore.RequestID, externalRef string) (domaintransport.Response, error) {
	response := domaintransport.Response{ResponseID: modulecore.NewResponseID(), RequestID: requestID, ExternalRef: externalRef}
	o.responses = append(o.responses, response)
	return response, nil
}

func (o *llmTransportReceiptOwner) FailWithoutResponse(_ context.Context, requestID modulecore.RequestID, _ string) error {
	o.failed = append(o.failed, requestID)
	return nil
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
		"initiator": "shiro",
		"caller":    "heartbeat.backlog",
		"purpose":   "process_backlog_item",
	} {
		if metadata[key] != want {
			t.Errorf("rencrow.%s=%#v want %q", key, metadata[key], want)
		}
	}
	if err := modulecore.RequestID(metadata["request_id"].(string)).Validate(); err != nil {
		t.Fatalf("rencrow.request_id is not canonical: %v", err)
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
	if !strings.HasPrefix(requestID, "req_") {
		t.Fatalf("rencrow.request_id=%q want generated req_ id", requestID)
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
			`data: {"choices":[{"delta":{"content":"回答"},"finish_reason":"stop"}]}`,
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
		fmt.Fprintln(w, `data: {"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"completion_tokens":7},"timings":{"predicted_per_second":51.1808}}`)
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
			`data: {"choices":[{"delta":{"content":"了解しました。"},"finish_reason":"stop"}]}`,
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
			{Role: "tool", Content: "result data", ProviderToolCallID: "call_1"},
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
