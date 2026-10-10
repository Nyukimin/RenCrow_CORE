package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/orchestrator"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	domainagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/routing"
	domaintool "github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

type agentOpsExecutorStub struct {
	ctx    context.Context
	input  conversation.TurnInput
	calls  int
	output string
	err    error
	ctxs   []context.Context
	inputs []conversation.TurnInput
}

type agentOpsNativeCodingAdmissionStub struct {
	calls  int
	ctxs   []context.Context
	inputs []conversation.TurnInput
	err    error
}

func (s *agentOpsNativeCodingAdmissionStub) AdmitNativeCoding(ctx context.Context, input conversation.TurnInput) error {
	s.calls++
	s.ctxs = append(s.ctxs, ctx)
	s.inputs = append(s.inputs, input)
	return s.err
}

func (s *agentOpsExecutorStub) Execute(ctx context.Context, got conversation.TurnInput) (string, error) {
	s.ctx = ctx
	s.input = got
	s.calls++
	s.ctxs = append(s.ctxs, ctx)
	s.inputs = append(s.inputs, got)
	return s.output, s.err
}

type agentOpsBusyNotifierStub struct {
	mu    sync.Mutex
	calls []bool
}

func (s *agentOpsBusyNotifierStub) SetWorkerBusy(busy bool) {
	s.mu.Lock()
	s.calls = append(s.calls, busy)
	s.mu.Unlock()
}

func (s *agentOpsBusyNotifierStub) Calls() []bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]bool(nil), s.calls...)
}

type agentOpsBlockingExecutor struct {
	entered chan struct{}
	release chan struct{}
}

func (s *agentOpsBlockingExecutor) Execute(ctx context.Context, got conversation.TurnInput) (string, error) {
	s.entered <- struct{}{}
	select {
	case <-s.release:
		return "ok", nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func assertAgentOpsWorkerBusyCalls(t *testing.T, notifier *agentOpsBusyNotifierStub, want ...bool) {
	t.Helper()
	got := notifier.Calls()
	if len(got) != len(want) {
		t.Fatalf("worker busy calls=%v want=%v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("worker busy calls=%v want=%v", got, want)
		}
	}
}

func assertAgentOpsTurnInput(t *testing.T, input conversation.TurnInput, taskID string) {
	t.Helper()
	if err := input.Validate(); err != nil {
		t.Fatalf("agent OPS input invalid: %v", err)
	}
	if string(input.RootTaskID()) != taskID {
		t.Fatalf("root TaskID=%q want response TaskID=%q", input.RootTaskID(), taskID)
	}
	identities := []string{
		string(input.TurnID()), string(input.TraceID()),
		string(input.UserMessageID()), string(input.AgentMessageID()),
	}
	seen := make(map[string]struct{}, len(identities))
	for _, identity := range identities {
		if identity == taskID {
			t.Fatalf("conversation identity reused TaskID=%q: %v", taskID, identities)
		}
		if _, exists := seen[identity]; exists {
			t.Fatalf("canonical input identities are not distinct: %v", identities)
		}
		seen[identity] = struct{}{}
	}
}

func TestAgentOpsHandlerExecutesWithAuthenticatedShiroWorkerScope(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	executor := &agentOpsExecutorStub{output: "実行結果"}
	notifier := &agentOpsBusyNotifierStub{}
	handler := newAgentOpsTestHandlerWithNotifier(t, token, executor, notifier)
	requestID := "req-agent-ops-1"
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/agent/ops", strings.NewReader(`{"message":"状態を確認して"}`))
	setAgentOpsHeaders(req, token, requestID)
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	localOnlyHandler(handler).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content-type=%q", got)
	}
	var response map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatalf("response JSON: %v", err)
	}
	wantKeys := map[string]bool{"request_id": true, "task_id": true, "agent_id": true, "role": true, "route": true, "output": true}
	if len(response) != len(wantKeys) {
		t.Fatalf("response keys=%v", response)
	}
	for key := range response {
		if !wantKeys[key] {
			t.Fatalf("unexpected response key %q", key)
		}
	}
	if response["request_id"] != requestID || response["agent_id"] != "shiro" || response["role"] != "worker" || response["route"] != "OPS" || response["output"] != "実行結果" {
		t.Fatalf("response=%v", response)
	}
	if executor.calls != 1 || executor.input.MessageText() != "状態を確認して" || executor.input.ChannelAddress().ChannelType() != "agent_ops" || executor.input.ChannelAddress().ExternalConversationID() != "agent-ops" || executor.input.Route() != routing.RouteOPS || executor.input.BackendSelection() != conversation.BackendSelectionNone {
		t.Fatalf("input=%#v calls=%d", executor.input, executor.calls)
	}
	if err := modulecore.SessionID(executor.input.SessionID()).Validate(); err != nil {
		t.Fatalf("agent OPS input SessionID=%q: %v", executor.input.SessionID(), err)
	}
	responseTaskID, ok := response["task_id"].(string)
	if !ok || responseTaskID == "" {
		t.Fatalf("task_id=%v", response["task_id"])
	}
	assertAgentOpsTurnInput(t, executor.input, responseTaskID)
	scope, ok := domaintool.ToolExecutionScopeFromContext(executor.ctx)
	if !ok {
		t.Fatal("executor did not receive a trusted scope")
	}
	if scope.RequestID != requestID || scope.RequestID == responseTaskID || scope.ActorKind != domaintool.ActorKindAgent || scope.ActorID != "shiro" || scope.AuthenticatedUserID != "ren" || scope.AuthenticationSource != domaintool.AuthenticationSourceAgentOrchestrator || scope.AgentRole != "worker" || scope.Purpose != "ops" {
		t.Fatalf("derived scope=%#v", scope)
	}
	if !scope.Allows(domaintool.DataScopePublic) || !scope.Allows(domaintool.DataScopeUser) || !scope.Allows(domaintool.DataScopeInternal) {
		t.Fatalf("derived scope missing access=%#v", scope)
	}
	assertAgentOpsWorkerBusyCalls(t, notifier, true, false)
}

