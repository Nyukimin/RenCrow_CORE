package stt

import "testing"

func TestBuildWebSocketHandlerPlanPrioritizesGateway(t *testing.T) {
	got := BuildWebSocketHandlerPlan(true, "http://provider/stt/file", " ws://gateway/stt ")
	if got.Mode != WebSocketModeGateway || got.GatewayURL != "ws://gateway/stt" || got.ProviderURL != "" {
		t.Fatalf("BuildWebSocketHandlerPlan() = %#v, want gateway", got)
	}
}

func TestBuildWebSocketHandlerPlanUsesProviderWhenAvailable(t *testing.T) {
	got := BuildWebSocketHandlerPlan(true, " http://provider/stt/file ", "")
	if got.Mode != WebSocketModeProvider || got.ProviderURL != "http://provider/stt/file" {
		t.Fatalf("BuildWebSocketHandlerPlan() = %#v, want provider", got)
	}
}

func TestBuildWebSocketHandlerPlanUsesHTTPWhenProviderUnavailable(t *testing.T) {
	got := BuildWebSocketHandlerPlan(false, " http://provider/stt/file ", "")
	if got.Mode != WebSocketModeHTTP || got.ProviderURL != "http://provider/stt/file" {
		t.Fatalf("BuildWebSocketHandlerPlan() = %#v, want http", got)
	}
}

func TestIsWebSocketTextFramePayload(t *testing.T) {
	if !IsWebSocketTextFramePayload([]byte(`{"type":"ready"}`)) {
		t.Fatal("json object should be text")
	}
	if !IsWebSocketTextFramePayload([]byte(`"final_pending"`)) {
		t.Fatal("json string should be text")
	}
	if IsWebSocketTextFramePayload([]byte{0, 1, 2}) {
		t.Fatal("binary payload should not be text")
	}
	if IsWebSocketTextFramePayload([]byte(`{"type":`)) {
		t.Fatal("invalid json-looking payload should not be text")
	}
}
