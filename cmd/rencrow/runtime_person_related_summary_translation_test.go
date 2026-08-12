package main

import (
	"context"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type summaryTranslationProvider struct {
	request llm.GenerateRequest
}

func (p *summaryTranslationProvider) Name() string { return "summary-translation-test" }
func (p *summaryTranslationProvider) Generate(_ context.Context, request llm.GenerateRequest) (llm.GenerateResponse, error) {
	p.request = request
	return llm.GenerateResponse{Content: `{"description_ja":"日本語の作品概要です。"}`}, nil
}

func TestRuntimeSummaryTranslatorTranslatesDescriptionOnly(t *testing.T) {
	provider := &summaryTranslationProvider{}
	translator := runtimePersonRelatedSummaryTranslator{provider: provider}
	got, err := translator.TranslateDescription(context.Background(), "An original work summary.", "en")
	if err != nil || got != "日本語の作品概要です。" {
		t.Fatalf("translation=%q err=%v", got, err)
	}
	if provider.request.ResponseFormat != llm.ResponseFormatJSONObject || !strings.Contains(provider.request.SystemPrompt, "作品名") || !strings.Contains(provider.request.SystemPrompt, "翻訳しない") {
		t.Fatalf("translation contract missing: %#v", provider.request)
	}
}

func TestRuntimeSummaryTranslatorRejectsNonJapaneseOutput(t *testing.T) {
	provider := &summaryTranslationProvider{}
	translator := runtimePersonRelatedSummaryTranslator{provider: provider}
	providerResponse := provider.Generate
	_ = providerResponse
	translator.provider = llmProviderFunc(func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
		return llm.GenerateResponse{Content: `{"description_ja":"English only"}`}, nil
	})
	if _, err := translator.TranslateDescription(context.Background(), "Original", "en"); err == nil {
		t.Fatal("non-Japanese translation accepted")
	}
}

type llmProviderFunc func(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error)

func (f llmProviderFunc) Generate(ctx context.Context, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	return f(ctx, req)
}
func (llmProviderFunc) Name() string { return "func" }
