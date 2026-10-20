package orchestrator

import (
	"context"
	"errors"
	"log"
	"strings"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/speech"
	"github.com/Nyukimin/RenCrow_CORE/internal/turntiming"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

type messageTTSLifecycle struct {
	ttsBridge    TTSBridge
	vtuberBridge VTuberBridge
	emit         messageEventEmitter
}

type speechPlanStreamModeKey struct{}

func withSpeechPlanStreamMode(ctx context.Context) context.Context {
	return context.WithValue(ctx, speechPlanStreamModeKey{}, true)
}

func speechPlanStreamModeFromContext(ctx context.Context) bool {
	enabled, _ := ctx.Value(speechPlanStreamModeKey{}).(bool)
	return enabled
}

func newMessageTTSLifecycle(ttsBridge TTSBridge, vtuberBridge VTuberBridge, emit messageEventEmitter) *messageTTSLifecycle {
	return &messageTTSLifecycle{
		ttsBridge:    ttsBridge,
		vtuberBridge: vtuberBridge,
		emit:         emit,
	}
}

func (l *messageTTSLifecycle) SetTTSBridge(ttsBridge TTSBridge) {
	l.ttsBridge = ttsBridge
}

func (l *messageTTSLifecycle) SetVTuberBridge(vtuberBridge VTuberBridge) {
	l.vtuberBridge = vtuberBridge
}

// PushSpeechPlan sends one complete CHAT SpeechPlan through the optional
// bridge seam while the caller's Action, Run, and receipt context remain live.
func (l *messageTTSLifecycle) PushSpeechPlan(ctx context.Context, ttsSessionID, conversationSessionID, turnID, speakerID string, plan speech.SpeechPlan) error {
	if ctx == nil {
		return errors.New("SpeechPlan context is required")
	}
	sessionID := strings.TrimSpace(conversationSessionID)
	turnID = strings.TrimSpace(turnID)
	if modulecore.SessionID(sessionID).Validate() != nil || modulecore.TurnID(turnID).Validate() != nil {
		return errors.New("SpeechPlan conversation session_id or turn_id is invalid")
	}
	pusher, ok := l.ttsBridge.(SpeechPlanTTSBridge)
	if !ok {
		return errors.New("TTS bridge does not support SpeechPlan synthesis")
	}
	speechCtx, done := SpeechSynthesisContext(ctx)
	defer done()
	return pusher.PushSpeechPlan(speechCtx, ttsSessionID, sessionID, turnID, speakerID, plan)
}

// StartSessionForRoute starts the turn's TTS session with the voice of the Agent
// that responds on the route (CHAT: the viewer recipient). It reports whether a
// session is open for ttsSessionID; false means nothing may be pushed to it.
func (l *messageTTSLifecycle) StartSessionForRoute(ctx context.Context, req ProcessMessageRequest, taskID modulecore.TaskID, decision routing.Decision, ttsSessionID string) bool {
	return l.StartSessionForResponder(ctx, req, taskID, decision, ttsSessionID, chatTTSCharacterForRequest(req, decision.Route))
}

// StartSessionForResponder is StartSessionForRoute for a CHAT turn whose
// responding Agent is known explicitly (and may differ from the viewer
// recipient, e.g. the daily news brief). responder is ignored for other routes.
func (l *messageTTSLifecycle) StartSessionForResponder(ctx context.Context, req ProcessMessageRequest, taskID modulecore.TaskID, decision routing.Decision, ttsSessionID, responder string) bool {
	if l.ttsBridge == nil || ttsSessionID == "" {
		return false
	}
	plan, ok := moduletts.BuildRouteTTSPlan(moduletts.RouteTTSPlanInput{
		Route:             string(decision.Route),
		SessionID:         ttsSessionID,
		PublicPlaybackRef: taskID.String(),
		ChatCharacterID:   responder,
		Urgency:           "normal",
	})
	if !ok {
		log.Printf("[MessageOrch] TTS session not started: unresolved speaker route=%s recipient=%q task_id=%s", decision.Route, responder, taskID.String())
		return false
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
		log.Printf("[MessageOrch] TTS route update degraded: %v", err)
		return false
	}
	return true
}

func chatTTSCharacterForRequest(req ProcessMessageRequest, route routing.Route) string {
	if route != routing.RouteCHAT {
		return ""
	}
	return normalizeProcessViewerRecipient(req.To)
}

func (l *messageTTSLifecycle) EndSession(ctx context.Context, ttsSessionID string) {
	if ttsSessionID == "" {
		return
	}
	if err := l.ttsBridge.EndSession(ctx, ttsSessionID); err != nil {
		log.Printf("[MessageOrch] TTS end degraded: %v", err)
	}
}

func (l *messageTTSLifecycle) WithStreamHooks(
	ctx context.Context,
	route routing.Route,
	speaker, taskID, sessionID, channel, chatID, ttsSessionID string,
) (context.Context, *streamBundle) {
	prev := llm.StreamCallbackFromContext(ctx)
	previousMetrics := llm.GenerationMetricsCallbackFromContext(ctx)
	latency := latencyTraceFromContext(ctx)
	var ttsStream *ttsStreamForwarder
	var vtuberStream *vtuberStreamForwarder
	if !speechPlanStreamModeFromContext(ctx) {
		ttsStream = newTTSStreamForwarder(l.ttsBridge, ttsSessionID, route, speaker, "agent.response", "[MessageOrch] TTS push degraded:")
		vtuberStream = newVTuberStreamForwarder(l.vtuberBridge, ttsSessionID, route, speaker, "agent.response", "[MessageOrch] VTuber push degraded:")
	}
	streamCtx := llm.ContextWithStreamCallback(ctx, func(token string) {
		if strings.TrimSpace(token) != "" {
			turntiming.Mark(ctx, turntiming.PointFirstTextCallback)
		}
		if prev != nil {
			prev(token)
		}
		if latency != nil && latency.markFirstToken() {
			emitLatencyMetric(l.emit, "llm", "first_token", latency.startedAt, string(route), taskID, sessionID, channel, chatID, "")
		}
		l.emit("agent.thinking", speaker, "user", token, string(route), taskID, sessionID, channel, chatID)
		ttsStream.OnToken(ctx, token)
		vtuberStream.OnToken(ctx, token)
	})
	streamCtx = llm.ContextWithGenerationMetricsCallback(streamCtx, func(metrics llm.GenerationMetrics) {
		if previousMetrics != nil {
			previousMetrics(metrics)
		}
		emitLLMThroughputMetric(l.emit, metrics, string(route), taskID, sessionID, channel, chatID)
	})
	return streamCtx, &streamBundle{tts: ttsStream, vtuber: vtuberStream}
}

// Push voices one finished utterance of speaker (the Agent that produced the
// text, equal to the turn's agent.response from) on the open session.
func (l *messageTTSLifecycle) Push(ctx context.Context, sessionID string, route routing.Route, speaker, eventType, text string) {
	voiceProfile, ok := voiceProfileForSpeaker(speaker, "[MessageOrch] TTS push degraded:")
	if !ok {
		return
	}
	ttsCtx := buildTTSContext(route, "normal", false)
	pushTTSTextChunks(ctx, l.ttsBridge, sessionID, route, eventType, text, ttsCtx, voiceProfile, "[MessageOrch] TTS push degraded:")
	req, ok := buildVTuberRequest(eventType, route, speaker, sessionID, text, ttsCtx, voiceProfile)
	if ok {
		pushVTuber(ctx, l.vtuberBridge, req, "[MessageOrch] VTuber push degraded:")
	}
}
