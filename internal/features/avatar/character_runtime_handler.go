package avatar

import (
	"encoding/json"
	"net/http"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/characterruntime"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func HandleCharacterRuntime(service *characterruntime.Service, listener orchestrator.EventListener) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			writeJSON(w, http.StatusOK, map[string]any{"characters": characterruntime.DefaultCharacters()})
			return
		case http.MethodPost:
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if service == nil {
			http.Error(w, "character runtime unavailable", http.StatusServiceUnavailable)
			return
		}
		if listener == nil {
			http.Error(w, "character runtime event publication unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req characterruntime.RunRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid character runtime payload", http.StatusBadRequest)
			return
		}
		result, err := service.RunRound(r.Context(), req)
		if err != nil {
			http.Error(w, "character runtime failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		for _, turn := range result.Turns {
			ev := orchestrator.NewEvent("character_runtime.turn", turn.CharacterID, "viewer", turn.Content, "CHARACTER_RUNTIME", "", result.SessionID, "viewer", req.RequestedBy)
			ev.TurnIndex = turn.TurnIndex
			ev.MessageID = turn.MessageID
			ev.TraceID = result.TraceID
			if err := listener.OnEvent(ev); err != nil {
				http.Error(w, "character runtime event publication failed", http.StatusServiceUnavailable)
				return
			}
		}
		writeJSON(w, http.StatusCreated, map[string]any{"result": result})
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
