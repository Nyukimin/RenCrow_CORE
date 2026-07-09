package main

import (
	"context"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

type ttsVTuberLipSync struct {
	bridge orchestrator.VTuberBridge
	mu     sync.Mutex
	active map[string]string
}

func newTTSVTuberLipSync(bridge orchestrator.VTuberBridge) *ttsVTuberLipSync {
	if bridge == nil {
		return nil
	}
	return &ttsVTuberLipSync{
		bridge: bridge,
		active: make(map[string]string),
	}
}

func (l *ttsVTuberLipSync) OnChunkReady(sessionID, characterID, text string) {
	if l == nil || l.bridge == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	characterID = strings.TrimSpace(strings.ToLower(characterID))
	if sessionID == "" || characterID == "" {
		return
	}
	if strings.TrimSpace(text) == "" {
		return
	}

	l.mu.Lock()
	l.active[sessionID] = characterID
	l.mu.Unlock()

	req := orchestrator.VTuberEmotionRequest{
		CharacterID:  characterID,
		Text:         text,
		Speaking:     true,
		Valence:      0.1,
		Arousal:      0.6,
		Intensity:    0.5,
		EmotionLabel: "neutral",
		EventType:    "tts.audio_chunk",
		Route:        "TTS",
		SessionID:    sessionID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := l.bridge.PublishEmotion(ctx, req); err != nil {
		log.Printf("[TTSLipSync] VTuber push degraded: %v", err)
	}
}

func (l *ttsVTuberLipSync) OnSessionCompleted(sessionID, characterID string) {
	if l == nil || l.bridge == nil {
		return
	}
	sessionID = strings.TrimSpace(sessionID)
	characterID = strings.TrimSpace(strings.ToLower(characterID))
	if sessionID == "" {
		return
	}
	l.mu.Lock()
	if characterID == "" {
		characterID = l.active[sessionID]
	}
	delete(l.active, sessionID)
	l.mu.Unlock()
	if characterID == "" {
		return
	}

	req := orchestrator.VTuberEmotionRequest{
		CharacterID:  characterID,
		Speaking:     false,
		Valence:      0,
		Arousal:      0,
		Intensity:    0,
		EmotionLabel: "neutral",
		EventType:    "tts.session_completed",
		Route:        "TTS",
		SessionID:    sessionID,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	if err := l.bridge.PublishEmotion(ctx, req); err != nil {
		log.Printf("[TTSLipSync] VTuber push degraded: %v", err)
	}
}
