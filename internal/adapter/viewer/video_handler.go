package viewer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	domainimage "github.com/Nyukimin/RenCrow_CORE/internal/domain/imagegeneration"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/imagegateway"
)

const (
	maxVideoPromptBytes      = 8 << 10
	maxVideoImageBytes       = 20 << 20
	maxVideoMultipartBytes   = maxVideoImageBytes + 64<<10
	maxVideoJSONBytes        = 1 << 20
	maxVideoErrorMessageLen  = 500
	maxVideoRangeHeaderLen   = 256
	maxSafeVideoSeed         = int64(1<<53 - 1)
	maxVideoAspectRatioBytes = domainimage.MaxVideoAspectRatioBytes
)

var (
	videoIDPattern     = regexp.MustCompile("^vid_[A-Za-z0-9_-]{1,128}$")
	videoSHA256Pattern = regexp.MustCompile("^[0-9a-f]{64}$")
	videoRangePattern  = regexp.MustCompile("^bytes [0-9]+-[0-9]+/(?:[0-9]+|\\*)$")
	videoUnsatRange    = regexp.MustCompile("^bytes \\*/[0-9]+$")
	videoCodePattern   = regexp.MustCompile("^[A-Za-z0-9_.-]{1,64}$")
)

type VideoGateway interface {
	CreateVideoGeneration(context.Context, domainimage.VideoGenerateRequest, string, string) (domainimage.VideoGenerationAccepted, error)
	GetVideoGeneration(context.Context, string, string) (domainimage.VideoGenerationStatus, error)
	OpenVideo(context.Context, string, string, string) (*http.Response, error)
}

type VideoOptionsGateway interface {
	VideoOptions(context.Context, string) (domainimage.VideoOptionsResponse, error)
}

// VideoBrowserAccessConfig gates Movie routes to one LAN client. ActorID is the shared Movie output owner key.
type VideoBrowserAccessConfig struct {
	Enabled           bool
	AllowGeneration   bool
	Host              string
	Origin            string
	AllowedClientIPv4 []string
	ActorID           string
}

func HandleVideoOptions(gateway VideoOptionsGateway, access VideoBrowserAccessConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := access.videoOwnerID(r); !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil || len(query) > 1 {
			writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_REQUEST", "Invalid video options request.")
			return
		}
		for key, values := range query {
			if key != "mode" || len(values) != 1 {
				writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_REQUEST", "Invalid video options request.")
				return
			}
		}
		mode := "t2v"
		if values, found := query["mode"]; found {
			mode = values[0]
		}
		if !domainimage.IsVideoMode(mode) {
			writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_REQUEST", "Invalid video options request.")
			return
		}
		if gateway == nil {
			writeVideoError(w, http.StatusServiceUnavailable, "VIDEO_UNAVAILABLE", "Video generation is unavailable.")
			return
		}
		options, err := gateway.VideoOptions(r.Context(), mode)
		if err != nil {
			status := http.StatusBadGateway
			var serviceError *imagegateway.ServiceError
			if errors.As(err, &serviceError) && serviceError.StatusCode == http.StatusServiceUnavailable {
				status = http.StatusServiceUnavailable
			}
			writeVideoError(w, status, "VIDEO_OPTIONS_UNAVAILABLE", "Video options are unavailable.")
			return
		}
		if !domainimage.IsValidVideoOptions(options, mode) {
			writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned invalid options.")
			return
		}
		writeImageJSON(w, http.StatusOK, options)
	}
}

