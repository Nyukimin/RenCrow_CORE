package stt

import (
	"strings"
	"time"
)

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
	WebSocketEventTypeCancelled   = "cancelled"

	WebSocketProtocolUtterance = "utterance.v1"

	WebSocketReadySampleRate  = 16000
	ProviderTimeoutStatusText = "stt provider timeout (retrying)"
)

func BuildSessionInfoEvent(provider string) map[string]any {
	return map[string]any{
		"type":     WebSocketEventTypeSessionInfo,
		"provider": strings.TrimSpace(provider),
	}
}

// BuildReadyEvent announces the utterance protocol and the longest utterance
// CORE accepts before it closes one and continues in a new utterance.
func BuildReadyEvent(maxUtterance time.Duration) map[string]any {
	event := map[string]any{
		"type":        WebSocketEventTypeReady,
		"sample_rate": WebSocketReadySampleRate,
		"protocol":    WebSocketProtocolUtterance,
	}
	if maxUtterance > 0 {
		event["max_utterance_ms"] = maxUtterance.Milliseconds()
	}
	return event
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

// BuildUtteranceFinalEvent is the single terminal event of a recognised or
// skipped utterance. text is empty when emptyReason is set; endReason records
// why the utterance was closed and audio its accumulated length.
func BuildUtteranceFinalEvent(text, endReason, emptyReason string, audio time.Duration) map[string]any {
	event := BuildFinalEvent(text)
	if endReason != "" {
		event["end_reason"] = endReason
	}
	event["audio_ms"] = audio.Milliseconds()
	if emptyReason != "" {
		event["empty_reason"] = emptyReason
	}
	return event
}

// BuildCancelledEvent is the terminal event of a cancelled utterance.
func BuildCancelledEvent() map[string]any {
	return map[string]any{"type": WebSocketEventTypeCancelled}
}

// BuildErrorEventWithCode is the terminal event of an utterance that failed.
func BuildErrorEventWithCode(code, message string) map[string]any {
	event := BuildErrorEvent(message)
	if code = strings.TrimSpace(code); code != "" {
		event["code"] = code
	}
	return event
}

// StampUtteranceEvent returns a copy of event carrying the utterance id (when
// there is one) and the session-wide event sequence number. Within one
// utterance a higher seq supersedes a lower one; final, error and cancelled are
// terminal.
func StampUtteranceEvent(event map[string]any, utteranceID string, seq int64) map[string]any {
	stamped := make(map[string]any, len(event)+2)
	for key, value := range event {
		stamped[key] = value
	}
	if utteranceID != "" {
		stamped["utterance_id"] = utteranceID
	}
	stamped["seq"] = seq
	return stamped
}
