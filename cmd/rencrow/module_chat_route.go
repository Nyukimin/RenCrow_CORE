package main

import (
	"encoding/json"
	"net/http"
	"time"

	modulechat "github.com/Nyukimin/RenCrow_CORE/modules/chat"
	"github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func handleModuleChatRouteDecision(service chatModuleService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !core.RequireHTTPMethod(w, r, http.MethodPost) {
			return
		}
		if service == nil {
			http.Error(w, modulechat.RouteServiceUnavailableMessage, http.StatusServiceUnavailable)
			return
		}
		var input modulechat.Input
		if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		decision, err := service.DecideRoute(r.Context(), input)
		if err != nil {
			http.Error(w, "route decision failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		_ = core.WriteJSON(w, modulechat.BuildRouteReport(r.Context(), service, input, decision, time.Now().UTC()))
	}
}
