package llm

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// Message はLLMメッセージを表す
type Message struct {
	Role     string // "user", "assistant", "system"
	Content  string
	Parts    []MessagePart
	Type     PromptContextType
	Metadata map[string]string
	// PromptSource is CORE-only provenance for an exact source excerpt. It is
	// intentionally omitted from JSON/provider payload serialization.
	PromptSource *PromptSourceRef `json:"-"`
}

// PromptContextType identifies a prompt block by responsibility rather than
// by its position in the final message list.
type PromptContextType string

const (
	PromptContextCharacter PromptContextType = "character_system_prompt"
	PromptContextStable    PromptContextType = "stable_runtime_context"
	PromptContextRecall    PromptContextType = "recall_pack"
	PromptContextVariable  PromptContextType = "variable_runtime_context"
	PromptContextUser      PromptContextType = "user_message"
)

// MessagePartType identifies a multimodal message part.
type MessagePartType string

const (
	MessagePartText  MessagePartType = "text"
	MessagePartImage MessagePartType = "image"
	MessagePartAudio MessagePartType = "audio"
	MessagePartVideo MessagePartType = "video"
)

// MessagePart is an optional multimodal payload for Generate requests.
type MessagePart struct {
	Type     MessagePartType
	Text     string
	MimeType string
	Data     []byte
}

// StreamCallback はストリーミング時にトークンごとに呼ばれるコールバック
type StreamCallback func(token string)

const (
	LogicalOutcomeStreamV1 = "v1"
	LogicalOutcomeStreamV2 = "v2"
)

type LogicalOutcomeEventKind string

const (
	LogicalOutcomeAnswerStart      LogicalOutcomeEventKind = "answer_start"
	LogicalOutcomeAnswerDelta      LogicalOutcomeEventKind = "answer_delta"
	LogicalOutcomeAnswerDone       LogicalOutcomeEventKind = "answer_done"
	LogicalOutcomeRetrievalRequest LogicalOutcomeEventKind = "retrieval_request"
	LogicalOutcomeHandoff          LogicalOutcomeEventKind = "handoff"
)

// LogicalOutcomeEvent is one normalized control or text event from the
// opt-in RenCrow_LLM logical outcome stream. RetrievalRequests retains the
// exact JSON array for CORE's domain/schema validation.
type LogicalOutcomeEvent struct {
	Kind              LogicalOutcomeEventKind
	SchemaVersion     int
	Route             string
	PayloadKind       string
	Text              string
	FinishReason      string
	ThreadBoundary    string
	RetrievalRequests json.RawMessage
}

// LogicalOutcomeEventCallback lets the caller validate each typed event before
// its answer text is forwarded through StreamCallback. Returning an error
// aborts the provider stream.
type LogicalOutcomeEventCallback func(event LogicalOutcomeEvent) error

type GenerationMetrics struct {
	CompletionTokens int
	TokensPerSecond  float64
}

type GenerationMetricsCallback func(metrics GenerationMetrics)

var (
	ErrGenerateRequestAdmissionUnavailable = errors.New("generation request admission is unavailable")
	ErrGenerateRequestAdmissionConsumed    = errors.New("generation request admission was already consumed")
)

// GenerateRequestAdmission is a one-shot, process-local capability that a
// provider consumes after it has built the exact outbound request and before
// it dispatches that request. Its callback must perform a short in-memory
// admission only; it must not hold a guard across network I/O.
type GenerateRequestAdmission struct {
	mu    sync.Mutex
	used  bool
	admit func() error
}

// NewGenerateRequestAdmission constructs a one-shot request admission
// capability. A nil callback produces an unavailable capability.
func NewGenerateRequestAdmission(admit func() error) *GenerateRequestAdmission {
	return &GenerateRequestAdmission{admit: admit}
}

