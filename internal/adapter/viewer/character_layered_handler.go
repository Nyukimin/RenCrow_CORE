package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HandleLayeredCharacterState returns layered character state with individual parts
func HandleLayeredCharacterState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	characterID := r.URL.Query().Get("character_id")
	if characterID == "" {
		http.Error(w, "character_id is required", http.StatusBadRequest)
		return
	}

	expression := r.URL.Query().Get("expression")
	if expression == "" {
		expression = "normal" // default to normal
	}

	lcm := GetLayeredCharacterManager()
	state, err := lcm.GetCharacterState(characterID, expression)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to get character state: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(state); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
}

// HandleLayeredCharacterMouth updates only the mouth part
func HandleLayeredCharacterMouth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	characterID := r.URL.Query().Get("character_id")
	if characterID == "" {
		http.Error(w, "character_id is required", http.StatusBadRequest)
		return
	}

	expression := r.URL.Query().Get("expression")
	if expression == "" {
		expression = "normal"
	}

	mouthID := r.URL.Query().Get("mouth_id")
	if mouthID == "" {
		http.Error(w, "mouth_id is required", http.StatusBadRequest)
		return
	}

	lcm := GetLayeredCharacterManager()
	state, err := lcm.SetMouthPart(characterID, expression, mouthID)
	if err != nil {
		http.Error(w, fmt.Sprintf("Failed to set mouth part: %v", err), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(state); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
}

// HandleLayeredCharacterManifest returns the full layered manifest
func HandleLayeredCharacterManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	characterID := r.URL.Query().Get("character_id")
	if characterID == "" {
		http.Error(w, "character_id is required", http.StatusBadRequest)
		return
	}

	lcm := GetLayeredCharacterManager()
	lcm.mu.RLock()
	manifest, ok := lcm.manifests[characterID]
	lcm.mu.RUnlock()

	if !ok {
		http.Error(w, fmt.Sprintf("Character %s not found", characterID), http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(manifest); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode manifest: %v", err), http.StatusInternalServerError)
		return
	}
}
