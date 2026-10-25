package routing

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainconversation "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainrouting "github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
)

func TestLLMClassifier_ClassifyWithMioTurnOutcomeLogicalStreamAnswer(t *testing.T) {
	events := []llm.LogicalOutcomeEvent{
		{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 2, Route: "CHAT", PayloadKind: "text"},
		{Kind: llm.LogicalOutcomeAnswerDelta, Text: "Mio "},
		{Kind: llm.LogicalOutcomeAnswerDelta, Text: "answer."},
		{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: "stop"},
	}
	provider := &logicalOutcomeStreamTestProvider{
		events: events,
		response: llm.GenerateResponse{
			Content:      "Mio answer.",
			FinishReason: "stop",
		},
	}
	classifier := NewLLMClassifier(provider, "route policy")
	var tokens strings.Builder
	var forwarded []llm.LogicalOutcomeEvent
	req := llm.GenerateRequest{
		LogicalOutcomeStream: llm.LogicalOutcomeStreamV1,
		OnLogicalOutcomeEvent: func(event llm.LogicalOutcomeEvent) error {
			forwarded = append(forwarded, event)
			return nil
		},
		OnToken: func(token string) { tokens.WriteString(token) },
	}

	result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), req, nil)
	if err != nil {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
	}
	if result.Decision.Route != domainrouting.RouteCHAT || result.Outcome.Kind != domainagent.MioTurnOutcomeAnswer || result.Outcome.Text != "Mio answer." {
		t.Fatalf("classification = %#v, want CHAT answer from streamed text", result)
	}
	if result.Generation.Content != "Mio answer." || result.Generation.FinishReason != "stop" {
		t.Fatalf("generation = %#v, want provider's completed answer", result.Generation)
	}
	if result.Boundary != domainconversation.ThreadBoundaryUnknown {
		t.Fatalf("v1 compatibility boundary = %q, want unknown", result.Boundary)
	}
	if provider.generateCalls != 1 {
		t.Fatalf("Generate calls = %d, want one", provider.generateCalls)
	}
	if provider.request.LogicalOutcomeStream != llm.LogicalOutcomeStreamV1 || provider.request.OnLogicalOutcomeEvent == nil || provider.request.OnToken == nil {
		t.Fatalf("provider request stream contract = %#v, want wrapped event and text callbacks", provider.request)
	}
	if !strings.Contains(provider.request.Messages[0].Content, "schema_version 2") {
		t.Fatalf("v1 compatibility prompt = %q, want schema_version 2", provider.request.Messages[0].Content)
	}
	if tokens.String() != "Mio answer." {
		t.Fatalf("streamed tokens = %q, want validated answer text only", tokens.String())
	}
	if len(forwarded) != len(events) {
		t.Fatalf("forwarded logical events = %d, want %d", len(forwarded), len(events))
	}
}

