package conversation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	ThreadSummaryGenerationLLM                   = "llm"
	ThreadSummaryGenerationDeterministicFallback = "deterministic_fallback"
	ThreadSummaryGenerationLegacyUnverified      = "legacy_unverified"

	ThreadSummaryFailureUnavailable    = "thread_summarizer_unavailable"
	ThreadSummaryFailureInvalid        = "thread_summarizer_invalid"
	ThreadSummaryFailureNotConfigured  = "thread_summarizer_not_configured"
	ThreadSummaryProviderNotConfigured = "not_configured"
)

var (
	ErrThreadSummarizerUnavailable   = errors.New(ThreadSummaryFailureUnavailable)
	ErrThreadSummarizerInvalid       = errors.New(ThreadSummaryFailureInvalid)
	ErrThreadSummarizerNotConfigured = errors.New(ThreadSummaryFailureNotConfigured)
)

// SummaryResidual is the bounded semantic residual returned by one summarizer call.
// Provenance is deliberately absent; CORE derives it from the source thread.
type SummaryResidual struct {
	Summary  string
	Keywords []string
	Provider string
}

// ConversationSummarizer は会話スレッドを1回の生成で要約・キーワード抽出する。
type ConversationSummarizer interface {
	Summarize(ctx context.Context, thread *Thread) (SummaryResidual, error)
}

// NormalizeSummaryResidual applies the canonical whitespace and keyword rules.
func NormalizeSummaryResidual(residual SummaryResidual) (SummaryResidual, error) {
	summary, err := normalizeSummaryText(residual.Summary, 1024)
	if err != nil {
		return SummaryResidual{}, err
	}
	if len(residual.Keywords) < 3 || len(residual.Keywords) > 5 {
		return SummaryResidual{}, fmt.Errorf("keywords must contain 3 to 5 items")
	}
	keywords := make([]string, 0, len(residual.Keywords))
	seen := make(map[string]struct{}, len(residual.Keywords))
	for _, keyword := range residual.Keywords {
		normalized, err := normalizeSummaryText(keyword, 64)
		if err != nil {
			return SummaryResidual{}, fmt.Errorf("invalid keyword: %w", err)
		}
		folded := strings.ToLower(normalized)
		if _, exists := seen[folded]; exists {
			return SummaryResidual{}, fmt.Errorf("keywords must be unique")
		}
		seen[folded] = struct{}{}
		keywords = append(keywords, normalized)
	}
	return SummaryResidual{Summary: summary, Keywords: keywords, Provider: strings.TrimSpace(residual.Provider)}, nil
}

// ValidateSummaryResidual requires a value already in canonical form.
func ValidateSummaryResidual(residual SummaryResidual) error {
	normalized, err := NormalizeSummaryResidual(residual)
	if err != nil {
		return err
	}
	if normalized.Summary != residual.Summary {
		return fmt.Errorf("summary is not normalized")
	}
	if len(normalized.Keywords) != len(residual.Keywords) {
		return fmt.Errorf("keywords are not normalized")
	}
	for i := range normalized.Keywords {
		if normalized.Keywords[i] != residual.Keywords[i] {
			return fmt.Errorf("keywords are not normalized")
		}
	}
	return nil
}

func normalizeSummaryText(value string, maxRunes int) (string, error) {
	if !utf8.ValidString(value) {
		return "", fmt.Errorf("text must be valid UTF-8")
	}
	if strings.ContainsAny(value, "\r\n\x00") {
		return "", fmt.Errorf("text contains a forbidden control character")
	}
	var builder strings.Builder
	spacePending := false
	for _, r := range value {
		if unicode.IsSpace(r) {
			spacePending = builder.Len() > 0
			continue
		}
		if spacePending {
			builder.WriteByte(' ')
			spacePending = false
		}
		builder.WriteRune(r)
	}
	normalized := builder.String()
	runeCount := utf8.RuneCountInString(normalized)
	if runeCount < 1 || runeCount > maxRunes {
		return "", fmt.Errorf("text must contain 1 to %d runes", maxRunes)
	}
	return normalized, nil
}
