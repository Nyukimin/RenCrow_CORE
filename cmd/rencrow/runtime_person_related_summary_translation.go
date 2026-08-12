package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type personRelatedSummaryTranslator interface {
	TranslateDescription(context.Context, string, string) (string, error)
}

type runtimePersonRelatedSummaryTranslator struct{ provider llm.LLMProvider }

func (t runtimePersonRelatedSummaryTranslator) TranslateDescription(ctx context.Context, original, language string) (string, error) {
	original = strings.TrimSpace(original)
	if t.provider == nil || original == "" {
		return "", fmt.Errorf("summary translation provider or original is unavailable")
	}
	if len([]rune(original)) > 8000 {
		return "", fmt.Errorf("summary source exceeds translation bound")
	}
	response, err := t.provider.Generate(ctx, llm.GenerateRequest{
		SystemPrompt: "作品概要の本文だけを自然な日本語へ翻訳してください。作品名、人名、固有名詞の名前は翻訳しないでください。名前を新造・補完せず、説明本文以外を出力しないでください。",
		Messages:     []llm.Message{{Role: "user", Content: fmt.Sprintf("source_language=%s\ndescription_original=%s", strings.TrimSpace(language), original)}},
		MaxTokens:    2000, Temperature: 0, ResponseFormat: llm.ResponseFormatJSONObject,
	})
	if err != nil {
		return "", fmt.Errorf("translate summary description: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewBufferString(strings.TrimSpace(response.Content)))
	decoder.DisallowUnknownFields()
	var payload struct {
		DescriptionJA string `json:"description_ja"`
	}
	if err := decoder.Decode(&payload); err != nil {
		return "", fmt.Errorf("decode summary translation: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return "", fmt.Errorf("summary translation has trailing data")
	}
	translated := strings.TrimSpace(payload.DescriptionJA)
	if translated == "" || len([]rune(translated)) > 8000 || !containsJapaneseRune(translated) {
		return "", fmt.Errorf("summary translation is not bounded Japanese text")
	}
	return translated, nil
}

func containsJapaneseRune(value string) bool {
	for _, r := range value {
		if unicode.In(r, unicode.Hiragana, unicode.Katakana, unicode.Han) {
			return true
		}
	}
	return false
}
