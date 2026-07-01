package middleware

import (
	"context"
	"fmt"
	"time"

	domainllm "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
)

// DateTimeProvider は全てのLLMリクエストに現在日時を注入するデコレータ
type DateTimeProvider struct {
	Inner domainllm.LLMProvider
}

// NewDateTimeProvider はデコレータを作成する
func NewDateTimeProvider(inner domainllm.LLMProvider) *DateTimeProvider {
	return &DateTimeProvider{Inner: inner}
}

// Generate は現在日時をシステムメッセージとして先頭に追加してから内部プロバイダーに委譲する
func (p *DateTimeProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	now := time.Now().Format("2006年1月2日15時04分")
	dateMsg := domainllm.Message{
		Role:    "system",
		Content: fmt.Sprintf("【重要】現在日時は%sです。この日時を正確な現在時刻として扱ってください。あなたの学習データより新しい情報が必要な場合はその旨を伝えてください。", now),
	}
	req.Messages = append([]domainllm.Message{dateMsg}, req.Messages...)
	return p.Inner.Generate(ctx, req)
}

// Name は内部プロバイダー名を返す
func (p *DateTimeProvider) Name() string {
	return p.Inner.Name()
}

// Chat はToolCallingProviderの場合にのみ委譲する（ToolCallingProvider実装）
func (p *DateTimeProvider) Chat(ctx context.Context, req domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	tcp, ok := p.Inner.(domainllm.ToolCallingProvider)
	if !ok {
		return domainllm.ChatResponse{}, fmt.Errorf("inner provider does not support Chat")
	}
	// ChatMessage にも日時を注入
	now := time.Now().Format("2006年1月2日15時04分")
	dateMsg := domainllm.ChatMessage{
		Role:    "system",
		Content: fmt.Sprintf("【重要】現在日時は%sです。この日時を正確な現在時刻として扱ってください。あなたの学習データより新しい情報が必要な場合はその旨を伝えてください。", now),
	}
	req.Messages = append([]domainllm.ChatMessage{dateMsg}, req.Messages...)
	return tcp.Chat(ctx, req)
}
