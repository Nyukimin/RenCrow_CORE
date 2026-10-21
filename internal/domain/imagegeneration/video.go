package imagegeneration

// VideoGenerateRequest is CORE's bounded public request to RenCrow_Image.
// Generation workflow and fixed model parameters remain owned by RenCrow_Image.
type VideoGenerateRequest struct {
	Mode      string
	Prompt    string
	Seed      int64
	Image     []byte
	ImageType string
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
