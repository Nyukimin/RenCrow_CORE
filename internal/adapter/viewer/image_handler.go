package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"unicode/utf8"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/imagegateway"
)

const maxImageGenerateRequestBytes = 64 << 10
const (
	maxImageEditImageBytes  = domainimage.MaxImageEditImageBytes
	maxImageEditPromptBytes = domainimage.MaxImageEditPromptBytes
	maxImageEditBodyBytes   = domainimage.MaxImageEditBodyBytes
)

type ImageGateway interface {
	Options(context.Context) (domainimage.OptionsResponse, error)
	Generate(context.Context, domainimage.GenerateRequest) (domainimage.GenerateResult, error)
	Image(context.Context, string) ([]byte, string, error)
}

type ImageEditGateway interface {
	Edit(context.Context, domainimage.EditRequest) (domainimage.GenerateResult, error)
}

type ImageEditOptionsGateway interface {
	EditOptions(context.Context) (domainimage.EditOptionsResponse, error)
}

func HandleImageEditOptions(gateway ImageEditOptionsGateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gateway == nil {
			writeImageError(w, http.StatusServiceUnavailable, "IMAGE_UNAVAILABLE", "RenCrow_Image is disabled")
			return
		}
		options, err := gateway.EditOptions(r.Context())
		if err != nil {
			status := http.StatusBadGateway
			var serviceError *imagegateway.ServiceError
			if errors.As(err, &serviceError) && serviceError.StatusCode == http.StatusServiceUnavailable {
				status = http.StatusServiceUnavailable
			}
			writeImageError(w, status, "IMAGE_EDIT_OPTIONS_UNAVAILABLE", "image edit options are unavailable")
			return
		}
		writeImageJSON(w, http.StatusOK, options)
	}
}

func HandleImageEdit(gateway ImageEditGateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gateway == nil {
			writeImageError(w, http.StatusServiceUnavailable, "IMAGE_UNAVAILABLE", "RenCrow_Image is disabled")
			return
		}
		request, ok := decodeImageEditRequest(w, r)
		if !ok {
			writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", "invalid image edit request")
			return
		}
		result, err := gateway.Edit(r.Context(), request)
		if err != nil {
			status := http.StatusBadGateway
			var serviceError *imagegateway.ServiceError
			if errors.As(err, &serviceError) {
				switch serviceError.StatusCode {
				case http.StatusBadRequest:
					writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", firstImageMessage(serviceError.Message, "invalid image edit request"))
					return
				case http.StatusServiceUnavailable:
					status = http.StatusServiceUnavailable
				}
			}
			writeImageError(w, status, "IMAGE_EDIT_FAILED", "image edit failed")
			return
		}
		writeImageGenerateResult(w, result)
	}
}

