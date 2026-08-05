package main

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
)

type wildImageGenerationGateway interface {
	Generate(context.Context, domainimage.GenerateRequest) (domainimage.GenerateResult, error)
}

type wildImageGenerator struct {
	gateway wildImageGenerationGateway
}

func newWildImageGenerator(gateway wildImageGenerationGateway) agent.ImageGenerator {
	if gateway == nil {
		return nil
	}
	return &wildImageGenerator{gateway: gateway}
}

func (g *wildImageGenerator) GenerateImage(ctx context.Context, prompt string) (agent.ImageGenerationResult, error) {
	seed := int64(-1)
	result, err := g.gateway.Generate(ctx, domainimage.GenerateRequest{
		Prompt: strings.TrimSpace(prompt),
		Seed:   &seed,
	})
	if err != nil {
		return agent.ImageGenerationResult{}, err
	}
	imageID := strings.TrimSpace(result.Image.ID)
	if imageID == "" {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image returned an empty image id")
	}
	return agent.ImageGenerationResult{
		PromptID: strings.TrimSpace(result.ID),
		ImageURL: "/viewer/image/result?id=" + url.QueryEscape(imageID),
		Filename: imageID + ".png",
	}, nil
}

func buildWildAgent(
	provider llm.LLMProvider,
	systemPrompt string,
	conversationEngine conversation.ConversationEngine,
	imageGenerator agent.ImageGenerator,
) *agent.WildAgent {
	wild := agent.NewWildAgent(provider, systemPrompt)
	if conversationEngine != nil {
		wild.WithConversationEngine(conversationEngine)
	}
	if imageGenerator != nil {
		wild.WithImageGenerator(imageGenerator)
	}
	return wild
}