func HandleVideoGenerate(gateway VideoGateway, access VideoBrowserAccessConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actorID, ok := access.videoOwnerID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !access.AllowGeneration {
			writeVideoError(w, http.StatusForbidden, "VIDEO_GENERATION_DISABLED", "Video generation is disabled by CORE policy.")
			return
		}
		if gateway == nil {
			writeVideoError(w, http.StatusServiceUnavailable, "VIDEO_UNAVAILABLE", "Video generation is unavailable.")
			return
		}
		idempotencyKey, ok := singleHeaderValue(r.Header, "Idempotency-Key")
		if !ok || !validVideoIdempotencyKey(idempotencyKey) {
			writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_REQUEST", "A valid Idempotency-Key is required.")
			return
		}
		request, err := parseVideoGenerateRequest(w, r)
		if err != nil {
			if errors.Is(err, errVideoUploadTooLarge) || isVideoBodyTooLarge(err) {
				writeVideoError(w, http.StatusRequestEntityTooLarge, "VIDEO_UPLOAD_TOO_LARGE", "The video request exceeds an input size limit.")
				return
			}
			writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_REQUEST", "Invalid video generation request.")
			return
		}
		accepted, err := gateway.CreateVideoGeneration(r.Context(), request, actorID, idempotencyKey)
		if err != nil {
			writeVideoGatewayError(w, err)
			return
		}
		if !videoIDPattern.MatchString(accepted.ID) || !domainimage.IsVideoGenerationStatus(accepted.Status) {
			writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned an invalid response.")
			return
		}
		writeImageJSON(w, http.StatusAccepted, accepted)
	}
}

func HandleVideoStatus(gateway VideoGateway, access VideoBrowserAccessConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actorID, ok := access.videoOwnerID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/viewer/image/video/generations/")
		if !videoIDPattern.MatchString(id) || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		if gateway == nil {
			writeVideoError(w, http.StatusServiceUnavailable, "VIDEO_UNAVAILABLE", "Video generation is unavailable.")
			return
		}
		status, err := gateway.GetVideoGeneration(r.Context(), id, actorID)
		if err != nil {
			writeVideoGatewayError(w, err)
			return
		}
		if err := validateVideoStatus(id, &status); err != nil {
			writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned an invalid response.")
			return
		}
		if status.Video != nil {
			status.Video.URL = "/viewer/image/video/" + id + ".mp4"
		}
		writeImageJSON(w, http.StatusOK, status)
	}
}

func HandleVideoResult(gateway VideoGateway, access VideoBrowserAccessConfig) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		actorID, ok := access.videoOwnerID(r)
		if !ok {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		const prefix, suffix = "/viewer/image/video/", ".mp4"
		path := r.URL.Path
		if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
			http.NotFound(w, r)
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
		if !videoIDPattern.MatchString(id) || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		rangeValues := headerValuesCaseInsensitive(r.Header, "Range")
		if len(rangeValues) > 1 || (len(rangeValues) == 1 && len(rangeValues[0]) > maxVideoRangeHeaderLen) {
			writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_RANGE", "Invalid video byte range.")
			return
		}
		rangeHeader := ""
		if len(rangeValues) == 1 {
			rangeHeader = rangeValues[0]
		}
		if gateway == nil {
			writeVideoError(w, http.StatusServiceUnavailable, "VIDEO_UNAVAILABLE", "Video generation is unavailable.")
			return
		}
		response, err := gateway.OpenVideo(r.Context(), id, actorID, rangeHeader)
		if err != nil {
			writeVideoGatewayError(w, err)
			return
		}
		if response == nil || response.Body == nil {
			writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned an invalid response.")
			return
		}
		defer response.Body.Close()
		switch response.StatusCode {
		case http.StatusOK, http.StatusPartialContent:
			if !isVideoMP4(response.Header.Get("Content-Type")) {
				writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned an invalid media type.")
				return
			}
			if response.StatusCode == http.StatusPartialContent && !videoRangePattern.MatchString(response.Header.Get("Content-Range")) {
				writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned an invalid byte range.")
				return
			}
			w.Header().Set("Content-Type", "video/mp4")
			w.Header().Set("Accept-Ranges", "bytes")
			if contentRange := response.Header.Get("Content-Range"); response.StatusCode == http.StatusPartialContent {
				w.Header().Set("Content-Range", contentRange)
			}
			if response.ContentLength >= 0 {
				w.Header().Set("Content-Length", strconv.FormatInt(response.ContentLength, 10))
			}
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
		case http.StatusRequestedRangeNotSatisfiable:
			contentRange := response.Header.Get("Content-Range")
			if !videoUnsatRange.MatchString(contentRange) {
				writeVideoError(w, http.StatusBadGateway, "VIDEO_INVALID_RESPONSE", "Video generation service returned an invalid byte range.")
				return
			}
			w.Header().Set("Content-Range", contentRange)
			w.Header().Set("Accept-Ranges", "bytes")
			w.Header().Set("Content-Length", "0")
			w.Header().Set("Cache-Control", "private, no-store")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
		default:
			if response.StatusCode == http.StatusNotFound {
				http.NotFound(w, r)
				return
			}
			writeVideoError(w, http.StatusBadGateway, "VIDEO_UNAVAILABLE", "Video result is unavailable.")
		}
	}
}

