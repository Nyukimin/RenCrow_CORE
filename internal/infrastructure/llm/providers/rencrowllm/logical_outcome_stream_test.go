package rencrowllm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

func logicalOutcomeRequest(onToken llm.StreamCallback, onEvent llm.LogicalOutcomeEventCallback) llm.GenerateRequest {
	return llm.GenerateRequest{
		Messages:              []llm.Message{{Role: "user", Content: "hello"}},
		LogicalOutcomeStream:  llm.LogicalOutcomeStreamV1,
		OnLogicalOutcomeEvent: onEvent,
		OnToken:               onToken,
	}
}

func writeLogicalSSE(w http.ResponseWriter, events ...string) {
	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		_, _ = fmt.Fprint(w, event)
	}
}

func TestGatewayProviderLogicalOutcomeStreamSendsOptInAndDispatchesTextOnly(t *testing.T) {
	var request map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		// Split a data line across flushed writes to exercise network fragmentation.
		for _, fragment := range []string{
			"event: answer_start\n",
			"data: {\"schema_version\":2,\"route\":\"CHAT\",",
			"\"payload_kind\":\"text\"}\n\n",
			"event: answer_delta\n",
			"data: {\"text\":\"hello \"}\n\n",
			"event: answer_delta\n",
			"data: {\"text\":\"world\"}\n\n",
			"event: answer_done\n",
			"data: {\"finish_reason\":\"stop\"}\n\n",
			"data: [DONE]\n\n",
		} {
			_, _ = fmt.Fprint(w, fragment)
			flusher.Flush()
		}
	}))
	defer server.Close()

	var tokens []string
	var events []llm.LogicalOutcomeEvent
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	response, err := provider.Generate(context.Background(), logicalOutcomeRequest(
		func(token string) { tokens = append(tokens, token) },
		func(event llm.LogicalOutcomeEvent) error {
			events = append(events, event)
			return nil
		},
	))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if request["stream"] != true {
		t.Fatalf("request stream = %#v, want true", request["stream"])
	}
	metadata, _ := request["rencrow"].(map[string]any)
	if metadata["logical_outcome_stream"] != llm.LogicalOutcomeStreamV1 {
		t.Fatalf("rencrow.logical_outcome_stream = %#v, want %q", metadata["logical_outcome_stream"], llm.LogicalOutcomeStreamV1)
	}
	if response.Content != "hello world" || response.FinishReason != "stop" {
		t.Fatalf("response = %#v, want complete logical answer", response)
	}
	if strings.Join(tokens, "") != "hello world" {
		t.Fatalf("OnToken values = %#v, want only answer_delta text", tokens)
	}
	wantKinds := []llm.LogicalOutcomeEventKind{
		llm.LogicalOutcomeAnswerStart,
		llm.LogicalOutcomeAnswerDelta,
		llm.LogicalOutcomeAnswerDelta,
		llm.LogicalOutcomeAnswerDone,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("logical events = %#v, want %d events", events, len(wantKinds))
	}
	for i, want := range wantKinds {
		if events[i].Kind != want {
			t.Errorf("logical event %d kind = %q, want %q", i, events[i].Kind, want)
		}
	}
	if events[0].SchemaVersion != 2 || events[0].Route != "CHAT" || events[0].PayloadKind != "text" {
		t.Fatalf("answer_start = %#v, want schema v2 CHAT text", events[0])
	}
}

func TestGatewayProviderLogicalOutcomeRetrievalDoesNotReachOnToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeLogicalSSE(w,
			"event: retrieval_request\n",
			"data: {\"schema_version\":2,\"route\":\"CHAT\",\"requests\":[{\"domain\":\"news\"}]}\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var tokens []string
	var events []llm.LogicalOutcomeEvent
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	response, err := provider.Generate(context.Background(), logicalOutcomeRequest(
		func(token string) { tokens = append(tokens, token) },
		func(event llm.LogicalOutcomeEvent) error {
			events = append(events, event)
			return nil
		},
	))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "" || len(tokens) != 0 {
		t.Fatalf("response content=%q tokens=%#v; retrieval JSON must not become answer text", response.Content, tokens)
	}
	if len(events) != 1 || events[0].Kind != llm.LogicalOutcomeRetrievalRequest {
		t.Fatalf("logical events = %#v, want one retrieval_request", events)
	}
	if string(events[0].RetrievalRequests) != `[{"domain":"news"}]` {
		t.Fatalf("retrieval requests = %s", events[0].RetrievalRequests)
	}
}

