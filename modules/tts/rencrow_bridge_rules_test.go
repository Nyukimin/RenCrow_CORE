package tts

import (
	"strings"
	"testing"
	"time"
)

func TestApplyRenCrowBridgeConfigDefaults(t *testing.T) {
	params := map[string]any{"style": "happy"}
	got := ApplyRenCrowBridgeConfigDefaults(RenCrowBridgeConfigDefaultsInput{
		VoiceID:        " ",
		RequestTimeout: 0,
		ProviderParams: params,
	})
	if got.VoiceID != DefaultRenCrowVoiceID || got.RequestTimeout != DefaultRenCrowSynthesisTimeout {
		t.Fatalf("defaults = %+v", got)
	}
	if got.ProviderParams["style"] != "happy" {
		t.Fatalf("provider params not copied: %+v", got.ProviderParams)
	}
	params["style"] = "changed"
	if got.ProviderParams["style"] != "happy" {
		t.Fatalf("provider params should copy map header: %+v", got.ProviderParams)
	}

	got = ApplyRenCrowBridgeConfigDefaults(RenCrowBridgeConfigDefaultsInput{
		VoiceID:        " male_01 ",
		RequestTimeout: 2 * time.Second,
	})
	if got.VoiceID != "male_01" || got.RequestTimeout != 2*time.Second {
		t.Fatalf("explicit config should be preserved: %+v", got)
	}
}

func TestBuildRenCrowSessionStart(t *testing.T) {
	got, err := BuildRenCrowSessionStart(RenCrowSessionStartInput{
		SessionID:      " s1 ",
		CharacterID:    " mio ",
		ResponseID:     " r1 ",
		RequestedVoice: " ",
		DefaultVoice:   " female_01 ",
	})
	if err != nil {
		t.Fatalf("BuildRenCrowSessionStart() error = %v", err)
	}
	if got.SessionID != "s1" || got.CharacterID != "mio" || got.ResponseID != "r1" || got.VoiceID != "female_01" {
		t.Fatalf("session start = %+v", got)
	}
	if _, err := BuildRenCrowSessionStart(RenCrowSessionStartInput{SessionID: " "}); err == nil || err.Error() != "session_id is required" {
		t.Fatalf("empty session error = %v", err)
	}
}

func TestPrepareRenCrowSpeechText(t *testing.T) {
	text, empty, err := PrepareRenCrowSpeechText(" hello ")
	if err != nil || empty || text != "hello" {
		t.Fatalf("PrepareRenCrowSpeechText() = text=%q empty=%v err=%v", text, empty, err)
	}
	text, empty, err = PrepareRenCrowSpeechText(" **重要**【本文】 ")
	if err != nil || empty || text != "重要「本文」" {
		t.Fatalf("markdown PrepareRenCrowSpeechText() = text=%q empty=%v err=%v", text, empty, err)
	}
	text, empty, err = PrepareRenCrowSpeechText(" ")
	if err != nil || !empty || text != "" {
		t.Fatalf("empty PrepareRenCrowSpeechText() = text=%q empty=%v err=%v", text, empty, err)
	}
	_, empty, err = PrepareRenCrowSpeechText(strings.Repeat("あ", DefaultRenCrowMaxTextLength+1))
	if err == nil || err.Error() != "text exceeds max_text_length" || empty {
		t.Fatalf("long text error = empty=%v err=%v", empty, err)
	}
}

func TestHasRenCrowSynthesisAudioOutput(t *testing.T) {
	if HasRenCrowSynthesisAudioOutput(" ", " ") {
		t.Fatal("empty audio output should be false")
	}
	if !HasRenCrowSynthesisAudioOutput(" /tmp/a.wav ", " ") {
		t.Fatal("audio path should be enough")
	}
	if !HasRenCrowSynthesisAudioOutput(" ", " http://audio ") {
		t.Fatal("audio url should be enough")
	}
}
