package viewer

import (
	"encoding/json"
	"fmt"
	"net/http"
)

// HandleCharacterState returns character state information including expression and body URLs
func HandleCharacterState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	characterID := r.URL.Query().Get("character_id")
	if characterID == "" {
		http.Error(w, "character_id is required", http.StatusBadRequest)
		return
	}

	stateName := r.URL.Query().Get("state")
	if stateName == "" {
		stateName = "idle" // default to idle
	}

	cm := GetCharacterManager()
	expressionURL := cm.GetExpressionURL(characterID, stateName)
	bodyURL := cm.GetBodyURL(characterID, stateName)

	// Get talk open and closed URLs for lipsync
	talkOpenURL := cm.GetExpressionURL(characterID, "speaking")

	// For talk_closed, use the current state's expression
	talkClosedURL := expressionURL

	availableStates := cm.GetAvailableStates(characterID)

	response := CharacterStateResponse{
		CharacterID:     characterID,
		State:           stateName,
		ExpressionURL:   expressionURL,
		BodyURL:         bodyURL,
		TalkOpenURL:     talkOpenURL,
		TalkClosedURL:   talkClosedURL,
		AvailableStates: availableStates,
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(response); err != nil {
		http.Error(w, fmt.Sprintf("Failed to encode response: %v", err), http.StatusInternalServerError)
		return
	}
}

// HandleCharacterManifest returns the full manifest for a character
func HandleCharacterManifest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	characterID := r.URL.Query().Get("character_id")
	if characterID == "" {
		http.Error(w, "character_id is required", http.StatusBadRequest)
		return
	}

	cm := GetCharacterManager()
	cm.mu.RLock()
	manifest, ok := cm.manifests[characterID]
	cm.mu.RUnlock()

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
