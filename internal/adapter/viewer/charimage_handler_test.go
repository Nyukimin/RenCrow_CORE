package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	appcharimage "github.com/Nyukimin/RenCrow_CORE/internal/application/charimage"
	domaincharimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/charimage"
	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

func TestCharImageCharactersAndComposeHandlersProjectCanonicalCatalog(t *testing.T) {
	catalog, err := domaincharimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	service := appcharimage.NewService(catalog, &charImageTestGateway{}, &charImageTestStore{})

	charactersResponse := httptest.NewRecorder()
	HandleCharImageCharacters(service)(charactersResponse, httptest.NewRequest(http.MethodGet, "/viewer/charimage/characters", nil))
	if charactersResponse.Code != http.StatusOK {
		t.Fatalf("characters status=%d body=%s", charactersResponse.Code, charactersResponse.Body.String())
	}
	var characters struct {
		OK         bool   `json:"ok"`
		Model      string `json:"model"`
		Characters []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			Fixed  struct {
				Core   string `json:"character_core_en"`
				TwoD   string `json:"render_2d_en"`
				ThreeD string `json:"render_3d_en"`
			} `json:"fixed"`
		} `json:"characters"`
	}
	if err := json.Unmarshal(charactersResponse.Body.Bytes(), &characters); err != nil {
		t.Fatal(err)
	}
	if !characters.OK || characters.Model != domaincharimage.ModelProfileID || len(characters.Characters) != domaincharimage.CatalogSize {
		t.Fatalf("characters response header/count=%#v", characters)
	}
	statusCounts := map[string]int{}
	for _, character := range characters.Characters {
		statusCounts[character.Status]++
		if character.ID == "" || character.Fixed.Core == "" || character.Fixed.TwoD == "" || character.Fixed.ThreeD == "" {
			t.Fatalf("characters response omitted fixed fields: %#v", character)
		}
	}
	if statusCounts[domaincharimage.StatusExistingConceptUnconfirmed] != 20 || statusCounts[domaincharimage.StatusCandidateUnapproved] != 2 {
		t.Fatalf("status counts=%v", statusCounts)
	}

	input := `{"character_id":"` + characters.Characters[0].ID + `","render_mode":"3d","scene_variant_text":"At a quiet station."}`
	composeResponse := httptest.NewRecorder()
	HandleCharImageCompose(service)(composeResponse, httptest.NewRequest(http.MethodPost, "/viewer/charimage/compose", strings.NewReader(input)))
	if composeResponse.Code != http.StatusOK {
		t.Fatalf("compose status=%d body=%s", composeResponse.Code, composeResponse.Body.String())
	}
	var composed map[string]any
	if err := json.Unmarshal(composeResponse.Body.Bytes(), &composed); err != nil {
		t.Fatal(err)
	}
	if composed["ok"] != true || composed["render_mode"] != "3d" || !strings.Contains(composed["final_prompt"].(string), "At a quiet station.") || strings.Contains(composed["final_prompt"].(string), "prompt_3d_en") {
		t.Fatalf("compose response=%v", composed)
	}
}

