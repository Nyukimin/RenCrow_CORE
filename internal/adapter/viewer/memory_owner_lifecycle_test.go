package viewer

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

func memoryOwnerLifecycleRequest(method, path, body, profile string) *http.Request {
	req := httptest.NewRequest(method, "http://127.0.0.1"+path, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:18790"
	req.Header.Set("Authorization", "Bearer owner-token-012345678901234567890123")
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", profile)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestMemoryOwnerLifecyclePlanAcceptsOnlyEmptyBodyFormsAndUsesTypedOwnerScope(t *testing.T) {
	for _, body := range []string{"", "{}", "{ }", " \n{} \t"} {
		t.Run(strings.ReplaceAll(body, "\n", "\\n"), func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerLifecycleRequest(http.MethodPost, "/viewer/memory/lifecycle/plan", body, "cmd-control"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
			}
			if store.lifecyclePlanRequestID == "" || store.lifecyclePlanOwnerID != "ren" || store.lifecyclePlanActorID != "ren" {
				t.Fatalf("plan args=%+v, want generated request and configured owner/actor", store)
			}
			if scope := store.lifecyclePlanScope; scope.RequestID != store.lifecyclePlanRequestID || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != "ren" || scope.AuthenticatedUserID != "ren" || !scope.Allows(domaintool.DataScopeUser) || scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
				t.Fatalf("plan scope=%+v, want trusted configured user scope", scope)
			}
			assertLifecycleResponseDoesNotLeakStorage(t, rec)
		})
	}
}

