package orchestrator

import (
	"context"
	"strings"
	"sync"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type vtuberStreamForwarder struct {
	bridge       VTuberBridge
	ctx          context.Context
	cancel       context.CancelFunc
	sessionID    string
	route        routing.Route
	speaker      string
	eventType    string
	ttsCtx       moduletts.EmotionContext
	voiceProfile string
	logPrefix    string
	chunker      moduletts.StreamChunker
	mu           sync.Mutex
	closed       bool
	aborted      bool
}

func newVTuberStreamForwarder(bridge VTuberBridge, sessionID string, route routing.Route, speaker, eventType, logPrefix string) *vtuberStreamForwarder {
	if bridge == nil || strings.TrimSpace(sessionID) == "" {
		return nil
	}
	voiceProfile, ok := voiceProfileForSpeaker(speaker, logPrefix)
	if !ok {
		return nil
	}
	forwarderCtx, cancel := context.WithCancel(context.Background())
	return &vtuberStreamForwarder{
		bridge:       bridge,
		ctx:          forwarderCtx,
		cancel:       cancel,
		sessionID:    sessionID,
		route:        route,
		speaker:      speaker,
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
	f.mu.Lock()
	if f.closed || f.aborted {
		f.mu.Unlock()
		return
	}
	chunks := f.chunker.AcceptToken(token)
	f.mu.Unlock()
	for _, chunk := range chunks {
		f.emit(ctx, chunk)
	}
}

func (f *vtuberStreamForwarder) Finalize(ctx context.Context, finalText string) {
	if f == nil {
		return
	}
	f.mu.Lock()
	if f.closed || f.aborted {
		f.mu.Unlock()
		return
	}
	f.closed = true
	chunks := f.chunker.FinalizeOne(finalText)
	f.mu.Unlock()
	for _, chunk := range chunks {
		f.emit(ctx, chunk)
	}
}

func (f *vtuberStreamForwarder) emit(ctx context.Context, text string) {
	if f.ctx.Err() != nil {
		return
	}
	req, ok := buildVTuberRequest(f.eventType, f.route, f.speaker, f.sessionID, text, f.ttsCtx, f.voiceProfile)
	if !ok {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	bridgeCtx, cancel := context.WithCancel(ctx)
	stopAbort := context.AfterFunc(f.ctx, cancel)
	defer func() {
		stopAbort()
		cancel()
	}()
	if f.ctx.Err() != nil || bridgeCtx.Err() != nil {
		return
	}
	pushVTuber(bridgeCtx, f.bridge, req, f.logPrefix)
}

func (f *vtuberStreamForwarder) Abort() {
	if f == nil {
		return
	}
	f.mu.Lock()
	if !f.aborted {
		f.aborted = true
		f.closed = true
		f.chunker = moduletts.StreamChunker{}
		f.cancel()
	}
	f.mu.Unlock()
}
