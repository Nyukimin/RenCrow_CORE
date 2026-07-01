package main

import (
	"context"
	"log"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

func warmPrimaryLLMProviders(parent context.Context, providers map[string]llm.LLMProvider, timeout time.Duration) {
	if timeout <= 0 {
		timeout = localLLMDefaultTimeout
	}
	for alias, provider := range providers {
		ctx, cancel := context.WithTimeout(parent, timeout)
		_, err := provider.Generate(ctx, llm.GenerateRequest{
			Messages:  []llm.Message{{Role: "user", Content: "warmup"}},
			MaxTokens: 1,
		})
		cancel()
		if err != nil {
			log.Printf("WARN: local LLM warmup failed alias=%s provider=%s err=%v", alias, provider.Name(), err)
			continue
		}
		log.Printf("Local LLM warmup ok alias=%s provider=%s", alias, provider.Name())
	}
}
