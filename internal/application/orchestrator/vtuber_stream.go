package orchestrator

import (
	"context"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type vtuberStreamForwarder struct {
	bridge       VTuberBridge
	sessionID    string
	route        routing.Route
	eventType    string
	ttsCtx       moduletts.EmotionContext
	voiceProfile string
	logPrefix    string
	chunker      moduletts.StreamChunker
}

func newVTuberStreamForwarder(bridge VTuberBridge, sessionID string, route routing.Route, eventType, logPrefix string) *vtuberStreamForwarder {
	if bridge == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	_, voiceProfile := voiceForSpeaker(speakerForRoute(route))
	return &vtuberStreamForwarder{
		bridge:       bridge,
		sessionID:    sessionID,
		route:        route,
		eventType:    eventType,
		ttsCtx:       buildTTSContext(route, "normal", false),
		voiceProfile: voiceProfile,
		logPrefix:    logPrefix,
	}
}

func (f *vtuberStreamForwarder) OnToken(ctx context.Context, token string) {
	if f == nil || token == "" {
		return
	}
	for _, chunk := range f.chunker.AcceptToken(token) {
		f.emit(ctx, chunk)
	}
}

func (f *vtuberStreamForwarder) Finalize(ctx context.Context, finalText string) {
	if f == nil {
		return
	}
	for _, chunk := range f.chunker.FinalizeOne(finalText) {
		f.emit(ctx, chunk)
	}
}

func (f *vtuberStreamForwarder) emit(ctx context.Context, text string) {
	req, ok := buildVTuberRequest(f.eventType, f.route, f.sessionID, text, f.ttsCtx, f.voiceProfile)
	if !ok {
		return
	}
	pushVTuber(ctx, f.bridge, req, f.logPrefix)
}
