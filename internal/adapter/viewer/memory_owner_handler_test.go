package viewer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	"github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/conversation/l1sqlite"
)

type memoryOwnerStoreStub struct {
	items         []domainmemory.UserMemory
	listUserID    string
	listScope     domaintool.ToolExecutionScope
	proposeScope  domaintool.ToolExecutionScope
	findID        string
	proposeID     string
	proposeUserID string
	proposeActor  string
	proposeType   string
	proposeText   string
	proposeReason string
	transitionOp  string
	transitionID  string
	transitionNew string
	transitionWhy string
	requestID     string
}

func (s *memoryOwnerStoreStub) OwnerListUserMemories(ctx context.Context, userID, state string, includeInactive bool, limit int) ([]domainmemory.UserMemory, error) {
	s.listUserID = userID
	s.listScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return append([]domainmemory.UserMemory(nil), s.items...), nil
}

func (s *memoryOwnerStoreStub) OwnerFindUserMemory(_ context.Context, _, id string) (domainmemory.UserMemory, error) {
	s.findID = id
	for _, item := range s.items {
		if item.ID == id {
			return item, nil
		}
	}
	return domainmemory.UserMemory{}, domainmemory.ErrUserMemoryOwnerNotFound
}

func (s *memoryOwnerStoreStub) OwnerProposeUserMemory(ctx context.Context, requestID, userID, actorID, memoryType, statement, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	s.requestID, s.proposeID, s.proposeUserID, s.proposeActor = requestID, requestID, userID, actorID
	s.proposeType, s.proposeText, s.proposeReason = memoryType, statement, reason
	s.proposeScope, _ = domaintool.ToolExecutionScopeFromContext(ctx)
	return domainmemory.UserMemoryOwnerResult{
		Item:    domainmemory.UserMemoryOwnerView{ID: "owner-proposed", Type: memoryType, Statement: statement, State: domainmemory.MemoryStateCandidate, Active: true},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: domainmemory.UserMemoryOwnerOperationPropose, Status: "completed", OwnerRoute: "conversation_l1/user_memory/propose", PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision, IdempotencyKey: requestID, InputCount: 1, OutputCount: 1, Warnings: []string{}, AuditReference: "owner-proposed", CompletedAt: time.Now().UTC()},
	}, nil
}

func (s *memoryOwnerStoreStub) OwnerTransitionUserMemory(_ context.Context, requestID, userID, actorID, id, operation, replacementID, reason string) (domainmemory.UserMemoryOwnerResult, error) {
	s.requestID, s.proposeUserID, s.proposeActor = requestID, userID, actorID
	s.transitionOp, s.transitionID, s.transitionNew, s.transitionWhy = operation, id, replacementID, reason
	return domainmemory.UserMemoryOwnerResult{
		Item:    domainmemory.UserMemoryOwnerView{ID: id, Type: domainmemory.UserMemoryTypePreference, Statement: "owner item", State: domainmemory.MemoryStateConfirmed, Active: true},
		Receipt: domainmemory.UserMemoryOwnerReceipt{RequestID: requestID, Operation: operation, Status: "completed", OwnerRoute: "conversation_l1/user_memory/" + operation, PolicyRevision: domainmemory.UserMemoryOwnerPolicyRevision, IdempotencyKey: requestID, InputCount: 1, OutputCount: 1, Warnings: []string{}, AuditReference: id, CompletedAt: time.Now().UTC()},
	}, nil
}

