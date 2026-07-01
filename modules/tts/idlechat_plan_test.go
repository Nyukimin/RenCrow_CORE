package tts

import (
	"testing"
	"time"
)

func TestBuildIdleChatTTSPlanBuildsSessionAndVoiceMetadata(t *testing.T) {
	now := time.Unix(0, 12345)
	got, ok := BuildIdleChatTTSPlan(IdleChatTTSPlanInput{
		PublicSessionID: " idle-1 ",
		ResponseID:      " idle-1:0001 ",
		MessageID:       " idle-1:msg:0002 ",
		TurnIndex:       2,
		Speaker:         "shiro",
		SpeechText:      " こんにちは。 ",
		DisplayText:     " こんにちは。 ",
		TimeOfDay:       "night",
		Now:             now,
	})
	if !ok {
		t.Fatal("expected plan")
	}
	if got.SessionID != "idle-1-tts-12345" ||
		got.PublicSessionID != "idle-1" ||
		got.ResponseID != "idle-1:0001" ||
		got.MessageID != "idle-1:msg:0002" ||
		got.TurnIndex != 2 {
		t.Fatalf("unexpected ids: %+v", got)
	}
	if got.CharacterID != "shiro" || got.VoiceID != IdleChatMaleVoiceID || got.VoiceProfile != IdleChatMaleVoiceProfile {
		t.Fatalf("unexpected voice metadata: %+v", got)
	}
	if got.SpeechMode != IdleChatTTSSpeechMode ||
		got.Event != IdleChatTTSEventName ||
		got.ConversationMode != IdleChatTTSEventConversationMode ||
		got.Urgency != IdleChatTTSUrgencyNormal ||
		got.TimeOfDay != "night" {
		t.Fatalf("unexpected session metadata: %+v", got)
	}
}

func TestBuildIdleChatTTSPlanDefaultsDisplayAndTimeOfDay(t *testing.T) {
	got, ok := BuildIdleChatTTSPlan(IdleChatTTSPlanInput{
		PublicSessionID: "idle-1",
		ResponseID:      "idle-1:0000",
		Speaker:         "mio",
		SpeechText:      "本文です。",
		Now:             time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected plan")
	}
	if got.DisplayText != "本文です。" || got.TimeOfDay != "day" {
		t.Fatalf("unexpected defaults: %+v", got)
	}
}

func TestBuildIdleChatTTSPlanRejectsMissingRequiredFields(t *testing.T) {
	tests := []IdleChatTTSPlanInput{
		{ResponseID: "r", SpeechText: "text"},
		{PublicSessionID: "s", SpeechText: "text"},
		{PublicSessionID: "s", ResponseID: "r"},
	}
	for _, input := range tests {
		if got, ok := BuildIdleChatTTSPlan(input); ok {
			t.Fatalf("expected invalid plan for %+v, got %+v", input, got)
		}
	}
}
