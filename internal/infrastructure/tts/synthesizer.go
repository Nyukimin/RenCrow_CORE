package tts

import (
	"context"
)

type EmotionState struct {
	Emotion        string         `json:"emotion"`
	Intensity      float64        `json:"intensity"`
	Speed          float64        `json:"speed"`
	Pitch          float64        `json:"pitch"`
	Pause          string         `json:"pause"`
	Expressiveness float64        `json:"expressiveness"`
	Reason         map[string]any `json:"reason,omitempty"`
}

type VoiceProfile struct {
	VoiceID string `json:"voice_id"`
}

type SynthesisInput struct {
	Text         string
	Emotion      EmotionState
	VoiceProfile VoiceProfile
	OutputDir    string
	FilePrefix   string
}

type SynthesisOutput struct {
	Provider      string `json:"provider"`
	VoiceID       string `json:"voice_id,omitempty"`
	AudioFilePath string `json:"audio_file_path"`
	AudioURL      string `json:"audio_url,omitempty"`
	DurationMS    int    `json:"audio_duration_ms,omitempty"`
}

type Provider interface {
	Name() string
	Synthesize(ctx context.Context, in SynthesisInput) (SynthesisOutput, error)
}
