package viewer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// CharacterManifest represents the character parts manifest structure
type CharacterManifest struct {
	CharacterID string            `json:"character_id"`
	MasterImage string            `json:"master_image"`
	Version     int               `json:"version"`
	Stage       int               `json:"stage"`
	ImageSize   ImageSize         `json:"image_size"`
	Body        map[string]string `json:"body"`
	Expressions map[string]string `json:"expressions"`
	Mouth       map[string]string `json:"mouth"`
	MouthAnchor Anchor            `json:"mouth_anchor"`
	StateMap    map[string]State  `json:"state_map"`
	Notes       []string          `json:"notes"`
}

type ImageSize struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type Anchor struct {
	X    int    `json:"x"`
	Y    int    `json:"y"`
	Note string `json:"note"`
}

type State struct {
	Expression string `json:"expression"`
	Body       string `json:"body"`
}

// CharacterManager manages character manifests and provides expression URLs
type CharacterManager struct {
	mu        sync.RWMutex
	manifests map[string]*CharacterManifest
	basePath  string
}

var (
	globalCharacterManager *CharacterManager
	characterManagerOnce   sync.Once
)

// GetCharacterManager returns the singleton CharacterManager instance
func GetCharacterManager() *CharacterManager {
	characterManagerOnce.Do(func() {
		globalCharacterManager = &CharacterManager{
			manifests: make(map[string]*CharacterManifest),
			basePath:  "internal/adapter/viewer/assets/images",
		}
		// Load manifests for mio and shiro
		_ = globalCharacterManager.LoadManifest("mio")
		_ = globalCharacterManager.LoadManifest("shiro")
	})
	return globalCharacterManager
}

// LoadManifest loads a character manifest from disk
func (cm *CharacterManager) LoadManifest(characterID string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	manifestPath := filepath.Join(cm.basePath, characterID, "parts", "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return fmt.Errorf("failed to read manifest for %s: %w", characterID, err)
	}

	var manifest CharacterManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return fmt.Errorf("failed to parse manifest for %s: %w", characterID, err)
	}

	cm.manifests[characterID] = &manifest
	return nil
}

// GetExpressionURL returns the URL for a character's expression based on state
func (cm *CharacterManager) GetExpressionURL(characterID, stateName string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	manifest, ok := cm.manifests[characterID]
	if !ok {
		return ""
	}

	// Get state mapping
	state, ok := manifest.StateMap[stateName]
	if !ok {
		// Default to normal if state not found
		state, ok = manifest.StateMap["idle"]
		if !ok {
			return ""
		}
	}

	// Get expression path
	expressionPath, ok := manifest.Expressions[state.Expression]
	if !ok {
		return ""
	}

	// Return URL path
	return fmt.Sprintf("/viewer/assets/images/%s/parts/%s", characterID, expressionPath)
}

// GetBodyURL returns the URL for a character's body position based on state
func (cm *CharacterManager) GetBodyURL(characterID, stateName string) string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	manifest, ok := cm.manifests[characterID]
	if !ok {
		return ""
	}

	// Get state mapping
	state, ok := manifest.StateMap[stateName]
	if !ok {
		// Default to idle if state not found
		state, ok = manifest.StateMap["idle"]
		if !ok {
			return ""
		}
	}

	// Get body path
	bodyPath, ok := manifest.Body[state.Body]
	if !ok {
		return ""
	}

	// Return URL path
	return fmt.Sprintf("/viewer/assets/images/%s/parts/%s", characterID, bodyPath)
}

// GetAvailableStates returns all available states for a character
func (cm *CharacterManager) GetAvailableStates(characterID string) []string {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	manifest, ok := cm.manifests[characterID]
	if !ok {
		return nil
	}

	states := make([]string, 0, len(manifest.StateMap))
	for state := range manifest.StateMap {
		states = append(states, state)
	}
	return states
}

// CharacterStateResponse represents the API response for character state
type CharacterStateResponse struct {
	CharacterID     string   `json:"character_id"`
	State           string   `json:"state"`
	ExpressionURL   string   `json:"expression_url"`
	BodyURL         string   `json:"body_url"`
	TalkOpenURL     string   `json:"talk_open_url"`
	TalkClosedURL   string   `json:"talk_closed_url"`
	AvailableStates []string `json:"available_states"`
}
