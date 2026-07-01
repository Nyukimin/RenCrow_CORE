package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domaindci "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/dci"
)

type stubDCITraceLister struct {
	items []domaindci.SearchTrace
	limit int
}

func (s *stubDCITraceLister) ListRecent(limit int) ([]domaindci.SearchTrace, error) {
	s.limit = limit
	return s.items, nil
}

func TestHandleDCIRecent(t *testing.T) {
	store := &stubDCITraceLister{items: []domaindci.SearchTrace{{
		EventID:            "evt_dci_1",
		StartedAt:          time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC),
		EndedAt:            time.Date(2026, 5, 18, 12, 0, 1, 0, time.UTC),
		Actor:              "Worker",
		Mode:               "dci",
		UserQuery:          "DCI",
		Status:             "completed",
		FinalEvidenceCount: 1,
	}}}
	req := httptest.NewRequest(http.MethodGet, "/viewer/dci/recent?limit=7", nil)
	rec := httptest.NewRecorder()

	HandleDCIRecent(store).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if store.limit != 7 {
		t.Fatalf("limit = %d", store.limit)
	}
	var body struct {
		Items []domaindci.SearchTrace `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0].EventID != "evt_dci_1" {
		t.Fatalf("items = %#v", body.Items)
	}
}

func TestHandleDCIRecentInvalidLimit(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/viewer/dci/recent?limit=bad", nil)
	rec := httptest.NewRecorder()

	HandleDCIRecent(&stubDCITraceLister{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

type stubDCISearcher struct {
	query string
}

func (s *stubDCISearcher) Search(_ context.Context, query string) (domaindci.SearchResult, error) {
	s.query = query
	return domaindci.SearchResult{
		Pack: domaindci.EvidencePack{
			EventID: "evt_dci_search",
			Query:   query,
			Evidence: []domaindci.Evidence{{
				EvidenceID: "ev1",
				FilePath:   "docs/10_新仕様/19_DCI_直接コーパス探索仕様.md",
				LineStart:  1,
				LineEnd:    1,
				Snippet:    "# DCI 直接コーパス探索仕様",
			}},
		},
		Trace: domaindci.SearchTrace{
			EventID:            "evt_dci_search",
			Status:             "completed",
			FinalEvidenceCount: 1,
		},
	}, nil
}

func TestHandleDCISearch(t *testing.T) {
	searcher := &stubDCISearcher{}
	req := httptest.NewRequest(http.MethodPost, "/viewer/dci/search", strings.NewReader(`{"query":"DCI"}`))
	rec := httptest.NewRecorder()

	HandleDCISearch(searcher).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if searcher.query != "DCI" {
		t.Fatalf("query = %q", searcher.query)
	}
	var body domaindci.SearchResult
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Pack.EventID != "evt_dci_search" || len(body.Pack.Evidence) != 1 {
		t.Fatalf("body = %#v", body)
	}
}

func TestHandleDCISearchRejectsEmptyQuery(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/viewer/dci/search", strings.NewReader(`{"query":""}`))
	rec := httptest.NewRecorder()

	HandleDCISearch(&stubDCISearcher{}).ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}