// Admit consumes this capability once. A failed admission also consumes it,
// so callers cannot replay a request after a revocation check failed.
func (a *GenerateRequestAdmission) Admit() error {
	if a == nil {
		return ErrGenerateRequestAdmissionUnavailable
	}
	a.mu.Lock()
	if a.used {
		a.mu.Unlock()
		return ErrGenerateRequestAdmissionConsumed
	}
	a.used = true
	admit := a.admit
	a.mu.Unlock()
	if admit == nil {
		return ErrGenerateRequestAdmissionUnavailable
	}
	return admit()
}

// ResponseFormat declares the logical assistant output contract.
// Provider and Model-specific enforcement stays behind the RenCrow_LLM boundary.
type ResponseFormat string

const (
	ResponseFormatText       ResponseFormat = ""
	ResponseFormatJSONObject ResponseFormat = "json_object"
)

// GenerateRequest はLLM生成リクエスト
type GenerateRequest struct {
	Messages        []Message
	MaxTokens       int
	Temperature     float64
	SystemPrompt    string
	ResponseFormat  ResponseFormat
	ReasoningEffort ReasoningEffort
	ProviderOptions map[string]any
	// RequestAdmission is internal and one-shot. It is consumed by the provider
	// immediately before outbound dispatch and never enters a provider payload.
	RequestAdmission *GenerateRequestAdmission `json:"-"`
	// LogicalOutcomeStream opts into a versioned structured SSE transport.
	// Empty preserves the ordinary Chat Completions stream contract.
	LogicalOutcomeStream  string
	OnLogicalOutcomeEvent LogicalOutcomeEventCallback
	OnToken               StreamCallback // nil = 非ストリーミング
	OnMetrics             GenerationMetricsCallback
}

// GenerateResponse はLLM生成レスポンス
type GenerateResponse struct {
	Content         string
	TokensUsed      int
	TokensPerSecond float64
	FinishReason    string
	HasToolCalls    bool
	RequestID       modulecore.RequestID
	ResponseID      modulecore.ResponseID
}

// LLMProvider はLLMプロバイダーの抽象化
type LLMProvider interface {
	Generate(ctx context.Context, req GenerateRequest) (GenerateResponse, error)
	Name() string
}

// --- Tool Calling 対応型 ---

// ChatMessage はツール呼び出し対応メッセージ
type ChatMessage struct {
	Role               string // "system", "user", "assistant", "tool"
	Content            string
	ToolCalls          []ToolCall // role="assistant" 時のツール呼び出し
	ProviderToolCallID string     // role="tool": provider-side correlation; not ActionID
	Type               PromptContextType
	Metadata           map[string]string
}

// ToolCall はLLMが返すツール呼び出し
type ToolCall struct {
	ID       string
	Function ToolCallFunction
}

// ToolCallFunction はツール呼び出しの関数情報
type ToolCallFunction struct {
	Name      string
	Arguments map[string]any
}

// ToolDefinition はLLMに渡すツール定義
type ToolDefinition struct {
	Type     string // "function"
	Function ToolFunctionDef
}

// ToolFunctionDef はツール関数の定義
type ToolFunctionDef struct {
	Name        string
	Description string
	Parameters  map[string]any // JSON Schema
}

// ReasoningEffort declares a provider-independent reasoning budget for tool calling.
type ReasoningEffort string

const (
	ReasoningEffortUnspecified ReasoningEffort = ""
	ReasoningEffortLow         ReasoningEffort = "low"
)

// ChatRequest はtool calling対応チャットリクエスト
type ChatRequest struct {
	Model           string
	Messages        []ChatMessage
	Tools           []ToolDefinition
	MaxTokens       int
	Temperature     float64
	ReasoningEffort ReasoningEffort
}

// ChatResponse はtool calling対応チャットレスポンス
type ChatResponse struct {
	Message      ChatMessage
	Done         bool
	FinishReason string // "stop" or "tool_calls"
	RequestID    modulecore.RequestID
	ResponseID   modulecore.ResponseID
}

// ToolCallingProvider はtool calling対応のLLMプロバイダー
type ToolCallingProvider interface {
	LLMProvider
	Chat(ctx context.Context, req ChatRequest) (ChatResponse, error)
}
