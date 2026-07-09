package tts

import (
	"context"
	"net/http"
)

// Dependencies groups feature dependencies supplied by cmd/rencrow.
type Dependencies struct {
	Ports  Ports
	Routes Routes
}

// Routes groups TTS route handlers supplied by cmd/rencrow.
// Handler implementations stay in legacy adapter/cmd packages during Ver0.80
// migration; this registrar owns only route registration and dependency handoff.
type Routes struct {
	Audio       http.HandlerFunc
	PlaybackAck http.HandlerFunc
}

// RegisterRoutes reserves the feature route boundary. Existing routes remain in
// their legacy packages until a phase migrates them through this registrar.
func RegisterRoutes(mux *http.ServeMux, deps Dependencies) {
	routes := deps.Routes
	registerRoute(mux, "/viewer/tts/audio", routes.Audio)
	registerRoute(mux, "/viewer/tts/playback-ack", routes.PlaybackAck)
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
