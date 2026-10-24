package main

import (
	"context"
	"net/http"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/security"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	modulellm "github.com/Nyukimin/RenCrow_CORE/modules/llm"
)

func handleModuleLLMDiagnostics(providers map[string]modulellm.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		now := time.Now().UTC()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		snapshot := modulellm.BuildDiagnosticsSnapshot(ctx, providers, now)
		if !security.IsDirectLoopbackRequest(r) {
			for i := range snapshot.Roles {
				snapshot.Roles[i].GatewayRequestWrite = nil
			}
		}
		_ = modulecore.WriteJSON(w, snapshot)
	}
}
