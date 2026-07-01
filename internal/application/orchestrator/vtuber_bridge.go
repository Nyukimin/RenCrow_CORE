package orchestrator

import "context"

// VTuberEmotionRequest carries one logical emotion_tick event for a character.
type VTuberEmotionRequest struct {
	CharacterID  string
	Text         string
	Speaking     bool
	Valence      float64
	Arousal      float64
	Intensity    float64
	EmotionLabel string
	Expression   string
	EventType    string
	Route        string
	SessionID    string
	AudioOutput  string
}

// VTuberBridge publishes emotion updates to character-specific VTS endpoints.
type VTuberBridge interface {
	PublishEmotion(ctx context.Context, req VTuberEmotionRequest) error
}
