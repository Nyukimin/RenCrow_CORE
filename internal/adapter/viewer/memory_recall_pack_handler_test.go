package viewer

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	domconv "github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

type recallPackUserStoreCall struct {
	userID          string
	state           string
	includeInactive bool
	limit           int
}

type recallPackUserStoreStub struct {
	itemsByState map[string][]domainmemory.UserMemory
	calls        []recallPackUserStoreCall
}

func (s *recallPackUserStoreStub) CreateUserMemory(context.Context, domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	return nil, nil
}

func (s *recallPackUserStoreStub) ListUserMemories(_ context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	s.calls = append(s.calls, recallPackUserStoreCall{
		userID:          userID,
		state:           state,
		includeInactive: includeInactive,
		limit:           limit,
	})
	return append([]domainmemory.UserMemory(nil), s.itemsByState[state]...), nil
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
		l0: []l1sqlite.L1MemoryEvent{{
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
	candidates := make([]domainmemory.UserMemory, 5)
	for i := range candidates {
		candidates[i] = domainmemory.UserMemory{
			ID:          fmt.Sprintf("mem-candidate-%d", i+1),
			Namespace:   "user:ren",
			UserID:      "ren",
			Type:        domainmemory.UserMemoryTypePreference,
			Statement:   "candidate は Recall Pack に入れない",
			Sensitivity: "normal",
			State:       domainmemory.MemoryStateCandidate,
			Active:      true,
			CreatedAt:   now.Add(-time.Duration(i+1) * time.Second),
		}
	}
	users := &recallPackUserStoreStub{
		itemsByState: map[string][]domainmemory.UserMemory{
			"": candidates,
			domainmemory.MemoryStateConfirmed: {
				{
					ID:               "mem-confirmed-old",
					Namespace:        "user:ren",
					UserID:           "ren",
					Type:             domainmemory.UserMemoryTypePreference,
					Statement:        "古い確定記憶",
					EvidenceEventIDs: []string{"evt-old"},
					Confidence:       0.9,
					Sensitivity:      "normal",
					State:            domainmemory.MemoryStateConfirmed,
					Active:           true,
					CreatedAt:        now.Add(-50 * time.Second),
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
					CreatedAt:   now,
				},
				{
					ID:               "mem-confirmed-new",
					Namespace:        "user:ren",
					UserID:           "ren",
					Type:             domainmemory.UserMemoryTypePreference,
					Statement:        "新しい確定記憶",
					EvidenceEventIDs: []string{"evt-new"},
					Confidence:       0.9,
					Sensitivity:      "normal",
					State:            domainmemory.MemoryStateConfirmed,
					Active:           true,
					CreatedAt:        now.Add(-20 * time.Second),
				},
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
					CreatedAt:        now.Add(-30 * time.Second),
				},
			},
			domainmemory.MemoryStatePinned: {
				{
					ID:          "mem-pinned",
					Namespace:   "user:ren",
					UserID:      "ren",
					Type:        domainmemory.UserMemoryTypeConstraint,
					Statement:   "日本語で答える",
					Sensitivity: "normal",
					State:       domainmemory.MemoryStatePinned,
					Active:      true,
					CreatedAt:   now.Add(-40 * time.Second),
				},
				{
					ID:          "mem-pinned-old",
					Namespace:   "user:ren",
					UserID:      "ren",
					Type:        domainmemory.UserMemoryTypeConstraint,
					Statement:   "古いPinned記憶",
					Sensitivity: "normal",
					State:       domainmemory.MemoryStatePinned,
					Active:      true,
					CreatedAt:   now.Add(-60 * time.Second),
				},
				{
					ID:          "mem-pinned-new",
					Namespace:   "user:ren",
					UserID:      "ren",
					Type:        domainmemory.UserMemoryTypeConstraint,
					Statement:   "新しいPinned記憶",
					Sensitivity: "normal",
					State:       domainmemory.MemoryStatePinned,
					Active:      true,
					CreatedAt:   now.Add(-10 * time.Second),
				},
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
	var pack domainmemory.UserMemoryRecallView
	if err := json.Unmarshal(rec.Body.Bytes(), &pack); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if pack.SessionID != "session-1" || pack.UserID != "ren" {
		t.Fatalf("unexpected pack identity: %+v", pack)
	}
	ids := map[string]domainmemory.UserMemoryRecallItem{}
	for _, item := range pack.Items {
		ids[item.MemoryID] = item
	}
	for _, want := range []string{"l0-1", "mem-pinned-new", "mem-confirmed-new", "mem-confirmed", "mem-pinned", "mem-confirmed-old", "thread:42", "kb-1"} {
		if _, ok := ids[want]; !ok {
			t.Fatalf("missing recall item %q in %+v", want, ids)
		}
	}
	for _, blocked := range []string{"mem-candidate-1", "mem-sensitive", "mem-pinned-old"} {
		if _, ok := ids[blocked]; ok {
			t.Fatalf("blocked recall item %q leaked into pack: %+v", blocked, ids[blocked])
		}
	}
	userMemoryIDs := make([]string, 0, 5)
	for _, item := range pack.Items {
		if item.Layer == "UserMemory" {
			userMemoryIDs = append(userMemoryIDs, item.MemoryID)
		}
	}
	wantUserMemoryIDs := []string{"mem-pinned-new", "mem-confirmed-new", "mem-confirmed", "mem-pinned", "mem-confirmed-old"}
	if !reflect.DeepEqual(userMemoryIDs, wantUserMemoryIDs) {
		t.Fatalf("unexpected user memory order or cap: got %v want %v", userMemoryIDs, wantUserMemoryIDs)
	}
	wantCalls := []recallPackUserStoreCall{
		{userID: "ren", state: domainmemory.MemoryStateConfirmed, includeInactive: false, limit: 5},
		{userID: "ren", state: domainmemory.MemoryStatePinned, includeInactive: false, limit: 5},
	}
	if !reflect.DeepEqual(users.calls, wantCalls) {
		t.Fatalf("unexpected user memory queries: got %+v want %+v", users.calls, wantCalls)
	}
	if ids["mem-pinned"].Score != 1.0 {
		t.Fatalf("pinned user memory should score 1.0, got %v", ids["mem-pinned"].Score)
	}
	if ids["kb-1"].Namespace != "kb:memory" {
		t.Fatalf("knowledge item must stay in kb namespace: %+v", ids["kb-1"])
	}
}
