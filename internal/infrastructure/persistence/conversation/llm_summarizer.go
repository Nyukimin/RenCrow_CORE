package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const maxThreadSummaryResponseBytes = 64 * 1024

// コンパイル時インターフェース適合チェック
var _ domconv.ConversationSummarizer = (*LLMSummarizer)(nil)

// defaultSummaryAttemptTimeout bounds one provider attempt. A busy slot must
// fail over to the next provider instead of consuming the whole follower
// budget in one queue.
const defaultSummaryAttemptTimeout = 25 * time.Second

// LLMSummarizer は LLMProvider chain を使って会話の意味残余を生成する。
// 各providerを最大1回、attemptTimeout以内で試し、全滅なら呼出元の
// deterministic fallbackに委ねる（2026-08-19 利用者指示:
// Worker -> Wild -> Chat -> LLMなし）。
type LLMSummarizer struct {
	providers      []llm.LLMProvider
	attemptTimeout time.Duration
}

// NewLLMSummarizer は単一providerのSummarizerを生成する（互換入口）。
func NewLLMSummarizer(provider llm.LLMProvider) *LLMSummarizer {
	return NewLLMSummarizerChain(provider)
}

// NewLLMSummarizerChain は先頭から順に試すprovider chainを生成する。
// nil providerは静かにスキップされる。
func NewLLMSummarizerChain(providers ...llm.LLMProvider) *LLMSummarizer {
	chain := make([]llm.LLMProvider, 0, len(providers))
	for _, p := range providers {
		if p != nil {
			chain = append(chain, p)
		}
	}
	return &LLMSummarizer{providers: chain, attemptTimeout: defaultSummaryAttemptTimeout}
}

// Summarize は各providerを順に最大1回試し、最初に成功した有効なJSONを返す。
func (s *LLMSummarizer) Summarize(ctx context.Context, thread *domconv.Thread) (domconv.SummaryResidual, error) {
	if thread == nil || len(thread.Turns) == 0 {
		return domconv.SummaryResidual{Provider: domconv.ThreadSummaryProviderNotConfigured}, domconv.ErrThreadSummarizerInvalid
	}
	if s == nil || len(s.providers) == 0 {
		return domconv.SummaryResidual{Provider: domconv.ThreadSummaryProviderNotConfigured}, domconv.ErrThreadSummarizerNotConfigured
	}

	requestCtx := llm.WithExecutionObservation(ctx, llm.ExecutionObservation{
		SessionID: thread.SessionID, Initiator: "shiro", Caller: "conversation.thread", Purpose: "summarize",
	})
	prompt := buildSummarizePrompt(thread)
	lastErr := domconv.ErrThreadSummarizerUnavailable
	lastProvider := domconv.ThreadSummaryProviderNotConfigured
	attempted := 0
	for _, provider := range s.providers {
		providerName := strings.TrimSpace(provider.Name())
		if providerName == "" {
			continue
		}
		attempted++
		lastProvider = providerName
		if err := ctx.Err(); err != nil {
			return domconv.SummaryResidual{Provider: providerName}, domconv.ErrThreadSummarizerUnavailable
		}
		attemptCtx, cancel := context.WithTimeout(requestCtx, s.attemptTimeout)
		resp, err := provider.Generate(attemptCtx, llm.GenerateRequest{
			Messages:        []llm.Message{{Role: "user", Content: prompt}},
			MaxTokens:       1024,
			Temperature:     0.3,
			ResponseFormat:  llm.ResponseFormatJSONObject,
			ReasoningEffort: llm.ReasoningEffortLow,
		})
		cancel()
		if err != nil {
			lastErr = domconv.ErrThreadSummarizerUnavailable
			continue
		}
		if len(resp.Content) > maxThreadSummaryResponseBytes || !utf8.ValidString(resp.Content) {
			lastErr = domconv.ErrThreadSummarizerInvalid
			continue
		}
		residual, err := decodeSummaryResponse(resp.Content)
		if err != nil {
			lastErr = domconv.ErrThreadSummarizerInvalid
			continue
		}
		residual.Provider = providerName
		return residual, nil
	}
	if attempted == 0 {
		return domconv.SummaryResidual{Provider: domconv.ThreadSummaryProviderNotConfigured}, domconv.ErrThreadSummarizerNotConfigured
	}
	return domconv.SummaryResidual{Provider: lastProvider}, lastErr
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
