package viewer

import (
	"context"
	"encoding/json"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
)

type memoryLayerHotStoreStub struct {
	sessionID string
	namespace string
	state     string
	limit     int
	l0        []l1sqlite.L1MemoryEvent
	l1        []l1sqlite.L1MemoryEvent
	l3        []l1sqlite.L1MemoryEvent
}

func (s *memoryLayerHotStoreStub) RecentBySession(_ context.Context, sessionID string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	s.sessionID = sessionID
	s.limit = limit
	if s.l0 != nil {
		return s.l0, nil
	}
	return []l1sqlite.L1MemoryEvent{{ID: "l0-1", SessionID: sessionID, Layer: "L0", Message: "current turn", CreatedAt: time.Now().UTC()}}, nil
}

func (s *memoryLayerHotStoreStub) RecentByNamespace(_ context.Context, namespace string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	s.namespace = namespace
	s.limit = limit
	if s.l1 != nil {
		return s.l1, nil
	}
	return []l1sqlite.L1MemoryEvent{{ID: "l1-1", Namespace: namespace, Layer: "L1", Message: "today memory", CreatedAt: time.Now().UTC()}}, nil
}

func (s *memoryLayerHotStoreStub) RecentByState(_ context.Context, memoryState string, limit int) ([]l1sqlite.L1MemoryEvent, error) {
	s.state = memoryState
	s.limit = limit
	if s.l3 != nil {
		return s.l3, nil
	}
	return []l1sqlite.L1MemoryEvent{{ID: "l3-1", MemoryState: memoryState, Layer: "L3", Message: "confirmed memory", CreatedAt: time.Now().UTC()}}, nil
}

type memoryLayerColdStoreStub struct {
	sessionID string
	domain    string
	kbDomain  string
	limit     int
	kbLimit   int
	history   []*domconv.ThreadSummary
	byDomain  []*domconv.ThreadSummary
	kbDocs    []*domconv.Document
}

func (s *memoryLayerColdStoreStub) GetSessionHistory(_ context.Context, sessionID string, limit int) ([]*domconv.ThreadSummary, error) {
	s.sessionID = sessionID
	s.limit = limit
	if s.history != nil {
		return s.history, nil
	}
	return []*domconv.ThreadSummary{{ThreadID: 10, Domain: "chat", Summary: "monthly summary"}}, nil
}

func (s *memoryLayerColdStoreStub) SearchByDomain(_ context.Context, domain string, limit int) ([]*domconv.ThreadSummary, error) {
	s.domain = domain
	s.limit = limit
	if s.byDomain != nil {
		return s.byDomain, nil
	}
	return []*domconv.ThreadSummary{{ThreadID: 11, Domain: domain, Summary: "domain summary"}}, nil
}

func (s *memoryLayerColdStoreStub) ListKBDocuments(_ context.Context, domain string, limit int) ([]*domconv.Document, error) {
	s.kbDomain = domain
	s.kbLimit = limit
	if s.kbDocs != nil {
		return s.kbDocs, nil
	}
	now := time.Now().UTC()
	return []*domconv.Document{{ID: "kb-1", Domain: domain, Content: "qdrant long-term knowledge", CreatedAt: now, UpdatedAt: now}}, nil
}

func TestHandleMemoryLayers(t *testing.T) {
	hot := &memoryLayerHotStoreStub{}
	cold := &memoryLayerColdStoreStub{}
	h := HandleMemoryLayers(hot, cold)

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/layers?session_id=session-1&namespace=user:ren&domain=movie&limit=4", nil)
	rec := httptest.NewRecorder()
	h(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if hot.sessionID != "session-1" || hot.namespace != "user:ren" || hot.state != l1sqlite.MemoryStateConfirmed || hot.limit != 4 {
		t.Fatalf("unexpected hot calls: %+v", hot)
	}
	if cold.sessionID != "session-1" || cold.domain != "movie" || cold.limit != 4 || cold.kbDomain != "movie" || cold.kbLimit != 4 {
		t.Fatalf("unexpected cold calls: %+v", cold)
	}

	var out struct {
		L0       []l1sqlite.L1MemoryEvent `json:"l0"`
		L1       []l1sqlite.L1MemoryEvent `json:"l1"`
		L2       []*domconv.ThreadSummary `json:"l2"`
		L3       []l1sqlite.L1MemoryEvent `json:"l3"`
		L3Qdrant []*domconv.Document      `json:"l3_qdrant"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if len(out.L0) != 1 || len(out.L1) != 1 || len(out.L2) != 2 || len(out.L3) != 1 || len(out.L3Qdrant) != 1 {
		t.Fatalf("unexpected layer snapshot: %+v", out)
	}
}

func TestHandleMemoryLayersRequiresHotStore(t *testing.T) {
	h := HandleMemoryLayers(nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/layers", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503, got %d", rec.Code)
	}
}

func TestHandleMemoryLayersRejectsMalformedSnapshot(t *testing.T) {
	now := time.Now().UTC()
	tests := []struct {
		name string
		hot  *memoryLayerHotStoreStub
		cold *memoryLayerColdStoreStub
		want string
	}{
		{
			name: "l1 missing message",
			hot: &memoryLayerHotStoreStub{
				l1: []l1sqlite.L1MemoryEvent{{ID: "l1-1", Layer: "L1", CreatedAt: now}},
			},
			cold: &memoryLayerColdStoreStub{},
			want: "l1 memory missing message",
		},
		{
			name: "l2 missing summary",
			hot:  &memoryLayerHotStoreStub{},
			cold: &memoryLayerColdStoreStub{
				history: []*domconv.ThreadSummary{{ThreadID: 10}},
			},
			want: "l2 summary missing summary",
		},
		{
			name: "l3 qdrant missing content",
			hot:  &memoryLayerHotStoreStub{},
			cold: &memoryLayerColdStoreStub{
				kbDocs: []*domconv.Document{{ID: "kb-1", Domain: "movie", CreatedAt: now, UpdatedAt: now}},
			},
			want: "l3_qdrant document missing content",
		},
		{
			name: "l3 qdrant missing created at",
			hot:  &memoryLayerHotStoreStub{},
			cold: &memoryLayerColdStoreStub{
				kbDocs: []*domconv.Document{{ID: "kb-1", Domain: "movie", Content: "qdrant long-term knowledge", UpdatedAt: now}},
			},
			want: "l3_qdrant document missing created_at",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := HandleMemoryLayers(tt.hot, tt.cold)
			req := httptest.NewRequest(http.MethodGet, "/viewer/memory/layers?session_id=session-1&namespace=user:ren&domain=movie&limit=4", nil)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("expected 500, got %d: %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.want) {
				t.Fatalf("body = %q, want %q", rec.Body.String(), tt.want)
			}
		})
	}
}