func TestAgentOpsHandlerRunsConfiguredNativeAdmissionBeforeExecutor(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	message := " \tOPS native selection — 診断\n"
	executor := &agentOpsExecutorStub{output: "ok"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, admission)
	body, err := json.Marshal(agentOpsRequest{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(string(body)))
	setAgentOpsHeaders(req, token, "req-native-selection")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if admission.calls != 1 || len(admission.inputs) != 1 {
		t.Fatalf("admission calls=%d inputs=%d, want exactly one", admission.calls, len(admission.inputs))
	}
	if admission.inputs[0].Route() != routing.RouteOPS || admission.inputs[0].MessageText() != message || admission.inputs[0].BackendSelection() != conversation.BackendSelectionNone {
		t.Fatalf("admission input=%#v", admission.inputs[0])
	}
	scope, ok := domaintool.ToolExecutionScopeFromContext(admission.ctxs[0])
	if !ok || scope.RequestID != "req-native-selection" || scope.ActorID != "shiro" || scope.AgentRole != "worker" || scope.Purpose != "ops" || scope.AuthenticatedUserID != "ren" {
		t.Fatalf("admission scope=%#v found=%t", scope, ok)
	}
	if executor.calls != 1 || executor.input.MessageText() != message || executor.input.BackendSelection() != conversation.BackendShiroNativeCodingV1 {
		t.Fatalf("executor calls=%d input=%#v", executor.calls, executor.input)
	}
}

func TestAgentOpsProductionWiringSharesInitializedNativeCodingRuntime(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	root := filepath.Dir(testFile)
	dependenciesSource, err := os.ReadFile(filepath.Join(root, "runtime_dependencies.go"))
	if err != nil {
		t.Fatal(err)
	}
	orchestratorSource, err := os.ReadFile(filepath.Join(root, "runtime_orchestrator.go"))
	if err != nil {
		t.Fatal(err)
	}
	dependenciesText := string(dependenciesSource)
	orchestratorText := string(orchestratorSource)
	orchestratorBuild := strings.Index(dependenciesText, "buildOrchestratorRuntime(")
	nativeAdmissionBuild := strings.Index(dependenciesText, "configuredNativeCodingAdmission(cfg, deps.nativeCoding)")
	agentOpsBuild := strings.Index(dependenciesText, "newConfiguredAgentOpsHandler(cfg, agents.Shiro, idleChatWorkerNotifier, deps.taskManager, nativeCodingAdmission, conversationRuntime.AcceptedOPSInputStore, withAgentOpsLeadRunRecorder(deps.superAgentStore))")
	if orchestratorBuild < 0 || nativeAdmissionBuild <= orchestratorBuild || agentOpsBuild <= nativeAdmissionBuild {
		t.Fatalf("Agent OPS handler must use the configured native runtime after the orchestrator initializes it: orchestrator=%d admission=%d handler=%d", orchestratorBuild, nativeAdmissionBuild, agentOpsBuild)
	}
	for _, required := range []string{
		"agents.Shiro.WithNativeCodingDelegate(nativeCoding)",
		"orch.SetNativeCodingAdmission(nativeCoding)",
		"deps.nativeCoding = nativeCoding",
	} {
		if !strings.Contains(orchestratorText, required) {
			t.Fatalf("runtime orchestrator must keep one native coding runtime for Shiro and admission; missing %q", required)
		}
	}
}

func TestAgentOpsAuthenticationFailureDoesNotReachNativeAdmission(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	executor := &agentOpsExecutorStub{output: "must not execute"}
	admission := &agentOpsNativeCodingAdmissionStub{}
	store := newAgentOpsAcceptedOPSInputStoreStub("ren")
	handler := newAgentOpsTestHandlerWithOwnerAndStore(t, token, executor, newAgentOpsTestTaskOwner(t), admission, store)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
	setAgentOpsHeaders(req, "wrong-token-wrong-token-wrong-token-", "req-unauthenticated")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || admission.calls != 0 || executor.calls != 0 {
		t.Fatalf("status=%d admission=%d executor=%d body=%q", rec.Code, admission.calls, executor.calls, rec.Body.String())
	}
	accepts, reads := store.counts()
	if accepts != 0 || reads != 0 {
		t.Fatalf("unauthorized request reached accepted OPS owner: accepts=%d reads=%d", accepts, reads)
	}
}

func TestAgentOpsStrictDecoderRejectsMalformedAuthenticatedBodiesBeforeAnyExecution(t *testing.T) {
	validDCI := []byte(`{"operation":"dci_identity_acceptance","query":"identity"}`)
	oversizedBody := append([]byte(strings.Repeat(" ", agentOpsMaxBodyBytes-len(validDCI))), validDCI...)
	oversizedBody = append(oversizedBody, ' ')
	invalidUTF8 := append([]byte(`{"message":"x`), 0xff)
	invalidUTF8 = append(invalidUTF8, []byte(`"}`)...)
	deeplyNested := []byte(`{"message":` + strings.Repeat("[", 10_001) + `0` + strings.Repeat("]", 10_001) + `}`)
	oversizedMessage := []byte(`{"message":"` + strings.Repeat("m", agentOpsMaxMessageBytes+1) + `"}`)
	cases := []struct {
		name       string
		body       []byte
		wantStatus int
	}{
		{name: "duplicate field", body: []byte(`{"message":"first","message":"second"}`), wantStatus: http.StatusBadRequest},
		{name: "escaped duplicate field", body: []byte(`{"message":"first","mess\u0061ge":"second"}`), wantStatus: http.StatusBadRequest},
		{name: "casefold duplicate field", body: []byte(`{"message":"first","MESSAGE":"second"}`), wantStatus: http.StatusBadRequest},
		{name: "duplicate fixed DCI operation", body: []byte(`{"operation":"dci_identity_acceptance","query":"identity","operation":"dci_identity_acceptance"}`), wantStatus: http.StatusBadRequest},
		{name: "unpaired high surrogate", body: []byte(`{"message":"\ud800"}`), wantStatus: http.StatusBadRequest},
		{name: "unpaired low surrogate", body: []byte(`{"message":"\udc00"}`), wantStatus: http.StatusBadRequest},
		{name: "invalid utf8", body: invalidUTF8, wantStatus: http.StatusBadRequest},
		{name: "malformed nested value", body: []byte(`{"message":"run","query":[[[}`), wantStatus: http.StatusBadRequest},
		{name: "deeply nested value", body: deeplyNested, wantStatus: http.StatusBadRequest},
		{name: "unknown field", body: []byte(`{"message":"run","source":"human"}`), wantStatus: http.StatusBadRequest},
		{name: "trailing value", body: []byte(`{"message":"run"}{}`), wantStatus: http.StatusBadRequest},
		{name: "oversized message", body: oversizedMessage, wantStatus: http.StatusRequestEntityTooLarge},
		{name: "oversized request body", body: oversizedBody, wantStatus: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			const token = "0123456789abcdef0123456789abcdef"
			executor := &agentOpsExecutorWithToolsStub{agentOpsExecutorStub: &agentOpsExecutorStub{output: "must not execute"}}
			admission := &agentOpsNativeCodingAdmissionStub{}
			handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, admission)
			req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(string(tc.body)))
			setAgentOpsHeaders(req, token, "req-strict-json")
			req.RemoteAddr = "127.0.0.1:18791"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d want=%d body=%q", rec.Code, tc.wantStatus, rec.Body.String())
			}
			if executor.calls != 0 || executor.toolCalls != 0 || admission.calls != 0 {
				t.Fatalf("malformed authenticated request reached execution: executor=%d DCI=%d admission=%d", executor.calls, executor.toolCalls, admission.calls)
			}
			if strings.Contains(rec.Body.String(), string(tc.body)) || strings.Contains(rec.Body.String(), token) {
				t.Fatalf("response leaked request secret/body: %q", rec.Body.String())
			}
		})
	}
}