func TestGatewayProviderLogicalOutcomeHandoffIsTypedAndNeverBecomesText(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeLogicalSSE(w,
			"event: handoff\n",
			"data: {\"schema_version\":2,\"route\":\"CODE\"}\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var tokens []string
	var events []llm.LogicalOutcomeEvent
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	response, err := provider.Generate(context.Background(), logicalOutcomeRequest(
		func(token string) { tokens = append(tokens, token) },
		func(event llm.LogicalOutcomeEvent) error {
			events = append(events, event)
			return nil
		},
	))
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if response.Content != "" || len(tokens) != 0 {
		t.Fatalf("response content=%q tokens=%#v; handoff must not become answer text", response.Content, tokens)
	}
	if len(events) != 1 || events[0].Kind != llm.LogicalOutcomeHandoff || events[0].SchemaVersion != 2 || events[0].Route != "CODE" {
		t.Fatalf("logical events = %#v, want typed handoff for CODE", events)
	}
}

func TestGatewayProviderLogicalOutcomeRequiresEOFAfterDone(t *testing.T) {
	doneFlushed := make(chan struct{})
	releaseEOF := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		_, _ = fmt.Fprint(w, "event: handoff\ndata: {\"schema_version\":2,\"route\":\"CODE\"}\n\n")
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
		close(doneFlushed)
		select {
		case <-releaseEOF:
		case <-r.Context().Done():
		}
	}))
	defer server.Close()

	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	result := make(chan error, 1)
	go func() {
		_, err := provider.Generate(context.Background(), logicalOutcomeRequest(nil, func(llm.LogicalOutcomeEvent) error { return nil }))
		result <- err
	}()
	select {
	case <-doneFlushed:
	case <-time.After(time.Second):
		t.Fatal("server did not flush the handoff terminal")
	}
	select {
	case err := <-result:
		t.Fatalf("Generate() returned before EOF after [DONE]: %v", err)
	case <-time.After(25 * time.Millisecond):
	}
	close(releaseEOF)
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Generate() error after EOF = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Generate() did not complete after EOF")
	}
}

func TestGatewayProviderLogicalOutcomeRejectsIncompleteOrInvalidStreams(t *testing.T) {
	tests := []struct {
		name   string
		stream string
	}{
		{
			name: "EOF without DONE",
			stream: "event: answer_start\ndata: {\"schema_version\":2,\"route\":\"CHAT\",\"payload_kind\":\"text\"}\n\n" +
				"event: answer_delta\ndata: {\"text\":\"partial\"}\n\n" +
				"event: answer_done\ndata: {\"finish_reason\":\"stop\"}\n\n",
		},
		{
			name: "contradictory answer and retrieval branches",
			stream: "event: answer_start\ndata: {\"schema_version\":2,\"route\":\"CHAT\",\"payload_kind\":\"text\"}\n\n" +
				"event: answer_delta\ndata: {\"text\":\"partial\"}\n\n" +
				"event: retrieval_request\ndata: {\"schema_version\":2,\"route\":\"CHAT\",\"requests\":[]}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name: "contradictory answer and handoff branches",
			stream: "event: answer_start\ndata: {\"schema_version\":2,\"route\":\"CHAT\",\"payload_kind\":\"text\"}\n\n" +
				"event: answer_delta\ndata: {\"text\":\"partial\"}\n\n" +
				"event: handoff\ndata: {\"schema_version\":2,\"route\":\"CODE\"}\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name:   "handoff without DONE",
			stream: "event: handoff\ndata: {\"schema_version\":2,\"route\":\"CODE\"}\n\n",
		},
		{
			name:   "malformed event payload",
			stream: "event: answer_start\ndata: {not-json}\n\n",
		},
		{
			name: "typed error after delta",
			stream: "event: answer_start\ndata: {\"schema_version\":2,\"route\":\"CHAT\",\"payload_kind\":\"text\"}\n\n" +
				"event: answer_delta\ndata: {\"text\":\"partial\"}\n\n" +
				"data: {\"error\":{\"code\":\"NORMALIZATION_ERROR\",\"message\":\"invalid outcome\",\"retryable\":false}}\n\n" +
				"data: [DONE]\n\n",
		},
	}

	var requestCount atomic.Int32
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requestCount.Add(1)
				writeLogicalSSE(w, tt.stream)
			}))
			defer server.Close()
			requestCount.Store(0)

			provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
			var tokens []string
			response, err := provider.Generate(context.Background(), logicalOutcomeRequest(
				func(token string) { tokens = append(tokens, token) },
				func(llm.LogicalOutcomeEvent) error { return nil },
			))
			if err == nil {
				t.Fatalf("Generate() succeeded with response %#v", response)
			}
			if requestCount.Load() != 1 {
				t.Fatalf("request count = %d, want one attempt", requestCount.Load())
			}
			if tt.name == "typed error after delta" {
				var gatewayErr *GatewayError
				if !errors.As(err, &gatewayErr) || gatewayErr.Code != "NORMALIZATION_ERROR" {
					t.Fatalf("error = %T %v, want typed NORMALIZATION_ERROR", err, err)
				}
			}
		})
	}
}

