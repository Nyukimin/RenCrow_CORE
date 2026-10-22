package imagegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

func TestClientGenerateUsesRenCrowImageContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/images/generations" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		var request domainimage.GenerateRequest
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if request.Prompt != "青い鳥" || request.Seed == nil || *request.Seed != 42 || request.Model == nil || *request.Model != "forge_zimage_4060" ||
			request.Width == nil || *request.Width != 832 || request.Height == nil || *request.Height != 896 || request.Steps == nil || *request.Steps != 32 ||
			request.CFGScale == nil || *request.CFGScale != 5.5 || request.FaceDetailer == nil || *request.FaceDetailer ||
			request.LoRAStrength == nil || *request.LoRAStrength != 0.65 {
			http.Error(w, "wrong payload", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"id":"img_1","profile":"forge_zimage_4060","prompt":"青い鳥","image":{"id":"img_1","path":"/v1/images/img_1.png","content_type":"image/png","width":768,"height":768}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	seed := int64(42)
	model := "forge_zimage_4060"
	width, height, steps := 832, 896, 32
	cfgScale, loraStrength := 5.5, 0.65
	faceDetailer := false
	result, err := client.Generate(context.Background(), domainimage.GenerateRequest{
		Prompt:       "青い鳥",
		Seed:         &seed,
		Model:        &model,
		Width:        &width,
		Height:       &height,
		Steps:        &steps,
		CFGScale:     &cfgScale,
		FaceDetailer: &faceDetailer,
		LoRAStrength: &loraStrength,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !result.OK || result.ID != "img_1" || result.Image.Width != 768 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestClientImageFetchDoesNotAcceptPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/images/img_1.png" {
			http.Error(w, "wrong path", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png"))
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	image, contentType, err := client.Image(context.Background(), "img_1")
	if err != nil {
		t.Fatalf("Image: %v", err)
	}
	if string(image) != "png" || contentType != "image/png" {
		t.Fatalf("unexpected image response: %q %q", image, contentType)
	}
	if _, _, err := client.Image(context.Background(), "../secret"); err == nil {
		t.Fatal("expected opaque image ID validation error")
	}
}

func TestClientOptionsUsesRenCrowImageContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v1/images/options" {
			http.Error(w, "wrong route", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"ok":true,"default_model":"forge_zimage_4060","models":[{"id":"forge_zimage_4060","engine":"comfyui","defaults":{"width":768,"height":768,"steps":28,"cfg_scale":4.5,"face_detailer":false,"lora_strength":0.7},"limits":{"width":{"min":256,"max":1536,"multiple_of":64},"height":{"min":256,"max":1536,"multiple_of":64},"steps":{"min":1,"max":60},"cfg_scale":{"min":1,"max":12,"step":0.5},"lora_strength":{"min":0,"max":1,"step":0.05}},"features":{"face_detailer":true,"lora_strength":true}}]}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.Options(context.Background())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if !options.OK || options.DefaultModel != "forge_zimage_4060" || len(options.Models) != 1 {
		t.Fatalf("unexpected options response: %+v", options)
	}
	model := options.Models[0]
	if model.Defaults.Width != 768 || model.Defaults.FaceDetailer == nil || *model.Defaults.FaceDetailer ||
		model.Limits.Width.MultipleOf != 64 || model.Features == nil || !model.Features.LoRAStrength ||
		model.Defaults.LoRAStrength == nil || *model.Defaults.LoRAStrength != 0.7 {
		t.Fatalf("unexpected model options: %+v", model)
	}
}

func TestClientOptionsAcceptsMOSSFaceDetailerLimits(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true,"default_model":"moss_profile","models":[{"id":"moss_profile","engine":"comfyui","defaults":{"width":768,"height":768,"steps":24,"cfg_scale":4,"face_detailer":false},"limits":{"width":{"min":256,"max":1536,"multiple_of":64},"height":{"min":256,"max":1536,"multiple_of":64},"steps":{"min":1,"max":50},"cfg_scale":{"min":1,"max":12,"step":0.5},"face_detailer":[false,true]},"features":{"face_detailer":true,"lora_strength":false}}]}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.Options(context.Background())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	values := options.Models[0].Limits.FaceDetailer
	if values == nil || len(*values) != 2 || (*values)[0] || !(*values)[1] {
		t.Fatalf("MOSS face_detailer limits = %v, want [false true]", values)
	}
}

func TestClientOptionsAcceptsEmptyModelListWithDefaultModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"ok":true,"default_model":"non_comfy_default","models":[]}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	options, err := client.Options(context.Background())
	if err != nil {
		t.Fatalf("Options: %v", err)
	}
	if !options.OK || options.DefaultModel != "non_comfy_default" || options.Models == nil || len(options.Models) != 0 {
		t.Fatalf("unexpected empty-model options: %+v", options)
	}
}

func TestClientOptionsRejectsMalformedOrMeaninglessResponse(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "unknown field",
			body: `{"ok":true,"default_model":"profile_a","models":[],"backend_url":"http://example.test"}`,
		},
		{
			name: "default model missing from allowlist",
			body: `{"ok":true,"default_model":"profile_a","models":[{"id":"profile_b","engine":"comfyui","defaults":{"width":768,"height":768,"steps":20,"cfg_scale":4},"limits":{"width":{"min":256,"max":1536,"multiple_of":64},"height":{"min":256,"max":1536,"multiple_of":64},"steps":{"min":1,"max":50},"cfg_scale":{"min":1,"max":12,"step":0.5}},"features":{"face_detailer":false,"lora_strength":false}}]}`,
		},
		{
			name: "duplicate face detailer values",
			body: `{"ok":true,"default_model":"moss_profile","models":[{"id":"moss_profile","engine":"comfyui","defaults":{"width":768,"height":768,"steps":24,"cfg_scale":4,"face_detailer":false},"limits":{"width":{"min":256,"max":1536,"multiple_of":64},"height":{"min":256,"max":1536,"multiple_of":64},"steps":{"min":1,"max":50},"cfg_scale":{"min":1,"max":12,"step":0.5},"face_detailer":[true,true]},"features":{"face_detailer":true,"lora_strength":false}}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			client, err := NewClient(server.URL, 2*time.Second)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if _, err := client.Options(context.Background()); err == nil {
				t.Fatal("Options succeeded for invalid response")
			}
		})
	}
}

func TestClientOptionsRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, strings.Repeat("x", maxJSONResponseBytes+1))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, 2*time.Second)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if _, err := client.Options(context.Background()); err == nil {
		t.Fatal("Options succeeded for oversized response")
	}
}
