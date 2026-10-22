package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/imagegateway"
)

type fakeImageGateway struct{}

func (fakeImageGateway) Options(context.Context) (domainimage.OptionsResponse, error) {
	return testImageOptions(), nil
}

func (fakeImageGateway) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	return domainimage.GenerateResult{
		OK:      true,
		ID:      "img_1",
		Profile: "forge_zimage_4060",
		Prompt:  request.Prompt,
		Image: domainimage.ImageResult{
			ID:          "img_1",
			Path:        "/v1/images/img_1.png",
			ContentType: "image/png",
			Width:       768,
			Height:      768,
		},
	}, nil
}

func (fakeImageGateway) Image(_ context.Context, id string) ([]byte, string, error) {
	return []byte("png:" + id), "image/png", nil
}

func TestHandleImageGenerateRewritesResultURLToCore(t *testing.T) {
	handler := HandleImageGenerate(fakeImageGateway{})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(
		http.MethodPost,
		"/viewer/image/generate",
		strings.NewReader(`{"prompt":"青い鳥"}`),
	)
	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	image := response["image"].(map[string]any)
	if image["url"] != "/viewer/image/result?id=img_1" {
		t.Fatalf("image url=%v", image["url"])
	}
}

type recordingImageGateway struct {
	request     domainimage.GenerateRequest
	generateHit bool
}

func (g *recordingImageGateway) Options(context.Context) (domainimage.OptionsResponse, error) {
	return testImageOptions(), nil
}

func (g *recordingImageGateway) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.generateHit = true
	g.request = request
	return fakeImageGateway{}.Generate(context.Background(), request)
}

func (g *recordingImageGateway) Image(ctx context.Context, id string) ([]byte, string, error) {
	return fakeImageGateway{}.Image(ctx, id)
}

func testImageOptions() domainimage.OptionsResponse {
	faceDetailer := false
	loraStrength := 0.7
	return domainimage.OptionsResponse{
		OK:           true,
		DefaultModel: "forge_zimage_4060",
		Models: []domainimage.ImageOptionModel{{
			ID:     "forge_zimage_4060",
			Engine: "comfyui",
			Defaults: domainimage.ImageOptionDefaults{
				Width: 768, Height: 768, Steps: 28, CFGScale: 4.5,
				FaceDetailer: &faceDetailer, LoRAStrength: &loraStrength,
			},
			Limits: domainimage.ImageOptionLimits{
				Width:        domainimage.ImageIntegerMultipleLimits{Min: 256, Max: 1536, MultipleOf: 64},
				Height:       domainimage.ImageIntegerMultipleLimits{Min: 256, Max: 1536, MultipleOf: 64},
				Steps:        domainimage.ImageIntegerLimits{Min: 1, Max: 60},
				CFGScale:     domainimage.ImageFloatLimits{Min: 1, Max: 12, Step: 0.5},
				LoRAStrength: &domainimage.ImageFloatLimits{Min: 0, Max: 1, Step: 0.05},
			},
			Features: &domainimage.ImageOptionFeatures{FaceDetailer: true, LoRAStrength: true},
		}},
	}
}

func TestHandleImageGeneratePassesOptionalParameters(t *testing.T) {
	gateway := &recordingImageGateway{}
	handler := HandleImageGenerate(gateway)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/viewer/image/generate", strings.NewReader(
		`{"prompt":"青い鳥","model":"forge_zimage_4060","width":832,"height":896,"steps":32,"cfg_scale":5.5,"face_detailer":false,"lora_strength":0.65}`,
	))
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !gateway.generateHit {
		t.Fatalf("status=%d hit=%t body=%s", rec.Code, gateway.generateHit, rec.Body.String())
	}
	got := gateway.request
	if got.Model == nil || *got.Model != "forge_zimage_4060" || got.Width == nil || *got.Width != 832 ||
		got.Height == nil || *got.Height != 896 || got.Steps == nil || *got.Steps != 32 ||
		got.CFGScale == nil || *got.CFGScale != 5.5 || got.FaceDetailer == nil || *got.FaceDetailer ||
		got.LoRAStrength == nil || *got.LoRAStrength != 0.65 {
		t.Fatalf("optional parameters were not preserved: %+v", got)
	}
}

