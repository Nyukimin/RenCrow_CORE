package viewer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// LayeredCharacterManifest represents the generated parts manifest structure
type LayeredCharacterManifest struct {
	SourceDir      string                      `json:"source_dir"`
	CanvasSize     CanvasSize                  `json:"canvas_size"`
	Body           string                      `json:"body"`
	Eyebrows       []Part                      `json:"eyebrows"`
	Eyes           []Part                      `json:"eyes"`
	Mouth          []Part                      `json:"mouth"`
	SampleAnchors  map[string]Anchor           `json:"sample_anchors"`
	Expressions    map[string][]string         `json:"expressions"`
}

type CanvasSize struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type Part struct {
	ID        string    `json:"id"`
	FullPath  string    `json:"full_path"`
	CropPath  string    `json:"crop_path"`
	Bounds    Bounds    `json:"bounds"`
	SheetCell SheetCell `json:"sheet_cell"`
}

type Bounds struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

type SheetCell struct {
	Row int `json:"row"`
	Col int `json:"col"`
}

// LayeredCharacterParts represents the current parts for a character
type LayeredCharacterParts struct {
	Base         PartWithBounds `json:"base"`
	EyebrowLeft  PartWithBounds `json:"eyebrow_left"`
	EyebrowRight PartWithBounds `json:"eyebrow_right"`
	EyeLeft      PartWithBounds `json:"eye_left"`
	EyeRight     PartWithBounds `json:"eye_right"`
	Mouth        PartWithBounds `json:"mouth"`
}

// PartWithBounds represents a part URL with its position and size
type PartWithBounds struct {
	URL    string  `json:"url"`
	Bounds *Bounds `json:"bounds,omitempty"`
}

// LayeredCharacterState represents a complete character state
type LayeredCharacterState struct {
	CharacterID          string                `json:"character_id"`
	Expression           string                `json:"expression"`
	Parts                LayeredCharacterParts `json:"parts"`
	CanvasSize           CanvasSize            `json:"canvas_size"`
	Anchors              map[string]Anchor     `json:"anchors,omitempty"`
	AvailableExpressions []string              `json:"available_expressions"`
	AvailableMouths      []string              `json:"available_mouths"`
}

// LayeredCharacterManager manages layered character manifests
type LayeredCharacterManager struct {
	mu        sync.RWMutex
	manifests map[string]*LayeredCharacterManifest
	basePath  string
}

var (
	globalLayeredManager *LayeredCharacterManager
	layeredManagerOnce   sync.Once
)

// GetLayeredCharacterManager returns the singleton LayeredCharacterManager instance
func GetLayeredCharacterManager() *LayeredCharacterManager {
	layeredManagerOnce.Do(func() {
		globalLayeredManager = &LayeredCharacterManager{
			manifests: make(map[string]*LayeredCharacterManifest),
			basePath:  "internal/adapter/viewer/assets/images",
		}
		// Load layered manifests for mio
		_ = globalLayeredManager.LoadManifest("mio")
	})
	return globalLayeredManager
}

// LoadManifest loads a layered character manifest from disk
func (lcm *LayeredCharacterManager) LoadManifest(characterID string) error {
	lcm.mu.Lock()
	defer lcm.mu.Unlock()

	manifestPath := filepath.Join(lcm.basePath, characterID, "parts", "generated", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read layered manifest for %s: %w", characterID, err)
	}

	var manifest LayeredCharacterManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("failed to parse layered manifest for %s: %w", characterID, err)
	}

	lcm.manifests[characterID] = &manifest
	return nil
}

