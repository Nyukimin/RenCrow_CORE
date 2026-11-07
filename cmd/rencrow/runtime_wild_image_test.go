package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"image"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/imagegateway"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type recordingWildImageGateway struct {
	request      domainimage.GenerateRequest
	result       domainimage.GenerateResult
	err          error
	calls        int
	options      domainimage.OptionsResponse
	optionsErr   error
	optionsCalls int
	imageBytes   []byte
	imageType    string
	imageErr     error
	imageID      string
	imageCalls   int
}

func (g *recordingWildImageGateway) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.calls++
	g.request = request
	return g.result, g.err
}

func (g *recordingWildImageGateway) Options(context.Context) (domainimage.OptionsResponse, error) {
	g.optionsCalls++
	return g.options, g.optionsErr
}

func (g *recordingWildImageGateway) Image(_ context.Context, id string) ([]byte, string, error) {
	g.imageCalls++
	g.imageID = id
	return g.imageBytes, g.imageType, g.imageErr
}

type recordingWildImageGatewayWithoutFetch struct {
	request      domainimage.GenerateRequest
	result       domainimage.GenerateResult
	calls        int
	options      domainimage.OptionsResponse
	optionsCalls int
}

type ownerWildImageE2EGateway struct {
	client        *imagegateway.Client
	options       domainimage.OptionsResponse
	request       domainimage.GenerateRequest
	receipt       domainimage.GenerateResult
	fetchedSize   int
	fetchedSHA256 string
}

func (g *ownerWildImageE2EGateway) Generate(ctx context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.request = request
	result, err := g.client.Generate(ctx, request)
	if err == nil {
		g.receipt = result
	}
	return result, err
}

func (g *ownerWildImageE2EGateway) Options(ctx context.Context) (domainimage.OptionsResponse, error) {
	options, err := g.client.Options(ctx)
	if err == nil {
		g.options = options
	}
	return options, err
}

func (g *ownerWildImageE2EGateway) Image(ctx context.Context, id string) ([]byte, string, error) {
	payload, contentType, err := g.client.Image(ctx, id)
	if err == nil {
		digest := sha256.Sum256(payload)
		g.fetchedSize = len(payload)
		g.fetchedSHA256 = hex.EncodeToString(digest[:])
	}
	return payload, contentType, err
}

func (g *recordingWildImageGatewayWithoutFetch) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.calls++
	g.request = request
	return g.result, nil
}

func (g *recordingWildImageGatewayWithoutFetch) Options(context.Context) (domainimage.OptionsResponse, error) {
	g.optionsCalls++
	return g.options, nil
}

func wildImagePNG(t *testing.T) []byte {
	t.Helper()
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 2, 3))); err != nil {
		t.Fatalf("png.Encode() error = %v", err)
	}
	return payload.Bytes()
}

func wildImageReceipt(profileID, imageID string, payload []byte, width, height int) domainimage.GenerateResult {
	digest := sha256.Sum256(payload)
	return domainimage.GenerateResult{
		OK: true, ID: imageID, Profile: profileID,
		Image: domainimage.ImageResult{
			ID: imageID, ContentType: "image/png", Width: width, Height: height,
			SHA256: hex.EncodeToString(digest[:]), SizeBytes: int64(len(payload)),
		},
	}
}

type rejectingWildLLMProvider struct{}

func (rejectingWildLLMProvider) Name() string { return "rejecting-wild-test" }

func (rejectingWildLLMProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, errors.New("text LLM must not handle an image generation request")
}

