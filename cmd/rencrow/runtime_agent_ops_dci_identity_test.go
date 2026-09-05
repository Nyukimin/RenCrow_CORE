package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	dcipersistence "github.com/Nyukimin/RenCrow_CORE/internal/infrastructure/persistence/dci"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type agentOpsDCIToolCall struct {
	ctx      context.Context
	toolName string
	args     map[string]interface{}
}

type agentOpsDCIToolExecutorStub struct {
	mu          sync.Mutex
	calls       []agentOpsDCIToolCall
	outputs     []string
	errors      []error
	executeCall int
}

func (s *agentOpsDCIToolExecutorStub) Execute(context.Context, conversation.TurnInput) (string, error) {
	s.mu.Lock()
	s.executeCall++
	s.mu.Unlock()
	return "legacy must not run", nil
}

func (s *agentOpsDCIToolExecutorStub) ExecuteTool(ctx context.Context, toolName string, args map[string]interface{}) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := len(s.calls)
	s.calls = append(s.calls, agentOpsDCIToolCall{ctx: ctx, toolName: toolName, args: args})
	if index < len(s.errors) && s.errors[index] != nil {
		return "", s.errors[index]
	}
	if index >= len(s.outputs) {
		return "", errors.New("tool output is missing")
	}
	return s.outputs[index], nil
}

func (s *agentOpsDCIToolExecutorStub) snapshot() ([]agentOpsDCIToolCall, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	calls := append([]agentOpsDCIToolCall(nil), s.calls...)
	return calls, s.executeCall
}

func TestAgentOpsDCIIdentityAcceptanceSuccessAndReplayAllowlist(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for _, firstReplay := range []bool{false, true} {
		t.Run("first_replay="+strings.ToLower(strconv.FormatBool(firstReplay)), func(t *testing.T) {
			actionID := modulecore.NewActionID()
			traceID := modulecore.NewTraceID()
			requestID := "req-dci-acceptance-" + strings.ToLower(strconv.FormatBool(firstReplay))
			stub := &agentOpsDCIToolExecutorStub{outputs: []string{
				agentOpsDCIWriteReceiptJSON(actionID, firstReplay),
				agentOpsDCIWriteReceiptJSON(actionID, true),
				agentOpsDCIRecallResultJSON(requestID, actionID, traceID),
			}}
			notifier := &agentOpsBusyNotifierStub{}
			handler := newAgentOpsTestHandlerWithNotifier(t, token, stub, notifier)
			query := "/private/sentinel-query"
			rec := serveAgentOpsDCIRequest(t, handler, token, requestID, `{"operation":"dci_identity_acceptance","query":"`+query+`"}`)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			var response agentOpsDCIIdentityAcceptanceResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("response: %v", err)
			}
			assertAgentOpsDCIIdentityResponse(t, response, requestID, actionID, traceID, firstReplay)
			assertAgentOpsDCIIdentityResponseKeys(t, rec.Body.Bytes())
			for _, forbidden := range []string{query, "path", "snippet", "url", "payload", "meta", "secret"} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Fatalf("success response leaked %q: %q", forbidden, rec.Body.String())
				}
			}

			calls, executeCalls := stub.snapshot()
			if executeCalls != 0 {
				t.Fatalf("legacy Execute calls=%d, want zero", executeCalls)
			}
			if len(calls) != 3 {
				t.Fatalf("tool calls=%d, want 3", len(calls))
			}
			wantNames := []string{"data.write", "data.write", "data.recall"}
			wantWriteArgs := map[string]interface{}{
				"store":     "dci",
				"operation": "search",
				"payload":   map[string]interface{}{"query": query},
			}
			wantRecallArgs := map[string]interface{}{
				"store":     "dci",
				"operation": "identity_evidence",
				"query":     string(actionID),
				"limit":     1,
			}
			for index, call := range calls {
				if call.toolName != wantNames[index] {
					t.Fatalf("call %d tool=%q, want %q", index, call.toolName, wantNames[index])
				}
				wantArgs := wantRecallArgs
				if index < 2 {
					wantArgs = wantWriteArgs
				}
				if !reflect.DeepEqual(call.args, wantArgs) {
					t.Fatalf("call %d args=%#v, want %#v", index, call.args, wantArgs)
				}
				scope, ok := domaintool.ToolExecutionScopeFromContext(call.ctx)
				if !ok || scope.RequestID != requestID || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != "shiro" || scope.AgentRole != "worker" || scope.Purpose != "ops" || scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || !scope.Allows(domaintool.DataScopeInternal) {
					t.Fatalf("call %d scope=%#v found=%v", index, scope, ok)
				}
			}
			if !reflect.DeepEqual(calls[0].args, calls[1].args) {
				t.Fatalf("write args changed between calls: %#v / %#v", calls[0].args, calls[1].args)
			}
			assertAgentOpsWorkerBusyCalls(t, notifier, true, false)
		})
	}
}

