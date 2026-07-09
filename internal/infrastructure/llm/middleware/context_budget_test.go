package middleware

import (
	"context"
	"errors"
	"strings"
	"testing"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type contextBudgetStubProvider struct {
	generateCalled bool
	chatCalled     bool
}

func (p *contextBudgetStubProvider) Generate(_ context.Context, _ domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.generateCalled = true
	return domainllm.GenerateResponse{Content: "ok"}, nil
}

func (p *contextBudgetStubProvider) Chat(_ context.Context, _ domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	p.chatCalled = true
	return domainllm.ChatResponse{Message: domainllm.ChatMessage{Role: "assistant", Content: "ok"}, Done: true}, nil
}

func (p *contextBudgetStubProvider) Name() string { return "stub" }

type contextBudgetRecorderStub struct {
	usages []domainai.ContextUsage
	events []domainai.WorkflowEvent
	err    error
}

func (s *contextBudgetRecorderStub) SaveContextUsage(_ context.Context, item domainai.ContextUsage) error {
	if s.err != nil {
		return s.err
	}
	s.usages = append(s.usages, item)
	return nil
}

func (s *contextBudgetRecorderStub) SaveWorkflowEvent(_ context.Context, item domainai.WorkflowEvent) error {
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, item)
	return nil
}

func TestContextBudgetProviderStopsGenerateBeforeInnerCall(t *testing.T) {
	inner := &contextBudgetStubProvider{}
	provider := NewContextBudgetProvider(inner, "chat", domainai.ContextBudgetPolicy{
		MaxContextTokens: 10,
		WarnAtRatio:      0.5,
		StopAtRatio:      0.8,
	})

	_, err := provider.Generate(context.Background(), domainllm.GenerateRequest{
		Messages:  []domainllm.Message{{Role: "user", Content: strings.Repeat("x", 100)}},
		MaxTokens: 16,
	})

	if err == nil {
		t.Fatal("expected context budget error")
	}
	if inner.generateCalled {
		t.Fatal("inner Generate should not be called after stop decision")
	}
}

func TestContextBudgetProviderWarnsButAllowsGenerate(t *testing.T) {
	inner := &contextBudgetStubProvider{}
	provider := NewContextBudgetProvider(inner, "chat", domainai.ContextBudgetPolicy{
		MaxContextTokens: 100,
		WarnAtRatio:      0.2,
		StopAtRatio:      0.9,
	})

	_, err := provider.Generate(context.Background(), domainllm.GenerateRequest{
		Messages:  []domainllm.Message{{Role: "user", Content: strings.Repeat("x", 100)}},
		MaxTokens: 1,
	})

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !inner.generateCalled {
		t.Fatal("inner Generate should be called for warn decision")
	}
}

func TestContextBudgetProviderStopsChatBeforeInnerCall(t *testing.T) {
	inner := &contextBudgetStubProvider{}
	provider := NewContextBudgetProvider(inner, "worker", domainai.ContextBudgetPolicy{
		MaxContextTokens: 10,
		WarnAtRatio:      0.5,
		StopAtRatio:      0.8,
	})

	_, err := provider.Chat(context.Background(), domainllm.ChatRequest{
		Messages: []domainllm.ChatMessage{{Role: "user", Content: strings.Repeat("x", 100)}},
	})

	if err == nil {
		t.Fatal("expected context budget error")
	}
	if inner.chatCalled {
		t.Fatal("inner Chat should not be called after stop decision")
	}
}

func TestContextBudgetProviderRecordsWarningUsageAndEvent(t *testing.T) {
	inner := &contextBudgetStubProvider{}
	recorder := &contextBudgetRecorderStub{}
	provider := NewContextBudgetProvider(inner, "chat", domainai.ContextBudgetPolicy{
		MaxContextTokens: 100,
		WarnAtRatio:      0.2,
		StopAtRatio:      0.9,
	}, recorder)

	_, err := provider.Generate(context.Background(), domainllm.GenerateRequest{
		Messages:  []domainllm.Message{{Role: "user", Content: strings.Repeat("x", 100)}},
		MaxTokens: 1,
	})

	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(recorder.usages) != 1 {
		t.Fatalf("expected one usage record, got %#v", recorder.usages)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("expected one workflow event, got %#v", recorder.events)
	}
	if recorder.events[0].EventType != "context_budget_warning" {
		t.Fatalf("unexpected event: %#v", recorder.events[0])
	}
	if recorder.events[0].ParentEventID != recorder.usages[0].EventID {
		t.Fatalf("event should link to usage: event=%#v usage=%#v", recorder.events[0], recorder.usages[0])
	}
}

func TestContextBudgetProviderRecorderFailureStopsBeforeInnerCall(t *testing.T) {
	inner := &contextBudgetStubProvider{}
	provider := NewContextBudgetProvider(inner, "chat", domainai.ContextBudgetPolicy{
		MaxContextTokens: 100,
		WarnAtRatio:      0.8,
		StopAtRatio:      0.95,
	}, &contextBudgetRecorderStub{err: errors.New("store unavailable")})

	_, err := provider.Generate(context.Background(), domainllm.GenerateRequest{
		Messages: []domainllm.Message{{Role: "user", Content: "hello"}},
	})

	if err == nil {
		t.Fatal("expected recorder failure")
	}
	if !strings.Contains(err.Error(), "llm context usage save failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.generateCalled {
		t.Fatal("inner Generate should not be called after recorder failure")
	}
}