func decodeImageEditRequest(w http.ResponseWriter, r *http.Request) (domainimage.EditRequest, bool) {
	if r == nil || len(r.Header.Values("Content-Type")) != 1 || r.ContentLength > maxImageEditBodyBytes {
		return domainimage.EditRequest{}, false
	}
	mediaType, parameters, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/form-data") || len(parameters) != 1 || parameters["boundary"] == "" {
		return domainimage.EditRequest{}, false
	}
	limitedBody := http.MaxBytesReader(w, r.Body, maxImageEditBodyBytes)
	reader := multipart.NewReader(limitedBody, parameters["boundary"])
	parts := make(map[string][]byte, 5)
	fileTypes := make(map[string]string, 2)
	for {
		part, err := reader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil || !validImageEditPartHeaders(part.Header) {
			return domainimage.EditRequest{}, false
		}
		name, filename, ok := parseImageEditDisposition(part.Header.Get("Content-Disposition"))
		if !ok {
			return domainimage.EditRequest{}, false
		}
		if _, duplicate := parts[name]; duplicate {
			return domainimage.EditRequest{}, false
		}
		limit, isFile := imageEditPartLimit(name)
		if limit == 0 || isFile != (filename != "") || (isFile && !validImageEditContentType(part.Header)) || (!isFile && !validImageEditTextType(part.Header)) {
			return domainimage.EditRequest{}, false
		}
		value, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil || int64(len(value)) > limit {
			return domainimage.EditRequest{}, false
		}
		if isFile {
			mediaType, _, _ := mime.ParseMediaType(part.Header.Get("Content-Type"))
			fileTypes[name] = strings.ToLower(mediaType)
		}
		parts[name] = value
		if err := part.Close(); err != nil {
			return domainimage.EditRequest{}, false
		}
	}
	if _, err := io.Copy(io.Discard, limitedBody); err != nil {
		return domainimage.EditRequest{}, false
	}
	for _, required := range []string{"operation", "person", "reference", "seed"} {
		if _, found := parts[required]; !found {
			return domainimage.EditRequest{}, false
		}
	}
	operation := string(parts["operation"])
	if operation != "pose" && operation != "clothing" {
		return domainimage.EditRequest{}, false
	}
	seed, ok := parseImageEditSeed(string(parts["seed"]))
	if !ok {
		return domainimage.EditRequest{}, false
	}
	prompt := parts["prompt"]
	_, promptProvided := parts["prompt"]
	if len(prompt) > 0 && !utf8.Valid(prompt) {
		return domainimage.EditRequest{}, false
	}
	if !promptProvided || strings.TrimSpace(string(prompt)) == "" {
		return domainimage.EditRequest{}, false
	}
	personType, personOK := validateImageEditImage(parts["person"], fileTypes["person"])
	referenceType, referenceOK := validateImageEditImage(parts["reference"], fileTypes["reference"])
	if !personOK || !referenceOK {
		return domainimage.EditRequest{}, false
	}
	return domainimage.EditRequest{
		Operation: operation, Person: parts["person"], PersonContentType: personType,
		Reference: parts["reference"], ReferenceContentType: referenceType,
		Prompt: string(prompt), PromptProvided: promptProvided, Seed: seed,
	}, true
}

func validImageEditPartHeaders(header textproto.MIMEHeader) bool {
	if len(header) == 0 || len(header.Values("Content-Disposition")) != 1 || len(header.Values("Content-Transfer-Encoding")) != 0 ||
		len(header.Values("Content-Type")) > 1 {
		return false
	}
	for name := range header {
		if name != "Content-Disposition" && name != "Content-Type" {
			return false
		}
	}
	return true
}

func parseImageEditDisposition(value string) (name, filename string, ok bool) {
	disposition, parameters, err := mime.ParseMediaType(value)
	if err != nil || !strings.EqualFold(disposition, "form-data") {
		return "", "", false
	}
	name, hasName := parameters["name"]
	if !hasName || name == "" {
		return "", "", false
	}
	for parameter := range parameters {
		if parameter != "name" && parameter != "filename" {
			return "", "", false
		}
	}
	filename, _ = parameters["filename"]
	return name, filename, true
}

func imageEditPartLimit(name string) (int64, bool) {
	switch name {
	case "operation", "seed":
		return 32, false
	case "prompt":
		return maxImageEditPromptBytes, false
	case "person", "reference":
		return maxImageEditImageBytes, true
	default:
		return 0, false
	}
}

func validImageEditTextType(header textproto.MIMEHeader) bool {
	values := header.Values("Content-Type")
	if len(values) == 0 {
		return true
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, "text/plain") {
		return false
	}
	for key, value := range parameters {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

func validImageEditContentType(header textproto.MIMEHeader) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || len(parameters) != 0 {
		return false
	}
	return strings.EqualFold(mediaType, "image/png") || strings.EqualFold(mediaType, "image/jpeg")
}

func validateImageEditImage(data []byte, declaredType string) (string, bool) {
	if len(data) == 0 || len(data) > maxImageEditImageBytes {
		return "", false
	}
	configuration, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || configuration.Width <= 0 || configuration.Height <= 0 ||
		configuration.Width > domainimage.MaxImageEditSidePixels || configuration.Height > domainimage.MaxImageEditSidePixels ||
		int64(configuration.Width)*int64(configuration.Height) > domainimage.MaxImageEditPixelCount {
		return "", false
	}
	actualType := ""
	switch format {
	case "png":
		actualType = "image/png"
	case "jpeg":
		actualType = "image/jpeg"
	default:
		return "", false
	}
	if !strings.EqualFold(actualType, declaredType) {
		return "", false
	}
	return actualType, true
}

func parseImageEditSeed(value string) (int64, bool) {
	if value == "-1" {
		return -1, true
	}
	if value == "" {
		return 0, false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return 0, false
		}
	}
	seed, err := strconv.ParseInt(value, 10, 64)
	return seed, err == nil
}

