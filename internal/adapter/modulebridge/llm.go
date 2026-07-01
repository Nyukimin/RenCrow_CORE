package modulebridge

import (
	"context"

	domainllm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

type LLMProviderAdapter struct {
	provider domainllm.LLMProvider
}

func NewLLMProviderAdapter(provider domainllm.LLMProvider) *LLMProviderAdapter {
	return &LLMProviderAdapter{provider: provider}
}

func NewLLMRoleProviders(chat, worker, heavy, wild domainllm.LLMProvider) map[string]modulellm.Provider {
	return modulellm.BuildRoleProviderMap(modulellm.RoleProviders{
		Chat:   NewLLMProviderAdapter(chat),
		Worker: NewLLMProviderAdapter(worker),
		Heavy:  NewLLMProviderAdapter(heavy),
		Wild:   NewLLMProviderAdapter(wild),
	})
}

func (a *LLMProviderAdapter) Name() string {
	if a == nil || a.provider == nil {
		return ""
	}
	return a.provider.Name()
}

func (a *LLMProviderAdapter) Health(context.Context) core.HealthReport {
	if a == nil || a.provider == nil {
		return modulellm.BuildProviderHealth(modulellm.ProviderHealthSnapshot{})
	}
	return modulellm.BuildProviderHealth(modulellm.ProviderHealthSnapshot{Provider: a.provider.Name(), Ready: true})
}

func (a *LLMProviderAdapter) Generate(ctx context.Context, req modulellm.GenerateRequest) (modulellm.GenerateResponse, error) {
	req = modulellm.CloneGenerateRequest(req)
	resp, err := a.provider.Generate(ctx, domainllm.GenerateRequest{
		Messages:        toDomainLLMMessages(req.Messages),
		MaxTokens:       req.MaxTokens,
		Temperature:     req.Temperature,
		SystemPrompt:    req.SystemPrompt,
		ProviderOptions: req.ProviderOptions,
		OnToken:         domainllm.StreamCallback(req.OnToken),
	})
	if err != nil {
		return modulellm.GenerateResponse{}, err
	}
	return modulellm.BuildGenerateResponse(modulellm.GenerateOutput{
		Content:      resp.Content,
		TokensUsed:   resp.TokensUsed,
		FinishReason: resp.FinishReason,
	}), nil
}

func toDomainLLMMessages(in []modulellm.Message) []domainllm.Message {
	out := make([]domainllm.Message, 0, len(in))
	for _, msg := range in {
		out = append(out, domainllm.Message{
			Role:    msg.Role,
			Content: msg.Content,
			Parts:   toDomainLLMParts(msg.Parts),
		})
	}
	return out
}

func toDomainLLMParts(in []modulellm.MessagePart) []domainllm.MessagePart {
	out := make([]domainllm.MessagePart, 0, len(in))
	for _, part := range in {
		out = append(out, domainllm.MessagePart{
			Type:     domainllm.MessagePartType(part.Type),
			Text:     part.Text,
			MimeType: part.MimeType,
			Data:     part.Data,
		})
	}
	return out
}
