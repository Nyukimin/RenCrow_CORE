package imagegateway

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

const (
	maxJSONResponseBytes = 1 << 20
	maxImageBytes        = 50 << 20
)

var imageIDPattern = regexp.MustCompile(`^img_[0-9A-Za-z_-]+$`)

type Client struct {
	baseURL    string
	httpClient *http.Client
}

type ServiceError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ServiceError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("image gateway base URL must be an absolute HTTP URL")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("image gateway timeout must be positive")
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

func (c *Client) Generate(ctx context.Context, request domainimage.GenerateRequest) (domainimage.GenerateResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("encode image request: %w", err)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/images/generations", bytes.NewReader(body))
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("create image request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", "application/json")

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("RenCrow_Image request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponseBytes+1))
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("read RenCrow_Image response: %w", err)
	}
	if len(payload) > maxJSONResponseBytes {
		return domainimage.GenerateResult{}, fmt.Errorf("RenCrow_Image response is too large")
	}
	var result domainimage.GenerateResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("decode RenCrow_Image response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK {
		return domainimage.GenerateResult{}, &ServiceError{
			StatusCode: response.StatusCode,
			Code:       firstNonEmpty(result.ErrorCode, "IMAGE_REQUEST_FAILED"),
			Message:    firstNonEmpty(result.Message, http.StatusText(response.StatusCode)),
		}
	}
	if !validResult(result) {
		return domainimage.GenerateResult{}, fmt.Errorf("RenCrow_Image returned an invalid result")
	}
	return result, nil
}

func (c *Client) Edit(ctx context.Context, request domainimage.EditRequest) (domainimage.GenerateResult, error) {
	if err := validateEditRequest(request); err != nil {
		return domainimage.GenerateResult{}, err
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("operation", request.Operation); err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("write image edit operation: %w", err)
	}
	if err := writeImageEditFile(writer, "person", request.PersonContentType, request.Person); err != nil {
		return domainimage.GenerateResult{}, err
	}
	if err := writeImageEditFile(writer, "reference", request.ReferenceContentType, request.Reference); err != nil {
		return domainimage.GenerateResult{}, err
	}
	if request.PromptProvided || request.Prompt != "" {
		if err := writer.WriteField("prompt", request.Prompt); err != nil {
			return domainimage.GenerateResult{}, fmt.Errorf("write image edit prompt: %w", err)
		}
	}
	if err := writer.WriteField("seed", fmt.Sprintf("%d", request.Seed)); err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("write image edit seed: %w", err)
	}
	if err := writer.Close(); err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("close image edit request: %w", err)
	}
	if body.Len() > domainimage.MaxImageEditBodyBytes {
		return domainimage.GenerateResult{}, fmt.Errorf("image edit request exceeds %d bytes", domainimage.MaxImageEditBodyBytes)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/images/edits", &body)
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("create image edit request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	client := *c.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("RenCrow_Image edit request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := "IMAGE_EDIT_FAILED"
		message := http.StatusText(response.StatusCode)
		if response.StatusCode == http.StatusBadRequest {
			code, message = "IMAGE_INVALID_REQUEST", "RenCrow_Image rejected the edit request"
		}
		return domainimage.GenerateResult{}, &ServiceError{StatusCode: response.StatusCode, Code: code, Message: message}
	}
	if !validJSONContentType(response.Header.Get("Content-Type")) {
		return domainimage.GenerateResult{}, fmt.Errorf("RenCrow_Image edit response has unsupported content type")
	}
	payload, err := readImageJSONResponse(response.Body)
	if err != nil {
		return domainimage.GenerateResult{}, err
	}
	result, err := decodeEditResult(payload, request.Operation)
	if err != nil {
		return domainimage.GenerateResult{}, fmt.Errorf("decode RenCrow_Image edit response: %w", err)
	}
	if !validResult(result) {
		return domainimage.GenerateResult{}, fmt.Errorf("RenCrow_Image returned an invalid edit result")
	}
	return result, nil
}

