package stt

import (
	"testing"
	"time"
)

func TestFinalTextForPending(t *testing.T) {
	text, ok := FinalTextForPending(DraftState{LastDraft: "  hello  "})
	if !ok || text != "hello" {
		t.Fatalf("FinalTextForPending() = %q,%t", text, ok)
	}
}

func TestFinalTextAfterDraftTimeout(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	state := DraftState{
		SpeechStarted: true,
		LastDraft:     " draft ",
		LastDraftAt:   now.Add(-1500 * time.Millisecond),
	}
	text, ok := FinalTextAfterDraftTimeout(state, now, time.Second)
	if !ok || text != "draft" {
		t.Fatalf("FinalTextAfterDraftTimeout() = %q,%t", text, ok)
	}

	state.LastDraftAt = now.Add(-500 * time.Millisecond)
	if _, ok := FinalTextAfterDraftTimeout(state, now, time.Second); ok {
		t.Fatal("draft should not finalize before timeout")
	}
}

func TestFinalTextAfterSilence(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	state := DraftState{
		SpeechStarted: true,
		LastDraft:     " draft ",
		LastVoiceAt:   now.Add(-1500 * time.Millisecond),
	}
	text, ok := FinalTextAfterSilence(state, now, time.Second)
	if !ok || text != "draft" {
		t.Fatalf("FinalTextAfterSilence() = %q,%t", text, ok)
	}
}

func TestFinalTextOnProviderError(t *testing.T) {
	text, ok := FinalTextOnProviderError(DraftState{SpeechStarted: true, LastDraft: " draft "})
	if !ok || text != "draft" {
		t.Fatalf("FinalTextOnProviderError() = %q,%t", text, ok)
	}
}

func TestNormalizeTranscriptText(t *testing.T) {
	if got := NormalizeTranscriptText("  hello  "); got != "hello" {
		t.Fatalf("NormalizeTranscriptText() = %q", got)
	}
	if got := NormalizeTranscriptText("  <|channel>thought\n<channel|> "); got != "" {
		t.Fatalf("NormalizeTranscriptText() should drop channel leaks, got %q", got)
	}
	if got := NormalizeTranscriptText("申し訳ございませんが、音声ファイルが添付されていないため、書き起こしを行うことができません。"); got != "" {
		t.Fatalf("NormalizeTranscriptText() should drop attachment boilerplate, got %q", got)
	}
}

func TestIsProviderErrorTranscriptText(t *testing.T) {
	if !IsProviderErrorTranscriptText("申し訳ございませんが、音声ファイルが添付されていないようです。") {
		t.Fatal("expected attachment boilerplate to be classified as provider error")
	}
	if IsProviderErrorTranscriptText("<|channel>thought\n<channel|>") {
		t.Fatal("channel leak should be transcript noise, not provider error")
	}
	if IsProviderErrorTranscriptText("こんにちは") {
		t.Fatal("ordinary transcript should not be classified as provider error")
	}
}

