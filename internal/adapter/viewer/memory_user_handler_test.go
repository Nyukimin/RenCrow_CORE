package viewer

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/picoclaw_multiLLM/internal/domain/memory"
)

type userMemoryStoreStub struct {
	createInput     domainmemory.CreateUserMemoryInput
	updateID        string
	updateState     string
	updateReason    string
	forgetID        string
	forgetReason    string
	supersedeOldID  string
	supersedeNewID  string
	supersedeReason string
	listUserID      string
	listState       string
	listInactive    bool
	listLimit       int
	items           []domainmemory.UserMemory
}

func (s *userMemoryStoreStub) CreateUserMemory(_ context.Context, input domainmemory.CreateUserMemoryInput) (*domainmemory.UserMemory, error) {
	s.createInput = input
	item := domainmemory.UserMemory{
		ID:               "mem-created",
		Namespace:        "user:" + strings.TrimSpace(input.UserID),
		UserID:           strings.TrimSpace(input.UserID),
		Type:             strings.TrimSpace(input.Type),
		Statement:        strings.TrimSpace(input.Statement),
		EvidenceEventIDs: input.EvidenceEventIDs,
		Confidence:       input.Confidence,
		Sensitivity:      input.Sensitivity,
		State:            input.State,
		Active:           true,
		CreatedAt:        time.Now().UTC(),
		UpdatedAt:        time.Now().UTC(),
	}
	return &item, nil
}

func (s *userMemoryStoreStub) ListUserMemories(_ context.Context, userID string, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	s.listUserID = userID
	s.listState = state
	s.listInactive = includeInactive
	s.listLimit = limit
	return append([]domainmemory.UserMemory(nil), s.items...), nil
}

func (s *userMemoryStoreStub) UpdateUserMemoryState(_ context.Context, id string, state string, reason string) (*domainmemory.UserMemory, error) {
	s.updateID = id
	s.updateState = state
	s.updateReason = reason
	return &domainmemory.UserMemory{ID: id, UserID: "ren", Namespace: "user:ren", State: state, Active: true}, nil
}

func (s *userMemoryStoreStub) ForgetUserMemory(_ context.Context, id string, reason string) (*domainmemory.UserMemory, error) {
	s.forgetID = id
	s.forgetReason = reason
	return &domainmemory.UserMemory{ID: id, UserID: "ren", Namespace: "user:ren", State: domainmemory.MemoryStateConfirmed, Active: false}, nil
}

func (s *userMemoryStoreStub) SupersedeUserMemory(_ context.Context, oldID string, newID string, reason string) (*domainmemory.UserMemory, error) {
	s.supersedeOldID = oldID
	s.supersedeNewID = newID
	s.supersedeReason = reason
	return &domainmemory.UserMemory{ID: oldID, UserID: "ren", Namespace: "user:ren", SupersededBy: newID, Active: false}, nil
}

func TestHandleUserMemoryCreateAndList(t *testing.T) {
	store := &userMemoryStoreStub{
		items: []domainmemory.UserMemory{{
			ID:         "mem-1",
			Namespace:  "user:ren",
			UserID:     "ren",
			Type:       domainmemory.UserMemoryTypePreference,
			Statement:  "短く論理的な説明を好む",
			State:      domainmemory.MemoryStateCandidate,
			Active:     true,
			Confidence: 0.7,
		}},
	}
	h := HandleUserMemory(store)

	body := bytes.NewBufferString(`{"user_id":"ren","type":"preference","statement":"短く論理的な説明を好む","state":"candidate","evidence_event_ids":["evt-1"],"confidence":0.7,"sensitivity":"normal","scope":"global","source":"test"}`)
	createReq := httptest.NewRequest(http.MethodPost, "/viewer/memory/user", body)
	createRec := httptest.NewRecorder()
	h(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("expected create 200, got %d: %s", createRec.Code, createRec.Body.String())
	}
	if store.createInput.UserID != "ren" || store.createInput.Type != "preference" || store.createInput.Statement == "" {
		t.Fatalf("unexpected create input: %+v", store.createInput)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/viewer/memory/user?user_id=ren&state=candidate&include_inactive=true&limit=3", nil)
	listRec := httptest.NewRecorder()
	h(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list 200, got %d: %s", listRec.Code, listRec.Body.String())
	}
	if store.listUserID != "ren" || store.listState != "candidate" || !store.listInactive || store.listLimit != 3 {
		t.Fatalf("unexpected list args: %+v", store)
	}
	var out struct {
		UserID string                    `json:"user_id"`
		Items  []domainmemory.UserMemory `json:"items"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &out); err != nil {
		t.Fatalf("invalid json: %v", err)
	}
	if out.UserID != "ren" || len(out.Items) != 1 || out.Items[0].ID != "mem-1" {
		t.Fatalf("unexpected list output: %+v", out)
	}
}

func TestHandleUserMemoryStateForgetSupersede(t *testing.T) {
	store := &userMemoryStoreStub{}

	stateReq := httptest.NewRequest(http.MethodPost, "/viewer/memory/user/state", bytes.NewBufferString(`{"id":"mem-1","state":"confirmed","reason":"reviewed"}`))
	stateRec := httptest.NewRecorder()
	HandleUserMemoryState(store)(stateRec, stateReq)
	if stateRec.Code != http.StatusOK {
		t.Fatalf("expected state 200, got %d: %s", stateRec.Code, stateRec.Body.String())
	}
	if store.updateID != "mem-1" || store.updateState != "confirmed" || store.updateReason != "reviewed" {
		t.Fatalf("unexpected state args: %+v", store)
	}

	forgetReq := httptest.NewRequest(http.MethodPost, "/viewer/memory/user/forget", bytes.NewBufferString(`{"id":"mem-1","reason":"これは違う"}`))
	forgetRec := httptest.NewRecorder()
	HandleUserMemoryForget(store)(forgetRec, forgetReq)
	if forgetRec.Code != http.StatusOK {
		t.Fatalf("expected forget 200, got %d: %s", forgetRec.Code, forgetRec.Body.String())
	}
	if store.forgetID != "mem-1" || store.forgetReason != "これは違う" {
		t.Fatalf("unexpected forget args: %+v", store)
	}

	supersedeReq := httptest.NewRequest(http.MethodPost, "/viewer/memory/user/supersede", bytes.NewBufferString(`{"id":"mem-1","superseded_by":"mem-2","reason":"newer fact"}`))
	supersedeRec := httptest.NewRecorder()
	HandleUserMemorySupersede(store)(supersedeRec, supersedeReq)
	if supersedeRec.Code != http.StatusOK {
		t.Fatalf("expected supersede 200, got %d: %s", supersedeRec.Code, supersedeRec.Body.String())
	}
	if store.supersedeOldID != "mem-1" || store.supersedeNewID != "mem-2" || store.supersedeReason != "newer fact" {
		t.Fatalf("unexpected supersede args: %+v", store)
	}
}