func (c *Client) EditOptions(ctx context.Context) (domainimage.EditOptionsResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/images/edits/options", nil)
	if err != nil {
		return domainimage.EditOptionsResponse{}, fmt.Errorf("create image edit options request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return domainimage.EditOptionsResponse{}, fmt.Errorf("RenCrow_Image edit options request failed: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domainimage.EditOptionsResponse{}, &ServiceError{
			StatusCode: response.StatusCode,
			Code:       "IMAGE_EDIT_OPTIONS_FAILED",
			Message:    http.StatusText(response.StatusCode),
		}
	}
	if !validJSONContentType(response.Header.Get("Content-Type")) {
		return domainimage.EditOptionsResponse{}, fmt.Errorf("RenCrow_Image edit options have unsupported content type")
	}
	payload, err := readImageJSONResponse(response.Body)
	if err != nil {
		return domainimage.EditOptionsResponse{}, err
	}
	options, err := decodeEditOptions(payload)
	if err != nil {
		return domainimage.EditOptionsResponse{}, fmt.Errorf("decode RenCrow_Image edit options: %w", err)
	}
	return options, nil
}

func validateEditRequest(request domainimage.EditRequest) error {
	if request.Operation != "pose" && request.Operation != "clothing" {
		return fmt.Errorf("invalid image edit operation")
	}
	if len(request.Person) == 0 || len(request.Person) > domainimage.MaxImageEditImageBytes ||
		len(request.Reference) == 0 || len(request.Reference) > domainimage.MaxImageEditImageBytes {
		return fmt.Errorf("invalid image edit image size")
	}
	if (request.PersonContentType != "image/png" && request.PersonContentType != "image/jpeg") ||
		(request.ReferenceContentType != "image/png" && request.ReferenceContentType != "image/jpeg") {
		return fmt.Errorf("invalid image edit content type")
	}
	if !utf8.ValidString(request.Prompt) || len(request.Prompt) > domainimage.MaxImageEditPromptBytes || request.Seed < -1 {
		return fmt.Errorf("invalid image edit prompt or seed")
	}
	return nil
}

func writeImageEditFile(writer *multipart.Writer, name, contentType string, data []byte) error {
	extension := ".png"
	if contentType == "image/jpeg" {
		extension = ".jpg"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
		"name": name, "filename": name + extension,
	}))
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return fmt.Errorf("create image edit %s part: %w", name, err)
	}
	if _, err := part.Write(data); err != nil {
		return fmt.Errorf("write image edit %s part: %w", name, err)
	}
	return nil
}

func readImageJSONResponse(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxJSONResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read RenCrow_Image response: %w", err)
	}
	if len(payload) > maxJSONResponseBytes {
		return nil, fmt.Errorf("RenCrow_Image response is too large")
	}
	return payload, nil
}

func validJSONContentType(header string) bool {
	mediaType, parameters, err := mime.ParseMediaType(header)
	if err != nil || !strings.EqualFold(mediaType, "application/json") {
		return false
	}
	for key, value := range parameters {
		if key != "charset" || !strings.EqualFold(value, "utf-8") {
			return false
		}
	}
	return true
}

type editGatewayResponse struct {
	OK        bool                     `json:"ok"`
	ID        string                   `json:"id"`
	Created   int64                    `json:"created"`
	Profile   string                   `json:"profile"`
	Prompt    string                   `json:"prompt"`
	Operation string                   `json:"operation"`
	Image     editGatewayImageResponse `json:"image"`
}

type editGatewayImageResponse struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
}

