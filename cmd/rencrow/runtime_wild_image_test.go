package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
)

type recordingWildImageGateway struct {
	request domainimage.GenerateRequest
	result  domainimage.GenerateResult
	err     error
	calls   int
}

func (g *recordingWildImageGateway) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.calls++
	g.request = request
	return g.result, g.err
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

	response, err := wild.Generate(context.Background(), task.NewTask(
		task.NewJobID(),
		"海辺の白い灯台の画像を生成して",
		"viewer",
		"viewer-user",
	))
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

func TestNewWildImageGeneratorRejectsMissingGateway(t *testing.T) {
	if generator := newWildImageGenerator(nil); generator != nil {
		t.Fatalf("generator = %T, want nil", generator)
	}
}

var _ agent.ImageGenerator = (*wildImageGenerator)(nil)
