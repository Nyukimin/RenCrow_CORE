package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/toolharness"
)

type toolHarnessEventStoreStub struct {
	items []toolharness.Event
	err   error
	limit int
}

func (s *toolHarnessEventStoreStub) ListRecent(limit int) ([]toolharness.Event, error) {
	s.limit = limit
	return s.items, s.err
}

func TestHandleToolHarnessRecent_Success(t *testing.T) {
	store := &toolHarnessEventStoreStub{items: []toolharness.Event{{
		EventID:          "evt_tool_1",
		ToolName:         "file_read",
		RawInputHash:     "sha256:test",
		ValidationStatus: toolharness.ValidationStatusRepaired,
		CreatedAt:        time.Now().UTC(),
	}}}
	req := httptest.NewRequest(http.MethodGet, "/viewer/tool-harness/recent?limit=10", nil)
	rec := httptest.NewRecorder()

	HandleToolHarnessRecent(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 10 {
		t.Fatalf("limit = %d, want 10", store.limit)
	}
	var payload struct {
		Items []toolharness.Event `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Items) != 1 || payload.Items[0].EventID != "evt_tool_1" {
		t.Fatalf("unexpected payload: %#v", payload)
	}
}

func TestHandleToolHarnessRecent_InvalidLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/tool-harness/recent?limit=0", nil)
	rec := httptest.NewRecorder()

	HandleToolHarnessRecent(&toolHarnessEventStoreStub{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
}
