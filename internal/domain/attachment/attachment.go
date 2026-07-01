package attachment

import (
	"path/filepath"
	"regexp"
	"strings"
)

// Kind identifies how an attachment should be passed downstream.
type Kind string

const (
	KindImage    Kind = "image"
	KindAudio    Kind = "audio"
	KindDocument Kind = "document"
	KindVideo    Kind = "video"
)

// Attachment is the domain representation of a user supplied file.
type Attachment struct {
	ID                  string   `json:"id"`
	Kind                Kind     `json:"kind"`
	Filename            string   `json:"filename"`
	ContentType         string   `json:"content_type"`
	SizeBytes           int64    `json:"size_bytes"`
	Path                string   `json:"path"`
	SHA256              string   `json:"sha256"`
	ExtractedText       string   `json:"extracted_text,omitempty"`
	ExtractionError     string   `json:"extraction_error,omitempty"`
	ExtractionTruncated bool     `json:"extraction_truncated,omitempty"`
	SecurityWarnings    []string `json:"security_warnings,omitempty"`
	Data                []byte   `json:"-"`
}

// Limits defines accepted attachment sizes.
type Limits struct {
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits keeps Viewer uploads bounded for local operation.
var DefaultLimits = Limits{
	MaxFileBytes:  10 << 20,
	MaxTotalBytes: 30 << 20,
}

// KindFromContentType maps a MIME type to a supported attachment kind.
func KindFromContentType(contentType string) (Kind, bool) {
	ct := strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	if strings.HasPrefix(ct, "image/") {
		return KindImage, true
	}
	if strings.HasPrefix(ct, "video/") {
		return KindVideo, true
	}
	if strings.HasPrefix(ct, "audio/") {
		return KindAudio, true
	}
	if ct == "application/pdf" || strings.HasPrefix(ct, "text/") {
		return KindDocument, true
	}
	switch ct {
	case "application/json", "application/x-yaml", "application/yaml", "application/xml":
		return KindDocument, true
	default:
		return "", false
	}
}

// KindFromFilename maps common Viewer-supported extensions when MIME type is missing.
func KindFromFilename(name string) (Kind, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return KindImage, true
	case ".mp4", ".mov", ".webm", ".m4v":
		return KindVideo, true
	case ".wav", ".mp3", ".flac", ".ogg", ".m4a":
		return KindAudio, true
	case ".pdf", ".txt", ".md", ".json", ".csv", ".yaml", ".yml", ".xml":
		return KindDocument, true
	default:
		return "", false
	}
}

var unsafeFilenameChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// SafeFilename strips paths and normalizes characters for workspace storage.
func SafeFilename(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "." || base == "/" || base == "" {
		return "attachment"
	}
	base = unsafeFilenameChars.ReplaceAllString(base, "_")
	base = strings.Trim(base, "._")
	if base == "" {
		return "attachment"
	}
	return base
}

// SummaryLine returns a compact reference suitable for text-only fallbacks.
func SummaryLine(a Attachment) string {
	kind := string(a.Kind)
	if kind == "" {
		kind = "file"
	}
	line := "- " + kind + ": " + a.Filename + " (" + a.ContentType + ", " + formatBytes(a.SizeBytes) + ", path=" + a.Path + ")"
	if preview := compactPreview(a.ExtractedText, 600); preview != "" {
		line += " 本文プレビュー: " + preview
		if a.ExtractionTruncated {
			line += " ..."
		}
	}
	if errText := compactPreview(a.ExtractionError, 200); errText != "" {
		line += " 抽出エラー: " + errText
	}
	if len(a.SecurityWarnings) > 0 {
		line += " 警告: " + strings.Join(a.SecurityWarnings, ",")
	}
	return line
}

func compactPreview(s string, limit int) string {
	s = strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
	if limit <= 0 || len(s) <= limit {
		return s
	}
	return strings.TrimSpace(s[:limit])
}

func formatBytes(n int64) string {
	if n < 1024 {
		return strconvFormatInt(n) + " B"
	}
	if n < 1024*1024 {
		return strconvFormatInt(n/1024) + " KiB"
	}
	return strconvFormatInt(n/(1024*1024)) + " MiB"
}

func strconvFormatInt(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
