package voicechat

import "testing"

func TestIsWebSocketTextFramePayload(t *testing.T) {
	if !IsWebSocketTextFramePayload([]byte(`{"type":"session.ready"}`)) {
		t.Fatal("json object should be text")
	}
	if !IsWebSocketTextFramePayload([]byte(`"session.commit"`)) {
		t.Fatal("json string should be text")
	}
	if IsWebSocketTextFramePayload([]byte{0, 1, 2}) {
		t.Fatal("binary payload should not be text")
	}
	if IsWebSocketTextFramePayload([]byte(`{"type":`)) {
		t.Fatal("invalid json-looking payload should not be text")
	}
}
