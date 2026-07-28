package imagegeneration

// GenerateRequest is the stable CORE-to-RenCrow_Image text-to-image request.
// Backend-specific settings intentionally do not belong here.
type GenerateRequest struct {
	Prompt         string `json:"prompt"`
	NegativePrompt string `json:"negative_prompt,omitempty"`
	Seed           *int64 `json:"seed,omitempty"`
}

type ImageResult struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	ContentType string `json:"content_type"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

type GenerateResult struct {
	OK      bool        `json:"ok"`
	ID      string      `json:"id"`
	Created int64       `json:"created"`
	Profile string      `json:"profile"`
	Prompt  string      `json:"prompt"`
	Image   ImageResult `json:"image"`

	ErrorCode string `json:"error_code,omitempty"`
	Message   string `json:"message,omitempty"`
}
