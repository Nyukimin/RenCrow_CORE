package modulebridge

import (
	"context"
	"testing"

	domainllm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

type fakeDomainLLMProvider struct {
	req domainllm.GenerateRequest
}

func (p *fakeDomainLLMProvider) Name() string {
	return "fake-llm"
}

func (p *fakeDomainLLMProvider) Generate(_ context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.req = req
	if req.OnToken != nil {
		req.OnToken("stream-token")
	}
	return domainllm.GenerateResponse{
		Content:      "generated",
		TokensUsed:   12,
		FinishReason: "stop",
	}, nil
}

func TestLLMProviderAdapterGenerate(t *testing.T) {
	provider := &fakeDomainLLMProvider{}
	adapter := NewLLMProviderAdapter(provider)
	var streamed []string

	got, err := adapter.Generate(context.Background(), modulellm.GenerateRequest{
		Messages: []modulellm.Message{
			{
				Role:    "user",
				Content: "hello",
				Parts: []modulellm.MessagePart{
					{Type: modulellm.MessagePartImage, MimeType: "image/png", Data: []byte("png")},
				},
			},
		},
		MaxTokens:    99,
		Temperature:  0.3,
		SystemPrompt: "system",
		OnToken: func(token string) {
			streamed = append(streamed, token)
		},
	})
	if err != nil {
		t.Fatalf("Generate returned error: %v", err)
	}
	if got.Content != "generated" || got.TokensUsed != 12 || got.FinishReason != "stop" {
		t.Fatalf("unexpected response: %+v", got)
	}
	if provider.req.MaxTokens != 99 || provider.req.Temperature != 0.3 || provider.req.SystemPrompt != "system" {
		t.Fatalf("request fields were not mapped: %+v", provider.req)
	}
	if len(provider.req.Messages) != 1 || len(provider.req.Messages[0].Parts) != 1 {
		t.Fatalf("message parts were not mapped: %+v", provider.req.Messages)
	}
	if provider.req.Messages[0].Parts[0].Type != domainllm.MessagePartImage {
		t.Fatalf("message part type was not mapped: %+v", provider.req.Messages[0].Parts[0])
	}
	if len(streamed) != 1 || streamed[0] != "stream-token" {
		t.Fatalf("stream callback was not preserved: %+v", streamed)
	}
}

func TestNewLLMRoleProviders(t *testing.T) {
	chat := &fakeDomainLLMProvider{}
	worker := &fakeDomainLLMProvider{}
	heavy := &fakeDomainLLMProvider{}
	wild := &fakeDomainLLMProvider{}

	got := NewLLMRoleProviders(chat, worker, heavy, wild)
	for _, role := range []string{"chat", "worker", "heavy", "wild"} {
		if got[role] == nil {
			t.Fatalf("role provider missing: %s in %+v", role, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("unexpected role provider count: %+v", got)
	}
}