// GetCharacterState returns the complete character state for a given expression
func (lcm *LayeredCharacterManager) GetCharacterState(characterID, expressionName string) (*LayeredCharacterState, error) {
	lcm.mu.RLock()
	defer lcm.mu.RUnlock()

	manifest, ok := lcm.manifests[characterID]
	if !ok {
		return nil, fmt.Errorf("character %s not found", characterID)
	}

	// Get expression parts
	partIDs, ok := manifest.Expressions[expressionName]
	if !ok {
		// Default to normal
		partIDs, ok = manifest.Expressions["normal"]
		if !ok {
			return nil, fmt.Errorf("expression %s not found and no normal expression available", expressionName)
		}
		expressionName = "normal"
	}

	parts := LayeredCharacterParts{}

	// Set base body (no bounds needed, it's the full canvas)
	parts.Base = PartWithBounds{
		URL:    lcm.convertToViewerPath(manifest.Body),
		Bounds: nil,
	}

	// Parse parts from expression
	for _, partID := range partIDs {
		if part := lcm.findPart(manifest, partID); part != nil {
			url := lcm.convertToViewerPath(part.CropPath)
			pwb := PartWithBounds{
				URL:    url,
				Bounds: &part.Bounds,
			}

			// Categorize part by ID prefix
			if len(partID) >= 7 && partID[:7] == "eyebrow" {
				// eyebrow_01 = left, eyebrow_02 = right, etc.
				if part.SheetCell.Col == 1 {
					parts.EyebrowLeft = pwb
				} else if part.SheetCell.Col == 2 {
					parts.EyebrowRight = pwb
				}
			} else if len(partID) >= 4 && partID[:4] == "eyes" {
				// eyes_01 = left, eyes_02 = right, etc.
				if part.SheetCell.Col == 1 {
					parts.EyeLeft = pwb
				} else if part.SheetCell.Col == 2 {
					parts.EyeRight = pwb
				}
			} else if len(partID) >= 5 && partID[:5] == "mouth" {
				parts.Mouth = pwb
			}
		}
	}

	// Get available expressions
	availableExpressions := make([]string, 0, len(manifest.Expressions))
	for expr := range manifest.Expressions {
		availableExpressions = append(availableExpressions, expr)
	}

	// Get available mouth parts
	availableMouths := make([]string, 0, len(manifest.Mouth))
	for _, mouth := range manifest.Mouth {
		availableMouths = append(availableMouths, mouth.ID)
	}

	state := &LayeredCharacterState{
		CharacterID:          characterID,
		Expression:           expressionName,
		Parts:                parts,
		CanvasSize:           manifest.CanvasSize,
		Anchors:              manifest.SampleAnchors,
		AvailableExpressions: availableExpressions,
		AvailableMouths:      availableMouths,
	}

	return state, nil
}

// SetMouthPart updates only the mouth part while keeping other parts
func (lcm *LayeredCharacterManager) SetMouthPart(characterID, currentExpression, mouthID string) (*LayeredCharacterState, error) {
	// Get current state
	state, err := lcm.GetCharacterState(characterID, currentExpression)
	if err != nil {
		return nil, err
	}

	// Find and update mouth part
	lcm.mu.RLock()
	manifest, ok := lcm.manifests[characterID]
	lcm.mu.RUnlock()

	if !ok {
		return nil, fmt.Errorf("character %s not found", characterID)
	}

	if part := lcm.findPart(manifest, mouthID); part != nil {
		state.Parts.Mouth = PartWithBounds{
			URL:    lcm.convertToViewerPath(part.CropPath),
			Bounds: &part.Bounds,
		}
	}

	return state, nil
}

// findPart finds a part by ID across all part arrays
func (lcm *LayeredCharacterManager) findPart(manifest *LayeredCharacterManifest, partID string) *Part {
	// Search in eyebrows
	for i := range manifest.Eyebrows {
		if manifest.Eyebrows[i].ID == partID {
			return &manifest.Eyebrows[i]
		}
	}

	// Search in eyes
	for i := range manifest.Eyes {
		if manifest.Eyes[i].ID == partID {
			return &manifest.Eyes[i]
		}
	}

	// Search in mouth
	for i := range manifest.Mouth {
		if manifest.Mouth[i].ID == partID {
			return &manifest.Mouth[i]
		}
	}

	return nil
}

// convertToViewerPath converts internal path to viewer URL path
func (lcm *LayeredCharacterManager) convertToViewerPath(internalPath string) string {
	// Remove the "internal/adapter/viewer/assets/" prefix
	const prefix = "internal/adapter/viewer/assets/"
	if len(internalPath) > len(prefix) && internalPath[:len(prefix)] == prefix {
		return "/viewer/assets/" + internalPath[len(prefix):]
	}
	return internalPath
}

// GetAvailableMouthShapes returns mouth shapes suitable for lip-sync
func (lcm *LayeredCharacterManager) GetAvailableMouthShapes(characterID string) []string {
	lcm.mu.RLock()
	defer lcm.mu.RUnlock()

	manifest, ok := lcm.manifests[characterID]
	if !ok {
		return nil
	}

	shapes := make([]string, 0, len(manifest.Mouth))
	for _, mouth := range manifest.Mouth {
		shapes = append(shapes, mouth.ID)
	}
	return shapes
}
