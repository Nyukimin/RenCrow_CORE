package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image/png"
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

type wildImageOptionsGateway interface {
	Options(context.Context) (domainimage.OptionsResponse, error)
}

type wildImageResultGateway interface {
	Image(context.Context, string) ([]byte, string, error)
}

const (
	wildImageMaxResultBytes = 50 << 20
	wildImageMaxSide        = 8192
	wildImageMaxPixels      = 16_777_216
)

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
	return g.generate(ctx, prompt, nil, nil)
}

func (g *wildImageGenerator) GenerateImageWithEngine(ctx context.Context, prompt string, engine domainimage.EngineConstraint) (agent.ImageGenerationResult, error) {
	if engine == domainimage.EngineConstraintImageGen {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "ImageGen is not available in the normal RenCrow_Image route")
	}
	if engine != domainimage.EngineConstraintComfyUI && engine != domainimage.EngineConstraintForgeNeo {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "unsupported engine constraint")
	}
	resultGateway, ok := g.gateway.(wildImageResultGateway)
	if !ok {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "RenCrow_Image result fetch is unavailable")
	}
	optionsGateway, ok := g.gateway.(wildImageOptionsGateway)
	if !ok {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "RenCrow_Image options are unavailable")
	}
	options, err := optionsGateway.Options(ctx)
	if err != nil {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "RenCrow_Image options lookup failed")
	}
	if !options.OK || !domainimage.ValidProfileID(options.DefaultModel) {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "RenCrow_Image returned no valid default profile")
	}
	wantEngine := string(engine)
	defaultProfileCount := 0
	defaultProfileEngine := ""
	for _, model := range options.Models {
		if model.ID == options.DefaultModel {
			defaultProfileCount++
			defaultProfileEngine = model.Engine
		}
	}
	if defaultProfileCount != 1 || defaultProfileEngine != wantEngine {
		return agent.ImageGenerationResult{}, imageEngineUnavailable(engine, "RenCrow_Image options do not advertise the default profile with the requested engine")
	}
	model := options.DefaultModel
	return g.generate(ctx, prompt, &model, resultGateway)
}

func (g *wildImageGenerator) generate(ctx context.Context, prompt string, model *string, resultGateway wildImageResultGateway) (agent.ImageGenerationResult, error) {
	seed := int64(-1)
	request := domainimage.GenerateRequest{
		Prompt: strings.TrimSpace(prompt),
		Seed:   &seed,
	}
	if model != nil {
		request.Model = model
	}
	result, err := g.gateway.Generate(ctx, request)
	if err != nil {
		return agent.ImageGenerationResult{}, err
	}
	if resultGateway != nil {
		if model == nil {
			return agent.ImageGenerationResult{}, fmt.Errorf("explicit image engine is missing its selected profile")
		}
		return verifyWildImageResult(ctx, result, *model, resultGateway)
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

func verifyWildImageResult(ctx context.Context, result domainimage.GenerateResult, selectedProfile string, gateway wildImageResultGateway) (agent.ImageGenerationResult, error) {
	if !result.OK {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image returned an unsuccessful generation receipt")
	}
	if selectedProfile == "" || result.Profile != selectedProfile {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image receipt profile %q does not match selected profile %q", result.Profile, selectedProfile)
	}
	imageID := result.Image.ID
	if result.ID == "" || result.ID != strings.TrimSpace(result.ID) || imageID == "" || imageID != result.ID {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image receipt image IDs do not match")
	}
	if result.Image.ContentType != "image/png" {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image receipt content type is not image/png")
	}
	if !validWildImageDimensions(result.Image.Width, result.Image.Height) {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image receipt dimensions exceed the CORE image validation limits")
	}
	if result.Image.SizeBytes <= 0 || result.Image.SizeBytes > wildImageMaxResultBytes {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image receipt size is outside the %d-byte image limit", wildImageMaxResultBytes)
	}
	receiptDigest, err := hex.DecodeString(result.Image.SHA256)
	if err != nil || len(receiptDigest) != sha256.Size || hex.EncodeToString(receiptDigest) != result.Image.SHA256 {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image receipt SHA-256 is invalid")
	}

	payload, contentType, err := gateway.Image(ctx, imageID)
	if err != nil {
		return agent.ImageGenerationResult{}, fmt.Errorf("fetch RenCrow_Image result: %w", err)
	}
	if contentType != "image/png" {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image result content type %q is not image/png", contentType)
	}
	if len(payload) == 0 || len(payload) > wildImageMaxResultBytes {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image result size is outside the %d-byte image limit", wildImageMaxResultBytes)
	}
	if int64(len(payload)) != result.Image.SizeBytes {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image result size does not match its receipt")
	}
	digest := sha256.Sum256(payload)
	if hex.EncodeToString(digest[:]) != result.Image.SHA256 {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image result SHA-256 does not match its receipt")
	}

	config, err := png.DecodeConfig(bytes.NewReader(payload))
	if err != nil {
		return agent.ImageGenerationResult{}, fmt.Errorf("decode RenCrow_Image PNG header: %w", err)
	}
	if !validWildImageDimensions(config.Width, config.Height) {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image PNG dimensions exceed the CORE image validation limits")
	}
	if config.Width != result.Image.Width || config.Height != result.Image.Height {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image PNG dimensions do not match its receipt")
	}
	decoded, err := png.Decode(bytes.NewReader(payload))
	if err != nil {
		return agent.ImageGenerationResult{}, fmt.Errorf("decode RenCrow_Image PNG: %w", err)
	}
	if decoded.Bounds().Dx() != config.Width || decoded.Bounds().Dy() != config.Height {
		return agent.ImageGenerationResult{}, fmt.Errorf("RenCrow_Image decoded dimensions do not match its PNG header")
	}

	return agent.ImageGenerationResult{
		PromptID: result.ID,
		ImageURL: "/viewer/image/result?id=" + url.QueryEscape(imageID),
		Filename: imageID + ".png",
	}, nil
}

func validWildImageDimensions(width, height int) bool {
	return width > 0 && height > 0 && width <= wildImageMaxSide && height <= wildImageMaxSide && int64(width)*int64(height) <= wildImageMaxPixels
}

func imageEngineUnavailable(engine domainimage.EngineConstraint, reason string) error {
	return fmt.Errorf("%w: %s: %s", agent.ErrImageGeneratorUnavailable, engine, reason)
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
