package charimage

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	ModelProfileID = "moss-image-sft-8-steps-dmd"
	CatalogSize    = 22
	MaxSceneBytes  = 8 << 10
)

type RenderMode string

const (
	RenderMode2D RenderMode = "2d"
	RenderMode3D RenderMode = "3d"
)

const (
	StatusExistingConceptUnconfirmed = "existing_concept_unconfirmed"
	StatusCandidateUnapproved        = "candidate_unapproved"
)

const (
	render2DTail = "Keep the character's defining facial and body features, silhouette, signature colors, and garment identity consistent across the image. Render one continuous image with clearly readable anatomy and intentional composition."
	render3DTail = "Preserve identity-specific geometry and proportions, with clean physically coherent materials, correctly connected anatomy, plausible object contact, and one unified camera view. This is a rendered 3D image, not an editable 3D asset."
)

var (
	//go:embed catalog.json
	catalogFS          embed.FS
	characterIDPattern = regexp.MustCompile(`^[0-9]{2}_[a-z0-9]+$`)
)

type CharacterFixed struct {
	CharacterCoreEN string `json:"character_core_en"`
	Render2DEN      string `json:"render_2d_en"`
	Render3DEN      string `json:"render_3d_en"`
}

type Character struct {
	ID             string         `json:"id"`
	NameJA         string         `json:"name_ja"`
	NameEN         string         `json:"name_en"`
	Status         string         `json:"status"`
	TaglineJA      string         `json:"tagline_ja"`
	SourceNotesJA  string         `json:"source_notes_ja,omitempty"`
	Fixed          CharacterFixed `json:"fixed"`
	DefaultSceneEN string         `json:"default_scene_en"`
}

type Catalog struct {
	SchemaVersion string       `json:"schema_version"`
	Model         string       `json:"model"`
	SourceModel   string       `json:"source_model_label"`
	Scope         string       `json:"scope"`
	PromptFormat  string       `json:"prompt_format"`
	RenderModes   []RenderMode `json:"render_modes"`
	StatusNotesJA string       `json:"status_notes_ja"`
	PrincipleJA   string       `json:"principle_ja"`
	Characters    []Character  `json:"characters"`
	Revision      string       `json:"-"`
}

type ComposeInput struct {
	CharacterID      string
	RenderMode       RenderMode
	SceneVariantText string
}

type ComposeOutput struct {
	CharacterCorePrompt string     `json:"character_core_prompt"`
	RenderProfilePrompt string     `json:"render_profile_prompt"`
	SceneVariantPrompt  string     `json:"scene_variant_prompt"`
	FinalPrompt         string     `json:"final_prompt"`
	RenderMode          RenderMode `json:"-"`
}

type HistoryEntry struct {
	ID                    string
	CreatedAt             time.Time
	CharacterID           string
	RenderMode            RenderMode
	SceneVariantText      string
	SceneVariantEffective string
	FinalPrompt           string
	Profile               string
	ImageID               string
	ImageSHA256           string
	ImageSizeBytes        int64
	ImageContentType      string
	ImageWidth            int
	ImageHeight           int
	CatalogRevision       string
}

