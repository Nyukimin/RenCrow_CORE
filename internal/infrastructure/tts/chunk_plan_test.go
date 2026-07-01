package tts

import "testing"

func TestPlanTTSChunksKeepsSpeechAndDisplayAlignedForDefaultText(t *testing.T) {
	plan := planTTSChunks("こんにちは", "")
	if len(plan) != 1 {
		t.Fatalf("expected one chunk, got %d: %#v", len(plan), plan)
	}
	if plan[0].SpeechText != "こんにちは。" {
		t.Fatalf("speech text = %q, want punctuated text", plan[0].SpeechText)
	}
	if plan[0].DisplayText != plan[0].SpeechText {
		t.Fatalf("display text = %q, want speech text %q", plan[0].DisplayText, plan[0].SpeechText)
	}
}

func TestPlanTTSChunksUsesSpeechPlanForDifferentMultiChunkDisplayText(t *testing.T) {
	plan := planTTSChunks(
		"一つ目の音声です。二つ目の音声です。",
		"表示側だけがまったく違う境界で分割される長い文字列です。",
	)
	if len(plan) != 2 {
		t.Fatalf("expected two chunks, got %d: %#v", len(plan), plan)
	}
	if plan[0].DisplayText != plan[0].SpeechText || plan[1].DisplayText != plan[1].SpeechText {
		t.Fatalf("display chunks must follow speech plan for different multi-chunk text: %#v", plan)
	}
}

func TestPlanTTSChunksFormatsSpeechTextWithoutChangingSingleChunkDisplayText(t *testing.T) {
	raw := "**重要**【本文】を `確認` して。"
	plan := planTTSChunks(raw, raw)
	if len(plan) != 1 {
		t.Fatalf("expected one chunk, got %d: %#v", len(plan), plan)
	}
	if plan[0].SpeechText != "重要「本文」を 確認 して。" {
		t.Fatalf("speech text = %q", plan[0].SpeechText)
	}
	if plan[0].DisplayText != raw {
		t.Fatalf("display text = %q, want raw %q", plan[0].DisplayText, raw)
	}
}
