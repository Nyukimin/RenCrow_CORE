package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type memorySnapshotStoreStub struct {
	limit     int
	perSource int
	balanced  bool
	windowed  bool
	since     time.Time
	namespace string
	category  string
	domain    string
}

func (s *memorySnapshotStoreStub) RecentNewsItemsSince(_ context.Context, category string, since time.Time) ([]l1sqlite.L1NewsItem, error) {
	s.category = category
	s.since = since
	s.windowed = true
	return []l1sqlite.L1NewsItem{{ID: "news-windowed", Category: category, SourceID: "rss:one"}}, nil
}

func (s *memorySnapshotStoreStub) RecentNewsItemsBySource(_ context.Context, category string, perSource int, limit int) ([]l1sqlite.L1NewsItem, error) {
	s.category = category
	s.limit = limit
	s.perSource = perSource
	s.balanced = true
	return []l1sqlite.L1NewsItem{{ID: "news-balanced", Category: category, SourceID: "rss:one"}}, nil
}

func (s *memorySnapshotStoreStub) RecentByNamespace(_ context.Context, namespace string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	s.namespace = namespace
	s.limit = limit
	return []l1sqlite.L1MemoryEvent{{ID: "mem-1", Namespace: namespace, Message: "remembered", CreatedAt: time.Now().UTC()}}, nil
}

func (s *memorySnapshotStoreStub) RecentNewsItems(_ context.Context, category string, limit int) ([]l1sqlite.L1NewsItem, error) {
	s.category = category
	s.limit = limit
	return []l1sqlite.L1NewsItem{{ID: "news-1", Category: category, SummaryDraft: "news summary"}}, nil
}

func (s *memorySnapshotStoreStub) RecentDailyDigests(_ context.Context, category string, limit int) ([]l1sqlite.L1DailyDigest, error) {
	s.category = category
	s.limit = limit
	return []l1sqlite.L1DailyDigest{{ID: "digest-1", Category: category, DigestText: "digest"}}, nil
}

func (s *memorySnapshotStoreStub) RecentKnowledgeItems(_ context.Context, domain string, limit int) ([]l1sqlite.L1KnowledgeItem, error) {
	s.domain = domain
	s.limit = limit
	return []l1sqlite.L1KnowledgeItem{{ID: "kb-1", Domain: domain, Title: "Knowledge"}}, nil
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
		Memory    []l1sqlite.L1MemoryEvent   `json:"memory"`
		News      []l1sqlite.L1NewsItem      `json:"news"`
		Digests   []l1sqlite.L1DailyDigest   `json:"digests"`
		Knowledge []l1sqlite.L1KnowledgeItem `json:"knowledge"`
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

func TestHandleMemorySnapshot_SourceBalancedNews(t *testing.T) {
	store := &memorySnapshotStoreStub{}
	h := HandleMemorySnapshot(store)
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/snapshot?limit=100&per_source=5", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !store.balanced || store.limit != 100 || store.perSource != 5 {
		t.Fatalf("source-balanced query was not used: %+v", store)
	}
}

func TestHandleMemorySnapshot_TimeWindowNews(t *testing.T) {
	store := &memorySnapshotStoreStub{}
	h := HandleMemorySnapshot(store)
	before := time.Now().UTC().Add(-24 * time.Hour)
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/snapshot?category=tech&news_window_hours=24", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	after := time.Now().UTC().Add(-24 * time.Hour)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if !store.windowed || store.category != "tech" || store.since.Before(before) || store.since.After(after) {
		t.Fatalf("time-windowed query was not used with a 24-hour cutoff: %+v", store)
	}
	var out struct {
		NewsWindowHours int    `json:"news_window_hours"`
		NewsSince       string `json:"news_since"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.NewsWindowHours != 24 || out.NewsSince == "" {
		t.Fatalf("unexpected time-window metadata: %+v", out)
	}
}

func TestHandleMemorySnapshot_InvalidNewsWindow(t *testing.T) {
	h := HandleMemorySnapshot(&memorySnapshotStoreStub{})
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/snapshot?news_window_hours=bad", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
