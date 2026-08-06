package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const (
	promptReceiptSchemaVersion    = 1
	promptReceiptMaxSentenceRunes = 240
)

// PromptReceiptProvider records a bounded, prompt-free receipt immediately
// before the LLM provider is called. It never persists the prompt body.
type PromptReceiptProvider struct {
	inner domainllm.LLMProvider
	name  string
}

type promptReceipt struct {
	SchemaVersion            int            `json:"schema_version"`
	CreatedAt                string         `json:"created_at"`
	Kind                     string         `json:"kind"`
	Provider                 string         `json:"provider"`
	RequestID                string         `json:"request_id,omitempty"`
	TraceID                  string         `json:"trace_id,omitempty"`
	JobID                    string         `json:"job_id,omitempty"`
	SessionID                string         `json:"session_id,omitempty"`
	Initiator                string         `json:"initiator,omitempty"`
	Caller                   string         `json:"caller,omitempty"`
	Purpose                  string         `json:"purpose,omitempty"`
	PromptHash               string         `json:"prompt_sha256"`
	SystemPromptHash         string         `json:"system_prompt_sha256"`
	PromptCharacters         int            `json:"prompt_characters"`
	SystemPromptCharacters   int            `json:"system_prompt_characters"`
	MessageCount             int            `json:"message_count"`
	SectionCounts            map[string]int `json:"section_counts"`
	LatestPromptSentence     string         `json:"latest_prompt_sentence,omitempty"`
	LatestPromptSentenceRole string         `json:"latest_prompt_sentence_role,omitempty"`
	RedactionApplied         bool           `json:"redaction_applied"`
}

type promptReceiptMessage struct {
	Role       string                  `json:"role"`
	Content    string                  `json:"content,omitempty"`
	Parts      []promptReceiptPart     `json:"parts,omitempty"`
	ToolCalls  []promptReceiptToolCall `json:"tool_calls,omitempty"`
	ToolCallID string                  `json:"tool_call_id,omitempty"`
}

type promptReceiptPart struct {
	Type       domainllm.MessagePartType `json:"type"`
	Text       string                    `json:"text,omitempty"`
	MimeType   string                    `json:"mime_type,omitempty"`
	ByteLength int                       `json:"byte_length,omitempty"`
	DataSHA256 string                    `json:"data_sha256,omitempty"`
}

type promptReceiptToolCall struct {
	ID       string                    `json:"id"`
	Function promptReceiptToolFunction `json:"function"`
}

type promptReceiptToolFunction struct {
	Name      string         `json:"name"`
	Arguments map[string]any `json:"arguments,omitempty"`
}

type promptReceiptHashPayload struct {
	Messages []promptReceiptMessage `json:"messages"`
	Tools    any                    `json:"tools,omitempty"`
}

var (
	promptReceiptOnce sync.Once
	promptReceiptFile rawLogSink
	promptReceiptErr  error
	promptReceiptMu   sync.Mutex

	promptReceiptSecretPattern = regexp.MustCompile(`(?i)(\b(?:password|passwd|token|api[_-]?key|secret|authorization|credential)\b\s*[:=]\s*)([^\s,;]+)`)
	promptReceiptBearerPattern = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
	promptReceiptEmailPattern  = regexp.MustCompile(`\b[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}\b`)
)

// NewPromptReceiptProvider wraps a provider with a bounded prompt receipt.
func NewPromptReceiptProvider(inner domainllm.LLMProvider, name string) *PromptReceiptProvider {
	return &PromptReceiptProvider{inner: inner, name: strings.TrimSpace(name)}
}

func (p *PromptReceiptProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	p.writeReceipt(buildGeneratePromptReceipt(ctx, p.Name(), req))
	return p.inner.Generate(ctx, req)
}

func (p *PromptReceiptProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	if p.inner == nil {
		return ""
	}
	return p.inner.Name()
}