func decodeEditResult(payload []byte, expectedOperation string) (domainimage.GenerateResult, error) {
	if !utf8.Valid(payload) {
		return domainimage.GenerateResult{}, fmt.Errorf("response is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var response editGatewayResponse
	if err := decoder.Decode(&response); err != nil {
		return domainimage.GenerateResult{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return domainimage.GenerateResult{}, fmt.Errorf("unexpected trailing JSON value")
		}
		return domainimage.GenerateResult{}, fmt.Errorf("trailing response data: %w", err)
	}
	if !validEditGatewayResponse(response, expectedOperation) {
		return domainimage.GenerateResult{}, fmt.Errorf("response does not match the image edit contract")
	}
	return domainimage.GenerateResult{
		OK:      response.OK,
		ID:      response.ID,
		Created: response.Created,
		Profile: response.Profile,
		Prompt:  response.Prompt,
		Image: domainimage.ImageResult{
			ID:          response.Image.ID,
			Path:        response.Image.Path,
			ContentType: response.Image.ContentType,
			Width:       response.Image.Width,
			Height:      response.Image.Height,
		},
	}, nil
}

func validEditGatewayResponse(response editGatewayResponse, expectedOperation string) bool {
	digest, err := hex.DecodeString(response.Image.SHA256)
	return err == nil && len(digest) == 32 && hex.EncodeToString(digest) == response.Image.SHA256 &&
		response.OK && response.Operation == expectedOperation &&
		imageIDPattern.MatchString(response.ID) && response.Image.ID == response.ID &&
		response.Created > 0 && strings.TrimSpace(response.Profile) != "" &&
		response.Prompt != "" && response.Image.Path == "/v1/images/"+response.ID+".png" &&
		response.Image.ContentType == "image/png" && response.Image.Width > 0 && response.Image.Height > 0 &&
		response.Image.SizeBytes > 0
}

func decodeEditOptions(payload []byte) (domainimage.EditOptionsResponse, error) {
	if !utf8.Valid(payload) {
		return domainimage.EditOptionsResponse{}, fmt.Errorf("response is not UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var options domainimage.EditOptionsResponse
	if err := decoder.Decode(&options); err != nil {
		return domainimage.EditOptionsResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return domainimage.EditOptionsResponse{}, fmt.Errorf("unexpected trailing JSON value")
		}
		return domainimage.EditOptionsResponse{}, fmt.Errorf("trailing response data: %w", err)
	}
	if !validEditOptions(options) {
		return domainimage.EditOptionsResponse{}, fmt.Errorf("invalid edit options")
	}
	return options, nil
}

func validEditOptions(options domainimage.EditOptionsResponse) bool {
	if !options.OK || len(options.Operations) == 0 || len(options.Operations) > 2 {
		return false
	}
	seen := make(map[string]struct{}, len(options.Operations))
	for _, operation := range options.Operations {
		if operation != "pose" && operation != "clothing" {
			return false
		}
		if _, found := seen[operation]; found {
			return false
		}
		seen[operation] = struct{}{}
	}
	return true
}

func (c *Client) Options(ctx context.Context) (domainimage.OptionsResponse, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/images/options", nil)
	if err != nil {
		return domainimage.OptionsResponse{}, fmt.Errorf("create image options request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return domainimage.OptionsResponse{}, fmt.Errorf("RenCrow_Image options request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxJSONResponseBytes+1))
	if err != nil {
		return domainimage.OptionsResponse{}, fmt.Errorf("read RenCrow_Image options: %w", err)
	}
	if len(payload) > maxJSONResponseBytes {
		return domainimage.OptionsResponse{}, fmt.Errorf("RenCrow_Image options response is too large")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return domainimage.OptionsResponse{}, &ServiceError{
			StatusCode: response.StatusCode,
			Code:       "IMAGE_OPTIONS_FAILED",
			Message:    http.StatusText(response.StatusCode),
		}
	}
	options, err := decodeOptionsResponse(payload)
	if err != nil {
		return domainimage.OptionsResponse{}, fmt.Errorf("decode RenCrow_Image options: %w", err)
	}
	return options, nil
}

func (c *Client) Image(ctx context.Context, id string) ([]byte, string, error) {
	id = strings.TrimSpace(id)
	if !imageIDPattern.MatchString(id) {
		return nil, "", fmt.Errorf("invalid image id")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/images/"+url.PathEscape(id)+".png", nil)
	if err != nil {
		return nil, "", fmt.Errorf("create image fetch request: %w", err)
	}
	request.Header.Set("Accept", "image/png")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, "", fmt.Errorf("fetch RenCrow_Image result: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, "", &ServiceError{
			StatusCode: response.StatusCode,
			Code:       "IMAGE_RESULT_FETCH_FAILED",
			Message:    http.StatusText(response.StatusCode),
		}
	}
	contentType := strings.TrimSpace(strings.Split(response.Header.Get("Content-Type"), ";")[0])
	if contentType != "image/png" {
		return nil, "", fmt.Errorf("RenCrow_Image returned unsupported content type %q", contentType)
	}
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxImageBytes+1))
	if err != nil {
		return nil, "", fmt.Errorf("read RenCrow_Image result: %w", err)
	}
	if len(payload) > maxImageBytes {
		return nil, "", fmt.Errorf("RenCrow_Image result exceeds %d bytes", maxImageBytes)
	}
	return payload, contentType, nil
}

func validResult(result domainimage.GenerateResult) bool {
	return imageIDPattern.MatchString(result.ID) &&
		result.Image.ID == result.ID &&
		result.Image.ContentType == "image/png" &&
		result.Image.Width > 0 &&
		result.Image.Height > 0
}

func decodeOptionsResponse(payload []byte) (domainimage.OptionsResponse, error) {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	var options domainimage.OptionsResponse
	if err := decoder.Decode(&options); err != nil {
		return domainimage.OptionsResponse{}, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return domainimage.OptionsResponse{}, fmt.Errorf("unexpected trailing JSON value")
		}
		return domainimage.OptionsResponse{}, fmt.Errorf("trailing response data: %w", err)
	}
	if !validOptionsResponse(options) {
		return domainimage.OptionsResponse{}, fmt.Errorf("RenCrow_Image returned invalid options")
	}
	return options, nil
}

