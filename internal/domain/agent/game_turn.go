package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const gameTurnMaxTokens = 1200

// DecideGameTurn is a dedicated Agent cognition path for game observations.
// Unlike Chat/Execute, the environment observation is not recorded as a user
// utterance and cannot trigger tools or side effects.
func (m *MioAgent) DecideGameTurn(ctx context.Context, prompt string, recipient string) (string, error) {
	if m == nil || m.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	messages := []llm.Message{}
	if systemPrompt := m.systemPromptForViewerRecipient(recipient); systemPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: systemPrompt})
	}
	messages = append(messages,
		llm.Message{Role: "system", Content: gameTurnBoundaryPrompt},
		llm.Message{Role: "user", Content: prompt},
	)
	resp, err := m.llmProvider.Generate(ctx, m.generationRequest(messages, nil))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (h *HeavyAgent) DecideGameTurn(ctx context.Context, prompt string) (string, error) {
	if h == nil || h.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	resp, err := h.llmProvider.Generate(ctx, llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		SystemPrompt: h.systemPrompt + "\n" + gameTurnBoundaryPrompt,
		Messages:     []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:    gameTurnMaxTokens,
		Temperature:  0.3,
	}))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (w *WildAgent) DecideGameTurn(ctx context.Context, prompt string) (string, error) {
	if w == nil || w.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	resp, err := w.llmProvider.Generate(ctx, llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		SystemPrompt: w.systemPrompt + "\n" + gameTurnBoundaryPrompt,
		Messages:     []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:    gameTurnMaxTokens,
		Temperature:  0.4,
	}))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

const gameTurnBoundaryPrompt = `This is an environment observation for an Agent-owned game turn, not a user message.
Decide only the requested game action. Do not call tools, modify files, or perform other side effects.
Return only the strict JSON object requested by the game turn prompt.`
