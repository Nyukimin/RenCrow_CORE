package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/Nyukimin/RenCrow_CORE/internal/adapter/config"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/subagent"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/taskmanager"
	"github.com/Nyukimin/RenCrow_CORE/internal/application/toolloop"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/agent"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/conversation"
	domainexecution "github.com/Nyukimin/RenCrow_CORE/internal/domain/execution"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/llm"
	domainsuperagent "github.com/Nyukimin/RenCrow_CORE/internal/domain/superagent"
	domaintask "github.com/Nyukimin/RenCrow_CORE/internal/domain/task"
	"github.com/Nyukimin/RenCrow_CORE/internal/domain/tool"
	modulecore "github.com/Nyukimin/RenCrow_CORE/modules/core"
)

// These tests cover the legacy (non-native) POST /v1/agent/ops branch. The
// production defect they pin: with the SuperAgent ledger configured on the
// Subagent Manager, a real Shiro failed every request with "superagent runtime
// context is required when recorder is configured" because the branch created
// no Task/Run and attached no runtime context, and the handler hid the error.

const legacyOPSTestToken = "0123456789abcdef0123456789abcdef"

// legacyOPSMessage is the request body text. No Task, ledger record, response
// error or log line may echo it.
const legacyOPSMessage = "legacy-ops-message-must-not-be-persisted"

type legacyOPSProvider struct {
	mu        sync.Mutex
	chatCalls int
}

func (p *legacyOPSProvider) Generate(context.Context, llm.GenerateRequest) (llm.GenerateResponse, error) {
	return llm.GenerateResponse{}, errors.New("generate is not used")
}

func (p *legacyOPSProvider) Name() string { return "legacy-ops-test" }

func (p *legacyOPSProvider) Chat(context.Context, llm.ChatRequest) (llm.ChatResponse, error) {
	p.mu.Lock()
	p.chatCalls++
	p.mu.Unlock()
	return llm.ChatResponse{Message: llm.ChatMessage{Role: "assistant", Content: "OK"}, FinishReason: "stop"}, nil
}

func (p *legacyOPSProvider) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.chatCalls
}

type legacyOPSToolRunner struct{}

func (legacyOPSToolRunner) ExecuteV2(context.Context, string, map[string]any) (*tool.ToolResponse, error) {
	return nil, errors.New("unexpected tool call")
}

func (legacyOPSToolRunner) ListTools(context.Context) ([]tool.ToolMetadata, error) { return nil, nil }

// legacyOPSLedger is an in-memory SuperAgent ledger. It satisfies both the
// Subagent Manager recorder and the lead-run recorder, as the production
// store does.
type legacyOPSLedger struct {
	mu        sync.Mutex
	runs      []domainsuperagent.AgentRun
	packs     []domainsuperagent.ContextPack
	subagents []domainsuperagent.SubagentTask
	events    []modulecore.EventEnvelope
	// failEvent makes Append fail for the named event type.
	failEvent string
}

func (l *legacyOPSLedger) SaveAgentRun(_ context.Context, item domainsuperagent.AgentRun) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.runs = append(l.runs, item)
	return nil
}

func (l *legacyOPSLedger) SaveContextPack(_ context.Context, item domainsuperagent.ContextPack) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.packs = append(l.packs, item)
	return nil
}

func (l *legacyOPSLedger) SaveSubagentTask(_ context.Context, item domainsuperagent.SubagentTask) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.subagents = append(l.subagents, item)
	return nil
}

func (l *legacyOPSLedger) Append(_ context.Context, event modulecore.EventEnvelope) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failEvent != "" && event.EventType == l.failEvent {
		return fmt.Errorf("ledger append refused for %s", event.EventType)
	}
	l.events = append(l.events, event)
	return nil
}

func (l *legacyOPSLedger) eventTypes() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	types := make([]string, 0, len(l.events))
	for _, event := range l.events {
		types = append(types, event.EventType)
	}
	return types
}

func (l *legacyOPSLedger) eventSnapshot() []modulecore.EventEnvelope {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]modulecore.EventEnvelope(nil), l.events...)
}