func TestMemoryOwnerLifecycleRunUsesStrictBodyAndTypedOwnerScope(t *testing.T) {
	store := &memoryOwnerStoreStub{}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	rec := httptest.NewRecorder()
	h(rec, memoryOwnerLifecycleRequest(http.MethodPost, "/viewer/memory/lifecycle/run", `{"plan_request_id":" plan-1 ","reason":" apply retention ","apply":true}`, "cmd-control"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if store.lifecycleRunRequestID == "" || store.lifecycleRunOwnerID != "ren" || store.lifecycleRunActorID != "ren" || store.lifecycleRunPlanID != "plan-1" || store.lifecycleRunReason != "apply retention" || !store.lifecycleRunApply {
		t.Fatalf("run args=%+v, want normalized body and generated owner scope", store)
	}
	if scope := store.lifecycleRunScope; scope.RequestID != store.lifecycleRunRequestID || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != "ren" || scope.AuthenticatedUserID != "ren" || !scope.Allows(domaintool.DataScopeUser) || scope.AuthenticationSource != domaintool.AuthenticationSourceHTTP {
		t.Fatalf("run scope=%+v, want trusted configured user scope", scope)
	}
	assertLifecycleResponseDoesNotLeakStorage(t, rec)
}

func TestMemoryOwnerLifecycleRejectsInvalidBodiesAndQueries(t *testing.T) {
	cases := []struct {
		name string
		path string
		body string
	}{
		{name: "plan unknown field", path: "/viewer/memory/lifecycle/plan", body: `{"reason":"caller"}`},
		{name: "plan array", path: "/viewer/memory/lifecycle/plan", body: `[]`},
		{name: "plan null", path: "/viewer/memory/lifecycle/plan", body: `null`},
		{name: "plan trailing json", path: "/viewer/memory/lifecycle/plan", body: `{} {}`},
		{name: "run missing apply", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":"p","reason":"r"}`},
		{name: "run apply false", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":"p","reason":"r","apply":false}`},
		{name: "run blank plan", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":" ","reason":"r","apply":true}`},
		{name: "run blank reason", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":"p","reason":" ","apply":true}`},
		{name: "run unknown field", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":"p","reason":"r","apply":true,"user_id":"caller"}`},
		{name: "run wrong case field", path: "/viewer/memory/lifecycle/run", body: `{"Plan_Request_ID":"p","reason":"r","apply":true}`},
		{name: "run duplicate field", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":"p","reason":"r","reason":"other","apply":true}`},
		{name: "run trailing json", path: "/viewer/memory/lifecycle/run", body: `{"plan_request_id":"p","reason":"r","apply":true}{}`},
		{name: "plan caller query", path: "/viewer/memory/lifecycle/plan?user_id=caller", body: `{}`},
		{name: "run caller query", path: "/viewer/memory/lifecycle/run?request_id=caller", body: `{"plan_request_id":"p","reason":"r","apply":true}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerLifecycleRequest(http.MethodPost, tc.path, tc.body, "cmd-control"))
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s, want 400", rec.Code, rec.Body.String())
			}
			if store.lifecyclePlanRequestID != "" || store.lifecycleRunRequestID != "" {
				t.Fatalf("invalid request reached store: %+v", store)
			}
		})
	}
}

func TestMemoryOwnerLifecycleRoutesRequireExactPath(t *testing.T) {
	h := NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123"))
	for _, path := range []string{
		"/viewer/memory/lifecycle/plan/",
		"/viewer/memory/lifecycle/run/",
	} {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerLifecycleRequest(http.MethodPost, path, `{}`, "cmd-control"))
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status=%d body=%s, want exact route 404", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestMemoryOwnerLifecycleMapsAuthProfileAndStoreSentinels(t *testing.T) {
	cases := []struct {
		name    string
		request *http.Request
		handler http.Handler
		want    int
		outcome string
	}{
		{
			name: "missing bearer",
			request: func() *http.Request {
				req := memoryOwnerLifecycleRequest(http.MethodPost, "/viewer/memory/lifecycle/plan", `{}`, "cmd-control")
				req.Header.Del("Authorization")
				return req
			}(),
			handler: NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123")),
			want:    http.StatusUnauthorized,
			outcome: "rejected",
		},
		{
			name:    "diagnostics profile",
			request: memoryOwnerLifecycleRequest(http.MethodPost, "/viewer/memory/lifecycle/plan", `{}`, "cmd-diagnostics"),
			handler: NewMemoryOwnerHandler(&memoryOwnerStoreStub{}, "ren", []byte("owner-token-012345678901234567890123")),
			want:    http.StatusForbidden,
			outcome: "rejected",
		},
	}
	for _, sentinel := range []struct {
		err     error
		status  int
		outcome string
	}{
		{domainmemory.ErrUserMemoryOwnerInvalid, http.StatusBadRequest, "rejected"},
		{domainmemory.ErrUserMemoryOwnerNotFound, http.StatusNotFound, "rejected"},
		{domainmemory.ErrUserMemoryOwnerForbidden, http.StatusForbidden, "rejected"},
		{domainmemory.ErrUserMemoryOwnerConflict, http.StatusConflict, "rejected"},
		{domainmemory.ErrUserMemoryOwnerUnavailable, http.StatusServiceUnavailable, "blocked"},
	} {
		name := sentinel.err.Error()
		cases = append(cases, struct {
			name    string
			request *http.Request
			handler http.Handler
			want    int
			outcome string
		}{
			name:    name,
			request: memoryOwnerLifecycleRequest(http.MethodPost, "/viewer/memory/lifecycle/run", `{"plan_request_id":"p","reason":"r","apply":true}`, "cmd-control"),
			handler: NewMemoryOwnerHandler(&memoryOwnerStoreStub{lifecycleRunErr: sentinel.err}, "ren", []byte("owner-token-012345678901234567890123")),
			want:    sentinel.status,
			outcome: sentinel.outcome,
		})
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			tc.handler.ServeHTTP(rec, tc.request)
			if rec.Code != tc.want {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.want)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if body["status"] != tc.outcome {
				t.Fatalf("outcome=%v, want %q", body["status"], tc.outcome)
			}
		})
	}
}

func assertLifecycleResponseDoesNotLeakStorage(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode lifecycle response: %v", err)
	}
	raw := rec.Body.String()
	for _, forbidden := range []string{`"statement"`, `"raw"`, `"meta"`, `"path"`, `"sql"`, `"user_id"`} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("lifecycle response leaked %s: %s", forbidden, raw)
		}
	}
	if _, ok := body["receipt"]; !ok {
		t.Fatalf("lifecycle response missing typed receipt: %s", raw)
	}
}
