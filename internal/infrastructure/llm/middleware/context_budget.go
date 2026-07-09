package middleware

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	domainllm "github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type ContextBudgetRecorder interface {
	SaveContextUsage(ctx context.Context, item domainai.ContextUsage) error
	SaveWorkflowEvent(ctx context.Context, item domainai.WorkflowEvent) error
}

type ContextBudgetProvider struct {
	inner  domainllm.LLMProvider
	name   string
	policy domainai.ContextBudgetPolicy
	rec    ContextBudgetRecorder
}

func NewContextBudgetProvider(inner domainllm.LLMProvider, name string, policy domainai.ContextBudgetPolicy, recorder ...ContextBudgetRecorder) *ContextBudgetProvider {
	provider := &ContextBudgetProvider{
		inner:  inner,
		name:   strings.TrimSpace(name),
		policy: policy,
	}
	if len(recorder) > 0 {
		provider.rec = recorder[0]
	}
	return provider
}

func (p *ContextBudgetProvider) Generate(ctx context.Context, req domainllm.GenerateRequest) (domainllm.GenerateResponse, error) {
	if err := p.checkBudget(ctx, estimateGenerateContextTokens(req), 0); err != nil {
		return domainllm.GenerateResponse{}, err
	}
	return p.inner.Generate(ctx, req)
}

func (p *ContextBudgetProvider) Chat(ctx context.Context, req domainllm.ChatRequest) (domainllm.ChatResponse, error) {
	tcp, ok := p.inner.(domainllm.ToolCallingProvider)
	if !ok {
		return domainllm.ChatResponse{}, fmt.Errorf("inner provider does not support Chat")
	}
	if err := p.checkBudget(ctx, estimateChatContextTokens(req), len(req.Tools)); err != nil {
		return domainllm.ChatResponse{}, err
	}
	return tcp.Chat(ctx, req)
}

func (p *ContextBudgetProvider) Name() string {
	if p.name != "" {
		return p.name
	}
	return p.inner.Name()
}

func (p *ContextBudgetProvider) checkBudget(ctx context.Context, contextTokens int, toolCallCount int) error {
	now := time.Now().UTC()
	usage := domainai.ContextUsage{
		EventID:       fmt.Sprintf("ctx_llm_%s_%d", strings.ToLower(p.Name()), now.UnixNano()),
		Agent:         p.Name(),
		ContextTokens: contextTokens,
		InputTokens:   contextTokens,
		ToolCallCount: toolCallCount,
		CreatedAt:     now,
	}
	decision, err := domainai.EvaluateContextBudget(usage, p.policy)
	if err != nil {
		return err
	}
	if err := p.recordContextBudget(ctx, usage, decision); err != nil {
		return err
	}
	switch decision.Status {
	case domainai.ContextBudgetStatusStop:
		return fmt.Errorf("context budget exceeded for %s: %s", p.Name(), decision.Reason)
	case domainai.ContextBudgetStatusWarn:
		log.Printf("[LLM][context_budget] provider=%s status=warn reason=%q context_tokens=%d max=%d",
			p.Name(), decision.Reason, decision.ContextTokens, decision.MaxContextTokens)
	}
	return nil
}

func (p *ContextBudgetProvider) recordContextBudget(ctx context.Context, usage domainai.ContextUsage, decision domainai.ContextBudgetDecision) error {
	if p.rec == nil {
		return nil
	}
	if err := p.rec.SaveContextUsage(ctx, usage); err != nil {
		return fmt.Errorf("llm context usage save failed: %w", err)
	}
	eventType := ""
	switch decision.Status {
	case domainai.ContextBudgetStatusWarn:
		eventType = "context_budget_warning"
	case domainai.ContextBudgetStatusStop:
		eventType = "context_budget_exceeded"
	default:
		return nil
	}
	now := time.Now().UTC()
	event := domainai.WorkflowEvent{
		EventID:       fmt.Sprintf("evt_llm_context_budget_%s_%d", strings.ToLower(p.Name()), now.UnixNano()),
		ParentEventID: usage.EventID,
		EventType:     eventType,
		Agent:         p.Name(),
		Status:        decision.Status,
		CreatedAt:     now,
		CompletedAt:   now,
		Summary:       decision.Reason,
	}
	if err := p.rec.SaveWorkflowEvent(ctx, event); err != nil {
		return fmt.Errorf("llm context budget event save failed: %w", err)
	}
	return nil
}

func estimateGenerateContextTokens(req domainllm.GenerateRequest) int {
	total := estimateTextTokens(req.SystemPrompt)
	for _, msg := range req.Messages {
		total += estimateTextTokens(msg.Role)
		total += estimateTextTokens(msg.Content)
		for _, part := range msg.Parts {
			total += estimateTextTokens(string(part.Type))
			total += estimateTextTokens(part.Text)
			total += len(part.Data) / 1024
		}
	}
	if req.MaxTokens > 0 {
		total += req.MaxTokens
	}
	return total
}

func estimateChatContextTokens(req domainllm.ChatRequest) int {
	total := estimateTextTokens(req.Model)
	for _, msg := range req.Messages {
		total += estimateTextTokens(msg.Role)
		total += estimateTextTokens(msg.Content)
		total += estimateTextTokens(msg.ToolCallID)
		for _, call := range msg.ToolCalls {
			total += estimateTextTokens(call.ID)
			total += estimateTextTokens(call.Function.Name)
			for key, value := range call.Function.Arguments {
				total += estimateTextTokens(key)
				total += estimateTextTokens(fmt.Sprint(value))
			}
		}
	}
	for _, tool := range req.Tools {
		total += estimateTextTokens(tool.Type)
		total += estimateTextTokens(tool.Function.Name)
		total += estimateTextTokens(tool.Function.Description)
	}
	return total
}

func estimateTextTokens(text string) int {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0
	}
	count := utf8.RuneCountInString(text)
	tokens := count / 4
	if tokens == 0 {
		return 1
	}
	return tokens
}
