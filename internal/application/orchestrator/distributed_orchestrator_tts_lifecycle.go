package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
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

// StartSessionForRoute starts the turn's TTS session with the voice of the Agent
// that responds on the route (CHAT: the viewer recipient) and returns its ID.
// An empty ID means no session is open and nothing may be pushed.
func (l *distributedTTSLifecycle) StartSessionForRoute(ctx context.Context, req ProcessMessageRequest, taskID modulecore.TaskID, decision routing.Decision) string {
	return l.StartSessionForResponder(ctx, req, taskID, decision, chatTTSCharacterForRequest(req, decision.Route))
}

// StartSessionForResponder is StartSessionForRoute for a CHAT turn whose
// responding Agent is known explicitly (and may differ from the viewer
// recipient, e.g. the daily news brief). responder is ignored for other routes.
func (l *distributedTTSLifecycle) StartSessionForResponder(ctx context.Context, req ProcessMessageRequest, taskID modulecore.TaskID, decision routing.Decision, responder string) string {
	if l.ttsBridge == nil || !ttsAllowedForRequest(req) || validateDistributedTaskID(taskID) != nil {
		return ""
	}
	ttsSessionID := fmt.Sprintf("%s-%s", req.SessionID, taskID.String())
	plan, ok := moduletts.BuildRouteTTSPlan(moduletts.RouteTTSPlanInput{
		Route:             string(decision.Route),
		SessionID:         ttsSessionID,
		PublicPlaybackRef: taskID.String(),
		ChatCharacterID:   responder,
		Urgency:           "normal",
	})
	if !ok {
		log.Printf("[DistributedOrch] TTS session not started: unresolved speaker route=%s recipient=%q task_id=%s", decision.Route, responder, taskID.String())
		return ""
	}
	startReq := TTSSessionStart{
		SessionID:             plan.SessionID,
		PublicPlaybackRef:     plan.PublicPlaybackRef,
		TraceID:               req.TraceID,
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
	speaker, taskIDText, sessionID, channel, chatID, ttsSessionID string,
) (context.Context, *streamBundle) {
	prev := llm.StreamCallbackFromContext(ctx)
	latency := latencyTraceFromContext(ctx)
	ttsStream := newTTSStreamForwarder(l.ttsBridge, ttsSessionID, route, speaker, "agent.response", "[DistributedOrch] TTS push degraded:")
	vtuberStream := newVTuberStreamForwarder(l.vtuberBridge, ttsSessionID, route, speaker, "agent.response", "[DistributedOrch] VTuber push degraded:")
	return llm.ContextWithStreamCallback(ctx, func(token string) {
		if strings.TrimSpace(token) != "" {
			turntiming.Mark(ctx, turntiming.PointFirstTextCallback)
		}
		if prev != nil {
			prev(token)
		}
		if latency != nil && latency.markFirstToken() {
			emitLatencyMetric(l.emit, "llm", "first_token", latency.startedAt, string(route), taskIDText, sessionID, channel, chatID, "")
		}
		ttsStream.OnToken(ctx, token)
		vtuberStream.OnToken(ctx, token)
	}), &streamBundle{tts: ttsStream, vtuber: vtuberStream}
}

// Push voices one finished utterance of speaker (the Agent that produced the
// text, equal to the turn's agent.response from) on the open session.
func (l *distributedTTSLifecycle) Push(ctx context.Context, sessionID string, route routing.Route, speaker, eventType, text string) {
	voiceProfile, ok := voiceProfileForSpeaker(speaker, "[DistributedOrch] TTS push degraded:")
	if !ok {
		return
	}
	ttsCtx := buildTTSContext(route, "normal", false)
	pushTTSTextChunks(ctx, l.ttsBridge, sessionID, route, eventType, text, ttsCtx, voiceProfile, "[DistributedOrch] TTS push degraded:")
	req, ok := buildVTuberRequest(eventType, route, speaker, sessionID, text, ttsCtx, voiceProfile)
	if ok {
		pushVTuber(ctx, l.vtuberBridge, req, "[DistributedOrch] VTuber push degraded:")
	}
}
