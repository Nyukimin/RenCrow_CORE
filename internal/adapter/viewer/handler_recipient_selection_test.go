package viewer

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
)

func TestHandleRecipientSelectionEmitsNormalizedViewerEvent(t *testing.T) {
	var emitted []orchestrator.OrchestratorEvent
	handler := HandleRecipientSelection(func(event orchestrator.OrchestratorEvent) {
		emitted = append(emitted, event)
	})
	body, _ := json.Marshal(map[string]string{
		"viewer_client_id": " portal-tab-1 ",
		"recipient":        " Shiro ",
	})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/recipient-selection", bytes.NewReader(body))

	handler(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
	}
	if len(emitted) != 1 {
		t.Fatalf("emitted events = %d, want 1", len(emitted))
	}
	if emitted[0].Type != "viewer.recipient_selected" || emitted[0].From != "viewer" || emitted[0].To != "shiro" {
		t.Fatalf("unexpected event: %#v", emitted[0])
	}
	var payload map[string]string
	if err := json.Unmarshal([]byte(emitted[0].Content), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["viewer_client_id"] != "portal-tab-1" || payload["recipient"] != "shiro" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestHandleRecipientSelectionRejectsUnknownRecipient(t *testing.T) {
	handler := HandleRecipientSelection(nil)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/viewer/recipient-selection", bytes.NewBufferString(`{"viewer_client_id":"portal-tab-1","recipient":"unknown"}`))

	handler(recorder, request)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", recorder.Code)
	}
}
