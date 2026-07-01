package tts

import "testing"

func TestPlanEmotion_ConversationWarm(t *testing.T) {
	got := PlanEmotion(EmotionInput{
		Event:        "conversation",
		Text:         "おはようございます。ありがとうございます。",
		VoiceProfile: "lumina_female",
	})
	if got.PrimaryEmotion != "warm" {
		t.Fatalf("expected warm, got %s", got.PrimaryEmotion)
	}
	if got.ReasonTrace.Event != "conversation" {
		t.Fatalf("unexpected reason trace: %+v", got.ReasonTrace)
	}
}

func TestPlanEmotion_WarningAlert(t *testing.T) {
	got := PlanEmotion(EmotionInput{
		Event: "warning",
		Text:  "警告です。注意してください。",
	})
	if got.PrimaryEmotion != "alert" {
		t.Fatalf("expected alert, got %s", got.PrimaryEmotion)
	}
}
