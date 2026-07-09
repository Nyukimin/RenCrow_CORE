package viewer

import (
	"encoding/json"
	"net/http"

	otelexportapp "github.com/Nyukimin/RenCrow_CORE/internal/application/otelexport"
)

func HandleOTelExport(service *otelexportapp.Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if service == nil {
			http.Error(w, "otel export service unavailable", http.StatusServiceUnavailable)
			return
		}
		defer r.Body.Close()
		var req otelexportapp.ExportRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid otel export payload", http.StatusBadRequest)
			return
		}
		report, err := service.Export(r.Context(), req)
		if err != nil {
			http.Error(w, "otel export failed: "+err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"report": report})
	}
}
