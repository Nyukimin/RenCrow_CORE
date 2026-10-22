package imagegeneration

import (
	"encoding/binary"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	MaxVideoAspectRatioBytes = 64
	MaxVideoAudioBytes       = 20 << 20
	MaxVideoV2VBytes         = 100 << 20
	VideoTaskLipSync         = "lip_sync"
	VideoModeV2V             = "v2v"
)

// VideoGenerateRequest is CORE's bounded public request to RenCrow_Image.
// Generation workflow and fixed model parameters remain owned by RenCrow_Image.
type VideoGenerateRequest struct {
	Task        string
	Mode        string
	Prompt      string
	Seed        int64
	AspectRatio string
	Image       []byte
	ImageType   string
	Video       []byte
	Audio       []byte
}

// VideoOptionsResponse is the mode-specific, owner-supplied Movie UI contract.
type VideoOptionsResponse struct {
	Task               string   `json:"task,omitempty"`
	Model              string   `json:"model"`
	Mode               string   `json:"mode"`
	AspectRatios       []string `json:"aspect_ratios,omitempty"`
	DefaultAspectRatio string   `json:"default_aspect_ratio,omitempty"`
	Megapixels         float64  `json:"megapixels,omitempty"`
	DurationSeconds    int      `json:"duration_seconds"`
	Steps              int      `json:"steps,omitempty"`
	MaxVideoBytes      int64    `json:"max_video_bytes,omitempty"`
	MaxAudioBytes      int64    `json:"max_audio_bytes,omitempty"`
}

func IsVideoMode(mode string) bool {
	return mode == "t2v" || mode == "i2v" || mode == VideoModeV2V
}

func IsVideoModeForTask(mode, task string) bool {
	if mode == VideoModeV2V {
		return task == VideoTaskLipSync
	}
	return (mode == "t2v" || mode == "i2v") && IsVideoTask(task)
}

func IsVideoTask(task string) bool {
	return task == "" || task == VideoTaskLipSync
}

// IsValidVideoPCMWave bounds Movie audio uploads and checks the RIFF/WAVE
// container and PCM format. Audio duration remains an Image owner check.
func IsValidVideoPCMWave(audio []byte) bool {
	if len(audio) < 46 || len(audio) > MaxVideoAudioBytes ||
		string(audio[:4]) != "RIFF" || string(audio[8:12]) != "WAVE" {
		return false
	}
	riFFEnd := uint64(binary.LittleEndian.Uint32(audio[4:8])) + 8
	if riFFEnd != uint64(len(audio)) {
		return false
	}

	hasPCMFormat := false
	hasAudioData := false
	for offset := uint64(12); offset < riFFEnd; {
		if riFFEnd-offset < 8 {
			return false
		}
		chunkID := string(audio[offset : offset+4])
		chunkSize := uint64(binary.LittleEndian.Uint32(audio[offset+4 : offset+8]))
		chunkStart := offset + 8
		chunkEnd := chunkStart + chunkSize
		if chunkEnd > riFFEnd {
			return false
		}
		switch chunkID {
		case "fmt ":
			if hasPCMFormat || chunkSize < 16 || binary.LittleEndian.Uint16(audio[chunkStart:chunkStart+2]) != 1 {
				return false
			}
			hasPCMFormat = true
		case "data":
			if hasAudioData || chunkSize == 0 {
				return false
			}
			hasAudioData = true
		}
		offset = chunkEnd + (chunkSize & 1)
		if offset > riFFEnd {
			return false
		}
	}
	return hasPCMFormat && hasAudioData
}

// IsValidVideoMP4 checks the first ISO BMFF box only. RenCrow_Image owns media
// decoding, duration, and the rest of the MP4 semantic validation.
func IsValidVideoMP4(video []byte) bool {
	if len(video) < 16 || string(video[4:8]) != "ftyp" {
		return false
	}
	boxSize := uint64(binary.BigEndian.Uint32(video[:4]))
	return boxSize >= 16 && boxSize <= uint64(len(video)) && boxSize%4 == 0
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

func IsValidVideoOptions(options VideoOptionsResponse, expectedMode, expectedTask string) bool {
	if !IsVideoModeForTask(expectedMode, expectedTask) || options.Task != expectedTask || options.Mode != expectedMode ||
		len(options.Model) == 0 || len(options.Model) > 128 || !utf8.ValidString(options.Model) ||
		strings.TrimSpace(options.Model) != options.Model {
		return false
	}
	if expectedMode == VideoModeV2V {
		return expectedTask == VideoTaskLipSync && options.Model == "MuseTalk 1.5" &&
			options.AspectRatios == nil && options.DefaultAspectRatio == "" && options.Megapixels == 0 && options.Steps == 0 &&
			options.DurationSeconds == 10 && options.MaxVideoBytes > 0 && options.MaxVideoBytes <= MaxVideoV2VBytes &&
			options.MaxAudioBytes == MaxVideoAudioBytes
	}
	if options.MaxVideoBytes != 0 || options.MaxAudioBytes != 0 || options.AspectRatios == nil ||
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
