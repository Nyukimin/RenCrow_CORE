package orchestrator

import (
	"context"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

func TestMessageTTSLifecycleChatUsesViewerRecipientVoice(t *testing.T) {
	for _, characterID := range []string{"mio", "shiro", "midori", "kuro"} {
		t.Run(characterID, func(t *testing.T) {
			bridge := &mockTTSBridge{}
			lifecycle := newMessageTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})

			lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
				SessionID: "viewer-chat",
				Channel:   "viewer",
				ChatID:    "viewer-user",
				To:        characterID,
			}, modulecore.NewTaskID(), routing.NewDecision(routing.RouteCHAT, 1, "chat"), "tts-chat")

			if len(bridge.startReqs) != 1 {
				t.Fatalf("TTS start request count = %d, want 1", len(bridge.startReqs))
			}
			got := bridge.startReqs[0]
			if got.CharacterID != characterID || got.VoiceID != characterID {
				t.Fatalf("TTS identity = character=%q voice=%q, want both %q", got.CharacterID, got.VoiceID, characterID)
			}
		})
	}
}

func TestDistributedTTSLifecycleChatUsesViewerRecipientVoice(t *testing.T) {
	for _, characterID := range []string{"mio", "shiro", "midori", "kuro"} {
		t.Run(characterID, func(t *testing.T) {
			bridge := &mockTTSBridge{}
			lifecycle := newDistributedTTSLifecycle(bridge, nil, func(eventType, from, to, content, route, taskID, sessionID, channel, chatID string) {})

			ttsSessionID := lifecycle.StartSessionForRoute(context.Background(), ProcessMessageRequest{
				SessionID: "viewer-chat",
				Channel:   "viewer",
				ChatID:    "viewer-user",
				To:        characterID,
			}, modulecore.NewTaskID(), routing.NewDecision(routing.RouteCHAT, 1, "chat"))

			if ttsSessionID == "" || len(bridge.startReqs) != 1 {
				t.Fatalf("expected one started TTS session, session=%q starts=%d", ttsSessionID, len(bridge.startReqs))
			}
			got := bridge.startReqs[0]
			if got.CharacterID != characterID || got.VoiceID != characterID {
				t.Fatalf("TTS identity = character=%q voice=%q, want both %q", got.CharacterID, got.VoiceID, characterID)
			}
		})
	}
}