func TestAgentOpsDCIIdentityAcceptanceUnavailableToolAndBusyLease(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	legacyOnly := &agentOpsExecutorStub{output: "legacy"}
	notifier := &agentOpsBusyNotifierStub{}
	handler := newAgentOpsTestHandlerWithNotifier(t, token, legacyOnly, notifier)
	rec := serveAgentOpsDCIRequest(t, handler, token, "req-dci-tool-unavailable", `{"operation":"dci_identity_acceptance","query":"query"}`)
	if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"runtime_unavailable"}
` {
		t.Fatalf("response status=%d body=%q", rec.Code, rec.Body.String())
	}
	if legacyOnly.calls != 0 {
		t.Fatalf("legacy Execute calls=%d, want zero", legacyOnly.calls)
	}
	assertAgentOpsWorkerBusyCalls(t, notifier, true, false)
}

func TestAgentOpsDCIIdentityAcceptanceRejectsRequestsBeforeExecutor(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	stub := &agentOpsDCIToolExecutorStub{}
	handler := newAgentOpsTestHandler(t, token, stub)
	cases := []struct {
		name string
		body string
		code int
	}{
		{name: "missing", body: `{}`, code: http.StatusBadRequest},
		{name: "mixed", body: `{"message":"legacy","operation":"dci_identity_acceptance","query":"query"}`, code: http.StatusBadRequest},
		{name: "unknown operation", body: `{"operation":"other","query":"query"}`, code: http.StatusBadRequest},
		{name: "operation without query", body: `{"operation":"dci_identity_acceptance"}`, code: http.StatusBadRequest},
		{name: "query without operation", body: `{"query":"query"}`, code: http.StatusBadRequest},
		{name: "unknown field", body: `{"operation":"dci_identity_acceptance","query":"query","tool":"data.write"}`, code: http.StatusBadRequest},
		{name: "trailing value", body: `{"operation":"dci_identity_acceptance","query":"query"}{}`, code: http.StatusBadRequest},
		{name: "oversize query", body: `{"operation":"dci_identity_acceptance","query":"` + strings.Repeat("x", agentOpsMaxMessageBytes+1) + `"}`, code: http.StatusRequestEntityTooLarge},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requestID := "req-dci-invalid-" + strconv.Itoa(index)
			rec := serveAgentOpsDCIRequest(t, handler, token, requestID, tc.body)
			if rec.Code != tc.code {
				t.Fatalf("status=%d want=%d body=%q", rec.Code, tc.code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "data.write") || strings.Contains(rec.Body.String(), "query") || strings.Contains(rec.Body.String(), token) {
				t.Fatalf("invalid response leaked request data: %q", rec.Body.String())
			}
		})
	}
	calls, executeCalls := stub.snapshot()
	if len(calls) != 0 || executeCalls != 0 {
		t.Fatalf("rejected request execution calls=%d/%d, want zero", len(calls), executeCalls)
	}
}

func TestAgentOpsDCIIdentityAcceptanceStopsAtFirstFailureAndNeverLeaks(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	actionID := modulecore.NewActionID()
	secret := "raw=/private/secret.txt query=sentinel snippet=secret"
	cases := []struct {
		name     string
		outputs  []string
		errors   []error
		wantCall int
	}{
		{name: "first tool error", errors: []error{errors.New(secret)}, wantCall: 1},
		{name: "second tool error", outputs: []string{agentOpsDCIWriteReceiptJSON(actionID, false)}, errors: []error{nil, errors.New(secret)}, wantCall: 2},
		{name: "recall tool error", outputs: []string{agentOpsDCIWriteReceiptJSON(actionID, false), agentOpsDCIWriteReceiptJSON(actionID, true)}, errors: []error{nil, nil, errors.New(secret)}, wantCall: 3},
		{name: "malformed first", outputs: []string{"{"}, wantCall: 1},
		{name: "unknown first", outputs: []string{agentOpsDCIWriteReceiptWithUnknownField(actionID, false)}, wantCall: 1},
		{name: "trailing second", outputs: []string{agentOpsDCIWriteReceiptJSON(actionID, false), agentOpsDCIWriteReceiptJSON(actionID, true) + " {}"}, wantCall: 2},
		{name: "oversize recall", outputs: []string{agentOpsDCIWriteReceiptJSON(actionID, false), agentOpsDCIWriteReceiptJSON(actionID, true), strings.Repeat("x", agentOpsToolOutputMaxBytes+1)}, wantCall: 3},
		{name: "null recall", outputs: []string{agentOpsDCIWriteReceiptJSON(actionID, false), agentOpsDCIWriteReceiptJSON(actionID, true), "null"}, wantCall: 3},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stub := &agentOpsDCIToolExecutorStub{outputs: tc.outputs, errors: tc.errors}
			handler := newAgentOpsTestHandler(t, token, stub)
			requestID := "req-dci-failure-" + strconv.Itoa(index)
			rec := serveAgentOpsDCIRequest(t, handler, token, requestID, `{"operation":"dci_identity_acceptance","query":"sentinel"}`)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"execution_failed"}
` {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret") || strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "sentinel") {
				t.Fatalf("failure response leaked details: %q", rec.Body.String())
			}
			calls, executeCalls := stub.snapshot()
			if len(calls) != tc.wantCall || executeCalls != 0 {
				t.Fatalf("calls=%d/%d want=%d/0", len(calls), executeCalls, tc.wantCall)
			}
		})
	}
}

