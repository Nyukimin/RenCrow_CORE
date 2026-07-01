package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	domconv "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type recallPackUserStoreStub struct {
	userID string
	state  string
	limit  int
	items  []domainmemory.UserMemory
}

func (s *recallPackUserStoreStub) CreateUserMemory(context.Context, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	return nil, nil
}

func (s *recallPackUserStoreStub) ListUserMemories(_ context.Context, userID string, state string, _ bool, limit int) ([]domainmemory.UserMemory, error) {
	s.userID = userID
	s.state = state
	s.limit = limit
	return append([]domainmemory.UserMemory(nil), s.items...), nil
}

func (s *recallPackUserStoreStub) UpdateUserMemoryState(context.Context, string, string, string) (*domainmemory.UserMemory, error) {
	return nil, nil
}

func (s *recallPackUserStoreStub) ForgetUserMemory(context.Context, string, string) (*domainmemory.UserMemory, error) {
	return nil, nil
}

func (s *recallPackUserStoreStub) SupersedeUserMemory(context.Context, string, string, string) (*domainmemory.UserMemory, error) {
	return nil, nil
}

func TestHandleMemoryRecallPackFiltersUserMemory(t *testing.T) {
	now := time.Now().UTC()
	hot := &memoryLayerHotStoreStub{
		l0: []conversationpersistence.L1MemoryEvent{{
			ID:          "l0-1",
			SessionID:   "session-1",
			Namespace:   "conv:session-1",
			Layer:       "L0",
			MemoryState: domainmemory.MemoryStateObserved,
			Message:     "現在の会話",
			CreatedAt:   now,
		}},
	}
	cold := &memoryLayerColdStoreStub{
		history: []*domconv.ThreadSummary{{
			ThreadID: 42,
			Domain:   "chat",
			Summary:  "今日の流れ",
		}},
		kbDocs: []*domconv.Document{{
			ID:        "kb-1",
			Domain:    "memory",
			Content:   "Knowledge DB は user memory と混ぜない",
			Source:    "spec",
			CreatedAt: now,
			UpdatedAt: now,
		}},
	}
	users := &recallPackUserStoreStub{
		items: []domainmemory.UserMemory{
			{
				ID:               "mem-confirmed",
				Namespace:        "user:ren",
				UserID:           "ren",
				Type:             domainmemory.UserMemoryTypePreference,
				Statement:        "短く論理的な説明を好む",
				EvidenceEventIDs: []string{"evt-1"},
				Confidence:       0.9,
				Sensitivity:      "normal",
				State:            domainmemory.MemoryStateConfirmed,
				Active:           true,
			},
			{
				ID:          "mem-pinned",
				Namespace:   "user:ren",
				UserID:      "ren",
				Type:        domainmemory.UserMemoryTypeConstraint,
				Statement:   "日本語で答える",
				Sensitivity: "normal",
				State:       domainmemory.MemoryStatePinned,
				Active:      true,
			},
			{
				ID:          "mem-candidate",
				Namespace:   "user:ren",
				UserID:      "ren",
				Type:        domainmemory.UserMemoryTypePreference,
				Statement:   "candidate は Recall Pack に入れない",
				Sensitivity: "normal",
				State:       domainmemory.MemoryStateCandidate,
				Active:      true,
			},
			{
				ID:          "mem-sensitive",
				Namespace:   "user:ren",
				UserID:      "ren",
				Type:        domainmemory.UserMemoryTypeSensitive,
				Statement:   "sensitive は Recall Pack に入れない",
				Sensitivity: "sensitive",
				State:       domainmemory.MemoryStateConfirmed,
				Active:      true,
			},
		},
	}
	h := HandleMemoryRecallPack(hot, cold, users)

	req := httptest.NewRequest(http.MethodGet, "/viewer/memory/recall-pack?session_id=session-1&user_id=ren&domain=memory&limit=5", nil)
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if users.userID != "ren" || users.state != "" || users.limit != 5 {
		t.Fatalf("unexpected user memory query: %+v", users)
	}

	var pack domainmemory.RecallPackView
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if pack.SessionID != "session-1" || pack.UserID != "ren" {
		t.Fatalf("unexpected pack identity: %+v", pack)
	}
	ids := map[string]domainmemory.RecallPackItem{}
	for _, item := range pack.Items {
		ids[item.MemoryID] = item
	}
	for _, want := range []string{"l0-1", "mem-confirmed", "mem-pinned", "thread:42", "kb-1"} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("missing recall item %q in %+v", want, ids)
		}
	}
	for _, blocked := range []string{"mem-candidate", "mem-sensitive"} {
		if _, ok := ids[blocked]; ok {
			t.Fatalf("blocked recall item %q leaked into pack: %+v", blocked, ids[blocked])
		}
	}
	if ids["mem-pinned"].Score != 1.0 {
		t.Fatalf("pinned user memory should score 1.0, got %v", ids["mem-pinned"].Score)
	}
	if ids["kb-1"].Namespace != "kb:memory" {
		t.Fatalf("knowledge item must stay in kb namespace: %+v", ids["kb-1"])
	}
}