func LoadEmbeddedCatalog() (Catalog, error) {
	raw, err := catalogFS.ReadFile("catalog.json")
	if err != nil {
		return Catalog{}, fmt.Errorf("read embedded CharImage catalog: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var catalog Catalog
	if err := decoder.Decode(&catalog); err != nil {
		return Catalog{}, fmt.Errorf("decode embedded CharImage catalog: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Catalog{}, fmt.Errorf("embedded CharImage catalog has trailing JSON")
	}
	if err := ValidateCatalog(catalog); err != nil {
		return Catalog{}, err
	}
	digest := sha256.Sum256(raw)
	catalog.Revision = "sha256:" + hex.EncodeToString(digest[:])
	return catalog, nil
}

func ValidateCatalog(catalog Catalog) error {
	if strings.TrimSpace(catalog.SchemaVersion) == "" || catalog.Model != ModelProfileID ||
		strings.TrimSpace(catalog.SourceModel) == "" || strings.TrimSpace(catalog.Scope) == "" ||
		strings.TrimSpace(catalog.PromptFormat) == "" {
		return fmt.Errorf("CharImage catalog metadata is incomplete")
	}
	if len(catalog.RenderModes) != 2 || catalog.RenderModes[0] != RenderMode2D || catalog.RenderModes[1] != RenderMode3D {
		return fmt.Errorf("CharImage catalog render modes must be 2d and 3d")
	}
	if len(catalog.Characters) != CatalogSize {
		return fmt.Errorf("CharImage catalog must contain exactly %d characters", CatalogSize)
	}
	ids := make(map[string]struct{}, len(catalog.Characters))
	statusCounts := map[string]int{}
	for i, character := range catalog.Characters {
		if !characterIDPattern.MatchString(character.ID) {
			return fmt.Errorf("CharImage catalog character %d has an invalid id", i)
		}
		if _, exists := ids[character.ID]; exists {
			return fmt.Errorf("CharImage catalog has duplicate character id %q", character.ID)
		}
		ids[character.ID] = struct{}{}
		if character.Status != StatusExistingConceptUnconfirmed && character.Status != StatusCandidateUnapproved {
			return fmt.Errorf("CharImage catalog character %q has an unsupported status", character.ID)
		}
		statusCounts[character.Status]++
		if strings.TrimSpace(character.NameJA) == "" || strings.TrimSpace(character.NameEN) == "" ||
			strings.TrimSpace(character.TaglineJA) == "" || strings.TrimSpace(character.Fixed.CharacterCoreEN) == "" ||
			strings.TrimSpace(character.Fixed.Render2DEN) == "" || strings.TrimSpace(character.Fixed.Render3DEN) == "" ||
			strings.TrimSpace(character.DefaultSceneEN) == "" {
			return fmt.Errorf("CharImage catalog character %q is missing a required field", character.ID)
		}
	}
	if statusCounts[StatusExistingConceptUnconfirmed] != 20 || statusCounts[StatusCandidateUnapproved] != 2 {
		return fmt.Errorf("CharImage catalog statuses must contain 20 unconfirmed concepts and 2 unapproved candidates")
	}
	return nil
}

func (catalog Catalog) Character(id string) (Character, bool) {
	for _, character := range catalog.Characters {
		if character.ID == id {
			return character, true
		}
	}
	return Character{}, false
}

func (catalog Catalog) Compose(input ComposeInput) (ComposeOutput, error) {
	character, ok := catalog.Character(input.CharacterID)
	if !ok {
		return ComposeOutput{}, fmt.Errorf("unknown CharImage character")
	}
	if input.RenderMode != RenderMode2D && input.RenderMode != RenderMode3D {
		return ComposeOutput{}, fmt.Errorf("unsupported CharImage render mode")
	}
	if !utf8.ValidString(input.SceneVariantText) || len(input.SceneVariantText) > MaxSceneBytes {
		return ComposeOutput{}, fmt.Errorf("CharImage scene variant is invalid or too long")
	}
	for _, r := range input.SceneVariantText {
		if (r < 0x20 && r != '\n' && r != '\r' && r != '\t') || r == 0x7f {
			return ComposeOutput{}, fmt.Errorf("CharImage scene variant contains a control character")
		}
	}
	scene := strings.TrimSpace(input.SceneVariantText)
	if scene == "" {
		scene = strings.TrimSpace(character.DefaultSceneEN)
	}
	profile := strings.TrimSpace(character.Fixed.Render2DEN)
	tail := render2DTail
	if input.RenderMode == RenderMode3D {
		profile = strings.TrimSpace(character.Fixed.Render3DEN)
		tail = render3DTail
	}
	core := strings.TrimSpace(character.Fixed.CharacterCoreEN)
	return ComposeOutput{
		CharacterCorePrompt: core,
		RenderProfilePrompt: profile,
		SceneVariantPrompt:  scene,
		FinalPrompt:         strings.Join([]string{core, scene, profile, tail}, " "),
		RenderMode:          input.RenderMode,
	}, nil
}