func TestAgentOpsAdmissionRefusalStopsBeforeExecutor(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	for _, refusal := range []error{domainagent.ErrNativeCodingBlocked, domainagent.ErrNativeCodingRejected} {
		t.Run(refusal.Error(), func(t *testing.T) {
			executor := &agentOpsExecutorStub{output: "must not execute"}
			admission := &agentOpsNativeCodingAdmissionStub{err: refusal}
			handler := newAgentOpsTestHandlerWithNativeAdmission(t, token, executor, admission)
			req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
			setAgentOpsHeaders(req, token, "req-admission-refused")
			req.RemoteAddr = "127.0.0.1:18791"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			var response agentOpsNativeResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if rec.Code != http.StatusInternalServerError || response.TaskStatus != "failed" || response.RunStatus != "failed" || admission.calls != 1 || executor.calls != 0 {
				t.Fatalf("status=%d admission=%d executor=%d body=%q", rec.Code, admission.calls, executor.calls, rec.Body.String())
			}
		})
	}
}

func TestAgentOpsHandlerPreservesOriginalMessageBytes(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	message := " \tこんにちは — исправить?\n "
	executor := &agentOpsExecutorStub{output: "ok"}
	handler := newAgentOpsTestHandler(t, token, executor)
	body, err := json.Marshal(agentOpsRequest{Message: message})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(string(body)))
	setAgentOpsHeaders(req, token, "req-message-bytes")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if executor.calls != 1 || executor.input.MessageText() != message {
		t.Fatalf("executor message=%q calls=%d want exact message %q", executor.input.MessageText(), executor.calls, message)
	}
}

