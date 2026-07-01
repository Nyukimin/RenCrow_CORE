package tts

import "strings"

const (
	PlaybackAckStatusError = "error"

	DeprecatedFallbackAckErrorCode = "TTS_FALLBACK_ACK_REJECTED"
	DeprecatedFallbackAckErrorText = "Viewer sent deprecated fallback playback ACK; treat as explicit TTS playback error"
)

type PlaybackAckInput struct {
	ResponseID     string
	SessionID      string
	UtteranceID    string
	ViewerClientID string
	Status         string
	ErrorCode      string
	Error          string
}

type PlaybackAckDecision struct {
	Status    string
	ErrorCode string
	Error     string
}

type PlaybackAckReceipt struct {
	OK             bool   `json:"ok"`
	Matched        bool   `json:"matched"`
	ResponseID     string `json:"response_id,omitempty"`
	SessionID      string `json:"session_id,omitempty"`
	UtteranceID    string `json:"utterance_id,omitempty"`
	ViewerClientID string `json:"viewer_client_id,omitempty"`
	ActiveAudio    bool   `json:"active_audio"`
	Status         string `json:"status"`
	ErrorCode      string `json:"error_code,omitempty"`
	Error          string `json:"error,omitempty"`
}

func NormalizePlaybackAck(input PlaybackAckInput) PlaybackAckDecision {
	status := strings.TrimSpace(input.Status)
	errorCode := strings.TrimSpace(input.ErrorCode)
	errorText := strings.TrimSpace(input.Error)
	if status == "fallback" {
		status = PlaybackAckStatusError
		if errorCode == "" {
			errorCode = DeprecatedFallbackAckErrorCode
		}
		if errorText == "" {
			errorText = DeprecatedFallbackAckErrorText
		}
	}
	return PlaybackAckDecision{
		Status:    status,
		ErrorCode: errorCode,
		Error:     errorText,
	}
}

func BuildPlaybackAckReceipt(input PlaybackAckInput, activeAudio bool, matched bool) PlaybackAckReceipt {
	ack := NormalizePlaybackAck(input)
	return PlaybackAckReceipt{
		OK:             true,
		Matched:        matched,
		ResponseID:     strings.TrimSpace(input.ResponseID),
		SessionID:      strings.TrimSpace(input.SessionID),
		UtteranceID:    strings.TrimSpace(input.UtteranceID),
		ViewerClientID: strings.TrimSpace(input.ViewerClientID),
		ActiveAudio:    activeAudio,
		Status:         ack.Status,
		ErrorCode:      ack.ErrorCode,
		Error:          ack.Error,
	}
}

func ShouldConsumePendingForPlaybackAck(activeAudio bool) bool {
	return activeAudio
}
