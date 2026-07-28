package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

type fakeImageGateway struct{}

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
