package viewer

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"
	"unicode/utf8"

	appcharimage "github.com/Nyukimin/RenCrow_CORE/internal/application/charimage"
	domaincharimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/charimage"
)

const maxCharImageRequestBytes = 16 << 10

type charImageImageResponse struct {
	ID          string `json:"id"`
	URL         string `json:"url"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

type charImageHistoryResponseItem struct {
	ID               string                     `json:"id"`
	CreatedAt        string                     `json:"created_at"`
	CharacterID      string                     `json:"character_id"`
	RenderMode       domaincharimage.RenderMode `json:"render_mode"`
	SceneVariantText string                     `json:"scene_variant_text"`
	FinalPrompt      string                     `json:"final_prompt"`
	ImageURL         string                     `json:"image_url"`
}

type charImageComposeRequest struct {
	CharacterID      string                     `json:"character_id"`
	RenderMode       domaincharimage.RenderMode `json:"render_mode"`
	SceneVariantText *string                    `json:"scene_variant_text"`
}

type charImageGenerateRequest struct {
	CharacterID      string                     `json:"character_id"`
	RenderMode       domaincharimage.RenderMode `json:"render_mode"`
	SceneVariantText *string                    `json:"scene_variant_text"`
	Seed             *int64                     `json:"seed,omitempty"`
	Width            *int                       `json:"width,omitempty"`
	Height           *int                       `json:"height,omitempty"`
	Steps            *int                       `json:"steps,omitempty"`
	CFGScale         *float64                   `json:"cfg_scale,omitempty"`
	FaceDetailer     *bool                      `json:"face_detailer,omitempty"`
	LoRAStrength     *float64                   `json:"lora_strength,omitempty"`
}

func HandleCharImageCharacters(service *appcharimage.Service) http.HandlerFunc {
	type characterItem struct {
		ID             string                         `json:"id"`
		NameJA         string                         `json:"name_ja"`
		NameEN         string                         `json:"name_en"`
		Status         string                         `json:"status"`
		TaglineJA      string                         `json:"tagline_ja"`
		SourceNotesJA  string                         `json:"source_notes_ja,omitempty"`
		Fixed          domaincharimage.CharacterFixed `json:"fixed"`
		DefaultSceneEN string                         `json:"default_scene_en"`
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeCharImageError(w, http.StatusMethodNotAllowed, "CHARIMAGE_METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if service == nil {
			writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_UNAVAILABLE", "CharImage is unavailable")
			return
		}
		characters := service.Characters()
		response := struct {
			OK         bool            `json:"ok"`
			Model      string          `json:"model"`
			Characters []characterItem `json:"characters"`
		}{OK: true, Model: service.Model(), Characters: make([]characterItem, 0, len(characters))}
		for _, character := range characters {
			response.Characters = append(response.Characters, characterItem{
				ID: character.ID, NameJA: character.NameJA, NameEN: character.NameEN, Status: character.Status,
				TaglineJA: character.TaglineJA, SourceNotesJA: character.SourceNotesJA,
				Fixed: character.Fixed, DefaultSceneEN: character.DefaultSceneEN,
			})
		}
		writeCharImageJSON(w, http.StatusOK, response)
	}
}

func HandleCharImageCompose(service *appcharimage.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeCharImageError(w, http.StatusMethodNotAllowed, "CHARIMAGE_METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if service == nil {
			writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_UNAVAILABLE", "CharImage is unavailable")
			return
		}
		var request charImageComposeRequest
		if !decodeCharImageRequest(w, r, &request) || request.SceneVariantText == nil {
			writeCharImageError(w, http.StatusBadRequest, "CHARIMAGE_INVALID_REQUEST", "invalid CharImage request")
			return
		}
		input := domaincharimage.ComposeInput{
			CharacterID: request.CharacterID, RenderMode: request.RenderMode, SceneVariantText: *request.SceneVariantText,
		}
		composed, err := service.Compose(input)
		if errors.Is(err, appcharimage.ErrInvalidRequest) {
			writeCharImageError(w, http.StatusBadRequest, "CHARIMAGE_INVALID_REQUEST", "invalid CharImage request")
			return
		}
		if err != nil {
			writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_UNAVAILABLE", "CharImage is unavailable")
			return
		}
		writeCharImageJSON(w, http.StatusOK, struct {
			OK                  bool                       `json:"ok"`
			CharacterID         string                     `json:"character_id"`
			RenderMode          domaincharimage.RenderMode `json:"render_mode"`
			CharacterCorePrompt string                     `json:"character_core_prompt"`
			RenderProfilePrompt string                     `json:"render_profile_prompt"`
			SceneVariantPrompt  string                     `json:"scene_variant_prompt"`
			FinalPrompt         string                     `json:"final_prompt"`
		}{
			OK: true, CharacterID: input.CharacterID, RenderMode: input.RenderMode,
			CharacterCorePrompt: composed.CharacterCorePrompt, RenderProfilePrompt: composed.RenderProfilePrompt,
			SceneVariantPrompt: composed.SceneVariantPrompt, FinalPrompt: composed.FinalPrompt,
		})
	}
}

func HandleCharImageGenerate(service *appcharimage.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeCharImageError(w, http.StatusMethodNotAllowed, "CHARIMAGE_METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if service == nil {
			writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_UNAVAILABLE", "CharImage is unavailable")
			return
		}
		var request charImageGenerateRequest
		if !decodeCharImageRequest(w, r, &request) || request.SceneVariantText == nil {
			writeCharImageError(w, http.StatusBadRequest, "CHARIMAGE_INVALID_REQUEST", "invalid CharImage request")
			return
		}
		outcome, err := service.Generate(r.Context(), appcharimage.GenerateInput{
			ComposeInput: domaincharimage.ComposeInput{
				CharacterID: request.CharacterID, RenderMode: request.RenderMode, SceneVariantText: *request.SceneVariantText,
			},
			Seed: request.Seed, Width: request.Width, Height: request.Height, Steps: request.Steps,
			CFGScale: request.CFGScale, FaceDetailer: request.FaceDetailer, LoRAStrength: request.LoRAStrength,
		})
		if err != nil && !outcome.Partial {
			writeCharImageServiceError(w, err)
			return
		}
		imageURL := "/viewer/image/result?id=" + url.QueryEscape(outcome.Image.Image.ID)
		image := charImageImageResponse{
			ID: outcome.Image.Image.ID, URL: imageURL, ContentType: outcome.Image.Image.ContentType,
			Width: outcome.Image.Image.Width, Height: outcome.Image.Image.Height,
		}
		if outcome.Partial {
			code := "CHARIMAGE_GENERATION_RESULT_AMBIGUOUS"
			message := "image generation returned an incomplete result; do not resubmit automatically"
			if errors.Is(err, appcharimage.ErrHistorySaveFailed) {
				code = "CHARIMAGE_HISTORY_SAVE_FAILED"
				message = "image generated, but history could not be saved; do not resubmit automatically"
			}
			writeCharImageJSON(w, http.StatusServiceUnavailable, struct {
				OK               bool                       `json:"ok"`
				Status           string                     `json:"status"`
				Retryable        bool                       `json:"retryable"`
				ErrorCode        string                     `json:"error_code"`
				Message          string                     `json:"message"`
				ID               string                     `json:"id"`
				Created          int64                      `json:"created"`
				Profile          string                     `json:"profile"`
				Prompt           string                     `json:"prompt"`
				CharacterID      string                     `json:"character_id"`
				RenderMode       domaincharimage.RenderMode `json:"render_mode"`
				SceneVariantText string                     `json:"scene_variant_text"`
				Image            charImageImageResponse     `json:"image"`
			}{
				OK: false, Status: "partial", Retryable: false, ErrorCode: code,
				Message: message,
				ID:      outcome.Image.ID, Created: outcome.Image.Created, Profile: outcome.Image.Profile, Prompt: outcome.Image.Prompt,
				CharacterID: outcome.CharacterID, RenderMode: outcome.RenderMode, SceneVariantText: outcome.SceneVariantText, Image: image,
			})
			return
		}
		writeCharImageJSON(w, http.StatusOK, struct {
			OK               bool                       `json:"ok"`
			ID               string                     `json:"id"`
			Created          int64                      `json:"created"`
			Profile          string                     `json:"profile"`
			Prompt           string                     `json:"prompt"`
			CharacterID      string                     `json:"character_id"`
			RenderMode       domaincharimage.RenderMode `json:"render_mode"`
			SceneVariantText string                     `json:"scene_variant_text"`
			Image            charImageImageResponse     `json:"image"`
		}{
			OK: true, ID: outcome.Image.ID, Created: outcome.Image.Created, Profile: outcome.Image.Profile,
			Prompt: outcome.Image.Prompt, CharacterID: outcome.CharacterID, RenderMode: outcome.RenderMode,
			SceneVariantText: outcome.SceneVariantText, Image: image,
		})
	}
}

func HandleCharImageHistory(service *appcharimage.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeCharImageError(w, http.StatusMethodNotAllowed, "CHARIMAGE_METHOD_NOT_ALLOWED", "method not allowed")
			return
		}
		if service == nil {
			writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_UNAVAILABLE", "CharImage is unavailable")
			return
		}
		items, err := service.History(r.Context())
		if err != nil {
			writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_HISTORY_UNAVAILABLE", "CharImage history is unavailable")
			return
		}
		responseItems := make([]charImageHistoryResponseItem, 0, len(items))
		for _, item := range items {
			responseItems = append(responseItems, charImageHistoryResponseItem{
				ID: item.ID, CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339Nano),
				CharacterID: item.CharacterID, RenderMode: item.RenderMode,
				SceneVariantText: item.SceneVariantText, FinalPrompt: item.FinalPrompt,
				ImageURL: "/viewer/image/result?id=" + url.QueryEscape(item.ImageID),
			})
		}
		writeCharImageJSON(w, http.StatusOK, struct {
			OK    bool                           `json:"ok"`
			Items []charImageHistoryResponseItem `json:"items"`
		}{OK: true, Items: responseItems})
	}
}

func decodeCharImageRequest(w http.ResponseWriter, r *http.Request, target any) bool {
	if r == nil || r.Body == nil || r.ContentLength > maxCharImageRequestBytes {
		return false
	}
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCharImageRequestBytes))
	if err != nil || !utf8.Valid(payload) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return false
	}
	return decoder.Decode(&struct{}{}) == io.EOF
}

func writeCharImageServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, appcharimage.ErrInvalidRequest):
		writeCharImageError(w, http.StatusBadRequest, "CHARIMAGE_INVALID_REQUEST", "invalid CharImage request")
	case errors.Is(err, appcharimage.ErrUnavailable):
		writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_UNAVAILABLE", "CharImage is unavailable")
	case errors.Is(err, appcharimage.ErrProfileUnavailable):
		writeCharImageError(w, http.StatusServiceUnavailable, "CHARIMAGE_PROFILE_UNAVAILABLE", "requested CharImage profile is unavailable")
	default:
		writeCharImageError(w, http.StatusBadGateway, "CHARIMAGE_GENERATION_FAILED", "CharImage generation failed")
	}
}

func writeCharImageError(w http.ResponseWriter, status int, code, message string) {
	writeCharImageJSON(w, status, struct {
		OK        bool   `json:"ok"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}{OK: false, ErrorCode: code, Message: message})
}

func writeCharImageJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