func singleHeaderValue(header http.Header, name string) (string, bool) {
	values := headerValuesCaseInsensitive(header, name)
	if len(values) != 1 {
		return "", false
	}
	return values[0], true
}

func headerValuesCaseInsensitive(header http.Header, name string) []string {
	var values []string
	for key, entries := range header {
		if strings.EqualFold(key, name) {
			values = append(values, entries...)
		}
	}
	return values
}

func hasHeaderName(header http.Header, name string) bool {
	for key := range header {
		if strings.EqualFold(key, name) {
			return true
		}
	}
	return false
}

func validVideoIdempotencyKey(value string) bool {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 128 {
		return false
	}
	for _, character := range value {
		if !((character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') || strings.ContainsRune("._:-", character)) {
			return false
		}
	}
	return true
}

var errVideoUploadTooLarge = errors.New("video upload too large")

func parseVideoGenerateRequest(w http.ResponseWriter, r *http.Request) (domainimage.VideoGenerateRequest, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxVideoMultipartBytes)
	reader, err := r.MultipartReader()
	if err != nil {
		return domainimage.VideoGenerateRequest{}, err
	}
	fields := make(map[string]string, 4)
	fieldsSeen := make(map[string]bool, 4)
	var imageData []byte
	var imageType string
	imageSeen := false
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return domainimage.VideoGenerateRequest{}, err
		}
		name := part.FormName()
		switch name {
		case "mode", "prompt", "seed", "aspect_ratio":
			if part.FileName() != "" || fieldsSeen[name] {
				_ = part.Close()
				return domainimage.VideoGenerateRequest{}, fmt.Errorf("duplicate or invalid %s field", name)
			}
			limit := int64(maxVideoPromptBytes)
			if name == "mode" {
				limit = 8
			} else if name == "seed" {
				limit = 32
			} else if name == "aspect_ratio" {
				limit = maxVideoAspectRatioBytes
			}
			value, readErr := readVideoPart(part, limit)
			_ = part.Close()
			if readErr != nil {
				return domainimage.VideoGenerateRequest{}, readErr
			}
			fields[name] = string(value)
			fieldsSeen[name] = true
		case "image":
			if imageSeen || part.FileName() == "" {
				_ = part.Close()
				return domainimage.VideoGenerateRequest{}, fmt.Errorf("duplicate or invalid image field")
			}
			imageSeen = true
			imageData, err = readVideoPart(part, maxVideoImageBytes)
			_ = part.Close()
			if err != nil {
				return domainimage.VideoGenerateRequest{}, err
			}
			imageType = http.DetectContentType(imageData)
			if imageType != "image/png" && imageType != "image/jpeg" {
				return domainimage.VideoGenerateRequest{}, fmt.Errorf("unsupported image media type")
			}
		default:
			_ = part.Close()
			return domainimage.VideoGenerateRequest{}, fmt.Errorf("unknown multipart field")
		}
	}
	mode := fields["mode"]
	prompt := strings.TrimSpace(fields["prompt"])
	aspectRatio := fields["aspect_ratio"]
	if !domainimage.IsVideoMode(mode) || prompt == "" || len(prompt) > maxVideoPromptBytes || !utf8.ValidString(prompt) {
		return domainimage.VideoGenerateRequest{}, fmt.Errorf("invalid mode or prompt")
	}
	if fieldsSeen["aspect_ratio"] && !domainimage.IsValidVideoAspectRatio(aspectRatio) {
		return domainimage.VideoGenerateRequest{}, fmt.Errorf("invalid aspect ratio")
	}
	seed, err := strconv.ParseInt(fields["seed"], 10, 64)
	if err != nil || seed < 0 || seed > maxSafeVideoSeed {
		return domainimage.VideoGenerateRequest{}, fmt.Errorf("invalid seed")
	}
	if mode == "i2v" && (!imageSeen || len(imageData) == 0) {
		return domainimage.VideoGenerateRequest{}, fmt.Errorf("i2v image is required")
	}
	if mode == "t2v" && imageSeen {
		return domainimage.VideoGenerateRequest{}, fmt.Errorf("t2v does not accept an image")
	}
	return domainimage.VideoGenerateRequest{
		Mode: mode, Prompt: prompt, Seed: seed, AspectRatio: aspectRatio, Image: imageData, ImageType: imageType,
	}, nil
}

