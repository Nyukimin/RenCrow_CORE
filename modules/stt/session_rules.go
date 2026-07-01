package stt

import (
	"strings"
	"time"
)

type DraftState struct {
	SpeechStarted bool
	LastDraft     string
	LastDraftAt   time.Time
	LastVoiceAt   time.Time
}

func MarkVoiceObserved(state DraftState, at time.Time) DraftState {
	state.LastVoiceAt = at
	return state
}

func MarkSpeechStarted(state DraftState) (DraftState, bool) {
	if state.SpeechStarted {
		return state, false
	}
	state.SpeechStarted = true
	return state, true
}

func ApplyDraftTranscript(state DraftState, text string, at time.Time) DraftState {
	state.LastDraft = NormalizeTranscriptText(text)
	state.LastDraftAt = at
	return state
}

func ResetDraftAfterFinal(state DraftState, resetVoice bool) DraftState {
	state.SpeechStarted = false
	state.LastDraft = ""
	state.LastDraftAt = time.Time{}
	if resetVoice {
		state.LastVoiceAt = time.Time{}
	}
	return state
}

const (
	InferCooldownAfterTimeout = 800 * time.Millisecond
	TimeoutNoticeInterval     = 3 * time.Second
	TimeoutBackoffThreshold   = 2
	SuccessShrinkThreshold    = 4
)

type AdaptiveTimeoutState struct {
	Timeout           time.Duration
	TimeoutStreak     int
	SuccessStreak     int
	CooldownUntil     time.Time
	LastTimeoutNotice time.Time
}

type TimeoutFailureUpdate struct {
	State            AdaptiveTimeoutState
	ShouldSendNotice bool
}

const (
	WebSocketEventTypeSessionInfo = "session_info"
	WebSocketEventTypeReady       = "ready"
	WebSocketEventTypeSpeechStart = "speech_start"
	WebSocketEventTypePartial     = "partial"
	WebSocketEventTypeDraft       = "draft"
	WebSocketEventTypeFinal       = "final"
	WebSocketEventTypeStatus      = "status"
	WebSocketEventTypeError       = "error"

	WebSocketReadySampleRate  = 16000
	ProviderTimeoutStatusText = "stt provider timeout (retrying)"
)

func BuildSessionInfoEvent(sessionID string, provider string) map[string]any {
	return map[string]any{
		"type":       WebSocketEventTypeSessionInfo,
		"session_id": strings.TrimSpace(sessionID),
		"provider":   strings.TrimSpace(provider),
	}
}

func BuildReadyEvent() map[string]any {
	return map[string]any{
		"type":        WebSocketEventTypeReady,
		"sample_rate": WebSocketReadySampleRate,
	}
}

func BuildSpeechStartEvent() map[string]any {
	return map[string]any{"type": WebSocketEventTypeSpeechStart}
}

func BuildTranscriptEvent(eventType string, text string) map[string]any {
	return map[string]any{
		"type": strings.TrimSpace(eventType),
		"text": strings.TrimSpace(text),
	}
}

func BuildDraftEvent(text string) map[string]any {
	return BuildTranscriptEvent(WebSocketEventTypeDraft, text)
}

func BuildFinalEvent(text string) map[string]any {
	return BuildTranscriptEvent(WebSocketEventTypeFinal, text)
}

func BuildTimeoutStatusEvent() map[string]any {
	return BuildTranscriptEvent(WebSocketEventTypeStatus, ProviderTimeoutStatusText)
}

func BuildErrorEvent(message string) map[string]any {
	return map[string]any{
		"type":  WebSocketEventTypeError,
		"error": strings.TrimSpace(message),
	}
}

func ApplyTimeoutFailure(state AdaptiveTimeoutState, now time.Time, minTimeout time.Duration, maxTimeout time.Duration) TimeoutFailureUpdate {
	state.TimeoutStreak++
	state.SuccessStreak = 0
	if state.TimeoutStreak >= TimeoutBackoffThreshold {
		state.Timeout = AdjustAdaptiveTimeout(state.Timeout, 300*time.Millisecond, minTimeout, maxTimeout)
	}
	state.CooldownUntil = now.Add(InferCooldownAfterTimeout)
	shouldNotice := now.Sub(state.LastTimeoutNotice) > TimeoutNoticeInterval
	if shouldNotice {
		state.LastTimeoutNotice = now
	}
	return TimeoutFailureUpdate{State: state, ShouldSendNotice: shouldNotice}
}