func TestAgentOpsMessageLimitUsesOriginalEncodedBytes(t *testing.T) {
	request := agentOpsRequest{Message: strings.Repeat(" ", agentOpsMaxMessageBytes+1) + "run"}
	if _, err := normalizeAgentOpsRequest(&request); !errors.Is(err, errAgentOpsRequestTooLarge) {
		t.Fatalf("oversized original message error=%v, want %v", err, errAgentOpsRequestTooLarge)
	}
}

func TestAgentOpsHandlerReusesAuthenticatedRequestIDForRepeatedPayload(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	const requestID = "req-agent-ops-replay"
	executor := &agentOpsExecutorStub{output: "ok"}
	handler := newAgentOpsTestHandler(t, token, executor)
	taskIDs := make([]string, 2)
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"repeat"}`))
		setAgentOpsHeaders(req, token, requestID)
		req.RemoteAddr = "127.0.0.1:18791"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d status=%d body=%q", i, rec.Code, rec.Body.String())
		}
		var response agentOpsResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("attempt %d response: %v", i, err)
		}
		if response.RequestID != requestID || response.TaskID == "" {
			t.Fatalf("attempt %d response=%+v", i, response)
		}
		taskIDs[i] = response.TaskID
	}
	if len(executor.ctxs) != 2 || len(executor.inputs) != 2 {
		t.Fatalf("captured executions=%d/%d", len(executor.ctxs), len(executor.inputs))
	}
	if executor.inputs[0].SessionID() == executor.inputs[1].SessionID() {
		t.Fatalf("independent requests reused SessionID=%q", executor.inputs[0].SessionID())
	}
	for i, ctx := range executor.ctxs {
		scope, ok := domaintool.ToolExecutionScopeFromContext(ctx)
		if !ok {
			t.Fatalf("attempt %d missing scope", i)
		}
		if scope.RequestID != requestID || scope.RequestID == taskIDs[i] {
			t.Fatalf("attempt %d scope=%#v", i, scope)
		}
		if err := modulecore.SessionID(executor.inputs[i].SessionID()).Validate(); err != nil {
			t.Fatalf("attempt %d input SessionID=%q: %v", i, executor.inputs[i].SessionID(), err)
		}
		assertAgentOpsTurnInput(t, executor.inputs[i], taskIDs[i])
	}
	if taskIDs[0] == taskIDs[1] {
		t.Fatalf("repeated requests reused TaskID=%q", taskIDs[0])
	}
}

func TestAgentOpsHandlerWorkerBusyLeaseRefCountsConcurrentRequests(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	executor := &agentOpsBlockingExecutor{
		entered: make(chan struct{}, 2),
		release: make(chan struct{}),
	}
	notifier := &agentOpsBusyNotifierStub{}
	// Legacy OPS requests run on Task owner Runs, so concurrent requests are
	// bounded by the operations slot. Two slots let both requests run at once,
	// which is what the lease ref-count needs to be observed.
	owner := newAgentOpsTestTaskOwnerWithLimits(t, taskmanager.ParallelLimits{Global: 3, PerModule: 1, CodingTasks: 2, LongResearchTasks: 1, DestructiveTasks: 2})
	handler := newAgentOpsTestHandlerWithOwnerAndNotifier(t, token, executor, notifier, owner)
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
			setAgentOpsHeaders(req, token, "req-concurrent-"+string(rune('1'+index)))
			req.RemoteAddr = "127.0.0.1:18791"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			responses <- rec
		}(i)
	}
	for i := 0; i < 2; i++ {
		select {
		case <-executor.entered:
		case <-time.After(time.Second):
			t.Fatal("concurrent OPS executor did not start")
		}
	}
	assertAgentOpsWorkerBusyCalls(t, notifier, true)
	close(executor.release)
	for i := 0; i < 2; i++ {
		select {
		case rec := <-responses:
			if rec.Code != http.StatusOK {
				t.Fatalf("concurrent response status=%d body=%q", rec.Code, rec.Body.String())
			}
		case <-time.After(time.Second):
			t.Fatal("concurrent OPS request did not complete")
		}
	}
	assertAgentOpsWorkerBusyCalls(t, notifier, true, false)
}

func TestAgentOpsHandlerRejectsRemoteRequestsThroughLocalWrapper(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	handler, err := newConfiguredAgentOpsHandler(cfg, &agentOpsExecutorStub{output: "ok"}, nil, newAgentOpsTestTaskOwner(t), nil, nil)
	if err != nil {
		t.Fatalf("newConfiguredAgentOpsHandler() error=%v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
	setAgentOpsHeaders(req, token, "req-remote")
	req.RemoteAddr = "192.0.2.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("remote status=%d want=%d", rec.Code, http.StatusNotFound)
	}
}

func TestAgentOpsHandlerRejectsMethodAuthHeadersAndBodies(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	executor := &agentOpsExecutorStub{output: "ok"}
	notifier := &agentOpsBusyNotifierStub{}
	handler := newAgentOpsTestHandlerWithNotifier(t, token, executor, notifier)
	cases := []struct {
		name   string
		method string
		path   string
		body   string
		setup  func(*http.Request)
		want   int
	}{
		{name: "method", method: http.MethodGet, body: `{}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "req-1") }, want: http.StatusMethodNotAllowed},
		{name: "missing auth", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, "", "req-1") }, want: http.StatusUnauthorized},
		{name: "wrong auth", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, "wrong-token-wrong-token-wrong-token-", "req-1") }, want: http.StatusUnauthorized},
		{name: "wrong client", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Set("X-RenCrow-Client", "RenCrow_PORTAL")
		}, want: http.StatusForbidden},
		{name: "wrong profile", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Set("X-RenCrow-Interaction-Profile", "cmd-control")
		}, want: http.StatusForbidden},
		{name: "duplicate auth", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Add("Authorization", "Bearer "+token)
		}, want: http.StatusUnauthorized},
		{name: "duplicate client", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Add("X-RenCrow-Client", agentOpsClient)
		}, want: http.StatusForbidden},
		{name: "duplicate profile", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Add("X-RenCrow-Interaction-Profile", agentOpsInteractionProfile)
		}, want: http.StatusForbidden},
		{name: "duplicate request id", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Add("X-Request-ID", "req-1")
		}, want: http.StatusBadRequest},
		{name: "missing request id", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "") }, want: http.StatusBadRequest},
		{name: "query", method: http.MethodPost, path: "/v1/agent/ops?unexpected=1", body: `{"message":"run"}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "req-1") }, want: http.StatusBadRequest},
		{name: "non-json content type", method: http.MethodPost, body: `{"message":"run"}`, setup: func(r *http.Request) {
			setAgentOpsHeaders(r, token, "req-1")
			r.Header.Set("Content-Type", "text/plain")
		}, want: http.StatusUnsupportedMediaType},
		{name: "unknown field", method: http.MethodPost, body: `{"message":"run","user_id":"spoof"}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "req-1") }, want: http.StatusBadRequest},
		{name: "trailing value", method: http.MethodPost, body: `{"message":"run"}{}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "req-1") }, want: http.StatusBadRequest},
		{name: "empty message", method: http.MethodPost, body: `{"message":"  "}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "req-1") }, want: http.StatusBadRequest},
		{name: "oversize message", method: http.MethodPost, body: `{"message":"` + strings.Repeat("x", agentOpsMaxMessageBytes+1) + `"}`, setup: func(r *http.Request) { setAgentOpsHeaders(r, token, "req-1") }, want: http.StatusRequestEntityTooLarge},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := tc.path
			if path == "" {
				path = "/v1/agent/ops"
			}
			req := httptest.NewRequest(tc.method, path, strings.NewReader(tc.body))
			tc.setup(req)
			req.RemoteAddr = "127.0.0.1:18791"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%q", rec.Code, tc.want, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), tc.body) || strings.Contains(rec.Body.String(), token) {
				t.Fatalf("response leaked request secret/body: %q", rec.Body.String())
			}
		})
	}
	if executor.calls != 0 {
		t.Fatalf("executor calls=%d for rejected requests", executor.calls)
	}
	assertAgentOpsWorkerBusyCalls(t, notifier)
}

