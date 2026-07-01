package voice

import (
	"context"
	"net/http"

	sttfeature "github.com/Nyukimin/picoclaw_multiLLM/internal/features/stt"
	ttsfeature "github.com/Nyukimin/picoclaw_multiLLM/internal/features/tts"
	modulevoicechat "github.com/Nyukimin/picoclaw_multiLLM/modules/voicechat"
)

// Dependencies groups feature dependencies supplied by cmd/picoclaw.
type Dependencies struct {
	Ports  Ports
	Routes Routes
	STT    sttfeature.Dependencies
	TTS    ttsfeature.Dependencies
}

// Routes groups Voice/Audio route handlers supplied by cmd/picoclaw.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	VoiceChat         http.Handler
	AudioRouterEvents http.HandlerFunc
	ActiveControl     http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
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
