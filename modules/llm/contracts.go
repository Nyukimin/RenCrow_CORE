// Package llm defines language-model module contracts.
package llm

import (
	"context"

	"github.com/Nyukimin/picoclaw_multiLLM/modules/core"
)

type MessagePartType string

const (
	MessagePartText  MessagePartType = "text"
	MessagePartImage MessagePartType = "image"
	MessagePartAudio MessagePartType = "audio"
	MessagePartVideo MessagePartType = "video"
)

type MessagePart struct {
	Type     MessagePartType `json:"type"`
	Text     string          `json:"text,omitempty"`
	MimeType string          `json:"mime_type,omitempty"`
	Data     []byte          `json:"data,omitempty"`
}

type Message struct {
	Role    string        `json:"role"`
	Content string        `json:"content,omitempty"`
	Parts   []MessagePart `json:"parts,omitempty"`
}

type StreamCallback func(token string)

type GenerateRequest struct {
	Messages        []Message      `json:"messages"`
	MaxTokens       int            `json:"max_tokens,omitempty"`
	Temperature     float64        `json:"temperature,omitempty"`
	SystemPrompt    string         `json:"system_prompt,omitempty"`
	ProviderOptions map[string]any `json:"provider_options,omitempty"`
	OnToken         StreamCallback `json:"-"`
}

type GenerateResponse struct {
	Content      string          `json:"content"`
	TokensUsed   int             `json:"tokens_used,omitempty"`
	FinishReason string          `json:"finish_reason,omitempty"`
	ResponseID   core.ResponseID `json:"response_id,omitempty"`
}

type Provider interface {
	Name() string
	Health(ctx context.Context) core.HealthReport
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
}

type Router interface {
	SelectProvider(ctx context.Context, purpose string) (Provider, error)
}
