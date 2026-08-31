package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/idlechat"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

const idleChatRoute = "IDLECHAT"

type idleChatTTSLifecycleController struct {
	ready     chan struct{}
	done      chan struct{}
	readyOnce sync.Once
	doneOnce  sync.Once
}

var idleChatTTSSynthesisLifecycles sync.Map

func registerIdleChatTTSSynthesisLifecycle(sessionID string, lifecycle *idleChatTTSLifecycleController) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || lifecycle == nil {
		return
	}
	idleChatTTSSynthesisLifecycles.Store(sessionID, lifecycle)
}

func unregisterIdleChatTTSSynthesisLifecycle(sessionID string, lifecycle *idleChatTTSLifecycleController) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" || lifecycle == nil {
		return
	}
	idleChatTTSSynthesisLifecycles.CompareAndDelete(sessionID, lifecycle)
}

func notifyIdleChatTTSSynthesisReady(sessionID string) {
	value, ok := idleChatTTSSynthesisLifecycles.Load(strings.TrimSpace(sessionID))
	if !ok {
		return
	}
	if lifecycle, ok := value.(*idleChatTTSLifecycleController); ok {
		lifecycle.signalReady()
	}
}

func newIdleChatTTSLifecycleController() *idleChatTTSLifecycleController {
	return &idleChatTTSLifecycleController{
		ready: make(chan struct{}),
		done:  make(chan struct{}),
	}
}

func (c *idleChatTTSLifecycleController) lifecycle() idlechat.TTSLifecycle {
	if c == nil {
		return idlechat.TTSLifecycle{}
	}
	return idlechat.TTSLifecycle{Ready: c.ready, Done: c.done}
}

func (c *idleChatTTSLifecycleController) signalReady() {
	if c == nil {
		return
	}
	c.readyOnce.Do(func() { close(c.ready) })
}

func (c *idleChatTTSLifecycleController) signalDone() {
	if c == nil {
		return
	}
	c.signalReady()
	c.doneOnce.Do(func() { close(c.done) })
}

func emitIdleChatTTS(ctx context.Context, bridge orchestrator.TTSBridge, ev idlechat.TimelineEvent) (idlechat.TTSLifecycle, bool) {
	if !hasIdleChatViewerClients() {
		log.Printf("[IdleChat] TTS synthesis skipped because no active audio Viewer is connected: session=%s response=%s", strings.TrimSpace(ev.SessionID), strings.TrimSpace(ev.MessageID))
		return idlechat.TTSLifecycle{}, false
	}
	controller := newIdleChatTTSLifecycleController()
	ok := emitIdleChatTTSWithLifecycle(ctx, bridge, ev, controller)
	return controller.lifecycle(), ok
}