func TestWildImageGeneratorUsesRenCrowImageAndReturnsViewerPNG(t *testing.T) {
	gateway := &recordingWildImageGateway{result: domainimage.GenerateResult{
		OK: true,
		ID: "img_midori_1",
		Image: domainimage.ImageResult{
			ID:          "img_midori_1",
			ContentType: "image/png",
			Width:       768,
			Height:      1024,
		},
	}}
	generator := newWildImageGenerator(gateway)
	if generator == nil {
		t.Fatal("configured RenCrow_Image gateway must create a Wild image generator")
	}

	result, err := generator.GenerateImage(context.Background(), "青空と白い鳥の画像を生成して")
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if gateway.calls != 1 {
		t.Fatalf("RenCrow_Image calls = %d, want 1", gateway.calls)
	}
	if gateway.optionsCalls != 0 {
		t.Fatalf("RenCrow_Image options calls = %d, want 0 for default request", gateway.optionsCalls)
	}
	if gateway.request.Model != nil {
		t.Fatalf("default request model = %v, want omitted", gateway.request.Model)
	}
	if gateway.request.Prompt != "青空と白い鳥の画像を生成して" {
		t.Fatalf("prompt = %q", gateway.request.Prompt)
	}
	if gateway.request.Seed == nil || *gateway.request.Seed != -1 {
		t.Fatalf("seed = %v, want -1", gateway.request.Seed)
	}
	if result.PromptID != "img_midori_1" || result.ImageURL != "/viewer/image/result?id=img_midori_1" {
		t.Fatalf("result = %+v", result)
	}
	if result.Filename != "img_midori_1.png" {
		t.Fatalf("filename = %q", result.Filename)
	}
}

func TestWildImageGeneratorExplicitEngineUsesAdvertisedDefaultProfile(t *testing.T) {
	profileID := "comfy-main"
	imageID := "img_engine"
	payload := wildImagePNG(t)
	gateway := &recordingWildImageGateway{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: profileID,
			Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
		},
		result:     wildImageReceipt(profileID, imageID, payload, 2, 3),
		imageBytes: payload,
		imageType:  "image/png",
	}
	generator := newWildImageGenerator(gateway)
	result, err := generator.(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	}).GenerateImageWithEngine(context.Background(), "ComfyUIで灯台の画像を生成して", domainimage.EngineConstraintComfyUI)
	if err != nil {
		t.Fatalf("GenerateImageWithEngine() error = %v", err)
	}
	if gateway.optionsCalls != 1 || gateway.calls != 1 {
		t.Fatalf("options calls=%d generate calls=%d, want 1 each", gateway.optionsCalls, gateway.calls)
	}
	if gateway.request.Model == nil || *gateway.request.Model != profileID {
		t.Fatalf("model = %v, want advertised default profile %q", gateway.request.Model, profileID)
	}
	if gateway.result.Profile != profileID {
		t.Fatalf("owner receipt profile = %q, want selected profile %q", gateway.result.Profile, profileID)
	}
	if gateway.imageCalls != 1 || gateway.imageID != imageID {
		t.Fatalf("image calls=%d image ID=%q, want one fetch for %q", gateway.imageCalls, gateway.imageID, imageID)
	}
	if result.ImageURL != "/viewer/image/result?id="+imageID {
		t.Fatalf("ImageURL = %q, want verified viewer result link", result.ImageURL)
	}
}

func TestWildImageGeneratorOwnerE2E(t *testing.T) {
	if os.Getenv("RENCROW_WILD_IMAGE_OWNER_E2E") != "1" {
		t.Skip("set RENCROW_WILD_IMAGE_OWNER_E2E=1 to run the single owner-backed WILD image request")
	}
	client, err := imagegateway.NewClient("http://127.0.0.1:8780", 10*time.Minute)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	profileID := "moss-image-sft-8-steps-dmd"
	gateway := &ownerWildImageE2EGateway{client: client}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	result, err := generator.GenerateImageWithEngine(ctx,
		"Create one synthetic square 1024 by 1024 PNG illustration of a solitary lighthouse on a rocky coast at dawn.",
		domainimage.EngineConstraintComfyUI,
	)
	if err != nil {
		t.Fatal("owner-backed WILD image verification failed")
	}
	if !gateway.options.OK || gateway.options.DefaultModel != profileID {
		t.Fatal("owner did not return the expected default profile")
	}
	defaultProfileCount := 0
	for _, model := range gateway.options.Models {
		if model.ID == profileID && model.Engine == domainimage.EngineComfyUI &&
			model.Defaults.Width == 1024 && model.Defaults.Height == 1024 {
			defaultProfileCount++
		}
	}
	if defaultProfileCount != 1 {
		t.Fatalf("owner options advertised %d matching default profiles, want 1", defaultProfileCount)
	}
	if gateway.request.Model == nil || *gateway.request.Model != profileID ||
		gateway.request.Width != nil || gateway.request.Height != nil {
		t.Fatalf("owner request did not pin the expected profile while preserving its 1024x1024 defaults")
	}
	if !gateway.receipt.OK || gateway.receipt.Profile != profileID ||
		gateway.receipt.Image.Width != 1024 || gateway.receipt.Image.Height != 1024 {
		t.Fatalf("owner receipt did not match the selected profile and requested dimensions")
	}
	if gateway.fetchedSize <= 0 || gateway.fetchedSHA256 != gateway.receipt.Image.SHA256 {
		t.Fatalf("fetched image size/hash did not match the successful owner receipt")
	}
	if result.PromptID != gateway.receipt.ID || result.ImageURL != "/viewer/image/result?id="+gateway.receipt.Image.ID {
		t.Fatalf("verified WILD result did not retain the owner image ID: %+v", result)
	}
	t.Logf("verified owner image %s (%dx%d, sha256=%s)", gateway.receipt.Image.ID, gateway.receipt.Image.Width, gateway.receipt.Image.Height, gateway.fetchedSHA256)
}