func ApplyInferenceSuccess(state AdaptiveTimeoutState, now time.Time, minTimeout time.Duration, maxTimeout time.Duration) AdaptiveTimeoutState {
	state.SuccessStreak++
	state.TimeoutStreak = 0
	if state.SuccessStreak >= SuccessShrinkThreshold {
		state.Timeout = AdjustAdaptiveTimeout(state.Timeout, -100*time.Millisecond, minTimeout, maxTimeout)
		state.SuccessStreak = 0
	}
	state.CooldownUntil = time.Time{}
	return state
}

func InferenceInCooldown(state AdaptiveTimeoutState, now time.Time) bool {
	return !state.CooldownUntil.IsZero() && now.Before(state.CooldownUntil)
}

func FinalTextForPending(state DraftState) (string, bool) {
	text := strings.TrimSpace(state.LastDraft)
	return text, text != ""
}

func FinalTextAfterDraftTimeout(state DraftState, now time.Time, timeout time.Duration) (string, bool) {
	text := strings.TrimSpace(state.LastDraft)
	if !state.SpeechStarted || text == "" || state.LastDraftAt.IsZero() {
		return "", false
	}
	return text, now.Sub(state.LastDraftAt) >= timeout
}

func FinalTextAfterSilence(state DraftState, now time.Time, timeout time.Duration) (string, bool) {
	text := strings.TrimSpace(state.LastDraft)
	if !state.SpeechStarted || text == "" || state.LastVoiceAt.IsZero() {
		return "", false
	}
	return text, now.Sub(state.LastVoiceAt) >= timeout
}

func FinalTextOnProviderError(state DraftState) (string, bool) {
	text := strings.TrimSpace(state.LastDraft)
	return text, state.SpeechStarted && text != ""
}

func NormalizeTranscriptText(text string) string {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	for _, marker := range transcriptNoiseMarkers {
		if containsTranscriptMarker(trimmed, marker) {
			return ""
		}
	}
	return trimmed
}

const ProviderTranscriptErrorMessage = "音声認識に失敗しました。もう一度お試しください。"

func IsProviderErrorTranscriptText(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	for _, marker := range providerErrorTranscriptMarkers {
		if containsTranscriptMarker(trimmed, marker) {
			return true
		}
	}
	return false
}

func IsUsableProvisionalFinalText(text string, audioDuration time.Duration) bool {
	normalized := NormalizeTranscriptText(text)
	if normalized == "" || IsProviderErrorTranscriptText(normalized) {
		return false
	}
	runes := []rune(normalized)
	if len(runes) <= 2 {
		return false
	}
	for _, noise := range provisionalFinalNoiseTexts {
		if normalized == noise {
			return false
		}
	}
	if audioDuration >= 5*time.Second && len(runes) < 5 {
		return false
	}
	return true
}

func containsTranscriptMarker(text, marker string) bool {
	return strings.Contains(strings.ToLower(text), strings.ToLower(marker))
}

var transcriptNoiseMarkers = []string{
	"<|channel",
	"channel>thought",
	"channel=analysis",
}

var providerErrorTranscriptMarkers = []string{
	"音声ファイルが添付されていない",
	"音声ファイルが添付されていないため",
	"添付されていないため",
	"音声をアップロードしていただければ",
	"音声をアップロード",
	"書き起こしを行うことができ",
	"日本語で書き起こし",
	"書き起こしをいたします",
	"申し訳ございませんが",
}

var provisionalFinalNoiseTexts = []string{
	"はい",
	"はい。",
	"です",
	"と",
}

func init() {
	transcriptNoiseMarkers = append(transcriptNoiseMarkers, providerErrorTranscriptMarkers...)
}
