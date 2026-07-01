package viewer

import (
	"encoding/json"
	"net/http"

	artifactcleanupapp "github.com/Nyukimin/picoclaw_multiLLM/internal/application/artifactcleanup"
)

func HandleArtifactCleanup(service *artifactcleanupapp.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if service == nil {
			http.Error(w, "artifact cleanup service unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req artifactcleanupapp.Request
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid artifact cleanup payload", http.StatusBadRequest)
			return
		}
		report, err := service.Cleanup(r.Context(), req)
		if err != nil {
			http.Error(w, "artifact cleanup failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"report": report})
	}
}