func HandleImageOptions(gateway ImageGateway) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if gateway == nil {
			writeImageError(w, http.StatusServiceUnavailable, "IMAGE_UNAVAILABLE", "RenCrow_Image is disabled")
			return
		}
		options, err := gateway.Options(r.Context())
		if err != nil {
			status := http.StatusBadGateway
			var serviceError *imagegateway.ServiceError
			if errors.As(err, &serviceError) && serviceError.StatusCode == http.StatusServiceUnavailable {
				status = http.StatusServiceUnavailable
			}
			writeImageError(w, status, "IMAGE_OPTIONS_UNAVAILABLE", "image options are unavailable")
			return
		}
		writeImageJSON(w, http.StatusOK, options)
	}
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
		request, ok := decodeImageGenerateRequest(r.Body)
		if !ok {
			writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", "invalid image generation request")
			return
		}
		if !validateImageGenerateRequest(&request) {
			writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", "invalid image generation request")
			return
		}
		result, err := gateway.Generate(r.Context(), request)
		if err != nil {
			status := http.StatusBadGateway
			var serviceError *imagegateway.ServiceError
			if errors.As(err, &serviceError) {
				switch {
				case serviceError.StatusCode == http.StatusServiceUnavailable:
					status = http.StatusServiceUnavailable
				case serviceError.StatusCode == http.StatusBadRequest && serviceError.Code == "IMAGE_INVALID_REQUEST":
					writeImageError(w, http.StatusBadRequest, "IMAGE_INVALID_REQUEST", firstImageMessage(serviceError.Message, "invalid image generation request"))
					return
				}
			}
			writeImageError(w, status, "IMAGE_GENERATION_FAILED", err.Error())
			return
		}
		writeImageGenerateResult(w, result)
	}
}

func writeImageGenerateResult(w http.ResponseWriter, result domainimage.GenerateResult) {
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

func decodeImageGenerateRequest(body io.Reader) (domainimage.GenerateRequest, bool) {
	payload, err := io.ReadAll(io.LimitReader(body, maxImageGenerateRequestBytes+1))
	if err != nil || len(payload) > maxImageGenerateRequestBytes {
		return domainimage.GenerateRequest{}, false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var request domainimage.GenerateRequest
	if err := decoder.Decode(&request); err != nil {
		return domainimage.GenerateRequest{}, false
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return domainimage.GenerateRequest{}, false
	}
	return request, true
}

func validateImageGenerateRequest(request *domainimage.GenerateRequest) bool {
	if request == nil {
		return false
	}
	request.Prompt = strings.TrimSpace(request.Prompt)
	request.NegativePrompt = strings.TrimSpace(request.NegativePrompt)
	if request.Prompt == "" {
		return false
	}
	if request.Model != nil {
		model := strings.TrimSpace(*request.Model)
		if !domainimage.ValidProfileID(model) {
			return false
		}
		request.Model = &model
	}
	if (request.Width != nil && *request.Width <= 0) || (request.Height != nil && *request.Height <= 0) || (request.Steps != nil && *request.Steps <= 0) {
		return false
	}
	if request.CFGScale != nil && *request.CFGScale < 0 {
		return false
	}
	return true
}

func firstImageMessage(message, fallback string) string {
	if message = strings.TrimSpace(message); message != "" {
		return message
	}
	return fallback
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