func readVideoPart(part *multipart.Part, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(part, limit+1))
	if err != nil {
		if isVideoBodyTooLarge(err) {
			return nil, errVideoUploadTooLarge
		}
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errVideoUploadTooLarge
	}
	return data, nil
}

func isVideoBodyTooLarge(err error) bool {
	var maxBytesError *http.MaxBytesError
	return errors.As(err, &maxBytesError)
}

func validateVideoStatus(expectedID string, status *domainimage.VideoGenerationStatus) error {
	if status == nil || status.ID != expectedID {
		return fmt.Errorf("status ID mismatch")
	}
	switch status.Status {
	case "queued", "running":
		if status.Video != nil {
			return fmt.Errorf("nonterminal video status includes media")
		}
		status.Error = nil
	case "succeeded":
		if status.Video == nil || status.Video.URL != "/v1/videos/"+expectedID+".mp4" ||
			status.Video.ContentType != "video/mp4" || status.Video.SizeBytes <= 0 ||
			!videoSHA256Pattern.MatchString(status.Video.SHA256) {
			return fmt.Errorf("invalid video metadata")
		}
		status.Error = nil
	case "failed":
		status.Video = nil
		if status.Error == nil || !videoCodePattern.MatchString(status.Error.Code) ||
			strings.TrimSpace(status.Error.Message) == "" || len(status.Error.Message) > maxVideoErrorMessageLen {
			status.Error = &domainimage.VideoGenerationError{
				Code: "VIDEO_GENERATION_FAILED", Message: "Video generation failed.",
			}
		}
	default:
		return fmt.Errorf("invalid video status")
	}
	if status.Progress != nil && (*status.Progress < 0 || *status.Progress > 100) {
		return fmt.Errorf("invalid video progress")
	}
	return nil
}

func isVideoMP4(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	return err == nil && mediaType == "video/mp4"
}

func writeVideoGatewayError(w http.ResponseWriter, err error) {
	var serviceError *imagegateway.ServiceError
	if errors.As(err, &serviceError) {
		switch serviceError.StatusCode {
		case http.StatusBadRequest:
			writeVideoError(w, http.StatusBadRequest, "VIDEO_INVALID_REQUEST", "Video generation request was rejected.")
		case http.StatusNotFound:
			writeVideoError(w, http.StatusNotFound, "VIDEO_NOT_FOUND", "Video job was not found.")
		case http.StatusConflict:
			writeVideoError(w, http.StatusConflict, "VIDEO_CONFLICT", "Video generation request conflicts with an existing job.")
		case http.StatusTooManyRequests:
			writeVideoError(w, http.StatusTooManyRequests, "VIDEO_BUSY", "Video generation service is busy.")
		case http.StatusServiceUnavailable:
			writeVideoError(w, http.StatusServiceUnavailable, "VIDEO_UNAVAILABLE", "Video generation is unavailable.")
		default:
			writeVideoError(w, http.StatusBadGateway, "VIDEO_UPSTREAM_FAILED", "Video generation service failed.")
		}
		return
	}
	writeVideoError(w, http.StatusBadGateway, "VIDEO_UPSTREAM_FAILED", "Video generation service failed.")
}

func writeVideoError(w http.ResponseWriter, status int, code, message string) {
	writeImageJSON(w, status, map[string]any{
		"ok": false,
		"error": map[string]string{
			"code": code, "message": message,
		},
	})
}
