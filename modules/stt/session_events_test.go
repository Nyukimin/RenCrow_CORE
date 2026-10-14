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

func TestBuildCodedErrorEvent(t *testing.T) {
	ev := BuildErrorEventWithCode(" stt_busy ", " too many utterances ")
	if ev["type"] != WebSocketEventTypeError || ev["code"] != "stt_busy" || ev["error"] != "too many utterances" {
		t.Fatalf("coded error event = %#v", ev)
	}
}

func TestStampUtteranceEventAddsIDAndSequenceWithoutMutatingInput(t *testing.T) {
	base := BuildErrorEventWithCode("stt_busy", "busy")
	stamped := StampUtteranceEvent(base, "u1", 7)
	if stamped["utterance_id"] != "u1" || stamped["seq"] != int64(7) || stamped["type"] != WebSocketEventTypeError {
		t.Fatalf("stamped event = %#v", stamped)
	}
	if _, ok := base["seq"]; ok {
		t.Fatalf("StampUtteranceEvent mutated its input: %#v", base)
	}
	noID := StampUtteranceEvent(BuildErrorEvent("x"), "", 8)
	if _, ok := noID["utterance_id"]; ok || noID["seq"] != int64(8) {
		t.Fatalf("an event without an utterance must carry seq only: %#v", noID)
	}
}