func (p *PromptReceiptProvider) Chat(ctx context.Context, req domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	tcp, ok := p.inner.(domainllm.ToolCallingProvider)
	if !ok {
		return domainllm.ChatResponse{}, fmt.Errorf("inner provider does not support Chat")
	}
	p.writeReceipt(buildChatPromptReceipt(ctx, p.Name(), req))
	return tcp.Chat(ctx, req)
}

func (p *PromptReceiptProvider) writeReceipt(receipt promptReceipt) {
	data, err := json.Marshal(receipt)
	if err != nil {
		log.Printf("[LLM][prompt_receipt] marshal failed: %v", err)
		return
	}
	file := openPromptReceiptFile()
	if file == nil {
		return
	}
	promptReceiptMu.Lock()
	defer promptReceiptMu.Unlock()
	data = append(data, '\n')
	if _, err := file.Write(data); err != nil {
		log.Printf("[LLM][prompt_receipt] write failed: %v", err)
		return
	}
	if err := file.Sync(); err != nil {
		log.Printf("[LLM][prompt_receipt] fsync failed: %v", err)
	}
}

func openPromptReceiptFile() rawLogSink {
	promptReceiptOnce.Do(func() {
		promptReceiptFile, promptReceiptErr = openRawLogSink("RENCROW_PROMPT_RECEIPT_LOG", "prompt_receipt.jsonl", "prompt receipt")
	})
	if promptReceiptErr != nil {
		return nil
	}
	return promptReceiptFile
}

func buildGeneratePromptReceipt(ctx context.Context, provider string, req domainllm.GenerateRequest) promptReceipt {
	messages := make([]promptReceiptMessage, 0, len(req.Messages)+1)
	if strings.TrimSpace(req.SystemPrompt) != "" {
		messages = append(messages, promptReceiptMessage{Role: "system", Content: req.SystemPrompt})
	}
	for _, message := range req.Messages {
		messages = append(messages, receiptMessageFromMessage(message))
	}
	return buildPromptReceipt(ctx, provider, "generate", messages, nil)
}

func buildChatPromptReceipt(ctx context.Context, provider string, req domainllm.ChatRequest) promptReceipt {
	messages := make([]promptReceiptMessage, 0, len(req.Messages))
	for _, message := range req.Messages {
		toolCalls := make([]promptReceiptToolCall, 0, len(message.ToolCalls))
		for _, call := range message.ToolCalls {
			toolCalls = append(toolCalls, promptReceiptToolCall{
				ID: call.ID,
				Function: promptReceiptToolFunction{
					Name:      call.Function.Name,
					Arguments: call.Function.Arguments,
				},
			})
		}
		messages = append(messages, promptReceiptMessage{
			Role:       message.Role,
			Content:    message.Content,
			ToolCalls:  toolCalls,
			ToolCallID: message.ToolCallID,
		})
	}
	return buildPromptReceipt(ctx, provider, "chat", messages, req.Tools)
}

func receiptMessageFromMessage(message domainllm.Message) promptReceiptMessage {
	parts := make([]promptReceiptPart, 0, len(message.Parts))
	for _, part := range message.Parts {
		item := promptReceiptPart{
			Type:       part.Type,
			Text:       part.Text,
			MimeType:   part.MimeType,
			ByteLength: len(part.Data),
		}
		if len(part.Data) > 0 {
			digest := sha256.Sum256(part.Data)
			item.DataSHA256 = hex.EncodeToString(digest[:])
		}
		parts = append(parts, item)
	}
	return promptReceiptMessage{Role: message.Role, Content: message.Content, Parts: parts}
}

