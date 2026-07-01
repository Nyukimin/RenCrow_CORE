package main

import (
	"net/http"
	"time"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

func handleModuleTTSDiagnostics(provider moduletts.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		if provider == nil {
			http.Error(w, moduletts.DiagnosticsProviderUnavailableMessage, http.StatusServiceUnavailable)
			return
		}
		now := time.Now().UTC()
		_ = modulecore.WriteJSON(w, moduletts.BuildDiagnosticsSnapshot(r.Context(), provider, now))
	}
}