func TestLLMClassifierLogicalOutcomeStreamV2UsesBoundaryOnlyInternally(t *testing.T) {
	provider := &logicalOutcomeStreamTestProvider{
		events: []llm.LogicalOutcomeEvent{
			{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 3, Route: "CHAT", PayloadKind: "text"},
			{Kind: llm.LogicalOutcomeAnswerDelta, Text: "answer"},
			{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: "stop", ThreadBoundary: "new"},
		},
		response: llm.GenerateResponse{Content: "answer", FinishReason: "stop"},
	}
	classifier := NewLLMClassifier(provider, "route policy")
	var forwarded []llm.LogicalOutcomeEvent
	result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), llm.GenerateRequest{
		LogicalOutcomeStream: llm.LogicalOutcomeStreamV2,
		OnLogicalOutcomeEvent: func(event llm.LogicalOutcomeEvent) error {
			forwarded = append(forwarded, event)
			return nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
	}
	if result.Boundary != domainconversation.ThreadBoundaryNew || result.Outcome.Text != "answer" {
		t.Fatalf("classification = %#v, want new boundary and answer", result)
	}
	if len(forwarded) != 3 || forwarded[2].ThreadBoundary != "" {
		t.Fatalf("forwarded events = %#v, want terminal boundary stripped", forwarded)
	}
	if !strings.Contains(provider.request.Messages[0].Content, "schema_version 3") {
		t.Fatalf("v2 stream prompt = %q, want schema_version 3", provider.request.Messages[0].Content)
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeLogicalStreamRetrievalAndHandoff(t *testing.T) {
	t.Run("retrieve", func(t *testing.T) {
		provider := &logicalOutcomeStreamTestProvider{
			events: []llm.LogicalOutcomeEvent{{
				Kind:              llm.LogicalOutcomeRetrievalRequest,
				SchemaVersion:     2,
				Route:             "CHAT",
				RetrievalRequests: json.RawMessage(`[{"source_kind":"user_memory","query":"出発時刻","references":[],"information_needed":"確定した時刻"}]`),
			}},
			response: llm.GenerateResponse{},
		}
		classifier := NewLLMClassifier(provider, "route policy")
		req := logicalOutcomeStreamRequest(nil, nil)

		result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), req, nil)
		if err != nil {
			t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
		}
		if result.Decision.Route != domainrouting.RouteCHAT || result.Outcome.Kind != domainagent.MioTurnOutcomeRetrieve || len(result.Outcome.Requests) != 1 {
			t.Fatalf("classification = %#v, want one CHAT retrieval request", result)
		}
		request := result.Outcome.Requests[0]
		if request.SourceKind != domainagent.MioRetrievalSourceUserMemory || request.Query != "出発時刻" || request.InformationNeeded != "確定した時刻" || request.References == nil {
			t.Fatalf("retrieval request = %#v, want validated semantic request", request)
		}
	})

	t.Run("handoff", func(t *testing.T) {
		provider := &logicalOutcomeStreamTestProvider{
			events: []llm.LogicalOutcomeEvent{{
				Kind:          llm.LogicalOutcomeHandoff,
				SchemaVersion: 2,
				Route:         "RESEARCH",
			}},
			response: llm.GenerateResponse{},
		}
		classifier := NewLLMClassifier(provider, "route policy")
		result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), logicalOutcomeStreamRequest(nil, nil), nil)
		if err != nil {
			t.Fatalf("ClassifyWithMioTurnOutcome() error = %v", err)
		}
		if result.Decision.Route != domainrouting.RouteRESEARCH || result.Outcome.Kind != domainagent.MioTurnOutcomeHandoff {
			t.Fatalf("classification = %#v, want RESEARCH handoff", result)
		}
	})
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeLogicalStreamRejectsInvalidStartBeforeText(t *testing.T) {
	for _, tt := range []struct {
		name       string
		route      string
		fixedRoute *domainrouting.Route
	}{
		{name: "route excluded by classifier policy", route: "WILD"},
		{name: "fixed route mismatch", route: "CODE2", fixedRoute: routePointer(domainrouting.RouteCHAT)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &logicalOutcomeStreamTestProvider{
				events: []llm.LogicalOutcomeEvent{
					{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 2, Route: tt.route, PayloadKind: "text"},
					{Kind: llm.LogicalOutcomeAnswerDelta, Text: "must not be published"},
				},
				response: llm.GenerateResponse{Content: "must not be published", FinishReason: "stop"},
			}
			classifier := NewLLMClassifier(provider, "route policy")
			var tokens strings.Builder
			var forwardedEvents []llm.LogicalOutcomeEvent
			req := logicalOutcomeStreamRequest(func(token string) { tokens.WriteString(token) }, func(event llm.LogicalOutcomeEvent) error {
				forwardedEvents = append(forwardedEvents, event)
				return nil
			})

			result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), req, tt.fixedRoute)
			if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
				t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want ErrInvalidMioTurnOutcome", err)
			}
			if result.Outcome.Kind != "" || tokens.Len() != 0 {
				t.Fatalf("classification = %#v, tokens = %q; invalid start must fail before text publication", result, tokens.String())
			}
			if len(forwardedEvents) != 0 {
				t.Fatalf("forwarded events = %#v, invalid answer_start must not reach caller", forwardedEvents)
			}
		})
	}
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeLogicalStreamRejectsPartialAndTerminalFailure(t *testing.T) {
	t.Run("missing answer_done", func(t *testing.T) {
		provider := &logicalOutcomeStreamTestProvider{
			events: []llm.LogicalOutcomeEvent{
				{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 2, Route: "CHAT", PayloadKind: "text"},
				{Kind: llm.LogicalOutcomeAnswerDelta, Text: "partial"},
			},
			response: llm.GenerateResponse{Content: "partial"},
		}
		classifier := NewLLMClassifier(provider, "route policy")
		var tokens strings.Builder
		result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), logicalOutcomeStreamRequest(func(token string) { tokens.WriteString(token) }, nil), nil)
		if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) || result.Outcome.Kind != "" {
			t.Fatalf("classification = %#v, error = %v, want failed closed partial stream", result, err)
		}
		if tokens.String() != "partial" {
			t.Fatalf("tokens = %q, want only text from the already validated CHAT route", tokens.String())
		}
	})

	t.Run("terminal generation error", func(t *testing.T) {
		providerErr := errors.New("gateway terminal error")
		provider := &logicalOutcomeStreamTestProvider{
			events: []llm.LogicalOutcomeEvent{
				{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 2, Route: "CHAT", PayloadKind: "text"},
				{Kind: llm.LogicalOutcomeAnswerDelta, Text: "partial"},
				{Kind: llm.LogicalOutcomeAnswerDone, FinishReason: "stop"},
			},
			response:    llm.GenerateResponse{Content: "partial", FinishReason: "stop"},
			generateErr: providerErr,
		}
		classifier := NewLLMClassifier(provider, "route policy")
		result, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), logicalOutcomeStreamRequest(nil, nil), nil)
		if !errors.Is(err, providerErr) || result.Outcome.Kind != "" {
			t.Fatalf("classification = %#v, error = %v, want terminal error and no outcome", result, err)
		}
	})
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeLogicalStreamValidatesRetrievalAndCallback(t *testing.T) {
	t.Run("bounded retrieval shape", func(t *testing.T) {
		provider := &logicalOutcomeStreamTestProvider{
			events: []llm.LogicalOutcomeEvent{{
				Kind:              llm.LogicalOutcomeRetrievalRequest,
				SchemaVersion:     2,
				Route:             "CHAT",
				RetrievalRequests: json.RawMessage(`[{"source_kind":"user_memory","query":"","references":[],"information_needed":"reason"}]`),
			}},
		}
		classifier := NewLLMClassifier(provider, "route policy")
		_, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), logicalOutcomeStreamRequest(nil, nil), nil)
		if !errors.Is(err, domainagent.ErrInvalidMioTurnOutcome) {
			t.Fatalf("ClassifyWithMioTurnOutcome() error = %v, want ErrInvalidMioTurnOutcome", err)
		}
	})

	t.Run("caller event callback rejects before token", func(t *testing.T) {
		callbackErr := errors.New("caller rejected logical event")
		provider := &logicalOutcomeStreamTestProvider{
			events: []llm.LogicalOutcomeEvent{
				{Kind: llm.LogicalOutcomeAnswerStart, SchemaVersion: 2, Route: "CHAT", PayloadKind: "text"},
				{Kind: llm.LogicalOutcomeAnswerDelta, Text: "must not be published"},
			},
			response: llm.GenerateResponse{Content: "must not be published", FinishReason: "stop"},
		}
		classifier := NewLLMClassifier(provider, "route policy")
		var tokens strings.Builder
		_, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), logicalOutcomeStreamRequest(func(token string) { tokens.WriteString(token) }, func(event llm.LogicalOutcomeEvent) error {
			if event.Kind == llm.LogicalOutcomeAnswerStart {
				return callbackErr
			}
			return nil
		}), nil)
		if !errors.Is(err, callbackErr) || tokens.Len() != 0 {
			t.Fatalf("error = %v, tokens = %q, want callback rejection before text", err, tokens.String())
		}
	})
}

