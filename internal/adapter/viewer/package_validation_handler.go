package viewer

import (
	"encoding/json"
	"net/http"

	packagevalidationapp "github.com/Nyukimin/RenCrow_CORE/internal/application/packagevalidation"
)

func HandlePackageValidation(service *packagevalidationapp.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if service == nil {
			http.Error(w, "package validation service unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req packagevalidationapp.ValidationRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid package validation payload", http.StatusBadRequest)
			return
		}
		report, err := service.ValidateUpdate(r.Context(), req)
		if err != nil {
			http.Error(w, "package validation failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"report": report})
	}
}
