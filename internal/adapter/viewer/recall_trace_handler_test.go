package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
)

type recallTraceStoreStub struct {
	limit     int
	sessionID string
}
func (s *recallTraceStoreStub) RecentRecallTraces(_ context.Context, sessionID string, limit int) ([]domconv.RecallTrace, error) {
	s.sessionID = sessionID
	s.limit = limit
	return []domconv.RecallTrace{{
		ResponseID: "job-1",
		SessionID:  sessionID,
		Role:       "chat",
		Items:      []domconv.RecallTraceItem{{Layer: "L1", Kind: "search_cache", Summary: "cached"}},
	}}, nil
}

func TestHandleRecallTraces(t *testing.T) {
	store := &recallTraceStoreStub{}
	h := HandleRecallTraces(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/recall/traces?session_id=sess-1&limit=7", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.sessionID != "sess-1" || store.limit != 7 {
		t.Fatalf("unexpected store call: %+v", store)
	}
	var out struct {
		Items []domconv.RecallTrace `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Items[0].Kind != "search_cache" {
		t.Fatalf("unexpected traces: %+v", out)
	}
}
