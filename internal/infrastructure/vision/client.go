package vision

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	domainvision "github.com/Nyukimin/RenCrow_CORE/internal/domain/vision"
)

const maxResponseBytes = 4 << 20

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

func (e *ServiceError) VisionErrorCode() string {
	return e.Code
}

func NewClient(baseURL string, timeout time.Duration) (*Client, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("vision base URL must be an absolute HTTP URL")
	}
	if timeout <= 0 {
		return nil, fmt.Errorf("vision timeout must be positive")
	}
	return &Client{
		baseURL:    baseURL,
		httpClient: &http.Client{Timeout: timeout},
	}, nil
}

func (c *Client) Analyze(ctx context.Context, request domainvision.AnalyzeRequest) (domainvision.AnalyzeResult, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	fields := map[string]string{
		"prompt":        request.Prompt,
		"kind":          request.Kind,
		"request_id":    request.RequestID,
		"session_id":    request.SessionID,
		"language":      request.Language,
		"max_frames":    strconv.Itoa(request.MaxFrames),
		"output_format": "json",
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			return domainvision.AnalyzeResult{}, fmt.Errorf("build vision request field %s: %w", name, err)
		}
	}
	filename := strings.TrimSpace(request.Filename)
	if filename == "" {
		filename = "upload.bin"
	}
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, escapeMultipartFilename(filename)))
	contentType := strings.TrimSpace(request.ContentType)
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	header.Set("Content-Type", contentType)
	part, err := writer.CreatePart(header)
	if err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("build vision file part: %w", err)
	}
	if _, err := part.Write(request.Data); err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("write vision file part: %w", err)
	}
	if err := writer.Close(); err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("finish vision request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/vision/analyze", &body)
	if err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("create vision request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	httpRequest.Header.Set("Accept", "application/json")
	if request.RequestID != "" {
		httpRequest.Header.Set("X-Request-Id", request.RequestID)
	}

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("vision request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("read vision response: %w", err)
	}
	if len(payload) > maxResponseBytes {
		return domainvision.AnalyzeResult{}, fmt.Errorf("vision response exceeds %d bytes", maxResponseBytes)
	}

	var result domainvision.AnalyzeResult
	if err := json.Unmarshal(payload, &result); err != nil {
		return domainvision.AnalyzeResult{}, fmt.Errorf("decode vision response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !result.OK {
		return domainvision.AnalyzeResult{}, &ServiceError{
			StatusCode: response.StatusCode,
			Code:       firstNonEmpty(result.ErrorCode, "VISION_REQUEST_FAILED"),
			Message:    firstNonEmpty(result.Message, http.StatusText(response.StatusCode)),
		}
	}
	if strings.TrimSpace(result.Text) == "" {
		return domainvision.AnalyzeResult{}, &ServiceError{
			StatusCode: response.StatusCode,
			Code:       "VISION_EMPTY_RESULT",
			Message:    "RenCrow_Vision returned an empty result",
		}
	}
	return result, nil
}

func (c *Client) Health(ctx context.Context) (domainvision.HealthReport, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/health", nil)
	if err != nil {
		return domainvision.HealthReport{}, fmt.Errorf("create vision health request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	response, err := c.httpClient.Do(request)
	if err != nil {
		return domainvision.HealthReport{}, fmt.Errorf("vision health request failed: %w", err)
	}
	defer response.Body.Close()
	var report domainvision.HealthReport
	if err := json.NewDecoder(io.LimitReader(response.Body, maxResponseBytes)).Decode(&report); err != nil {
		return domainvision.HealthReport{}, fmt.Errorf("decode vision health response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !report.OK || report.Status != "ready" || !report.Ready.ModelLoaded {
		return report, &ServiceError{
			StatusCode: response.StatusCode,
			Code:       "VISION_NOT_READY",
			Message:    fmt.Sprintf("RenCrow_Vision is not ready (status=%s model_loaded=%t)", report.Status, report.Ready.ModelLoaded),
		}
	}
	return report, nil
}

func escapeMultipartFilename(value string) string {
	return strings.NewReplacer("\\", "_", `"`, "_", "\r", "_", "\n", "_").Replace(value)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}
