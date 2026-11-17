package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const gameTurnMaxTokens = 16
const structuredGameTurnMaxTokens = 256

// DecideGameTurn is a dedicated Agent cognition path for game observations.
// Unlike Chat/Execute, the environment observation is not recorded as a user
// utterance and cannot trigger tools or side effects.
func (m *MioAgent) DecideGameTurn(ctx context.Context, prompt string, recipient string) (string, error) {
	if m == nil || m.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	messages := []llm.Message{}
	if systemPrompt := m.systemPromptForViewerRecipient(recipient); systemPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: systemPrompt, Type: llm.PromptContextCharacter})
	}
	messages = append(messages,
		llm.Message{Role: "system", Content: gameTurnBoundaryPrompt, Type: llm.PromptContextStable, Metadata: map[string]string{"runtime_context_kind": "game_turn_boundary"}},
		llm.Message{Role: "user", Content: prompt, Type: llm.PromptContextUser},
	)
	request := m.generationRequest(messages, nil)
	request.MaxTokens = gameTurnMaxTokens
	request.ResponseFormat = llm.ResponseFormatText
	request.ProviderOptions = gameTurnProviderOptions(request.ProviderOptions)
	request.OnToken = nil
	resp, err := m.llmProvider.Generate(ctx, request)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

// DecideStructuredGameTurn asks the selected Mio persona for one structured
// game action. It shares the real Agent identity/context path with the token
// contract while keeping its JSON generation settings separate.
func (m *MioAgent) DecideStructuredGameTurn(ctx context.Context, prompt string, recipient string) (string, error) {
	if m == nil || m.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	messages := []llm.Message{}
	if systemPrompt := m.systemPromptForViewerRecipient(recipient); systemPrompt != "" {
		messages = append(messages, llm.Message{Role: "system", Content: systemPrompt, Type: llm.PromptContextCharacter})
	}
	messages = append(messages,
		llm.Message{Role: "system", Content: structuredGameTurnBoundaryPrompt, Type: llm.PromptContextStable, Metadata: map[string]string{"runtime_context_kind": "game_turn_boundary"}},
		llm.Message{Role: "user", Content: prompt, Type: llm.PromptContextUser},
	)
	request := m.generationRequest(messages, nil)
	request.MaxTokens = structuredGameTurnMaxTokens
	request.ResponseFormat = llm.ResponseFormatJSONObject
	request.OnToken = nil
	resp, err := m.llmProvider.Generate(ctx, request)
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
		SystemPrompt:    h.systemPrompt + "\n" + gameTurnBoundaryPrompt,
		Messages:        []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:       gameTurnMaxTokens,
		Temperature:     0.3,
		ResponseFormat:  llm.ResponseFormatText,
		ProviderOptions: gameTurnProviderOptions(nil),
	}))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (h *HeavyAgent) DecideStructuredGameTurn(ctx context.Context, prompt string) (string, error) {
	if h == nil || h.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	resp, err := h.llmProvider.Generate(ctx, llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		SystemPrompt:   h.systemPrompt + "\n" + structuredGameTurnBoundaryPrompt,
		Messages:       []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:      structuredGameTurnMaxTokens,
		Temperature:    0.3,
		ResponseFormat: llm.ResponseFormatJSONObject,
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
		SystemPrompt:    w.systemPrompt + "\n" + gameTurnBoundaryPrompt,
		Messages:        []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:       gameTurnMaxTokens,
		Temperature:     0.4,
		ResponseFormat:  llm.ResponseFormatText,
		ProviderOptions: gameTurnProviderOptions(nil),
	}))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

func (w *WildAgent) DecideStructuredGameTurn(ctx context.Context, prompt string) (string, error) {
	if w == nil || w.llmProvider == nil {
		return "", fmt.Errorf("Agent is unavailable")
	}
	resp, err := w.llmProvider.Generate(ctx, llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		SystemPrompt:   w.systemPrompt + "\n" + structuredGameTurnBoundaryPrompt,
		Messages:       []llm.Message{{Role: "user", Content: prompt}},
		MaxTokens:      structuredGameTurnMaxTokens,
		Temperature:    0.4,
		ResponseFormat: llm.ResponseFormatJSONObject,
	}))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}

const gameTurnBoundaryPrompt = `This is an environment observation for an Agent-owned game turn, not a user message.
Decide only the requested game action. Do not call tools, modify files, or perform other side effects.
Return only the exact action token requested by the game turn prompt.`

const structuredGameTurnBoundaryPrompt = `This is an environment observation for an Agent-owned game turn, not a user message.
Decide one complete game action using only the provided action contracts and observation.
Return one JSON object with the fields action, optional target, and optional args. Do not include other fields or prose.
Do not invent targets or arguments that are not supported by the observation.`

func gameTurnProviderOptions(existing map[string]any) map[string]any {
	options := make(map[string]any, len(existing)+1)
	for key, value := range existing {
		options[key] = value
	}
	options["stop"] = []string{",", "\n", " ", "\""}
	return options
}
