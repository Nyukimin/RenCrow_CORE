package conversation

import (
	"context"
	"fmt"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation/l1sqlite"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

type LLMDailyDigestSummarizer struct {
	provider llm.LLMProvider
}

func NewLLMDailyDigestSummarizer(provider llm.LLMProvider) *LLMDailyDigestSummarizer {
	return &LLMDailyDigestSummarizer{provider: provider}
}

func (s *LLMDailyDigestSummarizer) SummarizeDailyDigest(ctx context.Context, digestDate time.Time, category string, slot string, news []l1sqlite.L1NewsItem) (string, error) {
	if s == nil || s.provider == nil {
		return "", fmt.Errorf("daily digest llm provider is nil")
	}
	if len(news) == 0 {
		return "", fmt.Errorf("daily digest requires news items")
	}
	resp, err := s.provider.Generate(ctx, llm.GenerateRequest{
		Messages:    []llm.Message{{Role: "user", Content: buildDailyDigestPrompt(digestDate, category, slot, news)}},
		MaxTokens:   256,
		Temperature: 0.2,
	})
	if err != nil {
		return "", fmt.Errorf("LLM daily digest summarize failed: %w", err)
	}
	text := strings.TrimSpace(resp.Content)
	if text == "" {
		return "", fmt.Errorf("LLM daily digest summary is empty")
	}
	return text, nil
}

func buildDailyDigestPrompt(digestDate time.Time, category string, slot string, news []l1sqlite.L1NewsItem) string {
	var sb strings.Builder
	sb.WriteString("以下のニュース候補を、雑談で使いやすい日本語の短いDaily Digestに要約してください。\n")
	sb.WriteString("出典本文と要約案を混ぜず、事実を足さず、3〜5行でまとめてください。\n\n")
	sb.WriteString("date: " + digestDate.UTC().Format("2006-01-02") + "\n")
	sb.WriteString("category: " + strings.TrimSpace(category) + "\n")
	sb.WriteString("slot: " + strings.TrimSpace(slot) + "\n\n")
	for i, item := range news {
		sb.WriteString(fmt.Sprintf("[%d]\n", i+1))
		sb.WriteString("source_id: " + item.SourceID + "\n")
		sb.WriteString("source_url: " + item.SourceURL + "\n")
		if !item.PublishedAt.IsZero() {
			sb.WriteString("published_at: " + item.PublishedAt.UTC().Format(time.RFC3339) + "\n")
		}
		if strings.TrimSpace(item.SummaryDraft) != "" {
			sb.WriteString("summary_draft: " + strings.TrimSpace(item.SummaryDraft) + "\n")
		}
		if strings.TrimSpace(item.RawText) != "" {
			sb.WriteString("raw_text: " + strings.TrimSpace(item.RawText) + "\n")
		}
		sb.WriteString("\n")
	}
	sb.WriteString("Daily Digest:")
	return sb.String()
}