func emitIdleChatTTSWithLifecycle(ctx context.Context, bridge orchestrator.TTSBridge, ev idlechat.TimelineEvent, lifecycle *idleChatTTSLifecycleController) (ok bool) {
	if lifecycle == nil {
		lifecycle = newIdleChatTTSLifecycleController()
	}
	defer lifecycle.signalDone()
	if ctx == nil {
		ctx = context.Background()
	}
	if bridge == nil || strings.TrimSpace(ev.Content) == "" || !isIdleChatTTSEventType(ev.Type) {
		return false
	}
	if ev.TraceID.Validate() != nil {
		log.Printf("[IdleChat] TTS event rejected with invalid trace: session=%s message_id=%s", strings.TrimSpace(ev.SessionID), strings.TrimSpace(ev.MessageID))
		return false
	}
	if !hasIdleChatViewerClients() {
		log.Printf("[IdleChat] TTS synthesis skipped because no active audio Viewer is connected: session=%s response=%s", strings.TrimSpace(ev.SessionID), strings.TrimSpace(ev.MessageID))
		return false
	}

	filtered := moduletts.FilterSpeakableText("agent.response", idleChatRoute, formatIdleChatTTSText(ev))
	if filtered == "" {
		return false
	}
	displayText := filtered
	if isIdleChatTopicAnnouncement(ev) {
		displayText = formatIdleChatDisplayText(ev)
	}

	publicSessionID := strings.TrimSpace(ev.SessionID)
	responseID := nextTTSPublicResponseIDForMessage(publicSessionID, ev.MessageID)
	plan, ok := moduletts.BuildIdleChatTTSPlan(moduletts.IdleChatTTSPlanInput{
		PublicSessionID: publicSessionID,
		ResponseID:      responseID,
		MessageID:       ev.MessageID,
		TurnIndex:       ev.TurnIndex,
		Speaker:         ev.From,
		SpeechText:      filtered,
		DisplayText:     displayText,
		TimeOfDay:       idleChatTimeOfDay(),
		Now:             time.Now(),
	})
	if !ok {
		return false
	}
	emotion := moduletts.PlanEmotion(moduletts.EmotionInput{
		Event: plan.Event,
		Text:  plan.SpeechText,
		Context: moduletts.EmotionContext{
			ConversationMode: plan.ConversationMode,
			TimeOfDay:        plan.TimeOfDay,
			Urgency:          plan.Urgency,
		},
		VoiceProfile: plan.VoiceProfile,
	})

	expectPlaybackAck := true
	registerTTSPublicSessionWithMessage(plan.SessionID, plan.PublicSessionID, plan.ResponseID, plan.MessageID, plan.TurnIndex)
	registerIdleChatTTSSynthesisLifecycle(plan.SessionID, lifecycle)
	defer unregisterIdleChatTTSSynthesisLifecycle(plan.SessionID, lifecycle)
	if expectPlaybackAck {
		registerIdleChatTTSPending(plan.SessionID, plan.ResponseID)
	} else {
		log.Printf("[IdleChat] TTS playback wait skipped because no Viewer SSE clients are connected: session=%s response=%s", plan.SessionID, plan.ResponseID)
	}
	if err := bridge.StartSession(ctx, orchestrator.TTSSessionStart{
		SessionID:        plan.SessionID,
		ResponseID:       plan.ResponseID,
		TraceID:          string(ev.TraceID),
		CharacterID:      plan.CharacterID,
		VoiceID:          plan.VoiceID,
		SpeechMode:       plan.SpeechMode,
		Event:            plan.Event,
		ConversationMode: plan.ConversationMode,
		Context: moduletts.EmotionContext{
			ConversationMode: plan.ConversationMode,
			TimeOfDay:        plan.TimeOfDay,
			Urgency:          plan.Urgency,
		},
		VoiceProfile: plan.VoiceProfile,
	}); err != nil {
		if expectPlaybackAck {
			clearIdleChatTTSPendingStale(plan.SessionID)
		} else {
			retireTTSPublicSession(plan.SessionID)
		}
		log.Printf("[IdleChat] TTS start failed: %v", err)
		return false
	}
	if displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge); ok {
		err := displayBridge.PushTextWithDisplay(ctx, plan.SessionID, plan.SpeechText, plan.DisplayText, &emotion)
		if err != nil {
			log.Printf("[IdleChat] TTS push failed: %v", err)
			if endErr := bridge.EndSession(ctx, plan.SessionID); endErr != nil {
				log.Printf("[IdleChat] TTS end after push failure failed: %v", endErr)
			}
			if expectPlaybackAck {
				clearIdleChatTTSPendingStale(plan.SessionID)
			} else {
				retireTTSPublicSession(plan.SessionID)
			}
			return false
		}
	} else if err := bridge.PushText(ctx, plan.SessionID, plan.SpeechText, &emotion); err != nil {
		log.Printf("[IdleChat] TTS push failed: %v", err)
		if endErr := bridge.EndSession(ctx, plan.SessionID); endErr != nil {
			log.Printf("[IdleChat] TTS end after push failure failed: %v", endErr)
		}
		if expectPlaybackAck {
			clearIdleChatTTSPendingStale(plan.SessionID)
		} else {
			retireTTSPublicSession(plan.SessionID)
		}
		return false
	}
	lifecycle.signalReady()
	if err := bridge.EndSession(ctx, plan.SessionID); err != nil {
		if expectPlaybackAck {
			clearIdleChatTTSPendingStale(plan.SessionID)
		} else {
			retireTTSPublicSession(plan.SessionID)
		}
		log.Printf("[IdleChat] TTS end failed: %v", err)
		return false
	}
	if !expectPlaybackAck {
		clearTTSPublicSession(plan.SessionID)
	}
	return true
}

func isIdleChatTTSEventType(eventType string) bool {
	return moduletts.IsIdleChatTTSEventType(eventType)
}