func (l *legacyOPSLedger) runSnapshot() []domainsuperagent.AgentRun {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]domainsuperagent.AgentRun(nil), l.runs...)
}

// newLegacyOPSRealShiro wires a real ShiroAgent and a real Subagent Manager
// the way production does. A nil ledger leaves the Manager without a recorder.
func newLegacyOPSRealShiro(t *testing.T, provider *legacyOPSProvider, ledger *legacyOPSLedger) *agent.ShiroAgent {
	t.Helper()
	// Production gives the Subagent loop an Action owner (cfg.Subagent.Enabled);
	// a loop bound to a Task/Run refuses to start without one.
	actions, err := newRuntimeActionManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	manager := subagent.NewManager(provider, legacyOPSToolRunner{}, nil, toolloop.Config{MaxIterations: 3, Actions: actions})
	if ledger != nil {
		manager.SetSuperAgentRecorder(ledger)
	}
	return agent.NewShiroAgent(provider, legacyOPSToolRunner{}, nil, "legacy-ops-system-prompt", manager)
}

func newLegacyOPSHandler(t *testing.T, owner *taskmanager.Manager, executor agentOpsExecutor, ledger *legacyOPSLedger) http.HandlerFunc {
	t.Helper()
	path := filepath.Join(t.TempDir(), "agent-ops.token")
	if err := os.WriteFile(path, []byte(legacyOPSTestToken), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{LocalAgentOps: config.LocalAgentOpsConfig{Enabled: true, AuthTokenFile: path, UserID: "ren"}}
	var options []agentOpsHandlerOption
	if ledger != nil {
		options = append(options, withAgentOpsLeadRunRecorder(ledger))
	}
	handler, err := newAgentOpsHandler(cfg, executor, nil, owner, nil, nil, options...)
	if err != nil {
		t.Fatalf("newAgentOpsHandler() error=%v", err)
	}
	return handler
}

func postLegacyOPS(handler http.HandlerFunc, requestID string) *httptest.ResponseRecorder {
	body, _ := json.Marshal(map[string]string{"message": legacyOPSMessage})
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1/v1/agent/ops", bytes.NewReader(body))
	setAgentOpsHeaders(req, legacyOPSTestToken, requestID)
	req.RemoteAddr = "127.0.0.1:18791"
	rec := httptest.NewRecorder()
	localOnlyHandler(handler).ServeHTTP(rec, req)
	return rec
}

func captureLegacyOPSLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previousWriter, previousFlags := log.Writer(), log.Flags()
	log.SetOutput(&buffer)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	})
	return &buffer
}

// legacyOPSOnlyTask returns the single OPS Task and its single Run. It fails
// the test when the Task owner holds any other shape.
func legacyOPSOnlyTask(t *testing.T, owner *taskmanager.Manager) (domaintask.Task, domaintask.Run) {
	t.Helper()
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 1 {
		t.Fatalf("Task owner holds %d Tasks, want exactly 1: %+v", len(tasks), tasks)
	}
	runs, err := owner.ListRuns(context.Background(), domaintask.RunFilter{TaskID: tasks[0].TaskID})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("Task %s holds %d Runs, want exactly 1", tasks[0].TaskID, len(runs))
	}
	return tasks[0], runs[0]
}

func assertLegacyOPSTaskShape(t *testing.T, task domaintask.Task) {
	t.Helper()
	if task.Route != domaintask.RouteOperations || task.Assignee != "shiro" || task.OwnerID != "shiro" {
		t.Fatalf("Task route/assignee/owner = %s/%s/%s", task.Route, task.Assignee, task.OwnerID)
	}
	if strings.Contains(task.Title, legacyOPSMessage) || strings.Contains(task.Summary, legacyOPSMessage) {
		t.Fatalf("Task persisted the request message: title=%q summary=%q", task.Title, task.Summary)
	}
}

