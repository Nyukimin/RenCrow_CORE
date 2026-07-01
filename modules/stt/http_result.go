package stt

import (
	"net/http"
	"strings"
)

const (
	ErrorNoSpeechDetected = "NO_SPEECH_DETECTED"
	ErrorInvalidAudio     = "INVALID_AUDIO"
	ErrorProviderFailure  = "PROVIDER_FAILURE"
	ErrorProviderTimeout  = "PROVIDER_TIMEOUT"
	ErrorProviderBusy     = "PROVIDER_BUSY"

	DefaultNoSpeechMessage = "音声が検出されませんでした。"
)

type HandlerResultInput struct {
	Text         string
	Language     string
	ErrorCode    string
	Message      string
	Provider     string
	ProcessingMS int64
}

type HandlerResultDecision struct {
	Language  string
	ErrorCode string
	Message   string
}

type ChatInputEnvelopeInput struct {
	Provider  string
	Text      string
	EventID   string
	ErrorCode string
}

func NormalizeHandlerResult(input HandlerResultInput) HandlerResultDecision {
	decision := HandlerResultDecision{
		Language:  strings.TrimSpace(input.Language),
		ErrorCode: strings.TrimSpace(input.ErrorCode),
		Message:   strings.TrimSpace(input.Message),
	}
	if decision.Language == "" {
		decision.Language = DefaultProviderLanguage
	}
	if strings.TrimSpace(input.Text) == "" && decision.ErrorCode == "" {
		decision.ErrorCode = ErrorNoSpeechDetected
		decision.Message = DefaultNoSpeechMessage
	}
	return decision
}

func StatusForHandlerError(code string) int {
	switch strings.TrimSpace(code) {
	case ErrorInvalidAudio:
		return http.StatusBadRequest
	case ErrorProviderTimeout:
		return http.StatusGatewayTimeout
	case ErrorProviderBusy:
		return http.StatusTooManyRequests
	case ErrorProviderFailure:
		return http.StatusBadGateway
	default:
		return http.StatusOK
	}
}

func BuildChatInputEnvelope(input ChatInputEnvelopeInput) map[string]any {
	return map[string]any{
		"type":            "user_input",
		"source":          "local_stt",
		"input_type":      "voice",
		"provider":        strings.TrimSpace(input.Provider),
		"text":            input.Text,
		"confidence_note": nil,
		"event_id":        strings.TrimSpace(input.EventID),
		"error_code":      emptyStringToNil(input.ErrorCode),
	}
}

func emptyStringToNil(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
