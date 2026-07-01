package conversation

import (
	"context"
	"fmt"
	"strings"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// コンパイル時インターフェース適合チェック
var _ domconv.ConversationSummarizer = (*LLMSummarizer)(nil)

// LLMSummarizer は LLMProvider を使って会話を要約・キーワード抽出する
type LLMSummarizer struct {
	provider llm.LLMProvider
}

// NewLLMSummarizer は新しい LLMSummarizer を生成する
func NewLLMSummarizer(provider llm.LLMProvider) *LLMSummarizer {
	return &LLMSummarizer{provider: provider}
}

// Summarize は Thread を LLM で要約する
func (s *LLMSummarizer) Summarize(ctx context.Context, thread *domconv.Thread) (string, error) {
	if len(thread.Turns) == 0 {
		return "", fmt.Errorf("thread has no messages")
	}

	prompt := buildSummarizePrompt(thread)
	resp, err := s.provider.Generate(ctx, llm.GenerateRequest{
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:   256,
		Temperature: 0.3,
	})
	if err != nil {
		return "", fmt.Errorf("LLM summarize failed: %w", err)
	}
	return strings.TrimSpace(resp.Content), nil
}

// ExtractKeywords は Thread から LLM でキーワードを抽出する
func (s *LLMSummarizer) ExtractKeywords(ctx context.Context, thread *domconv.Thread) ([]string, error) {
	if len(thread.Turns) == 0 {
		return []string{thread.Domain}, nil
	}

	prompt := buildKeywordsPrompt(thread)
	resp, err := s.provider.Generate(ctx, llm.GenerateRequest{
		Messages:    []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:   64,
		Temperature: 0.1,
	})
	if err != nil {
		return nil, fmt.Errorf("LLM keyword extraction failed: %w", err)
	}

	return parseKeywords(resp.Content), nil
}

// buildSummarizePrompt は要約用プロンプトを構築する
func buildSummarizePrompt(thread *domconv.Thread) string {
	var sb strings.Builder
	sb.WriteString("以下の会話を1〜2文で簡潔に要約してください。\n\n")
	for _, msg := range thread.Turns {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", msg.Speaker, msg.Msg))
	}
	sb.WriteString("\n要約:")
	return sb.String()
}

// buildKeywordsPrompt はキーワード抽出用プロンプトを構築する
func buildKeywordsPrompt(thread *domconv.Thread) string {
	var sb strings.Builder
	sb.WriteString("以下の会話から重要なキーワードを3〜5個抽出してください。改行またはカンマ区切りで出力してください。\n\n")
	for _, msg := range thread.Turns {
		sb.WriteString(fmt.Sprintf("[%s] %s\n", msg.Speaker, msg.Msg))
	}
	sb.WriteString("\nキーワード:")
	return sb.String()
}

// parseKeywords はLLMレスポンスからキーワードのスライスを生成する
func parseKeywords(raw string) []string {
	raw = strings.TrimSpace(raw)
	// カンマ区切りを改行に統一
	raw = strings.ReplaceAll(raw, ",", "\n")
	raw = strings.ReplaceAll(raw, "、", "\n")

	parts := strings.Split(raw, "\n")
	keywords := make([]string, 0, len(parts))
	for _, p := range parts {
		kw := strings.TrimSpace(p)
		if kw != "" {
			keywords = append(keywords, kw)
		}
	}
	return keywords
}
