package tts

import (
	"context"
	"fmt"
	"strings"
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

type FallbackSynthesizer struct {
	providers []Provider
}

func NewFallbackSynthesizer(providers ...Provider) *FallbackSynthesizer {
	return &FallbackSynthesizer{providers: providers}
}

func (s *FallbackSynthesizer) Synthesize(ctx context.Context, in SynthesisInput) (SynthesisOutput, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return SynthesisOutput{}, fmt.Errorf("%w: text is empty", ErrSynthesisFailed)
	}
	var lastErr error
	for _, p := range s.providers {
		if p == nil {
			continue
		}
		out, err := p.Synthesize(ctx, in)
		if err == nil {
			if strings.TrimSpace(out.Provider) == "" {
				out.Provider = p.Name()
			}
			return out, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = ErrProviderUnavailable
	}
	return SynthesisOutput{}, fmt.Errorf("%w: %v", ErrSynthesisFailed, lastErr)
}
