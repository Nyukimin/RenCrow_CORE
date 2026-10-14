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

func BuildTranscriptEvent(eventType string, text string) map[string]any {
	return map[string]any{
		"type": strings.TrimSpace(eventType),
		"text": strings.TrimSpace(text),
	}
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