func TestAgentOpsHandlerReturnsSafeExecutorError(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	secret := "executor secret should not be returned"
	executor := &agentOpsExecutorStub{err: errors.New(secret)}
	notifier := &agentOpsBusyNotifierStub{}
	handler := newAgentOpsTestHandlerWithNotifier(t, token, executor, notifier)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
	setAgentOpsHeaders(req, token, "req-error")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), secret) || strings.Contains(rec.Body.String(), "run") || strings.Contains(rec.Body.String(), token) {
		t.Fatalf("unsafe executor response=%q", rec.Body.String())
	}
	assertAgentOpsWorkerBusyCalls(t, notifier, true, false)

	blankExecutor := &agentOpsExecutorStub{output: " \n"}
	blankHandler := newAgentOpsTestHandler(t, token, blankExecutor)
	blankRequest := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
	setAgentOpsHeaders(blankRequest, token, "req-blank")
	blankRequest.RemoteAddr = "127.0.0.1:18791"
	blankRecord := httptest.NewRecorder()
	blankHandler.ServeHTTP(blankRecord, blankRequest)
	if blankRecord.Code != http.StatusInternalServerError {
		t.Fatalf("blank output status=%d body=%q", blankRecord.Code, blankRecord.Body.String())
	}
}

func TestAgentOpsHandlerWorkerBusyLeaseReleasesOnCanceledExecution(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	notifier := &agentOpsBusyNotifierStub{}
	handler := newAgentOpsTestHandlerWithNotifier(t, token, &agentOpsExecutorStub{err: context.Canceled}, notifier)
	req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
	setAgentOpsHeaders(req, token, "req-canceled")
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("canceled execution status=%d body=%q", rec.Code, rec.Body.String())
	}
	assertAgentOpsWorkerBusyCalls(t, notifier, true, false)
}