func TestHandleImageGenerateKeepsOmittedOptionsAbsent(t *testing.T) {
	gateway := &recordingImageGateway{}
	rec := httptest.NewRecorder()
	HandleImageGenerate(gateway).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/image/generate", strings.NewReader(`{"prompt":"bird"}`)))
	if rec.Code != http.StatusOK || !gateway.generateHit {
		t.Fatalf("status=%d hit=%t body=%s", rec.Code, gateway.generateHit, rec.Body.String())
	}
	got := gateway.request
	if got.Model != nil || got.Width != nil || got.Height != nil || got.Steps != nil || got.CFGScale != nil ||
		got.FaceDetailer != nil || got.LoRAStrength != nil {
		t.Fatalf("omitted options were filled in by CORE: %+v", got)
	}
}

func TestHandleImageGenerateRejectsInvalidOptionTypesAndSizes(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{name: "string width", body: `{"prompt":"bird","width":"768"}`},
		{name: "zero width", body: `{"prompt":"bird","width":0}`},
		{name: "negative height", body: `{"prompt":"bird","height":-64}`},
		{name: "string face detailer", body: `{"prompt":"bird","face_detailer":"false"}`},
		{name: "negative steps", body: `{"prompt":"bird","steps":-1}`},
		{name: "negative cfg scale", body: `{"prompt":"bird","cfg_scale":-0.5}`},
		{name: "string lora strength", body: `{"prompt":"bird","lora_strength":"0.5"}`},
		{name: "model path", body: `{"prompt":"bird","model":"../secret"}`},
		{name: "workflow field", body: `{"prompt":"bird","workflow":{}}`},
		{name: "oversized body", body: `{"prompt":"bird","padding":"` + strings.Repeat("x", maxImageGenerateRequestBytes) + `"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &recordingImageGateway{}
			rec := httptest.NewRecorder()
			HandleImageGenerate(gateway).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/image/generate", strings.NewReader(tc.body)))
			if rec.Code != http.StatusBadRequest || gateway.generateHit {
				t.Fatalf("status=%d hit=%t body=%s", rec.Code, gateway.generateHit, rec.Body.String())
			}
		})
	}
}

func TestHandleImageOptionsReturnsRenCrowImageOptions(t *testing.T) {
	rec := httptest.NewRecorder()
	HandleImageOptions(fakeImageGateway{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/viewer/image/options", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var response domainimage.OptionsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if !response.OK || response.DefaultModel != "forge_zimage_4060" {
		t.Fatalf("unexpected response: %+v", response)
	}
}

func TestHandleImageGenerateMapsRenCrowImageInvalidRequest(t *testing.T) {
	gateway := imageGenerationErrorGateway{err: &imagegateway.ServiceError{
		StatusCode: http.StatusBadRequest,
		Code:       "IMAGE_INVALID_REQUEST",
		Message:    "width is outside model limits",
	}}
	rec := httptest.NewRecorder()
	HandleImageGenerate(gateway).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/viewer/image/generate", strings.NewReader(`{"prompt":"bird","width":768}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

type imageGenerationErrorGateway struct {
	err error
}

func (g imageGenerationErrorGateway) Options(context.Context) (domainimage.OptionsResponse, error) {
	return domainimage.OptionsResponse{}, nil
}

func (g imageGenerationErrorGateway) Generate(context.Context, domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	return domainimage.GenerateResult{}, g.err
}

func (imageGenerationErrorGateway) Image(context.Context, string) ([]byte, string, error) {
	return nil, "", nil
}

func TestHandleImageResultStreamsGatewayPNG(t *testing.T) {
	handler := HandleImageResult(fakeImageGateway{})
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest(http.MethodGet, "/viewer/image/result?id=img_1", nil))

	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("status=%d content-type=%q", rec.Code, rec.Header().Get("Content-Type"))
	}
	if rec.Body.String() != "png:img_1" {
		t.Fatalf("body=%q", rec.Body.String())
	}
}
