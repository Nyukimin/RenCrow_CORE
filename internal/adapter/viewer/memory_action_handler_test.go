package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	conversationpersistence "github.com/Nyukimin/picoclaw_multiLLM/internal/infrastructure/persistence/conversation"
)

type memoryActionStoreStub struct {
	stateID       string
	memoryState   string
	promoteID     string
	targetNS      string
	promotedBy    string
	promotedEvent conversationpersistence.L1MemoryEvent
}

func (s *memoryActionStoreStub) UpdateMemoryState(_ context.Context, id string, memoryState string) error {
	s.stateID = id
	s.memoryState = memoryState
	return nil
}

func (s *memoryActionStoreStub) PromoteMemoryToNamespace(_ context.Context, id string, targetNamespace string, promotedBy string) (*conversationpersistence.L1MemoryEvent, error) {
	s.promoteID = id
	s.targetNS = targetNamespace
	s.promotedBy = promotedBy
	s.promotedEvent = conversationpersistence.L1MemoryEvent{ID: id, Namespace: targetNamespace, MemoryState: conversationpersistence.MemoryStateConfirmed}
	return &s.promotedEvent, nil
}

func TestHandleMemoryState(t *testing.T) {
	store := &memoryActionStoreStub{}
	body := bytes.NewBufferString(`{"id":"mem-1","memory_state":"confirmed"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/memory/state", body)
	rec := httptest.NewRecorder()

	HandleMemoryState(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.stateID != "mem-1" || store.memoryState != "confirmed" {
		t.Fatalf("unexpected store call: %+v", store)
	}
}

func TestHandleMemoryPromote(t *testing.T) {
	store := &memoryActionStoreStub{}
	body := bytes.NewBufferString(`{"id":"mem-1","target_kind":"user","target_id":"ren","promoted_by":"viewer"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/memory/promote", body)
	rec := httptest.NewRecorder()

	HandleMemoryPromote(store)(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.promoteID != "mem-1" || store.targetNS != "user:ren" || store.promotedBy != "viewer" {
		t.Fatalf("unexpected store call: %+v", store)
	}
	var out struct {
		Item conversationpersistence.L1MemoryEvent `json:"item"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.Item.Namespace != "user:ren" {
		t.Fatalf("unexpected response: %+v", out)
	}
}

func TestHandleMemoryPromoteRejectsInvalidTargetNamespace(t *testing.T) {
	store := &memoryActionStoreStub{}
	body := bytes.NewBufferString(`{"id":"mem-1","target_kind":"misc","target_id":"ren"}`)
	req := httptest.NewRequest(http.MethodPost, "/viewer/memory/promote", body)
	rec := httptest.NewRecorder()

	HandleMemoryPromote(store)(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
	if store.promoteID != "" {
		t.Fatalf("store should not be called for invalid target: %+v", store)
	}
}
