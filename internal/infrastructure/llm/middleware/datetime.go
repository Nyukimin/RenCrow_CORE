package middleware

import (
	"context"
	"fmt"
	"time"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

// DateTimeProvider は全てのLLMリクエストに現在日時を注入するデコレータ
type DateTimeProvider struct {
	Inner domainllm.LLMProvider
}

// NewDateTimeProvider はデコレータを作成する
func NewDateTimeProvider(inner domainllm.LLMProvider) *DateTimeProvider {
	return &DateTimeProvider{Inner: inner}
}

// Generate は安定した履歴prefixを保ったまま、最新user messageへ現在日時を注入する。
// 動的な日時を先頭へ置くと、日時が変わるたびに物理LLMのprompt cacheが全失効するため、
// 既存contextを削らず末尾側だけを変化させる。
func (p *DateTimeProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	now := time.Now().Format("2006年1月2日15時04分")
	req.Messages = injectGenerateDateTime(req.Messages, dateTimeInstruction(now))
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
	// ChatMessageでも安定した履歴prefixを保持する。
	now := time.Now().Format("2006年1月2日15時04分")
	req.Messages = injectChatDateTime(req.Messages, dateTimeInstruction(now))
	return tcp.Chat(ctx, req)
}

func dateTimeInstruction(now string) string {
	return fmt.Sprintf("【重要】現在日時は%sです。この日時を正確な現在時刻として扱ってください。あなたの学習データより新しい情報が必要な場合はその旨を伝えてください。", now)
}

func injectGenerateDateTime(messages []domainllm.Message, instruction string) []domainllm.Message {
	result := append([]domainllm.Message(nil), messages...)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i].Role != "user" {
			continue
		}
		if len(result[i].Parts) > 0 {
			result[i].Parts = append([]domainllm.MessagePart(nil), result[i].Parts...)
			result[i].Parts = append([]domainllm.MessagePart{{Type: domainllm.MessagePartText, Text: instruction}}, result[i].Parts...)
		} else {
			result[i].Content = instruction + "\n\n" + result[i].Content
		}
		return result
	}
	return append(result, domainllm.Message{Role: "user", Content: instruction})
}

func injectChatDateTime(messages []domainllm.ChatMessage, instruction string) []domainllm.ChatMessage {
	result := append([]domainllm.ChatMessage(nil), messages...)
	for i := len(result) - 1; i >= 0; i-- {
		if result[i].Role == "user" {
			result[i].Content = instruction + "\n\n" + result[i].Content
			return result
		}
	}
	return append(result, domainllm.ChatMessage{Role: "user", Content: instruction})
}
