package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const maxThreadSummaryResponseBytes = 64 * 1024

// コンパイル時インターフェース適合チェック
var _ domconv.ConversationSummarizer = (*LLMSummarizer)(nil)

// LLMSummarizer は LLMProvider を使って会話の意味残余を1回で生成する。
type LLMSummarizer struct {
	provider llm.LLMProvider
}

// NewLLMSummarizer は新しい LLMSummarizer を生成する。
func NewLLMSummarizer(provider llm.LLMProvider) *LLMSummarizer {
	return &LLMSummarizer{provider: provider}
}

// Summarize は Thread を1回のJSON生成で要約・キーワード抽出する。
func (s *LLMSummarizer) Summarize(ctx context.Context, thread *domconv.Thread) (domconv.SummaryResidual, error) {
	providerName := threadSummaryProviderName(s)
	if thread == nil || len(thread.Turns) == 0 {
		return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerInvalid
	}
	if s == nil || s.provider == nil || providerName == domconv.ThreadSummaryProviderNotConfigured {
		return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerNotConfigured
	}

	requestCtx := llm.WithExecutionObservation(ctx, llm.ExecutionObservation{
		SessionID: thread.SessionID, Initiator: "shiro", Caller: "conversation.thread", Purpose: "summarize",
	})
	resp, err := s.provider.Generate(requestCtx, llm.GenerateRequest{
		Messages:       []llm.Message{{Role: "user", Content: buildSummarizePrompt(thread)}},
		MaxTokens:      256,
		Temperature:    0.3,
		ResponseFormat: llm.ResponseFormatJSONObject,
	})
	if err != nil {
		return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerUnavailable
	}
	if len(resp.Content) > maxThreadSummaryResponseBytes {
		return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerInvalid
	}
	if !utf8.ValidString(resp.Content) {
		return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerInvalid
	}
	residual, err := decodeSummaryResponse(resp.Content)
	if err != nil {
		return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerInvalid
	}
	residual.Provider = providerName
	return residual, nil
}

func threadSummaryProviderName(s *LLMSummarizer) string {
	if s == nil || s.provider == nil {
		return domconv.ThreadSummaryProviderNotConfigured
	}
	name := strings.TrimSpace(s.provider.Name())
	if name == "" {
		return domconv.ThreadSummaryProviderNotConfigured
	}
	return name
}

// buildSummarizePrompt は要約とキーワードを同じ生成に要求する。
func buildSummarizePrompt(thread *domconv.Thread) string {
	var sb strings.Builder
	sb.WriteString("以下の会話を、発言者の帰属を保持して要約してください。Mio、Shiro、Kuro、Midoriの発言を別のAgentへ帰属させないでください。")
	sb.WriteString("応答はJSONオブジェクト1個だけにし、キーはsummaryとkeywordsだけにしてください。summaryは1〜1024文字、keywordsは3〜5個です。Markdown、説明文、余分なキーは不要です。\n\n")
	for _, msg := range thread.Turns {
		fmt.Fprintf(&sb, "[%s] %s\n", msg.Speaker, msg.Msg)
	}
	sb.WriteString("\nJSON:")
	return sb.String()
}

// decodeSummaryResponse accepts exactly one JSON object with exactly two keys.
// encoding/json's map decoding permits duplicate keys, so keys are consumed
// explicitly to reject duplicates as well as unknown fields and trailing data.
func decodeSummaryResponse(raw string) (domconv.SummaryResidual, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return domconv.SummaryResidual{}, err
	}
	delim, ok := token.(json.Delim)
	if !ok || delim != '{' {
		return domconv.SummaryResidual{}, fmt.Errorf("summary response must be an object")
	}

	seen := make(map[string]struct{}, 2)
	var summaryRaw, keywordsRaw json.RawMessage
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return domconv.SummaryResidual{}, err
		}
		key, ok := keyToken.(string)
		if !ok {
			return domconv.SummaryResidual{}, fmt.Errorf("summary response key must be a string")
		}
		if _, exists := seen[key]; exists {
			return domconv.SummaryResidual{}, fmt.Errorf("duplicate summary response key")
		}
		seen[key] = struct{}{}

		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return domconv.SummaryResidual{}, err
		}
		switch key {
		case "summary":
			summaryRaw = value
		case "keywords":
			keywordsRaw = value
		default:
			return domconv.SummaryResidual{}, fmt.Errorf("unknown summary response key")
		}
	}
	closing, err := decoder.Token()
	if err != nil {
		return domconv.SummaryResidual{}, err
	}
	closingDelim, ok := closing.(json.Delim)
	if !ok || closingDelim != '}' {
		return domconv.SummaryResidual{}, fmt.Errorf("summary response object is not closed")
	}
	if len(seen) != 2 {
		return domconv.SummaryResidual{}, fmt.Errorf("summary response requires summary and keywords")
	}
	if _, err := decoder.Token(); err != io.EOF {
		if err == nil {
			return domconv.SummaryResidual{}, fmt.Errorf("trailing summary response data")
		}
		return domconv.SummaryResidual{}, err
	}

	var summary string
	if err := json.Unmarshal(summaryRaw, &summary); err != nil {
		return domconv.SummaryResidual{}, err
	}
	var keywords []string
	if err := json.Unmarshal(keywordsRaw, &keywords); err != nil {
		return domconv.SummaryResidual{}, err
	}
	return domconv.NormalizeSummaryResidual(domconv.SummaryResidual{Summary: summary, Keywords: keywords})
}
