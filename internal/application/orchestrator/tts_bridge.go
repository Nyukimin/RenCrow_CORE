package orchestrator

import (
	"context"

	moduletts "github.com/Nyukimin/RenCrow_CORE/modules/tts"
)

// TTSSessionStart describes one TTS streaming session metadata.
type TTSSessionStart struct {
	SessionID             string
	ResponseID            string
	CharacterID           string
	VoiceID               string
	SpeechMode            string
	Event                 string
	Urgency               string
	ConversationMode      string
	UserAttentionRequired bool
	Context               moduletts.EmotionContext
	VoiceProfile          string
}

// TTSBridge streams response text to an external TTS server.
type TTSBridge interface {
	StartSession(ctx context.Context, req TTSSessionStart) error
	PushText(ctx context.Context, sessionID string, text string, emotion *moduletts.EmotionState) error
	EndSession(ctx context.Context, sessionID string) error
}

// TTSDisplayBridge accepts separate speech text and viewer display text.
// Speech text may be normalized for pronunciation; display text must stay close
// to the LLM/user-facing wording.
type TTSDisplayBridge interface {
	PushTextWithDisplay(ctx context.Context, sessionID string, speechText string, displayText string, emotion *moduletts.EmotionState) error
}