func validOptionsResponse(options domainimage.OptionsResponse) bool {
	if !options.OK || !domainimage.ValidProfileID(options.DefaultModel) || options.Models == nil || len(options.Models) > 256 {
		return false
	}
	seen := make(map[string]struct{}, len(options.Models))
	defaultFound := false
	for _, model := range options.Models {
		if !domainimage.ValidProfileID(model.ID) || strings.TrimSpace(model.Engine) == "" || model.Engine != strings.TrimSpace(model.Engine) || model.Features == nil {
			return false
		}
		if _, found := seen[model.ID]; found {
			return false
		}
		seen[model.ID] = struct{}{}
		if model.ID == options.DefaultModel {
			defaultFound = true
		}
		if !validDefaultInteger(model.Defaults.Width, model.Limits.Width.Min, model.Limits.Width.Max, model.Limits.Width.MultipleOf) ||
			!validDefaultInteger(model.Defaults.Height, model.Limits.Height.Min, model.Limits.Height.Max, model.Limits.Height.MultipleOf) ||
			!validDefaultIntegerRange(model.Defaults.Steps, model.Limits.Steps.Min, model.Limits.Steps.Max) ||
			!validDefaultFloat(model.Defaults.CFGScale, model.Limits.CFGScale) {
			return false
		}
		if model.Features.FaceDetailer {
			if model.Defaults.FaceDetailer == nil {
				return false
			}
		} else if model.Defaults.FaceDetailer != nil {
			return false
		}
		if model.Limits.FaceDetailer != nil && (!model.Features.FaceDetailer || model.Defaults.FaceDetailer == nil ||
			!validBooleanOptions(*model.Limits.FaceDetailer, *model.Defaults.FaceDetailer)) {
			return false
		}
		if model.Features.LoRAStrength {
			if model.Defaults.LoRAStrength == nil || model.Limits.LoRAStrength == nil ||
				!validDefaultFloat(*model.Defaults.LoRAStrength, *model.Limits.LoRAStrength) {
				return false
			}
		} else if model.Defaults.LoRAStrength != nil || model.Limits.LoRAStrength != nil {
			return false
		}
	}
	return len(options.Models) == 0 || defaultFound
}

func validBooleanOptions(options []bool, defaultValue bool) bool {
	if len(options) == 0 || len(options) > 2 {
		return false
	}
	hasFalse, hasTrue, hasDefault := false, false, false
	for _, option := range options {
		if option {
			if hasTrue {
				return false
			}
			hasTrue = true
		} else {
			if hasFalse {
				return false
			}
			hasFalse = true
		}
		if option == defaultValue {
			hasDefault = true
		}
	}
	return hasDefault
}

func validDefaultInteger(value, min, max, multipleOf int) bool {
	return min > 0 && max >= min && multipleOf > 0 && value >= min && value <= max && value%multipleOf == 0
}

func validDefaultIntegerRange(value, min, max int) bool {
	return min > 0 && max >= min && value >= min && value <= max
}

func validDefaultFloat(value float64, limits domainimage.ImageFloatLimits) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || math.IsNaN(limits.Min) || math.IsInf(limits.Min, 0) ||
		math.IsNaN(limits.Max) || math.IsInf(limits.Max, 0) || math.IsNaN(limits.Step) || math.IsInf(limits.Step, 0) ||
		limits.Max < limits.Min || limits.Step <= 0 || value < limits.Min || value > limits.Max {
		return false
	}
	steps := (value - limits.Min) / limits.Step
	return math.Abs(steps-math.Round(steps)) < 1e-6
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
