package agent

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

var ErrImageGeneratorUnavailable = errors.New("RenCrow_Image interface is unavailable")

const defaultWildSystemPrompt = `You are Wild, a creative LLM for RenCrow.
Focus on story generation, image search, image generation, image analysis, image prompts, mood, composition, clothing, texture, and visual interpretation.
When image generation is requested, use the RenCrow_Image interface.
Answer naturally and concretely in the user's language.`

// WildAgent は創作Wild用のLLM呼び出しを担当する。
type WildAgent struct {
	llmProvider          llm.LLMProvider
	systemPrompt         string
	stableRuntimeContext string
	conversationEngine   conversation.ConversationEngine
	imageGenerator       ImageGenerator
}

type ImageGenerator interface {
	GenerateImage(ctx context.Context, prompt string) (ImageGenerationResult, error)
}

type ImageGenerationResult struct {
	PromptID string
	ImageURL string
	Filename string
}

func (r ImageGenerationResult) FormatForUser() string {
	imageURL := strings.TrimSpace(r.ImageURL)
	promptID := strings.TrimSpace(r.PromptID)
	switch {
	case imageURL != "" && promptID != "":
		return "RenCrow_Image generation completed.\n\nprompt_id: " + promptID + "\nimage_url: " + imageURL + "\n\n![generated image](" + imageURL + ")"
	case imageURL != "":
		return "RenCrow_Image generation completed.\n\nimage_url: " + imageURL + "\n\n![generated image](" + imageURL + ")"
	default:
		return "RenCrow_Image generation completed."
	}
}

func NewWildAgent(llmProvider llm.LLMProvider, systemPrompt string) *WildAgent {
	if strings.TrimSpace(systemPrompt) == "" {
		systemPrompt = defaultWildSystemPrompt
	}
	return &WildAgent{llmProvider: llmProvider, systemPrompt: systemPrompt}
}

func (w *WildAgent) WithConversationEngine(engine conversation.ConversationEngine) *WildAgent {
	w.conversationEngine = engine
	return w
}

func (w *WildAgent) WithStableRuntimeContext(content string) *WildAgent {
	w.stableRuntimeContext = strings.TrimSpace(content)
	return w
}

func (w *WildAgent) WithImageGenerator(generator ImageGenerator) *WildAgent {
	w.imageGenerator = generator
	return w
}

func (w *WildAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	userMessage := stripWildCommand(t.UserMessage())
	if isImageGenerationRequest(userMessage) {
		if w.imageGenerator == nil {
			return "", ErrImageGeneratorUnavailable
		}
		result, err := w.imageGenerator.GenerateImage(ctx, userMessage)
		if err != nil {
			return "", err
		}
		response := result.FormatForUser()
		if w.conversationEngine != nil {
			if commitErr := commitConversationTurn(ctx, w.conversationEngine, t.JobID().String(), t.SessionID(), userMessage, response, conversation.SpeakerMidori, nil); commitErr != nil {
				return response, commitErr
			}
		}
		return response, nil
	}
	messages := []llm.Message{}
	var recallPack *conversation.RecallPack
	if w.conversationEngine != nil {
		pack, err := w.conversationEngine.BeginTurn(ctx, t.SessionID(), userMessage)
		if err != nil {
			log.Printf("[Wild] BeginTurn failed: %v", err)
		} else if pack != nil {
			filtered := pack.FilterForRole("wild").WithoutPersonaSystemPrompt()
			recallPack = &filtered
			messages = appendSharedConversationContinuityPrompt(messages, &filtered)
			messages = append(messages, filtered.ToPromptMessages()...)
		}
	}
	if response, ok := exactSharedRecallAnswer(userMessage, recallPack); ok {
		if onToken := llm.StreamCallbackFromContext(ctx); onToken != nil {
			onToken(response)
		}
		if w.conversationEngine != nil {
			if err := commitConversationTurn(ctx, w.conversationEngine, t.JobID().String(), t.SessionID(), userMessage, response, conversation.SpeakerMidori, recallPack); err != nil {
				return response, err
			}
		}
		return response, nil
	}
	messages = assemblePromptContext(w.systemPrompt, w.stableRuntimeContext, messages, userMessageWithAttachments(userMessage, t.Attachments()))
	onToken := llm.StreamCallbackFromContext(ctx)
	if onToken == nil {
		onToken = func(string) {}
	}
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		Messages:    messages,
		MaxTokens:   2048,
		Temperature: 0.8,
		OnToken:     onToken,
	})
	resp, err := w.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	response := strings.TrimSpace(resp.Content)
	response = enforceExactSharedRecallAnswer(userMessage, response, recallPack)
	if w.conversationEngine != nil {
		if err := commitConversationTurn(ctx, w.conversationEngine, t.JobID().String(), t.SessionID(), userMessage, response, conversation.SpeakerMidori, recallPack); err != nil {
			return response, err
		}
	}
	return response, nil
}

func isImageGenerationRequest(message string) bool {
	msg := strings.ToLower(strings.TrimSpace(message))
	if msg == "" {
		return false
	}
	if strings.Contains(msg, "画像プロンプト") || strings.Contains(msg, "prompt only") || strings.Contains(msg, "プロンプトを作") {
		return false
	}
	generationKeywords := []string{
		"画像生成",
		"画像を生成",
		"絵を生成",
		"生成して",
		"描いて",
		"描画して",
		"イラストにして",
		"generate image",
		"text-to-image",
	}
	hasImageContext := strings.Contains(msg, "画像") ||
		strings.Contains(msg, "絵") ||
		strings.Contains(msg, "背景画") ||
		strings.Contains(msg, "イラスト") ||
		strings.Contains(msg, "image") ||
		strings.Contains(msg, "rencrow_image") ||
		strings.Contains(msg, "rencrowimage")
	for _, keyword := range generationKeywords {
		if strings.Contains(msg, keyword) && hasImageContext {
			return true
		}
	}
	return false
}

func stripWildCommand(message string) string {
	trimmed := strings.TrimSpace(message)
	if strings.HasPrefix(trimmed, "/wild") {
		return strings.TrimSpace(strings.TrimPrefix(trimmed, "/wild"))
	}
	return trimmed
}