func TestAgentOpsTokenFileIsValidatedAndReadOnce(t *testing.T) {
	tests := []struct {
		name    string
		content string
		mode    os.FileMode
		wantErr bool
	}{
		{name: "valid", content: "0123456789abcdef0123456789abcdef\n", mode: 0o600},
		{name: "short", content: "0123456789abcdef0123456789abcde", mode: 0o600, wantErr: true},
		{name: "multiple tokens", content: "0123456789abcdef0123456789abcdef another", mode: 0o600, wantErr: true},
		{name: "internal whitespace", content: "0123456789abcdef 0123456789abcdef", mode: 0o600, wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "token")
			if err := os.WriteFile(path, []byte(tc.content), tc.mode); err != nil {
				t.Fatal(err)
			}
			if runtime.GOOS != "windows" && tc.mode.Perm()&0o077 != 0 {
				t.Fatal("test mode must be owner-only")
			}
			cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
			handler, err := newAgentOpsHandler(cfg, &agentOpsExecutorStub{output: "ok"}, nil, newAgentOpsTestTaskOwner(t), nil, nil)
			if tc.wantErr {
				if err == nil {
					t.Fatal("expected token validation error")
				}
				return
			}
			if err != nil {
				t.Fatalf("newAgentOpsHandler error=%v", err)
			}
			if err := os.WriteFile(path, []byte("changed-token-changed-token-changed-token"), tc.mode); err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, "/v1/agent/ops", strings.NewReader(`{"message":"run"}`))
			setAgentOpsHeaders(req, "0123456789abcdef0123456789abcdef", "req-read-once")
			req.RemoteAddr = "127.0.0.1:18791"
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
			}
		})
	}

	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte("0123456789abcdef0123456789abcdef"), 0o644); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
		if _, err := newAgentOpsHandler(cfg, &agentOpsExecutorStub{}, nil, newAgentOpsTestTaskOwner(t), nil, nil); err == nil {
			t.Fatal("group/world-readable token file must be rejected")
		}
	}
}

