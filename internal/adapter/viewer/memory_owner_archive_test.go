package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
)

func TestMemoryOwnerArchiveRouteIsRegisteredAndReturnsTypedSuccess(t *testing.T) {
	store := &memoryOwnerStoreStub{}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/viewer/memory/user/archive", strings.NewReader(`{"memory_id":"memory-1","reason":"retain exact copy"}`))
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want archive route success", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	receipt, ok := body["receipt"].(map[string]any)
	if !ok || receipt["operation"] != "archive" || receipt["owner_route"] != "conversation_archive/user_memory/archive" {
		t.Fatalf("archive receipt=%#v", body["receipt"])
	}
	if store.archiveUserID != "ren" || store.archiveActor != "ren" || store.archiveID != "memory-1" || store.archiveReason != "retain exact copy" ||
		store.archiveScope.AuthenticatedUserID != "ren" || store.archiveScope.ActorID != "ren" || !store.archiveScope.Allows("user") || store.archiveScope.RequestID != store.requestID {
		t.Fatalf("archive owner scope/request was not propagated: store=%+v", store)
	}
	for _, forbidden := range []string{"namespace", "user_id", "source", "meta", "sql", "path", "hash"} {
		if strings.Contains(rec.Body.String(), `"`+forbidden+`"`) {
			t.Fatalf("archive response leaked %q: %s", forbidden, rec.Body.String())
		}
	}
}

func TestMemoryOwnerArchiveRouteStrictBodyAndErrorProjection(t *testing.T) {
	newRequest := func(method, path, body, profile string) *http.Request {
		req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:18790"
		req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	for _, body := range []string{
		`{"memory_id":"memory-1"}`,
		`{"memory_id":"memory-1","reason":" "}`,
		`{"memory_id":"memory-1","reason":"retain","user_id":"caller"}`,
		`{"memory_id":"memory-1","reason":"retain","request_id":"caller"}`,
		`{"memory_id":"memory-1","reason":"retain","idempotency_key":"caller"}`,
		`{"memory_id":"memory-1","reason":"retain","db_path":"/tmp/private"}`,
		`{"memory_id":"memory-1","reason":"retain","path":"/tmp/private"}`,
		`{"memory_id":"memory-1","reason":"retain","payload_hash":"caller"}`,
	} {
		rec := httptest.NewRecorder()
		NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))(rec, newRequest(http.MethodPost, "/viewer/memory/user/archive", body, "cmd-control"))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d response=%s, want 400", body, rec.Code, rec.Body.String())
		}
	}
	rec := httptest.NewRecorder()
	NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))(rec, newRequest(http.MethodPost, "/viewer/memory/user/archive?user_id=caller", `{"memory_id":"memory-1","reason":"retain"}`, "cmd-control"))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("query status=%d body=%s, want 400", rec.Code, rec.Body.String())
	}

	for _, tc := range []struct {
		err  error
		want int
	}{
		{err: domainmemory.ErrUserMemoryOwnerNotFound, want: http.StatusNotFound},
		{err: domainmemory.ErrUserMemoryOwnerConflict, want: http.StatusConflict},
		{err: domainmemory.ErrUserMemoryOwnerForbidden, want: http.StatusForbidden},
		{err: domainmemory.ErrUserMemoryOwnerUnavailable, want: http.StatusServiceUnavailable},
	} {
		rec := httptest.NewRecorder()
		store := &memoryOwnerStoreStub{archiveErr: tc.err}
		NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))(rec, newRequest(http.MethodPost, "/viewer/memory/user/archive", `{"memory_id":"memory-1","reason":"retain"}`, "cmd-control"))
		if rec.Code != tc.want {
			t.Fatalf("err=%v status=%d body=%s want=%d", tc.err, rec.Code, rec.Body.String(), tc.want)
		}
		var response map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode error response: %v", err)
		}
		if response["status"] != map[int]string{404: "rejected", 409: "rejected", 403: "rejected", 503: "blocked"}[tc.want] {
			t.Fatalf("error outcome=%v for status=%d", response["status"], tc.want)
		}
	}
}

func TestMemoryOwnerArchiveRouteRequiresBearerAndControlProfile(t *testing.T) {
	newRequest := func(token, profile string) *http.Request {
		req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/viewer/memory/user/archive", strings.NewReader(`{"memory_id":"memory-1","reason":"retain"}`))
		req.RemoteAddr = "127.0.0.1:18790"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
		if profile != "" {
			req.Header.Set("X-RenCrow-Interaction-Profile", profile)
		}
		req.Header.Set("Content-Type", "application/json")
		return req
	}
	for _, tc := range []struct {
		name    string
		token   string
		profile string
		want    int
	}{
		{name: "missing bearer", profile: "cmd-control", want: http.StatusUnauthorized},
		{name: "wrong profile", token: "owner-token-012345678901234567890123", profile: "cmd-diagnostics", want: http.StatusForbidden},
		{name: "missing profile", token: "owner-token-012345678901234567890123", want: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))(rec, newRequest(tc.token, tc.profile))
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s want=%d", rec.Code, rec.Body.String(), tc.want)
			}
		})
	}
}
