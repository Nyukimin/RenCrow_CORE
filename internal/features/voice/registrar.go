package voice

import (
	"context"
	"net/http"

	sttfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/stt"
	ttsfeature "github.com/Nyukimin/RenCrow_CORE/internal/features/tts"
	modulevoicechat "github.com/Nyukimin/RenCrow_CORE/modules/voicechat"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
	STT    sttfeature.Dependencies
	TTS    ttsfeature.Dependencies
}

// Routes groups Voice/Audio route handlers supplied by cmd/rencrow.
// Handler implementations are supplied by their owning adapter and command packages.
type Routes struct {
	VoiceChat         http.Handler
	AudioRouterEvents http.HandlerFunc
	ActiveControl     http.HandlerFunc
}

// RegisterRoutes registers handlers at the feature route boundary.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	sttfeature.RegisterRoutes(mux, deps.STT)
	ttsfeature.RegisterRoutes(mux, deps.TTS)

	routes := deps.Routes
	for _, path := range modulevoicechat.WebSocketRoutePaths {
		registerHandler(mux, path, routes.VoiceChat)
	}
	registerRoute(mux, "/viewer/active-control", routes.ActiveControl)
	registerRoute(mux, "/audio-router/events", routes.AudioRouterEvents)
}

// StartBackground reserves the feature background-job boundary.
func StartBackground(ctx context.Context, deps Dependencies) error {
	_ = ctx
	_ = deps
	return nil
}

func registerRoute(mux *http.ServeMux, pattern string, handler http.HandlerFunc) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.HandleFunc(pattern, handler)
}

func registerHandler(mux *http.ServeMux, pattern string, handler http.Handler) {
	if mux == nil || pattern == "" || handler == nil {
		return
	}
	mux.Handle(pattern, handler)
}
