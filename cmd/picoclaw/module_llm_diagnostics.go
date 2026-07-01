package main

import (
	"context"
	"net/http"
	"time"

	modulecore "github.com/Nyukimin/picoclaw_multiLLM/modules/core"
	modulellm "github.com/Nyukimin/picoclaw_multiLLM/modules/llm"
)

func handleModuleLLMDiagnostics(providers map[string]modulellm.Provider) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !modulecore.RequireHTTPMethod(w, r, http.MethodGet) {
			return
		}
		now := time.Now().UTC()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		_ = modulecore.WriteJSON(w, modulellm.BuildDiagnosticsSnapshot(ctx, providers, now))
	}
}
