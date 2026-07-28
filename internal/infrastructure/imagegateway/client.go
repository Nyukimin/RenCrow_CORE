package imagegateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
