package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	glossaryentity "github.com/Nyukimin/picoclaw_multiLLM/internal/glossary/domain/entity"
)

type glossaryStoreStub struct {
	items []*glossaryentity.GlossaryItem
	err   error
	limit int
}

func (s *glossaryStoreStub) GetRecentGlossary(_ context.Context, limit int) ([]*glossaryentity.GlossaryItem, error) {
	s.limit = limit
	if s.err != nil {
		return nil, s.err
	}
	return s.items, nil
}

func TestHandleGlossaryRecent_Success(t *testing.T) {
	store := &glossaryStoreStub{items: []*glossaryentity.GlossaryItem{{
		ID:          "gloss_1",
		Term:        "AI Summit",
		Explanation: "Mentioned in news: AI Summit discusses new regulations",
		Source:      "rss",
		Category:    "organization",
		CreatedAt:   time.Now().UTC(),
		UpdatedAt:   time.Now().UTC(),
	}}}
	h := HandleGlossaryRecent(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/glossary/recent?limit=5", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if store.limit != 5 {
		t.Fatalf("expected limit=5, got %d", store.limit)
	}

	var out struct {
		Items []*glossaryentity.GlossaryItem `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.Items) != 1 || out.Items[0].Term != "AI Summit" {
		t.Fatalf("unexpected items: %+v", out.Items)
	}
}

func TestHandleGlossaryRecent_InvalidLimit(t *testing.T) {
	h := HandleGlossaryRecent(&glossaryStoreStub{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/glossary/recent?limit=bad", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
