package agent

import (
	"context"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const defaultHeavySystemPrompt = `You are Heavy, a deep analysis LLM for RenCrow.
Focus on careful diagnosis, root-cause analysis, assumption review, and final technical review.
Answer naturally and concretely in the user's language.`

// HeavyAgent は深い分析・診断用のLLM呼び出しを担当する。
type HeavyAgent struct {
	llmProvider          llm.LLMProvider
	systemPrompt         string
	stableRuntimeContext string
	conversationEngine   conversation.ConversationEngine
}

func NewHeavyAgent(llmProvider llm.LLMProvider, systemPrompt string) *HeavyAgent {
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultHeavySystemPrompt
	}
	return &HeavyAgent{llmProvider: llmProvider, systemPrompt: systemPrompt}
}

func (h *HeavyAgent) WithConversationEngine(engine conversation.ConversationEngine) *HeavyAgent {
	h.conversationEngine = engine
	return h
}

func (h *HeavyAgent) WithStableRuntimeContext(content string) *HeavyAgent {
	h.stableRuntimeContext = strings.TrimSpace(content)
	return h
}

func (h *HeavyAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	userMessage := stripHeavyCommand(t.UserMessage())
	messages := []llm.Message{}
	var recallPack *conversation.RecallPack
	if h.conversationEngine != nil {
		pack, err := h.conversationEngine.BeginTurn(ctx, t.SessionID(), userMessage)
		if err != nil {
			log.Printf("[Heavy] BeginTurn failed: %v", err)
		} else if pack != nil {
			filtered := pack.FilterForRole("heavy").WithoutPersonaSystemPrompt()
			recallPack = &filtered
			messages = appendSharedConversationContinuityPrompt(messages, &filtered)
			messages = append(messages, filtered.ToPromptMessages()...)
		}
	}
	if response, ok := exactSharedRecallAnswer(userMessage, recallPack); ok {
		if onToken := llm.StreamCallbackFromContext(ctx); onToken != nil {
			onToken(response)
		}
		if h.conversationEngine != nil {
			if err := commitConversationTurn(ctx, h.conversationEngine, t, t.SessionID(), userMessage, response, conversation.SpeakerKuro, recallPack); err != nil {
				return response, err
			}
		}
		return response, nil
	}
	messages = assemblePromptContext(h.systemPrompt, h.stableRuntimeContext, messages, userMessageWithAttachments(userMessage, t.Attachments()))
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   2048,
		Temperature: 0.4,
	})
	resp, err := h.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	response := strings.TrimSpace(resp.Content)
	response = enforceExactSharedRecallAnswer(userMessage, response, recallPack)
	if h.conversationEngine != nil {
		if err := commitConversationTurn(ctx, h.conversationEngine, t, t.SessionID(), userMessage, response, conversation.SpeakerKuro, recallPack); err != nil {
			return response, err
		}
	}
	return response, nil
}

func stripHeavyCommand(message string) string {
	trimmed := strings.TrimSpace(message)
	if strings.HasPrefix(trimmed, "/analyze") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "/analyze"))
	}
	if strings.HasPrefix(trimmed, "/heavy") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "/heavy"))
	}
	return trimmed
}
