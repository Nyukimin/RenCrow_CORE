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
	if executor.calls != 1 || executor.input.MessageText() != "状態を確認して" || executor.input.ChannelAddress().ChannelType() != "agent_ops" || executor.input.ChannelAddress().ExternalConversationID() != "agent-ops" || executor.input.Route() != routing.RouteOPS {
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
	handler := newAgentOpsTestHandlerWithNotifier(t, token, executor, notifier)
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
	handler, err := newConfiguredAgentOpsHandler(cfg, &agentOpsExecutorStub{output: "ok"}, nil)
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
			handler, err := newAgentOpsHandler(cfg, &agentOpsExecutorStub{output: "ok"}, nil)
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
		if _, err := newAgentOpsHandler(cfg, &agentOpsExecutorStub{}, nil); err == nil {
			t.Fatal("group/world-readable token file must be rejected")
		}
	}
}

func newAgentOpsTestHandler(t *testing.T, token string, executor agentOpsExecutor) http.HandlerFunc {
	return newAgentOpsTestHandlerWithNotifier(t, token, executor, nil)
}

func newAgentOpsTestHandlerWithNotifier(t *testing.T, token string, executor agentOpsExecutor, notifier agentOpsWorkerBusyNotifier) http.HandlerFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	handler, err := newAgentOpsHandler(cfg, executor, notifier)
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