func TestAgentOpsHandlerRequiresTaskOwnerAtStartup(t *testing.T) {
	const token = "0123456789abcdef0123456789abcdef"
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	if _, err := newAgentOpsHandler(cfg, &agentOpsExecutorStub{output: "ok"}, nil, nil, nil, nil); err == nil || err.Error() != "local_agent_ops task owner is unavailable" {
		t.Fatalf("nil task owner error=%v", err)
	}
}

func newAgentOpsTestHandler(t *testing.T, token string, executor agentOpsExecutor) http.HandlerFunc {
	return newAgentOpsTestHandlerWithNotifier(t, token, executor, nil)
}

func newAgentOpsTestTaskOwner(t *testing.T) *taskmanager.Manager {
	t.Helper()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
		t.Fatalf("initialize Agent OPS task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close Agent OPS task owner: %v", err)
		}
	})
	return deps.taskManager
}

// newAgentOpsTestTaskOwnerWithLimits returns a Task owner over the same kind of
// store as newAgentOpsTestTaskOwner but with caller-chosen parallel limits.
func newAgentOpsTestTaskOwnerWithLimits(t *testing.T, limits taskmanager.ParallelLimits) *taskmanager.Manager {
	t.Helper()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
		t.Fatalf("initialize Agent OPS task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close Agent OPS task owner: %v", err)
		}
	})
	return taskmanager.New(deps.taskStore, limits)
}

