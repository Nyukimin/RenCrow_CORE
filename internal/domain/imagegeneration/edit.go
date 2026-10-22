package imagegeneration

const (
	MaxImageEditImageBytes  = 20 << 20
	MaxImageEditPromptBytes = 8 << 10
	MaxImageEditBodyBytes   = (42 << 20) + MaxImageEditPromptBytes
	MaxImageEditSidePixels  = 12000
	MaxImageEditPixelCount  = 40_000_000
)

// EditRequest is CORE's validated two-image request to RenCrow_Image.
// Image edit operations and their workflows remain owned by RenCrow_Image.
type EditRequest struct {
	Operation            string
	Person               []byte
	PersonContentType    string
	Reference            []byte
	ReferenceContentType string
	Prompt               string
	PromptProvided       bool
	Seed                 int64
}

type EditOptionsResponse struct {
	OK         bool     `json:"ok"`
	Operations []string `json:"operations"`
}