func TestAgentOpsLegacyOPSRealShiroWithLedgerRecordsLeadAndSubagentOnTaskOwnerRun(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	ledger := &legacyOPSLedger{}
	provider := &legacyOPSProvider{}
	handler := newLegacyOPSHandler(t, owner, newLegacyOPSRealShiro(t, provider, ledger), ledger)

	rec := postLegacyOPS(handler, "req-legacy-ledger-ok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	var response agentOpsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	task, run := legacyOPSOnlyTask(t, owner)
	assertLegacyOPSTaskShape(t, task)
	if response.TaskID != task.TaskID.String() || response.Output != "OK" || response.AgentID != "shiro" || response.Route != "OPS" {
		t.Fatalf("response=%+v task=%s", response, task.TaskID)
	}
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("task=%s run=%s, want both succeeded", task.Status, run.Status)
	}
	if provider.calls() != 1 {
		t.Fatalf("provider chat calls=%d, want 1", provider.calls())
	}

	wantTypes := []string{"lead_agent.started", "subagent.started", "subagent.completed", "lead_agent.completed"}
	events := ledger.eventSnapshot()
	if got := ledger.eventTypes(); strings.Join(got, ",") != strings.Join(wantTypes, ",") {
		t.Fatalf("ledger event types=%v, want %v", got, wantTypes)
	}
	for _, event := range events {
		if event.TaskID != task.TaskID || event.RunID != run.RunID || event.TraceID != events[0].TraceID || event.ActorID != "shiro" {
			t.Fatalf("event %s is not bound to the Task owner Run: %+v", event.EventType, event)
		}
	}
	if events[1].CausationEventID != events[0].EventID {
		t.Fatalf("subagent.started causation=%q, want lead_agent.started %q", events[1].CausationEventID, events[0].EventID)
	}
	runs := ledger.runSnapshot()
	if len(runs) != 2 || runs[0].Status != "running" || runs[1].Status != "completed" {
		t.Fatalf("lead agent run records=%+v", runs)
	}
	for _, record := range runs {
		if record.TaskID != task.TaskID || record.RunID != run.RunID || record.ActorID != "shiro" {
			t.Fatalf("lead agent run record is not bound to the Task owner Run: %+v", record)
		}
		if strings.Contains(record.Goal, legacyOPSMessage) || record.Goal == "" {
			t.Fatalf("lead agent run goal must be a fixed non-empty statement, got %q", record.Goal)
		}
	}
	for _, pack := range ledger.packs {
		if strings.Contains(pack.Summary, legacyOPSMessage) {
			t.Fatalf("context pack persisted the request message: %q", pack.Summary)
		}
	}
}

func TestAgentOpsLegacyOPSRealShiroWithoutLedgerTerminatesTaskAndRun(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	handler := newLegacyOPSHandler(t, owner, newLegacyOPSRealShiro(t, &legacyOPSProvider{}, nil), nil)

	rec := postLegacyOPS(handler, "req-legacy-no-ledger")
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	task, run := legacyOPSOnlyTask(t, owner)
	assertLegacyOPSTaskShape(t, task)
	if task.Status != domaintask.StatusSucceeded || run.Status != domaintask.RunStatusSucceeded {
		t.Fatalf("task=%s run=%s, want both succeeded", task.Status, run.Status)
	}
	var response agentOpsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.TaskID != task.TaskID.String() {
		t.Fatalf("response task_id=%q want %s err=%v", response.TaskID, task.TaskID, err)
	}
}

func TestAgentOpsLegacyOPSExecutionFailureTerminatesFailedAndLogsWithoutMessage(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	ledger := &legacyOPSLedger{}
	executor := &agentOpsExecutorStub{err: errors.New("provider rejected the call")}
	handler := newLegacyOPSHandler(t, owner, executor, ledger)
	logs := captureLegacyOPSLog(t)

	rec := postLegacyOPS(handler, "req-legacy-exec-fail")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"execution_failed"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	task, run := legacyOPSOnlyTask(t, owner)
	if task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed {
		t.Fatalf("task=%s run=%s, want both failed", task.Status, run.Status)
	}
	logged := logs.String()
	for _, want := range []string{"[AgentOps] execution failed", "request_id=req-legacy-exec-fail", "task_id=" + task.TaskID.String(), "provider rejected the call"} {
		if !strings.Contains(logged, want) {
			t.Fatalf("log is missing %q: %q", want, logged)
		}
	}
	if strings.Contains(logged, legacyOPSMessage) || strings.Contains(rec.Body.String(), legacyOPSMessage) {
		t.Fatalf("request message leaked: log=%q body=%q", logged, rec.Body.String())
	}
	runs := ledger.runSnapshot()
	if len(runs) != 2 || runs[1].Status != "failed" {
		t.Fatalf("lead agent run records=%+v", runs)
	}
	if got := ledger.eventTypes(); strings.Join(got, ",") != "lead_agent.started,lead_agent.failed" {
		t.Fatalf("ledger event types=%v", got)
	}
}

