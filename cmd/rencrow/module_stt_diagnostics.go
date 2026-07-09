package main

import (
	"net/http"
	"time"

	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

func handleModuleSTTDiagnostics(provider modulestt.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		if provider == nil {
			http.Error(w, modulestt.DiagnosticsProviderUnavailableMessage, http.StatusServiceUnavailable)
			return
		}
		now := time.Now().UTC()
		_ = modulecore.WriteJSON(w, modulestt.BuildDiagnosticsSnapshot(r.Context(), provider, now))
	}
}
