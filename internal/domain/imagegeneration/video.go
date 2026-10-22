package imagegeneration

import (
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const MaxVideoAspectRatioBytes = 64

// VideoGenerateRequest is CORE's bounded public request to RenCrow_Image.
// Generation workflow and fixed model parameters remain owned by RenCrow_Image.
type VideoGenerateRequest struct {
	Mode        string
	Prompt      string
	Seed        int64
	AspectRatio string
	Image       []byte
	ImageType   string
}

// VideoOptionsResponse is the mode-specific, owner-supplied Movie UI contract.
type VideoOptionsResponse struct {
	Model              string   `json:"model"`
	Mode               string   `json:"mode"`
	AspectRatios       []string `json:"aspect_ratios"`
	DefaultAspectRatio string   `json:"default_aspect_ratio"`
	Megapixels         float64  `json:"megapixels"`
	DurationSeconds    int      `json:"duration_seconds"`
	Steps              int      `json:"steps"`
}

func IsVideoMode(mode string) bool {
	return mode == "t2v" || mode == "i2v"
}

// IsValidVideoAspectRatio preserves the exact upstream option while bounding
// data forwarded through the CORE multipart proxy.
func IsValidVideoAspectRatio(value string) bool {
	if len(value) == 0 || len(value) > MaxVideoAspectRatioBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func IsValidVideoOptions(options VideoOptionsResponse, expectedMode string) bool {
	if !IsVideoMode(expectedMode) || options.Mode != expectedMode ||
		len(options.Model) == 0 || len(options.Model) > 128 || !utf8.ValidString(options.Model) ||
		strings.TrimSpace(options.Model) != options.Model || options.AspectRatios == nil ||
		len(options.AspectRatios) == 0 || len(options.AspectRatios) > 32 ||
		!IsValidVideoAspectRatio(options.DefaultAspectRatio) ||
		math.IsNaN(options.Megapixels) || math.IsInf(options.Megapixels, 0) || options.Megapixels <= 0 || options.Megapixels > 64 ||
		options.DurationSeconds < 1 || options.DurationSeconds > 3600 || options.Steps < 1 || options.Steps > 10000 {
		return false
	}
	seen := make(map[string]struct{}, len(options.AspectRatios))
	defaultFound := false
	for _, aspectRatio := range options.AspectRatios {
		if !IsValidVideoAspectRatio(aspectRatio) {
			return false
		}
		if _, found := seen[aspectRatio]; found {
			return false
		}
		seen[aspectRatio] = struct{}{}
		if aspectRatio == options.DefaultAspectRatio {
			defaultFound = true
		}
	}
	return defaultFound
}

// IsVideoGenerationStatus reports whether status is part of the public video job state contract.
func IsVideoGenerationStatus(status string) bool {
	switch status {
	case "queued", "running", "succeeded", "failed":
		return true
	default:
		return false
	}
}

type VideoGenerationAccepted struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

type VideoArtifact struct {
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	SizeBytes   int64  `json:"size_bytes"`
	ContentType string `json:"content_type"`
}

type VideoGenerationError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type VideoGenerationStatus struct {
	ID        string                `json:"id"`
	Status    string                `json:"status"`
	CreatedAt string                `json:"created_at,omitempty"`
	UpdatedAt string                `json:"updated_at,omitempty"`
	Progress  *int                  `json:"progress,omitempty"`
	Video     *VideoArtifact        `json:"video,omitempty"`
	Error     *VideoGenerationError `json:"error,omitempty"`
}