func TestMemoryOwnerHandlerAuthenticatesLoopbackAndUsesConfiguredUserScope(t *testing.T) {
	store := &memoryOwnerStoreStub{items: []domainmemory.UserMemory{{ID: "mem-1", UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypePreference, Statement: "short answers", State: domainmemory.MemoryStateCandidate, Active: true}}}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user?limit=3", nil)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	rec := httptest.NewRecorder()
	h(rewritableResponseWriter{ResponseRecorder: rec}, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if store.listUserID != "ren" || store.listScope.AuthenticatedUserID != "ren" || !store.listScope.Allows(domaintool.DataScopeUser) || store.listScope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		t.Fatalf("owner scope/user not propagated: user=%q scope=%+v", store.listUserID, store.listScope)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if receipt, ok := body["receipt"].(map[string]any); !ok || receipt["operation"] != domainmemory.UserMemoryOwnerOperationList || receipt["status"] != "completed" {
		t.Fatalf("missing list receipt: %#v", body)
	}
	raw, _ := json.Marshal(body)
	for _, forbidden := range []string{"namespace", "user_id", "source", "meta", "sql", "path"} {
		if strings.Contains(string(raw), "\""+forbidden+"\"") {
			t.Fatalf("safe owner response leaked %q: %s", forbidden, raw)
		}
	}
}

func TestMemoryOwnerHandlerRejectsRemoteAndProfileOrBearerMismatch(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	tests := []struct {
		name    string
		remote  string
		token   string
		client  string
		profile string
		want    int
	}{
		{name: "remote", remote: "192.0.2.5:18790", token: "owner-token-012345678901234567890123", client: "RenCrow_CMD", profile: "cmd-diagnostics", want: http.StatusNotFound},
		{name: "bearer", remote: "127.0.0.1:18790", token: "wrong-token", client: "RenCrow_CMD", profile: "cmd-diagnostics", want: http.StatusUnauthorized},
		{name: "profile", remote: "127.0.0.1:18790", token: "owner-token-012345678901234567890123", client: "RenCrow_CMD", profile: "cmd-control", want: http.StatusForbidden},
		{name: "client", remote: "127.0.0.1:18790", token: "owner-token-012345678901234567890123", client: "RenCrow_PORTAL", profile: "cmd-diagnostics", want: http.StatusForbidden},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user", nil)
			req.RemoteAddr = tt.remote
			req.Header.Set("Authorization", "Bearer "+tt.token)
			req.Header.Set("X-RenCrow-Client", tt.client)
			req.Header.Set("X-RenCrow-Interaction-Profile", tt.profile)
			rec := httptest.NewRecorder()
			h(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestMemoryOwnerHandlerRejectsCallerOwnedQueryParameters(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodGet, "http://127.0.0.1/v1/memory/user?user_id=other", nil)
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestMemoryOwnerHandlerProposeAndTransitionAreStrictAndReceiptBound(t *testing.T) {
	store := &memoryOwnerStoreStub{}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	newRequest := func(method, path, body, profile string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		req.Header.Set("Content-Type", "application/json")
		return req
	}

	propose := httptest.NewRecorder()
	h(propose, newRequest(http.MethodPost, "/v1/memory/user/propose", `{"type":"preference","statement":"short answers","reason":"operator confirmed"}`, "cmd-control"))
	if propose.Code != http.StatusOK || store.proposeUserID != "ren" || store.proposeActor != "ren" || store.proposeType != "preference" || store.proposeText != "short answers" || store.proposeReason != "operator confirmed" {
		t.Fatalf("propose status/body=%d/%s store=%+v", propose.Code, propose.Body.String(), store)
	}
	if store.requestID == "" {
		t.Fatal("CORE must generate a request ID")
	}
	if store.proposeScope.RequestID != store.requestID {
		t.Fatalf("store request id=%q scope request id=%q", store.requestID, store.proposeScope.RequestID)
	}
	var proposeBody map[string]any
	if err := json.Unmarshal(propose.Body.Bytes(), &proposeBody); err != nil {
		t.Fatalf("decode propose: %v", err)
	}
	if receipt, ok := proposeBody["receipt"].(map[string]any); !ok || receipt["status"] != "completed" || receipt["idempotency_key"] != store.requestID {
		t.Fatalf("invalid propose receipt: %#v", proposeBody)
	}

	bad := httptest.NewRecorder()
	h(bad, newRequest(http.MethodPost, "/v1/memory/user/propose", `{"type":"preference","statement":"short answers","reason":"ok","request_id":"caller-controlled"}`, "cmd-control"))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown request_id status=%d body=%s", bad.Code, bad.Body.String())
	}

	transition := httptest.NewRecorder()
	h(transition, newRequest(http.MethodPost, "/v1/memory/user/mem-1/confirm", `{"reason":"reviewed"}`, "cmd-control"))
	if transition.Code != http.StatusOK || store.transitionOp != domainmemory.UserMemoryOwnerOperationConfirm || store.transitionID != "mem-1" || store.transitionWhy != "reviewed" {
		t.Fatalf("transition status/body=%d/%s store=%+v", transition.Code, transition.Body.String(), store)
	}
}

func TestMemoryOwnerHandlerUnescapesSlashMemoryIDs(t *testing.T) {
	const id = "user-memory-candidate/sha256:abc"
	store := &memoryOwnerStoreStub{items: []domainmemory.UserMemory{{ID: id, UserID: "ren", Namespace: "user:ren", Type: domainmemory.UserMemoryTypePreference, Statement: "short answers", State: domainmemory.MemoryStateCandidate, Active: true}}}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	request := func(method, path, body string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	escaped := url.PathEscape(id)
	show := request(http.MethodGet, "/v1/memory/user/"+escaped, "")
	show.Header.Set("X-RenCrow-Interaction-Profile", "cmd-diagnostics")
	showRec := httptest.NewRecorder()
	h(showRec, show)
	if showRec.Code != http.StatusOK || store.findID != id {
		t.Fatalf("show status=%d body=%s id=%q", showRec.Code, showRec.Body.String(), store.findID)
	}
	var showBody map[string]any
	if err := json.Unmarshal(showRec.Body.Bytes(), &showBody); err != nil {
		t.Fatalf("decode show: %v", err)
	}
	if receipt, ok := showBody["receipt"].(map[string]any); !ok || receipt["operation"] != domainmemory.UserMemoryOwnerOperationShow || receipt["status"] != "completed" {
		t.Fatalf("missing show receipt: %#v", showBody)
	}
	confirm := httptest.NewRecorder()
	h(confirm, request(http.MethodPost, "/v1/memory/user/"+escaped+"/confirm", `{"reason":"reviewed"}`))
	if confirm.Code != http.StatusOK || store.transitionID != id || store.transitionOp != domainmemory.UserMemoryOwnerOperationConfirm {
		t.Fatalf("confirm status=%d body=%s id=%q operation=%q", confirm.Code, confirm.Body.String(), store.transitionID, store.transitionOp)
	}
}

func TestMemoryOwnerHandlerRealStoreUsesEscapedIDsForEveryMutation(t *testing.T) {
	store, err := l1sqlite.NewL1SQLiteStore(filepath.Join(t.TempDir(), "owner-e2e.db"))
	if err != nil {
		t.Fatalf("NewL1SQLiteStore: %v", err)
	}
	defer store.Close()
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	request := func(method, path, body, profile string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	write := func(method, path, body string) domainmemory.UserMemoryOwnerResult {
		rec := httptest.NewRecorder()
		h(rec, request(method, path, body, "cmd-control"))
		if rec.Code != http.StatusOK {
			t.Fatalf("write %s status=%d body=%s", path, rec.Code, rec.Body.String())
		}
		var result domainmemory.UserMemoryOwnerResult
		if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return result
	}
	first := write(http.MethodPost, "/v1/memory/user/propose", `{"type":"preference","statement":"short answers","reason":"operator confirmed"}`)
	firstID := first.Item.ID
	if !strings.Contains(firstID, "/") {
		t.Fatalf("candidate ID=%q must exercise escaped path segment", firstID)
	}
	show := httptest.NewRecorder()
	h(show, request(http.MethodGet, "/v1/memory/user/"+url.PathEscape(firstID), "", "cmd-diagnostics"))
	if show.Code != http.StatusOK {
		t.Fatalf("show status=%d body=%s", show.Code, show.Body.String())
	}
	confirmed := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(firstID)+"/confirm", `{"reason":"reviewed"}`)
	if confirmed.Item.State != domainmemory.MemoryStateConfirmed {
		t.Fatalf("confirmed=%+v", confirmed.Item)
	}
	pinned := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(firstID)+"/pin", `{"reason":"keep fixed"}`)
	if pinned.Item.State != domainmemory.MemoryStatePinned {
		t.Fatalf("pinned=%+v", pinned.Item)
	}
	second := write(http.MethodPost, "/v1/memory/user/propose", `{"type":"project","statement":"owner project","reason":"operator confirmed"}`)
	superseded := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(firstID)+"/supersede", `{"replacement_id":"`+second.Item.ID+`","reason":"newer item"}`)
	if superseded.Item.Active || superseded.Item.SupersededBy != second.Item.ID {
		t.Fatalf("superseded=%+v", superseded.Item)
	}
	forgotten := write(http.MethodPost, "/v1/memory/user/"+url.PathEscape(second.Item.ID)+"/forget", `{"reason":"remove item"}`)
	if forgotten.Item.Active {
		t.Fatalf("forgotten=%+v", forgotten.Item)
	}
}

// rewritableResponseWriter keeps the test compatible with handlers that set
// response headers before writing JSON.
type rewritableResponseWriter struct{ *httptest.ResponseRecorder }