func TestAgentOpsDCIIdentityAcceptanceRejectsWriteReceiptTamper(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	actionID := modulecore.NewActionID()
	traceID := modulecore.NewTraceID()
	cases := []struct {
		name      string
		first     func(*agentOpsDCIWriteReceipt)
		second    func(*agentOpsDCIWriteReceipt)
		wantCalls int
	}{
		{name: "owner", first: func(r *agentOpsDCIWriteReceipt) { r.Owner = "other" }, wantCalls: 1},
		{name: "actor", first: func(r *agentOpsDCIWriteReceipt) { r.ActorID = "mio" }, wantCalls: 1},
		{name: "schema", first: func(r *agentOpsDCIWriteReceipt) { r.SchemaVersion = "wrong/v1" }, wantCalls: 1},
		{name: "policy", first: func(r *agentOpsDCIWriteReceipt) { r.PolicyRevision = "wrong/v1" }, wantCalls: 1},
		{name: "action", first: func(r *agentOpsDCIWriteReceipt) { r.AuditRef = "not-action" }, wantCalls: 1},
		{name: "second action mismatch", second: func(r *agentOpsDCIWriteReceipt) { r.AuditRef = string(modulecore.NewActionID()) }, wantCalls: 2},
		{name: "second replay false", second: func(r *agentOpsDCIWriteReceipt) { r.IdempotentReplay = false }, wantCalls: 2},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			first := agentOpsDCIWriteReceipt{Owner: "dci", OwnerRoute: "dci/search", AuditRef: string(actionID), ActorID: "shiro", AgentRole: "worker", Purpose: "ops", DataScope: "internal", Status: "completed", SchemaVersion: "dci-search/v2", MigrationState: "embedded_current", ValidationState: "owner_validated", PolicyRevision: runtimeDataWritePolicyRevision, CompletedAt: validAgentOpsDCITimestamp}
			second := first
			second.IdempotentReplay = true
			if tc.first != nil {
				tc.first(&first)
			}
			if tc.second != nil {
				tc.second(&second)
			}
			requestID := "req-dci-write-tamper-" + strconv.Itoa(index)
			stub := &agentOpsDCIToolExecutorStub{outputs: []string{agentOpsDCIWriteReceiptJSONFrom(first), agentOpsDCIWriteReceiptJSONFrom(second), agentOpsDCIRecallResultJSON(requestID, actionID, traceID)}}
			handler := newAgentOpsTestHandler(t, token, stub)
			rec := serveAgentOpsDCIRequest(t, handler, token, requestID, `{"operation":"dci_identity_acceptance","query":"query"}`)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"execution_failed"}
` {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			calls, _ := stub.snapshot()
			if len(calls) != tc.wantCalls {
				t.Fatalf("calls=%d want=%d", len(calls), tc.wantCalls)
			}
		})
	}
}

func TestAgentOpsDCIIdentityAcceptanceRejectsRecallEvidenceTamper(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	actionID := modulecore.NewActionID()
	traceID := modulecore.NewTraceID()
	cases := []struct {
		name   string
		mutate func(*agentOpsDCIRecallResult)
	}{
		{name: "request", mutate: func(r *agentOpsDCIRecallResult) { r.Evidence.RequestID = "other-request" }},
		{name: "actor", mutate: func(r *agentOpsDCIRecallResult) { r.Evidence.ActorID = "mio" }},
		{name: "action", mutate: func(r *agentOpsDCIRecallResult) { r.Records[0].ActionID = string(modulecore.NewActionID()) }},
		{name: "graph", mutate: func(r *agentOpsDCIRecallResult) { r.Records[0].EventGraphSHA256 = strings.Repeat("A", 64) }},
		{name: "count", mutate: func(r *agentOpsDCIRecallResult) { r.Records[0].EventCount++ }},
		{name: "multiple records", mutate: func(r *agentOpsDCIRecallResult) { r.Records = append(r.Records, r.Records[0]) }},
		{name: "partial", mutate: func(r *agentOpsDCIRecallResult) { r.Partial = true }},
	}
	for index, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recall := validAgentOpsDCIRecallResult(actionID, traceID)
			requestID := "req-dci-recall-tamper-" + strconv.Itoa(index)
			recall.Evidence.RequestID = requestID
			tc.mutate(&recall)
			stub := &agentOpsDCIToolExecutorStub{outputs: []string{
				agentOpsDCIWriteReceiptJSON(actionID, false),
				agentOpsDCIWriteReceiptJSON(actionID, true),
				agentOpsDCIRecallResultJSONFrom(requestID, recall),
			}}
			handler := newAgentOpsTestHandler(t, token, stub)
			rec := serveAgentOpsDCIRequest(t, handler, token, requestID, `{"operation":"dci_identity_acceptance","query":"query"}`)
			if rec.Code != http.StatusInternalServerError || rec.Body.String() != `{"error":"execution_failed"}
` {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
			calls, _ := stub.snapshot()
			if len(calls) != 3 {
				t.Fatalf("calls=%d want=3", len(calls))
			}
		})
	}
}

func assertAgentOpsDCIIdentityResponse(t *testing.T, got agentOpsDCIIdentityAcceptanceResponse, requestID string, actionID modulecore.ActionID, traceID modulecore.TraceID, firstReplay bool) {
	t.Helper()
	if got.SchemaVersion != "rencrow.agent-ops.dci-identity-acceptance/v1" || got.Status != "passed" || got.RequestID != requestID || got.AgentID != "shiro" || got.Role != "worker" || got.Operation != agentOpsDCIIdentityAcceptanceOperation || got.ActionID != string(actionID) || got.TraceID != string(traceID) || got.FirstWriteReplay != firstReplay || !got.SecondWriteReplay || got.EventCount != 6 || got.StepCount != 1 || got.EvidenceCount != 1 || got.CurrentProjectionCount != 1 || got.ArchiveProjectionCount != 1 || got.EventGraphSHA256 != strings.Repeat("a", 64) {
		t.Fatalf("response=%+v", got)
	}
}

func assertAgentOpsDCIIdentityResponseKeys(t *testing.T, body []byte) {
	t.Helper()
	var values map[string]interface{}
	if err := json.Unmarshal(body, &values); err != nil {
		t.Fatalf("response JSON: %v", err)
	}
	want := map[string]interface{}{
		"schema_version": true, "status": true, "request_id": true, "agent_id": true,
		"role": true, "operation": true, "action_id": true, "trace_id": true,
		"first_write_replay": true, "second_write_replay": true, "event_count": true,
		"step_count": true, "evidence_count": true, "current_projection_count": true,
		"archive_projection_count": true, "event_graph_sha256": true,
	}
	if !reflect.DeepEqual(valuesKeys(values), valuesKeys(want)) {
		t.Fatalf("response keys=%v want=%v", valuesKeys(values), valuesKeys(want))
	}
}

func valuesKeys(values map[string]interface{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func serveAgentOpsDCIRequest(t *testing.T, handler http.HandlerFunc, token, requestID, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(body))
	setAgentOpsHeaders(req, token, requestID)
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

const validAgentOpsDCITimestamp = "2026-09-01T00:00:00Z"

func agentOpsDCIWriteReceiptJSON(actionID modulecore.ActionID, replay bool) string {
	return agentOpsDCIWriteReceiptJSONFrom(agentOpsDCIWriteReceipt{
		Owner: "dci", OwnerRoute: "dci/search", AuditRef: string(actionID), ActorID: "shiro", AgentRole: "worker", Purpose: "ops", DataScope: "internal", Status: "completed", SchemaVersion: "dci-search/v2", MigrationState: "embedded_current", ValidationState: "owner_validated", IdempotentReplay: replay, PolicyRevision: runtimeDataWritePolicyRevision, CompletedAt: validAgentOpsDCITimestamp,
	})
}

func agentOpsDCIWriteReceiptJSONFrom(receipt agentOpsDCIWriteReceipt) string {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func agentOpsDCIWriteReceiptWithUnknownField(actionID modulecore.ActionID, replay bool) string {
	var values map[string]interface{}
	if err := json.Unmarshal([]byte(agentOpsDCIWriteReceiptJSON(actionID, replay)), &values); err != nil {
		panic(err)
	}
	values["secret"] = "must not cross boundary"
	encoded, err := json.Marshal(values)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}

func validAgentOpsDCIRecallResult(actionID modulecore.ActionID, traceID modulecore.TraceID) agentOpsDCIRecallResult {
	return agentOpsDCIRecallResult{
		Store: "dci", Operation: "identity_evidence", Records: []agentOpsDCIIdentityRecord{{
			SchemaVersion: dcipersistence.IdentityEvidenceSchemaVersion, Status: "passed", ActionID: string(actionID), TraceID: string(traceID), ActorKind: "agent", ActorID: "shiro", SearchStatus: "completed", EventCount: 6, StepCount: 1, EvidenceCount: 1, CurrentProjectionCount: 1, ArchiveProjectionCount: 1, EventGraphSHA256: strings.Repeat("a", 64),
		}}, Evidence: agentOpsDCIRecallEvidence{
			RequestID: "placeholder", ActorID: "shiro", AgentRole: "worker", Purpose: "ops", DataScope: "internal", Owner: "dci", OwnerRoute: "dci/identity_evidence", RetrievedAt: validAgentOpsDCITimestamp, FreshnessState: "observed_at_read", ValidationState: "owner_route_succeeded", BudgetLimit: 1, ReturnedCount: 1,
		},
	}
}

func agentOpsDCIRecallResultJSON(requestID string, actionID modulecore.ActionID, traceID modulecore.TraceID) string {
	result := validAgentOpsDCIRecallResult(actionID, traceID)
	result.Evidence.RequestID = requestID
	return agentOpsDCIRecallResultJSONFrom(requestID, result)
}

func agentOpsDCIRecallResultJSONFrom(_ string, result agentOpsDCIRecallResult) string {
	encoded, err := json.Marshal(result)
	if err != nil {
		panic(err)
	}
	return string(encoded)
}
