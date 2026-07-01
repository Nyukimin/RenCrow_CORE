package tts

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	DefaultRenCrowVoiceID          = "female_01"
	DefaultRenCrowSynthesisTimeout = 30 * time.Second
	DefaultRenCrowMaxTextLength    = 1000
)

type RenCrowBridgeConfigDefaultsInput struct {
	VoiceID        string
	RequestTimeout time.Duration
	ProviderParams map[string]any
}

type RenCrowBridgeConfigDefaults struct {
	VoiceID        string
	RequestTimeout time.Duration
	ProviderParams map[string]any
}

type RenCrowSessionStartInput struct {
	SessionID      string
	CharacterID    string
	ResponseID     string
	RequestedVoice string
	DefaultVoice   string
}

type RenCrowSessionStart struct {
	SessionID   string
	CharacterID string
	ResponseID  string
	VoiceID     string
}

func ApplyRenCrowBridgeConfigDefaults(input RenCrowBridgeConfigDefaultsInput) RenCrowBridgeConfigDefaults {
	voiceID := strings.TrimSpace(input.VoiceID)
	if voiceID == "" {
		voiceID = DefaultRenCrowVoiceID
	}
	timeout := input.RequestTimeout
	if timeout <= 0 {
		timeout = DefaultRenCrowSynthesisTimeout
	}
	params := map[string]any{}
	for key, value := range input.ProviderParams {
		params[key] = value
	}
	return RenCrowBridgeConfigDefaults{
		VoiceID:        voiceID,
		RequestTimeout: timeout,
		ProviderParams: params,
	}
}

func BuildRenCrowSessionStart(input RenCrowSessionStartInput) (RenCrowSessionStart, error) {
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return RenCrowSessionStart{}, fmt.Errorf("session_id is required")
	}
	voiceID := firstNonEmpty(input.RequestedVoice, input.DefaultVoice)
	return RenCrowSessionStart{
		SessionID:   sessionID,
		CharacterID: strings.TrimSpace(input.CharacterID),
		ResponseID:  strings.TrimSpace(input.ResponseID),
		VoiceID:     voiceID,
	}, nil
}

func PrepareRenCrowSpeechText(text string) (string, bool, error) {
	trimmed := FormatTTSSpeechPlainText(text)
	if trimmed == "" {
		return "", true, nil
	}
	if utf8.RuneCountInString(trimmed) > DefaultRenCrowMaxTextLength {
		return "", false, fmt.Errorf("text exceeds max_text_length")
	}
	return trimmed, false, nil
}

func HasRenCrowSynthesisAudioOutput(audioPath, audioURL string) bool {
	return strings.TrimSpace(audioPath) != "" || strings.TrimSpace(audioURL) != ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed != "" {
			return trimmed
		}
	}
	return ""
}
