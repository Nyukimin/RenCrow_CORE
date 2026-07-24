package agent

import (
	"context"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

const defaultWildSystemPrompt = `You are Wild, a creative LLM for RenCrow.
Focus on story generation, image search, image generation, image analysis, image prompts, mood, composition, clothing, texture, and visual interpretation.
When image generation is requested, assume RenCrow uses the local ComfyUI API as the generation backend unless the user explicitly says otherwise.
Answer naturally and concretely in the user's language.`

// WildAgent は創作Wild用のLLM呼び出しを担当する。
type WildAgent struct {
	llmProvider        llm.LLMProvider
	systemPrompt       string
	conversationEngine conversation.ConversationEngine
	imageGenerator     ImageGenerator
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
		return "ComfyUI image generated.\n\nprompt_id: " + promptID + "\nimage_url: " + imageURL + "\n\n![generated image](" + imageURL + ")"
	case imageURL != "":
		return "ComfyUI image generated.\n\nimage_url: " + imageURL + "\n\n![generated image](" + imageURL + ")"
	default:
		return "ComfyUI image generation completed."
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

func (w *WildAgent) WithImageGenerator(generator ImageGenerator) *WildAgent {
	w.imageGenerator = generator
	return w
}

func (w *WildAgent) Generate(ctx context.Context, t task.Task) (string, error) {
	userMessage := stripWildCommand(t.UserMessage())
	if w.imageGenerator != nil && isComfyUIImageGenerationRequest(userMessage) {
		result, err := w.imageGenerator.GenerateImage(ctx, userMessage)
		if err != nil {
			return "", err
		}
		return result.FormatForUser(), nil
	}
	messages := []llm.Message{}
	if w.conversationEngine != nil {
		recallPack, err := w.conversationEngine.BeginTurn(ctx, t.ChatID(), userMessage)
		if err != nil {
			log.Printf("[Wild] BeginTurn failed: %v", err)
		} else if recallPack != nil {
			filtered := recallPack.FilterForRole("wild")
			if err := recordRecallTrace(ctx, w.conversationEngine, t.ChatID(), t.JobID().String(), "wild", filtered); err != nil {
				log.Printf("[Wild] RecordRecallTrace failed: %v", err)
			}
			messages = append(messages, filtered.ToPromptMessages()...)
		}
	}
	messages = append(messages, userMessageWithAttachments(userMessage, t.Attachments()))
	req := llm.WithCurrentJSTTimeNow(llm.GenerateRequest{
		SystemPrompt: w.systemPrompt,
		Messages:     messages,
		MaxTokens:    2048,
		Temperature:  0.8,
	})
	resp, err := w.llmProvider.Generate(ctx, req)
	if err != nil {
		return "", err
	}
	response := strings.TrimSpace(resp.Content)
	if w.conversationEngine != nil {
		if err := endConversationTurnAs(ctx, w.conversationEngine, t.ChatID(), userMessage, response, conversation.Speaker("wild")); err != nil {
			log.Printf("[Wild] EndTurn failed: %v", err)
		}
	}
	return response, nil
}

func isComfyUIImageGenerationRequest(message string) bool {
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
		"generate image",
		"text-to-image",
	}
	hasImageContext := strings.Contains(msg, "画像") || strings.Contains(msg, "絵") || strings.Contains(msg, "image") || strings.Contains(msg, "comfyui")
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
