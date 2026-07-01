package agent

import (
	"context"
	"log"
	"strings"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/llm"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/domain/task"
)

const defaultHeavySystemPrompt = `You are Heavy, a deep analysis LLM for RenCrow.
Focus on careful diagnosis, root-cause analysis, assumption review, and final technical review.
Answer naturally and concretely in the user's language.`

// HeavyAgent は深い分析・診断用のLLM呼び出しを担当する。
type HeavyAgent struct {
	llmProvider        llm.LLMProvider
	systemPrompt       string
	conversationEngine conversation.ConversationEngine
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

func (h *HeavyAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	userMessage := stripHeavyCommand(t.UserMessage())
	messages := []llm.Message{}
	if h.conversationEngine != nil {
		recallPack, err := h.conversationEngine.BeginTurn(ctx, t.ChatID(), userMessage)
		if err != nil {
			log.Printf("[Heavy] BeginTurn failed: %v", err)
		} else if recallPack != nil {
			filtered := recallPack.FilterForRole("heavy")
			if err := recordRecallTrace(ctx, h.conversationEngine, t.ChatID(), t.JobID().String(), "heavy", filtered); err != nil {
				log.Printf("[Heavy] RecordRecallTrace failed: %v", err)
			}
			messages = append(messages, filtered.ToPromptMessages()...)
		}
	}
	messages = append(messages, userMessageWithAttachments(userMessage, t.Attachments()))
	req := llm.GenerateRequest{
		SystemPrompt: h.systemPrompt,
		Messages:     messages,
		MaxTokens:    2048,
		Temperature:  0.4,
	}
	resp, err := h.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	response := strings.TrimSpace(resp.Content)
	if h.conversationEngine != nil {
		if err := endConversationTurnAs(ctx, h.conversationEngine, t.ChatID(), userMessage, response, conversation.Speaker("heavy")); err != nil {
			log.Printf("[Heavy] EndTurn failed: %v", err)
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