func newAgentOpsTestHandlerWithOwnerAndNotifier(t *testing.T, token string, executor agentOpsExecutor, notifier agentOpsWorkerBusyNotifier, owner *taskmanager.Manager) http.HandlerFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	handler, err := newAgentOpsHandler(cfg, executor, notifier, owner, nil, nil)
	if err != nil {
		t.Fatalf("newAgentOpsHandler() error=%v", err)
	}
	return handler
}

func newAgentOpsTestHandlerWithTaskOwner(t *testing.T, token string, executor agentOpsExecutor, owner *taskmanager.Manager) http.HandlerFunc {
	return newAgentOpsTestHandlerWithTaskOwnerAndNativeAdmission(t, token, executor, owner, nil)
}

func newAgentOpsTestHandlerWithTaskOwnerAndNativeAdmission(t *testing.T, token string, executor agentOpsExecutor, owner *taskmanager.Manager, admission orchestrator.NativeCodingAdmission) http.HandlerFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	handler, err := newAgentOpsHandler(cfg, executor, nil, owner, admission, func() conversation.AcceptedOPSInputStore {
		if admission == nil {
			return nil
		}
		return newAgentOpsAcceptedOPSInputStoreStub("ren")
	}())
	if err != nil {
		t.Fatalf("newAgentOpsHandler() error=%v", err)
	}
	return handler
}

func newAgentOpsTestHandlerWithNotifier(t *testing.T, token string, executor agentOpsExecutor, notifier agentOpsWorkerBusyNotifier) http.HandlerFunc {
	return newAgentOpsTestHandlerWithNotifierAndNativeAdmission(t, token, executor, notifier, nil)
}

func newAgentOpsTestHandlerWithNativeAdmission(t *testing.T, token string, executor agentOpsExecutor, admission orchestrator.NativeCodingAdmission) http.HandlerFunc {
	return newAgentOpsTestHandlerWithNotifierAndNativeAdmission(t, token, executor, nil, admission)
}

func newAgentOpsTestHandlerWithNotifierAndNativeAdmission(t *testing.T, token string, executor agentOpsExecutor, notifier agentOpsWorkerBusyNotifier, admission orchestrator.NativeCodingAdmission) http.HandlerFunc {
	t.Helper()
	deps := &Dependencies{}
	if err := initializeRuntimeTaskOwner(deps, t.TempDir()); err != nil {
		t.Fatalf("initialize Agent OPS task owner: %v", err)
	}
	t.Cleanup(func() {
		if err := deps.taskManager.Close(); err != nil {
			t.Errorf("close Agent OPS task owner: %v", err)
		}
	})
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	handler, err := newAgentOpsHandler(cfg, executor, notifier, deps.taskManager, admission, func() conversation.AcceptedOPSInputStore {
		if admission == nil {
			return nil
		}
		return newAgentOpsAcceptedOPSInputStoreStub("ren")
	}())
	if err != nil {
		t.Fatalf("newAgentOpsHandler() error=%v", err)
	}
	return handler
}

func setAgentOpsHeaders(req *http.Request, token, requestID string) {
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("X-RenCrow-Client", "RenCrow_CMD")
	req.Header.Set("X-RenCrow-Interaction-Profile", "agent-ops")
	req.Header.Set("Content-Type", "application/json")
	if requestID != "" {
		req.Header.Set("X-Request-ID", requestID)
	}
}