func TestLLMClassifier_ClassifyWithMioTurnOutcomeRejectsInvalidStreamOptIn(t *testing.T) {
	for _, tt := range []struct {
		name string
		set  func(*llm.GenerateRequest)
	}{
		{name: "event callback without stream version", set: func(req *llm.GenerateRequest) {
			req.OnLogicalOutcomeEvent = func(llm.LogicalOutcomeEvent) error { return nil }
		}},
		{name: "stream version without event callback", set: func(req *llm.GenerateRequest) {
			req.LogicalOutcomeStream = llm.LogicalOutcomeStreamV1
		}},
		{name: "unknown stream version", set: func(req *llm.GenerateRequest) {
			req.LogicalOutcomeStream = "v3"
			req.OnLogicalOutcomeEvent = func(llm.LogicalOutcomeEvent) error { return nil }
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			provider := &logicalOutcomeStreamTestProvider{}
			classifier := NewLLMClassifier(provider, "route policy")
			req := llm.GenerateRequest{}
			tt.set(&req)
			_, err := classifier.ClassifyWithMioTurnOutcome(context.Background(), req, nil)
			if err == nil || provider.generateCalls != 0 {
				t.Fatalf("error = %v, Generate calls = %d, want reject before generation", err, provider.generateCalls)
			}
		})
	}
}

func logicalOutcomeStreamRequest(onToken llm.StreamCallback, onEvent llm.LogicalOutcomeEventCallback) llm.GenerateRequest {
	if onEvent == nil {
		onEvent = func(llm.LogicalOutcomeEvent) error { return nil }
	}
	return llm.GenerateRequest{
		LogicalOutcomeStream:  llm.LogicalOutcomeStreamV1,
		OnLogicalOutcomeEvent: onEvent,
		OnToken:               onToken,
	}
}

type logicalOutcomeStreamTestProvider struct {
	events        []llm.LogicalOutcomeEvent
	response      llm.GenerateResponse
	generateErr   error
	generateCalls int
	request       llm.GenerateRequest
}

func (p *logicalOutcomeStreamTestProvider) Generate(_ context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.generateCalls++
	p.request = req
	for _, event := range p.events {
		if req.OnLogicalOutcomeEvent == nil {
			return llm.GenerateResponse{}, errors.New("logical outcome callback was not forwarded")
		}
		if err := req.OnLogicalOutcomeEvent(event); err != nil {
			return llm.GenerateResponse{}, err
		}
		if event.Kind == llm.LogicalOutcomeAnswerDelta && event.Text != "" && req.OnToken != nil {
			req.OnToken(event.Text)
		}
	}
	return p.response, p.generateErr
}

func (p *logicalOutcomeStreamTestProvider) Name() string { return "logical-outcome-stream-test" }