func TestGatewayProviderLogicalOutcomeCancellationDoesNotReturnSuccess(t *testing.T) {
	started := make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Error("response writer does not support flushing")
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, "event: answer_start\ndata: {\"schema_version\":2,\"route\":\"CHAT\",\"payload_kind\":\"text\"}\n\n")
		_, _ = fmt.Fprint(w, "event: answer_delta\ndata: {\"text\":\"partial\"}\n\n")
		flusher.Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	tokens := make(chan string, 1)
	result := make(chan error, 1)
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, 2*time.Second)
	go func() {
		_, err := provider.Generate(ctx, logicalOutcomeRequest(
			func(token string) { tokens <- token },
			func(llm.LogicalOutcomeEvent) error { return nil },
		))
		result <- err
	}()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("server did not start the SSE response")
	}
	select {
	case token := <-tokens:
		if token != "partial" {
			t.Fatalf("first token = %q, want partial", token)
		}
	case <-time.After(time.Second):
		t.Fatal("answer delta was not delivered before cancellation")
	}
	cancel()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("Generate() succeeded after cancellation without DONE")
		}
	case <-time.After(time.Second):
		t.Fatal("Generate() did not stop after context cancellation")
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want one attempt", requests.Load())
	}
}

func TestGatewayProviderLogicalOutcomeCallbackCanRejectBeforeText(t *testing.T) {
	var requestCount atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		writeLogicalSSE(w,
			"event: answer_start\ndata: {\"schema_version\":2,\"route\":\"UNEXPECTED\",\"payload_kind\":\"text\"}\n\n",
			"event: answer_delta\ndata: {\"text\":\"must not leak\"}\n\n",
			"event: answer_done\ndata: {\"finish_reason\":\"stop\"}\n\n",
			"data: [DONE]\n\n",
		)
	}))
	defer server.Close()

	var tokens []string
	provider := NewGatewayProviderWithOptions("", "mio", server.URL, time.Second)
	_, err := provider.Generate(context.Background(), logicalOutcomeRequest(
		func(token string) { tokens = append(tokens, token) },
		func(event llm.LogicalOutcomeEvent) error {
			if event.Kind == llm.LogicalOutcomeAnswerStart && event.Route != "CHAT" {
				return errors.New("unexpected route")
			}
			return nil
		},
	))
	if err == nil || !strings.Contains(err.Error(), "unexpected route") {
		t.Fatalf("Generate() error = %v, want callback rejection", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("OnToken received %#v after callback rejected answer_start", tokens)
	}
	if requestCount.Load() != 1 {
		t.Fatalf("request count = %d, want one attempt", requestCount.Load())
	}
}
