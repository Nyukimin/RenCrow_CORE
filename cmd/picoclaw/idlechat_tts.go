package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/idlechat"
	"github.com/Nyukimin/picoclaw_multiLLM/internal/application/orchestrator"
	moduletts "github.com/Nyukimin/picoclaw_multiLLM/modules/tts"
)

const idleChatRoute = "IDLECHAT"

func emitIdleChatTTS(ctx context.Context, bridge orchestrator.TTSBridge, ev idlechat.TimelineEvent) (<-chan struct{}, bool) {
	if bridge == nil || strings.TrimSpace(ev.Content) == "" || !isIdleChatTTSEventType(ev.Type) {
		return nil, false
	}

	filtered := moduletts.FilterSpeakableText("agent.response", idleChatRoute, formatIdleChatTTSText(ev))
	if filtered == "" {
		return nil, false
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
		return nil, false
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

	expectPlaybackAck := hasIdleChatViewerClients()
	registerTTSPublicSessionWithMessage(plan.SessionID, plan.PublicSessionID, plan.ResponseID, plan.MessageID, plan.TurnIndex)
	var waitCh <-chan struct{}
	if expectPlaybackAck {
		waitCh = registerIdleChatTTSPending(plan.SessionID, plan.ResponseID)
	} else {
		log.Printf("[IdleChat] TTS playback wait skipped because no Viewer SSE clients are connected: session=%s response=%s", plan.SessionID, plan.ResponseID)
	}
	if err := bridge.StartSession(ctx, orchestrator.TTSSessionStart{
		SessionID:        plan.SessionID,
		ResponseID:       plan.ResponseID,
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
			clearIdleChatTTSPending(plan.SessionID)
		} else {
			clearTTSPublicSession(plan.SessionID)
		}
		log.Printf("[IdleChat] TTS start failed: %v", err)
		return nil, false
	}
	if displayBridge, ok := bridge.(orchestrator.TTSDisplayBridge); ok {
		err := displayBridge.PushTextWithDisplay(ctx, plan.SessionID, plan.SpeechText, plan.DisplayText, &emotion)
		if err != nil {
			log.Printf("[IdleChat] TTS push failed: %v", err)
			if endErr := bridge.EndSession(ctx, plan.SessionID); endErr != nil {
				log.Printf("[IdleChat] TTS end after push failure failed: %v", endErr)
			}
			if expectPlaybackAck {
				clearIdleChatTTSPending(plan.SessionID)
			} else {
				clearTTSPublicSession(plan.SessionID)
			}
			return waitCh, true
		}
	} else if err := bridge.PushText(ctx, plan.SessionID, plan.SpeechText, &emotion); err != nil {
		log.Printf("[IdleChat] TTS push failed: %v", err)
		if endErr := bridge.EndSession(ctx, plan.SessionID); endErr != nil {
			log.Printf("[IdleChat] TTS end after push failure failed: %v", endErr)
		}
		if expectPlaybackAck {
			clearIdleChatTTSPending(plan.SessionID)
		} else {
			clearTTSPublicSession(plan.SessionID)
		}
		return waitCh, true
	}
	if err := bridge.EndSession(ctx, plan.SessionID); err != nil {
		if expectPlaybackAck {
			clearIdleChatTTSPending(plan.SessionID)
		} else {
			clearTTSPublicSession(plan.SessionID)
		}
		log.Printf("[IdleChat] TTS end failed: %v", err)
		return nil, false
	}
	if !expectPlaybackAck {
		clearTTSPublicSession(plan.SessionID)
	}
	return waitCh, true
}

func isIdleChatTTSEventType(eventType string) bool {
	return moduletts.IsIdleChatTTSEventType(eventType)
}
