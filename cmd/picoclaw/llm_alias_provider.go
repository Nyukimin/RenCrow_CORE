package main

import (
	"context"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

type namedLLMProvider struct {
	name  string
	inner llm.LLMProvider
}

func (p namedLLMProvider) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return p.inner.Generate(ctx, req)
}

func (p namedLLMProvider) Name() string {
	if name := strings.TrimSpace(p.name); name != "" {
		return name
	}
	if p.inner != nil {
		return p.inner.Name()
	}
	return ""
}
