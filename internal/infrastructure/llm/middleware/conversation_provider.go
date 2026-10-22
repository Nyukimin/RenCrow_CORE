package middleware

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	domainai "github.com/Nyukimin/RenCrow_CORE/internal/domain/aiworkflow"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type ConversationProvider struct {
	inner  llm.LLMProvider
	name   string
	policy domainai.ContextBudgetPolicy
	slot   ConversationExecutionSlot
}

type ConversationExecutionSlot interface {
	AcquireConversation(context.Context, conversation.ConversationScope) (context.Context, func(), error)
}

func NewConversationProvider(inner llm.LLMProvider, name string, policy domainai.ContextBudgetPolicy) *ConversationProvider {
	return &ConversationProvider{inner: inner, name: strings.TrimSpace(name), policy: policy}
}

func (p *ConversationProvider) WithExecutionSlot(slot ConversationExecutionSlot) *ConversationProvider {
	if p == nil {
		return nil
	}
	cloned := *p
	cloned.slot = slot
	return &cloned
}

func (p *ConversationProvider) GenerateConversation(ctx context.Context, scope conversation.ConversationScope, req llm.GenerateRequest) (llm.GenerateResponse, error) {
	if err := scope.Validate(); err != nil {
		return llm.GenerateResponse{}, fmt.Errorf("validate conversation scope: %w", err)
	}
	if p == nil || p.inner == nil {
		return llm.GenerateResponse{}, errors.New("conversation gateway provider is unavailable")
	}
	if err := ctx.Err(); err != nil {
		return llm.GenerateResponse{}, err
	}
	if req.OnToken != nil {
		return llm.GenerateResponse{}, errors.New("conversation provider requires non-streaming generation")
	}
	req = cloneConversationGenerateRequest(req)
	req.ResponseFormat = llm.ResponseFormatJSONObject
	// The caller cannot select a model, decoder, or arbitrary provider behavior.
	req.ProviderOptions = conversationProviderOptions(req.ProviderOptions)
	req.ReasoningEffort = llm.ReasoningEffortUnspecified
	req = llm.WithCurrentJSTTimeNow(req)
	name := p.name
	if name == "" {
		name = p.inner.Name()
	}
	if err := p.checkBudget(name, req); err != nil {
		return llm.GenerateResponse{}, err
	}
	callCtx := ctx
	release := func() {}
	if p.slot != nil {
		reservedCtx, reservedRelease, err := p.slot.AcquireConversation(ctx, scope)
		if err != nil {
			return llm.GenerateResponse{}, fmt.Errorf("reserve conversation generation slot: %w", err)
		}
		if reservedCtx == nil {
			if reservedRelease != nil {
				reservedRelease()
			}
			return llm.GenerateResponse{}, errors.New("conversation execution slot returned a nil context")
		}
		callCtx = reservedCtx
		if reservedRelease != nil {
			release = reservedRelease
		}
	}
	defer release()
	return p.inner.Generate(callCtx, req)
}

func conversationProviderOptions(options map[string]any) map[string]any {
	if len(options) == 0 {
		return nil
	}
	const (
		optionTopP = "top_p"
		optionTopK = "top_k"
		optionMinP = "min_p"
		optionSeed = "seed"
	)
	allowed := map[string]struct{}{optionTopP: {}, optionTopK: {}, optionMinP: {}, optionSeed: {}}
	filtered := make(map[string]any, len(allowed))
	for key, value := range options {
		if _, ok := allowed[key]; ok {
			filtered[key] = value
		}
	}
	if len(filtered) == 0 {
		return nil
	}
	return filtered
}

func (p *ConversationProvider) checkBudget(name string, req llm.GenerateRequest) error {
	now := time.Now().UTC()
	contextTokens := estimateGenerateContextTokens(req)
	usage := domainai.ContextUsage{
		EventID:       fmt.Sprintf("conversation-context-%d", now.UnixNano()),
		Agent:         name,
		ContextTokens: contextTokens,
		InputTokens:   contextTokens,
		CreatedAt:     now,
	}
	decision, err := domainai.EvaluateContextBudget(usage, p.policy)
	if err != nil {
		return fmt.Errorf("evaluate conversation context budget: %w", err)
	}
	switch decision.Status {
	case domainai.ContextBudgetStatusStop:
		return fmt.Errorf("conversation context budget exceeded for %s: %s", name, decision.Reason)
	case domainai.ContextBudgetStatusWarn:
		log.Printf("[LLM][conversation_context_budget] provider=%s status=warn reason=%q context_tokens=%d max=%d",
			name, decision.Reason, decision.ContextTokens, decision.MaxContextTokens)
	}
	return nil
}

func cloneConversationGenerateRequest(req llm.GenerateRequest) llm.GenerateRequest {
	if req.Messages == nil {
		return req
	}
	messages := make([]llm.Message, len(req.Messages))
	for index, message := range req.Messages {
		messages[index] = message
		if message.Parts != nil {
			messages[index].Parts = make([]llm.MessagePart, len(message.Parts))
			for partIndex, part := range message.Parts {
				messages[index].Parts[partIndex] = part
				messages[index].Parts[partIndex].Data = append([]byte(nil), part.Data...)
			}
		}
		if message.Metadata != nil {
			messages[index].Metadata = make(map[string]string, len(message.Metadata))
			for key, value := range message.Metadata {
				messages[index].Metadata[key] = value
			}
		}
		messages[index].PromptSource = llm.ClonePromptSourceRef(message.PromptSource)
	}
	req.Messages = messages
	return req
}
