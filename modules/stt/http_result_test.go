package stt

import (
	"net/http"
	"testing"
)

func TestNormalizeHandlerResultDefaultsLanguageAndNoSpeech(t *testing.T) {
	got := NormalizeHandlerResult(HandlerResultInput{Text: " ", Language: " "})
	if got.Language != DefaultProviderLanguage || got.ErrorCode != ErrorNoSpeechDetected || got.Message != DefaultNoSpeechMessage {
		t.Fatalf("NormalizeHandlerResult() = %+v", got)
	}

	got = NormalizeHandlerResult(HandlerResultInput{Text: " hello ", Language: " en ", ErrorCode: " PROVIDER_FAILURE ", Message: " failed "})
	if got.Language != "en" || got.ErrorCode != ErrorProviderFailure || got.Message != "failed" {
		t.Fatalf("NormalizeHandlerResult(explicit) = %+v", got)
	}
}

func TestStatusForHandlerError(t *testing.T) {
	tests := []struct {
		code string
		want int
	}{
		{ErrorInvalidAudio, http.StatusBadRequest},
		{ErrorProviderTimeout, http.StatusGatewayTimeout},
		{ErrorProviderBusy, http.StatusTooManyRequests},
		{ErrorProviderFailure, http.StatusBadGateway},
		{"", http.StatusOK},
	}
	for _, tt := range tests {
		if got := StatusForHandlerError(tt.code); got != tt.want {
			t.Fatalf("StatusForHandlerError(%q) = %d, want %d", tt.code, got, tt.want)
		}
	}
}

func TestBuildChatInputEnvelope(t *testing.T) {
	got := BuildChatInputEnvelope(ChatInputEnvelopeInput{
		Provider:  " mock ",
		Text:      "hello",
		EventID:   " evt ",
		ErrorCode: " ",
	})
	if got["type"] != "user_input" || got["source"] != "local_stt" || got["input_type"] != "voice" {
		t.Fatalf("envelope routing fields = %+v", got)
	}
	if got["provider"] != "mock" || got["text"] != "hello" || got["event_id"] != "evt" || got["error_code"] != nil {
		t.Fatalf("envelope payload fields = %+v", got)
	}
}