func TestWildImageGeneratorExplicitEngineRejectsWrongOwnerReceiptProfile(t *testing.T) {
	profileID := "comfy-main"
	payload := wildImagePNG(t)
	gateway := &recordingWildImageGateway{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: profileID,
			Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
		},
		result:     wildImageReceipt("wrong-profile", "img_wrong_profile", payload, 2, 3),
		imageBytes: payload,
		imageType:  "image/png",
	}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
		t.Fatal("GenerateImageWithEngine() succeeded with a different owner receipt profile")
	}
	if gateway.calls != 1 || gateway.imageCalls != 0 {
		t.Fatalf("generate calls=%d image calls=%d, want 1 and 0", gateway.calls, gateway.imageCalls)
	}
}

func TestWildImageGeneratorExplicitEngineRejectsInvalidPNG(t *testing.T) {
	profileID := "comfy-main"
	payload := []byte("not a PNG")
	gateway := &recordingWildImageGateway{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: profileID,
			Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
		},
		result:     wildImageReceipt(profileID, "img_corrupt", payload, 2, 3),
		imageBytes: payload,
		imageType:  "image/png",
	}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
		t.Fatal("GenerateImageWithEngine() succeeded with corrupt PNG bytes")
	}
	if gateway.imageCalls != 1 {
		t.Fatalf("image calls = %d, want 1", gateway.imageCalls)
	}
}

func TestWildImageGeneratorExplicitEngineRejectsReceiptDigestAndDimensionMismatch(t *testing.T) {
	profileID := "comfy-main"
	payload := wildImagePNG(t)
	tests := []struct {
		name   string
		mutate func(*domainimage.GenerateResult)
	}{
		{name: "digest", mutate: func(result *domainimage.GenerateResult) { result.Image.SHA256 = strings.Repeat("0", 64) }},
		{name: "dimensions", mutate: func(result *domainimage.GenerateResult) { result.Image.Width = 3 }},
		{name: "size", mutate: func(result *domainimage.GenerateResult) { result.Image.SizeBytes++ }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageID := "img_mismatch"
			gateway := &recordingWildImageGateway{
				options: domainimage.OptionsResponse{
					OK: true, DefaultModel: profileID,
					Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
				},
				result:     wildImageReceipt(profileID, imageID, payload, 2, 3),
				imageBytes: payload,
				imageType:  "image/png",
			}
			tt.mutate(&gateway.result)
			generator := newWildImageGenerator(gateway).(interface {
				GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
			})
			if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
				t.Fatal("GenerateImageWithEngine() succeeded with mismatched owner receipt metadata")
			}
		})
	}
}

func TestWildImageGeneratorExplicitEngineRejectsImageIDMismatch(t *testing.T) {
	profileID := "comfy-main"
	payload := wildImagePNG(t)
	receipt := wildImageReceipt(profileID, "img_receipt", payload, 2, 3)
	receipt.Image.ID = "img_other"
	gateway := &recordingWildImageGateway{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: profileID,
			Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
		},
		result:     receipt,
		imageBytes: payload,
		imageType:  "image/png",
	}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
		t.Fatal("GenerateImageWithEngine() succeeded with different receipt image IDs")
	}
	if gateway.imageCalls != 0 {
		t.Fatalf("image calls = %d, want 0 after receipt ID mismatch", gateway.imageCalls)
	}
}