func buildPromptReceipt(ctx context.Context, provider, kind string, messages []promptReceiptMessage, tools any) promptReceipt {
	sections := make(map[string]int)
	var allText strings.Builder
	var systemText strings.Builder
	for _, message := range messages {
		role := strings.ToLower(strings.TrimSpace(message.Role))
		if role == "" {
			role = "unknown"
		}
		sections[role]++
		text := receiptMessageText(message)
		allText.WriteString(text)
		allText.WriteByte('\n')
		if role == "system" {
			if systemText.Len() > 0 {
				systemText.WriteString("\n\n")
			}
			systemText.WriteString(text)
		}
	}
	payload, _ := json.Marshal(promptReceiptHashPayload{Messages: messages, Tools: tools})
	promptDigest := sha256.Sum256(payload)
	systemDigest := sha256.Sum256([]byte(systemText.String()))

	receipt := promptReceipt{
		SchemaVersion:          promptReceiptSchemaVersion,
		CreatedAt:              time.Now().UTC().Format(time.RFC3339Nano),
		Kind:                   kind,
		Provider:               provider,
		PromptHash:             hex.EncodeToString(promptDigest[:]),
		SystemPromptHash:       hex.EncodeToString(systemDigest[:]),
		PromptCharacters:       utf8.RuneCountInString(allText.String()),
		SystemPromptCharacters: utf8.RuneCountInString(systemText.String()),
		MessageCount:           len(messages),
		SectionCounts:          sections,
		RedactionApplied:       false,
	}
	if sentence := latestPromptSentence(systemText.String()); sentence != "" {
		receipt.LatestPromptSentence = redactPromptReceiptText(sentence)
		receipt.LatestPromptSentenceRole = "system"
		receipt.RedactionApplied = receipt.LatestPromptSentence != sentence
	}
	if observation, ok := domainllm.ExecutionObservationFromContext(ctx); ok {
		receipt.RequestID = observation.RequestID
		receipt.TraceID = observation.TraceID
		receipt.JobID = observation.JobID
		receipt.SessionID = observation.SessionID
		receipt.Initiator = observation.Initiator
		receipt.Caller = observation.Caller
		receipt.Purpose = observation.Purpose
	}
	return receipt
}

func receiptMessageText(message promptReceiptMessage) string {
	var b strings.Builder
	b.WriteString(message.Content)
	for _, part := range message.Parts {
		if part.Text != "" {
			if b.Len() > 0 {
				b.WriteString("\n")
			}
			b.WriteString(part.Text)
		}
	}
	return b.String()
}

func latestPromptSentence(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	last := ""
	start := 0
	for index, r := range text {
		if !isPromptSentenceBoundaryAt(text, index, r) {
			continue
		}
		candidate := strings.TrimSpace(text[start:index])
		if candidate != "" {
			last = candidate
		}
		start = index + utf8.RuneLen(r)
	}
	if candidate := strings.TrimSpace(text[start:]); candidate != "" {
		last = candidate
	}
	return truncatePromptReceiptSentence(last)
}

func isPromptSentenceBoundaryAt(text string, index int, r rune) bool {
	if r != '.' {
		return r == '。' || r == '！' || r == '？' || r == '!' || r == '?' || r == '\n' || r == '\r'
	}
	nextIndex := index + utf8.RuneLen(r)
	if nextIndex >= len(text) {
		return true
	}
	next, _ := utf8.DecodeRuneInString(text[nextIndex:])
	return unicode.IsSpace(next) || strings.ContainsRune("。！？!?", next)
}

func truncatePromptReceiptSentence(value string) string {
	value = strings.Join(strings.Fields(value), " ")
	runes := []rune(value)
	if len(runes) <= promptReceiptMaxSentenceRunes {
		return value
	}
	return string(runes[:promptReceiptMaxSentenceRunes]) + "…"
}

func redactPromptReceiptText(value string) string {
	redacted := promptReceiptSecretPattern.ReplaceAllString(value, "$1[REDACTED]")
	redacted = promptReceiptBearerPattern.ReplaceAllString(redacted, "Bearer [REDACTED]")
	redacted = promptReceiptEmailPattern.ReplaceAllString(redacted, "[REDACTED_EMAIL]")
	redacted = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\t' {
			return ' '
		}
		return r
	}, redacted)
	return truncatePromptReceiptSentence(redacted)
}