func TestAgentOpsLegacyOPSBlankOutputIsAFailureWithTerminalTask(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	handler := newLegacyOPSHandler(t, owner, &agentOpsExecutorStub{output: " \n"}, nil)

	rec := postLegacyOPS(handler, "req-legacy-blank")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"execution_failed"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	task, run := legacyOPSOnlyTask(t, owner)
	if task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed {
		t.Fatalf("task=%s run=%s, want both failed", task.Status, run.Status)
	}
}

func TestAgentOpsLegacyOPSCanceledExecutionTerminatesCancelled(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	handler := newLegacyOPSHandler(t, owner, &agentOpsExecutorStub{err: context.Canceled}, nil)

	rec := postLegacyOPS(handler, "req-legacy-canceled")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	task, run := legacyOPSOnlyTask(t, owner)
	if task.Status != domaintask.StatusCancelled || run.Status != domaintask.RunStatusCancelled {
		t.Fatalf("task=%s run=%s, want both cancelled", task.Status, run.Status)
	}
}

func TestAgentOpsLegacyOPSParallelLimitReturns503AndLeavesNoQueuedTask(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	// One running OPS Task already holds the single operations slot.
	holder, err := owner.Create(context.Background(), domaintask.Task{Title: "slot holder", Route: domaintask.RouteOperations, Assignee: "shiro"}, domaintask.SharedRoleContext{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := owner.StartRunWithReason(context.Background(), holder.TaskID, domaintask.RunStartReasonFirst); err != nil {
		t.Fatal(err)
	}
	executor := &agentOpsExecutorStub{output: "must not run"}
	handler := newLegacyOPSHandler(t, owner, executor, &legacyOPSLedger{})

	rec := postLegacyOPS(handler, "req-legacy-limit")
	if rec.Code != http.StatusServiceUnavailable || strings.TrimSpace(rec.Body.String()) != `{"error":"runtime_unavailable"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor ran %d times although admission was refused", executor.calls)
	}
	tasks, err := owner.List(context.Background(), domaintask.Filter{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range tasks {
		if task.TaskID == holder.TaskID {
			continue
		}
		if task.Status != domaintask.StatusFailed {
			t.Fatalf("refused admission left Task %s in %s, want failed", task.TaskID, task.Status)
		}
		assertLegacyOPSTaskShape(t, task)
	}
	if len(tasks) != 2 {
		t.Fatalf("Task owner holds %d Tasks, want the holder and one failed admission", len(tasks))
	}
}

func TestAgentOpsLegacyOPSTerminalWriteFailureIsNotSwallowed(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	// The executor closes the Run behind the handler's back, so the handler's
	// own CompleteRun conflicts. The failure must surface instead of a 200.
	executor := legacyOPSExecutorFunc(func(ctx context.Context, _ conversation.TurnInput) (string, error) {
		identity, err := domainexecution.IdentityFromContext(ctx)
		if err != nil {
			return "", err
		}
		if _, err := owner.CompleteRun(context.WithoutCancel(ctx), identity.TaskID, identity.RunID, "shiro", domaintask.StatusCancelled, "closed by test", ""); err != nil {
			return "", err
		}
		return "done", nil
	})
	handler := newLegacyOPSHandler(t, owner, executor, nil)
	logs := captureLegacyOPSLog(t)

	rec := postLegacyOPS(handler, "req-legacy-terminal-conflict")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"runtime_unavailable"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if !strings.Contains(logs.String(), "[AgentOps]") || strings.Contains(logs.String(), legacyOPSMessage) {
		t.Fatalf("terminal write failure must be logged without the message: %q", logs.String())
	}
}

func TestAgentOpsLegacyOPSLeadFinishFailureStillTerminatesTaskAndRun(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	ledger := &legacyOPSLedger{failEvent: "lead_agent.completed"}
	handler := newLegacyOPSHandler(t, owner, &agentOpsExecutorStub{output: "done"}, ledger)

	rec := postLegacyOPS(handler, "req-legacy-lead-finish")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"runtime_unavailable"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	task, run := legacyOPSOnlyTask(t, owner)
	if task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed {
		t.Fatalf("task=%s run=%s, want both failed so no operations slot leaks", task.Status, run.Status)
	}
}

func TestAgentOpsLegacyOPSLeadStartFailureStopsBeforeExecutor(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	ledger := &legacyOPSLedger{failEvent: "lead_agent.started"}
	executor := &agentOpsExecutorStub{output: "must not run"}
	handler := newLegacyOPSHandler(t, owner, executor, ledger)

	rec := postLegacyOPS(handler, "req-legacy-lead-start")
	if rec.Code != http.StatusInternalServerError || strings.TrimSpace(rec.Body.String()) != `{"error":"runtime_unavailable"}` {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("executor ran %d times although the lead run was not recorded", executor.calls)
	}
	task, run := legacyOPSOnlyTask(t, owner)
	if task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed {
		t.Fatalf("task=%s run=%s, want both failed", task.Status, run.Status)
	}
}

func TestAgentOpsLegacyOPSPanickingExecutorStillClosesTheRun(t *testing.T) {
	owner := newAgentOpsTestTaskOwner(t)
	handler := newLegacyOPSHandler(t, owner, legacyOPSExecutorFunc(func(context.Context, conversation.TurnInput) (string, error) {
		panic("executor panic")
	}), nil)

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Fatal("the executor panic must propagate to the HTTP server's recovery")
			}
		}()
		postLegacyOPS(handler, "req-legacy-panic")
	}()
	task, run := legacyOPSOnlyTask(t, owner)
	if task.Status != domaintask.StatusFailed || run.Status != domaintask.RunStatusFailed {
		t.Fatalf("task=%s run=%s, want both failed so the operations slot is released", task.Status, run.Status)
	}
}

type legacyOPSExecutorFunc func(context.Context, conversation.TurnInput) (string, error)

func (f legacyOPSExecutorFunc) Execute(ctx context.Context, input conversation.TurnInput) (string, error) {
	return f(ctx, input)
}

// Enforcement of the Failure Knowledge invariant: an OPS ingress function that
// calls the Shiro executor must first bind the Task owner's Task/Run identity
// to the execution context. A function that calls Execute without doing so is
// the shape of the defect this file's tests were written for.
func TestAgentOpsIngressBindsTaskRunIdentityBeforeCallingExecutor(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	sources, err := filepath.Glob(filepath.Join(filepath.Dir(testFile), "runtime_agent_ops*.go"))
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string]bool{}
	for _, source := range sources {
		if strings.HasSuffix(source, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), source, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", source, err)
		}
		for _, declaration := range file.Decls {
			function, ok := declaration.(*ast.FuncDecl)
			if !ok || function.Body == nil {
				continue
			}
			callsExecutor, bindsIdentity := false, false
			ast.Inspect(function.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				if owner, ok := selector.X.(*ast.SelectorExpr); ok && selector.Sel.Name == "Execute" && owner.Sel.Name == "executor" {
					callsExecutor = true
				}
				if selector.Sel.Name == "WithIdentity" {
					bindsIdentity = true
				}
				return true
			})
			if callsExecutor {
				callers[function.Name.Name] = bindsIdentity
			}
		}
	}
	var names []string
	for name, binds := range callers {
		names = append(names, name)
		if !binds {
			t.Errorf("%s calls the Shiro executor without binding the Task owner Run identity (domainexecution.WithIdentity)", name)
		}
	}
	sort.Strings(names)
	if want := "executeLegacyOPS,serveAcceptedNativeOPS"; strings.Join(names, ",") != want {
		t.Fatalf("OPS ingress functions that call the executor = %v, want %s (update this guard when an ingress is added or removed)", names, want)
	}
}
