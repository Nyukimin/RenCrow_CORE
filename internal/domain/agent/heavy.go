package agent

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

const defaultHeavySystemPrompt = `You are Heavy, a deep analysis LLM for RenCrow.
Focus on careful diagnosis, root-cause analysis, assumption review, and final technical review.
Answer naturally and concretely in the user's language.`

// HeavyAgent は深い分析・診断用のLLM呼び出しを担当する。
type HeavyAgent struct {
	llmProvider            llm.LLMProvider
	systemPrompt           string
	runtimeContextProvider RuntimeContextProvider
	stableRuntimeContext   string
	conversationEngine     conversation.ConversationEngine
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

// ExecutePrivateWork executes a Task-owned Kuro assignment without recording a
// legacy public conversation turn. The current Kuro Gateway binding can run
// Codex with full access, so the existing provider is never called unless the
// Task owner supplied the exact full-access capability. This method does not
// create or infer that grant.
func (h *HeavyAgent) ExecutePrivateWork(ctx context.Context, input conversation.TurnInput, allowedCapabilities []string) (string, error) {
	capabilities, err := validatePrivateWorkEntry(ctx, input, "kuro", allowedCapabilities)
	if err != nil {
		return "", err
	}
	if !hasPrivateCapability(capabilities, privateCapabilityKuroCodexFullAccess) {
		return "", fmt.Errorf("%w: Kuro requires %s", ErrPrivateWorkCapabilityDenied, privateCapabilityKuroCodexFullAccess)
	}

	private := *h
	private.conversationEngine = nil
	return private.Generate(ctx, input)
}

func (h *HeavyAgent) Generate(ctx context.Context, t conversation.TurnInput) (string, error) {
	userMessage := stripHeavyCommand(t.MessageText())
	speechPlanMode := SpeechPlanCallbackFromContext(ctx) != nil
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
		if !reportDeterministicSpeechPlan(ctx, response) {
			if onToken := llm.StreamCallbackFromContext(ctx); onToken != nil {
				onToken(response)
			}
		}
		if h.conversationEngine != nil {
			if err := commitConversationTurn(ctx, h.conversationEngine, t, t.SessionID(), userMessage, response, conversation.SpeakerKuro, recallPack); err != nil {
				return response, err
			}
		}
		return response, nil
	}
	if speechPlanMode {
		messages = append(messages, llm.Message{Role: "system", Content: speechPlanInstruction(), Type: llm.PromptContextVariable, Metadata: map[string]string{"runtime_context_kind": "speech_plan_contract"}})
	}
	messages = assemblePromptContext(h.systemPrompt, currentRuntimeContext(ctx, h.runtimeContextProvider, "kuro", h.stableRuntimeContext), messages, userMessageWithAttachments(userMessage, t.Attachments()))
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   2048,
		Temperature: 0.4,
	})
	if speechPlanMode {
		req.ResponseFormat = llm.ResponseFormatJSONObject
	}
	resp, generationDuration, err := generateSpeechPlanResponse(ctx, h.llmProvider, req, speechPlanMode)
	if err != nil {
		return "", err
	}
	planResult, structuredSpeechPlan := parseSpeechPlan(ctx, resp.Content, generationDuration)
	response := conversation.StripLeadingSpeakerLabels(resp.Content, conversation.SpeakerKuro)
	if structuredSpeechPlan {
		response = planResult.DisplayText
		reportGenerationMetrics(ctx, resp)
	} else {
		response = enforceExactSharedRecallAnswer(userMessage, response, recallPack)
	}
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

func (h *HeavyAgent) WithRuntimeContextProvider(provider RuntimeContextProvider) *HeavyAgent {
	h.runtimeContextProvider = provider
	return h
}