func TestIsUsableProvisionalFinalText(t *testing.T) {
	tests := []struct {
		name            string
		text            string
		audioDurationMS int
		want            bool
	}{
		{name: "ordinary transcript", text: "And so", audioDurationMS: 1000, want: true},
		{name: "empty", text: " ", audioDurationMS: 1000, want: false},
		{name: "provider error phrase", text: "申し訳ございませんが、音声ファイルが添付されていないようです。", audioDurationMS: 1000, want: false},
		{name: "short noise", text: "はい", audioDurationMS: 1000, want: false},
		{name: "too short for long audio", text: "abc", audioDurationMS: 6000, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsUsableProvisionalFinalText(tt.text, time.Duration(tt.audioDurationMS)*time.Millisecond)
			if got != tt.want {
				t.Fatalf("IsUsableProvisionalFinalText() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDraftStateTransitions(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	state := DraftState{}

	state = MarkVoiceObserved(state, now)
	if !state.LastVoiceAt.Equal(now) {
		t.Fatalf("LastVoiceAt = %s", state.LastVoiceAt)
	}

	var started bool
	state, started = MarkSpeechStarted(state)
	if !started || !state.SpeechStarted {
		t.Fatalf("speech start was not marked: %+v started=%t", state, started)
	}
	state, started = MarkSpeechStarted(state)
	if started {
		t.Fatal("already-started speech should not send another start event")
	}

	draftAt := now.Add(100 * time.Millisecond)
	state = ApplyDraftTranscript(state, " draft ", draftAt)
	if state.LastDraft != "draft" || !state.LastDraftAt.Equal(draftAt) {
		t.Fatalf("draft was not applied: %+v", state)
	}

	reset := ResetDraftAfterFinal(state, false)
	if reset.SpeechStarted || reset.LastDraft != "" || !reset.LastDraftAt.IsZero() {
		t.Fatalf("draft fields were not reset: %+v", reset)
	}
	if !reset.LastVoiceAt.Equal(now) {
		t.Fatalf("voice timestamp should be preserved: %+v", reset)
	}

	reset = ResetDraftAfterFinal(state, true)
	if !reset.LastVoiceAt.IsZero() {
		t.Fatalf("voice timestamp should be reset: %+v", reset)
	}
}

func TestBuildWebSocketEvents(t *testing.T) {
	session := BuildSessionInfoEvent(" sid ", " http ")
	if session["type"] != WebSocketEventTypeSessionInfo || session["session_id"] != "sid" || session["provider"] != "http" {
		t.Fatalf("session event = %#v", session)
	}
	ready := BuildReadyEvent()
	if ready["type"] != WebSocketEventTypeReady || ready["sample_rate"] != WebSocketReadySampleRate {
		t.Fatalf("ready event = %#v", ready)
	}
	if got := BuildSpeechStartEvent(); got["type"] != WebSocketEventTypeSpeechStart {
		t.Fatalf("speech start event = %#v", got)
	}
	if got := BuildDraftEvent(" draft "); got["type"] != WebSocketEventTypeDraft || got["text"] != "draft" {
		t.Fatalf("draft event = %#v", got)
	}
	if got := BuildFinalEvent(" final "); got["type"] != WebSocketEventTypeFinal || got["text"] != "final" {
		t.Fatalf("final event = %#v", got)
	}
	if got := BuildTimeoutStatusEvent(); got["type"] != WebSocketEventTypeStatus || got["text"] != ProviderTimeoutStatusText {
		t.Fatalf("status event = %#v", got)
	}
	if got := BuildErrorEvent(" err "); got["type"] != WebSocketEventTypeError || got["error"] != "err" {
		t.Fatalf("error event = %#v", got)
	}
}

func TestApplyTimeoutFailureBacksOffAndSetsCooldown(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	update := ApplyTimeoutFailure(AdaptiveTimeoutState{
		Timeout:           1200 * time.Millisecond,
		TimeoutStreak:     1,
		LastTimeoutNotice: now.Add(-4 * time.Second),
	}, now, 1200*time.Millisecond, 3200*time.Millisecond)

	if update.State.TimeoutStreak != 2 || update.State.SuccessStreak != 0 {
		t.Fatalf("unexpected streaks: %+v", update.State)
	}
	if update.State.Timeout != 1500*time.Millisecond {
		t.Fatalf("timeout = %s", update.State.Timeout)
	}
	if !update.State.CooldownUntil.Equal(now.Add(InferCooldownAfterTimeout)) {
		t.Fatalf("cooldown = %s", update.State.CooldownUntil)
	}
	if !update.ShouldSendNotice || !update.State.LastTimeoutNotice.Equal(now) {
		t.Fatalf("notice was not updated: %+v", update)
	}
}

func TestApplyTimeoutFailureSuppressesFrequentNotice(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	update := ApplyTimeoutFailure(AdaptiveTimeoutState{
		Timeout:           1200 * time.Millisecond,
		LastTimeoutNotice: now.Add(-time.Second),
	}, now, 1200*time.Millisecond, 3200*time.Millisecond)
	if update.ShouldSendNotice {
		t.Fatal("notice should be suppressed within interval")
	}
}

func TestApplyInferenceSuccessShrinksAfterThreshold(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	got := ApplyInferenceSuccess(AdaptiveTimeoutState{
		Timeout:       1500 * time.Millisecond,
		SuccessStreak: 3,
		TimeoutStreak: 2,
		CooldownUntil: now.Add(time.Second),
	}, now, 1200*time.Millisecond, 3200*time.Millisecond)

	if got.Timeout != 1400*time.Millisecond {
		t.Fatalf("timeout = %s", got.Timeout)
	}
	if got.SuccessStreak != 0 || got.TimeoutStreak != 0 || !got.CooldownUntil.IsZero() {
		t.Fatalf("unexpected state: %+v", got)
	}
}

func TestInferenceInCooldown(t *testing.T) {
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if !InferenceInCooldown(AdaptiveTimeoutState{CooldownUntil: now.Add(time.Second)}, now) {
		t.Fatal("expected cooldown")
	}
	if InferenceInCooldown(AdaptiveTimeoutState{CooldownUntil: now.Add(-time.Second)}, now) {
		t.Fatal("expired cooldown should not block")
	}
}