func TestCharImageGenerateReturnsOpaqueImageAndPartialHistoryFailure(t *testing.T) {
	catalog, err := domaincharimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, failSave := range []bool{false, true} {
		name := "saved"
		if failSave {
			name = "history failure"
		}
		t.Run(name, func(t *testing.T) {
			gateway := &charImageTestGateway{}
			store := &charImageTestStore{failSave: failSave}
			service := appcharimage.NewService(catalog, gateway, store)
			input := `{"character_id":"` + catalog.Characters[0].ID + `","render_mode":"2d","scene_variant_text":"  A paper lantern.  ","seed":42,"width":768}`
			response := httptest.NewRecorder()
			HandleCharImageGenerate(service)(response, httptest.NewRequest(http.MethodPost, "/viewer/charimage/generate", strings.NewReader(input)))
			if response.Header().Get("Cache-Control") != "no-store" {
				t.Fatalf("Cache-Control = %q, want no-store", response.Header().Get("Cache-Control"))
			}
			if gateway.generateCalls != 1 || gateway.lastRequest.NegativePrompt != "" || *gateway.lastRequest.Model != domaincharimage.ModelProfileID {
				t.Fatalf("Image Generate calls/request=%d/%+v", gateway.generateCalls, gateway.lastRequest)
			}
			var body map[string]any
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			image, ok := body["image"].(map[string]any)
			if !ok || image["id"] != "img_charimage_1" || image["url"] != "/viewer/image/result?id=img_charimage_1" {
				t.Fatalf("image response=%v", body)
			}
			if failSave {
				if response.Code != http.StatusServiceUnavailable || body["status"] != "partial" || body["retryable"] != false || body["error_code"] != "CHARIMAGE_HISTORY_SAVE_FAILED" || body["id"] != "img_charimage_1" {
					t.Fatalf("partial response status=%d body=%v", response.Code, body)
				}
				if store.saveCalls != 1 {
					t.Fatalf("history save calls=%d, want 1 and no resubmission", store.saveCalls)
				}
			} else if response.Code != http.StatusOK || body["ok"] != true || store.saveCalls != 1 {
				t.Fatalf("success response status=%d body=%v saves=%d", response.Code, body, store.saveCalls)
			}
		})
	}
}

func TestCharImageGenerateRejectsNegativePromptAndPreflightsHistory(t *testing.T) {
	catalog, err := domaincharimage.LoadEmbeddedCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name       string
		body       string
		store      *charImageTestStore
		wantStatus int
	}{
		{name: "negative prompt rejected", body: `{"character_id":"01_sera","render_mode":"2d","scene_variant_text":"","negative_prompt":"x"}`, store: &charImageTestStore{}, wantStatus: http.StatusBadRequest},
		{name: "history unavailable before image generation", body: `{"character_id":"01_sera","render_mode":"2d","scene_variant_text":""}`, store: &charImageTestStore{failPing: true}, wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &charImageTestGateway{}
			service := appcharimage.NewService(catalog, gateway, tc.store)
			response := httptest.NewRecorder()
			HandleCharImageGenerate(service)(response, httptest.NewRequest(http.MethodPost, "/viewer/charimage/generate", strings.NewReader(tc.body)))
			if response.Code != tc.wantStatus || gateway.generateCalls != 0 {
				t.Fatalf("status=%d Generate calls=%d body=%s", response.Code, gateway.generateCalls, response.Body.String())
			}
		})
	}
}

type charImageTestGateway struct {
	generateCalls int
	lastRequest   domainimage.GenerateRequest
}

func (g *charImageTestGateway) Options(context.Context) (domainimage.OptionsResponse, error) {
	return domainimage.OptionsResponse{OK: true, Models: []domainimage.ImageOptionModel{{ID: domaincharimage.ModelProfileID}}}, nil
}

func (g *charImageTestGateway) Generate(_ context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	g.generateCalls++
	g.lastRequest = request
	return domainimage.GenerateResult{
		OK: true, ID: "img_charimage_1", Created: 1792735200, Profile: domaincharimage.ModelProfileID,
		Prompt: request.Prompt,
		Image: domainimage.ImageResult{
			ID: "img_charimage_1", Path: "/v1/images/img_charimage_1.png", ContentType: "image/png",
			Width: 768, Height: 768, SHA256: strings.Repeat("a", 64), SizeBytes: 1234,
		},
	}, nil
}

type charImageTestStore struct {
	failPing  bool
	failSave  bool
	saveCalls int
}

func (s *charImageTestStore) Ping(context.Context) error {
	if s.failPing {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *charImageTestStore) Save(context.Context, domaincharimage.HistoryEntry) error {
	s.saveCalls++
	if s.failSave {
		return context.DeadlineExceeded
	}
	return nil
}

func (s *charImageTestStore) List(context.Context, int) ([]domaincharimage.HistoryEntry, error) {
	return nil, nil
}
