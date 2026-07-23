package middleware

import (
	"context"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// NoThinkingProvider enforces the Chat no-thinking contract.
type NoThinkingProvider struct {
	inner domainllm.LLMProvider
}

func NewNoThinkingProvider(inner domainllm.LLMProvider) domainllm.LLMProvider {
	if inner == nil {
		return nil
	}
	return &NoThinkingProvider{inner: inner}
}

func (p *NoThinkingProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	options := make(map[string]any, len(req.ProviderOptions)+1)
	for key, value := range req.ProviderOptions {
		options[key] = value
	}
	options["think"] = false
	kwargs := map[string]any{}
	if configured, ok := options["chat_template_kwargs"].(map[string]any); ok {
		for key, value := range configured {
			kwargs[key] = value
		}
	}
	kwargs["enable_thinking"] = false
	options["chat_template_kwargs"] = kwargs
	req.ProviderOptions = options
	return p.inner.Generate(ctx, req)
}

func (p *NoThinkingProvider) Name() string {
	return p.inner.Name()
}

// LowThinkingProvider enforces the GPT-OSS ChatWorker reasoning-low contract.
type LowThinkingProvider struct {
	inner domainllm.LLMProvider
}

func NewLowThinkingProvider(inner domainllm.LLMProvider) domainllm.LLMProvider {
	if inner == nil {
		return nil
	}
	return &LowThinkingProvider{inner: inner}
}

func (p *LowThinkingProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	options := make(map[string]any, len(req.ProviderOptions)+2)
	for key, value := range req.ProviderOptions {
		options[key] = value
	}
	options["think"] = "low"
	options["reasoning_effort"] = "low"
	kwargs := map[string]any{}
	if configured, ok := options["chat_template_kwargs"].(map[string]any); ok {
		for key, value := range configured {
			kwargs[key] = value
		}
	}
	kwargs["enable_thinking"] = true
	kwargs["reasoning_effort"] = "low"
	options["chat_template_kwargs"] = kwargs
	req.ProviderOptions = options
	return p.inner.Generate(ctx, req)
}

func (p *LowThinkingProvider) Name() string {
	return p.inner.Name()
}
