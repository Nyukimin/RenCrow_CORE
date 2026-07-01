package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type memorySnapshotStoreStub struct {
	limit     int
	namespace string
	category  string
	domain    string
}

func (s *memorySnapshotStoreStub) RecentByNamespace(_ context.Context, namespace string, limit int) ([]conversationpersistence.L1MemoryEvent, error) {
	s.namespace = namespace
	s.limit = limit
	return []conversationpersistence.L1MemoryEvent{{ID: "mem-1", Namespace: namespace, Message: "remembered", CreatedAt: time.Now().UTC()}}, nil
}

func (s *memorySnapshotStoreStub) RecentNewsItems(_ context.Context, category string, limit int) ([]conversationpersistence.L1NewsItem, error) {
	s.category = category
	s.limit = limit
	return []conversationpersistence.L1NewsItem{{ID: "news-1", Category: category, SummaryDraft: "news summary"}}, nil
}

func (s *memorySnapshotStoreStub) RecentDailyDigests(_ context.Context, category string, limit int) ([]conversationpersistence.L1DailyDigest, error) {
	s.category = category
	s.limit = limit
	return []conversationpersistence.L1DailyDigest{{ID: "digest-1", Category: category, DigestText: "digest"}}, nil
}

func (s *memorySnapshotStoreStub) RecentKnowledgeItems(_ context.Context, domain string, limit int) ([]conversationpersistence.L1KnowledgeItem, error) {
	s.domain = domain
	s.limit = limit
	return []conversationpersistence.L1KnowledgeItem{{ID: "kb-1", Domain: domain, Title: "Knowledge"}}, nil
}

func TestHandleMemorySnapshot(t *testing.T) {
	store := &memorySnapshotStoreStub{}
	h := HandleMemorySnapshot(store)

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/snapshot?namespace=conv:1&category=ai&domain=movie&limit=5", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.limit != 5 || store.namespace != "conv:1" || store.category != "ai" || store.domain != "movie" {
		t.Fatalf("unexpected store calls: %+v", store)
	}
	var out struct {
		Memory    []conversationpersistence.L1MemoryEvent   `json:"memory"`
		News      []conversationpersistence.L1NewsItem      `json:"news"`
		Digests   []conversationpersistence.L1DailyDigest   `json:"digests"`
		Knowledge []conversationpersistence.L1KnowledgeItem `json:"knowledge"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.Memory) != 1 || len(out.News) != 1 || len(out.Digests) != 1 || len(out.Knowledge) != 1 {
		t.Fatalf("unexpected snapshot: %+v", out)
	}
}

func TestHandleMemorySnapshot_InvalidLimit(t *testing.T) {
	h := HandleMemorySnapshot(&memorySnapshotStoreStub{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/snapshot?limit=bad", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
