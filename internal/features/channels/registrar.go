package channels

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups channel and entry route handlers supplied by cmd/picoclaw.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Webhook            http.Handler
	TelegramWebhook    http.Handler
	DiscordWebhook     http.Handler
	SlackWebhook       http.Handler
	Entry              http.HandlerFunc
	ChromeBridge       http.HandlerFunc
	ChromeBridgeStatus http.HandlerFunc
	ChromeBridgeEvents http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerHandlerOrUnavailable(mux, "/webhook", routes.Webhook, "line webhook unavailable")
	registerHandlerOrUnavailable(mux, "/webhook/telegram", routes.TelegramWebhook, "telegram webhook unavailable")
	registerHandlerOrUnavailable(mux, "/webhook/discord", routes.DiscordWebhook, "discord webhook unavailable")
	registerHandlerOrUnavailable(mux, "/webhook/slack", routes.SlackWebhook, "slack webhook unavailable")
	registerRoute(mux, "/entry", routes.Entry)
	registerRoute(mux, "/chrome/bridge", routes.ChromeBridge)
	registerRoute(mux, "/chrome/bridge/status", routes.ChromeBridgeStatus)
	registerRoute(mux, "/chrome/bridge/events", routes.ChromeBridgeEvents)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}

func registerHandler(mux *http.ServeMux, pattern string, handler http.Handler) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.Handle(pattern, handler)
}

func registerHandlerOrUnavailable(mux *http.ServeMux, pattern string, handler http.Handler, message string) {
	if mux == nil || pattern == "" {
		return
	}
	if handler != nil {
		mux.Handle(pattern, handler)
		return
	}
	mux.Handle(pattern, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, message, http.StatusServiceUnavailable)
	}))
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}
