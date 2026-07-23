package orchestrator

import (
	"context"
	"fmt"
	"log"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type distributedTTSLifecycle struct {
	ttsBridge    TTSBridge
	vtuberBridge VTuberBridge
	emit         messageEventEmitter
}

func newDistributedTTSLifecycle(ttsBridge TTSBridge, vtuberBridge VTuberBridge, emit messageEventEmitter) *distributedTTSLifecycle {
	return &distributedTTSLifecycle{
		ttsBridge:    ttsBridge,
		vtuberBridge: vtuberBridge,
		emit:         emit,
	}
}

func (l *distributedTTSLifecycle) SetTTSBridge(ttsBridge TTSBridge) {
	l.ttsBridge = ttsBridge
}

func (l *distributedTTSLifecycle) SetVTuberBridge(vtuberBridge VTuberBridge) {
	l.vtuberBridge = vtuberBridge
}

func (l *distributedTTSLifecycle) StartSessionForRoute(ctx context.Context, req ProcessMessageRequest, jobID task.JobID, decision routing.Decision) string {
	if l.ttsBridge == nil || !ttsAllowedForOperationSource(req.OperationSource) {
		return ""
	}
	ttsSessionID := fmt.Sprintf("%s-%s", req.SessionID, jobID.String())
	plan, ok := moduletts.BuildRouteTTSPlan(moduletts.RouteTTSPlanInput{
		Route:      string(decision.Route),
		SessionID:  ttsSessionID,
		ResponseID: jobID.String(),
		Urgency:    "normal",
	})
	if !ok {
		return ""
	}
	startReq := TTSSessionStart{
		SessionID:             plan.SessionID,
		ResponseID:            plan.ResponseID,
		CharacterID:           plan.CharacterID,
		VoiceID:               plan.VoiceID,
		SpeechMode:            plan.SpeechMode,
		Event:                 plan.Event,
		Urgency:               plan.Urgency,
		ConversationMode:      plan.ConversationMode,
		UserAttentionRequired: plan.UserAttentionRequired,
		Context:               emotionContextFromRouteTTS(plan.Context),
		VoiceProfile:          plan.VoiceProfile,
	}
	if err := l.ttsBridge.StartSession(ctx, startReq); err != nil {
		log.Printf("[DistributedOrch] TTS start degraded: %v", err)
		return ""
	}
	return ttsSessionID
}

func (l *distributedTTSLifecycle) EndSession(ctx context.Context, ttsSessionID string) {
	if l.ttsBridge == nil || ttsSessionID == "" {
		return
	}
	if err := l.ttsBridge.EndSession(ctx, ttsSessionID); err != nil {
		log.Printf("[DistributedOrch] TTS end degraded: %v", err)
	}
}

func (l *distributedTTSLifecycle) WithStreamHooks(
	ctx context.Context,
	route routing.Route,
	jid, sessionID, channel, chatID, ttsSessionID string,
) (context.Context, *streamBundle) {
	prev := llm.StreamCallbackFromContext(ctx)
	latency := latencyTraceFromContext(ctx)
	ttsStream := newTTSStreamForwarder(l.ttsBridge, ttsSessionID, route, "agent.response", "[DistributedOrch] TTS push degraded:")
	vtuberStream := newVTuberStreamForwarder(l.vtuberBridge, ttsSessionID, route, "agent.response", "[DistributedOrch] VTuber push degraded:")
	return llm.ContextWithStreamCallback(ctx, func(token string) {
		if prev != nil {
			prev(token)
		}
		if latency != nil && latency.markFirstToken() {
			emitLatencyMetric(l.emit, "llm", "first_token", latency.startedAt, string(route), jid, sessionID, channel, chatID, "")
		}
		l.emit("agent.thinking", "mio", "user", token, string(route), jid, sessionID, channel, chatID)
		ttsStream.OnToken(ctx, token)
		vtuberStream.OnToken(ctx, token)
	}), &streamBundle{tts: ttsStream, vtuber: vtuberStream}
}

func (l *distributedTTSLifecycle) Push(ctx context.Context, sessionID string, route routing.Route, eventType, text string) {
	ttsCtx := buildTTSContext(route, "normal", false)
	_, voiceProfile := voiceForSpeaker(speakerForRoute(route))
	pushTTSTextChunks(ctx, l.ttsBridge, sessionID, route, eventType, text, ttsCtx, voiceProfile, "[DistributedOrch] TTS push degraded:")
	req, ok := buildVTuberRequest(eventType, route, sessionID, text, ttsCtx, voiceProfile)
	if ok {
		pushVTuber(ctx, l.vtuberBridge, req, "[DistributedOrch] VTuber push degraded:")
	}
}
