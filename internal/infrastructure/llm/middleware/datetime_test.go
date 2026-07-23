package middleware

import (
	"context"
	"strings"
	"testing"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type dateTimeCaptureProvider struct {
	generateRequest domainllm.GenerateRequest
	chatRequest     domainllm.ChatRequest
}

func (p *dateTimeCaptureProvider) Generate(_ context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.generateRequest = req
	return domainllm.GenerateResponse{}, nil
}

func (p *dateTimeCaptureProvider) Chat(_ context.Context, req domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	p.chatRequest = req
	return domainllm.ChatResponse{}, nil
}

func (p *dateTimeCaptureProvider) Name() string { return "capture" }

func TestDateTimeProviderKeepsStableGeneratePrefix(t *testing.T) {
	inner := &dateTimeCaptureProvider{}
	provider := NewDateTimeProvider(inner)
	original := []domainllm.Message{
		{Role: "system", Content: "stable persona"},
		{Role: "assistant", Content: "previous answer"},
		{Role: "user", Content: "current question"},
	}

	if _, err := provider.Generate(context.Background(), domainllm.GenerateRequest{Messages: original}); err != nil {
		t.Fatalf("Generate() error = %v", err)
	}

	got := inner.generateRequest.Messages
	if len(got) != len(original) {
		t.Fatalf("message count changed: got %d want %d", len(got), len(original))
	}
	if got[0].Role != original[0].Role || got[0].Content != original[0].Content ||
		got[1].Role != original[1].Role || got[1].Content != original[1].Content {
		t.Fatalf("stable prompt prefix changed: got %#v", got[:2])
	}
	if !strings.Contains(got[2].Content, "【重要】現在日時") || !strings.Contains(got[2].Content, original[2].Content) {
		t.Fatalf("latest user message must retain content and receive datetime: %q", got[2].Content)
	}
}

func TestDateTimeProviderKeepsStableChatPrefix(t *testing.T) {
	inner := &dateTimeCaptureProvider{}
	provider := NewDateTimeProvider(inner)
	original := []domainllm.ChatMessage{
		{Role: "system", Content: "stable tool persona"},
		{Role: "assistant", Content: "previous tool answer"},
		{Role: "user", Content: "current tool question"},
	}

	if _, err := provider.Chat(context.Background(), domainllm.ChatRequest{Messages: original}); err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	got := inner.chatRequest.Messages
	if len(got) != len(original) {
		t.Fatalf("message count changed: got %d want %d", len(got), len(original))
	}
	if got[0].Role != original[0].Role || got[0].Content != original[0].Content ||
		got[1].Role != original[1].Role || got[1].Content != original[1].Content {
		t.Fatalf("stable chat prefix changed: got %#v", got[:2])
	}
	if !strings.Contains(got[2].Content, "【重要】現在日時") || !strings.Contains(got[2].Content, original[2].Content) {
		t.Fatalf("latest user message must retain content and receive datetime: %q", got[2].Content)
	}
}
