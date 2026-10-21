package imagegateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
)

const maxVideoJSONResponseBytes = 1 << 20

var videoGenerationIDPattern = regexp.MustCompile("^vid_[A-Za-z0-9_-]{1,128}$")

type videoServiceErrorPayload struct {
	Error     *domainimage.VideoGenerationError `json:"error"`
	ErrorCode string                            `json:"error_code"`
	Message   string                            `json:"message"`
}

func (c *Client) CreateVideoGeneration(
	ctx context.Context,
	request domainimage.VideoGenerateRequest,
	actorID string,
	idempotencyKey string,
) (domainimage.VideoGenerationAccepted, error) {
	if err := validateVideoActor(actorID); err != nil {
		return domainimage.VideoGenerationAccepted{}, err
	}
	if err := validateVideoIdempotencyKey(idempotencyKey); err != nil {
		return domainimage.VideoGenerationAccepted{}, err
	}
	if err := validateVideoRequest(request); err != nil {
		return domainimage.VideoGenerationAccepted{}, err
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for name, value := range map[string]string{
		"mode":   request.Mode,
		"prompt": request.Prompt,
		"seed":   strconv.FormatInt(request.Seed, 10),
	} {
		if err := writer.WriteField(name, value); err != nil {
			return domainimage.VideoGenerationAccepted{}, fmt.Errorf("write video request field: %w", err)
		}
	}
	if request.Mode == "i2v" {
		filename := "input.png"
		if request.ImageType == "image/jpeg" {
			filename = "input.jpg"
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
			"name": "image", "filename": filename,
		}))
		header.Set("Content-Type", request.ImageType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return domainimage.VideoGenerationAccepted{}, fmt.Errorf("create video image part: %w", err)
		}
		if _, err := part.Write(request.Image); err != nil {
			return domainimage.VideoGenerationAccepted{}, fmt.Errorf("write video image part: %w", err)
		}
	}
	if err := writer.Close(); err != nil {
		return domainimage.VideoGenerationAccepted{}, fmt.Errorf("close video request multipart: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/v1/videos/generations", &body)
	if err != nil {
		return domainimage.VideoGenerationAccepted{}, fmt.Errorf("create video generation request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("Content-Type", writer.FormDataContentType())
	httpRequest.Header.Set("Idempotency-Key", idempotencyKey)
	httpRequest.Header.Set("X-RenCrow-Actor-ID", actorID)

	response, err := c.doVideoRequest(httpRequest)
	if err != nil {
		return domainimage.VideoGenerationAccepted{}, fmt.Errorf("RenCrow_Image video request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := readVideoJSONResponse(response.Body)
	if err != nil {
		return domainimage.VideoGenerationAccepted{}, err
	}
	if response.StatusCode != http.StatusAccepted {
		return domainimage.VideoGenerationAccepted{}, parseVideoServiceError(response.StatusCode, payload)
	}
	var accepted domainimage.VideoGenerationAccepted
	if err := json.Unmarshal(payload, &accepted); err != nil {
		return domainimage.VideoGenerationAccepted{}, fmt.Errorf("decode RenCrow_Image video response: %w", err)
	}
	if !videoGenerationIDPattern.MatchString(accepted.ID) || !domainimage.IsVideoGenerationStatus(accepted.Status) {
		return domainimage.VideoGenerationAccepted{}, fmt.Errorf("RenCrow_Image returned an invalid video generation response")
	}
	return accepted, nil
}

func (c *Client) GetVideoGeneration(
	ctx context.Context,
	id string,
	actorID string,
) (domainimage.VideoGenerationStatus, error) {
	if !videoGenerationIDPattern.MatchString(id) {
		return domainimage.VideoGenerationStatus{}, fmt.Errorf("invalid video generation ID")
	}
	if err := validateVideoActor(actorID); err != nil {
		return domainimage.VideoGenerationStatus{}, err
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v1/videos/generations/"+url.PathEscape(id), nil)
	if err != nil {
		return domainimage.VideoGenerationStatus{}, fmt.Errorf("create video status request: %w", err)
	}
	httpRequest.Header.Set("Accept", "application/json")
	httpRequest.Header.Set("X-RenCrow-Actor-ID", actorID)

	response, err := c.doVideoRequest(httpRequest)
	if err != nil {
		return domainimage.VideoGenerationStatus{}, fmt.Errorf("RenCrow_Image video status request failed: %w", err)
	}
	defer response.Body.Close()
	payload, err := readVideoJSONResponse(response.Body)
	if err != nil {
		return domainimage.VideoGenerationStatus{}, err
	}
	if response.StatusCode != http.StatusOK {
		return domainimage.VideoGenerationStatus{}, parseVideoServiceError(response.StatusCode, payload)
	}
	var status domainimage.VideoGenerationStatus
	if err := json.Unmarshal(payload, &status); err != nil {
		return domainimage.VideoGenerationStatus{}, fmt.Errorf("decode RenCrow_Image video status: %w", err)
	}
	return status, nil
}

func (c *Client) OpenVideo(
	ctx context.Context,
	id string,
	actorID string,
	rangeHeader string,
) (*http.Response, error) {
	if !videoGenerationIDPattern.MatchString(id) {
		return nil, fmt.Errorf("invalid video generation ID")
	}
	if err := validateVideoActor(actorID); err != nil {
		return nil, err
	}
	if len(rangeHeader) > 256 || strings.ContainsAny(rangeHeader, "\r\n") {
		return nil, fmt.Errorf("invalid video range header")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.baseURL+"/v1/videos/"+url.PathEscape(id)+".mp4", nil)
	if err != nil {
		return nil, fmt.Errorf("create video media request: %w", err)
	}
	httpRequest.Header.Set("Accept", "video/mp4")
	httpRequest.Header.Set("X-RenCrow-Actor-ID", actorID)
	if rangeHeader != "" {
		httpRequest.Header.Set("Range", rangeHeader)
	}
	response, err := c.doVideoRequest(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("RenCrow_Image video media request failed: %w", err)
	}
	return response, nil
}

func (c *Client) doVideoRequest(request *http.Request) (*http.Response, error) {
	client := *c.httpClient
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client.Do(request)
}

func readVideoJSONResponse(body io.Reader) ([]byte, error) {
	payload, err := io.ReadAll(io.LimitReader(body, maxVideoJSONResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read RenCrow_Image video response: %w", err)
	}
	if len(payload) > maxVideoJSONResponseBytes {
		return nil, fmt.Errorf("RenCrow_Image video response is too large")
	}
	return payload, nil
}

func parseVideoServiceError(status int, payload []byte) error {
	var details videoServiceErrorPayload
	_ = json.Unmarshal(payload, &details)
	code, message := details.ErrorCode, details.Message
	if details.Error != nil {
		code, message = details.Error.Code, details.Error.Message
	}
	return &ServiceError{
		StatusCode: status,
		Code:       firstNonEmpty(code, "VIDEO_REQUEST_FAILED"),
		Message:    firstNonEmpty(message, http.StatusText(status)),
	}
}

func validateVideoRequest(request domainimage.VideoGenerateRequest) error {
	if request.Mode != "t2v" && request.Mode != "i2v" {
		return fmt.Errorf("invalid video mode")
	}
	if strings.TrimSpace(request.Prompt) == "" || len(request.Prompt) > 8<<10 {
		return fmt.Errorf("invalid video prompt")
	}
	if request.Seed < 0 || request.Seed > int64(1<<53-1) {
		return fmt.Errorf("invalid video seed")
	}
	if request.Mode == "i2v" {
		if len(request.Image) == 0 || len(request.Image) > 20<<20 ||
			(request.ImageType != "image/png" && request.ImageType != "image/jpeg") {
			return fmt.Errorf("invalid video input image")
		}
	} else if len(request.Image) != 0 || request.ImageType != "" {
		return fmt.Errorf("text-to-video request cannot include an image")
	}
	return nil
}

func validateVideoActor(actorID string) error {
	if actorID == "" || strings.TrimSpace(actorID) != actorID || len(actorID) > 255 {
		return fmt.Errorf("invalid video actor ID")
	}
	for _, character := range actorID {
		if character < 0x20 || character == 0x7f {
			return fmt.Errorf("invalid video actor ID")
		}
	}
	return nil
}

func validateVideoIdempotencyKey(key string) error {
	if key == "" || strings.TrimSpace(key) != key || len(key) > 128 {
		return fmt.Errorf("invalid video idempotency key")
	}
	for _, character := range key {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character)) {
			return fmt.Errorf("invalid video idempotency key")
		}
	}
	return nil
}
