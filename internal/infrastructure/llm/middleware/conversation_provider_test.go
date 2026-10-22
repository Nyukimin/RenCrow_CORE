package middleware

import (
	"context"
	"strings"
	"testing"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type conversationProviderStub struct {
	calls  int
	req    llm.GenerateRequest
	resp   llm.GenerateResponse
	ctxErr error
}

func (p *conversationProviderStub) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.calls++
	p.req = req
	p.ctxErr = ctx.Err()
	return p.resp, nil
}

func (p *conversationProviderStub) Name() string { return "mio-local" }

func conversationProviderTestScope() conversation.ConversationScope {
	return conversation.ConversationScope{
		TurnID:        core.NewTurnID(),
		SessionID:     core.NewSessionID(),
		TraceID:       core.NewTraceID(),
		ActorID:       "user-1",
		InputID:       core.NewMessageID(),
		GenerationID:  "generation-1",
		Purpose:       conversation.PurposeMioTurn,
		PolicyVersion: "policy-v1",
	}
}

func TestConversationProviderValidatesScopeAndForcesStructuredNonStreamingRequest(t *testing.T) {
	inner := &conversationProviderStub{resp: llm.GenerateResponse{Content: "{}"}}
	provider := NewConversationProvider(inner, "mio-local", domainai.ContextBudgetPolicy{})
	req := llm.GenerateRequest{
		Messages:        []llm.Message{{Role: "user", Content: "hello"}},
		ResponseFormat:  llm.ResponseFormatText,
		ProviderOptions: map[string]any{"model": "unlisted", "top_k": 32, "arbitrary": "value"},
	}

	if _, err := provider.GenerateConversation(context.Background(), conversationProviderTestScope(), req); err != nil {
		t.Fatalf("GenerateConversation() error = %v", err)
	}
	if inner.calls != 1 {
		t.Fatalf("inner Generate calls = %d, want 1", inner.calls)
	}
	if inner.req.ResponseFormat != llm.ResponseFormatJSONObject || inner.req.OnToken != nil {
		t.Fatalf("conversation request format=%q stream=%t; want json_object and non-stream", inner.req.ResponseFormat, inner.req.OnToken != nil)
	}
	if len(inner.req.ProviderOptions) != 1 || inner.req.ProviderOptions["top_k"] != 32 {
		t.Fatalf("provider options = %#v, want only the configured sampler option", inner.req.ProviderOptions)
	}
	if !hasPromptType(inner.req.Messages, llm.PromptContextVariable) {
		t.Fatal("conversation provider did not apply the normal request-time variable context")
	}
}

func TestConversationProviderRejectsInvalidScopeBeforeGateway(t *testing.T) {
	inner := &conversationProviderStub{}
	provider := NewConversationProvider(inner, "mio-local", domainai.ContextBudgetPolicy{})
	scope := conversationProviderTestScope()
	scope.TraceID = ""

	if _, err := provider.GenerateConversation(context.Background(), scope, llm.GenerateRequest{}); err == nil {
		t.Fatal("GenerateConversation() error = nil, want scope rejection")
	}
	if inner.calls != 0 {
		t.Fatalf("inner Generate calls = %d, want 0 for invalid scope", inner.calls)
	}
}

func TestConversationProviderStopsOverBudgetBeforeGateway(t *testing.T) {
	inner := &conversationProviderStub{}
	provider := NewConversationProvider(inner, "mio-local", domainai.ContextBudgetPolicy{
		MaxContextTokens: 24,
		WarnAtRatio:      0.5,
		StopAtRatio:      0.5,
	})
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: strings.Repeat("word ", 100)}}, MaxTokens: 10}

	if _, err := provider.GenerateConversation(context.Background(), conversationProviderTestScope(), req); err == nil || !strings.Contains(strings.ToLower(err.Error()), "budget") {
		t.Fatal("GenerateConversation() error = nil, want context budget rejection")
	}
	if inner.calls != 0 {
		t.Fatalf("inner Generate calls = %d, want 0 for over-budget request", inner.calls)
	}
}

func TestConversationProviderRejectsStreamingWithoutCallingGateway(t *testing.T) {
	inner := &conversationProviderStub{}
	provider := NewConversationProvider(inner, "mio-local", domainai.ContextBudgetPolicy{})
	req := llm.GenerateRequest{Messages: []llm.Message{{Role: "user", Content: "hello"}}, OnToken: func(string) {}}

	if _, err := provider.GenerateConversation(context.Background(), conversationProviderTestScope(), req); err == nil || !strings.Contains(strings.ToLower(err.Error()), "stream") {
		t.Fatal("GenerateConversation() error = nil, want streaming request rejection")
	}
	if inner.calls != 0 {
		t.Fatalf("inner Generate calls = %d, want 0 for streaming request", inner.calls)
	}
}

func TestConversationProviderDoesNotImplementTaskOrToolCallingInterfaces(t *testing.T) {
	provider := NewConversationProvider(&conversationProviderStub{}, "mio-local", domainai.ContextBudgetPolicy{})
	if _, ok := any(provider).(llm.LLMProvider); ok {
		t.Fatal("ConversationProvider unexpectedly implements the Task-scoped LLMProvider contract")
	}
	if _, ok := any(provider).(llm.ToolCallingProvider); ok {
		t.Fatal("ConversationProvider unexpectedly implements ToolCallingProvider")
	}
}

type conversationSlotStub struct {
	acquired int
	released int
	scope    conversation.ConversationScope
}

func (s *conversationSlotStub) AcquireConversation(ctx context.Context, scope conversation.ConversationScope) (context.Context, func(), error) {
	s.acquired++
	s.scope = scope
	return ctx, func() { s.released++ }, nil
}

func TestConversationProviderReservesAndReleasesRAMExecutionSlot(t *testing.T) {
	inner := &conversationProviderStub{}
	slot := &conversationSlotStub{}
	scope := conversationProviderTestScope()
	provider := NewConversationProvider(inner, "mio-local", domainai.ContextBudgetPolicy{}).WithExecutionSlot(slot)
	if _, err := provider.GenerateConversation(context.Background(), scope, llm.GenerateRequest{}); err != nil {
		t.Fatalf("GenerateConversation() error = %v", err)
	}
	if slot.acquired != 1 || slot.released != 1 || slot.scope != scope {
		t.Fatalf("slot acquire/release/scope = %d/%d/%#v, want 1/1/%#v", slot.acquired, slot.released, slot.scope, scope)
	}
}

func TestConversationProviderPreservesCancellation(t *testing.T) {
	inner := &conversationProviderStub{}
	provider := NewConversationProvider(inner, "mio-local", domainai.ContextBudgetPolicy{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := provider.GenerateConversation(ctx, conversationProviderTestScope(), llm.GenerateRequest{}); err == nil {
		t.Fatal("GenerateConversation() error = nil, want canceled request")
	}
	if inner.calls != 0 {
		t.Fatalf("inner Generate calls = %d, want 0 after cancellation", inner.calls)
	}
}

func hasPromptType(messages []llm.Message, want llm.PromptContextType) bool {
	for _, message := range messages {
		if message.Type == want {
			return true
		}
	}
	return false
}
