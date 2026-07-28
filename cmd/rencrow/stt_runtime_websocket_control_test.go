package main

import (
	"encoding/json"
	"strings"
	"testing"

	modulestt "github.com/Nyukimin/RenCrow_CORE/modules/stt"
)

func TestIsSTTFinalControl(t *testing.T) {
	for _, control := range []string{"final_pending", " stop "} {
		if !isSTTFinalControl(control) {
			t.Fatalf("expected %q to request finalization", control)
		}
	}
	for _, control := range []string{"start", "config", ""} {
		if isSTTFinalControl(control) {
			t.Fatalf("did not expect %q to request finalization", control)
		}
	}
}

func TestTransformSTTGatewayTextFrame_ProviderErrorPhraseBecomesError(t *testing.T) {
	payload := []byte(`{"type":"partial","text":"申し訳ございませんが、音声ファイルが添付されていないようです。"}`)

	transformed, handled := transformSTTGatewayTextFrame(payload)
	if !handled {
		t.Fatal("expected provider error phrase to be handled")
	}
	if strings.TrimSpace(transformed) == "" {
		t.Fatal("expected transformed error event")
	}
	var ev map[string]any
	if err := json.Unmarshal([]byte(transformed), &ev); err != nil {
		t.Fatalf("decode transformed event: %v", err)
	}
	if ev["type"] != modulestt.WebSocketEventTypeError {
		t.Fatalf("expected error event, got %+v", ev)
	}
	if ev["error"] != modulestt.ProviderTranscriptErrorMessage {
		t.Fatalf("expected user-facing provider error message, got %+v", ev)
	}
}
