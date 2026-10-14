package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager/taskmanagertest"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	taskpersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/task"
)

// Only a full operations slot (the Task owner refused to admit the Task, and
// persisted nothing) answers with the operations_task_limit code and
// Retry-After. Every other unavailable response keeps its status and its
// generic runtime_unavailable body, and carries no Retry-After: that header
// would tell a client that waiting helps, which is not known for them.
func requireGenericAgentOpsFailure(t *testing.T, rec *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rec.Code != wantStatus || strings.TrimSpace(rec.Body.String()) != `{"error":"runtime_unavailable"}` {
		t.Fatalf("status=%d body=%q, want %d with the generic runtime_unavailable body", rec.Code, rec.Body.String(), wantStatus)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After=%q on a response that is not a capacity refusal", got)
	}
}

func TestAgentOpsLegacyAdmissionFailureOtherThanCapacityKeepsTheGenericCode(t *testing.T) {
	baseStore, err := taskpersistence.NewJSONLStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	faultStore := &agentOpsFaultTaskStore{Store: baseStore}
	faultStore.failTransactions.Store(true)
	owner := taskmanager.New(faultStore, taskmanager.DefaultParallelLimits())
	t.Cleanup(func() {
		if err := owner.Close(); err != nil {
			t.Errorf("close Task owner: %v", err)
		}
	})
	executor := &agentOpsExecutorStub{output: "must not run"}
	handler := newLegacyOPSHandler(t, owner, executor, &legacyOPSLedger{})
	rec := postLegacyOPS(handler, "req-legacy-store-fail")
	requireGenericAgentOpsFailure(t, rec, http.StatusInternalServerError)
	if executor.calls != 0 {
		t.Fatalf("executor ran %d times although admission failed", executor.calls)
	}
}

func TestAgentOpsNativeUnavailableResponsesKeepTheGenericCode(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	t.Run("Task claim fails", func(t *testing.T) {
		baseStore, err := taskpersistence.NewJSONLStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		faultStore := &agentOpsFaultTaskStore{Store: baseStore}
		faultStore.failTransactions.Store(true)
		owner := taskmanager.New(faultStore, taskmanager.DefaultParallelLimits())
		t.Cleanup(func() {
			if err := owner.Close(); err != nil {
				t.Errorf("close Task owner: %v", err)
			}
		})
		handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, &agentOpsExecutorStub{output: "must not execute"}, owner, &agentOpsNativeCodingAdmissionStub{}, newAgentOpsAcceptedOPSInputStoreStub("ren"))
		requireGenericAgentOpsFailure(t, serveNativeOPSRequest(t, handler, token, "req-native-claim-fail", "raw input"), http.StatusServiceUnavailable)
	})
	t.Run("accepted input owner unavailable", func(t *testing.T) {
		store := newAgentOpsAcceptedOPSInputStoreStub("ren")
		store.readErr = conversation.ErrAcceptedOPSInputUnavailable
		handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, &agentOpsExecutorStub{output: "must not execute"}, newAgentOpsTestTaskOwner(t), &agentOpsNativeCodingAdmissionStub{}, store)
		requireGenericAgentOpsFailure(t, serveNativeOPSRequest(t, handler, token, "req-native-owner-unavailable", "run"), http.StatusServiceUnavailable)
	})
	t.Run("accept outcome unknown", func(t *testing.T) {
		store := newAgentOpsAcceptedOPSInputStoreStub("ren")
		store.acceptErr = errors.New("accept transport outcome unknown")
		handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, &agentOpsExecutorStub{output: "must not execute"}, newAgentOpsTestTaskOwner(t), &agentOpsNativeCodingAdmissionStub{}, store)
		requireGenericAgentOpsFailure(t, serveNativeOPSRequest(t, handler, token, "req-native-accept-unknown", "run"), http.StatusServiceUnavailable)
	})
}

// With the native coding profile the capacity refusal is a blocked claim on a
// Task that was accepted before the claim, not a refusal that persists nothing.
// It keeps its own 503 body and is outside the operations_task_limit code.
func TestAgentOpsNativeCapacityBlockKeepsItsBlockedClaimResponse(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	saturated := taskmanagertest.NewSaturated(t)
	executor := &agentOpsExecutorStub{output: "must not execute"}
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, saturated.Manager, &agentOpsNativeCodingAdmissionStub{}, newAgentOpsAcceptedOPSInputStoreStub("ren"))
	rec := serveNativeOPSRequest(t, handler, token, "req-native-capacity", "run")
	if rec.Code != http.StatusServiceUnavailable || executor.calls != 0 {
		t.Fatalf("status=%d execution=%d body=%q, want 503 without execution", rec.Code, executor.calls, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v\n%s", err, rec.Body.String())
	}
	if body["claim_status"] != "blocked" || body["error"] != "runtime_unavailable" {
		t.Fatalf("body=%q, want claim_status=blocked and error=runtime_unavailable", rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After=%q on the native blocked claim", got)
	}
}
