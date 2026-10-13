package stt

import (
	"testing"
	"time"
)

func TestBuildReadyEventAnnouncesUtteranceProtocolAndLimit(t *testing.T) {
	ready := BuildReadyEvent(30 * time.Second)
	if ready["type"] != WebSocketEventTypeReady || ready["sample_rate"] != WebSocketReadySampleRate {
		t.Fatalf("ready event keeps the legacy keys: %#v", ready)
	}
	if ready["protocol"] != WebSocketProtocolUtterance {
		t.Fatalf("ready event must announce the utterance protocol: %#v", ready)
	}
	if ready["max_utterance_ms"] != int64(30000) {
		t.Fatalf("ready event must announce max_utterance_ms: %#v", ready)
	}
}

func TestBuildUtteranceFinalEvent(t *testing.T) {
	ev := BuildUtteranceFinalEvent(" こんにちは ", EndReasonStop, "", 1500*time.Millisecond)
	if ev["type"] != WebSocketEventTypeFinal || ev["text"] != "こんにちは" {
		t.Fatalf("final event = %#v", ev)
	}
	if ev["end_reason"] != EndReasonStop || ev["audio_ms"] != int64(1500) {
		t.Fatalf("final event metadata = %#v", ev)
	}
	if _, ok := ev["empty_reason"]; ok {
		t.Fatalf("a recognised final must not carry empty_reason: %#v", ev)
	}

	empty := BuildUtteranceFinalEvent("", EndReasonIdleTimeout, EmptyReasonTooShort, 80*time.Millisecond)
	if empty["text"] != "" || empty["empty_reason"] != EmptyReasonTooShort || empty["end_reason"] != EndReasonIdleTimeout {
		t.Fatalf("empty final = %#v", empty)
	}
}

func TestBuildCancelledAndCodedErrorEvents(t *testing.T) {
	if ev := BuildCancelledEvent(); ev["type"] != WebSocketEventTypeCancelled {
		t.Fatalf("cancelled event = %#v", ev)
	}
	ev := BuildErrorEventWithCode(" stt_busy ", " too many utterances ")
	if ev["type"] != WebSocketEventTypeError || ev["code"] != "stt_busy" || ev["error"] != "too many utterances" {
		t.Fatalf("coded error event = %#v", ev)
	}
}

func TestStampUtteranceEventAddsIDAndSequenceWithoutMutatingInput(t *testing.T) {
	base := BuildSpeechStartEvent()
	stamped := StampUtteranceEvent(base, "u1", 7)
	if stamped["utterance_id"] != "u1" || stamped["seq"] != int64(7) || stamped["type"] != WebSocketEventTypeSpeechStart {
		t.Fatalf("stamped event = %#v", stamped)
	}
	if _, ok := base["seq"]; ok {
		t.Fatalf("StampUtteranceEvent mutated its input: %#v", base)
	}
	noID := StampUtteranceEvent(BuildCancelledEvent(), "", 8)
	if _, ok := noID["utterance_id"]; ok || noID["seq"] != int64(8) {
		t.Fatalf("an event without an utterance must carry seq only: %#v", noID)
	}
}
