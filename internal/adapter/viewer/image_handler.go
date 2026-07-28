package viewer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/imagegateway"
)

const maxImageGenerateRequestBytes = 64 << 10

type ImageGateway interface {
	Generate(context.Context, domainimage.GenerateRequest) (domainimage.GenerateResult, error)
	Image(context.Context, string) ([]byte, string, error)
}

func HandleImageGenerate(gateway ImageGateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gateway == nil {
			writeImageError(w, http.StatusServiceUnavailable, "IMAGE_UNAVAILABLE", "RenCrow_Image is disabled")
			return
		}
		var request domainimage.GenerateRequest
		decoder := json.NewDecoder(io.LimitReader(r.Body, maxImageGenerateRequestBytes))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&request); err != nil {
			writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", "invalid image generation request")
			return
		}
		request.Prompt = strings.TrimSpace(request.Prompt)
		request.NegativePrompt = strings.TrimSpace(request.NegativePrompt)
		if request.Prompt == "" {
			writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", "prompt is required")
			return
		}
		result, err := gateway.Generate(r.Context(), request)
		if err != nil {
			status := http.StatusBadGateway
			var serviceError *imagegateway.ServiceError
			if errors.As(err, &serviceError) && serviceError.StatusCode == http.StatusServiceUnavailable {
				status = http.StatusServiceUnavailable
			}
			writeImageError(w, status, "IMAGE_GENERATION_FAILED", err.Error())
			return
		}
		response := struct {
			OK      bool   `json:"ok"`
			ID      string `json:"id"`
			Created int64  `json:"created"`
			Profile string `json:"profile"`
			Prompt  string `json:"prompt"`
			Image   struct {
				ID          string `json:"id"`
				URL         string `json:"url"`
				ContentType string `json:"content_type"`
				Width       int    `json:"width"`
				Height      int    `json:"height"`
			} `json:"image"`
		}{
			OK: result.OK, ID: result.ID, Created: result.Created,
			Profile: result.Profile, Prompt: result.Prompt,
		}
		response.Image.ID = result.Image.ID
		response.Image.URL = "/viewer/image/result?id=" + result.Image.ID
		response.Image.ContentType = result.Image.ContentType
		response.Image.Width = result.Image.Width
		response.Image.Height = result.Image.Height
		writeImageJSON(w, http.StatusOK, response)
	}
}

func HandleImageResult(gateway ImageGateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gateway == nil {
			http.Error(w, "RenCrow_Image is disabled", http.StatusServiceUnavailable)
			return
		}
		id := strings.TrimSpace(r.URL.Query().Get("id"))
		if id == "" {
			http.Error(w, "image id is required", http.StatusBadRequest)
			return
		}
		image, contentType, err := gateway.Image(r.Context(), id)
		if err != nil {
			http.Error(w, "image result unavailable", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Cache-Control", "private, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(image)
	}
}

func writeImageError(w http.ResponseWriter, status int, code, message string) {
	writeImageJSON(w, status, map[string]any{
		"ok": false, "error_code": code, "message": message,
	})
}

func writeImageJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