func TestWildImageGeneratorExplicitEngineEnforcesImageLimitsBeforeFetch(t *testing.T) {
	profileID := "comfy-main"
	payload := wildImagePNG(t)
	tests := []struct {
		name   string
		mutate func(*domainimage.GenerateResult)
	}{
		{name: "side length", mutate: func(result *domainimage.GenerateResult) { result.Image.Width = wildImageMaxSide + 1 }},
		{name: "pixel count", mutate: func(result *domainimage.GenerateResult) { result.Image.Width, result.Image.Height = 4097, 4097 }},
		{name: "image bytes", mutate: func(result *domainimage.GenerateResult) { result.Image.SizeBytes = wildImageMaxResultBytes + 1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			imageID := "img_limit"
			gateway := &recordingWildImageGateway{
				options: domainimage.OptionsResponse{
					OK: true, DefaultModel: profileID,
					Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
				},
				result:     wildImageReceipt(profileID, imageID, payload, 2, 3),
				imageBytes: payload,
				imageType:  "image/png",
			}
			tt.mutate(&gateway.result)
			generator := newWildImageGenerator(gateway).(interface {
				GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
			})
			if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
				t.Fatal("GenerateImageWithEngine() succeeded beyond CORE image limits")
			}
			if gateway.imageCalls != 0 {
				t.Fatalf("image calls = %d, want 0 after oversized receipt", gateway.imageCalls)
			}
		})
	}
}

func TestWildImageGeneratorExplicitEngineDoesNotRetryAfterFetchFailure(t *testing.T) {
	profileID := "comfy-main"
	payload := wildImagePNG(t)
	gateway := &recordingWildImageGateway{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: profileID,
			Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
		},
		result:   wildImageReceipt(profileID, "img_fetch_error", payload, 2, 3),
		imageErr: errors.New("owner image fetch unavailable"),
	}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
		t.Fatal("GenerateImageWithEngine() succeeded after an image fetch error")
	}
	if gateway.calls != 1 || gateway.imageCalls != 1 {
		t.Fatalf("generate calls=%d image calls=%d, want exactly one of each", gateway.calls, gateway.imageCalls)
	}
}

func TestWildImageGeneratorExplicitEngineRequiresFetchBeforeGeneration(t *testing.T) {
	profileID := "comfy-main"
	gateway := &recordingWildImageGatewayWithoutFetch{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: profileID,
			Models: []domainimage.ImageOptionModel{{ID: profileID, Engine: domainimage.EngineComfyUI}},
		},
	}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	if _, err := generator.GenerateImageWithEngine(context.Background(), "ComfyUIで生成", domainimage.EngineConstraintComfyUI); err == nil {
		t.Fatal("GenerateImageWithEngine() succeeded without an image fetch route")
	}
	if gateway.calls != 0 {
		t.Fatalf("generate calls = %d, want 0 without a fetch route", gateway.calls)
	}
}

func TestWildImageGeneratorExplicitEngineFailsClosedWithoutAdvertisedMatch(t *testing.T) {
	tests := []struct {
		name       string
		options    domainimage.OptionsResponse
		optionsErr error
	}{
		{
			name: "engine mismatch",
			options: domainimage.OptionsResponse{
				OK: true, DefaultModel: "comfy-main",
				Models: []domainimage.ImageOptionModel{{ID: "comfy-main", Engine: domainimage.EngineComfyUI}},
			},
		},
		{
			name:    "non-ComfyUI default is not listed",
			options: domainimage.OptionsResponse{OK: true, DefaultModel: "forge-main", Models: []domainimage.ImageOptionModel{}},
		},
		{
			name: "default ID does not match listed engine",
			options: domainimage.OptionsResponse{
				OK: true, DefaultModel: "forge-main",
				Models: []domainimage.ImageOptionModel{{ID: "other-forge", Engine: domainimage.EngineForgeNeo}},
			},
		},
		{
			name:       "options request failure",
			optionsErr: errors.New("options unavailable"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gateway := &recordingWildImageGateway{options: tt.options, optionsErr: tt.optionsErr}
			generator := newWildImageGenerator(gateway).(interface {
				GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
			})
			_, err := generator.GenerateImageWithEngine(context.Background(), "画像をForgeNeoで生成して", domainimage.EngineConstraintForgeNeo)
			if !errors.Is(err, agent.ErrImageGeneratorUnavailable) {
				t.Fatalf("error = %v, want ErrImageGeneratorUnavailable", err)
			}
			if err == agent.ErrImageGeneratorUnavailable {
				t.Fatal("unavailability error must include the reason")
			}
			if gateway.optionsCalls != 1 || gateway.calls != 0 {
				t.Fatalf("options calls=%d generate calls=%d, want 1 and 0", gateway.optionsCalls, gateway.calls)
			}
		})
	}
}

