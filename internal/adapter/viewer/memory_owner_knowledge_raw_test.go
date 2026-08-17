package viewer

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	domainmemory "github.com/Nyukimin/RenCrow_CORE/internal/domain/memory"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
)

const knowledgeBackfillPath = "/viewer/memory/knowledge-raw/backfill"

func TestMemoryOwnerKnowledgeBackfillDefaultsToDryRunAndUsesTypedOwnerScope(t *testing.T) {
	for _, body := range []string{"", "{}", `{"apply":false}`} {
		t.Run("body="+body, func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerLifecycleRequest(http.MethodPost, knowledgeBackfillPath, body, "cmd-control"))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
			}
			if store.knowledgeBackfillCalls != 1 || store.knowledgeBackfillApply {
				t.Fatalf("backfill calls=%d apply=%v, want one dry-run call", store.knowledgeBackfillCalls, store.knowledgeBackfillApply)
			}
			if store.knowledgeBackfillReqID == "" || store.knowledgeBackfillOwner != "ren" || store.knowledgeBackfillActor != "ren" {
				t.Fatalf("backfill args=%+v, want generated request and configured owner/actor", store)
			}
			scope := store.knowledgeBackfillScope
			if scope.RequestID != store.knowledgeBackfillReqID || scope.ActorKind != domaintool.ActorKindUser || scope.ActorID != "ren" || scope.AuthenticatedUserID != "ren" || !scope.Allows(domaintool.DataScopeUser) {
				t.Fatalf("backfill scope=%+v, want trusted configured user scope", scope)
			}
		})
	}
}

func TestMemoryOwnerKnowledgeBackfillApplyRelaysStoreResult(t *testing.T) {
	store := &memoryOwnerStoreStub{}
	h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
	rec := httptest.NewRecorder()
	h(rec, memoryOwnerLifecycleRequest(http.MethodPost, knowledgeBackfillPath, `{"apply":true}`, "cmd-control"))
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s, want 200", rec.Code, rec.Body.String())
	}
	if !store.knowledgeBackfillApply {
		t.Fatal("apply=true was not relayed to the store")
	}
	payload := rec.Body.String()
	for _, want := range []string{`"status":"completed"`, `"ready":true`, `"manifest_id":"knowledge-manifest"`} {
		if !strings.Contains(payload, want) {
			t.Fatalf("response %s does not contain %s", payload, want)
		}
	}
}

func TestMemoryOwnerKnowledgeBackfillRejectsInvalidRequests(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		profile string
		method  string
		status  int
	}{
		{name: "unknown key", body: `{"reason":"x"}`, profile: "cmd-control", method: http.MethodPost, status: http.StatusBadRequest},
		{name: "duplicate key", body: `{"apply":true,"apply":true}`, profile: "cmd-control", method: http.MethodPost, status: http.StatusBadRequest},
		{name: "array body", body: `[]`, profile: "cmd-control", method: http.MethodPost, status: http.StatusBadRequest},
		{name: "diagnostics profile", body: `{}`, profile: "cmd-diagnostics", method: http.MethodPost, status: http.StatusForbidden},
		{name: "get method", body: "", profile: "cmd-control", method: http.MethodGet, status: http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryOwnerStoreStub{}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerLifecycleRequest(tc.method, knowledgeBackfillPath, tc.body, tc.profile))
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
			if store.knowledgeBackfillCalls != 0 {
				t.Fatalf("store was called %d times for a rejected request", store.knowledgeBackfillCalls)
			}
		})
	}
}

func TestMemoryOwnerKnowledgeBackfillMapsCommonRawErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{name: "forbidden scope", err: domainmemory.NewCommonRawError(domainmemory.CommonRawErrorForbidden, "scope"), status: http.StatusForbidden},
		{name: "source changed", err: domainmemory.NewCommonRawError(domainmemory.CommonRawErrorSourceChanged, "hash"), status: http.StatusConflict},
		{name: "unavailable", err: domainmemory.NewCommonRawError(domainmemory.CommonRawErrorUnavailable, "store"), status: http.StatusServiceUnavailable},
		{name: "invalid empty", err: domainmemory.NewCommonRawError(domainmemory.CommonRawErrorInvalid, "empty"), status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &memoryOwnerStoreStub{knowledgeBackfillErr: tc.err}
			h := NewMemoryOwnerHandler(store, "ren", []byte("owner-token-012345678901234567890123"))
			rec := httptest.NewRecorder()
			h(rec, memoryOwnerLifecycleRequest(http.MethodPost, knowledgeBackfillPath, `{"apply":true}`, "cmd-control"))
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s, want %d", rec.Code, rec.Body.String(), tc.status)
			}
		})
	}
}
