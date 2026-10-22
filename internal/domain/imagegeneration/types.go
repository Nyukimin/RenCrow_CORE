package imagegeneration

import "regexp"

var profileIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// GenerateRequest is the stable CORE-to-RenCrow_Image text-to-image request.
// Backend-specific settings intentionally do not belong here.
type GenerateRequest struct {
	Prompt         string   `json:"prompt"`
	NegativePrompt string   `json:"negative_prompt,omitempty"`
	Seed           *int64   `json:"seed,omitempty"`
	Model          *string  `json:"model,omitempty"`
	Width          *int     `json:"width,omitempty"`
	Height         *int     `json:"height,omitempty"`
	Steps          *int     `json:"steps,omitempty"`
	CFGScale       *float64 `json:"cfg_scale,omitempty"`
	FaceDetailer   *bool    `json:"face_detailer,omitempty"`
	LoRAStrength   *float64 `json:"lora_strength,omitempty"`
}

// ValidProfileID reports whether an Image profile identifier is safe to pass
// through the CORE-to-RenCrow_Image contract. RenCrow_Image remains the owner
// of the allowlist and validates that the identifier is configured.
func ValidProfileID(id string) bool {
	return profileIDPattern.MatchString(id)
}

type OptionsResponse struct {
	OK           bool               `json:"ok"`
	DefaultModel string             `json:"default_model"`
	Models       []ImageOptionModel `json:"models"`
}

type ImageOptionModel struct {
	ID       string               `json:"id"`
	Engine   string               `json:"engine"`
	Defaults ImageOptionDefaults  `json:"defaults"`
	Limits   ImageOptionLimits    `json:"limits"`
	Features *ImageOptionFeatures `json:"features"`
}

type ImageOptionDefaults struct {
	Width        int      `json:"width"`
	Height       int      `json:"height"`
	Steps        int      `json:"steps"`
	CFGScale     float64  `json:"cfg_scale"`
	FaceDetailer *bool    `json:"face_detailer,omitempty"`
	LoRAStrength *float64 `json:"lora_strength,omitempty"`
}

type ImageOptionLimits struct {
	Width        ImageIntegerMultipleLimits `json:"width"`
	Height       ImageIntegerMultipleLimits `json:"height"`
	Steps        ImageIntegerLimits         `json:"steps"`
	CFGScale     ImageFloatLimits           `json:"cfg_scale"`
	FaceDetailer *[]bool                    `json:"face_detailer,omitempty"`
	LoRAStrength *ImageFloatLimits          `json:"lora_strength,omitempty"`
}

type ImageIntegerMultipleLimits struct {
	Min        int `json:"min"`
	Max        int `json:"max"`
	MultipleOf int `json:"multiple_of"`
}

type ImageIntegerLimits struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

type ImageFloatLimits struct {
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Step float64 `json:"step"`
}

type ImageOptionFeatures struct {
	FaceDetailer bool `json:"face_detailer"`
	LoRAStrength bool `json:"lora_strength"`
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
