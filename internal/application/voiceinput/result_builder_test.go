package voiceinput

import (
	"strings"
	"testing"
	"time"
)

func TestBuildFromLLMFinal_SplitsStructuredJSON(t *testing.T) {
	result, err := BuildFromLLMFinal(BuildLLMRequest{
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		FinalText:   `{"user_text":"Mioさんいますか","reply":"はい、います。"}`,
		StartedAt:   time.Now(),
	})
	if err != nil {
		t.Fatalf("BuildFromLLMFinal failed: %v", err)
	}
	if result.UserText != "Mioさんいますか" || result.Reply != "はい、います。" {
		t.Fatalf("unexpected split result: %+v", result)
	}
	if !strings.Contains(result.RawFinal, "user_text") {
		t.Fatalf("raw final should preserve original JSON: %q", result.RawFinal)
	}
}

func TestBuildFromLLMFinal_UsesHintAfterRelaySplit(t *testing.T) {
	result, err := BuildFromLLMFinal(BuildLLMRequest{
		UtteranceID:  "utt-1",
		SessionID:    "viewer",
		Channel:      "viewer",
		ChatID:       "default",
		UserTextHint: "れんの発話",
		FinalText:    "Mioの応答",
	})
	if err != nil {
		t.Fatalf("BuildFromLLMFinal failed: %v", err)
	}
	if result.UserText != "れんの発話" || result.Reply != "Mioの応答" {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestBuildFromLLMFinal_RejectsNoAudioMeta(t *testing.T) {
	_, err := BuildFromLLMFinal(BuildLLMRequest{
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		FinalText:   "音声が提供されていないため、音声ファイルをアップロードしてください。",
	})
	if err == nil {
		t.Fatal("expected no-audio final to be rejected")
	}
}

func TestBuildFromSTTFinal_RequiresUserAndReply(t *testing.T) {
	result, err := BuildFromSTTFinal(BuildSTTRequest{
		UtteranceID: "utt-1",
		SessionID:   "viewer",
		Channel:     "viewer",
		ChatID:      "default",
		UserText:    "こんにちは",
		Reply:       "こんにちは。",
	})
	if err != nil {
		t.Fatalf("BuildFromSTTFinal failed: %v", err)
	}
	if result.Mode != ModeSTT || result.UserText != "こんにちは" || result.Reply != "こんにちは。" {
		t.Fatalf("unexpected result: %+v", result)
	}
}