func TestWildImageGeneratorDoesNotRouteImageGenToRenCrowImage(t *testing.T) {
	gateway := &recordingWildImageGateway{}
	generator := newWildImageGenerator(gateway).(interface {
		GenerateImageWithEngine(context.Context, string, domainimage.EngineConstraint) (agent.ImageGenerationResult, error)
	})
	_, err := generator.GenerateImageWithEngine(context.Background(), "画像をImageGenで", domainimage.EngineConstraintImageGen)
	if !errors.Is(err, agent.ErrImageGeneratorUnavailable) {
		t.Fatalf("error = %v, want ErrImageGeneratorUnavailable", err)
	}
	if gateway.optionsCalls != 0 || gateway.calls != 0 {
		t.Fatalf("options calls=%d generate calls=%d, want both 0", gateway.optionsCalls, gateway.calls)
	}
}

func TestBuildWildAgentInjectsImageGeneratorForMidoriChat(t *testing.T) {
	gateway := &recordingWildImageGateway{result: domainimage.GenerateResult{
		OK: true,
		ID: "img_midori_chat",
		Image: domainimage.ImageResult{
			ID:          "img_midori_chat",
			ContentType: "image/png",
			Width:       768,
			Height:      1024,
		},
	}}
	wild := buildWildAgent(rejectingWildLLMProvider{}, "Wild", nil, newWildImageGenerator(gateway))

	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "海辺の白い灯台の画像を生成して", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	response, err := wild.Generate(context.Background(), input)
	if err != nil {
		t.Fatalf("Midori image generation error = %v", err)
	}
	if gateway.calls != 1 {
		t.Fatalf("RenCrow_Image calls = %d, want 1", gateway.calls)
	}
	want := "![generated image](/viewer/image/result?id=img_midori_chat)"
	if !strings.Contains(response, want) {
		t.Fatalf("response %q does not contain %q", response, want)
	}
}

func TestWildNormalRouteRejectsExplicitEngineWhenAdvertisedDefaultMismatches(t *testing.T) {
	gateway := &recordingWildImageGateway{
		options: domainimage.OptionsResponse{
			OK: true, DefaultModel: "forge-main",
			Models: []domainimage.ImageOptionModel{{ID: "forge-main", Engine: "forge-neo"}},
		},
		result: domainimage.GenerateResult{OK: true, ID: "must-not-generate", Image: domainimage.ImageResult{ID: "must-not-generate"}},
	}
	wild := buildWildAgent(rejectingWildLLMProvider{}, "Wild", nil, newWildImageGenerator(gateway))
	address, err := conversation.NewChannelAddress("viewer", "viewer-user")
	if err != nil {
		t.Fatalf("NewChannelAddress() error = %v", err)
	}
	input, err := conversation.NewTurnInput(modulecore.NewTaskID(), "ComfyUIで灯台の画像を生成して", address)
	if err != nil {
		t.Fatalf("NewTurnInput() error = %v", err)
	}
	_, err = wild.Generate(context.Background(), input)
	if !errors.Is(err, agent.ErrImageGeneratorUnavailable) {
		t.Fatalf("Generate() error = %v, want ErrImageGeneratorUnavailable", err)
	}
	if gateway.calls != 0 {
		t.Fatalf("RenCrow_Image generate calls = %d, want 0 on explicit engine mismatch", gateway.calls)
	}
}

func TestNewWildImageGeneratorRejectsMissingGateway(t *testing.T) {
	if generator := newWildImageGenerator(nil); generator != nil {
		t.Fatalf("generator = %T, want nil", generator)
	}
}

var _ agent.ImageGenerator = (*wildImageGenerator)(nil)
