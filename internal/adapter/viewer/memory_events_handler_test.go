package viewer

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type memoryEventsStoreStub struct {
	namespace string
	limit     int
}

func (s *memoryEventsStoreStub) RecentEvents(_ context.Context, namespace string, limit int) ([]l1sqlite.L1EventLogEntry, error) {
	s.namespace = namespace
	s.limit = limit
	return []l1sqlite.L1EventLogEntry{{
		ID:        "event-1",
		EventType: "search.cache_saved",
		Namespace: namespace,
		Payload:   map[string]interface{}{"query": "RenCrow"},
		Source:    "search_cache",
		CreatedAt: time.Now().UTC(),
	}}, nil
}

func (s *memoryEventsStoreStub) RecentSearchCache(_ context.Context, limit int) ([]l1sqlite.L1SearchCacheEntry, error) {
	s.limit = limit
	return []l1sqlite.L1SearchCacheEntry{{
		QueryHash:       "hash-1",
		NormalizedQuery: "rencrow",
		Provider:        "web",
		RawQuery:        "RenCrow",
		ResultsJSON:     `[{"title":"memo"}]`,
		SourceURLs:      []string{"https://example.com"},
		RetrievedAt:     time.Now().UTC(),
		ExpiresAt:       time.Now().UTC().Add(time.Hour),
	}}, nil
}

func TestHandleMemoryEvents(t *testing.T) {
	store := &memoryEventsStoreStub{}
	h := HandleMemoryEvents(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/events?namespace=kb:web&limit=7", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.namespace != "kb:web" || store.limit != 7 {
		t.Fatalf("unexpected store call: %+v", store)
	}
	var out struct {
		Namespace   string                        `json:"namespace"`
		Events      []l1sqlite.L1EventLogEntry    `json:"events"`
		SearchCache []l1sqlite.L1SearchCacheEntry `json:"search_cache"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Namespace != "kb:web" || len(out.Events) != 1 || len(out.SearchCache) != 1 {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestHandleMemoryEventsRequiresNamespace(t *testing.T) {
	h := HandleMemoryEvents(&memoryEventsStoreStub{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/events", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
