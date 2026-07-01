package tts

import (
	"testing"
	"time"
)

func TestBuildRouteTTSPlanForOpsUsesShiroReportVoice(t *testing.T) {
	got, ok := BuildRouteTTSPlan(RouteTTSPlanInput{
		Route:      "OPS",
		SessionID:  "tts-1",
		ResponseID: "job-1",
		Now:        time.Date(2026, 5, 30, 22, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected plan")
	}
	if got.CharacterID != "shiro" ||
		got.VoiceID != RouteTTSMaleVoiceID ||
		got.VoiceProfile != RouteTTSMaleVoiceProfile {
		t.Fatalf("unexpected voice metadata: %+v", got)
	}
	if got.SpeechMode != "report" ||
		got.Event != "analysis_report" ||
		got.ConversationMode != "report" ||
		got.Context.TimeOfDay != "night" ||
		got.Urgency != "normal" {
		t.Fatalf("unexpected route metadata: %+v", got)
	}
}

func TestBuildRouteTTSPlanForChatUsesMioConversation(t *testing.T) {
	got, ok := BuildRouteTTSPlan(RouteTTSPlanInput{
		Route:      "CHAT",
		SessionID:  "tts-1",
		ResponseID: "job-1",
		Urgency:    "high",
		Now:        time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC),
	})
	if !ok {
		t.Fatal("expected plan")
	}
	if got.CharacterID != "mio" ||
		got.VoiceID != RouteTTSDefaultVoiceID ||
		got.VoiceProfile != RouteTTSDefaultVoiceProfile {
		t.Fatalf("unexpected voice metadata: %+v", got)
	}
	if got.SpeechMode != "conversational" ||
		got.Event != "conversation" ||
		got.ConversationMode != "chat" ||
		got.Context.TimeOfDay != "day" ||
		got.Urgency != "high" {
		t.Fatalf("unexpected route metadata: %+v", got)
	}
}

func TestBuildRouteTTSPlanRejectsMissingRequiredFields(t *testing.T) {
	tests := []RouteTTSPlanInput{
		{ResponseID: "job-1"},
		{SessionID: "tts-1"},
	}
	for _, input := range tests {
		if got, ok := BuildRouteTTSPlan(input); ok {
			t.Fatalf("expected invalid plan for %+v, got %+v", input, got)
		}
	}
}

func TestChooseNonEmpty(t *testing.T) {
	if got := ChooseNonEmpty(" ", "  a  ", "b"); got != "a" {
		t.Fatalf("ChooseNonEmpty() = %q", got)
	}
	if got := ChooseNonEmpty(" ", ""); got != "" {
		t.Fatalf("ChooseNonEmpty() empty = %q", got)
	}
}

func TestEqualFoldTrim(t *testing.T) {
	if !EqualFoldTrim(" Mio ", "mio") {
		t.Fatal("EqualFoldTrim() should ignore case and trim")
	}
	if EqualFoldTrim(" Mio ", "shiro") {
		t.Fatal("EqualFoldTrim() should distinguish values")
	}
}
